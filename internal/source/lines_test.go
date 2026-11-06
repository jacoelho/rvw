package source_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// TestLinesRanges pins Snapshot.Lines' multi-range contract against a real blob: out[k] belongs
// to ranges[k] whatever their order or overlap, ranges past the end get fewer lines or none, keep
// bounds every kept line without hiding its length, and an invalid range fails the whole call.
func TestLinesRanges(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	long := strings.Repeat("L", 100)
	// Every line is distinct, so a result attached to the wrong range shows; the last line has no
	// final LF.
	r.write("a.txt", "one\ntwo\n\n"+long+"\nfive\nsix\nseven")
	huge := strings.Repeat("h", source.LargeBytes+10)
	r.write("huge.txt", huge+"\n")
	r.commit("base")
	snap := resolve(t, r.dir, source.Last())
	a, hugeFile := fileIndex(t, snap, "a.txt"), fileIndex(t, snap, "huge.txt")

	line := func(text string) source.SideLine { return source.SideLine{Text: text, Bytes: len(text)} }
	cut := func(text string, bytes int) source.SideLine { return source.SideLine{Text: text, Bytes: bytes} }
	type rs = []source.LineRange
	for _, tc := range []struct {
		name   string
		ranges rs
		keep   int
		want   [][]source.SideLine
	}{
		{"none", rs{}, source.LargeBytes, [][]source.SideLine{}},
		{"unsorted with a gap", rs{{5, 5}, {1, 1}}, source.LargeBytes,
			[][]source.SideLine{{line("five")}, {line("one")}}},
		{"unsorted", rs{{5, 6}, {1, 2}}, source.LargeBytes,
			[][]source.SideLine{{line("five"), line("six")}, {line("one"), line("two")}}},
		{"overlapping, nested and repeated", rs{{2, 4}, {3, 5}, {2, 5}, {3, 4}, {2, 4}}, source.LargeBytes,
			[][]source.SideLine{
				{line("two"), line(""), line(long)},
				{line(""), line(long), line("five")},
				{line("two"), line(""), line(long), line("five")},
				{line(""), line(long)},
				{line("two"), line(""), line(long)},
			}},
		{"ending on the unterminated last line", rs{{7, 7}}, source.LargeBytes,
			[][]source.SideLine{{line("seven")}}},
		{"past the end", rs{{6, 9}, {8, 9}}, source.LargeBytes,
			[][]source.SideLine{{line("six"), line("seven")}, {}}},
		{"keep cuts kept lines only", rs{{4, 4}, {1, 1}, {5, 5}}, 3,
			[][]source.SideLine{{cut("LLL", 100)}, {line("one")}, {cut("fiv", 4)}}},
		{"keep zero", rs{{4, 5}}, 0, [][]source.SideLine{{cut("", 100), cut("", 4)}}},
		{"keep negative", rs{{4, 5}}, -1, [][]source.SideLine{{cut("", 100), cut("", 4)}}},
	} {
		got, err := snap.Lines(t.Context(), a, true, tc.ranges, tc.keep)
		if err != nil {
			t.Errorf("%s: Lines(%v, keep %d): %v", tc.name, tc.ranges, tc.keep, err)
			continue
		}
		// "None" is a length: nil and empty are the same answer.
		equal := slices.EqualFunc(got, tc.want, slices.Equal[[]source.SideLine])
		if len(got) != len(tc.want) || !equal {
			t.Errorf("%s: Lines(%v, keep %d) = %+v; want %+v", tc.name, tc.ranges, tc.keep, got, tc.want)
		}
	}

	got, err := snap.Lines(t.Context(), hugeFile, true, rs{{1, 1}}, 4*source.LargeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if l := got[0][0]; len(l.Text) != source.LargeBytes || l.Bytes != len(huge) {
		t.Errorf("keep above LargeBytes kept %d of %d bytes; want %d of %d", len(l.Text), l.Bytes, source.LargeBytes, len(huge))
	}

	for _, ranges := range []rs{{{0, 1}}, {{-1, 2}}, {{3, 2}}, {{1, 2}, {0, 1}}} {
		if got, err := snap.Lines(t.Context(), a, true, ranges, source.LargeBytes); err == nil || got != nil {
			t.Errorf("Lines(%v) = %+v, %v; want no lines and an error", ranges, got, err)
		}
	}
}
