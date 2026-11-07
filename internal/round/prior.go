package round

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// Delivered is what the review delivered from snap adds to the ledger (REQ-RND-002): draft is the
// draft as delivered and at the delivery time. Each earlier comment's ID is derived from at and the
// draft comment's ID, so IDs differ between deliveries.
func Delivered(snap *source.Snapshot, draft review.Draft, at time.Time) Delivery {
	at = at.UTC().Round(0)
	shot := Snapshot{Base: snap.BaseTree(), Head: snap.HeadTree()}
	d := Delivery{
		At: at, Snapshot: shot, HideWhitespace: draft.HideWhitespace,
		Reviewed: make(map[FileKey]Reviewed, len(draft.Viewed)),
		Comments: make([]Earlier, 0, len(draft.Comments)),
	}
	for _, m := range draft.Viewed {
		f := snap.File(m.File)
		d.Reviewed[KeyOf(f)] = Reviewed{At: at, Snapshot: shot, Version: VersionOf(f), Whitespace: m.Whitespace}
	}
	for _, c := range draft.Comments {
		e := Earlier{ID: earlierIDOf(at, c.ID), Author: c.Author, Body: c.Body, Target: c.Target}
		if c.File >= 0 {
			f := snap.File(c.File)
			e.Key = KeyOf(f)
			if k := c.Target.Kind(); k == review.TargetLine || k == review.TargetRange {
				e.Blob = f.Side(c.Target.Side() == review.New)
			}
		}
		d.Comments = append(d.Comments, e)
	}
	return d
}

func earlierIDOf(at time.Time, id string) string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(at.UnixNano(), 10) + "\x00" + id))
	return hex.EncodeToString(sum[:8])
}

// State is a changed file's relation to its reviewed version (Definitions: round state), before
// the Hide whitespace setting decides between reviewed unchanged and reviewed with whitespace
// hidden for StateSame.
type State int

const (
	// StateUnreviewed is a file with no reviewed version.
	StateUnreviewed State = iota
	// StateSame is a file whose version equals the reviewed version.
	StateSame
	// StateChanged is a file whose new side differs and whose old side is equal.
	StateChanged
	// StateBaseChanged is a file whose new side is equal and whose old side differs.
	StateBaseChanged
	// StateChangedAndBase is a file both of whose sides differ.
	StateChangedAndBase
)

// Changed reports whether the new side differs from the reviewed version's.
func (s State) Changed() bool { return s == StateChanged || s == StateChangedAndBase }

// Status is an earlier comment's status (Definitions: earlier comment status).
type Status int

const (
	// StatusPending is a line or range comment whose blob differs from the snapshot's, whose
	// status Place computes.
	StatusPending Status = iota
	// StatusCurrent is a comment that still holds, at its placed lines for a line or range.
	StatusCurrent
	// StatusOutdated is a line or range comment whose lines changed.
	StatusOutdated
	// StatusUnplaced is a comment whose blob, side or file key is gone.
	StatusUnplaced
)

// Prior is what a session derives from the ledger for its snapshot (REQ-RND-008): fixed at start,
// computed from the changed-file list alone. Place, Since, AddedSince and StillApplies run git on
// request
// (REQ-PERF-005).
type Prior struct {
	snap     *source.Snapshot
	reviewed []*Reviewed // by file index; nil when unreviewed
	states   []State
	earlier  []PriorComment
	ledger   Ledger
}

// PriorComment is an earlier comment as this snapshot sees it before any git work.
type PriorComment struct {
	Earlier
	// File is the index of the changed file with the comment's key, or -1.
	File int
	// Status is final unless StatusPending.
	Status Status
}

