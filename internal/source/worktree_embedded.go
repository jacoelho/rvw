package source

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const (
	embeddedWorktreeQuantumRecords = 2_048
	embeddedWorktreeQuantumBytes   = 16 << 10
	embeddedWorktreeStateBytes     = 64
)

type embeddedWorktreeState uint8

const (
	embeddedWorktreeAbsent embeddedWorktreeState = iota + 1
	embeddedWorktreeOrdinary
	embeddedWorktreeRepository
	embeddedWorktreeTerminal
)

type embeddedWorktreeProbe func(context.Context, string) (embeddedWorktreeState, error)

type embeddedWorktreeCursor struct {
	path string
	next int
	done bool
}

type embeddedWorktreeProbeRequest struct {
	path  string
	state embeddedWorktreeState
}

type embeddedWorktreeDiscovery struct {
	checked  map[string]embeddedWorktreeState
	paths    []string
	cursors  []embeddedWorktreeCursor
	roots    []string
	nextPath int
	scan     int
}

func (service *Service) collapseEmbeddedWorktreeRepositories(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	ledger *sourceLedger,
	raw []rawWorktreeEntry,
	removals []indexUpdate,
	paths []string,
	runCPU CPUAdmission,
) (
	resultRaw []rawWorktreeEntry,
	resultRemovals []indexUpdate,
	resultPaths []string,
	returnErr error,
) {
	if runCPU == nil {
		return nil, nil, nil, errors.New("embedded worktree CPU admission is nil")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, nil, nil, err
	}
	checkpoint := ledger.checkpoint()
	defer checkpoint.rollback()

	roots := make([]string, 0)
	for start := 0; start < len(raw); {
		var next int
		if err := runCPU(ctx, func() error {
			var scanErr error
			next, scanErr = appendExplicitWorktreeRoots(ctx, raw, start)
			if scanErr == nil {
				for _, entry := range raw[start:next] {
					if entry.mode.String() == "160000" {
						roots = append(roots, entry.path)
					}
				}
			}
			return scanErr
		}); err != nil {
			return nil, nil, nil, err
		}
		start = next
	}

	var embeddedEnvironment map[string]string
	if err := runCPU(ctx, func() error {
		embeddedEnvironment = sanitizedEmbeddedWorktreeEnvironment(environment)
		return context.Cause(ctx)
	}); err != nil {
		return nil, nil, nil, err
	}
	discovered, err := discoverEmbeddedWorktreeRepositories(
		ctx,
		paths,
		ledger,
		runCPU,
		func(ctx context.Context, path string) (embeddedWorktreeState, error) {
			return service.probeEmbeddedWorktreeRepository(
				ctx, worktree, embeddedEnvironment, path,
			)
		},
	)
	if err != nil {
		return nil, nil, nil, err
	}
	roots = append(roots, discovered...)
	if len(roots) == 0 {
		return raw, removals, paths, nil
	}
	if err := runCPU(ctx, func() error {
		if sortErr := sortWorktreeStrings(ctx, ledger, roots); sortErr != nil {
			return sortErr
		}
		uniqueRoots, rootSet := uniqueSortedWorktreeRepositoryRoots(roots)
		var collapseErr error
		resultRaw, resultRemovals, resultPaths, collapseErr = collapseWorktreeRepositoryEntries(
			ctx,
			ledger,
			raw,
			removals,
			paths,
			uniqueRoots,
			rootSet,
		)
		return collapseErr
	}); err != nil {
		return nil, nil, nil, err
	}
	return resultRaw, resultRemovals, resultPaths, nil
}

func appendExplicitWorktreeRoots(
	ctx context.Context,
	raw []rawWorktreeEntry,
	start int,
) (int, error) {
	if start < 0 || start >= len(raw) {
		return start, errors.New("embedded worktree root scan start is invalid")
	}
	if err := context.Cause(ctx); err != nil {
		return start, err
	}
	records := 0
	pathBytes := 0
	end := start
	for end < len(raw) && records < embeddedWorktreeQuantumRecords {
		entryBytes := len(raw[end].path)
		if records > 0 && entryBytes > embeddedWorktreeQuantumBytes-pathBytes {
			break
		}
		if entryBytes > embeddedWorktreeQuantumBytes {
			return start, errors.New("embedded worktree root path exceeds quantum")
		}
		pathBytes += entryBytes
		records++
		end++
	}
	if end == start {
		return start, errors.New("embedded worktree root scan made no progress")
	}
	return end, context.Cause(ctx)
}

