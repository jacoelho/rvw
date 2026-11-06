package source_test

import (
	"slices"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

func textFacts(oldLines, newLines int, hunks ...source.HunkRange) source.FileFacts {
	return source.FileFacts{
		Kind:  source.KindText,
		Old:   source.SideFacts{Present: true, Lines: oldLines},
		New:   source.SideFacts{Present: true, Lines: newLines},
		Hunks: hunks,
	}
}

// TestGaps covers expandable context: the unchanged lines before, between and after hunks,
// numbered on both sides from the hunk headers, and the whole of a file with no hunks.
func TestGaps(t *testing.T) {
	t.Parallel()
	large := textFacts(10, 11, source.HunkRange{OldStart: 3, OldCount: 2, NewStart: 3, NewCount: 3})
	large.Large = true
	added := textFacts(0, 4)
	added.Old.Present = false
	for _, tc := range []struct {
		name  string
		facts source.FileFacts
		want  []source.Gap
	}{
		{"no hunks", textFacts(10, 10), []source.Gap{{From: 1, To: 10, OldFrom: 1}}},
		{"one hunk inside", textFacts(10, 11, source.HunkRange{OldStart: 3, OldCount: 2, NewStart: 3, NewCount: 3}),
			[]source.Gap{{From: 1, To: 2, OldFrom: 1}, {From: 6, To: 11, OldFrom: 5}}},
		{"two hunks", textFacts(20, 20,
			source.HunkRange{OldStart: 2, OldCount: 1, NewStart: 2, NewCount: 1},
			source.HunkRange{OldStart: 10, OldCount: 1, NewStart: 10, NewCount: 1}),
			[]source.Gap{{From: 1, To: 1, OldFrom: 1}, {From: 3, To: 9, OldFrom: 3}, {From: 11, To: 20, OldFrom: 11}}},
		{"hunk at the start", textFacts(5, 5, source.HunkRange{OldStart: 1, OldCount: 2, NewStart: 1, NewCount: 2}),
			[]source.Gap{{}, {From: 3, To: 5, OldFrom: 3}}},
		{"hunk covers the file", textFacts(5, 5, source.HunkRange{OldStart: 1, OldCount: 5, NewStart: 1, NewCount: 5}),
			[]source.Gap{{}, {}}},
		{"insertion before the first line", textFacts(3, 5, source.HunkRange{OldStart: 0, OldCount: 0, NewStart: 1, NewCount: 2}),
			[]source.Gap{{}, {From: 3, To: 5, OldFrom: 1}}},
		{"deletion", textFacts(6, 4, source.HunkRange{OldStart: 3, OldCount: 2, NewStart: 2, NewCount: 0}),
			[]source.Gap{{From: 1, To: 2, OldFrom: 1}, {From: 3, To: 4, OldFrom: 5}}},
		{"large", large, nil},
		{"one side absent", added, nil},
		{"binary", source.FileFacts{Kind: source.KindBinary, Old: source.SideFacts{Present: true}, New: source.SideFacts{Present: true}}, nil},
	} {
		if got := tc.facts.Gaps(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: Gaps() = %+v; want %+v", tc.name, got, tc.want)
		}
	}
}

// TestShowable covers the lines a comment may target: lines in a hunk or its context, a range
// across both, and nothing beyond the file, before line 1 or, in a large file, outside a hunk.
func TestShowable(t *testing.T) {
	t.Parallel()
	hunk := source.HunkRange{OldStart: 3, OldCount: 2, NewStart: 3, NewCount: 3}
	small := textFacts(10, 11, hunk)
	large := small
	large.Large = true
	rename := textFacts(10, 10)
	binary := source.FileFacts{Kind: source.KindBinary, Old: source.SideFacts{Present: true}, New: source.SideFacts{Present: true}}
	for _, tc := range []struct {
		name    string
		facts   source.FileFacts
		newSide bool
		r       source.LineRange
		want    bool
	}{
		{"context before", small, true, source.LineRange{Start: 1, End: 2}, true},
		{"hunk", small, true, source.LineRange{Start: 3, End: 5}, true},
		{"whole new side", small, true, source.LineRange{Start: 1, End: 11}, true},
		{"context after", small, true, source.LineRange{Start: 6, End: 11}, true},
		{"past the new side", small, true, source.LineRange{Start: 11, End: 12}, false},
		{"line 0", small, true, source.LineRange{Start: 0, End: 1}, false},
		{"reversed", small, true, source.LineRange{Start: 3, End: 2}, false},
		{"whole old side", small, false, source.LineRange{Start: 1, End: 10}, true},
		{"old context after", small, false, source.LineRange{Start: 5, End: 10}, true},
		{"past the old side", small, false, source.LineRange{Start: 10, End: 11}, false},
		{"large hunk", large, true, source.LineRange{Start: 3, End: 5}, true},
		{"large context", large, true, source.LineRange{Start: 1, End: 2}, false},
		{"large across", large, true, source.LineRange{Start: 2, End: 3}, false},
		{"rename, any line", rename, true, source.LineRange{Start: 1, End: 10}, true},
		{"rename, old side", rename, false, source.LineRange{Start: 4, End: 4}, true},
		{"rename, past the end", rename, true, source.LineRange{Start: 11, End: 11}, false},
		{"binary", binary, true, source.LineRange{Start: 1, End: 1}, false},
	} {
		if got := tc.facts.Showable(tc.newSide, tc.r); got != tc.want {
			t.Errorf("%s: Showable(%v, %+v) = %v; want %v", tc.name, tc.newSide, tc.r, got, tc.want)
		}
	}
}
