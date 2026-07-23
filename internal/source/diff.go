// Package source acquires and materializes Git review sources.
package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jacoelho/rvw/internal/review"
)

type diffRecord struct {
	oldOID    *review.ObjectID
	newOID    *review.ObjectID
	oldMode   review.FileMode
	newMode   review.FileMode
	oldPath   string
	newPath   string
	additions uint32
	deletions uint32
	status    byte
	binary    bool
}

// diffRecordConstructionCharge covers the record, two heap-resident object-ID
// headers and SHA-256 string backings, and the two mode string backings.
const diffRecordConstructionCharge int64 = 320

type diffRecordBuilder struct {
	ledger      *sourceLedger
	records     []diffRecord
	pathBytes   int64
	pathLimit   int64
	recordLimit int
}

func (builder *diffRecordBuilder) ensureCapacity() error {
	if len(builder.records) == cap(builder.records) {
		capacity := max(16, cap(builder.records)*2)
		capacity = min(capacity, builder.recordLimit)
		allocation := int64(capacity) * diffRecordConstructionCharge
		if err := builder.ledger.reserve(allocation); err != nil {
			return err
		}
		grown := make([]diffRecord, len(builder.records), capacity)
		copy(grown, builder.records)
		builder.ledger.release(int64(cap(builder.records)) * diffRecordConstructionCharge)
		builder.records = grown
	}
	return nil
}

func (builder *diffRecordBuilder) reservePath(pathBytes int) error {
	if int64(pathBytes) > builder.pathLimit-builder.pathBytes {
		return fmt.Errorf("source path bytes exceed %d", builder.pathLimit)
	}
	if err := builder.ledger.reserve(int64(pathBytes)); err != nil {
		return err
	}
	builder.pathBytes += int64(pathBytes)
	return nil
}

func (builder *diffRecordBuilder) add(record diffRecord) {
	builder.records = append(builder.records, record)
}

func (service *Service) diffMetadata(
	ctx context.Context,
	repository *rootedRepository,
	environment map[string]string,
	base, head review.ObjectID,
	runCPU CPUAdmission,
) ([]diffRecord, error) {
	if err := service.checkRenameCandidates(ctx, repository, environment, base, head, runCPU); err != nil {
		return nil, err
	}
	common := []string{
		"diff-tree",
		"--no-commit-id",
		"-r",
		"-z",
		gitNoExtDiff,
		gitNoTextconv,
		"--find-renames=50%",
		"--find-copies=50%",
		"-l1000",
		base.String(),
		head.String(),
	}
	rawArguments := append([]string{}, common...)
	rawArguments = append(rawArguments[:8], append([]string{gitRaw}, rawArguments[8:]...)...)
	numstatArguments := append([]string{}, common...)
	numstatArguments = append(numstatArguments[:8], append([]string{"--numstat"}, numstatArguments[8:]...)...)
	return service.readDiffMetadata(ctx, repository, environment, rawArguments, numstatArguments, runCPU)
}

func (service *Service) diffIndexMetadata(
	ctx context.Context,
	repository *rootedRepository,
	environment map[string]string,
	base review.ObjectID,
	runCPU CPUAdmission,
) ([]diffRecord, error) {
	if err := service.checkIndexRenameCandidates(ctx, repository, environment, base, runCPU); err != nil {
		return nil, err
	}
	common := []string{
		gitDiff,
		gitCached,
		gitNoAbbrev,
		"-z",
		gitNoExtDiff,
		gitNoTextconv,
		"--find-renames=50%",
		"--find-copies=50%",
		"-l1000",
		base.String(),
	}
	rawArguments := append([]string{gitDiff, gitCached, gitRaw}, common[2:]...)
	numstatArguments := append([]string{gitDiff, gitCached, "--numstat"}, common[2:]...)
	return service.readDiffMetadata(ctx, repository, environment, rawArguments, numstatArguments, runCPU)
}

