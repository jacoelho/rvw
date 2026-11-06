package review_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

// TestFreshStartsTheDraft: without a draft to restore, the session starts with the preset viewed
// marks, each keeping its whitespace flag, and the Hide whitespace setting (REQ-RND-009,
// REQ-DIFF-012), and writes them so a restart keeps them.
func TestFreshStartsTheDraft(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	dir := t.TempDir()
	iA, iB := fx.fileIndex(t, pathA), fx.fileIndex(t, pathB)
	st := openStore(t, dir, fx.key(fx.last))
	s, o, err := review.Start(t.Context(), st, fx.last, review.Fresh{
		Viewed: []review.Mark{{File: iB, Whitespace: true}, {File: iA}}, HideWhitespace: true,
	})
	if err != nil || o.Resumed() {
		t.Fatalf("Start = resumed %v, %v; want a new draft", o.Resumed(), err)
	}
	want := review.Draft{
		Comments: []review.Comment{}, HideWhitespace: true,
		Viewed: []review.Mark{{File: min(iA, iB), Whitespace: iB < iA}, {File: max(iA, iB), Whitespace: iB > iA}},
	}
	if got := s.Draft(); !reflect.DeepEqual(got.Viewed, want.Viewed) || !got.HideWhitespace || len(got.Comments) != 0 {
		t.Fatalf("view = %+v; want %+v", got, want)
	}
	if !fileExists(t, st.Path()) {
		t.Fatal("a draft with preset marks was not written")
	}
	s.End()
	assertDraftRestores(t, fx, st, s.Draft())
}

// TestRestoredDraftIgnoresThePreset: a restored draft keeps the marks the human cleared; the preset
// applies only to a new draft.
func TestRestoredDraftIgnoresThePreset(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	dir := t.TempDir()
	iA, iB := fx.fileIndex(t, pathA), fx.fileIndex(t, pathB)
	preset := review.Fresh{Viewed: []review.Mark{{File: iA}, {File: iB}}}
	st := openStore(t, dir, fx.key(fx.last))
	s, _, err := review.Start(t.Context(), st, fx.last, preset)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetViewed([]int{iA}, false); err != nil {
		t.Fatal(err)
	}
	s.End()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = openStore(t, dir, fx.key(fx.last))
	s, o, err := review.Start(t.Context(), st, fx.last, preset)
	if err != nil || !o.Resumed() {
		t.Fatalf("restart = resumed %v, %v; want the draft resumed", o.Resumed(), err)
	}
	if got, want := s.Draft().Viewed, []review.Mark{{File: iB}}; !reflect.DeepEqual(got, want) {
		t.Errorf("restored marks = %+v; want %+v (the cleared mark stays cleared)", got, want)
	}
}

// TestEmptyFreshWritesNoDraft: a new draft with nothing in it is not written.
func TestEmptyFreshWritesNoDraft(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	_, st := fx.start(t, t.TempDir(), review.NewSeed())
	if fileExists(t, st.Path()) {
		t.Fatal("an empty new draft was written")
	}
}

func TestFreshRejectsAnInvalidMark(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	for name, marks := range map[string][]review.Mark{
		"a negative index":      {{File: -1}},
		"an index past the end": {{File: fx.last.FileCount()}},
		"two marks on a file":   {{File: 0}, {File: 0, Whitespace: true}},
	} {
		st := openStore(t, t.TempDir(), fx.key(fx.last))
		if _, _, err := review.Start(t.Context(), st, fx.last, review.Fresh{Viewed: marks}); err == nil {
			t.Errorf("Start with %s succeeded; want an error", name)
		}
		if fileExists(t, st.Path()) {
			t.Errorf("Start with %s wrote a draft", name)
		}
	}
}

