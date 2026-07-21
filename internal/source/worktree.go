package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/jacoelho/rvw/internal/review"
)

const (
	maxWorktreeInventoryBytes  int64 = 64 << 20
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
	path []byte
	kind untrackedWorktreeKind
}

type rawWorktreeEntry struct {
	oid  *review.ObjectID
	mode review.FileMode
	path []byte
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
	path   []byte
	remove bool
}

func (service *Service) synthesizeRawWorktree(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	base review.ObjectID,
	contents worktreeContents,
	workspace string,
) (review.ObjectID, error) {
	liveIndex, err := service.worktreeInventory(
		ctx,
		root,
		environment,
		[]string{gitLSFiles, gitStage, "-z"},
	)
	if err != nil {
		return "", fmt.Errorf("read live Git index: %w", err)
	}
	if indexFlagErr := service.rejectWorktreeIndexFlags(ctx, root, environment); indexFlagErr != nil {
		return "", indexFlagErr
	}
	changed, err := service.worktreeChanges(ctx, root, environment)
	if err != nil {
		return "", err
	}
	untracked := []byte(nil)
	if contents == worktreeWithUntracked {
		untracked, err = service.worktreeInventory(
			ctx,
			root,
			environment,
			[]string{gitLSFiles, "--others", "-z", "--"},
		)
		if err != nil {
			return "", fmt.Errorf("read untracked files: %w", err)
		}
	}

	raw, removals, paths, err := prepareWorktreeCandidates(changed, untracked)
	if err != nil {
		return "", err
	}
	raw, removals, paths, err = service.collapseEmbeddedWorktreeRepositories(
		ctx,
		root,
		environment,
		raw,
		removals,
		paths,
	)
	if err != nil {
		return "", err
	}
	if filterErr := service.rejectWorktreeFilterDrivers(ctx, root, environment, paths); filterErr != nil {
		return "", filterErr
	}
	raw, err = service.hashRawWorktreeEntries(ctx, root, environment, workspace, raw)
	if err != nil {
		return "", err
	}
	raw, err = service.resolveRawWorktreeGitlinks(ctx, root, environment, raw)
	if err != nil {
		return "", err
	}
	if filterErr := service.rejectWorktreeFilterDrivers(ctx, root, environment, paths); filterErr != nil {
		return "", filterErr
	}

	if verifyErr := service.verifyWorktreeInventories(
		ctx,
		root,
		environment,
		contents,
		liveIndex,
		untracked,
	); verifyErr != nil {
		return "", verifyErr
	}

	privateEnvironment, err := initializePrivateIndex(
		ctx,
		service,
		root,
		environment,
		workspace,
		liveIndex,
	)
	if err != nil {
		return "", err
	}
	updates, err := rawWorktreeUpdates(raw)
	if err != nil {
		return "", err
	}
	updates = append(updates, removals...)
	slices.SortFunc(updates, func(left, right indexUpdate) int {
		if left.remove != right.remove {
			if left.remove {
				return -1
			}
			return 1
		}
		return bytes.Compare(left.path, right.path)
	})
	if applyErr := service.applyIndexUpdates(
		ctx,
		root,
		privateEnvironment,
		base,
		updates,
	); applyErr != nil {
		return "", applyErr
	}
	tree, err := service.writeIndexTree(ctx, root, privateEnvironment)
	if err != nil {
		return "", err
	}
	if err := service.verifyRawWorktreeGitlinks(ctx, root, environment, raw); err != nil {
		return "", err
	}
	return tree, nil
}

func (service *Service) verifyWorktreeInventories(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	contents worktreeContents,
	liveIndex []byte,
	untracked []byte,
) error {
	currentIndex, err := service.worktreeInventory(
		ctx,
		root,
		environment,
		[]string{gitLSFiles, gitStage, "-z"},
	)
	if err != nil {
		return fmt.Errorf("verify live Git index: %w", err)
	}
	if !bytes.Equal(liveIndex, currentIndex) {
		return errors.New("live Git index changed while snapshotting worktree")
	}
	if contents != worktreeWithUntracked {
		return nil
	}
	currentUntracked, err := service.worktreeInventory(
		ctx,
		root,
		environment,
		[]string{gitLSFiles, "--others", "-z", "--"},
	)
	if err != nil {
		return fmt.Errorf("verify untracked files: %w", err)
	}
	if !bytes.Equal(untracked, currentUntracked) {
		return errors.New("untracked file inventory changed while snapshotting worktree")
	}
	return nil
}

