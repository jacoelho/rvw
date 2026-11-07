package review_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// refreshRepo is a repository reviewed as uncommitted changes: a.txt, gone.txt, kept.txt and
// old.txt are committed, then edited in the worktree.
type refreshRepo struct {
	repo *source.Repo
	dir  string
}

func newRefreshRepo(t *testing.T) *refreshRepo {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", numbered("a", 20))
	write(t, dir, "gone.txt", numbered("gone", 5))
	write(t, dir, "kept.txt", numbered("kept", 5))
	write(t, dir, "old.txt", numbered("old", 20))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "one")
	// The reviewed change: a.txt line 5 and 15 changed, gone.txt and kept.txt changed, old.txt
	// changed at line 3.
	a := numbered("a", 20)
	a[4], a[14] = "a 5 changed", "a 15 changed"
	write(t, dir, "a.txt", a)
	write(t, dir, "gone.txt", append(numbered("gone", 5), "gone extra"))
	write(t, dir, "kept.txt", append(numbered("kept", 5), "kept extra"))
	o := numbered("old", 20)
	o[2] = "old 3 changed"
	write(t, dir, "old.txt", o)
	repo, err := source.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return &refreshRepo{repo: repo, dir: dir}
}

func (r *refreshRepo) snapshot(t *testing.T) *source.Snapshot {
	t.Helper()
	snap := resolve(t, r.repo, source.Uncommitted())
	t.Cleanup(func() { _ = snap.Close() })
	return snap
}

func indexOf(t *testing.T, snap *source.Snapshot, path string) int {
	t.Helper()
	for i := range snap.FileCount() {
		if snap.File(i).Path() == path {
			return i
		}
	}
	t.Fatalf("%q is not a changed file", path)
	return -1
}

func mustCreate(t *testing.T, s *review.Session, id string, target review.Target, body string) {
	t.Helper()
	if _, err := s.CreateComment(t.Context(), id, on(t, s, target), body); err != nil {
		t.Fatalf("create %s on %+v: %v", id, target, err)
	}
}

