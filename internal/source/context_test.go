package source_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// textDiff is a text file's diff with both sides present: the new side's content, its line count
// as a patch numbers lines, and the hunk headers.
func textDiff(content string, lines int, hunks ...source.HunkRange) source.FileDiff {
	return source.FileDiff{
		New: []byte(content),
		Facts: source.FileFacts{
			Kind:  source.KindText,
			Old:   source.SideFacts{Present: true, Lines: 1},
			New:   source.SideFacts{Present: true, Lines: lines},
			Hunks: hunks,
		},
	}
}

// TestContextLines covers FileDiff.Context's numbering and splitting: lines split on LF only, as a
// patch numbers them, and each carries its number on both sides.
func TestContextLines(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		d        source.FileDiff
		want     []source.ContextLine
		from, to int
	}{
		{
			name: "CRLF without a final newline",
			d:    textDiff("a\r\nb\r\nc\r\nd", 4, source.HunkRange{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}),
			from: 2, to: 4,
			want: []source.ContextLine{{Old: 2, New: 2, Text: "b\r"}, {Old: 3, New: 3, Text: "c\r"}, {Old: 4, New: 4, Text: "d"}},
		},
		{
			name: "gap between two hunks",
			d: textDiff("1\n2\n3\n4\n5\n6\n", 6,
				source.HunkRange{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 2},
				source.HunkRange{OldStart: 5, OldCount: 1, NewStart: 6, NewCount: 1}),
			from: 3, to: 5,
			want: []source.ContextLine{{Old: 2, New: 3, Text: "3"}, {Old: 3, New: 4, Text: "4"}, {Old: 4, New: 5, Text: "5"}},
		},
		{
			name: "tail after the last hunk",
			d:    textDiff("x\nc\nd\n", 3, source.HunkRange{OldStart: 1, OldCount: 2, NewStart: 1, NewCount: 1}),
			from: 2, to: 3,
			want: []source.ContextLine{{Old: 3, New: 2, Text: "c"}, {Old: 4, New: 3, Text: "d"}},
		},
		{
			name: "a lone CR is not a line break",
			d:    textDiff("a\rb\nc\nd\n", 3, source.HunkRange{OldStart: 3, OldCount: 1, NewStart: 3, NewCount: 1}),
			from: 1, to: 2,
			want: []source.ContextLine{{Old: 1, New: 1, Text: "a\rb"}, {Old: 2, New: 2, Text: "c"}},
		},
		{
			name: "part of a gap",
			d:    textDiff("a\nb\nc\nd\n", 4, source.HunkRange{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}),
			from: 3, to: 3,
			want: []source.ContextLine{{Old: 3, New: 3, Text: "c"}},
		},
		{
			name: "final newline",
			d:    textDiff("a\nb\n", 2, source.HunkRange{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}),
			from: 2, to: 2,
			want: []source.ContextLine{{Old: 2, New: 2, Text: "b"}},
		},
		{
			name: "no final newline",
			d:    textDiff("a\nb", 2, source.HunkRange{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}),
			from: 2, to: 2,
			want: []source.ContextLine{{Old: 2, New: 2, Text: "b"}},
		},
		{
			// The old side lacked the final newline too, so the patch's "\ No newline" line is
			// outside every hunk and the last line is plain context.
			name: "unchanged last line without a newline",
			d:    textDiff("A\nb\nc", 3, source.HunkRange{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}),
			from: 2, to: 3,
			want: []source.ContextLine{{Old: 2, New: 2, Text: "b"}, {Old: 3, New: 3, Text: "c"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.d.Context(tc.from, tc.to)
			if err != nil {
				t.Fatalf("Context(%d, %d): %v", tc.from, tc.to, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Context(%d, %d) = %+v\nwant %+v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

// TestContextErrors covers FileDiff.Context's refusals: a file without gaps has no context, and a
// range must lie inside one gap.
func TestContextErrors(t *testing.T) {
	t.Parallel()
	hunk := source.HunkRange{OldStart: 3, OldCount: 1, NewStart: 3, NewCount: 1}
	text := textDiff("a\nb\nc\nd\ne\n", 5, hunk) // gaps 1-2 and 4-5
	with := func(change func(*source.FileDiff)) source.FileDiff {
		d := textDiff("a\nb\nc\nd\ne\n", 5, hunk)
		change(&d)
		return d
	}
	for _, tc := range []struct {
		want     error
		name     string
		d        source.FileDiff
		from, to int
	}{
		{name: "binary", d: with(func(d *source.FileDiff) { d.Facts.Kind = source.KindBinary }), from: 1, to: 1, want: source.ErrNoContext},
		{name: "unavailable", d: with(func(d *source.FileDiff) { d.Facts.Kind = source.KindUnavailable }), from: 1, to: 1, want: source.ErrNoContext},
		{name: "large", d: with(func(d *source.FileDiff) { d.Facts.Large = true }), from: 1, to: 1, want: source.ErrNoContext},
		{name: "added", d: with(func(d *source.FileDiff) { d.Facts.Old = source.SideFacts{} }), from: 1, to: 1, want: source.ErrNoContext},
		{name: "deleted", d: with(func(d *source.FileDiff) { d.Facts.New = source.SideFacts{}; d.New = nil }), from: 1, to: 1, want: source.ErrNoContext},
		{name: "emptied", d: textDiff("", 0, source.HunkRange{OldStart: 1, OldCount: 1}), from: 1, to: 1, want: source.ErrOutsideGap},
		{name: "a hunk line", d: text, from: 3, to: 3, want: source.ErrOutsideGap},
		{name: "past the end", d: text, from: 6, to: 6, want: source.ErrOutsideGap},
		{name: "line 0", d: text, from: 0, to: 1, want: source.ErrOutsideGap},
		{name: "across a hunk", d: text, from: 2, to: 4, want: source.ErrOutsideGap},
		{name: "partly past the end", d: text, from: 5, to: 6, want: source.ErrOutsideGap},
		{name: "reversed", d: text, from: 2, to: 1, want: source.ErrOutsideGap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.d.Context(tc.from, tc.to)
			if !errors.Is(err, tc.want) || got != nil {
				t.Errorf("Context(%d, %d) = %+v, %v; want %v", tc.from, tc.to, got, err, tc.want)
			}
		})
	}
}

// TestDiffLineNumbers checks each patch line's numbers on both sides against a worked patch with
// three hunks: a deletion, a replacement, and an appended line after a last line without a newline.
func TestDiffLineNumbers(t *testing.T) {
	t.Parallel()
	numbered := func(from, to int) []string {
		var out []string
		for n := from; n <= to; n++ {
			out = append(out, fmt.Sprintf("l%d", n))
		}
		return out
	}
	old := numbered(1, 30)
	r := newRepo(t)
	r.write("f.txt", strings.Join(old, "\n"))
	r.commit("base")
	updated := slices.Delete(slices.Clone(old), 2, 3) // delete l3
	updated[13] = "X"                                 // replace l15
	r.write("f.txt", strings.Join(updated, "\n")+"\nl31\n")
	r.commit("edit")
	d, err := resolve(t, r.dir, source.Last()).Diff(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		text     string
		old, new int
		op       source.LineOp
		noEOL    bool
	}
	c, del, add := source.OpContext, source.OpDelete, source.OpAdd
	want := [][]row{
		{{"l1", 1, 1, c, false}, {"l2", 2, 2, c, false}, {"l3", 3, 0, del, false}, {"l4", 4, 3, c, false}, {"l5", 5, 4, c, false}, {"l6", 6, 5, c, false}},
		{{"l12", 12, 11, c, false}, {"l13", 13, 12, c, false}, {"l14", 14, 13, c, false}, {"l15", 15, 0, del, false}, {"X", 0, 14, add, false},
			{"l16", 16, 15, c, false}, {"l17", 17, 16, c, false}, {"l18", 18, 17, c, false}},
		{{"l27", 27, 26, c, false}, {"l28", 28, 27, c, false}, {"l29", 29, 28, c, false}, {"l30", 30, 0, del, true},
			{"l30", 0, 29, add, false}, {"l31", 0, 30, add, false}},
	}
	got := make([][]row, len(d.Hunks))
	for k, h := range d.Hunks {
		for _, l := range h.Lines {
			got[k] = append(got[k], row{l.Text, l.Old, l.New, l.Op, l.NoEOL})
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hunk lines (text, old, new, op, no EOL):\n got %v\nwant %v", got, want)
	}
}
