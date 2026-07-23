package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/jacoelho/rvw/internal/review"
)

const (
	maxWorktreeFileBytes       int64 = 128 << 20
	rawWorktreeBatchBytes      int64 = 16 << 20
	rawWorktreeBatchEntries          = 256
	rawWorktreeHashInputBytes        = 1 << 20
	rawWorktreeCopyBufferBytes       = 64 << 10
	indexUpdateBatchBytes            = 1 << 20
)

type worktreeFilterName uint8

const (
	worktreeFilterUnspecified worktreeFilterName = iota + 1
	worktreeFilterUnset
)

type worktreeFilterNames struct {
	bits uint8
}

func (names *worktreeFilterNames) add(name worktreeFilterName) {
	names.bits |= 1 << (name - 1)
}

func (names *worktreeFilterNames) has(name worktreeFilterName) bool {
	return names.bits&(1<<(name-1)) != 0
}

type untrackedWorktreeKind uint8

const (
	untrackedWorktreeOrdinary untrackedWorktreeKind = iota + 1
	untrackedWorktreeGitlink
)

type untrackedWorktreeRecord struct {
	path string
	kind untrackedWorktreeKind
}

type rawWorktreeEntry struct {
	oid  *review.ObjectID
	mode review.FileMode
	path string
}

type stagedWorktreeBlob struct {
	file       string
	mode       review.FileMode
	entryIndex int
	size       int64
}

type rawWorktreeCaptureKind uint8

const (
	rawWorktreeRegular rawWorktreeCaptureKind = iota + 1
	rawWorktreeSymlink
	rawWorktreeGitlink
)

type rawWorktreeCapture struct {
	file *os.File
	root *os.Root
	info os.FileInfo
	path string
	link string
	mode review.FileMode
	size int64
	kind rawWorktreeCaptureKind
}

type rawWorktreeBatch struct {
	blobs      []stagedWorktreeBlob
	rawBytes   int64
	inputBytes int
}

type indexUpdate struct {
	mode   review.FileMode
	object review.ObjectID
	path   string
	remove bool
}

func (service *Service) synthesizeRawWorktree(
	ctx context.Context,
	repository *rootedRepository,
	environment map[string]string,
	base review.ObjectID,
	contents worktreeContents,
	workspace string,
	runCPU CPUAdmission,
) (_ review.ObjectID, returnErr error) {
	ledger, err := newSourceLedger(MaxCommandErrorConstructionBytes + maxSourceProtocolToken)
	if err != nil {
		return "", err
	}
	inventories, untracked, untrackedPathBytes, err := service.captureInitialWorktreeInventories(
		ctx, repository, environment, contents, ledger, runCPU,
	)
	if err != nil {
		return "", err
	}
	changed, err := service.worktreeChanges(
		ctx,
		repository,
		environment,
		ledger,
		review.MaxFiles-len(untracked),
		review.MaxSourcePaths-untrackedPathBytes,
		runCPU,
	)
	if err != nil {
		return "", err
	}
	raw, removals, paths, err := prepareRawWorktreeCandidates(
		ctx, runCPU, ledger, changed, untracked,
	)
	if err != nil {
		return "", err
	}
	raw, removals, paths, err = service.collapseEmbeddedWorktreeRepositories(
		ctx,
		repository,
		environment,
		ledger,
		raw,
		removals,
		paths,
		runCPU,
	)
	if err != nil {
		return "", err
	}
	if filterErr := service.rejectWorktreeFilterDrivers(
		ctx, repository, environment, paths, runCPU,
	); filterErr != nil {
		return "", filterErr
	}
	raw, err = service.hashRawWorktreeEntries(
		ctx, repository, environment, workspace, raw, runCPU,
	)
	if err != nil {
		return "", err
	}
	raw, err = service.resolveRawWorktreeGitlinks(ctx, repository, environment, raw)
	if err != nil {
		return "", err
	}
	if filterErr := service.rejectWorktreeFilterDrivers(
		ctx, repository, environment, paths, runCPU,
	); filterErr != nil {
		return "", filterErr
	}
	if verifyErr := service.verifyRawWorktreeGitlinks(ctx, repository, environment, raw); verifyErr != nil {
		return "", verifyErr
	}
	var updates []indexUpdate
	err = runCPU(ctx, func() error {
		updates, err = rawWorktreeUpdates(raw)
		return err
	})
	if err != nil {
		return "", err
	}
	updates = append(updates, removals...)
	if sortErr := runCPU(ctx, func() error {
		return sortWorktreeUpdates(ctx, ledger, updates)
	}); sortErr != nil {
		return "", sortErr
	}
	stage, err := service.verifyWorktreeInventories(
		ctx, repository, environment, inventories, ledger, runCPU,
	)
	if err != nil {
		return "", err
	}
	defer func() {
		returnErr = errors.Join(returnErr, stage.Close())
	}()
	privateEnvironment, err := service.initializePrivateIndexFromSpool(
		ctx, repository, environment, workspace, stage,
	)
	if err != nil {
		return "", err
	}
	if applyErr := service.applyIndexUpdates(
		ctx,
		repository,
		privateEnvironment,
		base,
		updates,
		runCPU,
	); applyErr != nil {
		return "", applyErr
	}
	tree, err := service.writeWorktreeIndexTree(ctx, repository, privateEnvironment)
	if err != nil {
		return "", err
	}
	return tree, nil
}

