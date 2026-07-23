package source

import (
	"context"
	"fmt"

	"github.com/jacoelho/rvw/internal/review"
)

const (
	worktreeRadixBuckets     = 257
	worktreeRadixStackCharge = 24
	worktreeStringSlotCharge = 16
	worktreeUpdateSlotCharge = 64
)

type worktreeRadixTask struct {
	start int
	end   int
	depth int
}

func collapseWorktreeRepositoryEntries(
	ctx context.Context,
	ledger *sourceLedger,
	raw []rawWorktreeEntry,
	removals []indexUpdate,
	paths []string,
	uniqueRoots []string,
	rootSet map[string]string,
) ([]rawWorktreeEntry, []indexUpdate, []string, error) {
	removalPaths := make(map[string]struct{}, len(removals))
	for index, removal := range removals {
		if err := checkWorktreeCancellation(ctx, index); err != nil {
			return nil, nil, nil, err
		}
		removalPaths[removal.path] = struct{}{}
	}
	collapsedRaw := make([]rawWorktreeEntry, 0, len(raw)+len(uniqueRoots))
	rootPresent := make(map[string]struct{}, len(uniqueRoots))
	for index, entry := range raw {
		if err := checkWorktreeCancellation(ctx, index); err != nil {
			return nil, nil, nil, err
		}
		rootPath, under := containingWorktreeRepository(entry.path, rootSet)
		if !under {
			collapsedRaw = append(collapsedRaw, entry)
			continue
		}
		if entry.path == rootPath {
			if _, exists := rootPresent[rootPath]; !exists {
				collapsedRaw = append(collapsedRaw, rawWorktreeEntry{
					path: rootPath,
					mode: review.FileMode("160000"),
				})
				rootPresent[rootPath] = struct{}{}
			}
			continue
		}
		if _, exists := removalPaths[entry.path]; !exists {
			removals = append(removals, indexUpdate{path: entry.path, remove: true})
			removalPaths[entry.path] = struct{}{}
		}
	}
	for index, rootPath := range uniqueRoots {
		if err := checkWorktreeCancellation(ctx, index); err != nil {
			return nil, nil, nil, err
		}
		if _, exists := rootPresent[rootPath]; exists {
			continue
		}
		collapsedRaw = append(collapsedRaw, rawWorktreeEntry{
			path: rootPath,
			mode: review.FileMode("160000"),
		})
	}

	collapsedPaths := make([]string, 0, len(paths)+len(uniqueRoots))
	seenPaths := make(map[string]struct{}, len(paths)+len(uniqueRoots))
	for index, path := range paths {
		if err := checkWorktreeCancellation(ctx, index); err != nil {
			return nil, nil, nil, err
		}
		if _, under := containingWorktreeRepository(path, rootSet); under {
			continue
		}
		if _, exists := seenPaths[path]; !exists {
			collapsedPaths = append(collapsedPaths, path)
			seenPaths[path] = struct{}{}
		}
	}
	for index, rootPath := range uniqueRoots {
		if err := checkWorktreeCancellation(ctx, index); err != nil {
			return nil, nil, nil, err
		}
		if _, exists := seenPaths[rootPath]; !exists {
			collapsedPaths = append(collapsedPaths, rootPath)
			seenPaths[rootPath] = struct{}{}
		}
	}
	if len(seenPaths) > review.MaxFiles {
		return nil, nil, nil, fmt.Errorf("worktree paths exceed %d", review.MaxFiles)
	}
	if err := sortWorktreeStrings(ctx, ledger, collapsedPaths); err != nil {
		return nil, nil, nil, err
	}
	return collapsedRaw, removals, collapsedPaths, nil
}

func uniqueSortedWorktreeRepositoryRoots(roots []string) ([]string, map[string]string) {
	unique := roots[:0]
	rootSet := make(map[string]string, len(roots))
	for _, candidate := range roots {
		if _, exists := containingWorktreeRepository(candidate, rootSet); exists {
			continue
		}
		unique = append(unique, candidate)
		rootSet[candidate] = candidate
	}
	return unique, rootSet
}

func containingWorktreeRepository(
	path string,
	roots map[string]string,
) (string, bool) {
	for end := 0; end <= len(path); end++ {
		if end != len(path) && path[end] != '/' {
			continue
		}
		if root, exists := roots[path[:end]]; exists {
			return root, true
		}
	}
	return "", false
}

func checkWorktreeCancellation(ctx context.Context, index int) error {
	if index%2_048 != 0 {
		return nil
	}
	return context.Cause(ctx)
}

