package review_test

import (
	"context"
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
	got, err := s.Refresh(t.Context(), next, returns(nextStore))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Unchanged || got.Snapshot != next || got.OldSnapshot != snap || got.MovedAside != "" || got.OldDraftErr != nil {
		t.Fatalf("Refresh = %+v; want a replaced snapshot, nothing moved aside", got)
	}
	if s.Snapshot() != next || s.DraftPath() != nextStore.Path() {
		t.Fatal("the session does not review the new snapshot and draft")
	}
	// The session closed the replaced store, so its key's lock is free.
	openStore(t, dir, review.DraftKey(snap))

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
	if err := s.Close(); err != nil {
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
	got, err := s.Refresh(t.Context(), next, neverOpens(t))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Unchanged || got.Snapshot != next || got.OldSnapshot != snap || len(got.Moved) != 0 {
		t.Fatalf("Refresh = %+v; want unchanged, the old snapshot released", got)
	}
	if s.Snapshot() != next || s.DraftPath() != st.Path() {
		t.Fatal("same-tree refresh did not swap the snapshot only")
	}
	if after := s.Draft(); !reflect.DeepEqual(after, before) {
		t.Fatalf("draft = %+v; want %+v unchanged", after, before)
	}
}

// returns is an opener that hands out st.
func returns(st *review.Store) func(string) (*review.Store, error) {
	return func(string) (*review.Store, error) { return st, nil }
}

// neverOpens is an opener that fails the test when called.
func neverOpens(t *testing.T) func(string) (*review.Store, error) {
	t.Helper()
	return func(key string) (*review.Store, error) {
		t.Errorf("the opener was called for key %s", key)
		return nil, errors.New("the opener must not be called")
	}
}

// TestRefreshRefusals: an ended session refuses, as do a nil opener, an opener that fails and one
// that returns the store of another key; none changes the session or writes the new store's draft,
// and a store the opener returned is closed.
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
	before := s.Draft()
	write(t, r.dir, "kept.txt", numbered("kept", 5))
	next := r.snapshot(t)
	unchanged := func(what string) {
		t.Helper()
		if s.Snapshot() != snap || s.DraftPath() != st.Path() || !reflect.DeepEqual(s.Draft(), before) {
			t.Errorf("%s changed the session", what)
		}
	}

	wrong, err := review.OpenStore(dir, "not-the-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(t.Context(), next, returns(wrong)); err == nil {
		t.Error("Refresh with a store of another key succeeded")
	}
	unchanged("an opener returning another key")
	// The refresh closed the wrong store, so its lock is free.
	again, err := review.OpenStore(dir, "not-the-key")
	if err != nil {
		t.Errorf("the store of the wrong key was left open: %v", err)
	} else if err := again.Close(); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("no store")
	if _, err := s.Refresh(t.Context(), next, func(string) (*review.Store, error) { return nil, sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("Refresh with a failing opener: err = %v; want the opener's error", err)
	}
	unchanged("a failing opener")

	if _, err := s.Refresh(t.Context(), next, func(string) (*review.Store, error) { return nil, nil }); err == nil { //nolint:nilnil // the contract violation under test
		t.Error("Refresh with an opener returning no store succeeded")
	}
	unchanged("an opener returning no store")

	if _, err := s.Refresh(t.Context(), next, nil); err == nil {
		t.Error("Refresh with a nil opener succeeded")
	}
	unchanged("a nil opener")

	nextStore := openStore(t, dir, review.DraftKey(next))
	s.End()
	if _, err := s.Refresh(t.Context(), next, neverOpens(t)); !errors.Is(err, review.ErrEnded) {
		t.Errorf("Refresh after End: err = %v; want ErrEnded", err)
	}
	if s.Snapshot() != snap || fileExists(t, nextStore.Path()) || !fileExists(t, st.Path()) {
		t.Error("a refused refresh changed the session or the drafts")
	}
}

// TestRefreshClosesTheOpenedStoreOnFailure: when carrying the draft over fails after the opener
// returned the new key's store, Refresh closes that store and leaves the session as it was.
func TestRefreshClosesTheOpenedStoreOnFailure(t *testing.T) {
	t.Parallel()
	r := newRefreshRepo(t)
	dir := t.TempDir()
	snap := r.snapshot(t)
	st := openStore(t, dir, review.DraftKey(snap))
	s, _, err := review.Start(t.Context(), st, snap, review.Fresh{})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "c", line("a.txt", review.New, 15), "body")
	write(t, r.dir, "kept.txt", numbered("kept", 5))
	next := r.snapshot(t)
	key := review.DraftKey(next)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.Refresh(ctx, next, func(k string) (*review.Store, error) { return review.OpenStore(dir, k) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh: err = %v; want context.Canceled", err)
	}
	if s.Snapshot() != snap || s.DraftPath() != st.Path() {
		t.Error("a failed refresh changed the session")
	}
	// The failed refresh closed the store it opened and saved nothing to it.
	opened, err := review.OpenStore(dir, key)
	if err != nil {
		t.Fatalf("the opened store was left locked: %v", err)
	}
	defer func() { _ = opened.Close() }()
	if fileExists(t, opened.Path()) {
		t.Error("a failed refresh wrote the new draft")
	}
}

// newListingRepo returns a repository whose last commit renames a.txt to b.txt with an edit and appends to
// c.txt. With the old a.txt blob present the listing is [b.txt R, c.txt M]; without it git is not
// asked for renames and it is [a.txt D, b.txt A, c.txt M].
func newListingRepo(t *testing.T) (*source.Repo, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", numbered("a", 20))
	write(t, dir, "c.txt", numbered("c", 10))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "one")
	git(t, dir, "mv", "a.txt", "b.txt")
	b := numbered("a", 20)
	b[6] = "a 7 edited"
	write(t, dir, "b.txt", b)
	write(t, dir, "c.txt", append(numbered("c", 10), "c extra"))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "two")
	repo, err := source.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, dir
}