func prepareRawWorktreeCandidates(
	ctx context.Context,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	changed []diffRecord,
	untracked []untrackedWorktreeRecord,
) ([]rawWorktreeEntry, []indexUpdate, []string, error) {
	const candidateMetadataBytes int64 = 200
	candidateBytes := int64(len(changed)+len(untracked))*candidateMetadataBytes +
		rawWorktreeBatchBytes
	if err := ledger.reserve(candidateBytes); err != nil {
		return nil, nil, nil, err
	}
	var raw []rawWorktreeEntry
	var removals []indexUpdate
	var paths []string
	err := runCPU(ctx, func() error {
		var prepareErr error
		raw, removals, paths, prepareErr = prepareWorktreeCandidates(
			ctx, ledger, changed, untracked,
		)
		return prepareErr
	})
	return raw, removals, paths, err
}

func (service *Service) worktreeChanges(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	ledger *sourceLedger,
	recordLimit int,
	pathLimit int64,
	runCPU CPUAdmission,
) ([]diffRecord, error) {
	spool, err := service.runRootedGitToSpool(
		ctx,
		worktree,
		environment,
		[]string{
			"diff-files", gitRaw, gitNoAbbrev, "-z", gitNoRenames,
			gitNoExtDiff, gitNoTextconv,
		},
		rawDiffWireLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read worktree changes: %w", err)
	}
	records, err := parseRawDiffStreamWithLedgerLimitsCPU(
		ctx,
		spool.Reader(),
		ledger,
		recordLimit,
		pathLimit,
		runCPU,
	)
	err = errors.Join(err, spool.Close())
	if err != nil {
		return nil, fmt.Errorf("parse worktree changes: %w", err)
	}
	return records, nil
}