// NewPrior derives snap's round facts from l.
func NewPrior(snap *source.Snapshot, l Ledger) *Prior {
	n := snap.FileCount()
	p := &Prior{snap: snap, reviewed: make([]*Reviewed, n), states: make([]State, n), ledger: l}
	byKey := make(map[FileKey]int, n)
	for i := range n {
		f := snap.File(i)
		byKey[KeyOf(f)] = i
		r, ok := l.Reviewed(KeyOf(f))
		if !ok {
			continue
		}
		p.reviewed[i] = &r
		p.states[i] = stateOf(VersionOf(f), r.Version)
	}
	for _, e := range l.Comments().Items {
		pc := PriorComment{Earlier: e, File: -1, Status: StatusUnplaced}
		if i, ok := byKey[e.Key]; ok && e.Key != (FileKey{}) {
			pc.File = i
		}
		switch e.Target.Kind() {
		case review.TargetReview:
			pc.Status = StatusCurrent
		case review.TargetFile:
			if pc.File >= 0 {
				pc.Status = StatusCurrent
			}
		case review.TargetLine, review.TargetRange:
			if pc.File >= 0 {
				pc.Status = staticStatus(snap.File(pc.File), e)
			}
		}
		p.earlier = append(p.earlier, pc)
	}
	return p
}

func stateOf(cur, rev Version) State {
	switch newSame, oldSame := cur.New == rev.New, cur.Old == rev.Old; {
	case newSame && oldSame:
		return StateSame
	case newSame:
		return StateBaseChanged
	case oldSame:
		return StateChanged
	default:
		return StateChangedAndBase
	}
}

// staticStatus is a line or range comment's status as far as the changed-file list decides it.
func staticStatus(f source.ChangedFile, e Earlier) Status {
	cur := f.Side(e.Target.Side() == review.New)
	switch {
	case cur.ID == "" || cur.Submodule():
		return StatusUnplaced
	case cur.ID == e.Blob.ID:
		return StatusCurrent
	default:
		return StatusPending
	}
}

// Empty reports whether the ledger recorded no round.
func (p *Prior) Empty() bool { return p.ledger.Empty() }

// LastAt is the time of the last recorded round.
func (p *Prior) LastAt() time.Time { return p.ledger.LastAt() }

// HideWhitespace is the setting a new draft starts with (REQ-DIFF-012).
func (p *Prior) HideWhitespace() bool { return p.ledger.HideWhitespace() }

// State returns file i's round state and, unless unreviewed, its reviewed version.
func (p *Prior) State(i int) (State, Reviewed) {
	if p.reviewed[i] == nil {
		return StateUnreviewed, Reviewed{}
	}
	return p.states[i], *p.reviewed[i]
}

// Unchanged reports whether a file in state st, reviewed with whitespace hidden when reviewedWs,
// is reviewed unchanged under the Hide whitespace setting hide (Definitions: reviewed unchanged):
// a version reviewed with whitespace hidden may differ in whitespace, so it counts only while
// whitespace stays hidden. The web UI applies the same rule as the setting changes.
func Unchanged(st State, reviewedWs, hide bool) bool {
	return st == StateSame && (!reviewedWs || hide)
}

// PresetMarks returns the viewed marks a new draft starts with (REQ-RND-009): each file reviewed
// unchanged under the ledger's Hide whitespace setting, with its reviewed whitespace flag, in file
// order.
func (p *Prior) PresetMarks() []review.Mark {
	var marks []review.Mark
	for i, r := range p.reviewed {
		if r != nil && Unchanged(p.states[i], r.Whitespace, p.ledger.HideWhitespace()) {
			marks = append(marks, review.Mark{File: i, Whitespace: r.Whitespace})
		}
	}
	return marks
}

// Earlier returns the earlier comments in result order. The caller must not modify the slice.
func (p *Prior) Earlier() []PriorComment { return p.earlier }

// EarlierAt is the delivery time of the earlier comments, zero when there are none.
func (p *Prior) EarlierAt() time.Time { return p.ledger.Comments().At }

// Find returns the earlier comment with id.
func (p *Prior) Find(id string) (PriorComment, bool) {
	for _, c := range p.earlier {
		if c.ID == id {
			return c, true
		}
	}
	return PriorComment{}, false
}

