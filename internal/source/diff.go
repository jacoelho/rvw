package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/change"
)

type diffRecord struct {
	unmerged   *change.FileSemanticIdentity
	oldOID     *change.ObjectID
	newOID     *change.ObjectID
	oldMode    change.FileMode
	newMode    change.FileMode
	oldPath    string
	newPath    string
	additions  uint32
	deletions  uint32
	similarity uint16
	status     byte
	binary     bool
}

// diffRecordConstructionCharge covers the record, two heap-resident object-ID
// headers and SHA-256 string backings, and the two mode string backings.
const diffRecordConstructionCharge int64 = 320

func (service *Service) snapshot(
	ctx context.Context,
	runCPU CPUAdmission,
	sourceID change.SourceID,
	repository *repositorySession,
	records []diffRecord,
	options OpenOptions,
) (change.Snapshot, error) {
	var recordBytes int64
	if err := runCPU(ctx, func() error {
		recordBytes = sourceSnapshotRecordBytes(records) + options.RetainedBytes()
		return context.Cause(ctx)
	}); err != nil {
		return change.Snapshot{}, err
	}
	ledger, err := newSourceLedger(recordBytes)
	if err != nil {
		return change.Snapshot{}, err
	}
	projectionBytes := int64(len(records)) * 4
	if reserveErr := ledger.reserve(projectionBytes); reserveErr != nil {
		return change.Snapshot{}, reserveErr
	}
	var rules []ignoreRule
	if options.Gitignore && len(records) != 0 {
		if repository == nil {
			return change.Snapshot{}, errors.New("native repository is required for ignore evaluation")
		}
		rules, err = nativeGitignoreRules(ctx, repository, records)
		if err != nil {
			return change.Snapshot{}, err
		}
	}
	if options.RVWIgnore && options.IgnoreFile != "" {
		contents, readErr := readIgnoreRuleContents(repository, options.IgnoreFile)
		if readErr != nil {
			return change.Snapshot{}, readErr
		}
		if err := runCPU(ctx, func() error {
			var rvwRules []ignoreRule
			rvwRules, readErr = parseIgnoreRules(ctx, options.IgnoreFile, contents)
			rules = append(rules, rvwRules...)
			return readErr
		}); err != nil {
			return change.Snapshot{}, err
		}
	}
	var filters filterProjection
	if err := runCPU(ctx, func() error {
		var filterErr error
		filters, filterErr = buildFilterProjection(
			ctx,
			options,
			records,
			rules,
			ledger,
		)
		return filterErr
	}); err != nil {
		return change.Snapshot{}, err
	}
	var snapshot change.Snapshot
	if err := runCPU(ctx, func() error {
		var buildErr error
		snapshot, buildErr = buildSnapshot(ctx, sourceID, records, filters, ledger)
		return buildErr
	}); err != nil {
		return change.Snapshot{}, err
	}
	return snapshot, nil
}

func buildSnapshot(
	ctx context.Context,
	sourceID change.SourceID,
	records []diffRecord,
	filters filterProjection,
	ledger *sourceLedger,
) (change.Snapshot, error) {
	if len(filters.flags) != len(records) {
		return change.Snapshot{}, errors.New("snapshot filter projection is incomplete")
	}
	builderBytes := change.SnapshotBuilderConstructionBytes(len(records))
	if reserveErr := ledger.reserve(builderBytes); reserveErr != nil {
		return change.Snapshot{}, reserveErr
	}
	builder, err := change.NewSnapshotBuilder(sourceID, len(records))
	if err != nil {
		return change.Snapshot{}, err
	}

	for index, record := range records {
		if index%filterCancellationRecords == 0 {
			if err := context.Cause(ctx); err != nil {
				return change.Snapshot{}, err
			}
		}
		oldPathValue, hasOldPath, err := reviewPath(record.oldPath)
		if err != nil {
			return change.Snapshot{}, fmt.Errorf("file %d old path: %w", index, err)
		}
		newPathValue, hasNewPath, err := reviewPath(record.newPath)
		if err != nil {
			return change.Snapshot{}, fmt.Errorf("file %d new path: %w", index, err)
		}
		var oldPath, newPath *change.Path
		if hasOldPath {
			oldPath = new(oldPathValue)
		}
		if hasNewPath {
			newPath = new(newPathValue)
		}
		fileChange, err := changeFromDiff(record)
		if err != nil {
			return change.Snapshot{}, fmt.Errorf("file %d change: %w", index, err)
		}
		flags := filters.flags[index]
		var contentValues []change.FileContent
		if record.oldOID != nil && objectKind(record.oldMode) == change.ObjectBlob {
			contentValues = append(contentValues, change.OldFileContent(*record.oldOID))
		}
		if record.newOID != nil && objectKind(record.newMode) == change.ObjectBlob {
			contentValues = append(contentValues, change.NewFileContent(*record.newOID))
		}
		contents, err := change.NewFileContents(contentValues...)
		if record.unmerged != nil {
			contents, err = change.NewFileContentsWithSemanticIdentity(
				*record.unmerged,
				contentValues...,
			)
		}
		if err != nil {
			return change.Snapshot{}, fmt.Errorf("file %d content identities: %w", index, err)
		}
		if _, err := builder.AddFileWithContents(
			oldPath,
			newPath,
			fileChange,
			flags,
			contents,
		); err != nil {
			return change.Snapshot{}, fmt.Errorf("file %d: %w", index, err)
		}
	}
	return builder.FinishCancellable(func() error { return context.Cause(ctx) })
}