func prepareWorktreeCandidates(
	ctx context.Context,
	ledger *sourceLedger,
	changed []diffRecord,
	untracked []untrackedWorktreeRecord,
) ([]rawWorktreeEntry, []indexUpdate, []string, error) {
	type recordGroup struct {
		path          string
		unmergedIndex int
		concreteIndex int
	}
	groups := make([]recordGroup, 0, len(changed))
	groupIndexes := make(map[string]int, len(changed))
	for recordIndex, record := range changed {
		path := record.newPath
		if record.status == 'D' {
			path = record.oldPath
		}
		if err := validateRepositoryRelativePathString(path); err != nil {
			return nil, nil, nil, err
		}
		key := path
		groupIndex, exists := groupIndexes[key]
		if !exists {
			if len(groups) == review.MaxFiles {
				return nil, nil, nil, fmt.Errorf("worktree paths exceed %d", review.MaxFiles)
			}
			groupIndex = len(groups)
			groupIndexes[key] = groupIndex
			groups = append(groups, recordGroup{
				path: path, unmergedIndex: -1, concreteIndex: -1,
			})
		}
		group := &groups[groupIndex]
		if record.status == 'U' {
			if group.unmergedIndex >= 0 {
				return nil, nil, nil, fmt.Errorf(
					"worktree inventory repeats unmerged path %q",
					path,
				)
			}
			group.unmergedIndex = recordIndex
		} else {
			if group.concreteIndex >= 0 {
				return nil, nil, nil, fmt.Errorf(
					"worktree inventory repeats concrete path %q",
					path,
				)
			}
			group.concreteIndex = recordIndex
		}
	}

	raw := make([]rawWorktreeEntry, 0, len(groups))
	removals := make([]indexUpdate, 0)
	paths := make([]string, 0, len(groups))
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		selectedIndex := group.concreteIndex
		if selectedIndex < 0 {
			selectedIndex = group.unmergedIndex
		}
		if selectedIndex < 0 {
			return nil, nil, nil, errors.New("worktree record group is empty")
		}
		selected := changed[selectedIndex]
		paths = append(paths, group.path)
		seen[group.path] = struct{}{}
		if selected.status == 'D' ||
			selected.status == 'U' && selected.newMode.String() == "000000" {
			removals = append(removals, indexUpdate{path: group.path, remove: true})
			continue
		}
		raw = append(raw, rawWorktreeEntry{path: group.path, mode: selected.newMode})
	}

	for _, record := range untracked {
		key := record.path
		if _, exists := seen[key]; exists {
			continue
		}
		if len(seen) == review.MaxFiles {
			return nil, nil, nil, fmt.Errorf("worktree paths exceed %d", review.MaxFiles)
		}
		seen[key] = struct{}{}
		paths = append(paths, record.path)
		entry := rawWorktreeEntry{path: record.path}
		if record.kind == untrackedWorktreeGitlink {
			entry.mode = review.FileMode("160000")
		}
		raw = append(raw, entry)
	}
	if len(seen) > review.MaxFiles {
		return nil, nil, nil, fmt.Errorf("worktree paths exceed %d", review.MaxFiles)
	}
	if err := sortWorktreeStrings(ctx, ledger, paths); err != nil {
		return nil, nil, nil, err
	}
	return raw, removals, paths, nil
}

func parseUntrackedWorktreeRecords(
	inventory []byte,
	existing map[string]struct{},
	limit int,
) ([]untrackedWorktreeRecord, error) {
	if limit < 0 {
		return nil, errors.New("untracked inventory limit is invalid")
	}
	records := make([]untrackedWorktreeRecord, 0)
	inventorySeen := make(map[string]untrackedWorktreeKind)
	for len(inventory) > 0 {
		path, remaining, ok := nextNULTerminated(inventory)
		if !ok {
			return nil, errors.New("untracked file inventory is not NUL-terminated")
		}
		inventory = remaining
		kind := untrackedWorktreeOrdinary
		if len(path) > 0 && path[len(path)-1] == '/' {
			kind = untrackedWorktreeGitlink
			path = path[:len(path)-1]
		}
		if err := validateRepositoryRelativePath(path); err != nil {
			return nil, err
		}
		key := string(path)
		if previous, exists := inventorySeen[key]; exists {
			if previous != kind {
				return nil, fmt.Errorf("untracked inventory changes kind for path %q", path)
			}
			return nil, fmt.Errorf("untracked inventory repeats path %q", path)
		}
		inventorySeen[key] = kind
		if _, exists := existing[key]; exists {
			continue
		}
		if len(existing) == limit {
			return nil, fmt.Errorf("worktree paths exceed %d", limit)
		}
		existing[key] = struct{}{}
		records = append(records, untrackedWorktreeRecord{
			path: key,
			kind: kind,
		})
	}
	return records, nil
}

func validateRepositoryRelativePathString(path string) error {
	return validateRepositoryRelativePath([]byte(path))
}

