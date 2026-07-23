package app

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"

	"github.com/jacoelho/rvw/internal/review"
)

const findScaleFileCount = 100_000

var benchmarkFindResultCount int

func TestFileFindKeepsPerUpdateWorkBoundedAtMaximumScale(t *testing.T) {
	current, files := findScaleFixture(t, findScaleFileCount, func(index int) string {
		return fmt.Sprintf("src/file-%06d.go", index)
	})
	model := newScaleFindModel(current, files, "file")

	chunks := runScaleFileFind(t, &model, func(before, after int) {
		if processed := after - before; processed <= 0 || processed > maxFindFilesPerUpdate {
			t.Fatalf("chunk processed %d files, limit is %d", processed, maxFindFilesPerUpdate)
		}
		var pathBytes int
		for _, fileID := range files[before:after] {
			file, ok := current.Snapshot().File(fileID)
			if !ok {
				t.Fatalf("chunk file %d is unavailable", fileID)
			}
			pathBytes += len(file.DisplayPath())
		}
		if pathBytes > maxFindBytesPerUpdate {
			t.Fatalf("chunk retained %d path bytes, limit is %d", pathBytes, maxFindBytesPerUpdate)
		}
	})

	if chunks < findScaleFileCount/maxFindFilesPerUpdate {
		t.Fatalf("matcher completed in %d chunks; per-update file bound was not exercised", chunks)
	}
	if model.find.phase != findReady || len(model.find.fileResults) != maxFindResults ||
		model.find.totalMatches != findScaleFileCount || !model.find.limited {
		t.Fatalf(
			"result phase=%v retained=%d total=%d limited=%v",
			model.find.phase,
			len(model.find.fileResults),
			model.find.totalMatches,
			model.find.limited,
		)
	}
}

func TestFileFindKeepsPathBytesBoundedPerUpdate(t *testing.T) {
	current, files := findScaleFixture(t, 40, func(index int) string {
		return strings.Repeat("a", 4_080) + fmt.Sprintf("-%03d", index)
	})
	model := newScaleFindModel(current, files, "a")
	firstChunk := 0
	runScaleFileFind(t, &model, func(before, after int) {
		var pathBytes int
		for _, fileID := range files[before:after] {
			file, ok := current.Snapshot().File(fileID)
			if !ok {
				t.Fatalf("chunk file %d is unavailable", fileID)
			}
			pathBytes += len(file.DisplayPath())
		}
		if pathBytes > maxFindBytesPerUpdate {
			t.Fatalf("chunk retained %d path bytes, limit is %d", pathBytes, maxFindBytesPerUpdate)
		}
		if before == 0 {
			firstChunk = after - before
		}
	})
	if firstChunk >= len(files) || firstChunk >= maxFindFilesPerUpdate {
		t.Fatalf("path-byte limit did not split the first chunk: %d files", firstChunk)
	}
}

func BenchmarkFileFind100000Paths(b *testing.B) {
	current, files := findScaleFixture(b, findScaleFileCount, func(index int) string {
		return fmt.Sprintf("src/file-%06d.go", index)
	})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		model := newScaleFindModel(current, files, "file")
		runScaleFileFind(b, &model, nil)
		benchmarkFindResultCount = len(model.find.fileResults)
	}
}

func newScaleFindModel(
	current review.Review,
	files []review.FileID,
	query string,
) Model {
	input := textinput.New()
	input.SetValue(query)
	return Model{
		review:      current,
		searchInput: input,
		overlay:     OverlayFind,
		find: findPalette{
			scope:      findFiles,
			phase:      findDebouncing,
			generation: 1,
			candidates: files,
		},
	}
}

func runScaleFileFind(
	tb testing.TB,
	model *Model,
	checkChunk func(before, after int),
) int {
	tb.Helper()
	command := model.beginFileFind(findDebounceMsg{generation: model.find.generation})
	chunks := 0
	for command != nil {
		message, ok := command().(findChunkMsg)
		if !ok {
			tb.Fatal("matcher command returned an unexpected message")
		}
		build := model.find.fileBuild
		if build == nil {
			tb.Fatal("matcher command has no active build")
			return chunks
		}
		before := build.position
		command = model.advanceFileFind(message)
		if checkChunk != nil {
			checkChunk(before, build.position)
		}
		chunks++
	}
	return chunks
}

func findScaleFixture(
	tb testing.TB,
	fileCount int,
	pathAt func(index int) string,
) (review.Review, []review.FileID) {
	tb.Helper()
	sourceID, err := review.NewSourceID(1)
	if err != nil {
		tb.Fatal(err)
	}
	builder, err := review.NewSnapshotBuilder(sourceID, fileCount)
	if err != nil {
		tb.Fatal(err)
	}
	flags, err := review.NewFlags(review.FileTraits{}, review.HiddenReasons{})
	if err != nil {
		tb.Fatal(err)
	}
	change, err := review.NewUnclassifiedChange(review.ChangeAdded, 1, 0)
	if err != nil {
		tb.Fatal(err)
	}
	files := make([]review.FileID, 0, fileCount)
	for index := range fileCount {
		path, err := review.NewPath([]byte(pathAt(index)))
		if err != nil {
			tb.Fatal(err)
		}
		handle, err := builder.AddFile(nil, &path, change, flags)
		if err != nil {
			tb.Fatal(err)
		}
		files = append(files, handle.FileID())
	}
	snapshot, err := builder.Finish()
	if err != nil {
		tb.Fatal(err)
	}
	current, err := review.NewReview(testReviewRequest(snapshot))
	if err != nil {
		tb.Fatal(err)
	}
	return current, files
}
