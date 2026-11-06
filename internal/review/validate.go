package review

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jacoelho/rvw/internal/source"
)

// targetPath is the path a target of kind on side names in file f: the display path for a file
// target, else the path of the target's side, "" when f lacks that side or side is neither Old nor
// New. It is the one rule tying target paths to files.
func targetPath(f source.ChangedFile, kind TargetKind, side Side) string {
	switch {
	case kind == TargetFile:
		return f.Path()
	case side == Old:
		return f.OldPath
	case side == New:
		return f.NewPath
	default:
		return ""
	}
}

// pathIndex maps target paths to changed-file indexes, by targetPath.
type pathIndex struct {
	old, new, display map[string]int
	// commentable[i] is CommentableFile for file i.
	commentable []bool
}

func newPathIndex(snap *source.Snapshot) pathIndex {
	n := snap.FileCount()
	p := pathIndex{
		old: make(map[string]int, n), new: make(map[string]int, n), display: make(map[string]int, n),
		commentable: make([]bool, n),
	}
	for i := range n {
		f := snap.File(i)
		p.commentable[i] = CommentableFile(f)
		if path := targetPath(f, TargetLine, Old); path != "" {
			p.old[path] = i
		}
		if path := targetPath(f, TargetLine, New); path != "" {
			p.new[path] = i
		}
		p.display[targetPath(f, TargetFile, "")] = i
	}
	return p
}

// file returns the changed-file index a file, line or range target refers to.
func (p pathIndex) file(t Target) (int, bool) {
	var m map[string]int
	switch {
	case t.Kind() == TargetFile:
		m = p.display
	case t.side == Old:
		m = p.old
	default:
		m = p.new
	}
	i, ok := m[t.path]
	return i, ok
}

// validator checks targets against the snapshot. It memoises each file's facts within one batch
// (a seed, a draft, or one comment) and retains nothing beyond it.
type validator struct {
	snap  *source.Snapshot
	memo  map[int]source.FileFacts
	paths pathIndex
}

// fileOf returns the changed-file index a seed or draft target names by path, or -1 for the review
// target. A path that names no changed file is an *InvalidError.
func (v *validator) fileOf(t Target) (int, error) {
	if t.Kind() == TargetReview {
		return -1, nil
	}
	i, ok := v.paths.file(t)
	if !ok {
		if t.Kind() == TargetFile {
			return 0, invalid("%q is not a changed file", t.path)
		}
		return 0, invalid("%q is not a changed file on the %s side", t.path, t.side)
	}
	return i, nil
}

// checkAt validates t, which names file i (-1 for the review target), against the snapshot's facts.
// A target that does not fit is an *InvalidError; any other error is a failure to read the
// snapshot.
func (v *validator) checkAt(ctx context.Context, i int, t Target) error {
	if t.Kind() == TargetReview {
		return nil
	}
	if !v.paths.commentable[i] {
		return invalid("%q accepts only review comments: one of its paths is not commentable", t.path)
	}
	if t.Kind() == TargetFile {
		return nil
	}
	facts, ok := v.memo[i]
	if !ok {
		var err error
		if facts, err = v.snap.Facts(ctx, i); err != nil {
			return err
		}
		if v.memo == nil {
			v.memo = map[int]source.FileFacts{}
		}
		v.memo[i] = facts
	}
	return commentableLines(facts, t)
}

// check validates a seed or draft target: fileOf, then checkAt.
func (v *validator) check(ctx context.Context, t Target) (int, error) {
	i, err := v.fileOf(t)
	if err != nil {
		return 0, err
	}
	return i, v.checkAt(ctx, i, t)
}

// commentableLines is the rule for line and range targets: a text file's side that exists, and
// lines on it the file view can show: any line of a file that is not large, and only lines inside
// one hunk of a large file. source.FileFacts.Showable owns which lines can be shown.
func commentableLines(f source.FileFacts, t Target) error {
	if f.Kind != source.KindText {
		return invalid("%q has no line diff; comment on the file instead", t.path)
	}
	side := f.New
	if t.side == Old {
		side = f.Old
	}
	if !side.Present {
		return invalid("%q has no %s side", t.path, t.side)
	}
	span := strconv.Itoa(t.start)
	if t.end != t.start {
		span += "-" + strconv.Itoa(t.end)
	}
	// A large side may be uncounted (Lines 0), so only a file that is not large is checked by count.
	if !f.Large && t.end > side.Lines {
		return invalid("%s:%s (%s) is beyond the file's %d lines", t.path, span, t.side, side.Lines)
	}
	if !f.Showable(t.side == New, source.LineRange{Start: t.start, End: t.end}) {
		if f.Large {
			return invalid("%s:%s (%s) is outside the hunks of a large file", t.path, span, t.side)
		}
		return fmt.Errorf("%s:%s (%s) exists but cannot be shown", t.path, span, t.side)
	}
	return nil
}