func (service *Service) readDiffMetadata(
	ctx context.Context,
	repository *rootedRepository,
	environment map[string]string,
	rawArguments, numstatArguments []string,
	runCPU CPUAdmission,
) ([]diffRecord, error) {
	rawSpool, err := service.runGitToSpoolIn(
		ctx,
		repository.gitDirectory(),
		environment,
		rawArguments,
		rawDiffWireLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read Git path metadata: %w", err)
	}
	ledger, err := newSourceLedger(MaxCommandErrorConstructionBytes + maxSourceProtocolToken)
	if err != nil {
		return nil, err
	}
	raw, err := parseRawDiffStreamWithLedgerCPU(ctx, rawSpool.Reader(), ledger, runCPU)
	err = errors.Join(err, rawSpool.Close())
	if err != nil {
		return nil, err
	}

	numstatSpool, err := service.runGitToSpoolIn(
		ctx,
		repository.gitDirectory(),
		environment,
		numstatArguments,
		numstatWireLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read Git change counts: %w", err)
	}
	err = mergeNumstatStreamContextCPU(ctx, numstatSpool.Reader(), raw, ledger, runCPU)
	err = errors.Join(err, numstatSpool.Close())
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (service *Service) checkRenameCandidates(
	ctx context.Context,
	repository *rootedRepository,
	environment map[string]string,
	base, head review.ObjectID,
	runCPU CPUAdmission,
) error {
	spool, err := service.runGitToSpoolIn(
		ctx,
		repository.gitDirectory(),
		environment,
		[]string{
			"diff-tree", "--no-commit-id", "-r", "-z", gitRaw,
			gitNoRenames, gitNoExtDiff, gitNoTextconv,
			base.String(), head.String(),
		},
		rawDiffWireLimit,
	)
	if err != nil {
		return fmt.Errorf("count Git rename candidates: %w", err)
	}
	err = enforceRenameCandidateLimitStreamContextCPU(ctx, spool.Reader(), runCPU)
	err = errors.Join(err, spool.Close())
	return err
}

func (service *Service) checkIndexRenameCandidates(
	ctx context.Context,
	repository *rootedRepository,
	environment map[string]string,
	base review.ObjectID,
	runCPU CPUAdmission,
) error {
	spool, err := service.runGitToSpoolIn(
		ctx,
		repository.gitDirectory(),
		environment,
		[]string{
			gitDiff, gitCached, gitRaw, gitNoAbbrev, "-z",
			gitNoRenames, gitNoExtDiff, gitNoTextconv,
			base.String(),
		},
		rawDiffWireLimit,
	)
	if err != nil {
		return fmt.Errorf("count staged Git rename candidates: %w", err)
	}
	err = enforceRenameCandidateLimitStreamContextCPU(ctx, spool.Reader(), runCPU)
	return errors.Join(err, spool.Close())
}

func enforceRenameCandidateLimitStream(input io.Reader) error {
	return enforceRenameCandidateLimitStreamContext(context.Background(), input)
}

func enforceRenameCandidateLimitStreamContext(ctx context.Context, input io.Reader) error {
	tokens := newNULTokenReaderContext(ctx, input)
	var candidates uint32
	for count := 0; ; count++ {
		header, err := tokens.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read Git raw diff header: %w", err)
		}
		if count == review.MaxFiles {
			return fmt.Errorf("changed files exceed %d", review.MaxFiles)
		}
		parsed, err := parseRawDiffHeader(header)
		if err != nil {
			return err
		}
		pathCount := 1
		if parsed.status == 'R' || parsed.status == 'C' {
			pathCount = 2
		}
		for pathIndex := range pathCount {
			path, pathErr := tokens.Next()
			if pathErr != nil {
				return fmt.Errorf("read Git raw diff path %d: %w", pathIndex+1, pathErr)
			}
			if pathErr = validateRawPath(path); pathErr != nil {
				return pathErr
			}
		}
		if parsed.status == 'A' || parsed.status == 'D' {
			candidates++
			if candidates > 1_000 {
				return &LimitError{Resource: LimitRenameCandidates, Limit: 1_000}
			}
		}
	}
}

func parseRawDiff(value []byte) ([]diffRecord, error) {
	return parseRawDiffStream(bytes.NewReader(value))
}

func parseRawDiffStream(input io.Reader) ([]diffRecord, error) {
	return parseRawDiffStreamContext(context.Background(), input)
}

func parseRawDiffStreamContext(
	ctx context.Context,
	input io.Reader,
) ([]diffRecord, error) {
	ledger, err := newSourceLedger(0)
	if err != nil {
		return nil, err
	}
	return parseRawDiffStreamWithLedger(ctx, input, ledger)
}

func parseRawDiffStreamWithLedger(
	ctx context.Context,
	input io.Reader,
	ledger *sourceLedger,
) ([]diffRecord, error) {
	return parseRawDiffStreamWithLedgerLimits(
		ctx,
		input,
		ledger,
		review.MaxFiles,
		review.MaxSourcePaths,
	)
}