func (service *Service) rejectWorktreeIndexFlags(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
) error {
	inventory, err := service.worktreeInventory(
		ctx,
		root,
		environment,
		[]string{gitLSFiles, "-v", "-z", "--"},
	)
	if err != nil {
		return fmt.Errorf("inspect worktree index flags: %w", err)
	}
	for len(inventory) != 0 {
		end := bytes.IndexByte(inventory, 0)
		if end < 0 {
			return errors.New("worktree index flag entry is not NUL terminated")
		}
		entry := inventory[:end]
		inventory = inventory[end+1:]
		if len(entry) < 3 || entry[1] != ' ' {
			return errors.New("worktree index flag entry is malformed")
		}
		if entry[0] >= 'a' && entry[0] <= 'z' || entry[0] == 'S' {
			return errors.New(
				"assume-unchanged and skip-worktree paths cannot be snapshotted safely",
			)
		}
	}
	return nil
}

func initializePrivateIndex(
	ctx context.Context,
	service *Service,
	root RepositoryPath,
	environment map[string]string,
	workspace string,
	liveIndex []byte,
) (map[string]string, error) {
	privateEnvironment := cloneEnvironment(environment)
	indexPath := filepath.Join(workspace, "index")
	privateEnvironment["GIT_INDEX_FILE"] = indexPath
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("prepare private worktree index: %w", err)
	}
	if _, err := service.runGitWithInput(
		ctx,
		root.String(),
		privateEnvironment,
		[]string{gitUpdateIndex, "-z", gitIndexInfo},
		liveIndex,
		nil,
		64<<10,
	); err != nil {
		return nil, fmt.Errorf("copy live Git index: %w", err)
	}
	return privateEnvironment, nil
}

func (service *Service) worktreeInventory(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	arguments []string,
) ([]byte, error) {
	result, err := service.runGit(
		ctx,
		root.String(),
		environment,
		arguments,
		nil,
		maxWorktreeInventoryBytes,
	)
	if err != nil {
		return nil, err
	}
	return result.Stdout, nil
}

func (service *Service) worktreeChanges(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
) ([]diffRecord, error) {
	result, err := service.runGit(
		ctx,
		root.String(),
		environment,
		[]string{
			"diff-files", gitRaw, gitNoAbbrev, "-z", gitNoRenames,
			gitNoExtDiff, gitNoTextconv,
		},
		nil,
		maxWorktreeInventoryBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("read worktree changes: %w", err)
	}
	records, err := parseRawDiff(result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("parse worktree changes: %w", err)
	}
	return records, nil
}

func prepareWorktreeCandidates(
	changed []diffRecord,
	untracked []byte,
) ([]rawWorktreeEntry, []indexUpdate, [][]byte, error) {
	type recordGroup struct {
		unmerged *diffRecord
		concrete *diffRecord
		path     []byte
	}

	groups := make([]recordGroup, 0, len(changed))
	groupIndexes := make(map[string]int, len(changed))
	for _, record := range changed {
		path := record.newPath
		if record.status == 'D' {
			path = record.oldPath
		}
		if err := validateRepositoryRelativePath(path); err != nil {
			return nil, nil, nil, err
		}
		key := string(path)
		groupIndex, exists := groupIndexes[key]
		if !exists {
			if len(groups) == review.MaxFiles {
				return nil, nil, nil, fmt.Errorf("worktree paths exceed %d", review.MaxFiles)
			}
			groupIndex = len(groups)
			groupIndexes[key] = groupIndex
			groups = append(groups, recordGroup{path: bytes.Clone(path)})
		}
		group := &groups[groupIndex]
		if record.status == 'U' {
			if group.unmerged != nil {
				return nil, nil, nil, fmt.Errorf(
					"worktree inventory repeats unmerged path %q",
					path,
				)
			}
			group.unmerged = new(record)
		} else {
			if group.concrete != nil {
				return nil, nil, nil, fmt.Errorf(
					"worktree inventory repeats concrete path %q",
					path,
				)
			}
			group.concrete = new(record)
		}
	}

	raw := make([]rawWorktreeEntry, 0, len(groups))
	removals := make([]indexUpdate, 0)
	paths := make([][]byte, 0, len(groups))
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		selected := group.concrete
		if selected == nil {
			selected = group.unmerged
		}
		if selected == nil {
			return nil, nil, nil, errors.New("worktree record group is empty")
		}
		paths = append(paths, group.path)
		seen[string(group.path)] = struct{}{}
		if selected.status == 'D' ||
			selected.status == 'U' && selected.newMode.String() == "000000" {
			removals = append(removals, indexUpdate{path: group.path, remove: true})
			continue
		}
		raw = append(raw, rawWorktreeEntry{path: group.path, mode: selected.newMode})
	}

	untrackedRecords, err := parseUntrackedWorktreeRecords(untracked, seen, review.MaxFiles)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, record := range untrackedRecords {
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
	slices.SortFunc(paths, bytes.Compare)
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
			path: bytes.Clone(path),
			kind: kind,
		})
	}
	return records, nil
}

