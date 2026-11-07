package review

import (
	"context"
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/source"
)

// Moved is a draft comment a refresh moved to a wider target because its target did not survive:
// a line or range comment whose lines changed became a file comment, and a comment whose file left
// the change, or became uncommentable, became a review comment (REQ-DRF-006).
type Moved struct {
	ID       string
	From, To Target
}

// Refreshed is what Refresh did. The caller closes OldSnapshot once nothing reads it.
type Refreshed struct {
	// OldDraftErr is a warning only: when the draft key changed, the replaced draft could not be
	// deleted, so it expires later, or its store could not be closed.
	OldDraftErr error
	// Snapshot is snap, the snapshot the session now serves.
	Snapshot *source.Snapshot
	// OldSnapshot is the replaced snapshot.
	OldSnapshot *source.Snapshot
	// MovedAside is where a draft already saved for the new snapshot was moved; "" when there was
	// none.
	MovedAside string
	// Moved lists the comments moved to a wider target, in result order.
	Moved []Moved
	// Unchanged reports the same draft key and the same file list (source.Snapshot.SameFiles):
	// only the snapshot was replaced, the draft is kept as it is, and file indexes still name the
	// same files. In a partial clone, a blob arriving that leaves a file with none missing makes it
	// false.
	Unchanged bool
}

// ErrBusy is Refresh's error when the draft changed while the refresh carried it over. Nothing
// changed; the refresh may be retried.
var ErrBusy = errors.New("the draft changed during the refresh; try again")