func discoverEmbeddedWorktreeRepositories(
	ctx context.Context,
	paths []string,
	ledger *sourceLedger,
	runCPU CPUAdmission,
	probe embeddedWorktreeProbe,
) ([]string, error) {
	if runCPU == nil || probe == nil || ledger == nil {
		return nil, errors.New("embedded worktree discovery is incomplete")
	}
	discovery := embeddedWorktreeDiscovery{
		checked: make(map[string]embeddedWorktreeState),
		paths:   paths,
	}
	for !discovery.done() {
		if len(discovery.cursors) == 0 {
			if err := runCPU(ctx, func() error {
				return discovery.loadWindow(ctx)
			}); err != nil {
				return nil, err
			}
			continue
		}
		var requests []embeddedWorktreeProbeRequest
		if err := runCPU(ctx, func() error {
			var prepareErr error
			requests, prepareErr = discovery.prepareProbes(ctx)
			return prepareErr
		}); err != nil {
			return nil, err
		}
		if len(requests) == 0 {
			continue
		}
		for index := range requests {
			if err := context.Cause(ctx); err != nil {
				return nil, err
			}
			state, err := probe(ctx, requests[index].path)
			if err != nil {
				return nil, err
			}
			requests[index].state = state
		}
		if err := runCPU(ctx, func() error {
			return discovery.installProbeResults(ctx, ledger, requests)
		}); err != nil {
			return nil, err
		}
	}
	return discovery.roots, nil
}

func (discovery *embeddedWorktreeDiscovery) done() bool {
	return discovery.nextPath == len(discovery.paths) && len(discovery.cursors) == 0
}

