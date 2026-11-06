package review

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/source"
)

// Session is the one writer of a snapshot's draft. Every mutation is validated, persisted, and only
// then applied. Once Finish starts, or after Cancel or End, it rejects all mutations.
type Session struct {
	snap  *source.Snapshot
	store *Store
	// abort cancels the result being built while the session is finishing.
	abort   context.CancelFunc
	paths   pathIndex
	state   state
	nextSeq int64
	// version is the last Comment.Version assigned.
	version int64
	mu      sync.Mutex // guards state, nextSeq, version, phase and abort
	phase   phase
}

// phase is the session's lifecycle. open accepts mutations. finishing builds the result from a
// fixed state and rejects mutations, but End or Abandon still interrupts it, or a failed build
// reopens it. Every other phase is terminal and accepts nothing: delivering, then delivered or
// undelivered, once a finish has built its result, or abandoned when Abandon gave up waiting for
// it; cancelled after Cancel; interrupted after End or Abandon.
type phase int

const (
	open phase = iota
	finishing
	delivering
	delivered
	undelivered
	abandoned
	cancelled
	interrupted
)

// state is the draft content. It is copied before a mutation so a failed save leaves it intact.
type state struct {
	comments map[string]Comment
	viewed   map[int]bool
	summary  string
	bytes    int // total body bytes
}

func emptyState() state {
	return state{comments: map[string]Comment{}, viewed: map[int]bool{}}
}

func (s state) clone() state {
	c := s
	c.comments = make(map[string]Comment, len(s.comments)+1)
	maps.Copy(c.comments, s.comments)
	c.viewed = make(map[int]bool, len(s.viewed)+1)
	maps.Copy(c.viewed, s.viewed)
	return c
}

// Opening says how Start found the draft.
type Opening struct {
	movedReason error
	movedTo     string
	resumed     bool
}

// Resumed reports that a saved draft was restored and the seed ignored.
func (o Opening) Resumed() bool { return o.resumed }

// MovedAside reports a saved draft that could not be restored: it was moved to path, and reason
// says why. The session then started as if there were no draft.
func (o Opening) MovedAside() (path string, ok bool, reason error) {
	return o.movedTo, o.movedTo != "", o.movedReason
}

// Snapshot returns the snapshot the session reviews; every file index the session accepts or
// reports refers to it.
func (s *Session) Snapshot() *source.Snapshot { return s.snap }

// Start opens the session for snap. It validates seed first, so an invalid seed fails even when a
// draft exists. It then restores the draft in store. A draft that no longer validates against snap,
// or that is unreadable as a draft, is moved aside and the session starts without it. Without a
// restored draft, seed applies as agent comments.
//
// Restoring and seeding read each commented file's facts: one patch run, plus a line count of
// each side for a file with line comments.
func Start(ctx context.Context, store *Store, snap *source.Snapshot, seed Seed) (*Session, Opening, error) {
	s := &Session{snap: snap, store: store, paths: newPathIndex(snap), state: emptyState()}
	seeded, err := s.seeded(ctx, seed)
	if err != nil {
		return nil, Opening{}, err
	}
	var o Opening
	d, err := store.load()
	if err == nil {
		st, next, rerr := s.restore(ctx, d)
		if rerr == nil {
			s.state, s.nextSeq = st, next
			return s, Opening{resumed: true}, nil
		}
		err = rerr
	}
	_, unusable := errors.AsType[*unusableDraftError](err)
	_, invalidDraft := errors.AsType[*InvalidError](err)
	switch {
	case errors.Is(err, errNoDraft):
	case unusable || invalidDraft:
		to, merr := store.moveAside()
		if merr != nil {
			return nil, Opening{}, fmt.Errorf("draft %s could not be restored (%w) or moved aside: %w", store.Path(), err, merr)
		}
		o = Opening{movedTo: to, movedReason: err}
	default:
		return nil, Opening{}, fmt.Errorf("restore draft %s: %w", store.Path(), err)
	}
	if seed.Len() > 0 {
		if err := s.commit(seeded); err != nil {
			if o.movedTo != "" {
				err = fmt.Errorf("saved draft moved to %s: %w", o.movedTo, err)
			}
			return nil, Opening{}, err
		}
		s.nextSeq = int64(seed.Len())
	}
	return s, o, nil
}

