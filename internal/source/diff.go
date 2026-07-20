package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jacoelho/rvw/internal/review"
)

type diffRecord struct {
	oldMode   review.FileMode
	newMode   review.FileMode
	oldOID    *review.ObjectID
	newOID    *review.ObjectID
	status    byte
	oldPath   []byte
	newPath   []byte
	additions uint32
	deletions uint32
	binary    bool
}

type numstatRecord struct {
	oldPath   []byte
	newPath   []byte
	additions uint32
	deletions uint32
	binary    bool
}

func (service *Service) diffMetadata(
	ctx context.Context,
	repository RepositoryPath,
	environment map[string]string,
	base, head review.ObjectID,
) ([]diffRecord, error) {
	if err := service.checkRenameCandidates(ctx, repository, environment, base, head); err != nil {
		return nil, err
	}
	common := []string{
		"diff-tree",
		"--no-commit-id",
		"-r",
		"-z",
		"--no-ext-diff",
		"--no-textconv",
		"--find-renames=50%",
		"--find-copies=50%",
		"-l1000",
		base.String(),
		head.String(),
	}
	rawArguments := append([]string{}, common...)
	rawArguments = append(rawArguments[:8], append([]string{"--raw"}, rawArguments[8:]...)...)
	numstatArguments := append([]string{}, common...)
	numstatArguments = append(numstatArguments[:8], append([]string{"--numstat"}, numstatArguments[8:]...)...)
	return service.readDiffMetadata(ctx, repository, environment, rawArguments, numstatArguments)
}

func (service *Service) diffIndexMetadata(
	ctx context.Context,
	repository RepositoryPath,
	environment map[string]string,
	base review.ObjectID,
) ([]diffRecord, error) {
	if err := service.checkIndexRenameCandidates(ctx, repository, environment, base); err != nil {
		return nil, err
	}
	common := []string{
		"diff",
		"--cached",
		"--no-abbrev",
		"-z",
		"--no-ext-diff",
		"--no-textconv",
		"--find-renames=50%",
		"--find-copies=50%",
		"-l1000",
		base.String(),
	}
	rawArguments := append([]string{"diff", "--cached", "--raw"}, common[2:]...)
	numstatArguments := append([]string{"diff", "--cached", "--numstat"}, common[2:]...)
	return service.readDiffMetadata(ctx, repository, environment, rawArguments, numstatArguments)
}

func (service *Service) readDiffMetadata(
	ctx context.Context,
	repository RepositoryPath,
	environment map[string]string,
	rawArguments, numstatArguments []string,
) ([]diffRecord, error) {
	rawResult, err := service.runGit(
		ctx,
		repository.String(),
		environment,
		rawArguments,
		nil,
		64<<20,
	)
	if err != nil {
		return nil, fmt.Errorf("read Git path metadata: %w", err)
	}
	raw, err := parseRawDiff(rawResult.Stdout)
	if err != nil {
		return nil, err
	}

	numstatResult, err := service.runGit(
		ctx,
		repository.String(),
		environment,
		numstatArguments,
		nil,
		64<<20,
	)
	if err != nil {
		return nil, fmt.Errorf("read Git change counts: %w", err)
	}
	numstat, err := parseNumstat(numstatResult.Stdout)
	if err != nil {
		return nil, err
	}
	if len(raw) != len(numstat) {
		return nil, fmt.Errorf("Git metadata streams disagree: raw=%d numstat=%d", len(raw), len(numstat))
	}
	for index := range raw {
		if !matchingNumstatPath(raw[index], numstat[index]) {
			return nil, fmt.Errorf("Git metadata streams disagree at file %d", index)
		}
		raw[index].additions = numstat[index].additions
		raw[index].deletions = numstat[index].deletions
		raw[index].binary = numstat[index].binary
	}
	return raw, nil
}

func (service *Service) checkRenameCandidates(
	ctx context.Context,
	repository RepositoryPath,
	environment map[string]string,
	base, head review.ObjectID,
) error {
	result, err := service.runGit(
		ctx,
		repository.String(),
		environment,
		[]string{
			"diff-tree", "--no-commit-id", "-r", "-z", "--raw",
			"--no-renames", "--no-ext-diff", "--no-textconv",
			base.String(), head.String(),
		},
		nil,
		64<<20,
	)
	if err != nil {
		return fmt.Errorf("count Git rename candidates: %w", err)
	}
	records, err := parseRawDiff(result.Stdout)
	if err != nil {
		return err
	}
	var candidates uint32
	for _, record := range records {
		if record.status == 'A' || record.status == 'D' {
			candidates++
			if candidates > 1_000 {
				return &LimitError{Resource: LimitRenameCandidates, Limit: 1_000}
			}
		}
	}
	return nil
}