func parseRawDiffStreamWithLedgerLimits(
	ctx context.Context,
	input io.Reader,
	ledger *sourceLedger,
	recordLimit int,
	pathLimit int64,
) ([]diffRecord, error) {
	if recordLimit < 0 || recordLimit > review.MaxFiles ||
		pathLimit < 0 || pathLimit > review.MaxSourcePaths {
		return nil, errors.New("source diff limits are invalid")
	}
	builder := diffRecordBuilder{
		ledger: ledger, recordLimit: recordLimit, pathLimit: pathLimit,
	}
	tokens := newNULTokenReaderContext(ctx, input)
	for {
		header, err := tokens.Next()
		if errors.Is(err, io.EOF) {
			return builder.records, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read Git raw diff header: %w", err)
		}
		if len(builder.records) == builder.recordLimit {
			return nil, fmt.Errorf("changed files exceed %d", builder.recordLimit)
		}
		if capacityErr := builder.ensureCapacity(); capacityErr != nil {
			return nil, capacityErr
		}
		parsed, err := parseRawDiffHeader(header)
		if err != nil {
			return nil, err
		}
		var oldOID, newOID *review.ObjectID
		if parsed.hasOldOID {
			oldOID = new(parsed.oldOID)
		}
		if parsed.hasNewOID {
			newOID = new(parsed.newOID)
		}
		record := diffRecord{
			oldMode: parsed.oldMode,
			newMode: parsed.newMode,
			oldOID:  oldOID,
			newOID:  newOID,
			status:  parsed.status,
		}
		if err := assignRawDiffPaths(tokens, &builder, &record); err != nil {
			return nil, err
		}
		builder.add(record)
	}
}

type rawDiffHeader struct {
	oldOID    review.ObjectID
	newOID    review.ObjectID
	oldMode   review.FileMode
	newMode   review.FileMode
	status    byte
	hasOldOID bool
	hasNewOID bool
}

func parseRawDiffHeader(header []byte) (rawDiffHeader, error) {
	fields := bytes.Fields(header)
	if len(fields) != 5 || len(fields[0]) != 7 || fields[0][0] != ':' {
		return rawDiffHeader{}, fmt.Errorf(
			"invalid Git raw diff header %q",
			boundedText(string(header), 256),
		)
	}
	oldMode, err := review.NewFileMode(string(fields[0][1:]))
	if err != nil {
		return rawDiffHeader{}, err
	}
	newMode, err := review.NewFileMode(string(fields[1]))
	if err != nil {
		return rawDiffHeader{}, err
	}
	if len(fields[2]) != len(fields[3]) {
		return rawDiffHeader{}, errors.New("git raw diff mixes object ID formats")
	}
	oldOID, hasOldOID, err := rawObjectID(fields[2])
	if err != nil {
		return rawDiffHeader{}, err
	}
	newOID, hasNewOID, err := rawObjectID(fields[3])
	if err != nil {
		return rawDiffHeader{}, err
	}
	if !validRawStatus(fields[4]) {
		return rawDiffHeader{}, fmt.Errorf("unsupported Git path status %q", fields[4])
	}
	return rawDiffHeader{
		oldMode: oldMode, newMode: newMode, oldOID: oldOID, newOID: newOID,
		status: fields[4][0], hasOldOID: hasOldOID, hasNewOID: hasNewOID,
	}, nil
}