// restore validates d against the snapshot and returns its state and the next sequence number.
// A draft that does not fit is an *InvalidError; any other error is a failure to read.
//
// Sequence numbers only order comments, so restored ones are renumbered 0..n-1 in their saved
// order: numbering never grows across restarts, and stays exact in the UI's JavaScript numbers.
func (s *Session) restore(ctx context.Context, d draftFile) (state, int64, error) {
	v := validator{snap: s.snap, paths: s.paths}
	next := emptyState()
	seqs := make(map[int64]bool, len(d.Comments))
	for _, dc := range d.Comments {
		c, err := s.restoreComment(ctx, &v, dc)
		if err == nil && next.comments[dc.ID].ID != "" {
			err = invalid("duplicate comment id")
		}
		if err == nil && (dc.Seq < 0 || seqs[dc.Seq]) {
			err = invalid("sequence number %d is negative or repeated", dc.Seq)
		}
		if err != nil {
			return state{}, 0, commentError(dc.ID, err)
		}
		seqs[c.Seq] = true
		next.comments[c.ID] = c
		next.bytes += len(c.Body)
	}
	saved := slices.Sorted(maps.Keys(seqs))
	for id, c := range next.comments {
		n, _ := slices.BinarySearch(saved, c.Seq)
		c.Seq = int64(n)
		next.comments[id] = c
	}
	nextSeq := int64(len(saved))
	if len(next.comments) > MaxComments || next.bytes > MaxTotalBytes {
		return state{}, 0, invalid("draft exceeds the comment limits")
	}
	summary, err := normalizeSummary(d.Summary)
	if err != nil {
		return state{}, 0, err
	}
	next.summary = summary
	for _, vf := range d.Viewed {
		i, ok := s.fileByPaths(vf)
		if !ok {
			return state{}, 0, invalid("viewed file %q -> %q is not a changed file", vf.OldPath, vf.NewPath)
		}
		next.viewed[i] = true
	}
	return next, nextSeq, nil
}

func (s *Session) restoreComment(ctx context.Context, v *validator, dc draftComment) (Comment, error) {
	if err := checkID(dc.ID); err != nil {
		return Comment{}, err
	}
	author, err := authorOf(dc.Author)
	if err != nil {
		return Comment{}, err
	}
	body, err := normalizeBody(dc.Body)
	if err != nil {
		return Comment{}, err
	}
	t, err := dc.Target.target()
	if err != nil {
		return Comment{}, err
	}
	file, err := v.check(ctx, t)
	if err != nil {
		return Comment{}, err
	}
	return Comment{ID: dc.ID, Author: author, Body: body, Target: t, File: file, Seq: dc.Seq, Version: s.nextVersion()}, nil
}

func (s *Session) fileByPaths(vf viewedFile) (int, bool) {
	var i int
	var ok bool
	if vf.NewPath != "" {
		i, ok = s.paths.new[vf.NewPath]
	} else {
		i, ok = s.paths.old[vf.OldPath]
	}
	if !ok {
		return 0, false
	}
	f := s.snap.File(i)
	return i, f.OldPath == vf.OldPath && f.NewPath == vf.NewPath
}

// seeded validates every seed comment against the snapshot and the limits, and returns the
// state holding them, numbered from 0. It reports every problem at once.
func (s *Session) seeded(ctx context.Context, seed Seed) (state, error) {
	v := validator{snap: s.snap, paths: s.paths}
	var problems []string
	next := emptyState()
	for k, e := range seed.entries {
		if e.problem != "" {
			problems = append(problems, fmt.Sprintf("comment %d: %s", k+1, e.problem))
			continue
		}
		c := e.comment
		file, err := v.check(ctx, c.Target)
		if err != nil {
			if _, ok := errors.AsType[*InvalidError](err); !ok {
				return state{}, err
			}
			problems = append(problems, fmt.Sprintf("comment %d: %v", k+1, err))
			continue
		}
		id := fmt.Sprintf("seed-%d", k+1)
		next.comments[id] = Comment{
			ID: id, Author: Agent, Target: c.Target, File: file, Body: c.Body, Seq: int64(k), Version: s.nextVersion(),
		}
		next.bytes += len(c.Body)
	}
	if n := seed.Len(); n > MaxComments {
		problems = append(problems, fmt.Sprintf("seed has %d comments; the limit is %d", n, MaxComments))
	}
	if next.bytes > MaxTotalBytes {
		problems = append(problems, fmt.Sprintf("seed bodies total %d bytes; the limit is %d", next.bytes, MaxTotalBytes))
	}
	if problems != nil {
		return state{}, &SeedError{Problems: problems}
	}
	return next, nil
}