func (service *Service) checkIndexRenameCandidates(
	ctx context.Context,
	repository RepositoryPath,
	environment map[string]string,
	base review.ObjectID,
) error {
	result, err := service.runGit(
		ctx,
		repository.String(),
		environment,
		[]string{
			"diff", "--cached", "--raw", "--no-abbrev", "-z",
			"--no-renames", "--no-ext-diff", "--no-textconv",
			base.String(),
		},
		nil,
		64<<20,
	)
	if err != nil {
		return fmt.Errorf("count staged Git rename candidates: %w", err)
	}
	return enforceRenameCandidateLimit(result.Stdout)
}

func enforceRenameCandidateLimit(value []byte) error {
	records, err := parseRawDiff(value)
	if err != nil {
		return err
	}
	var candidates uint32
	for _, record := range records {
		if record.status == 'A' || record.status == 'D' {
			candidates++
			if candidates > 1_000 {
				return &LimitError{Resource: LimitRenameCandidates, Limit: 1_000}
			}
		}
	}
	return nil
}

func parseRawDiff(value []byte) ([]diffRecord, error) {
	var records []diffRecord
	for len(value) > 0 {
		header, remaining, ok := nextNULTerminated(value)
		if !ok {
			return nil, errors.New("Git raw diff has an unterminated header")
		}
		value = remaining
		fields := bytes.Fields(header)
		if len(fields) != 5 || len(fields[0]) != 7 || fields[0][0] != ':' {
			return nil, fmt.Errorf("invalid Git raw diff header %q", boundedText(string(header), 256))
		}
		oldMode, err := review.NewFileMode(string(fields[0][1:]))
		if err != nil {
			return nil, err
		}
		newMode, err := review.NewFileMode(string(fields[1]))
		if err != nil {
			return nil, err
		}
		if len(fields[2]) != len(fields[3]) {
			return nil, errors.New("Git raw diff mixes object ID formats")
		}
		oldOID, err := optionalRawObjectID(fields[2])
		if err != nil {
			return nil, err
		}
		newOID, err := optionalRawObjectID(fields[3])
		if err != nil {
			return nil, err
		}
		if !validRawStatus(fields[4]) {
			return nil, fmt.Errorf("unsupported Git path status %q", fields[4])
		}
		status := fields[4][0]
		path, remaining, ok := nextNULTerminated(value)
		if !ok {
			return nil, errors.New("Git raw diff has an unterminated path")
		}
		value = remaining
		if err := validateRawPath(path); err != nil {
			return nil, err
		}

		record := diffRecord{
			oldMode: oldMode,
			newMode: newMode,
			oldOID:  oldOID,
			newOID:  newOID,
			status:  status,
		}
		switch status {
		case 'A':
			record.newPath = bytes.Clone(path)
		case 'D':
			record.oldPath = bytes.Clone(path)
		case 'R', 'C':
			newPath, rest, ok := nextNULTerminated(value)
			if !ok {
				return nil, errors.New("Git raw rename/copy has an unterminated new path")
			}
			value = rest
			if err := validateRawPath(newPath); err != nil {
				return nil, err
			}
			record.oldPath = bytes.Clone(path)
			record.newPath = bytes.Clone(newPath)
		default:
			record.oldPath = bytes.Clone(path)
			record.newPath = bytes.Clone(path)
		}
		records = append(records, record)
		if len(records) > review.MaxFiles {
			return nil, fmt.Errorf("changed files exceed %d", review.MaxFiles)
		}
	}
	return records, nil
}

func validRawStatus(value []byte) bool {
	if len(value) == 1 {
		return strings.ContainsRune("ADMTU", rune(value[0]))
	}
	if len(value) < 2 || value[0] != 'R' && value[0] != 'C' {
		return false
	}
	score, err := strconv.ParseUint(string(value[1:]), 10, 8)
	return err == nil && score <= 100
}

func parseNumstat(value []byte) ([]numstatRecord, error) {
	var records []numstatRecord
	for len(value) > 0 {
		token, remaining, ok := nextNULTerminated(value)
		if !ok {
			return nil, errors.New("Git numstat has an unterminated record")
		}
		value = remaining
		firstTab := bytes.IndexByte(token, '\t')
		secondTab := -1
		if firstTab >= 0 {
			offset := bytes.IndexByte(token[firstTab+1:], '\t')
			if offset >= 0 {
				secondTab = firstTab + 1 + offset
			}
		}
		if firstTab <= 0 || secondTab <= firstTab+1 {
			return nil, fmt.Errorf("invalid Git numstat record %q", boundedText(string(token), 256))
		}
		additions, binaryA, err := parseCount(token[:firstTab], review.MaxAdditions)
		if err != nil {
			return nil, err
		}
		deletions, binaryD, err := parseCount(token[firstTab+1:secondTab], review.MaxDeletions)
		if err != nil {
			return nil, err
		}
		if binaryA != binaryD {
			return nil, errors.New("Git numstat has inconsistent binary counts")
		}

		record := numstatRecord{additions: additions, deletions: deletions, binary: binaryA}
		path := token[secondTab+1:]
		if len(path) == 0 {
			oldPath, rest, ok := nextNULTerminated(value)
			if !ok {
				return nil, errors.New("Git numstat rename has an unterminated old path")
			}
			newPath, rest, ok := nextNULTerminated(rest)
			if !ok {
				return nil, errors.New("Git numstat rename has an unterminated new path")
			}
			value = rest
			if err := validateRawPath(oldPath); err != nil {
				return nil, err
			}
			if err := validateRawPath(newPath); err != nil {
				return nil, err
			}
			record.oldPath = bytes.Clone(oldPath)
			record.newPath = bytes.Clone(newPath)
		} else {
			if err := validateRawPath(path); err != nil {
				return nil, err
			}
			record.oldPath = bytes.Clone(path)
			record.newPath = bytes.Clone(path)
		}
		records = append(records, record)
		if len(records) > review.MaxFiles {
			return nil, fmt.Errorf("changed files exceed %d", review.MaxFiles)
		}
	}
	return records, nil
}

