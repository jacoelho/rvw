package server

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// Files 0..3: 0 plain, 1 vendored, 2 generated, 3 plain.
const (
	plainA    = 0
	vendoredF = 1
	genF      = 2
	plainB    = 3
)

func newFacts() *contentFacts {
	return &contentFacts{vendored: []bool{false, true, false, false}}
}

func genMarks() []bool { return []bool{false, false, true, false} }

func countAll(c *contentFacts) {
	c.count(plainA, source.LineCount{Added: 1, Deleted: 2})
	c.count(vendoredF, source.LineCount{Added: 3, Deleted: 4})
	c.count(genF, source.LineCount{Added: 5, Deleted: 6})
	c.count(plainB, source.LineCount{Added: 7, Deleted: 8})
}

var wantPlain = []fileCount{
	{plainA, source.LineCount{Added: 1, Deleted: 2}},
	{plainB, source.LineCount{Added: 7, Deleted: 8}},
}

// TestFactsPublishOnlyCountedFiles: whether the counts arrive before or after the marks, the
// stream holds exactly the plain files' counts in arrival order, and nothing before the marks.
func TestFactsPublishOnlyCountedFiles(t *testing.T) {
	t.Parallel()
	t.Run("counts before marks", func(t *testing.T) {
		t.Parallel()
		c := newFacts()
		countAll(c)
		if v := c.since(0, false); len(v.counts) != 0 || v.next != 0 || v.generated != nil || v.genDone {
			t.Fatalf("before marks: %+v; want nothing published", v)
		}
		c.mark(genMarks(), nil)
		checkPublished(t, c, wantPlain)
	})
	t.Run("marks before counts", func(t *testing.T) {
		t.Parallel()
		c := newFacts()
		c.mark(genMarks(), nil)
		countAll(c)
		checkPublished(t, c, wantPlain)
	})
}

func checkPublished(t *testing.T, c *contentFacts, want []fileCount) {
	t.Helper()
	v := c.since(0, false)
	if !v.genDone || !slices.Equal(v.generated, []int{genF}) || v.genErr != "" {
		t.Errorf("marks: done %v generated %v err %q; want done [%d] none", v.genDone, v.generated, v.genErr, genF)
	}
	if !slices.Equal(v.counts, want) || v.next != len(want) {
		t.Errorf("counts %+v next %d; want %+v next %d", v.counts, v.next, want, len(want))
	}
}

// TestFactsFailedWhitespaceMarksNothing: a failed whitespace-only read publishes no file, only its
// error; a complete read publishes every file found.
func TestFactsFailedWhitespaceMarksNothing(t *testing.T) {
	t.Parallel()
	c := newFacts()
	c.markWhitespace([]int{1}, errors.New("numstat failed"))
	if v := c.since(0, false); !v.wsDone || v.ws != nil || v.wsErr != "numstat failed" {
		t.Errorf("failed read: done %v files %v err %q; want done, none, numstat failed", v.wsDone, v.ws, v.wsErr)
	}
	ok := newFacts()
	ok.markWhitespace([]int{1, 3}, nil)
	if v := ok.since(0, false); !v.wsDone || !slices.Equal(v.ws, []int{1, 3}) || v.wsErr != "" {
		t.Errorf("complete read: done %v files %v err %q; want done, [1 3], no error", v.wsDone, v.ws, v.wsErr)
	}
}

// TestFactsFailedMarksCountEveryUnvendoredFile: when the generated read fails, its error is
// reported with the marks and every file that is not vendored is counted.
func TestFactsFailedMarksCountEveryUnvendoredFile(t *testing.T) {
	t.Parallel()
	want := []fileCount{
		{plainA, source.LineCount{Added: 1, Deleted: 2}},
		{genF, source.LineCount{Added: 5, Deleted: 6}},
		{plainB, source.LineCount{Added: 7, Deleted: 8}},
	}
	for name, before := range map[string]bool{"counts before marks": true, "marks before counts": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newFacts()
			if before {
				countAll(c)
			}
			c.mark(nil, errors.New("grep failed"))
			if !before {
				countAll(c)
			}
			v := c.since(0, false)
			if !v.genDone || v.generated != nil || v.genErr != "grep failed" {
				t.Errorf("marks: done %v generated %v err %q; want done, none, grep failed", v.genDone, v.generated, v.genErr)
			}
			if !slices.Equal(v.counts, want) {
				t.Errorf("counts %+v; want %+v", v.counts, want)
			}
			if later := c.since(1, false); later.genDone || later.genErr != "" || later.generated != nil {
				t.Errorf("since(1) carries marks: %+v", later)
			}
		})
	}
}

// TestFactsCursor: a cursor past 0 gets only newer counts and no marks; out-of-range cursors clamp.
func TestFactsCursor(t *testing.T) {
	t.Parallel()
	c := newFacts()
	c.mark(genMarks(), nil)
	c.count(plainA, source.LineCount{Added: 1, Deleted: 2})
	next := c.since(0, false).next
	c.count(plainB, source.LineCount{Added: 7, Deleted: 8})
	v := c.since(next, false)
	if !slices.Equal(v.counts, wantPlain[1:]) || v.next != 2 || v.generated != nil || v.genDone {
		t.Errorf("since(%d) = %+v; want only file %d's count, no marks", next, v, plainB)
	}
	if v := c.since(99, false); len(v.counts) != 0 || v.next != 2 {
		t.Errorf("since(99) = %+v; want no counts, next 2", v)
	}
	if v := c.since(-1, false); !slices.Equal(v.counts, wantPlain) || !v.genDone {
		t.Errorf("since(-1) = %+v; want the whole stream with marks", v)
	}
}

// TestFactsFinishCounts: the end of the count read is reported, with its error if any.
func TestFactsFinishCounts(t *testing.T) {
	t.Parallel()
	ok := newFacts()
	ok.finishCounts(nil)
	if v := ok.since(0, false); !v.countDone || v.countErr != "" {
		t.Errorf("finishCounts(nil): done %v err %q; want done, no error", v.countDone, v.countErr)
	}
	failed := newFacts()
	failed.finishCounts(errors.New("numstat failed"))
	if v := failed.since(0, false); !v.countDone || v.countErr != "numstat failed" {
		t.Errorf("finishCounts(err): done %v err %q; want done, numstat failed", v.countDone, v.countErr)
	}
}

// TestFactsConcurrentViews: under any interleaving of counts and marks, a view never holds a
// count without the marks, nor a count for a vendored or generated file. Run with -race.
func TestFactsConcurrentViews(t *testing.T) {
	t.Parallel()
	c := newFacts()
	var wg sync.WaitGroup
	wg.Go(func() { countAll(c) })
	wg.Go(func() { c.mark(genMarks(), nil) })
	check := func(v factsView) {
		if len(v.counts) > 0 && !v.genDone {
			t.Errorf("counts %+v without marks", v.counts)
		}
		for _, fc := range v.counts {
			if fc.i == vendoredF || fc.i == genF {
				t.Errorf("count published for file %d", fc.i)
			}
		}
	}
	for range 1000 {
		check(c.since(0, false))
	}
	wg.Wait()
	check(c.since(0, false))
	if v := c.since(0, false); len(v.counts) != len(wantPlain) {
		t.Errorf("final counts %+v; want %+v", v.counts, wantPlain)
	}
}