// commit persists next and makes it current.
func (s *Session) commit(next state) error {
	if err := s.store.save(s.encode(next)); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Session) encode(st state) draftFile {
	viewed := []viewedFile{}
	for i := range s.snap.FileCount() {
		if st.viewed[i] {
			f := s.snap.File(i)
			viewed = append(viewed, viewedFile{OldPath: f.OldPath, NewPath: f.NewPath})
		}
	}
	return encodeDraft(st, sorted(st), viewed)
}

// sorted returns comments in result order, by Comment.SortKey.
func sorted(st state) []Comment {
	out := slices.Collect(maps.Values(st.comments))
	slices.SortFunc(out, func(a, b Comment) int {
		ka, kb := a.SortKey(), b.SortKey()
		return slices.Compare(ka[:], kb[:])
	})
	return out
}

// nextVersion assigns a new Comment.Version. The caller holds mu, or is Start.
func (s *Session) nextVersion() int64 {
	s.version++
	return s.version
}

// CreateComment adds comment id as the human, placed by p. It checks p against the snapshot and
// resolves the target's path from the file. When id is taken by a comment with the same target and
// body, such as one this create made before its reply was lost, it returns that comment unchanged
// and writes nothing; when id is taken otherwise it fails with ErrCommentExists.
//
// Validating a line or range reads the file's facts, which may run git, so it runs without the
// session lock; it reads only the snapshot, which never changes. A validation that outlasts the
// session is then rejected by the phase check under the lock.
func (s *Session) CreateComment(ctx context.Context, id string, p Placement, body string) (Comment, error) {
	if err := checkID(id); err != nil {
		return Comment{}, err
	}
	body, err := normalizeBody(body)
	if err != nil {
		return Comment{}, err
	}
	if !s.isOpen() {
		return Comment{}, ErrEnded
	}
	t, file, err := s.place(ctx, p)
	if err != nil {
		return Comment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != open {
		return Comment{}, ErrEnded
	}
	if old, exists := s.state.comments[id]; exists {
		if old.Target == t && old.Body == body {
			return old, nil
		}
		return Comment{}, ErrCommentExists
	}
	if len(s.state.comments) >= MaxComments {
		return Comment{}, invalid("the review already has %d comments", MaxComments)
	}
	next := s.state.clone()
	next.bytes += len(body)
	if next.bytes > MaxTotalBytes {
		return Comment{}, invalid("comment bodies would exceed %d bytes in total", MaxTotalBytes)
	}
	// A version spent on a failed save is never seen, so it needs no undo.
	c := Comment{ID: id, Author: Human, Target: t, File: file, Body: body, Seq: s.nextSeq, Version: s.nextVersion()}
	next.comments[id] = c
	if err := s.commit(next); err != nil {
		return Comment{}, err
	}
	s.nextSeq++
	return c, nil
}

// place checks p against the snapshot and returns its target and file index (-1 for the review).
func (s *Session) place(ctx context.Context, p Placement) (Target, int, error) {
	if p.Kind() == TargetReview {
		return ReviewTarget(), -1, nil
	}
	if p.file < 0 || p.file >= s.snap.FileCount() {
		return Target{}, 0, invalid("file index %d out of range", p.file)
	}
	if p.kind == TargetLine || p.kind == TargetRange {
		// Checked first: an invalid side would otherwise read as a missing side of the file.
		if err := checkSide(p.side); err != nil {
			return Target{}, 0, err
		}
	}
	f := s.snap.File(p.file)
	path := targetPath(f, p.kind, p.side)
	if path == "" {
		return Target{}, 0, invalid("%q has no %s side", f.Path(), p.side)
	}
	var t Target
	var err error
	switch p.kind {
	case TargetFile:
		t, err = FileTarget(path)
	case TargetLine:
		t, err = LineTarget(path, p.side, p.start)
	case TargetRange:
		t, err = RangeTarget(path, p.side, p.start, p.end)
	case TargetReview:
	}
	if err != nil {
		return Target{}, 0, err
	}
	v := validator{snap: s.snap, paths: s.paths}
	if err := v.checkAt(ctx, p.file, t); err != nil {
		return Target{}, 0, err
	}
	return t, p.file, nil
}

// EditComment replaces comment id's body, given the version the editor loaded. One rule covers
// every version: when the stored body already equals body, such as after an edit whose reply was
// lost, it returns the stored comment unchanged (same version and author) and writes nothing.
// Otherwise it fails with ErrStale when the comment has another version, and an edit assigns a new
// version and makes the human the author. It fails with ErrNoComment when there is no such
// comment, for example because it was deleted meanwhile.
func (s *Session) EditComment(id string, version int64, body string) (Comment, error) {
	if err := checkID(id); err != nil {
		return Comment{}, err
	}
	body, err := normalizeBody(body)
	if err != nil {
		return Comment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != open {
		return Comment{}, ErrEnded
	}
	old, exists := s.state.comments[id]
	switch {
	case !exists:
		return Comment{}, ErrNoComment
	case old.Body == body:
		return old, nil
	case old.Version != version:
		return Comment{}, ErrStale
	}
	next := s.state.clone()
	next.bytes += len(body) - len(old.Body)
	if next.bytes > MaxTotalBytes {
		return Comment{}, invalid("comment bodies would exceed %d bytes in total", MaxTotalBytes)
	}
	c := old
	c.Body, c.Version, c.Author = body, s.nextVersion(), Human
	next.comments[id] = c
	if err := s.commit(next); err != nil {
		return Comment{}, err
	}
	return c, nil
}

func (s *Session) isOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase == open
}

// DeleteComment removes comment id at version. A comment saved since version is not deleted
// (ErrStale), so a stale page cannot discard a newer edit (Q-27). Deleting an absent id succeeds,
// so retries are idempotent.
func (s *Session) DeleteComment(id string, version int64) error {
	if err := checkID(id); err != nil {
		return err
	}
	return s.mutate(func(next *state) error {
		old, ok := next.comments[id]
		switch {
		case !ok:
			return nil
		case old.Version != version:
			return ErrStale
		}
		next.bytes -= len(old.Body)
		delete(next.comments, id)
		return nil
	})
}

// SetViewed sets or clears file i's viewed mark.
func (s *Session) SetViewed(i int, viewed bool) error {
	if i < 0 || i >= s.snap.FileCount() {
		return invalid("file index %d out of range", i)
	}
	return s.mutate(func(next *state) error {
		if viewed {
			next.viewed[i] = true
		} else {
			delete(next.viewed, i)
		}
		return nil
	})
}

// SetSummary replaces the summary.
func (s *Session) SetSummary(summary string) error {
	summary, err := normalizeSummary(summary)
	if err != nil {
		return err
	}
	return s.mutate(func(next *state) error {
		next.summary = summary
		return nil
	})
}

func normalizeSummary(summary string) (string, error) {
	if !utf8.ValidString(summary) {
		return "", invalid("summary is not valid UTF-8")
	}
	summary = NormalizeText(summary)
	if len(summary) > MaxSummaryByte {
		return "", invalid("summary exceeds %d bytes", MaxSummaryByte)
	}
	return summary, nil
}

func (s *Session) mutate(fn func(*state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != open {
		return ErrEnded
	}
	next := s.state.clone()
	if err := fn(&next); err != nil {
		return err
	}
	return s.commit(next)
}

// View is a read-only copy of the draft for presentation.
type View struct {
	Comments []Comment // result order
	Summary  string
	Viewed   []int // file indexes, ascending
}

// View returns the current draft.
func (s *Session) View() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := View{Comments: sorted(s.state), Summary: s.state.summary, Viewed: []int{}}
	for i := range s.snap.FileCount() {
		if s.state.viewed[i] {
			v.Viewed = append(v.Viewed, i)
		}
	}
	return v
}

// Result builds the review result for the current draft without ending the session. Quoting
// reads blobs, so it runs on a copy of the state without holding the lock.
func (s *Session) Result(ctx context.Context) (Result, error) {
	s.mu.Lock()
	st := s.state
	s.mu.Unlock()
	return s.result(ctx, st)
}

// Delivery is what Finish delivered. DraftErr is a warning only: the result was delivered but
// the draft could not be deleted, so it expires later.
type Delivery struct {
	DraftErr error
	Result   Result
}

// DeliveryError is Finish's error when the result does not count as delivered: deliver failed, or
// the delivery was abandoned. The session has ended and the draft is kept; part or all of the result
// may have been written.
type DeliveryError struct {
	Err error
}

func (e *DeliveryError) Error() string { return "deliver the review result: " + e.Err.Error() }

func (e *DeliveryError) Unwrap() error { return e.Err }

// Finish builds the result from the current draft, ends the session, then passes the result to
// deliver. A nil error means the result was delivered; otherwise:
//
//   - ErrEnded: the session had ended, or End or Abandon interrupted the build; nothing was
//     delivered.
//   - *DeliveryError: deliver failed, or Abandon ran before it returned (ErrAbandoned); the
//     session has ended and the draft is kept. Delivery did not complete, but part or all of the
//     result may already have been written.
//   - any other error: building the result failed, nothing was delivered, and the session reopens.
//
// While the result is built, mutations are rejected and End or Abandon still interrupts it. Once
// delivery starts the session never reopens, and End reports EndedByFinish.
func (s *Session) Finish(ctx context.Context, deliver func(Result) error) (Delivery, error) {
	r, err := s.finish(ctx)
	if err != nil {
		return Delivery{}, err
	}
	// Delivery runs unlocked: a stdout write may block, and End must still answer a signal.
	err = deliver(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.phase == abandoned && err != nil:
		return Delivery{}, &DeliveryError{Err: errors.Join(err, ErrAbandoned)}
	case s.phase == abandoned:
		return Delivery{}, &DeliveryError{Err: ErrAbandoned}
	case err != nil:
		s.phase = undelivered
		return Delivery{}, &DeliveryError{Err: err}
	}
	s.phase = delivered
	return Delivery{Result: r, DraftErr: s.store.delete()}, nil
}

// ErrAbandoned is a *DeliveryError's cause when Abandon ran before deliver returned: the caller has
// already reported the review as not delivered, so the draft is kept even if deliver later
// succeeded.
var ErrAbandoned = errors.New("delivery was abandoned before it completed")

// Fate is how a session ended, for a caller that will wait no longer.
type Fate int

// Fates.
const (
	// FateInterrupted: the session ended before delivery started; nothing was written and the
	// draft is kept.
	FateInterrupted Fate = iota + 1
	// FateCancelled: Cancel ended the session and deleted the draft.
	FateCancelled
	// FateDelivered: the result was delivered and the draft deleted.
	FateDelivered
	// FateUndelivered: delivery failed or was abandoned; part or all of the result may have been
	// written, and the draft is kept.
	FateUndelivered
)

// Abandon ends the session now and returns its fate; repeated calls agree. It differs from End
// in one way only: End lets a delivery in progress decide its outcome, while Abandon gives up on
// it. Whatever deliver later returns, Finish then reports ErrAbandoned and keeps the draft, so the
// caller's report of "not delivered" stays true.
func (s *Session) Abandon() Fate {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endLocked()
	if s.phase == delivering {
		s.phase = abandoned
	}
	switch s.phase {
	case delivered:
		return FateDelivered
	case abandoned, undelivered:
		return FateUndelivered
	case cancelled:
		return FateCancelled
	case open, finishing, delivering, interrupted:
		// endLocked and the move above leave only interrupted of these.
	}
	return FateInterrupted
}

// finish builds the result and moves to delivering, unless End interrupts first.
func (s *Session) finish(ctx context.Context) (Result, error) {
	s.mu.Lock()
	if s.phase != open {
		s.mu.Unlock()
		return Result{}, ErrEnded
	}
	st := s.state
	ctx, s.abort = context.WithCancel(ctx)
	s.phase = finishing
	s.mu.Unlock()

	r, err := s.result(ctx, st)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.abort()
	s.abort = nil
	switch {
	case s.phase == interrupted:
		return Result{}, ErrEnded
	case err != nil:
		s.phase = open
		return Result{}, err
	}
	s.phase = delivering
	return r, nil
}

// Cancel ends the session and deletes the draft. The session has ended even when the delete
// fails.
func (s *Session) Cancel() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != open {
		return ErrEnded
	}
	s.phase = cancelled
	return s.store.delete()
}