// Fallback is why a file shows its snapshot diff instead of its interdiff (REQ-RND-012).
type Fallback int

const (
	// NoFallback is a file shown with its interdiff.
	NoFallback Fallback = iota
	// FallbackNotChanged is a file not changed since review, or with no current or reviewed new
	// side to compare.
	FallbackNotChanged
	// FallbackMissing is a file whose reviewed blob is gone.
	FallbackMissing
	// FallbackNotText is a file whose versions are not comparable as text.
	FallbackNotText
)

// reviewedNew returns file i's reviewed new side, or false when i is not changed since review or
// either new side is absent.
func (p *Prior) reviewedNew(i int) (source.Blob, bool, error) {
	if i < 0 || i >= len(p.states) {
		return source.Blob{}, false, fmt.Errorf("file index %d out of range", i)
	}
	r := p.reviewed[i]
	if r == nil || !p.states[i].Changed() || r.Version.New == (source.Blob{}) || !p.snap.File(i).HasSide(true) {
		return source.Blob{}, false, nil
	}
	return r.Version.New, true, nil
}

// fallbackOf maps a git failure to the fallback it causes, or reports that it causes none.
func fallbackOf(err error) (Fallback, bool) {
	switch {
	case errors.Is(err, source.ErrMissing):
		return FallbackMissing, true
	case errors.Is(err, source.ErrNotText):
		return FallbackNotText, true
	default:
		return NoFallback, false
	}
}

// Since returns file i's interdiff (REQ-RND-010). When the file has none,
// it returns the reason and a zero diff, and the caller shows the snapshot diff.
func (p *Prior) Since(ctx context.Context, i int) (source.FileDiff, Fallback, error) {
	from, ok, err := p.reviewedNew(i)
	if err != nil || !ok {
		return source.FileDiff{}, FallbackNotChanged, err
	}
	d, err := p.snap.Interdiff(ctx, i, from)
	if f, ok := fallbackOf(err); ok {
		return source.FileDiff{}, f, nil
	}
	if err != nil {
		return source.FileDiff{}, NoFallback, err
	}
	return d, NoFallback, nil
}

// AddedSince returns the new-side line ranges, first to last inclusive, that file i's interdiff
// adds (REQ-RND-013), or none when the file has no interdiff. It is read from Since's
// diff, so the two agree on which files have one.
func (p *Prior) AddedSince(ctx context.Context, i int) ([][2]int, error) {
	d, fb, err := p.Since(ctx, i)
	if err != nil || fb != NoFallback {
		return nil, err
	}
	var out [][2]int
	for _, h := range d.Hunks {
		for _, l := range h.Lines {
			if l.Op != source.OpAdd {
				continue
			}
			if n := len(out); n > 0 && out[n-1][1] == l.New-1 {
				out[n-1][1] = l.New
			} else {
				out = append(out, [2]int{l.New, l.New})
			}
		}
	}
	return out, nil
}

// Placed is an earlier line or range comment's status in its file, with its placed lines while
// current and its quote, read from its blob, while outdated. Cause is why the placement could not
// be computed, which leaves the comment unplaced (Definitions: unplaced); it is nil otherwise.
type Placed struct {
	Cause      error
	Quote      *review.Quote
	ID         string
	Status     Status
	Start, End int
}

