package review_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// Large-file fixture paths. Each file has more than 20,000 lines, so it is large, and git's patch
// has its default three lines of context, which fixes the hunks below.
const (
	// pathBig: line 100 becomes two lines and line 25,000 changes, so the hunks are old 97-103 /
	// new 97-104, and old 24997-25003 / new 24998-25004: new numbering is one past old there.
	pathBig = "big.txt"
	// pathAppend: two lines are appended to 20,001, so the hunk is old 19999-20001 / new
	// 19999-20003.
	pathAppend = "append.txt"
	// pathBigAdded is added: its one hunk is new 1-20001, and it has no old side.
	pathBigAdded = "bigadd.txt"
)

func newLargeFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	bigOld := numbered("big", 30_000)
	appendOld := numbered("append", 20_001)
	write(t, dir, pathBig, bigOld)
	write(t, dir, pathAppend, appendOld)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "one")

	bigNew := slices.Concat(bigOld[:99], []string{"big 100 a", "big 100 b"}, bigOld[100:24_999], []string{"big 25000 changed"}, bigOld[25_000:])
	write(t, dir, pathBig, bigNew)
	write(t, dir, pathAppend, slices.Concat(appendOld, []string{"tail 1", "tail 2"}))
	write(t, dir, pathBigAdded, numbered("added", 20_001))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "two")

	repo, err := source.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	fx := &fixture{repo: repo, dir: dir, last: resolve(t, repo, source.Last())}
	for _, path := range []string{pathBig, pathAppend, pathBigAdded} {
		facts, err := fx.last.Facts(t.Context(), fx.fileIndex(t, path))
		if err != nil {
			t.Fatal(err)
		}
		if !facts.Large {
			t.Fatalf("%s is not large; the fixture does not exercise large files", path)
		}
	}
	return fx
}

// TestLargeFileCommentsStayInsideHunks checks that in a large file only lines inside one hunk, on
// the hunk's side numbering, take line comments; the file still takes a file comment. Checked
// through both entry points: a seed at Start and a comment created in the session.
func TestLargeFileCommentsStayInsideHunks(t *testing.T) {
	t.Parallel()
	fx := newLargeFixture(t)
	tests := []struct {
		name   string
		target review.Target
		valid  bool
	}{
		{"file comment", file(pathBig), true},
		{"first line of the first hunk, new", line(pathBig, review.New, 97), true},
		{"last line of the first hunk, new", line(pathBig, review.New, 104), true},
		{"last line of the first hunk, old", line(pathBig, review.Old, 103), true},
		{"line before the first hunk", line(pathBig, review.New, 96), false},
		{"line after the first hunk, new", line(pathBig, review.New, 105), false},
		{"line after the first hunk, old", line(pathBig, review.Old, 104), false},
		{"context line of the second hunk, old", line(pathBig, review.Old, 24_997), true},
		{"old-only number on the new side", line(pathBig, review.New, 24_997), false},
		{"new-only number on the new side", line(pathBig, review.New, 25_004), true},
		{"new-only number on the old side", line(pathBig, review.Old, 25_004), false},
		{"whole first hunk, new", span(pathBig, review.New, 97, 104), true},
		{"whole first hunk, old", span(pathBig, review.Old, 97, 103), true},
		{"range leaving the old hunk", span(pathBig, review.Old, 97, 104), false},
		{"range entering a hunk", span(pathBig, review.New, 90, 100), false},
		{"range across both hunks", span(pathBig, review.New, 100, 25_000), false},
		{"line 1, outside every hunk", line(pathBig, review.New, 1), false},
		{"append: hunk start, old", line(pathAppend, review.Old, 19_999), true},
		{"append: before the hunk, old", line(pathAppend, review.Old, 19_998), false},
		{"append: appended line", line(pathAppend, review.New, 20_003), true},
		{"append: beyond the file", line(pathAppend, review.New, 20_004), false},
		{"append: line 1", line(pathAppend, review.Old, 1), false},
		{"added: first line", line(pathBigAdded, review.New, 1), true},
		{"added: last line", line(pathBigAdded, review.New, 20_001), true},
		{"added: past the end", line(pathBigAdded, review.New, 20_002), false},
		{"added: old side", line(pathBigAdded, review.Old, 1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := openStore(t, t.TempDir(), fx.key(fx.last))
			s, _, err := review.Start(t.Context(), st, fx.last, review.NewSeed(review.SeedComment{Target: tt.target, Body: "seeded"}))
			if tt.valid != (err == nil) {
				t.Fatalf("Start with the comment as a seed: err = %v; want valid = %v", err, tt.valid)
			}
			if !tt.valid {
				assertSeedProblems(t, err, 1)
				se, _ := errors.AsType[*review.SeedError](err)
				if !strings.Contains(se.Problems[0], tt.target.Path()) {
					t.Errorf("problem %q does not name %s", se.Problems[0], tt.target.Path())
				}
				s, _ = fx.start(t, t.TempDir(), review.NewSeed())
			}
			_, err = s.CreateComment(t.Context(), "human", on(t, s, tt.target), "created")
			if tt.valid {
				if err != nil {
					t.Fatalf("CreateComment: %v", err)
				}
				return
			}
			if _, ok := errors.AsType[*review.InvalidError](err); !ok {
				t.Fatalf("CreateComment: err = %v; want an *InvalidError", err)
			}
			if !strings.Contains(err.Error(), tt.target.Path()) {
				t.Errorf("CreateComment error %q does not name %s", err, tt.target.Path())
			}
			if n := len(s.View().Comments); n != 0 {
				t.Errorf("a rejected comment left %d comments", n)
			}
		})
	}
}