func validateRepositoryRelativePath(path []byte) error {
	if err := validateRawPath(path); err != nil {
		return err
	}
	if path[0] == '/' {
		return errors.New("git path is absolute")
	}
	for component := range bytes.SplitSeq(path, []byte{'/'}) {
		if len(component) == 0 || bytes.Equal(component, []byte(".")) ||
			bytes.Equal(component, []byte("..")) {
			return errors.New("git path contains an invalid component")
		}
	}
	return nil
}

func (service *Service) rejectWorktreeFilterDrivers(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	paths []string,
	runCPU CPUAdmission,
) error {
	names := worktreeFilterNames{}
	for start := 0; start < len(paths); {
		var batch worktreePathBatch
		if err := runCPU(ctx, func() error {
			var buildErr error
			batch, buildErr = buildWorktreePathBatch(ctx, paths, start)
			return buildErr
		}); err != nil {
			return err
		}
		result, err := service.runRootedGitWithInput(
			ctx,
			worktree,
			environment,
			[]string{"check-attr", "-z", gitStdin, "filter"},
			batch.input,
			batch.outputLimit,
		)
		if err != nil {
			return fmt.Errorf("inspect Git filter attributes: %w", err)
		}
		var batchNames worktreeFilterNames
		err = runCPU(ctx, func() error {
			var validationErr error
			batchNames, validationErr = validateFilterAttributeOutput(
				result.Stdout, paths[start:batch.next],
			)
			return validationErr
		})
		if err != nil {
			return err
		}
		names.bits |= batchNames.bits
		start = batch.next
	}
	return service.rejectConfiguredAmbiguousFilterDrivers(
		ctx, worktree, environment, paths, names, runCPU,
	)
}

func worktreeFilterOutputLimit(inputBytes, pathCount int) (int64, error) {
	const recordOverhead = int64(len("filter") + 1 + len("unspecified") + 1)
	if inputBytes < 0 || pathCount < 0 ||
		int64(pathCount) > (math.MaxInt64-int64(inputBytes))/recordOverhead {
		return 0, errors.New("worktree filter output limit overflows")
	}
	return int64(inputBytes) + int64(pathCount)*recordOverhead, nil
}

func validateFilterAttributeOutput(
	output []byte,
	paths []string,
) (worktreeFilterNames, error) {
	names := worktreeFilterNames{}
	for index, path := range paths {
		actualPath, rest, ok := nextNULTerminated(output)
		if !ok {
			return names, errors.New("git filter attribute output omits a path")
		}
		attribute, rest, ok := nextNULTerminated(rest)
		if !ok {
			return names, errors.New("git filter attribute output omits an attribute")
		}
		value, rest, ok := nextNULTerminated(rest)
		if !ok {
			return names, errors.New("git filter attribute output omits a value")
		}
		output = rest
		if string(actualPath) != path || !bytes.Equal(attribute, []byte("filter")) {
			return names, fmt.Errorf("git returned an unexpected filter attribute at path %d", index+1)
		}
		switch {
		case bytes.Equal(value, []byte("unspecified")):
			names.add(worktreeFilterUnspecified)
		case bytes.Equal(value, []byte("unset")):
			names.add(worktreeFilterUnset)
		default:
			return names, errors.New(
				"worktree files using Git clean/process filters cannot be snapshotted safely",
			)
		}
	}
	if len(output) != 0 {
		return names, errors.New("git filter attribute output contains extra records")
	}
	return names, nil
}

