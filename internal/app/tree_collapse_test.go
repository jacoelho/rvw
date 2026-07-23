package app

import (
	"context"
	"fmt"
	"testing"
	"unsafe"

	"github.com/jacoelho/rvw/internal/review"
)

func TestCollapsedTreeLayoutAndMaximumCharge(t *testing.T) {
	if got := unsafe.Sizeof(collapsedIntervalNode{}); got != collapsedIntervalNodeCharge {
		t.Fatalf("collapsed AVL node size = %d, want %d", got, collapsedIntervalNodeCharge)
	}
	if got := unsafe.Sizeof(collapsedTreeState{}); got != collapsedStateCharge {
		t.Fatalf("collapsed state size = %d, want %d", got, collapsedStateCharge)
	}
	state := collapsedTreeState{
		nodes:     make([]collapsedIntervalNode, 0, 100_000),
		positions: make([]int32, maxFileTreeNodes),
		root:      missingCollapsedPosition,
	}
	const want int64 = 4_000_064
	if got := state.retainedBytes(); got != want {
		t.Fatalf("maximum collapse charge = %d, want %d", got, want)
	}
}

func TestCollapsedTreeReturnsOutermostContainingDirectory(t *testing.T) {
	state := newTestCollapsedTreeState(10, 3)
	for _, interval := range []struct{ index, end int }{
		{index: 2, end: 5},
		{index: 0, end: 10},
		{index: 1, end: 8},
	} {
		if err := state.insert(interval.index, interval.end); err != nil {
			t.Fatal(err)
		}
	}
	assertCollapsedAVL(t, state)
	if got, ok := state.outermostContaining(4); !ok || got != 0 {
		t.Fatalf("outermost collapse for node 4 = %d, %v; want directory 0", got, ok)
	}
	state.remove(0)
	assertCollapsedAVL(t, state)
	if got, ok := state.outermostContaining(4); !ok || got != 1 {
		t.Fatalf("outermost collapse after expanding root = %d, %v; want directory 1", got, ok)
	}
	state.remove(1)
	assertCollapsedAVL(t, state)
	if got, ok := state.outermostContaining(4); !ok || got != 2 {
		t.Fatalf("outermost collapse after expanding parent = %d, %v; want directory 2", got, ok)
	}
}