// TestRefreshCarriesTheDraftOver covers REQ-DRF-006: lines that only shifted keep their comment on
// the same code; changed lines move the comment to its file; a file that left the change moves its
// comments to the review; a renamed file keeps its file comment; viewed marks survive only on
// unchanged files; the summary is kept; the old draft is replaced by one that restores.
func TestRefreshCarriesTheDraftOver(t *testing.T) {
	t.Parallel()
	r := newRefreshRepo(t)
	dir := t.TempDir()
	snap := r.snapshot(t)
	st := openStore(t, dir, review.DraftKey(snap))
	s, _, err := review.Start(t.Context(), st, snap, review.Fresh{})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "shifted", line("a.txt", review.New, 15), "on a 15")
	mustCreate(t, s, "edited", span("a.txt", review.New, 4, 6), "on a 4-6")
	mustCreate(t, s, "old-side", line("a.txt", review.Old, 15), "old a 15")
	mustCreate(t, s, "gone-line", line("gone.txt", review.New, 6), "on gone 6")
	mustCreate(t, s, "gone-file", file("gone.txt"), "on gone")
	mustCreate(t, s, "renamed-file", file("old.txt"), "on old")
	mustCreate(t, s, "renamed-line", line("old.txt", review.New, 3), "on old 3")
	mustCreate(t, s, "whole", reviewTarget, "on the review")
	if err := s.SetSummary("the summary"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetViewed([]int{indexOf(t, snap, "a.txt"), indexOf(t, snap, "kept.txt")}, true); err != nil {
		t.Fatal(err)
	}
	before := commentsByID(s.Draft())

	// Two lines inserted above a.txt line 15 and line 5 changed again; gone.txt back to its
	// committed content; old.txt renamed, unchanged otherwise.
	a := numbered("a", 20)
	a[4], a[14] = "a 5 changed twice", "a 15 changed"
	a = slices.Concat(a[:9], []string{"inserted 1", "inserted 2"}, a[9:])
	write(t, r.dir, "a.txt", a)
	write(t, r.dir, "gone.txt", numbered("gone", 5))
	if err := os.Rename(filepath.Join(r.dir, "old.txt"), filepath.Join(r.dir, "new.txt")); err != nil {
		t.Fatal(err)
	}
	next := r.snapshot(t)
	nextStore := openStore(t, dir, review.DraftKey(next))
	got, err := s.Refresh(t.Context(), next, nextStore)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Unchanged || got.Released != st || got.OldSnapshot != snap || got.MovedAside != "" || got.OldDraftErr != nil {
		t.Fatalf("Refresh = %+v; want a replaced store and snapshot, nothing moved aside", got)
	}
	if s.Snapshot() != next || s.DraftPath() != nextStore.Path() {
		t.Fatal("the session does not review the new snapshot and draft")
	}

	want := map[string]review.Target{
		"shifted":      line("a.txt", review.New, 17),
		"edited":       file("a.txt"),
		"old-side":     line("a.txt", review.Old, 15),
		"gone-line":    reviewTarget,
		"gone-file":    reviewTarget,
		"renamed-file": file("new.txt"),
		"renamed-line": file("new.txt"),
		"whole":        reviewTarget,
	}
	d := s.Draft()
	after := commentsByID(d)
	for id, target := range want {
		c, ok := after[id]
		switch {
		case !ok:
			t.Errorf("comment %s was dropped", id)
		case c.Target != target:
			t.Errorf("comment %s on %+v; want %+v", id, c.Target, target)
		case c.Body != before[id].Body || c.Author != before[id].Author:
			t.Errorf("comment %s = %q by %s; want %q by %s", id, c.Body, c.Author, before[id].Body, before[id].Author)
		case c.Version == before[id].Version:
			t.Errorf("comment %s kept version %d; a refresh gives a new one", id, c.Version)
		case target.Kind() != review.TargetReview && next.File(c.File).Path() != target.Path():
			t.Errorf("comment %s has file %d (%s); want %s", id, c.File, next.File(c.File).Path(), target.Path())
		}
	}
	if len(after) != len(want) {
		t.Errorf("draft has %d comments; want %d", len(after), len(want))
	}
	wantMoved := []review.Moved{
		{ID: "edited", From: span("a.txt", review.New, 4, 6), To: file("a.txt")},
		{ID: "gone-file", From: file("gone.txt"), To: reviewTarget},
		{ID: "gone-line", From: line("gone.txt", review.New, 6), To: reviewTarget},
		{ID: "renamed-line", From: line("old.txt", review.New, 3), To: file("new.txt")},
	}
	gotMoved := slices.Clone(got.Moved)
	slices.SortFunc(gotMoved, func(x, y review.Moved) int { return strings.Compare(x.ID, y.ID) })
	if !reflect.DeepEqual(gotMoved, wantMoved) {
		t.Errorf("Moved =\n%+v\nwant\n%+v", gotMoved, wantMoved)
	}
	if want := []review.Mark{{File: indexOf(t, next, "kept.txt")}}; !reflect.DeepEqual(d.Viewed, want) {
		t.Errorf("viewed = %+v; want only kept.txt %+v", d.Viewed, want)
	}
	if d.Summary != "the summary" {
		t.Errorf("summary = %q; want it kept", d.Summary)
	}
	if fileExists(t, st.Path()) {
		t.Error("the replaced draft was not deleted")
	}

	// The carried draft is saved: a restart on the new snapshot restores it.
	s.End()
	if err := nextStore.Close(); err != nil {
		t.Fatal(err)
	}
	again := openStore(t, dir, review.DraftKey(next))
	s2, o, err := review.Start(t.Context(), again, next, review.Fresh{})
	if err != nil || !o.Resumed() {
		t.Fatalf("restart = resumed %v, %v; want the carried draft", o.Resumed(), err)
	}
	if got := withoutSeqs(s2.Draft()); !reflect.DeepEqual(got, withoutSeqs(d)) {
		t.Fatalf("restored =\n%+v\nwant\n%+v", got, withoutSeqs(d))
	}
}

func commentsByID(d review.Draft) map[string]review.Comment {
	out := map[string]review.Comment{}
	for _, c := range d.Comments {
		out[c.ID] = c
	}
	return out
}

