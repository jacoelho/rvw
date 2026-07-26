package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/projection"
)

func TestFileTreeBudgetRejectsNodeOverflow(t *testing.T) {
	nodeBudget := fileTreeBudget{nodes: maxFileTreeNodes}
	if err := nodeBudget.reserveNodes(1); !errors.Is(err, ErrFileTreeLimited) {
		t.Fatalf("node overflow error = %v, want ErrFileTreeLimited", err)
	}
}

func TestFileTreeDirectBuildPreservesHierarchicalOrder(t *testing.T) {
	snapshot, files := fileTreeSnapshot(t, []string{
		"a.z", "b/c.go", "a/x.go", "b/a.go",
	})
	tree, err := buildFileTreeContext(t.Context(), snapshot, files)
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, len(tree.nodes))
	for index, node := range tree.nodes {
		labels[index] = node.label
	}
	want := []string{"a", "x.go", "a.z", "b", "a.go", "c.go"}
	if !slices.Equal(labels, want) {
		t.Fatalf("tree labels = %q, want %q", labels, want)
	}
}

func TestFileTreeBuildSupportsOneHundredThousandFlatFiles(t *testing.T) {
	const count = 100_000
	paths := make([]string, count)
	for index := range paths {
		paths[index] = fmt.Sprintf("file-%06d.go", index)
	}
	snapshot, files := fileTreeSnapshot(t, paths)
	tree, err := buildFileTreeContext(t.Context(), snapshot, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.nodes) != count || len(tree.files) != count || len(tree.directories) != 0 {
		t.Fatalf(
			"tree sizes = nodes:%d files:%d directories:%d, want %d/%d/0",
			len(tree.nodes),
			len(tree.files),
			len(tree.directories),
			count,
			count,
		)
	}
}

func TestFileTreeProjectionSharesCanonicalBacking(t *testing.T) {
	snapshot, files := fileTreeSnapshot(t, []string{"a/one.go", "a/two.go", "b/three.go"})
	tree, err := buildFileTreeContext(t.Context(), snapshot, files)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := tree.project(t.Context(), []change.FileID{files[1]})
	if err != nil {
		t.Fatal(err)
	}
	visible := tree.withProjection(projection)
	if len(tree.nodes) == 0 || &visible.nodes[0] != &tree.nodes[0] {
		t.Fatal("visible projection did not share canonical node backing")
	}
	if _, ok := visible.identityForFile(files[0]); ok {
		t.Fatal("projection retained an excluded file")
	}
	if _, ok := visible.identityForFile(files[1]); !ok {
		t.Fatal("projection omitted its selected file")
	}
	if len(visible.projection) != 2 {
		t.Fatalf("projection nodes = %d, want directory plus file", len(visible.projection))
	}
}

func fileTreeSnapshot(
	t *testing.T,
	paths []string,
) (change.Snapshot, []change.FileID) {
	t.Helper()
	sourceID, err := change.NewSourceID(1)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := change.NewSnapshotBuilder(sourceID, len(paths))
	if err != nil {
		t.Fatal(err)
	}
	flags, err := change.NewFlags(change.FileTraits{}, change.HiddenReasons{})
	if err != nil {
		t.Fatal(err)
	}
	fileChange, err := change.NewUnclassifiedChange(change.ChangeAdded, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]change.FileID, 0, len(paths))
	for _, raw := range paths {
		path, pathErr := change.NewPathString(raw)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		handle, addErr := builder.AddFile(nil, &path, fileChange, flags)
		if addErr != nil {
			t.Fatal(addErr)
		}
		files = append(files, handle.FileID())
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, files
}

func TestFileTreeBuildObservesCancellationBeforeWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := buildFileTreeContext(
		ctx,
		change.Snapshot{},
		nil,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled build error = %v, want context.Canceled", err)
	}
}

func TestTreeDescriptorOrderingMatchesCanonicalComparator(t *testing.T) {
	descriptors := make([]treeDescriptor, maxTreeSortRun*3+17)
	for index := range descriptors {
		value := len(descriptors) - index
		descriptors[index] = treeDescriptor{
			path: fmt.Sprintf("directory-%03d/file-%06d.go", value%19, value),
			file: change.FileID(value),
		}
	}
	descriptors[3].invalid = true
	descriptors[3].path = ""
	want := slices.Clone(descriptors)
	slices.SortFunc(want, compareTreeDescriptors)

	ordered, err := orderTreeDescriptors(
		t.Context(),
		descriptors,
		make([]treeDescriptor, len(descriptors)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ordered, want) {
		t.Fatal("bounded tree ordering differs from the canonical comparator")
	}
}

func TestTreeDescriptorOrderingObservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	descriptors := make([]treeDescriptor, maxTreeSortRun+1)
	if _, err := orderTreeDescriptors(
		ctx,
		descriptors,
		make([]treeDescriptor, len(descriptors)),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("ordering cancellation error = %v", err)
	}
}

func TestFileTreeOrderingScratchUsesSharedLedger(t *testing.T) {
	snapshot, files := fileTreeSnapshot(t, []string{"one.go"})
	const exact = int64(fileTreeDescriptorCharge + fileTreeNodeCharge + fileTreeFileCharge)
	ledger, err := projection.NewLedger(exact, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildFileTreeWithLedger(t.Context(), snapshot, files, ledger); err != nil {
		t.Fatalf("exact ordering construction limit rejected: %v", err)
	}
	ledger, err = projection.NewLedger(exact-1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildFileTreeWithLedger(
		t.Context(),
		snapshot,
		files,
		ledger,
	); !errors.Is(err, projection.ErrRetainedLimited) {
		t.Fatalf("one-byte-over ordering error = %v", err)
	}
}
