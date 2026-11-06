// Package review owns the review's comments, summary and viewed marks, their validation
// and persistence as a draft, and the rendering of the review result.
package review

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/source"
)

// Comment limits and the summary limit.
const (
	MaxComments    = 4096
	MaxBodyBytes   = 64 << 10
	MaxTotalBytes  = 4 << 20
	MaxSummaryByte = 64 << 10
)

// Author says whose words a comment holds.
type Author string

// Authors.
const (
	Human Author = "human"
	Agent Author = "agent"
)

// Side is a line target's side of the diff.
type Side string

// Sides.
const (
	Old Side = "old"
	New Side = "new"
)

// TargetKind is what a comment is attached to.
type TargetKind string

// Target kinds.
const (
	TargetReview TargetKind = "review"
	TargetFile   TargetKind = "file"
	TargetLine   TargetKind = "line"
	TargetRange  TargetKind = "range"
)

// Target is a comment's attachment. The zero Target is the review target; FileTarget, LineTarget
// and RangeTarget build the others and are the only check of a target's shape, so a Target value is
// always well-formed. Whether it fits the snapshot is checked when a comment is saved.
type Target struct {
	kind       TargetKind // "" is TargetReview
	path       string
	side       Side
	start, end int
}

// ReviewTarget is the target of a comment on the whole review.
func ReviewTarget() Target { return Target{} }

// FileTarget is the target of a comment on the changed file whose display path is path.
func FileTarget(path string) (Target, error) {
	if path == "" {
		return Target{}, invalid("a file comment needs a path")
	}
	return Target{kind: TargetFile, path: path}, nil
}

// LineTarget is the target of a comment on one line of side's path.
func LineTarget(path string, side Side, line int) (Target, error) {
	if err := checkLinePath(TargetLine, path, side, line); err != nil {
		return Target{}, err
	}
	return Target{kind: TargetLine, path: path, side: side, start: line, end: line}, nil
}

// RangeTarget is the target of a comment on lines start..end of side's path, start before end.
func RangeTarget(path string, side Side, start, end int) (Target, error) {
	if err := checkLinePath(TargetRange, path, side, start); err != nil {
		return Target{}, err
	}
	if end <= start {
		return Target{}, invalid("a range needs start before end")
	}
	return Target{kind: TargetRange, path: path, side: side, start: start, end: end}, nil
}

func checkLinePath(kind TargetKind, path string, side Side, start int) error {
	if path == "" {
		return invalid("a %s comment needs a path", kind)
	}
	if err := checkSide(side); err != nil {
		return err
	}
	if start < 1 {
		return invalid("line numbers start at 1")
	}
	return nil
}

// checkSide is the one owner of the side rule: a line or range target is on the old or new side.
func checkSide(side Side) error {
	if side != Old && side != New {
		return invalid("side must be %q or %q", Old, New)
	}
	return nil
}

// Placement is where a new comment goes, by changed-file index: the UI's view of a target. It is
// data, built by OnReview, OnFile, OnLine and OnRange without any check: CreateComment alone checks
// it, the file index against the snapshot and the side, lines and range through the Target it
// resolves to, so a placement's rules and a target's are the same rules.
type Placement struct {
	kind       TargetKind // "" is TargetReview
	side       Side
	file       int // the changed-file index; unused for the review
	start, end int
}

// OnReview places a comment on the whole review.
func OnReview() Placement { return Placement{} }

// OnFile places a comment on changed file i.
func OnFile(i int) Placement { return Placement{kind: TargetFile, file: i} }

// OnLine places a comment on line n of file i's side.
func OnLine(i int, side Side, n int) Placement {
	return Placement{kind: TargetLine, file: i, side: side, start: n, end: n}
}

// OnRange places a comment on lines start..end of file i's side.
func OnRange(i int, side Side, start, end int) Placement {
	return Placement{kind: TargetRange, file: i, side: side, start: start, end: end}
}

// Kind is what the placement is attached to.
func (p Placement) Kind() TargetKind {
	if p.kind == "" {
		return TargetReview
	}
	return p.kind
}

// Kind is what the target is attached to.
func (t Target) Kind() TargetKind {
	if t.kind == "" {
		return TargetReview
	}
	return t.kind
}

// Path is the path a file, line or range target names, or "" for the review target.
func (t Target) Path() string { return t.path }

// Side is a line or range target's side, or "" for review and file targets.
func (t Target) Side() Side { return t.side }

// Lines are a line or range target's first and last lines (equal for a line target), or 0, 0.
func (t Target) Lines() (start, end int) { return t.start, t.end }

// Comment is one validated comment.
type Comment struct {
	ID     string
	Author Author
	Body   string
	// CopiedFrom is the ID of the earlier comment this comment was copied from by Still applies,
	// or "" (REQ-CMT-011). It is opaque: no check ties it to an earlier comment.
	CopiedFrom string
	Target     Target
	// File is the changed-file index Target names, or -1 for a review comment.
	File int
	// Seq is the creation order, unique within a draft.
	Seq int64
	// Version identifies this state of the comment within the session: every create, restore and
	// seed, and every edit that changes the body, assigns a new one. EditComment requires it. It is
	// not persisted.
	Version int64
}

// SortKey is c's position in result order: comments sort by comparing keys element by element. It
// depends only on c, so a change to another comment never alters it.
func (c Comment) SortKey() [4]int64 {
	side := int64(0)
	switch c.Target.side {
	case Old:
		side = 1
	case New:
		side = 2
	}
	return [4]int64{int64(c.File) + 1, side, int64(c.Target.start), c.Seq}
}

// InvalidError reports input that violates a comment, target or limit rule.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidError{Reason: fmt.Sprintf(format, args...)}
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func checkID(id string) error {
	if !idPattern.MatchString(id) {
		return invalid("comment id must be 1-64 characters of A-Z, a-z, 0-9, '_' or '-'")
	}
	return nil
}

// NormalizeText converts CRLF and CR line endings to LF.
func NormalizeText(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// normalizeBody returns the stored form of a comment body or why it is rejected.
func normalizeBody(body string) (string, error) {
	if !utf8.ValidString(body) {
		return "", invalid("comment body is not valid UTF-8")
	}
	body = NormalizeText(body)
	if strings.TrimSpace(body) == "" {
		return "", invalid("a comment cannot be empty or only whitespace")
	}
	if len(body) > MaxBodyBytes {
		return "", invalid("comment body exceeds %d bytes", MaxBodyBytes)
	}
	return body, nil
}

// CommentableFile reports whether a changed file accepts file and line comments: every path it has
// must be commentable, so a rename from or to an unprintable path takes review comments only. The
// session's per-file flag and target validation both use it.
func CommentableFile(f source.ChangedFile) bool {
	return commentablePath(f.OldPath) && commentablePath(f.NewPath)
}

// commentablePath reports whether a path is valid UTF-8 without control characters.
func commentablePath(path string) bool {
	if !utf8.ValidString(path) {
		return false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ErrEnded is returned by mutations after the session has ended.
var ErrEnded = errors.New("the review session has ended")

// ErrCommentExists is returned when creating a comment whose ID is taken.
var ErrCommentExists = errors.New("the comment already exists")

// ErrNoComment is returned when editing a comment that does not exist, for example because it
// was deleted meanwhile.
var ErrNoComment = errors.New("the comment does not exist")

// ErrStale is returned when editing a comment whose version is not the one given: it changed since
// the editor loaded it.
var ErrStale = errors.New("the comment changed since it was loaded")