// Refresh moves the open session to snap, a new snapshot of the same source. With the same draft
// key (DraftKey) and the same file list (source.Snapshot.SameFiles) only the snapshot is replaced.
// With the same key and a different file list, the draft is carried over in place into the current
// store, with no move-aside and no delete. With a new key, the draft is carried over into the store
// returned by opener; opener is called at most once, without the session lock, and only when
// DraftKey(snap) differs from the current key. It must return a store locked for exactly that key.
// From then on the session owns that store: Refresh closes it on any later error, including a key
// mismatch, and otherwise closes the replaced store, so Close releases the new one.
//
// Carrying over: a review comment is kept as it is; a file comment goes to the file with the same
// display path, else the one with its file's new or old path, else to the review; a line or range
// comment goes to the same lines of the file with the same path on its side, as the diff from the
// old side's blob to the new side's maps them, else to its file as a file comment would go.
// Comments keep ID, author, body and order and get a new Version. A viewed mark is kept when a file
// has the same paths, modes and blobs. Summary and Hide whitespace are kept. A draft already in the
// new store is moved aside, the new draft saved, and the replaced draft deleted.
//
// Carrying over reads both snapshots and runs git without the lock; mutations meanwhile make it fail
// with ErrBusy, and an end with ErrEnded. On any error the session is unchanged and the caller still
// owns snap.
func (s *Session) Refresh(ctx context.Context, snap *source.Snapshot, opener func(key string) (*Store, error)) (res Refreshed, err error) {
	if opener == nil {
		return Refreshed{}, errors.New("refresh needs a store opener")
	}
	key := DraftKey(snap)
	s.mu.Lock()
	if s.phase != open {
		s.mu.Unlock()
		return Refreshed{}, ErrEnded
	}
	from, st, rev := s.cur, s.state, s.rev
	sameKey := key == s.store.key
	if sameKey && from.snap.SameFiles(snap) {
		s.cur = newView(snap)
		s.mu.Unlock()
		return Refreshed{Unchanged: true, Snapshot: snap, OldSnapshot: from.snap}, nil
	}
	s.mu.Unlock()

	var store *Store
	if !sameKey {
		store, err = opener(key)
		if err != nil {
			return Refreshed{}, fmt.Errorf("open the new snapshot's draft: %w", err)
		}
		if store == nil {
			return Refreshed{}, errors.New("the store opener returned no store")
		}
		if store.key != key {
			return Refreshed{}, errors.Join(fmt.Errorf("opener returned the draft of key %s, want %s", store.key, key), store.Close())
		}
		defer func() {
			if err != nil {
				err = errors.Join(err, store.Close())
			}
		}()
	}

	to := newView(snap)
	next, moved, err := carry(ctx, from, to, st)
	if err != nil {
		return Refreshed{}, fmt.Errorf("carry the draft over: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.phase != open:
		return Refreshed{}, ErrEnded
	case s.rev != rev:
		return Refreshed{}, ErrBusy
	}
	var aside string
	if store != nil {
		if aside, err = store.moveAsideIfAny(); err != nil {
			return Refreshed{}, err
		}
	}
	for id, c := range next.comments {
		c.Version = s.nextVersion()
		next.comments[id] = c
	}
	target := s.store
	if store != nil {
		target = store
	}
	if err := target.save(encode(snap, next)); err != nil {
		return Refreshed{}, err
	}
	r := Refreshed{Snapshot: snap, OldSnapshot: from.snap, MovedAside: aside, Moved: moved}
	if store != nil {
		r.OldDraftErr = errors.Join(s.store.delete(), s.store.Close())
		s.store = store
	}
	s.cur, s.state = to, next
	s.rev++
	return r, nil
}

// carry returns st, whose file indexes refer to from, carried over to to, and the comments it moved
// to a wider target, in result order.
func carry(ctx context.Context, from, to *view, st state) (state, []Moved, error) {
	next := emptyState()
	next.summary, next.bytes, next.hideWhitespace = st.summary, st.bytes, st.hideWhitespace
	val := validator{snap: to.snap, paths: to.paths}
	diffs := map[sideFile]diffResult{}
	var moved []Moved
	for _, c := range sorted(st) {
		t, file, err := carryTarget(ctx, from, to, &val, diffs, c)
		if err != nil {
			return state{}, nil, commentError(c.ID, err)
		}
		// Lines that only shifted, or a file comment following its file's new path, stay on what
		// they were on: only a wider target is a move.
		if t.Kind() != c.Target.Kind() {
			moved = append(moved, Moved{ID: c.ID, From: c.Target, To: t})
		}
		c.Target, c.File = t, file
		next.comments[c.ID] = c
	}
	for i, ws := range st.viewed {
		if j, ok := sameFile(from, to, i); ok {
			next.viewed[j] = ws
		}
	}
	return next, moved, nil
}

// sideFile is one side of a file of the new snapshot, diffed from one blob.
type sideFile struct {
	from    source.Blob
	file    int
	newSide bool
}

type diffResult struct {
	hunks []source.HunkRange
	ok    bool // false when the sides cannot be diffed as lines
}

// carryTarget returns where c, a comment of from, goes in to.
func carryTarget(ctx context.Context, from, to *view, val *validator, diffs map[sideFile]diffResult, c Comment) (Target, int, error) {
	kind := c.Target.Kind()
	if kind == TargetReview {
		return c.Target, -1, nil
	}
	if kind == TargetLine || kind == TargetRange {
		t, file, ok, err := carryLines(ctx, from, to, val, diffs, c)
		if err != nil || ok {
			return t, file, err
		}
	}
	return carryFile(from, to, c)
}

// carryLines maps a line or range comment to the same lines in to; ok is false when they did not
// survive.
func carryLines(ctx context.Context, from, to *view, val *validator, diffs map[sideFile]diffResult, c Comment) (Target, int, bool, error) {
	side := c.Target.side
	m := to.paths.new
	if side == Old {
		m = to.paths.old
	}
	j, found := m[c.Target.path]
	if !found {
		return Target{}, 0, false, nil
	}
	key := sideFile{from: from.snap.File(c.File).Side(side == New), file: j, newSide: side == New}
	d, seen := diffs[key]
	if !seen {
		hunks, err := to.snap.ChangesFrom(ctx, j, key.newSide, key.from)
		switch {
		case ctx.Err() != nil:
			return Target{}, 0, false, errors.Join(ctx.Err(), err)
		case errors.Is(err, source.ErrMissing), errors.Is(err, source.ErrNotText):
		case err != nil:
			return Target{}, 0, false, err
		default:
			d = diffResult{hunks: hunks, ok: true}
		}
		diffs[key] = d
	}
	if !d.ok {
		return Target{}, 0, false, nil
	}
	start, end := c.Target.Lines()
	start, end, ok := source.MapLines(d.hunks, start, end)
	if !ok {
		return Target{}, 0, false, nil
	}
	t := c.Target
	t.start, t.end = start, end
	if err := val.checkAt(ctx, j, t); err != nil {
		if _, invalidTarget := errors.AsType[*InvalidError](err); invalidTarget {
			return Target{}, 0, false, nil
		}
		return Target{}, 0, false, err
	}
	return t, j, true, nil
}

// carryFile places c on its file in to as a file comment, or on the review when no commentable file
// matches: the one with c's display path, else c's file's new path, else its old path.
func carryFile(from, to *view, c Comment) (Target, int, error) {
	f := from.snap.File(c.File)
	j, ok := to.paths.display[f.Path()]
	if !ok && f.NewPath != "" {
		j, ok = to.paths.new[f.NewPath]
	}
	if !ok && f.OldPath != "" {
		j, ok = to.paths.old[f.OldPath]
	}
	if !ok || !to.paths.commentable[j] {
		return ReviewTarget(), -1, nil
	}
	t, err := FileTarget(to.snap.File(j).Path())
	return t, j, err
}

// sameFile returns the file of to that is file i of from: the same paths, modes and blobs.
func sameFile(from, to *view, i int) (int, bool) {
	f := from.snap.File(i)
	m, path := to.paths.new, f.NewPath
	if path == "" {
		m, path = to.paths.old, f.OldPath
	}
	j, ok := m[path]
	if !ok {
		return 0, false
	}
	g := to.snap.File(j)
	same := f.OldPath == g.OldPath && f.NewPath == g.NewPath && f.Side(false) == g.Side(false) && f.Side(true) == g.Side(true)
	return j, same
}