// Ending is what ended a session. The zero value is not an Ending.
type Ending int

// Endings.
const (
	// EndedByEnd: End ended the session, cancelling a result still being built; the draft is
	// kept.
	EndedByEnd Ending = iota + 1
	// EndedByFinish: a finish reached delivery; it decides the draft's fate.
	EndedByFinish
	// EndedByCancel: Cancel ended the session and deleted the draft.
	EndedByCancel
)

// End ends the session, keeping the draft, unless it has already ended. It returns what ended the
// session first, so repeated calls agree. It differs from Abandon in one way only: End lets a
// delivery in progress decide its outcome, while Abandon gives up on it.
func (s *Session) End() Ending {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endLocked()
	switch s.phase {
	case delivering, delivered, undelivered, abandoned:
		return EndedByFinish
	case cancelled:
		return EndedByCancel
	case open, finishing, interrupted:
	}
	return EndedByEnd
}

// endLocked interrupts a session that is open or building its result, cancelling the build; it
// leaves every other phase unchanged. It is the one writer of the move to interrupted. The caller
// holds mu.
func (s *Session) endLocked() {
	switch s.phase {
	case finishing:
		s.abort()
		s.phase = interrupted
	case open:
		s.phase = interrupted
	case delivering, delivered, undelivered, abandoned, cancelled, interrupted:
	}
}