func sourceSnapshotRecordBytes(records []diffRecord) int64 {
	retained := int64(cap(records)) * diffRecordConstructionCharge
	for _, record := range records {
		retained += int64(len(record.oldPath))
		if record.newPath != record.oldPath {
			retained += int64(len(record.newPath))
		}
	}
	return retained
}

func changeFromDiff(record diffRecord) (change.Change, error) {
	mode := optionalMode(record.oldMode, record.newMode)
	oldKind := objectKind(record.oldMode)
	newKind := objectKind(record.newMode)
	if oldKind != newKind && record.oldOID != nil && record.newOID != nil {
		oldObject, err := change.NewGitObject(oldKind, *record.oldOID, record.oldMode)
		if err != nil {
			return change.Change{}, err
		}
		newObject, err := change.NewGitObject(newKind, *record.newOID, record.newMode)
		if err != nil {
			return change.Change{}, err
		}
		return change.NewObjectTypeChange(record.additions, record.deletions, 0, oldObject, newObject)
	}
	if oldKind == change.ObjectGitlink || newKind == change.ObjectGitlink {
		return change.NewSubmoduleChange(record.oldOID, record.newOID)
	}
	if record.binary {
		return change.NewSimpleChange(change.ChangeBinary, mode)
	}
	if record.status == 'R' && record.additions == 0 && record.deletions == 0 {
		return change.NewSimpleChange(change.ChangeRenameOnly, mode)
	}
	if record.status == 'M' && record.additions == 0 && record.deletions == 0 && mode != nil {
		return change.NewSimpleChange(change.ChangeModeOnly, mode)
	}
	kind, err := pathKind(record.status)
	if err != nil {
		return change.Change{}, err
	}
	return change.NewUnclassifiedChange(kind, record.additions, record.deletions)
}

func pathKind(status byte) (change.ChangeKind, error) {
	switch status {
	case 'A':
		return change.ChangeAdded, nil
	case 'D':
		return change.ChangeDeleted, nil
	case 'R':
		return change.ChangeRenamed, nil
	case 'C':
		return change.ChangeCopied, nil
	case 'M', 'T', 'U':
		return change.ChangeModified, nil
	default:
		return "", fmt.Errorf("unsupported Git status %q", status)
	}
}

func reviewPath(raw string) (change.Path, bool, error) {
	if raw == "" {
		return change.Path{}, false, nil
	}
	path, err := change.NewPathString(raw)
	if err != nil {
		return change.Path{}, false, err
	}
	return path, true, nil
}

func optionalMode(oldMode, newMode change.FileMode) *change.Mode {
	if oldMode == newMode {
		return nil
	}
	mode, err := change.NewMode(oldMode.String(), newMode.String())
	if err != nil {
		return nil
	}
	return new(mode)
}

func objectKind(mode change.FileMode) change.ObjectKind {
	if mode.String() == "160000" {
		return change.ObjectGitlink
	}
	return change.ObjectBlob
}

func validateRawPath(path []byte) error {
	switch {
	case len(path) == 0:
		return errors.New("git path is empty")
	case int64(len(path)) > change.MaxPathBytes:
		return fmt.Errorf("git path exceeds %d bytes", change.MaxPathBytes)
	case bytes.IndexByte(path, 0) >= 0:
		return errors.New("git path contains NUL")
	default:
		return nil
	}
}