// TestSetViewedBatch: one call sets or clears several marks, all or none (REQ-NAV-010); a mark
// records the Hide whitespace setting when it is set, also when it was already set.
func TestSetViewedBatch(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	iA, iB, iD := fx.fileIndex(t, pathA), fx.fileIndex(t, pathB), fx.fileIndex(t, pathDeleted)
	marks := func() map[int]bool {
		m := map[int]bool{}
		for _, v := range s.Draft().Viewed {
			m[v.File] = v.Whitespace
		}
		return m
	}
	if err := s.SetViewed([]int{iA, iB}, true); err != nil {
		t.Fatal(err)
	}
	if got, want := marks(), map[int]bool{iA: false, iB: false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after setting a and b: %v; want %v", got, want)
	}

	draft := readFile(t, st.Path())
	err := s.SetViewed([]int{iD, fx.last.FileCount()}, true)
	if _, ok := errors.AsType[*review.InvalidError](err); !ok {
		t.Fatalf("a batch with an out-of-range index: err = %v; want *InvalidError", err)
	}
	if got, want := marks(), map[int]bool{iA: false, iB: false}; !reflect.DeepEqual(got, want) {
		t.Errorf("a refused batch changed the marks: %v; want %v", got, want)
	}
	if got := readFile(t, st.Path()); got != draft {
		t.Error("a refused batch rewrote the draft")
	}

	if err := s.SetHideWhitespace(true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetViewed([]int{iB, iD}, true); err != nil {
		t.Fatal(err)
	}
	if got, want := marks(), map[int]bool{iA: false, iB: true, iD: true}; !reflect.DeepEqual(got, want) {
		t.Errorf("after setting b and del with whitespace hidden: %v; want %v", got, want)
	}
	if err := s.SetViewed([]int{iA, iD}, false); err != nil {
		t.Fatal(err)
	}
	if got, want := marks(), map[int]bool{iB: true}; !reflect.DeepEqual(got, want) {
		t.Errorf("after clearing a and del: %v; want %v", got, want)
	}
	s.End()
	want := s.Draft()
	assertDraftRestores(t, fx, st, want)
}

// TestCopyCommentPlacement: a copy keeps the earlier comment's author and body and goes to the
// first placement the snapshot accepts, else the review (REQ-CMT-011).
func TestCopyCommentPlacement(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	iA := fx.fileIndex(t, pathA)
	for name, c := range map[string]struct {
		places []review.Placement
		want   review.Target
	}{
		"the placed line":           {[]review.Placement{review.OnLine(iA, review.New, 3), review.OnFile(iA)}, line(pathA, review.New, 3)},
		"a range":                   {[]review.Placement{review.OnRange(iA, review.Old, 2, 4)}, span(pathA, review.Old, 2, 4)},
		"the file after a bad line": {[]review.Placement{review.OnLine(iA, review.New, 99), review.OnFile(iA)}, file(pathA)},
		"the review after none":     {[]review.Placement{review.OnLine(iA, review.New, 99)}, reviewTarget},
		"the review with no places": {nil, reviewTarget},
	} {
		from := "e" + strings.ReplaceAll(name, " ", "-")
		got, err := s.CopyComment(t.Context(), "copy-"+from, from, review.Agent, "earlier: "+name, c.places...)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got.Target != c.want || got.Author != review.Agent || got.Body != "earlier: "+name || got.CopiedFrom != from {
			t.Errorf("%s: copy = %+v; want target %+v, author agent, the earlier body, copied from %s", name, got, c.want, from)
		}
	}
}

// TestCopyCommentOnce: copying an earlier comment again returns the copy already made and writes
// nothing, whatever the new id, also after a restart; once the copy is deleted, copying adds one.
func TestCopyCommentOnce(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	dir := t.TempDir()
	s, st := fx.start(t, dir, review.NewSeed())
	first, err := s.CopyComment(t.Context(), "copy1", "0123456789abcdef", review.Human, "still wrong", review.OnFile(fx.fileIndex(t, pathB)))
	if err != nil {
		t.Fatal(err)
	}
	draft := readFile(t, st.Path())
	again, err := s.CopyComment(t.Context(), "copy2", "0123456789abcdef", review.Human, "still wrong")
	if err != nil || again != first {
		t.Fatalf("second copy = %+v, %v; want %+v", again, err, first)
	}
	if got := readFile(t, st.Path()); got != draft {
		t.Error("a repeated copy rewrote the draft")
	}
	s.End()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st = openStore(t, filepath.Clean(dir), fx.key(fx.last))
	s, o, err := review.Start(t.Context(), st, fx.last, review.Fresh{})
	if err != nil || !o.Resumed() {
		t.Fatalf("restart = resumed %v, %v", o.Resumed(), err)
	}
	restored, err := s.CopyComment(t.Context(), "copy3", "0123456789abcdef", review.Human, "still wrong")
	if err != nil || restored.ID != "copy1" || len(s.Draft().Comments) != 1 {
		t.Fatalf("copy after restart = %+v, %v with %d comments; want copy1 alone", restored, err, len(s.Draft().Comments))
	}
	if err := s.DeleteComment(restored.ID, restored.Version); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.CopyComment(t.Context(), "copy4", "0123456789abcdef", review.Human, "still wrong")
	if err != nil || fresh.ID != "copy4" {
		t.Fatalf("copy after deleting the copy = %+v, %v; want a new comment copy4", fresh, err)
	}
	if _, err := s.CopyComment(t.Context(), "copy4", "fedcba9876543210", review.Human, "other"); !errors.Is(err, review.ErrCommentExists) {
		t.Errorf("copying another earlier comment onto a taken id: err = %v; want ErrCommentExists", err)
	}
}

// TestCopiedFromIsNotDelivered: the result names neither the earlier comment nor the copy's origin
// (REQ-CMT-012).
func TestCopiedFromIsNotDelivered(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	if _, err := s.CopyComment(t.Context(), "copy1", "0123456789abcdef", review.Human, "copied body"); err != nil {
		t.Fatal(err)
	}
	d, err := s.Finish(t.Context(), func(review.Result) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	js, err := review.RenderJSON(d.Result)
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"markdown": string(review.RenderMarkdown(d.Result)), "json": string(js)} {
		if !strings.Contains(out, "copied body") {
			t.Errorf("%s result lacks the copied comment:\n%s", name, out)
		}
		if strings.Contains(out, "0123456789abcdef") || strings.Contains(out, "copied_from") {
			t.Errorf("%s result names the earlier comment:\n%s", name, out)
		}
	}
}