func (discovery *embeddedWorktreeDiscovery) loadWindow(ctx context.Context) error {
	if len(discovery.cursors) != 0 || discovery.nextPath >= len(discovery.paths) {
		return context.Cause(ctx)
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	start := discovery.nextPath
	pathBytes := 0
	for discovery.nextPath < len(discovery.paths) &&
		discovery.nextPath-start < embeddedWorktreeQuantumRecords {
		path := discovery.paths[discovery.nextPath]
		if discovery.nextPath > start && len(path) > embeddedWorktreeQuantumBytes-pathBytes {
			break
		}
		if len(path) > embeddedWorktreeQuantumBytes {
			return errors.New("embedded worktree path exceeds discovery quantum")
		}
		discovery.nextPath++
		pathBytes += len(path)
	}
	if discovery.nextPath == start {
		return errors.New("embedded worktree discovery made no path progress")
	}
	discovery.cursors = make([]embeddedWorktreeCursor, discovery.nextPath-start)
	for index, path := range discovery.paths[start:discovery.nextPath] {
		discovery.cursors[index].path = path
	}
	return context.Cause(ctx)
}

func (discovery *embeddedWorktreeDiscovery) prepareProbes(
	ctx context.Context,
) ([]embeddedWorktreeProbeRequest, error) {
	if len(discovery.cursors) == 0 {
		return nil, errors.New("embedded worktree discovery has no active window")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	requests := make(
		[]embeddedWorktreeProbeRequest,
		0,
		min(embeddedWorktreeQuantumRecords, len(discovery.cursors)),
	)
	records := 0
	componentBytes := 0
	for discovery.scan < len(discovery.cursors) && records < embeddedWorktreeQuantumRecords {
		cursor := &discovery.cursors[discovery.scan]
		if cursor.done {
			discovery.scan++
			continue
		}
		component, next, ok := nextEmbeddedWorktreeComponent(cursor.path, cursor.next)
		if !ok {
			cursor.done = true
			discovery.scan++
			continue
		}
		if records > 0 && len(component) > embeddedWorktreeQuantumBytes-componentBytes {
			break
		}
		componentBytes += len(component)
		records++
		state, exists := discovery.checked[component]
		if !exists {
			if len(requests) == 0 || requests[len(requests)-1].path != component {
				requests = append(requests, embeddedWorktreeProbeRequest{path: component})
			}
			discovery.scan++
			continue
		}
		cursor.next = next
		switch state {
		case embeddedWorktreeAbsent, embeddedWorktreeOrdinary:
		case embeddedWorktreeRepository:
			discovery.roots = append(discovery.roots, component)
			cursor.done = true
			discovery.scan++
		case embeddedWorktreeTerminal:
			cursor.done = true
			discovery.scan++
		default:
			return nil, errors.New("embedded worktree cache contains an invalid state")
		}
	}
	if discovery.scan == len(discovery.cursors) {
		discovery.scan = 0
		active := discovery.cursors[:0]
		for _, cursor := range discovery.cursors {
			if !cursor.done {
				active = append(active, cursor)
			}
		}
		discovery.cursors = active
	}
	if records == 0 && len(discovery.cursors) != 0 {
		return nil, errors.New("embedded worktree discovery made no component progress")
	}
	return requests, context.Cause(ctx)
}

func nextEmbeddedWorktreeComponent(path string, start int) (string, int, bool) {
	if start < 0 || start > len(path) {
		return "", start, false
	}
	for end := start; end <= len(path); end++ {
		if end != len(path) && path[end] != '/' {
			continue
		}
		if end == 0 {
			continue
		}
		return path[:end], end + 1, true
	}
	return "", len(path) + 1, false
}

func (discovery *embeddedWorktreeDiscovery) installProbeResults(
	ctx context.Context,
	ledger *sourceLedger,
	results []embeddedWorktreeProbeRequest,
) error {
	if len(results) == 0 || len(results) > embeddedWorktreeQuantumRecords {
		return errors.New("embedded worktree probe result batch is invalid")
	}
	pathBytes := 0
	newStates := 0
	seen := make(map[string]embeddedWorktreeState, len(results))
	for _, result := range results {
		if result.state < embeddedWorktreeAbsent || result.state > embeddedWorktreeTerminal {
			return errors.New("embedded worktree probe returned an invalid state")
		}
		if len(result.path) > embeddedWorktreeQuantumBytes-pathBytes {
			return errors.New("embedded worktree probe results exceed byte quantum")
		}
		pathBytes += len(result.path)
		if previous, exists := seen[result.path]; exists {
			if previous != result.state {
				return errors.New("embedded worktree probe changed state within one batch")
			}
			return errors.New("embedded worktree probe result batch repeats a path")
		}
		seen[result.path] = result.state
		if previous, exists := discovery.checked[result.path]; exists {
			if previous != result.state {
				return errors.New("embedded worktree probe changed a cached result")
			}
			continue
		}
		newStates++
	}
	if err := ledger.reserve(int64(newStates) * embeddedWorktreeStateBytes); err != nil {
		return err
	}
	for _, result := range results {
		discovery.checked[result.path] = result.state
	}
	return context.Cause(ctx)
}

func sanitizedEmbeddedWorktreeEnvironment(environment map[string]string) map[string]string {
	result := cloneEnvironment(environment)
	for _, key := range []string{
		gitDirectoryKey,
		gitWorkTreeKey,
		gitObjectDirectoryKey,
		gitAlternateObjectDirectoriesKey,
		gitIndexFileKey,
	} {
		delete(result, key)
	}
	return result
}

func (service *Service) probeEmbeddedWorktreeRepository(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	path string,
) (embeddedWorktreeState, error) {
	info, err := worktree.filesystem.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOTDIR) {
			return embeddedWorktreeTerminal, nil
		}
		return 0, fmt.Errorf("inspect worktree path %q: %w", path, err)
	}
	if !info.IsDir() {
		return embeddedWorktreeTerminal, nil
	}
	_, err = worktree.filesystem.Lstat(path + "/.git")
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist), errors.Is(err, unix.ENOTDIR):
		return embeddedWorktreeAbsent, nil
	default:
		return 0, fmt.Errorf("inspect embedded repository %q: %w", path, err)
	}
	valid, err := service.isEmbeddedWorktreeRepository(ctx, worktree, environment, path)
	if err != nil {
		return 0, err
	}
	if valid {
		return embeddedWorktreeRepository, nil
	}
	return embeddedWorktreeOrdinary, nil
}

func (service *Service) isEmbeddedWorktreeRepository(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	path string,
) (bool, error) {
	result, err := service.runRootedGitAt(
		ctx,
		worktree,
		path,
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