// TestRefreshToTheSameTrees: a snapshot with the current draft key replaces the snapshot only; the
// draft, its file and its versions stay.
func TestRefreshToTheSameTrees(t *testing.T) {
	t.Parallel()
	r := newRefreshRepo(t)
	dir := t.TempDir()
	snap := r.snapshot(t)
	st := openStore(t, dir, review.DraftKey(snap))
	s, _, err := review.Start(t.Context(), st, snap, review.Fresh{})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "c", line("a.txt", review.New, 5), "body")
	before := s.Draft()

	next := r.snapshot(t)
	got, err := s.Refresh(t.Context(), next, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Unchanged || got.Released != nil || got.OldSnapshot != snap || len(got.Moved) != 0 {
		t.Fatalf("Refresh = %+v; want unchanged, the old snapshot released", got)
	}
	if s.Snapshot() != next || s.DraftPath() != st.Path() {
		t.Fatal("same-tree refresh did not swap the snapshot only")
	}
	if after := s.Draft(); !reflect.DeepEqual(after, before) {
		t.Fatalf("draft = %+v; want %+v unchanged", after, before)
	}
}

// TestRefreshRefusals: an ended session refuses, as does a store for the wrong key; neither changes
// the session or writes the new store's draft.
func TestRefreshRefusals(t *testing.T) {
	t.Parallel()
	r := newRefreshRepo(t)
	dir := t.TempDir()
	snap := r.snapshot(t)
	st := openStore(t, dir, review.DraftKey(snap))
	s, _, err := review.Start(t.Context(), st, snap, review.Fresh{})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "c", line("a.txt", review.New, 5), "body")
	write(t, r.dir, "kept.txt", numbered("kept", 5))
	next := r.snapshot(t)

	wrong := openStore(t, dir, "not-the-key")
	if _, err := s.Refresh(t.Context(), next, wrong); err == nil {
		t.Error("Refresh with a store of another key succeeded")
	}
	if _, err := s.Refresh(t.Context(), next, nil); err == nil {
		t.Error("Refresh to new trees without a store succeeded")
	}
	nextStore := openStore(t, dir, review.DraftKey(next))
	s.End()
	if _, err := s.Refresh(t.Context(), next, nextStore); !errors.Is(err, review.ErrEnded) {
		t.Errorf("Refresh after End: err = %v; want ErrEnded", err)
	}
	if s.Snapshot() != snap || fileExists(t, nextStore.Path()) || !fileExists(t, st.Path()) {
		t.Error("a refused refresh changed the session or the drafts")
	}
}

// TestRefreshMovesAnExistingDraftAside: a draft already saved for the new trees, from an earlier
// session, is moved aside rather than overwritten or merged.
func TestRefreshMovesAnExistingDraftAside(t *testing.T) {
	t.Parallel()
	r := newRefreshRepo(t)
	dir := t.TempDir()
	write(t, r.dir, "kept.txt", numbered("kept", 5))
	later := r.snapshot(t)
	earlier := openStore(t, dir, review.DraftKey(later))
	s0, _, err := review.Start(t.Context(), earlier, later, review.Fresh{})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s0, "earlier", reviewTarget, "from an earlier session")
	s0.End()
	if err := earlier.Close(); err != nil {
		t.Fatal(err)
	}

	write(t, r.dir, "kept.txt", append(numbered("kept", 5), "kept extra"))
	snap := r.snapshot(t)
	st := openStore(t, dir, review.DraftKey(snap))
	s, _, err := review.Start(t.Context(), st, snap, review.Fresh{})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "now", reviewTarget, "this session")
	write(t, r.dir, "kept.txt", numbered("kept", 5))
	next := r.snapshot(t)
	nextStore := openStore(t, dir, review.DraftKey(next))
	got, err := s.Refresh(t.Context(), next, nextStore)
	if err != nil {
		t.Fatal(err)
	}
	if got.MovedAside == "" || !fileExists(t, got.MovedAside) {
		t.Fatalf("MovedAside = %q; want the earlier draft's new path", got.MovedAside)
	}
	if ids := slices.Collect(maps.Keys(commentsByID(s.Draft()))); !reflect.DeepEqual(ids, []string{"now"}) {
		t.Fatalf("comments = %v; want only this session's", ids)
	}
}
