package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeDiskDiffAppendPreservesCounts(t *testing.T) {
	oldContents := diskDiffAppendFixture(4_097)
	newContents := append(bytes.Clone(oldContents), "appended-line\n"...)
	analysis := analyzeDiskDiffFixture(t, t.Context(), oldContents, newContents)
	defer func() {
		if err := analysis.Close(); err != nil {
			t.Errorf("close analysis: %v", err)
		}
	}()
	if analysis.additions != 1 || analysis.deletions != 0 {
		t.Fatalf(
			"analysis counts = +%d -%d, want +1 -0",
			analysis.additions,
			analysis.deletions,
		)
	}
}

func TestAnalyzeDiskDiffHonorsCancellationDuringIndexing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	contents := diskDiffAppendFixture(4_097)
	oldContent, _ := openDiskDiffFixture(t, contents)
	newContent, _ := openDiskDiffFixture(t, contents)
	workspace := t.TempDir()
	analysis, err := analyzeDiskDiff(
		ctx,
		workspace,
		oldContent,
		newContent,
		uint64(len(contents)),
		uint64(len(contents)),
	)
	if analysis != nil {
		_ = analysis.Close()
		t.Fatal("cancelled analysis returned owned state")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled analysis error = %v, want context.Canceled", err)
	}
	for name, content := range map[string]*os.File{"old": oldContent, "new": newContent} {
		if _, statErr := content.Stat(); !errors.Is(statErr, os.ErrClosed) {
			t.Errorf("%s input Stat error = %v, want os.ErrClosed", name, statErr)
		}
	}
}

func BenchmarkAnalyzeDiskDiffAppend10000Lines(b *testing.B) {
	oldContents := diskDiffAppendFixture(10_000)
	newContents := append(bytes.Clone(oldContents), "appended-line\n"...)
	oldPath := writeDiskDiffFixture(b, oldContents)
	newPath := writeDiskDiffFixture(b, newContents)
	workspace := b.TempDir()
	b.ReportAllocs()
	b.SetBytes(int64(len(oldContents) + len(newContents)))
	for b.Loop() {
		oldContent, err := os.Open(oldPath)
		if err != nil {
			b.Fatal(err)
		}
		newContent, err := os.Open(newPath)
		if err != nil {
			_ = oldContent.Close()
			b.Fatal(err)
		}
		analysis, err := analyzeDiskDiff(
			b.Context(),
			workspace,
			oldContent,
			newContent,
			uint64(len(oldContents)),
			uint64(len(newContents)),
		)
		if err != nil {
			b.Fatal(err)
		}
		if analysis.additions != 1 || analysis.deletions != 0 {
			_ = analysis.Close()
			b.Fatalf(
				"analysis counts = +%d -%d, want +1 -0",
				analysis.additions,
				analysis.deletions,
			)
		}
		if err := analysis.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHistogramDiffAppend10000Lines(b *testing.B) {
	oldContents := diskDiffAppendFixture(10_000)
	newContents := append(bytes.Clone(oldContents), "appended-line\n"...)
	b.ReportAllocs()
	b.SetBytes(int64(len(oldContents) + len(newContents)))
	for b.Loop() {
		oldLines, err := tokenizeNativeLines(b.Context(), oldContents)
		if err != nil {
			b.Fatal(err)
		}
		newLines, err := tokenizeNativeLines(b.Context(), newContents)
		if err != nil {
			b.Fatal(err)
		}
		edits, err := histogramEdits(
			b.Context(),
			oldContents,
			newContents,
			oldLines,
			newLines,
		)
		if err != nil {
			b.Fatal(err)
		}
		if len(edits) != 2 ||
			edits[0].kind != nativeEditEqual ||
			edits[0].count != 10_000 ||
			edits[1].kind != nativeEditAdd ||
			edits[1].count != 1 {
			b.Fatalf("unexpected edits: %+v", edits)
		}
	}
}

func BenchmarkHistogramDiffMaxAdmittedDistinctLines(b *testing.B) {
	oldContents, newContents := nativeDiffMaxAdmittedFixture()
	if !nativeDiffFitsMemory(uint64(len(oldContents)), uint64(len(newContents))) {
		b.Fatal("maximum-line fixture exceeds the in-memory byte limit")
	}
	if !nativeDiffLinesFitMemory(oldContents, newContents) {
		b.Fatal("maximum-line fixture exceeds the in-memory line limit")
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(oldContents) + len(newContents)))
	for b.Loop() {
		oldLines, err := tokenizeNativeLines(b.Context(), oldContents)
		if err != nil {
			b.Fatal(err)
		}
		newLines, err := tokenizeNativeLines(b.Context(), newContents)
		if err != nil {
			b.Fatal(err)
		}
		edits, err := histogramEdits(
			b.Context(), oldContents, newContents, oldLines, newLines,
		)
		if err != nil {
			b.Fatal(err)
		}
		if len(edits) != 2 ||
			edits[0].kind != nativeEditDelete ||
			edits[0].count != nativeInMemoryDiffLineLimit/2 ||
			edits[1].kind != nativeEditAdd ||
			edits[1].count != nativeInMemoryDiffLineLimit/2 {
			b.Fatalf("unexpected edits: %+v", edits)
		}
	}
}

func analyzeDiskDiffFixture(
	t *testing.T,
	ctx context.Context,
	oldContents, newContents []byte,
) *diskDiffAnalysis {
	t.Helper()
	oldContent, _ := openDiskDiffFixture(t, oldContents)
	newContent, _ := openDiskDiffFixture(t, newContents)
	analysis, err := analyzeDiskDiff(
		ctx,
		t.TempDir(),
		oldContent,
		newContent,
		uint64(len(oldContents)),
		uint64(len(newContents)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return analysis
}

func openDiskDiffFixture(t *testing.T, contents []byte) (*os.File, string) {
	t.Helper()
	path := writeDiskDiffFixture(t, contents)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return file, path
}

func writeDiskDiffFixture(t testing.TB, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func diskDiffAppendFixture(lines int) []byte {
	var contents bytes.Buffer
	contents.Grow(lines * 64)
	for line := range lines {
		_, _ = fmt.Fprintf(
			&contents,
			"line-%06d-abcdefghijklmnopqrstuvwxyz-ABCDEFGHIJKLMNOPQRSTUVWXYZ\n",
			line,
		)
	}
	return contents.Bytes()
}

func nativeDiffMaxAdmittedFixture() ([]byte, []byte) {
	var oldContents, newContents bytes.Buffer
	for line := range nativeInMemoryDiffLineLimit / 2 {
		_, _ = fmt.Fprintf(&oldContents, "old-line-%05d-abcdefghijklmno\n", line)
		_, _ = fmt.Fprintf(&newContents, "new-line-%05d-abcdefghijklmno\n", line)
	}
	return oldContents.Bytes(), newContents.Bytes()
}