func (service *Service) rejectConfiguredAmbiguousFilterDrivers(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	paths []string,
	names worktreeFilterNames,
	runCPU CPUAdmission,
) error {
	var pattern string
	switch {
	case names.has(worktreeFilterUnspecified) && names.has(worktreeFilterUnset):
		pattern = `^filter\.(unspecified|unset)\.(clean|process)$`
	case names.has(worktreeFilterUnspecified):
		pattern = `^filter\.unspecified\.(clean|process)$`
	case names.has(worktreeFilterUnset):
		pattern = `^filter\.unset\.(clean|process)$`
	default:
		return nil
	}
	result, err := service.runRootedGit(
		ctx,
		worktree,
		environment,
		[]string{"config", "--null", "--name-only", "--get-regexp", pattern},
		[]int{0, 1},
		256,
	)
	if err != nil {
		return fmt.Errorf("inspect Git filter driver configuration: %w", err)
	}
	if result.ExitCode == 1 {
		return nil
	}
	var configured worktreeFilterNames
	err = runCPU(ctx, func() error {
		var parseErr error
		configured, parseErr = parseConfiguredAmbiguousFilterNames(result.Stdout)
		return parseErr
	})
	if err != nil {
		return err
	}
	return service.rejectExplicitAmbiguousFilterAttributes(
		ctx,
		worktree,
		environment,
		paths,
		configured,
		runCPU,
	)
}

func parseConfiguredAmbiguousFilterNames(output []byte) (worktreeFilterNames, error) {
	names := worktreeFilterNames{}
	for len(output) != 0 {
		name, remaining, ok := nextNULTerminated(output)
		if !ok {
			return names, errors.New("git filter driver configuration is not NUL-terminated")
		}
		output = remaining
		switch string(name) {
		case "filter.unspecified.clean", "filter.unspecified.process":
			names.add(worktreeFilterUnspecified)
		case "filter.unset.clean", "filter.unset.process":
			names.add(worktreeFilterUnset)
		default:
			return names, fmt.Errorf("git returned unexpected filter driver configuration %q", name)
		}
	}
	if names.bits == 0 {
		return names, errors.New("git reported empty filter driver configuration")
	}
	return names, nil
}

func (service *Service) rejectExplicitAmbiguousFilterAttributes(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	paths []string,
	configured worktreeFilterNames,
	runCPU CPUAdmission,
) error {
	for start := 0; start < len(paths); {
		var batch worktreePathBatch
		if err := runCPU(ctx, func() error {
			var buildErr error
			batch, buildErr = buildWorktreePathBatch(ctx, paths, start)
			return buildErr
		}); err != nil {
			return err
		}
		result, err := service.runRootedGitWithInput(
			ctx,
			worktree,
			environment,
			[]string{"check-attr", "-z", "--all", gitStdin},
			batch.input,
			indexUpdateBatchBytes,
		)
		if err != nil {
			return fmt.Errorf("disambiguate Git filter attributes: %w", err)
		}
		if err := runCPU(ctx, func() error {
			return validateExplicitAmbiguousFilterAttributes(
				result.Stdout,
				paths[start:batch.next],
				configured,
			)
		}); err != nil {
			return err
		}
		start = batch.next
	}
	return nil
}

func validateExplicitAmbiguousFilterAttributes(
	output []byte,
	paths []string,
	configured worktreeFilterNames,
) error {
	expected := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		expected[path] = struct{}{}
	}
	for len(output) != 0 {
		path, rest, ok := nextNULTerminated(output)
		if !ok {
			return errors.New("git attribute provenance omits a path terminator")
		}
		attribute, rest, ok := nextNULTerminated(rest)
		if !ok {
			return errors.New("git attribute provenance omits an attribute terminator")
		}
		value, rest, ok := nextNULTerminated(rest)
		if !ok {
			return errors.New("git attribute provenance omits a value terminator")
		}
		output = rest
		if _, exists := expected[string(path)]; !exists {
			return fmt.Errorf("git returned attribute provenance for unexpected path %q", path)
		}
		if !bytes.Equal(attribute, []byte("filter")) {
			continue
		}
		switch {
		case bytes.Equal(value, []byte("unspecified")):
			if configured.has(worktreeFilterUnspecified) {
				return errors.New(
					"worktree filter attribute is ambiguous with configured driver name \"unspecified\"",
				)
			}
		case bytes.Equal(value, []byte("unset")):
			if configured.has(worktreeFilterUnset) {
				return errors.New(
					"worktree filter attribute is ambiguous with configured driver name \"unset\"",
				)
			}
		default:
			return errors.New(
				"worktree files using Git clean/process filters cannot be snapshotted safely",
			)
		}
	}
	return nil
}
