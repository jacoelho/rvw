package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/review"
)

type diffRecord struct {
	unmerged   *review.FileSemanticIdentity
	oldOID     *review.ObjectID
	newOID     *review.ObjectID
	oldMode    review.FileMode
	newMode    review.FileMode
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
	sourceID review.SourceID,
	repository *repositorySession,
	records []diffRecord,
	options OpenOptions,
) (review.Snapshot, error) {
	var recordBytes int64
	if err := runCPU(ctx, func() error {
		recordBytes = sourceSnapshotRecordBytes(records) + options.RetainedBytes()
		return context.Cause(ctx)
	}); err != nil {
		return review.Snapshot{}, err
	}
	ledger, err := newSourceLedger(recordBytes)
	if err != nil {
		return review.Snapshot{}, err
	}
	projectionBytes := int64(len(records)) * 4
	if reserveErr := ledger.reserve(projectionBytes); reserveErr != nil {
		return review.Snapshot{}, reserveErr
	}
	var rules []ignoreRule
	if options.Gitignore && len(records) != 0 {
		if repository == nil {
			return review.Snapshot{}, errors.New("native repository is required for ignore evaluation")
		}
		rules, err = nativeGitignoreRules(ctx, repository, records)
		if err != nil {
			return review.Snapshot{}, err
		}
	}
	if options.RVWIgnore && options.IgnoreFile != "" {
		contents, readErr := readIgnoreRuleContents(repository, options.IgnoreFile)
		if readErr != nil {
			return review.Snapshot{}, readErr
		}
		if err := runCPU(ctx, func() error {
			var rvwRules []ignoreRule
			rvwRules, readErr = parseIgnoreRules(ctx, options.IgnoreFile, contents)
			rules = append(rules, rvwRules...)
			return readErr
		}); err != nil {
			return review.Snapshot{}, err
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
		return review.Snapshot{}, err
	}
	var snapshot review.Snapshot
	if err := runCPU(ctx, func() error {
		var buildErr error
		snapshot, buildErr = buildSnapshot(ctx, sourceID, records, filters, ledger)
		return buildErr
	}); err != nil {
		return review.Snapshot{}, err
	}
	return snapshot, nil
}

func buildSnapshot(
	ctx context.Context,
	sourceID review.SourceID,
	records []diffRecord,
	filters filterProjection,
	ledger *sourceLedger,
) (review.Snapshot, error) {
	if len(filters.flags) != len(records) {
		return review.Snapshot{}, errors.New("snapshot filter projection is incomplete")
	}
	builderBytes := review.SnapshotBuilderConstructionBytes(len(records))
	if reserveErr := ledger.reserve(builderBytes); reserveErr != nil {
		return review.Snapshot{}, reserveErr
	}
	builder, err := review.NewSnapshotBuilder(sourceID, len(records))
	if err != nil {
		return review.Snapshot{}, err
	}

	for index, record := range records {
		if index%filterCancellationRecords == 0 {
			if err := context.Cause(ctx); err != nil {
				return review.Snapshot{}, err
			}
		}
		oldPathValue, hasOldPath, err := reviewPath(record.oldPath)
		if err != nil {
			return review.Snapshot{}, fmt.Errorf("file %d old path: %w", index, err)
		}
		newPathValue, hasNewPath, err := reviewPath(record.newPath)
		if err != nil {
			return review.Snapshot{}, fmt.Errorf("file %d new path: %w", index, err)
		}
		var oldPath, newPath *review.Path
		if hasOldPath {
			oldPath = new(oldPathValue)
		}
		if hasNewPath {
			newPath = new(newPathValue)
		}
		change, err := changeFromDiff(record)
		if err != nil {
			return review.Snapshot{}, fmt.Errorf("file %d change: %w", index, err)
		}
		flags := filters.flags[index]
		var contentValues []review.FileContent
		if record.oldOID != nil && objectKind(record.oldMode) == review.ObjectBlob {
			contentValues = append(contentValues, review.OldFileContent(*record.oldOID))
		}
		if record.newOID != nil && objectKind(record.newMode) == review.ObjectBlob {
			contentValues = append(contentValues, review.NewFileContent(*record.newOID))
		}
		contents, err := review.NewFileContents(contentValues...)
		if record.unmerged != nil {
			contents, err = review.NewFileContentsWithSemanticIdentity(
				*record.unmerged,
				contentValues...,
			)
		}
		if err != nil {
			return review.Snapshot{}, fmt.Errorf("file %d content identities: %w", index, err)
		}
		if _, err := builder.AddFileWithContents(
			oldPath,
			newPath,
			change,
			flags,
			contents,
		); err != nil {
			return review.Snapshot{}, fmt.Errorf("file %d: %w", index, err)
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

func changeFromDiff(record diffRecord) (review.Change, error) {
	mode := optionalMode(record.oldMode, record.newMode)
	oldKind := objectKind(record.oldMode)
	newKind := objectKind(record.newMode)
	if oldKind != newKind && record.oldOID != nil && record.newOID != nil {
		oldObject, err := review.NewGitObject(oldKind, *record.oldOID, record.oldMode)
		if err != nil {
			return review.Change{}, err
		}
		newObject, err := review.NewGitObject(newKind, *record.newOID, record.newMode)
		if err != nil {
			return review.Change{}, err
		}
		return review.NewObjectTypeChange(record.additions, record.deletions, 0, oldObject, newObject)
	}
	if oldKind == review.ObjectGitlink || newKind == review.ObjectGitlink {
		return review.NewSubmoduleChange(record.oldOID, record.newOID)
	}
	if record.binary {
		return review.NewSimpleChange(review.ChangeBinary, mode)
	}
	if record.status == 'R' && record.additions == 0 && record.deletions == 0 {
		return review.NewSimpleChange(review.ChangeRenameOnly, mode)
	}
	if record.status == 'M' && record.additions == 0 && record.deletions == 0 && mode != nil {
		return review.NewSimpleChange(review.ChangeModeOnly, mode)
	}
	kind, err := pathKind(record.status)
	if err != nil {
		return review.Change{}, err
	}
	return review.NewUnclassifiedChange(kind, record.additions, record.deletions)
}

func pathKind(status byte) (review.ChangeKind, error) {
	switch status {
	case 'A':
		return review.ChangeAdded, nil
	case 'D':
		return review.ChangeDeleted, nil
	case 'R':
		return review.ChangeRenamed, nil
	case 'C':
		return review.ChangeCopied, nil
	case 'M', 'T', 'U':
		return review.ChangeModified, nil
	default:
		return "", fmt.Errorf("unsupported Git status %q", status)
	}
}

func reviewPath(raw string) (review.Path, bool, error) {
	if raw == "" {
		return review.Path{}, false, nil
	}
	path, err := review.NewPathString(raw)
	if err != nil {
		return review.Path{}, false, err
	}
	return path, true, nil
}

func optionalMode(oldMode, newMode review.FileMode) *review.Mode {
	if oldMode == newMode {
		return nil
	}
	mode, err := review.NewMode(oldMode.String(), newMode.String())
	if err != nil {
		return nil
	}
	return new(mode)
}

func objectKind(mode review.FileMode) review.ObjectKind {
	if mode.String() == "160000" {
		return review.ObjectGitlink
	}
	return review.ObjectBlob
}

func validateRawPath(path []byte) error {
	switch {
	case len(path) == 0:
		return errors.New("git path is empty")
	case int64(len(path)) > review.MaxPathBytes:
		return fmt.Errorf("git path exceeds %d bytes", review.MaxPathBytes)
	case bytes.IndexByte(path, 0) >= 0:
		return errors.New("git path contains NUL")
	default:
		return nil
	}
}