func (service *Service) collapseEmbeddedWorktreeRepositories(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	raw []rawWorktreeEntry,
	removals []indexUpdate,
	paths [][]byte,
) (
	resultRaw []rawWorktreeEntry,
	resultRemovals []indexUpdate,
	resultPaths [][]byte,
	returnErr error,
) {
	filesystem, err := os.OpenRoot(root.String())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open worktree root: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, filesystem.Close())
	}()
	roots := make([][]byte, 0)
	for _, entry := range raw {
		if entry.mode.String() == "160000" {
			roots = append(roots, bytes.Clone(entry.path))
		}
	}
	checked := make(map[string]embeddedWorktreeState)
	for _, path := range paths {
		embedded, ok, err := service.embeddedWorktreeRepositoryRoot(
			ctx,
			root,
			environment,
			filesystem,
			path,
			checked,
		)
		if err != nil {
			return nil, nil, nil, err
		}
		if ok {
			roots = append(roots, embedded)
		}
	}
	if len(roots) == 0 {
		return raw, removals, paths, nil
	}
	uniqueRoots, rootSet := uniqueWorktreeRepositoryRoots(roots)

	removalPaths := make(map[string]struct{}, len(removals))
	for _, removal := range removals {
		removalPaths[string(removal.path)] = struct{}{}
	}
	collapsedRaw := make([]rawWorktreeEntry, 0, len(raw)+len(uniqueRoots))
	rootPresent := make(map[string]struct{}, len(uniqueRoots))
	for _, entry := range raw {
		rootPath, under := containingWorktreeRepository(entry.path, rootSet)
		if !under {
			collapsedRaw = append(collapsedRaw, entry)
			continue
		}
		if bytes.Equal(entry.path, rootPath) {
			key := string(rootPath)
			if _, exists := rootPresent[key]; !exists {
				collapsedRaw = append(collapsedRaw, rawWorktreeEntry{
					path: bytes.Clone(rootPath),
					mode: review.FileMode("160000"),
				})
				rootPresent[key] = struct{}{}
			}
			continue
		}
		key := string(entry.path)
		if _, exists := removalPaths[key]; !exists {
			removals = append(removals, indexUpdate{path: entry.path, remove: true})
			removalPaths[key] = struct{}{}
		}
	}
	for _, rootPath := range uniqueRoots {
		if _, exists := rootPresent[string(rootPath)]; exists {
			continue
		}
		collapsedRaw = append(collapsedRaw, rawWorktreeEntry{
			path: bytes.Clone(rootPath),
			mode: review.FileMode("160000"),
		})
	}

	collapsedPaths := make([][]byte, 0, len(paths)+len(uniqueRoots))
	seenPaths := make(map[string]struct{}, len(paths)+len(uniqueRoots))
	for _, path := range paths {
		if _, under := containingWorktreeRepository(path, rootSet); under {
			continue
		}
		key := string(path)
		if _, exists := seenPaths[key]; !exists {
			collapsedPaths = append(collapsedPaths, path)
			seenPaths[key] = struct{}{}
		}
	}
	for _, rootPath := range uniqueRoots {
		key := string(rootPath)
		if _, exists := seenPaths[key]; !exists {
			collapsedPaths = append(collapsedPaths, bytes.Clone(rootPath))
			seenPaths[key] = struct{}{}
		}
	}
	if len(seenPaths) > review.MaxFiles {
		return nil, nil, nil, fmt.Errorf("worktree paths exceed %d", review.MaxFiles)
	}
	slices.SortFunc(collapsedPaths, bytes.Compare)
	return collapsedRaw, removals, collapsedPaths, nil
}

type embeddedWorktreeState uint8