// Place returns the status of each earlier line or range comment of file i (REQ-CMT-009), in
// result order. It diffs each distinct commented blob with the file's side once, with no context
// (Definitions: earlier comment status), and reads each outdated comment's lines from its blob.
// A failed diff or read degrades only the comments it serves, to unplaced with a Cause; the error
// is non-nil only when ctx ends, since a killed git then reports its own failure instead.
func (p *Prior) Place(ctx context.Context, i int) ([]Placed, error) {
	type group struct {
		blob    source.Blob
		newSide bool
	}
	// changes is a group's hunks, nil when its blob is missing or not text, or why the diff failed.
	type changes struct {
		hunks *[]source.HunkRange
		err   error
	}
	diffs := map[group]changes{}
	var out []Placed
	for _, c := range p.earlier {
		if c.File != i || (c.Target.Kind() != review.TargetLine && c.Target.Kind() != review.TargetRange) {
			continue
		}
		start, end := c.Target.Lines()
		pl := Placed{ID: c.ID, Status: c.Status, Start: start, End: end}
		if c.Status == StatusPending {
			g := group{blob: c.Blob, newSide: c.Target.Side() == review.New}
			ch, seen := diffs[g]
			if !seen {
				h, err := p.snap.ChangesFrom(ctx, i, g.newSide, g.blob)
				switch {
				case ctx.Err() != nil:
					// err may be nil when git finished just before the cancel.
					return nil, errors.Join(ctx.Err(), err)
				case errors.Is(err, source.ErrMissing), errors.Is(err, source.ErrNotText):
				case err != nil:
					ch.err = err
				default:
					ch.hunks = &h
				}
				diffs[g] = ch
			}
			pl.Status, pl.Start, pl.End, pl.Cause = StatusUnplaced, 0, 0, ch.err
			if ch.hunks != nil {
				pl.Status = StatusOutdated
				if s, e, ok := source.MapLines(*ch.hunks, start, end); ok {
					pl.Status, pl.Start, pl.End = StatusCurrent, s, e
				}
			}
		}
		if pl.Status == StatusOutdated {
			q, err := p.quote(ctx, c.Blob, start, end)
			switch {
			case ctx.Err() != nil:
				return nil, errors.Join(ctx.Err(), err)
			case err != nil:
				pl.Status, pl.Cause = StatusUnplaced, err
			default:
				pl.Quote = &q
			}
		}
		out = append(out, pl)
	}
	return out, nil
}

// quote reads lines start to end of b, cut as the review result cuts quotes. A missing blob was
// already found by ChangesFrom, so it is an error here.
func (p *Prior) quote(ctx context.Context, b source.Blob, start, end int) (review.Quote, error) {
	lines, err := p.snap.BlobLines(ctx, b, []source.LineRange{review.QuoteRange(start, end)}, review.QuoteKeep)
	if err != nil {
		return review.Quote{}, fmt.Errorf("quote earlier comment: %w", err)
	}
	return review.CutQuote(start, end, lines[0]), nil
}

// StillApplies returns where a copy of earlier comment c goes (REQ-CMT-011), most specific first:
// its placed lines when current, then its file when its file key is a changed file. CopyComment
// takes the first the snapshot accepts, else the review. Placing a line or range comment runs git
// as Place does, and fails when c's own placement could not be computed: a copy records
// copied_from, so a copy made from a degraded placement could never be retried onto its lines.
func (p *Prior) StillApplies(ctx context.Context, c PriorComment) ([]review.Placement, error) {
	if c.File < 0 {
		return nil, nil
	}
	switch k := c.Target.Kind(); k {
	case review.TargetLine, review.TargetRange:
		all, err := p.Place(ctx, c.File)
		if err != nil {
			return nil, err
		}
		var out []review.Placement
		for _, pl := range all {
			if pl.ID != c.ID {
				continue
			}
			if pl.Cause != nil {
				return nil, fmt.Errorf("place earlier comment %s: %w", c.ID, pl.Cause)
			}
			if pl.Status != StatusCurrent {
				continue
			}
			if k == review.TargetLine {
				out = append(out, review.OnLine(c.File, c.Target.Side(), pl.Start))
			} else {
				out = append(out, review.OnRange(c.File, c.Target.Side(), pl.Start, pl.End))
			}
		}
		return append(out, review.OnFile(c.File)), nil
	case review.TargetFile:
		return []review.Placement{review.OnFile(c.File)}, nil
	default:
		return nil, nil
	}
}