// TestRefreshToTheSameTreesWithAnotherListing covers REQ-DRF-006 and the basis of REQ-SES-015
// (file indices belong to one snapshot): a blob arriving changes the listing, not the trees, so
// the draft key is the same but the file indexes are not. Comments must keep their paths and
// quote their own file's lines, and a viewed mark must not follow an index to another file. That
// Unchanged is false for the same draft key encodes contract revision 11.
func TestRefreshToTheSameTreesWithAnotherListing(t *testing.T) {
	t.Parallel()
	repo, dir := newListingRepo(t)
	restore := hideBlob(t, dir, "HEAD~1:a.txt")
	first := resolve(t, repo, source.Last())
	t.Cleanup(func() { _ = first.Close() })
	restore()
	next := resolve(t, repo, source.Last())
	t.Cleanup(func() { _ = next.Close() })
	if first.RenameDetection() != source.RenameDetectionMissingBlobs || first.FileCount() != 3 ||
		next.RenameDetection() != source.RenameDetectionFull || next.FileCount() != 2 {
		t.Fatalf("listings = %v/%d and %v/%d; want the degraded one of 3 and the full one of 2",
			first.RenameDetection(), first.FileCount(), next.RenameDetection(), next.FileCount())
	}
	if review.DraftKey(first) != review.DraftKey(next) {
		t.Fatal("the snapshots have different draft keys")
	}

	drafts := t.TempDir()
	st := openStore(t, drafts, review.DraftKey(first))
	s, _, err := review.Start(t.Context(), st, first, review.Fresh{})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "L", line("b.txt", review.New, 3), "on b 3")
	mustCreate(t, s, "F", file("b.txt"), "on b")
	mustCreate(t, s, "C", line("c.txt", review.New, 11), "on c 11")
	mustCreate(t, s, "R", reviewTarget, "on the review")
	if err := s.SetViewed([]int{indexOf(t, first, "b.txt"), indexOf(t, first, "c.txt")}, true); err != nil {
		t.Fatal(err)
	}
	before := commentsByID(s.Draft())

	got, err := s.Refresh(t.Context(), next, neverOpens(t))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// Contract revision 11: another file list is not unchanged.
	if got.Unchanged || got.OldDraftErr != nil || got.Snapshot != next || got.OldSnapshot != first || len(got.Moved) != 0 {
		t.Fatalf("Refresh = %+v; want a carried-over draft in the same store", got)
	}
	d := s.Draft()
	after := commentsByID(d)
	want := map[string]review.Target{
		"L": line("b.txt", review.New, 3), "F": file("b.txt"), "C": line("c.txt", review.New, 11), "R": reviewTarget,
	}
	for id, target := range want {
		c, ok := after[id]
		switch {
		case !ok:
			t.Errorf("comment %s was dropped", id)
		case c.Target != target:
			t.Errorf("comment %s on %+v; want %+v", id, c.Target, target)
		case c.Version == before[id].Version:
			t.Errorf("comment %s kept version %d; a refresh gives a new one", id, c.Version)
		case target.Kind() != review.TargetReview && next.File(c.File).Path() != target.Path():
			t.Errorf("comment %s has file %d (%s); want %s", id, c.File, next.File(c.File).Path(), target.Path())
		}
	}
	quotes, err := s.Quotes(t.Context(), []string{"L", "C"})
	if err != nil {
		t.Fatal(err)
	}
	for id, code := range map[string]string{"L": "a 3", "C": "c extra"} {
		if q := quotes[id].Code; !slices.Equal(q, []string{code}) {
			t.Errorf("comment %s quotes %q; want %q", id, q, code)
		}
	}
	// b.txt was added and is now a rename: its paths differ, so its mark is dropped.
	if want := []review.Mark{{File: indexOf(t, next, "c.txt")}}; !reflect.DeepEqual(d.Viewed, want) {
		t.Errorf("viewed = %+v; want only c.txt %+v", d.Viewed, want)
	}
	if !fileExists(t, st.Path()) || s.DraftPath() != st.Path() {
		t.Error("the draft is not saved in the same store")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again := openStore(t, drafts, review.DraftKey(next))
	s2, o, err := review.Start(t.Context(), again, next, review.Fresh{})
	if err != nil || !o.Resumed() {
		t.Fatalf("restart = resumed %v, %v; want the carried draft", o.Resumed(), err)
	}
	if got := withoutSeqs(s2.Draft()); !reflect.DeepEqual(got, withoutSeqs(d)) {
		t.Fatalf("restored =\n%+v\nwant\n%+v", got, withoutSeqs(d))
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
	if err := s0.Close(); err != nil {
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
	got, err := s.Refresh(t.Context(), next, returns(nextStore))
	if err != nil {
		t.Fatal(err)
	}
	if s.DraftPath() != nextStore.Path() || got.Snapshot != next {
		t.Fatalf("Refresh = %+v; want the opened store and snapshot", got)
	}
	if got.MovedAside == "" || !fileExists(t, got.MovedAside) {
		t.Fatalf("MovedAside = %q; want the earlier draft's new path", got.MovedAside)
	}
	if ids := slices.Collect(maps.Keys(commentsByID(s.Draft()))); !reflect.DeepEqual(ids, []string{"now"}) {
		t.Fatalf("comments = %v; want only this session's", ids)
	}
}