func TestCollapsedTreeNavigationSkipsOutermostSubtree(t *testing.T) {
	snapshot, files := fileTreeSnapshot(t, []string{
		"a/b/file.go",
		"a/c/excluded.go",
		"z.go",
	})
	index, err := buildWorkspaceIndex(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tree := index.FileTree
	a := tree.directories["a"]
	b := tree.directories["a/b"]
	z := tree.files[files[2]]
	if _, err := index.Collapsed.set(a, tree.nodes[a].subtreeEnd, collapseCollapsed); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Collapsed.set(b, tree.nodes[b].subtreeEnd, collapseCollapsed); err != nil {
		t.Fatal(err)
	}
	if got, ok := tree.previousVisible(z, index.Collapsed); !ok || got != a {
		t.Fatalf("previous visible before z = %d, %v; want outer directory %d", got, ok, a)
	}
	projection, err := tree.project(t.Context(), []review.FileID{files[0], files[2]})
	if err != nil {
		t.Fatal(err)
	}
	filtered := tree.withProjection(projection)
	if got, ok := filtered.previousVisible(z, index.Collapsed); !ok || got != a {
		t.Fatalf(
			"filtered previous visible before z = %d, %v; want outer directory %d",
			got,
			ok,
			a,
		)
	}
	fileIdentity := tree.nodes[tree.files[files[0]]].identity
	if got, ok := tree.visibleIdentity(fileIdentity, index.Collapsed); !ok || got != tree.nodes[a].identity {
		t.Fatalf("visible file identity = %#v, %v; want %#v", got, ok, tree.nodes[a].identity)
	}
	if _, err := index.Collapsed.set(a, tree.nodes[a].subtreeEnd, collapseExpanded); err != nil {
		t.Fatal(err)
	}
	if got, ok := filtered.nextVisible(a, index.Collapsed); !ok || got != b {
		t.Fatalf("next after expanded a = %d, %v; want independently collapsed b %d", got, ok, b)
	}
	if got, ok := filtered.nextVisible(b, index.Collapsed); !ok || got != z {
		t.Fatalf("next after collapsed b = %d, %v; want z %d", got, ok, z)
	}
}

func TestCollapsedTreeRemapsPathsOffLoopAtExactGeneration(t *testing.T) {
	oldSnapshot, _ := fileTreeSnapshot(t, []string{"a/file.go", "b/file.go"})
	oldIndex, err := buildWorkspaceIndex(t.Context(), oldSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	a := oldIndex.FileTree.directories["a"]
	if changed, setErr := oldIndex.Collapsed.set(
		a,
		oldIndex.FileTree.nodes[a].subtreeEnd,
		collapseCollapsed,
	); setErr != nil || !changed {
		t.Fatalf("collapse a = changed %v, error %v", changed, setErr)
	}
	snapshot := collapseSnapshot{
		tree:           oldIndex.FileTree,
		positionChunks: [][]int32{append([]int32(nil), oldIndex.Collapsed.positions...)},
		positionCount:  len(oldIndex.Collapsed.positions),
		generation:     oldIndex.Collapsed.generation,
	}
	newSnapshot, _ := fileTreeSnapshot(t, []string{"a/file.go", "c/file.go"})
	ledger, err := newRetainedLedger(MaxRetainedResultBytes, newSnapshot.RetainedBytes())
	if err != nil {
		t.Fatal(err)
	}
	remapped, err := buildWorkspaceIndexWithLedger(
		context.Background(),
		newSnapshot,
		snapshot,
		ledger,
	)
	if err != nil {
		t.Fatal(err)
	}
	newA := remapped.FileTree.directories["a"]
	if remapped.Collapsed.generation != snapshot.generation ||
		!remapped.Collapsed.isCollapsed(newA) {
		t.Fatalf(
			"remapped collapse = generation %d collapsed %v, want %d/true",
			remapped.Collapsed.generation,
			remapped.Collapsed.isCollapsed(newA),
			snapshot.generation,
		)
	}
}

func TestCollapsedTreeSupportsOneHundredThousandAllocationFreeToggles(t *testing.T) {
	const directories = 100_000
	state := newTestCollapsedTreeState(directories*2, directories)
	for index := range directories {
		canonical := index * 2
		if err := state.insert(canonical, canonical+2); err != nil {
			t.Fatal(err)
		}
	}
	assertCollapsedAVL(t, state)
	if height := state.height(state.root); height >= 24 {
		t.Fatalf("100k-node AVL height = %d, want below 24", height)
	}
	for index := range directories {
		state.remove(index * 2)
	}
	assertCollapsedAVL(t, state)
	allocations := testing.AllocsPerRun(1_000, func() {
		if _, err := state.set(0, 2, collapseCollapsed); err != nil {
			panic(err)
		}
		if _, err := state.set(0, 2, collapseExpanded); err != nil {
			panic(err)
		}
	})
	if allocations != 0 {
		t.Fatalf("toggle allocations = %f, want 0", allocations)
	}
}

func TestWorkspaceIndexSupportsOneHundredThousandHierarchicalFiles(t *testing.T) {
	const files = 100_000
	paths := make([]string, files)
	for index := range paths {
		paths[index] = fmt.Sprintf("directory-%06d/file.go", index)
	}
	snapshot, _ := fileTreeSnapshot(t, paths)
	outcome, err := buildWorkspaceIndex(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(outcome.FileTree.nodes); got != maxFileTreeNodes {
		t.Fatalf("hierarchical tree nodes = %d, want %d", got, maxFileTreeNodes)
	}
	if got := cap(outcome.Collapsed.nodes); got != files {
		t.Fatalf("collapsed AVL capacity = %d, want %d", got, files)
	}
	if got := len(outcome.Collapsed.positions); got != maxFileTreeNodes {
		t.Fatalf("collapse positions = %d, want %d", got, maxFileTreeNodes)
	}
	allowance, err := workspaceIndexAllowance(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if retained := outcome.RetainedBytes(); retained > allowance {
		t.Fatalf("workspace index retains %d bytes, allowance %d", retained, allowance)
	} else if retained <= 32<<20 {
		t.Fatalf("hierarchical fixture retains %d bytes and does not exercise the former fixed cap", retained)
	}
}

func newTestCollapsedTreeState(nodes, directories int) collapsedTreeState {
	positions := make([]int32, nodes)
	for index := range positions {
		positions[index] = missingCollapsedPosition
	}
	return collapsedTreeState{
		nodes:     make([]collapsedIntervalNode, 0, directories),
		positions: positions,
		root:      missingCollapsedPosition,
	}
}

func assertCollapsedAVL(t *testing.T, state collapsedTreeState) {
	t.Helper()
	seen := make([]bool, len(state.nodes))
	var inspect func(int32, int32, int32) (int32, int32)
	inspect = func(slot, lower, upper int32) (int32, int32) {
		if slot < 0 {
			return 0, 0
		}
		if int(slot) >= len(state.nodes) || seen[slot] {
			t.Fatalf("invalid or repeated AVL slot %d", slot)
		}
		seen[slot] = true
		node := state.nodes[slot]
		if node.start <= lower || node.start >= upper {
			t.Fatalf("AVL key %d outside (%d,%d)", node.start, lower, upper)
		}
		if node.left >= 0 && state.nodes[node.left].parent != slot {
			t.Fatalf("left parent for slot %d is inconsistent", slot)
		}
		if node.right >= 0 && state.nodes[node.right].parent != slot {
			t.Fatalf("right parent for slot %d is inconsistent", slot)
		}
		leftHeight, leftEnd := inspect(node.left, lower, node.start)
		rightHeight, rightEnd := inspect(node.right, node.start, upper)
		height := max(leftHeight, rightHeight) + 1
		maximum := max(node.end, leftEnd, rightEnd)
		if node.height != height || node.maxEnd != maximum {
			t.Fatalf(
				"slot %d metadata = height %d max %d, want %d/%d",
				slot,
				node.height,
				node.maxEnd,
				height,
				maximum,
			)
		}
		if difference := leftHeight - rightHeight; difference < -1 || difference > 1 {
			t.Fatalf("slot %d balance = %d", slot, difference)
		}
		if position := state.positions[int(node.start)-1]; position != slot {
			t.Fatalf("position for slot %d = %d", slot, position)
		}
		return height, maximum
	}
	if state.root >= 0 && state.nodes[state.root].parent >= 0 {
		t.Fatalf("root parent = %d, want -1", state.nodes[state.root].parent)
	}
	inspect(state.root, -1, int32(len(state.positions)+1))
	for slot, wasSeen := range seen {
		if !wasSeen {
			t.Fatalf("AVL slot %d is unreachable", slot)
		}
	}
}