const (
	embeddedWorktreeAbsent embeddedWorktreeState = iota + 1
	embeddedWorktreeOrdinary
	embeddedWorktreeRepository
)

func (service *Service) embeddedWorktreeRepositoryRoot(
	ctx context.Context,
	repository RepositoryPath,
	environment map[string]string,
	root *os.Root,
	path []byte,
	checked map[string]embeddedWorktreeState,
) ([]byte, bool, error) {
	fullPath := string(path)
	for end := 0; end <= len(path); end++ {
		if end != len(path) && path[end] != '/' {
			continue
		}
		candidate := path[:end]
		if len(candidate) == 0 {
			continue
		}
		key := fullPath[:end]
		if state, exists := checked[key]; exists {
			if state == embeddedWorktreeRepository {
				return bytes.Clone(candidate), true, nil
			}
			continue
		}
		info, err := root.Lstat(key)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOTDIR) {
				checked[key] = embeddedWorktreeAbsent
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("inspect worktree path %q: %w", candidate, err)
		}
		if !info.IsDir() {
			checked[key] = embeddedWorktreeAbsent
			return nil, false, nil
		}
		_, err = root.Lstat(key + "/.git")
		switch {
		case err == nil:
			valid, validateErr := service.isEmbeddedWorktreeRepository(
				ctx,
				repository,
				environment,
				candidate,
			)
			if validateErr != nil {
				return nil, false, validateErr
			}
			if valid {
				checked[key] = embeddedWorktreeRepository
				return bytes.Clone(candidate), true, nil
			}
			checked[key] = embeddedWorktreeOrdinary
		case errors.Is(err, os.ErrNotExist), errors.Is(err, unix.ENOTDIR):
			checked[key] = embeddedWorktreeAbsent
		default:
			return nil, false, fmt.Errorf("inspect embedded repository %q: %w", candidate, err)
		}
	}
	return nil, false, nil
}

func (service *Service) isEmbeddedWorktreeRepository(
	ctx context.Context,
	repository RepositoryPath,
	environment map[string]string,
	path []byte,
) (bool, error) {
	directory := filepath.Join(repository.String(), string(path))
	result, err := service.runGit(
		ctx,
		directory,
		environment,
		[]string{"rev-parse", "--is-inside-work-tree"},
		[]int{0, 1, 128},
		32,
	)
	if err != nil {
		return false, fmt.Errorf("validate embedded repository %q: %w", path, err)
	}
	if result.ExitCode != 0 {
		return false, nil
	}
	return string(result.Stdout) == "true\n", nil
}

func uniqueWorktreeRepositoryRoots(roots [][]byte) ([][]byte, map[string][]byte) {
	slices.SortFunc(roots, bytes.Compare)
	unique := roots[:0]
	rootSet := make(map[string][]byte, len(roots))
	for _, candidate := range roots {
		if _, exists := containingWorktreeRepository(candidate, rootSet); exists {
			continue
		}
		owned := bytes.Clone(candidate)
		unique = append(unique, owned)
		rootSet[string(owned)] = owned
	}
	return unique, rootSet
}