func (service *Service) snapshot(
	ctx context.Context,
	sourceID review.SourceID,
	repository RepositoryPath,
	environment map[string]string,
	records []diffRecord,
	options OpenOptions,
) (review.Snapshot, error) {
	builder, err := review.NewSnapshotBuilder(sourceID, len(records))
	if err != nil {
		return review.Snapshot{}, err
	}
	gitignore := skipGitignore
	if options.Gitignore {
		gitignore = useGitignore
	}
	ignored, err := service.gitignored(ctx, repository, environment, records, gitignore)
	if err != nil {
		return review.Snapshot{}, err
	}
	filters, err := newFilterSet(repository, options)
	if err != nil {
		return review.Snapshot{}, err
	}

	for index, record := range records {
		oldPath, err := optionalReviewPath(record.oldPath)
		if err != nil {
			return review.Snapshot{}, fmt.Errorf("file %d old path: %w", index, err)
		}
		newPath, err := optionalReviewPath(record.newPath)
		if err != nil {
			return review.Snapshot{}, fmt.Errorf("file %d new path: %w", index, err)
		}
		change, err := changeFromDiff(record)
		if err != nil {
			return review.Snapshot{}, fmt.Errorf("file %d change: %w", index, err)
		}
		match := notGitignored
		if ignored[index] {
			match = gitignored
		}
		flags, err := filters.flagsFor(record, match)
		if err != nil {
			return review.Snapshot{}, fmt.Errorf("file %d flags: %w", index, err)
		}
		var contentValues []review.FileContent
		if record.oldOID != nil && objectKind(record.oldMode) == review.ObjectBlob {
			contentValues = append(contentValues, review.OldFileContent(*record.oldOID))
		}
		if record.newOID != nil && objectKind(record.newMode) == review.ObjectBlob {
			contentValues = append(contentValues, review.NewFileContent(*record.newOID))
		}
		contents, err := review.NewFileContents(contentValues...)
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
	return builder.Finish()
}

func matchingNumstatPath(raw diffRecord, numstat numstatRecord) bool {
	switch raw.status {
	case 'A':
		return bytes.Equal(raw.newPath, numstat.newPath)
	case 'D':
		return bytes.Equal(raw.oldPath, numstat.oldPath)
	default:
		return bytes.Equal(raw.oldPath, numstat.oldPath) && bytes.Equal(raw.newPath, numstat.newPath)
	}
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

func optionalReviewPath(raw []byte) (*review.Path, error) {
	if raw == nil {
		return nil, nil
	}
	path, err := review.NewPath(raw)
	if err != nil {
		return nil, err
	}
	return new(path), nil
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

func optionalRawObjectID(raw []byte) (*review.ObjectID, error) {
	if (len(raw) == 40 || len(raw) == 64) &&
		bytes.Count(raw, []byte{'0'}) == len(raw) {
		return nil, nil
	}
	id, err := review.NewObjectID(string(raw))
	if err != nil {
		return nil, err
	}
	return new(id), nil
}

func parseCount(value []byte, maximum int) (uint32, bool, error) {
	if bytes.Equal(value, []byte("-")) {
		return 0, true, nil
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, false, fmt.Errorf("invalid Git count %q", value)
		}
	}
	count, err := strconv.ParseUint(string(value), 10, 32)
	if err != nil || count > uint64(maximum) {
		return 0, false, fmt.Errorf("Git count %q exceeds %d", value, maximum)
	}
	return uint32(count), false, nil
}

func nextNULTerminated(value []byte) ([]byte, []byte, bool) {
	index := bytes.IndexByte(value, 0)
	if index < 0 {
		return nil, value, false
	}
	return value[:index], value[index+1:], true
}

func validateRawPath(path []byte) error {
	switch {
	case len(path) == 0:
		return errors.New("Git path is empty")
	case int64(len(path)) > review.MaxPathBytes:
		return fmt.Errorf("Git path exceeds %d bytes", review.MaxPathBytes)
	case bytes.IndexByte(path, 0) >= 0:
		return errors.New("Git path contains NUL")
	default:
		return nil
	}
}