func sortWorktreeStrings(
	ctx context.Context,
	ledger *sourceLedger,
	values []string,
) error {
	if len(values) < 2 {
		return context.Cause(ctx)
	}
	scratchBytes := int64(len(values)) *
		(worktreeStringSlotCharge + worktreeRadixStackCharge)
	if err := ledger.reserve(scratchBytes); err != nil {
		return err
	}
	defer ledger.release(scratchBytes)
	scratch := make([]string, len(values))
	stack := make([]worktreeRadixTask, 0, len(values))
	stack = append(stack, worktreeRadixTask{end: len(values)})
	return radixWorktreeStrings(ctx, values, scratch, stack)
}

func radixWorktreeStrings(
	ctx context.Context,
	values []string,
	scratch []string,
	stack []worktreeRadixTask,
) error {
	inspected := 0
	for len(stack) > 0 {
		task := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		var counts [worktreeRadixBuckets]int
		for index := task.start; index < task.end; index++ {
			counts[worktreeRadixSymbol(values[index], task.depth)]++
			inspected++
			if inspected%2_048 == 0 {
				if err := context.Cause(ctx); err != nil {
					return err
				}
			}
		}
		var starts [worktreeRadixBuckets + 1]int
		starts[0] = task.start
		for bucket := range worktreeRadixBuckets {
			starts[bucket+1] = starts[bucket] + counts[bucket]
		}
		positions := starts
		for index := task.start; index < task.end; index++ {
			bucket := worktreeRadixSymbol(values[index], task.depth)
			scratch[positions[bucket]] = values[index]
			positions[bucket]++
		}
		copy(values[task.start:task.end], scratch[task.start:task.end])
		for bucket := 1; bucket < worktreeRadixBuckets; bucket++ {
			start, end := starts[bucket], starts[bucket+1]
			if end-start > 1 {
				stack = append(stack, worktreeRadixTask{
					start: start,
					end:   end,
					depth: task.depth + 1,
				})
			}
		}
	}
	return context.Cause(ctx)
}

func worktreeRadixSymbol(value string, depth int) int {
	if depth >= len(value) {
		return 0
	}
	return int(value[depth]) + 1
}

func sortWorktreeUpdates(
	ctx context.Context,
	ledger *sourceLedger,
	updates []indexUpdate,
) error {
	if len(updates) < 2 {
		return context.Cause(ctx)
	}
	scratchBytes := int64(len(updates)) *
		(worktreeUpdateSlotCharge + worktreeRadixStackCharge)
	if err := ledger.reserve(scratchBytes); err != nil {
		return err
	}
	defer ledger.release(scratchBytes)
	scratch := make([]indexUpdate, len(updates))
	removals := 0
	for _, update := range updates {
		if update.remove {
			scratch[removals] = update
			removals++
		}
	}
	addition := removals
	for _, update := range updates {
		if !update.remove {
			scratch[addition] = update
			addition++
		}
	}
	copy(updates, scratch)
	stack := make([]worktreeRadixTask, 0, len(updates))
	if removals > 1 {
		stack = append(stack, worktreeRadixTask{end: removals})
	}
	if len(updates)-removals > 1 {
		stack = append(stack, worktreeRadixTask{start: removals, end: len(updates)})
	}
	return radixWorktreeUpdates(ctx, updates, scratch, stack)
}

func radixWorktreeUpdates(
	ctx context.Context,
	updates []indexUpdate,
	scratch []indexUpdate,
	stack []worktreeRadixTask,
) error {
	inspected := 0
	for len(stack) > 0 {
		task := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		var counts [worktreeRadixBuckets]int
		for index := task.start; index < task.end; index++ {
			counts[worktreeRadixSymbol(updates[index].path, task.depth)]++
			inspected++
			if inspected%2_048 == 0 {
				if err := context.Cause(ctx); err != nil {
					return err
				}
			}
		}
		var starts [worktreeRadixBuckets + 1]int
		starts[0] = task.start
		for bucket := range worktreeRadixBuckets {
			starts[bucket+1] = starts[bucket] + counts[bucket]
		}
		positions := starts
		for index := task.start; index < task.end; index++ {
			bucket := worktreeRadixSymbol(updates[index].path, task.depth)
			scratch[positions[bucket]] = updates[index]
			positions[bucket]++
		}
		copy(updates[task.start:task.end], scratch[task.start:task.end])
		for bucket := 1; bucket < worktreeRadixBuckets; bucket++ {
			start, end := starts[bucket], starts[bucket+1]
			if end-start > 1 {
				stack = append(stack, worktreeRadixTask{
					start: start,
					end:   end,
					depth: task.depth + 1,
				})
			}
		}
	}
	return context.Cause(ctx)
}