func containingWorktreeRepository(
	path []byte,
	roots map[string][]byte,
) ([]byte, bool) {
	for end := 0; end <= len(path); end++ {
		if end != len(path) && path[end] != '/' {
			continue
		}
		if root, exists := roots[string(path[:end])]; exists {
			return root, true
		}
	}
	return nil, false
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
	root RepositoryPath,
	environment map[string]string,
	paths [][]byte,
) error {
	names := worktreeFilterNames{}
	for start := 0; start < len(paths); {
		input := make([]byte, 0, indexUpdateBatchBytes)
		end := start
		for end < len(paths) &&
			len(input)+len(paths[end])+1 <= indexUpdateBatchBytes {
			input = append(input, paths[end]...)
			input = append(input, 0)
			end++
		}
		if end == start {
			return errors.New("worktree filter path exceeds batch capacity")
		}
		outputLimit, err := worktreeFilterOutputLimit(len(input), end-start)
		if err != nil {
			return err
		}
		result, err := service.runGitWithInput(
			ctx,
			root.String(),
			environment,
			[]string{"check-attr", "-z", gitStdin, "filter"},
			input,
			nil,
			outputLimit,
		)
		if err != nil {
			return fmt.Errorf("inspect Git filter attributes: %w", err)
		}
		batchNames, err := validateFilterAttributeOutput(result.Stdout, paths[start:end])
		if err != nil {
			return err
		}
		names.bits |= batchNames.bits
		start = end
	}
	return service.rejectConfiguredAmbiguousFilterDrivers(ctx, root, environment, paths, names)
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
	paths [][]byte,
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
		if !bytes.Equal(actualPath, path) || !bytes.Equal(attribute, []byte("filter")) {
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
	root RepositoryPath,
	environment map[string]string,
	paths [][]byte,
	names worktreeFilterNames,
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
	result, err := service.runGit(
		ctx,
		root.String(),
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
	configured, err := parseConfiguredAmbiguousFilterNames(result.Stdout)
	if err != nil {
		return err
	}
	return service.rejectExplicitAmbiguousFilterAttributes(
		ctx,
		root,
		environment,
		paths,
		configured,
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
	root RepositoryPath,
	environment map[string]string,
	paths [][]byte,
	configured worktreeFilterNames,
) error {
	for start := 0; start < len(paths); {
		input := make([]byte, 0, indexUpdateBatchBytes)
		end := start
		for end < len(paths) && len(input)+len(paths[end])+1 <= indexUpdateBatchBytes {
			input = append(input, paths[end]...)
			input = append(input, 0)
			end++
		}
		if end == start {
			return errors.New("worktree filter path exceeds batch capacity")
		}
		result, err := service.runGitWithInput(
			ctx,
			root.String(),
			environment,
			[]string{"check-attr", "-z", "--all", gitStdin},
			input,
			nil,
			indexUpdateBatchBytes,
		)
		if err != nil {
			return fmt.Errorf("disambiguate Git filter attributes: %w", err)
		}
		if err := validateExplicitAmbiguousFilterAttributes(
			result.Stdout,
			paths[start:end],
			configured,
		); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func validateExplicitAmbiguousFilterAttributes(
	output []byte,
	paths [][]byte,
	configured worktreeFilterNames,
) error {
	expected := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		expected[string(path)] = struct{}{}
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

func (service *Service) hashRawWorktreeEntries(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	workspace string,
	entries []rawWorktreeEntry,
) (result []rawWorktreeEntry, returnErr error) {
	staging := filepath.Join(workspace, "raw-blobs")
	if strings.ContainsAny(staging, "\r\n") {
		return nil, errors.New("raw blob staging path contains a line break")
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return nil, fmt.Errorf("create raw blob staging directory: %w", err)
	}
	stagingRoot, err := os.OpenRoot(staging)
	if err != nil {
		return nil, fmt.Errorf("open raw blob staging directory: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, stagingRoot.Close())
	}()
	batch := rawWorktreeBatch{}
	copyBuffer := make([]byte, rawWorktreeCopyBufferBytes)
	defer func() {
		returnErr = errors.Join(returnErr, cleanupStagedWorktreeBlobs(batch.blobs))
	}()
	for index := range entries {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		capture, err := openRawWorktreePath(
			filepath.Join(root.String(), string(entries[index].path)),
			entries[index].mode,
		)
		if err != nil {
			return nil, fmt.Errorf("read worktree path %q: %w", entries[index].path, err)
		}
		entries[index].mode = capture.mode
		if capture.kind == rawWorktreeGitlink {
			if closeErr := capture.close(); closeErr != nil {
				return nil, fmt.Errorf("close worktree path %q: %w", entries[index].path, closeErr)
			}
			continue
		}
		name := "blob-" + strconv.Itoa(index)
		filePath := filepath.Join(staging, name)
		inputBytes := len(filePath) + 1
		if inputBytes > rawWorktreeHashInputBytes {
			return nil, errors.Join(
				errors.New("raw blob staging path exceeds hash input batch capacity"),
				capture.close(),
			)
		}
		if !batch.accepts(capture.size, inputBytes) {
			if flushErr := service.flushRawWorktreeBatch(
				ctx,
				root,
				environment,
				entries,
				&batch,
			); flushErr != nil {
				return nil, errors.Join(flushErr, capture.close())
			}
		}
		file, err := stagingRoot.OpenFile(
			name,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("create raw blob staging file: %w", err),
				capture.close(),
			)
		}
		batch.add(stagedWorktreeBlob{
			file:       filePath,
			mode:       capture.mode,
			entryIndex: index,
			size:       capture.size,
		})
		written, writeErr := capture.write(ctx, file, copyBuffer)
		closeErr := file.Close()
		verifyErr := capture.verify(written)
		captureCloseErr := capture.close()
		if err := errors.Join(writeErr, closeErr, verifyErr, captureCloseErr); err != nil {
			return nil, fmt.Errorf("read worktree path %q: %w", entries[index].path, err)
		}
	}
	if err := service.flushRawWorktreeBatch(
		ctx,
		root,
		environment,
		entries,
		&batch,
	); err != nil {
		return nil, err
	}
	return entries, nil
}

func (batch *rawWorktreeBatch) accepts(rawBytes int64, inputBytes int) bool {
	if len(batch.blobs) == 0 {
		return inputBytes <= rawWorktreeHashInputBytes
	}
	return len(batch.blobs) < rawWorktreeBatchEntries &&
		batch.rawBytes <= rawWorktreeBatchBytes-rawBytes &&
		batch.inputBytes <= rawWorktreeHashInputBytes-inputBytes
}

func (batch *rawWorktreeBatch) add(blob stagedWorktreeBlob) {
	batch.blobs = append(batch.blobs, blob)
	batch.rawBytes += blob.size
	batch.inputBytes += len(blob.file) + 1
}

func (service *Service) flushRawWorktreeBatch(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	entries []rawWorktreeEntry,
	batch *rawWorktreeBatch,
) (returnErr error) {
	if len(batch.blobs) == 0 {
		return nil
	}
	defer func() {
		returnErr = errors.Join(returnErr, cleanupStagedWorktreeBlobs(batch.blobs))
		*batch = rawWorktreeBatch{}
	}()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	input := make([]byte, 0, batch.inputBytes)
	for _, blob := range batch.blobs {
		input = append(input, blob.file...)
		input = append(input, '\n')
	}
	result, err := service.runGitWithInput(
		ctx,
		root.String(),
		environment,
		[]string{"hash-object", "--no-filters", "-w", "--stdin-paths"},
		input,
		nil,
		int64(len(batch.blobs))*80,
	)
	if err != nil {
		return fmt.Errorf("hash raw worktree blobs: %w", err)
	}
	identities, err := parseRawWorktreeIdentities(result.Stdout, len(batch.blobs))
	if err != nil {
		return err
	}
	for index, identity := range identities {
		blob := batch.blobs[index]
		entry := &entries[blob.entryIndex]
		entry.oid = new(identity)
		if entry.mode != blob.mode {
			return fmt.Errorf("raw worktree mode changed while hashing path %q", entry.path)
		}
	}
	return nil
}

func cleanupStagedWorktreeBlobs(blobs []stagedWorktreeBlob) error {
	var cleanupErr error
	for _, blob := range blobs {
		if err := os.Remove(blob.file); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove raw blob staging file: %w", err))
		}
	}
	return cleanupErr
}

func parseRawWorktreeIdentities(output []byte, count int) ([]review.ObjectID, error) {
	identities := make([]review.ObjectID, 0, count)
	for index := range count {
		lineEnd := bytes.IndexByte(output, '\n')
		if lineEnd < 0 {
			return nil, fmt.Errorf(
				"git returned %d raw blob identities, want %d",
				index,
				count,
			)
		}
		identity, err := review.NewObjectID(string(output[:lineEnd]))
		if err != nil {
			return nil, fmt.Errorf("validate raw blob identity %d: %w", index+1, err)
		}
		identities = append(identities, identity)
		output = output[lineEnd+1:]
	}
	if len(output) != 0 {
		return nil, fmt.Errorf("git returned extra raw blob identities after %d", count)
	}
	return identities, nil
}

func openRawWorktreePath(
	path string,
	indexMode review.FileMode,
) (rawWorktreeCapture, error) {
	return openRawWorktreePathWith(path, indexMode, openRawWorktreeRegular)
}

func openRawWorktreePathWith(
	path string,
	indexMode review.FileMode,
	openRegular func(string) (*os.File, error),
) (rawWorktreeCapture, error) {
	before, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rawWorktreeCapture{}, errors.New("worktree path disappeared")
		}
		return rawWorktreeCapture{}, err
	}
	switch {
	case before.Mode().IsRegular():
		if before.Size() > maxWorktreeFileBytes {
			return rawWorktreeCapture{}, errors.New("worktree file exceeds raw byte limit")
		}
		file, err := openRegular(path)
		if err != nil {
			return rawWorktreeCapture{}, err
		}
		opened, err := file.Stat()
		if err != nil {
			return rawWorktreeCapture{}, errors.Join(err, file.Close())
		}
		if !opened.Mode().IsRegular() || !sameWorktreeFile(before, opened) {
			return rawWorktreeCapture{}, errors.Join(
				errors.New("worktree path changed while opening"),
				file.Close(),
			)
		}
		if opened.Size() > maxWorktreeFileBytes {
			return rawWorktreeCapture{}, errors.Join(
				errors.New("worktree file exceeds raw byte limit"),
				file.Close(),
			)
		}
		mode := "100644"
		if opened.Mode()&0o111 != 0 {
			mode = "100755"
		}
		if indexMode.String() == "100644" || indexMode.String() == "100755" {
			mode = indexMode.String()
		}
		validated, err := review.NewFileMode(mode)
		if err != nil {
			return rawWorktreeCapture{}, errors.Join(err, file.Close())
		}
		return rawWorktreeCapture{
			file: file,
			info: opened,
			path: path,
			mode: validated,
			size: opened.Size(),
			kind: rawWorktreeRegular,
		}, nil
	case before.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(path)
		if err != nil {
			return rawWorktreeCapture{}, err
		}
		mode, err := review.NewFileMode("120000")
		if err != nil {
			return rawWorktreeCapture{}, err
		}
		return rawWorktreeCapture{
			info: before,
			path: path,
			link: link,
			mode: mode,
			size: int64(len(link)),
			kind: rawWorktreeSymlink,
		}, nil
	case before.IsDir() && indexMode.String() == "160000":
		return rawWorktreeCapture{
			info: before,
			path: path,
			mode: indexMode,
			kind: rawWorktreeGitlink,
		}, nil
	default:
		return rawWorktreeCapture{}, errors.New("worktree path has an unsupported file type")
	}
}

func openRawWorktreeRegular(path string) (*os.File, error) {
	descriptor, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func (capture rawWorktreeCapture) write(
	ctx context.Context,
	destination io.Writer,
	buffer []byte,
) (int64, error) {
	switch capture.kind {
	case rawWorktreeRegular:
		return copyBoundedWorktreeFile(
			ctx,
			destination,
			capture.file,
			buffer,
			maxWorktreeFileBytes,
		)
	case rawWorktreeSymlink:
		if err := context.Cause(ctx); err != nil {
			return 0, err
		}
		written, err := io.WriteString(destination, capture.link)
		if err == nil && written != len(capture.link) {
			err = io.ErrShortWrite
		}
		return int64(written), err
	default:
		return 0, errors.New("worktree capture cannot be staged")
	}
}

func (capture rawWorktreeCapture) verify(written int64) error {
	if written != capture.size {
		return errors.New("worktree path changed while reading")
	}
	current, err := os.Lstat(capture.path)
	if err != nil {
		return errors.New("worktree path changed while reading")
	}
	switch capture.kind {
	case rawWorktreeRegular:
		after, err := capture.file.Stat()
		if err != nil || !sameWorktreeFile(capture.info, after) ||
			!sameWorktreeFile(capture.info, current) {
			return errors.New("worktree file changed while reading")
		}
	case rawWorktreeSymlink:
		if !sameWorktreeFile(capture.info, current) {
			return errors.New("worktree symlink changed while reading")
		}
	default:
		return errors.New("worktree capture cannot be verified")
	}
	return nil
}

func (capture rawWorktreeCapture) close() error {
	if capture.file == nil {
		return nil
	}
	return capture.file.Close()
}

func copyBoundedWorktreeFile(
	ctx context.Context,
	destination io.Writer,
	source io.Reader,
	buffer []byte,
	limit int64,
) (int64, error) {
	if len(buffer) == 0 || limit < 0 {
		return 0, errors.New("worktree copy has an invalid bound")
	}
	var written int64
	emptyReads := 0
	for written <= limit {
		if err := context.Cause(ctx); err != nil {
			return written, err
		}
		remaining := limit - written + 1
		readBuffer := buffer
		if int64(len(readBuffer)) > remaining {
			readBuffer = readBuffer[:remaining]
		}
		count, readErr := source.Read(readBuffer)
		if count < 0 || count > len(readBuffer) {
			return written, errors.New("worktree file reader returned an invalid count")
		}
		if count > 0 {
			emptyReads = 0
			if written+int64(count) > limit {
				return written, errors.New("worktree file exceeds raw byte limit")
			}
			countWritten, writeErr := destination.Write(readBuffer[:count])
			written += int64(countWritten)
			if writeErr != nil {
				return written, writeErr
			}
			if countWritten != count {
				return written, io.ErrShortWrite
			}
		} else {
			emptyReads++
			if emptyReads >= 100 {
				return written, io.ErrNoProgress
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
	return written, errors.New("worktree file exceeds raw byte limit")
}

func sameWorktreeFile(left, right os.FileInfo) bool {
	return right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() &&
		left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime())
}

func rawWorktreeUpdates(entries []rawWorktreeEntry) ([]indexUpdate, error) {
	updates := make([]indexUpdate, 0, len(entries))
	for _, entry := range entries {
		if entry.oid == nil {
			return nil, fmt.Errorf("worktree path %q has no object identity", entry.path)
		}
		updates = append(updates, indexUpdate{
			path:   entry.path,
			mode:   entry.mode,
			object: *entry.oid,
		})
	}
	return updates, nil
}

func (service *Service) resolveRawWorktreeGitlinks(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	entries []rawWorktreeEntry,
) ([]rawWorktreeEntry, error) {
	for index := range entries {
		if entries[index].mode.String() != "160000" {
			continue
		}
		oid, err := service.worktreeGitlinkHead(
			ctx,
			root,
			environment,
			entries[index].path,
		)
		if err != nil {
			return nil, err
		}
		entries[index].oid = new(oid)
	}
	return entries, nil
}

func (service *Service) verifyRawWorktreeGitlinks(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	entries []rawWorktreeEntry,
) error {
	for _, entry := range entries {
		if entry.mode.String() != "160000" {
			continue
		}
		if entry.oid == nil {
			return fmt.Errorf("submodule %q has no object identity", entry.path)
		}
		current, err := service.worktreeGitlinkHead(ctx, root, environment, entry.path)
		if err != nil {
			return err
		}
		if current != *entry.oid {
			return fmt.Errorf("submodule %q changed while snapshotting worktree", entry.path)
		}
	}
	return nil
}

func (service *Service) worktreeGitlinkHead(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	path []byte,
) (review.ObjectID, error) {
	directory := filepath.Join(root.String(), string(path))
	gitlinkEnvironment := cloneEnvironment(environment)
	delete(gitlinkEnvironment, "GIT_OBJECT_DIRECTORY")
	delete(gitlinkEnvironment, "GIT_ALTERNATE_OBJECT_DIRECTORIES")
	delete(gitlinkEnvironment, "GIT_INDEX_FILE")
	result, err := service.runGit(
		ctx,
		directory,
		gitlinkEnvironment,
		[]string{gitRevParse, gitVerify, "HEAD^{commit}"},
		nil,
		128,
	)
	if err != nil {
		return "", fmt.Errorf(
			"worktree gitlink %q HEAD does not resolve to a commit: %w",
			path,
			err,
		)
	}
	value := strings.TrimSuffix(string(result.Stdout), "\n")
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("submodule %q returned multiple HEAD records", path)
	}
	oid, err := review.NewObjectID(value)
	if err != nil {
		return "", fmt.Errorf("validate worktree gitlink %q HEAD: %w", path, err)
	}
	return oid, nil
}

func (service *Service) applyIndexUpdates(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	base review.ObjectID,
	updates []indexUpdate,
) error {
	zero := strings.Repeat("0", len(base.String()))
	for start := 0; start < len(updates); {
		input := make([]byte, 0, indexUpdateBatchBytes)
		end := start
		for end < len(updates) {
			update := updates[end]
			record := make([]byte, 0, len(update.path)+96)
			if update.remove {
				record = append(record, "0 "...)
				record = append(record, zero...)
			} else {
				record = append(record, update.mode.String()...)
				record = append(record, ' ')
				record = append(record, update.object.String()...)
			}
			record = append(record, '\t')
			record = append(record, update.path...)
			record = append(record, 0)
			if len(input)+len(record) > indexUpdateBatchBytes && len(input) != 0 {
				break
			}
			input = append(input, record...)
			end++
		}
		if _, err := service.runGitWithInput(
			ctx,
			root.String(),
			environment,
			[]string{gitUpdateIndex, "-z", gitIndexInfo},
			input,
			nil,
			64<<10,
		); err != nil {
			return fmt.Errorf("update private worktree index: %w", err)
		}
		start = end
	}
	return nil
}
