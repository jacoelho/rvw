// Package round owns review rounds (contract revision 3, Review rounds): the review ledger of what
// the human reviewed in one worktree, its store and retention refs, and what a new session derives
// from it — each changed file's round state, the viewed marks a new draft starts with, and the
// earlier comments with their statuses.
package round

import (
	"cmp"
	"maps"
	"slices"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// Bounds of a written ledger (REQ-RND-004).
const (
	// TTL is how long a reviewed version, the earlier comments and a retention ref are kept.
	TTL = 30 * 24 * time.Hour
	// MaxSnapshots bounds the snapshots whose reviewed versions a ledger keeps, and so the
	// worktree's retention refs.
	MaxSnapshots = 256
	// MaxReviewed bounds a ledger's reviewed versions.
	MaxReviewed = 20000
)

// FileKey is a changed file's identity across snapshots (Definitions: file key). A path is ""
// for the side an added or deleted file lacks.
type FileKey struct{ OldPath, NewPath string }

// KeyOf returns f's file key.
func KeyOf(f source.ChangedFile) FileKey { return FileKey{OldPath: f.OldPath, NewPath: f.NewPath} }

// Version is a changed file's sides (Definitions: file version); the zero Blob is an absent side.
type Version struct{ Old, New source.Blob }

// VersionOf returns f's file version.
func VersionOf(f source.ChangedFile) Version {
	return Version{Old: f.Side(false), New: f.Side(true)}
}

// Snapshot names a delivered snapshot by its trees: what a retention ref keeps.
type Snapshot struct{ Base, Head string }

// Reviewed is a file key's reviewed version (Definitions).
type Reviewed struct {
	At       time.Time // delivery time
	Snapshot Snapshot
	Version  Version
	// Whitespace reports that whitespace was hidden when the viewed mark was set.
	Whitespace bool
}

// Earlier is one comment of the last delivered review result (Definitions: earlier comment).
type Earlier struct {
	// ID is unique among one ledger's earlier comments and differs between deliveries.
	ID     string
	Author review.Author
	Body   string
	// Key is the commented file's key; the zero key for a review comment.
	Key FileKey
	// Blob is the commented side for a line or range comment, else the zero Blob.
	Blob   source.Blob
	Target review.Target
}

// Comments are the earlier comments with the delivery they came from.
type Comments struct {
	At       time.Time
	Snapshot Snapshot
	Items    []Earlier // result order
}

// Ledger is a worktree's review ledger (Definitions). The zero Ledger is empty.
type Ledger struct {
	// lastAt is the newest delivery recorded; hideWhitespace is that delivery's setting.
	lastAt         time.Time
	reviewed       map[FileKey]Reviewed
	comments       Comments
	hideWhitespace bool
}

// Reviewed returns key's reviewed version, if any.
func (l Ledger) Reviewed(key FileKey) (Reviewed, bool) {
	r, ok := l.reviewed[key]
	return r, ok
}

// Comments returns the earlier comments.
func (l Ledger) Comments() Comments { return l.comments }

// HideWhitespace is the Hide whitespace setting of the last recorded round.
func (l Ledger) HideWhitespace() bool { return l.hideWhitespace }

// LastAt is the time of the last recorded round, zero when there is none.
func (l Ledger) LastAt() time.Time { return l.lastAt }

// Empty reports whether the ledger records nothing.
func (l Ledger) Empty() bool { return l.lastAt.IsZero() }

// snapshots returns the snapshots the ledger names, the retention refs it needs, each with the
// newest delivery time that names it.
func (l Ledger) snapshots() map[Snapshot]time.Time {
	named := map[Snapshot]time.Time{}
	for _, r := range l.reviewed {
		if r.At.After(named[r.Snapshot]) {
			named[r.Snapshot] = r.At
		}
	}
	if len(l.comments.Items) > 0 && l.comments.At.After(named[l.comments.Snapshot]) {
		named[l.comments.Snapshot] = l.comments.At
	}
	return named
}

// Delivery is what one delivered review adds to the ledger (REQ-RND-002).
type Delivery struct {
	At       time.Time
	Snapshot Snapshot
	// Reviewed holds the file version of each file whose viewed mark was set.
	Reviewed map[FileKey]Reviewed
	Comments []Earlier
	// HideWhitespace is the draft's Hide whitespace setting.
	HideWhitespace bool
}

// merge returns l with d recorded, then bounded as of d.At (REQ-RND-003, REQ-RND-004). Per file
// key the later delivery wins; the earlier comments and the setting are the latest delivery's. A
// delivery older than what l holds only adds what nothing newer replaced.
func (l Ledger) merge(d Delivery) Ledger {
	next := Ledger{
		reviewed: maps.Clone(l.reviewed), comments: l.comments,
		lastAt: l.lastAt, hideWhitespace: l.hideWhitespace,
	}
	if next.reviewed == nil {
		next.reviewed = map[FileKey]Reviewed{}
	}
	for key, r := range d.Reviewed {
		if old, ok := next.reviewed[key]; !ok || !old.At.After(r.At) {
			next.reviewed[key] = r
		}
	}
	if !l.comments.At.After(d.At) {
		next.comments = Comments{At: d.At, Snapshot: d.Snapshot, Items: d.Comments}
	}
	if !l.lastAt.After(d.At) {
		next.lastAt, next.hideWhitespace = d.At, d.HideWhitespace
	}
	return next.bounded(d.At)
}

// bounded, applied on read and on write, drops what REQ-RND-004 drops as of now: reviewed versions
// and earlier comments delivered more than TTL earlier, then the reviewed versions of all but the
// MaxSnapshots most recently delivered snapshots, the earlier comments' among them, then all but
// the MaxReviewed most recently delivered versions. Ties keep the smaller key, so the result does
// not depend on map order.
func (l Ledger) bounded(now time.Time) Ledger {
	cutoff := now.Add(-TTL)
	maps.DeleteFunc(l.reviewed, func(_ FileKey, r Reviewed) bool { return r.At.Before(cutoff) })
	if l.comments.At.Before(cutoff) {
		l.comments = Comments{}
	}
	// The earlier comments' snapshot also needs a retention ref, so it counts towards the bound.
	// It is the latest delivery's, so ranking never drops it.
	newest := l.snapshots()
	if len(newest) > MaxSnapshots {
		snaps := slices.SortedFunc(maps.Keys(newest), func(a, b Snapshot) int {
			return cmp.Or(newest[b].Compare(newest[a]), cmp.Compare(a.Base, b.Base), cmp.Compare(a.Head, b.Head))
		})
		dropped := map[Snapshot]bool{}
		for _, s := range snaps[MaxSnapshots:] {
			dropped[s] = true
		}
		maps.DeleteFunc(l.reviewed, func(_ FileKey, r Reviewed) bool { return dropped[r.Snapshot] })
	}
	if len(l.reviewed) > MaxReviewed {
		keys := slices.SortedFunc(maps.Keys(l.reviewed), func(a, b FileKey) int {
			return cmp.Or(l.reviewed[b].At.Compare(l.reviewed[a].At),
				cmp.Compare(a.OldPath, b.OldPath), cmp.Compare(a.NewPath, b.NewPath))
		})
		for _, k := range keys[MaxReviewed:] {
			delete(l.reviewed, k)
		}
	}
	return l
}