func assignRawDiffPaths(
	tokens *nulTokenReader,
	builder *diffRecordBuilder,
	record *diffRecord,
) error {
	path, err := tokens.Next()
	if err != nil {
		return fmt.Errorf("read Git raw diff path: %w", err)
	}
	if err := validateRawPath(path); err != nil {
		return err
	}
	switch record.status {
	case 'A':
		if err := builder.reservePath(len(path)); err != nil {
			return err
		}
		record.newPath = string(path)
	case 'D':
		if err := builder.reservePath(len(path)); err != nil {
			return err
		}
		record.oldPath = string(path)
	case 'R', 'C':
		newPath, err := tokens.Next()
		if err != nil {
			return fmt.Errorf("read Git raw rename/copy path: %w", err)
		}
		if err := validateRawPath(newPath); err != nil {
			return err
		}
		if err := builder.reservePath(len(path) + len(newPath)); err != nil {
			return err
		}
		record.oldPath = string(path)
		record.newPath = string(newPath)
	default:
		if err := builder.reservePath(len(path)); err != nil {
			return err
		}
		owned := string(path)
		record.oldPath = owned
		record.newPath = owned
	}
	return nil
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

func mergeNumstatStream(input io.Reader, records []diffRecord) error {
	return mergeNumstatStreamContext(context.Background(), input, records)
}

func mergeNumstatStreamContext(
	ctx context.Context,
	input io.Reader,
	records []diffRecord,
) error {
	tokens := newNULTokenReaderContext(ctx, input)
	for index := 0; ; index++ {
		token, err := tokens.Next()
		if errors.Is(err, io.EOF) {
			if index != len(records) {
				return fmt.Errorf("git metadata streams disagree: raw=%d numstat=%d", len(records), index)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("read Git numstat record: %w", err)
		}
		if index >= len(records) {
			return fmt.Errorf("git metadata streams disagree: raw=%d numstat>%d", len(records), index)
		}
		firstTab := bytes.IndexByte(token, '\t')
		secondTab := -1
		if firstTab >= 0 {
			offset := bytes.IndexByte(token[firstTab+1:], '\t')
			if offset >= 0 {
				secondTab = firstTab + 1 + offset
			}
		}
		if firstTab <= 0 || secondTab <= firstTab+1 {
			return fmt.Errorf("invalid Git numstat record %q", boundedText(string(token), 256))
		}
		additions, binaryA, err := parseCount(token[:firstTab])
		if err != nil {
			return err
		}
		deletions, binaryD, err := parseCount(token[firstTab+1 : secondTab])
		if err != nil {
			return err
		}
		if binaryA != binaryD {
			return errors.New("git numstat has inconsistent binary counts")
		}

		var oldPath, newPath []byte
		path := token[secondTab+1:]
		if len(path) == 0 {
			oldPath, err = tokens.Next()
			if err != nil {
				return fmt.Errorf("read Git numstat old path: %w", err)
			}
			newPath, err = tokens.Next()
			if err != nil {
				return fmt.Errorf("read Git numstat new path: %w", err)
			}
			if err := validateRawPath(oldPath); err != nil {
				return err
			}
			if err := validateRawPath(newPath); err != nil {
				return err
			}
		} else {
			if err := validateRawPath(path); err != nil {
				return err
			}
			oldPath = path
			newPath = path
		}
		raw := &records[index]
		var matches bool
		switch raw.status {
		case 'A':
			matches = raw.newPath == string(newPath)
		case 'D':
			matches = raw.oldPath == string(oldPath)
		default:
			matches = raw.oldPath == string(oldPath) && raw.newPath == string(newPath)
		}
		if !matches {
			return fmt.Errorf("git metadata streams disagree at file %d", index)
		}
		raw.additions = additions
		raw.deletions = deletions
		raw.binary = binaryA
	}
}

func (service *Service) snapshot(
	ctx context.Context,
	runCPU CPUAdmission,
	sourceID review.SourceID,
	repository *rootedRepository,
	environment map[string]string,
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
	gitignore := skipGitignore
	if options.Gitignore {
		gitignore = useGitignore
	}
	ignored, err := service.gitignored(
		ctx,
		runCPU,
		repository,
		environment,
		records,
		gitignore,
		ledger,
	)
	if err != nil {
		return review.Snapshot{}, err
	}
	var rules []ignoreRule
	if options.RVWIgnore && options.IgnoreFile != "" {
		contents, readErr := readIgnoreRuleContents(repository, options.IgnoreFile)
		if readErr != nil {
			return review.Snapshot{}, readErr
		}
		if err := runCPU(ctx, func() error {
			rules, readErr = parseIgnoreRules(ctx, options.IgnoreFile, contents)
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
			ignored,
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

func rawObjectID(raw []byte) (review.ObjectID, bool, error) {
	if (len(raw) == 40 || len(raw) == 64) &&
		bytes.Count(raw, []byte{'0'}) == len(raw) {
		return "", false, nil
	}
	id, err := review.NewObjectID(string(raw))
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func parseCount(value []byte) (uint32, bool, error) {
	if bytes.Equal(value, []byte("-")) {
		return 0, true, nil
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, false, fmt.Errorf("invalid git count %q", value)
		}
	}
	count, err := strconv.ParseUint(string(value), 10, 32)
	if err != nil || count > uint64(review.MaxAdditions) {
		return 0, false, fmt.Errorf("git count %q exceeds %d", value, review.MaxAdditions)
	}
	return uint32(count), false, nil
}

func nextNULTerminated(value []byte) ([]byte, []byte, bool) {
	before, after, ok := bytes.Cut(value, []byte{0})
	if !ok {
		return nil, value, false
	}
	return before, after, true
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
