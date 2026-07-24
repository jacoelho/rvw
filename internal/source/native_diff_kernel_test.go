package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestNativeDiffFitsMemoryAtBoundary(t *testing.T) {
	tests := []struct {
		name    string
		oldSize uint64
		newSize uint64
		want    bool
	}{
		{name: "empty", want: true},
		{
			name:    "exact combined limit",
			oldSize: nativeInMemoryDiffByteLimit / 2,
			newSize: nativeInMemoryDiffByteLimit / 2,
			want:    true,
		},
		{
			name:    "one byte above",
			oldSize: nativeInMemoryDiffByteLimit / 2,
			newSize: nativeInMemoryDiffByteLimit/2 + 1,
		},
		{
			name:    "one side exact limit",
			oldSize: nativeInMemoryDiffByteLimit,
			want:    true,
		},
		{
			name:    "one side above limit",
			oldSize: nativeInMemoryDiffByteLimit + 1,
		},
		{
			name:    "overflow cannot pass",
			oldSize: ^uint64(0),
			newSize: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nativeDiffFitsMemory(test.oldSize, test.newSize); got != test.want {
				t.Fatalf(
					"nativeDiffFitsMemory(%d, %d) = %v, want %v",
					test.oldSize,
					test.newSize,
					got,
					test.want,
				)
			}
		})
	}
}

func TestNativeDiffLinesFitMemoryAtBoundary(t *testing.T) {
	exact := bytes.Repeat([]byte{'\n'}, nativeInMemoryDiffLineLimit)
	if !nativeDiffLinesFitMemory(exact, nil) {
		t.Fatal("exact line limit did not fit in memory")
	}
	above := append(bytes.Clone(exact), '\n')
	if nativeDiffLinesFitMemory(above, nil) {
		t.Fatal("line count above limit fit in memory")
	}
	oldContents := bytes.Repeat([]byte("old\n"), nativeInMemoryDiffLineLimit/2)
	newContents := bytes.Repeat([]byte("new\n"), nativeInMemoryDiffLineLimit/2-1)
	newContents = append(newContents, "unterminated"...)
	if !nativeDiffLinesFitMemory(oldContents, newContents) {
		t.Fatal("combined exact line limit with an unterminated line did not fit in memory")
	}
	if nativeDiffLinesFitMemory(oldContents, append(newContents, "\nextra"...)) {
		t.Fatal("combined line count above limit fit in memory")
	}
}

func TestNativeMemoryAndDiskEditsAgree(t *testing.T) {
	crossoverOld, crossoverNew := nativeDiffCrossoverFixture()
	tests := []struct {
		name string
		old  []byte
		new  []byte
	}{
		{name: "append", old: []byte("one\ntwo\n"), new: []byte("one\ntwo\nthree\n")},
		{name: "prepend", old: []byte("two\nthree\n"), new: []byte("one\ntwo\nthree\n")},
		{name: "middle", old: []byte("one\ntwo\nthree\n"), new: []byte("one\nchanged\nthree\n")},
		{
			name: "ambiguous repeated lines",
			old:  []byte("same\nsame\nold\nsame\nold\nsame\n"),
			new:  []byte("same\nnew\nsame\nsame\nnew\nsame\n"),
		},
		{name: "unterminated", old: []byte("one\ntwo"), new: []byte("one\nchanged")},
		{name: "crossover range", old: crossoverOld, new: crossoverNew},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			memory := analyzeMemoryDiffFixture(t, test.old, test.new)
			disk := analyzeDiskEditsFixture(t, test.old, test.new)
			if !slices.Equal(disk, memory) {
				t.Fatalf("disk edits = %+v, memory edits = %+v", disk, memory)
			}
		})
	}
}

func TestNativeHistogramAnchorHonorsCancellation(t *testing.T) {
	oldContents := []byte("old\nshared\n")
	newContents := []byte("new\nshared\n")
	oldLines, err := tokenizeNativeLines(t.Context(), oldContents)
	if err != nil {
		t.Fatal(err)
	}
	newLines, err := tokenizeNativeLines(t.Context(), newContents)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var probes uint64
	_, _, _, _, err = nativeHistogramAnchor(
		ctx, &probes, oldContents, newContents, oldLines, newLines,
		0, len(oldLines), 0, len(newLines),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("nativeHistogramAnchor error = %v, want context.Canceled", err)
	}
}

func analyzeMemoryDiffFixture(
	t *testing.T,
	oldContents, newContents []byte,
) []nativeEdit {
	t.Helper()
	oldLines, err := tokenizeNativeLines(t.Context(), oldContents)
	if err != nil {
		t.Fatal(err)
	}
	newLines, err := tokenizeNativeLines(t.Context(), newContents)
	if err != nil {
		t.Fatal(err)
	}
	edits, err := histogramEdits(
		t.Context(),
		oldContents,
		newContents,
		oldLines,
		newLines,
	)
	if err != nil {
		t.Fatal(err)
	}
	return edits
}

func analyzeDiskEditsFixture(t *testing.T, oldContents, newContents []byte) []nativeEdit {
	t.Helper()
	analysis := analyzeDiskDiffFixture(t, t.Context(), oldContents, newContents)
	defer func() {
		if err := analysis.Close(); err != nil {
			t.Error(err)
		}
	}()
	edits := make([]nativeEdit, 0, analysis.editCount)
	for index := range analysis.editCount {
		edit, err := readDiskEditAt(analysis.edits, index)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, nativeEdit{
			kind:     edit.kind,
			oldStart: int(edit.oldStart),
			newStart: int(edit.newStart),
			count:    int(edit.count),
		})
	}
	return edits
}

func nativeDiffCrossoverFixture() ([]byte, []byte) {
	var oldContents, newContents bytes.Buffer
	oldContents.Grow(700 << 10)
	newContents.Grow(700 << 10)
	padding := strings.Repeat("x", 1_300)
	for line := range 500 {
		switch line {
		case 249, 251:
			oldContents.WriteString("repeated-middle-line\n")
			newContents.WriteString("repeated-middle-line\n")
		case 250:
			oldContents.WriteString("old-middle-line\n")
			newContents.WriteString("new-middle-line\n")
		case 499:
			oldContents.WriteString("old-unterminated-tail")
			newContents.WriteString("new-unterminated-tail")
		default:
			_, _ = fmt.Fprintf(
				&oldContents,
				"stable-line-%06d-%s\n",
				line, padding,
			)
			_, _ = fmt.Fprintf(
				&newContents,
				"stable-line-%06d-%s\n",
				line, padding,
			)
		}
	}
	return oldContents.Bytes(), newContents.Bytes()
}
