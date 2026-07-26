package tui

import (
	"context"
	"fmt"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

const (
	largeTreeFileCount = 1_024
	largeTreeFileLines = 10_000
)

var benchmarkFileTree fileTree

func BenchmarkBuildFileTreeContext1000Files(b *testing.B) {
	snapshot, files := largeTreeFixture(b)
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		tree, err := buildFileTreeContext(context.Background(), snapshot, files)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkFileTree = tree
	}
}

func TestFileTreeWindowMaterializationIsViewportBounded(t *testing.T) {
	snapshot, files := largeTreeFixture(t)
	tree, err := buildFileTreeContext(t.Context(), snapshot, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.files) < 1_000 {
		t.Fatalf("tree files = %d, want at least 1000", len(tree.files))
	}

	cursor, ok := tree.identityForFile(files[len(files)/2])
	if !ok {
		t.Fatal("missing middle-file tree identity")
	}
	for _, height := range []int{1, 17, 80} {
		materialized := 0
		for range tree.window(cursor, height, collapsedTreeState{}) {
			materialized++
		}
		if materialized != height {
			t.Fatalf(
				"height %d materialized %d rows from %d-node tree, want exactly the viewport",
				height,
				materialized,
				len(tree.nodes),
			)
		}
	}
}

func largeTreeFixture(tb testing.TB) (change.Snapshot, []change.FileID) {
	tb.Helper()
	fileCount := largeTreeFileCount

	sourceID, err := change.NewSourceID(1)
	if err != nil {
		tb.Fatal(err)
	}
	builder, err := change.NewSnapshotBuilder(sourceID, fileCount)
	if err != nil {
		tb.Fatal(err)
	}
	flags, err := change.NewFlags(change.FileTraits{}, change.HiddenReasons{})
	if err != nil {
		tb.Fatal(err)
	}
	fileChange, err := change.NewUnclassifiedChange(
		change.ChangeAdded,
		largeTreeFileLines,
		0,
	)
	if err != nil {
		tb.Fatal(err)
	}

	files := make([]change.FileID, 0, fileCount)
	for index := range fileCount {
		path, err := change.NewPath(fmt.Appendf(nil,
			"pkg/%03d/file-%04d.go",
			index/32,
			index,
		))
		if err != nil {
			tb.Fatal(err)
		}
		handle, err := builder.AddFile(nil, &path, fileChange, flags)
		if err != nil {
			tb.Fatal(err)
		}
		files = append(files, handle.FileID())
	}
	snapshot, err := builder.Finish()
	if err != nil {
		tb.Fatal(err)
	}
	return snapshot, files
}
