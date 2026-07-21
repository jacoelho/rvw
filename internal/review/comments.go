package review

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode/utf8"
)

// MaxComments defines the supported comments limit.
const (
	MaxComments               = 4_096
	MaxCommentBodyBytes int64 = 64 << 10
	MaxCommentBodyTotal int64 = 4 << 20
)

// ErrInvalidComment identifies an invalid comment failure.
var ErrInvalidComment = errors.New("invalid comment")

// LineSide represents line side.
type LineSide uint8

// LineOld identifies a supported line old value.
const (
	LineOld LineSide = iota + 1
	LineNew
)

// LineNumber represents line number.
type LineNumber uint32

// NewLineNumber constructs and validates line number.
func NewLineNumber(value uint32) (LineNumber, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: line number must be positive", ErrInvalidComment)
	}
	return LineNumber(value), nil
}

// Uint32 returns the receiver's uint32 value.
func (line LineNumber) Uint32() uint32 {
	return uint32(line)
}

type commentAnchor struct {
	file FileID
	side LineSide
	line LineNumber
}

type commentAnchorCounts map[commentAnchor]uint16

func targetCommentAnchor(target CommentTarget) (commentAnchor, bool) {
	file, mapped := target.FileID()
	side, _, end, lineTarget := target.Lines()
	if !mapped || !lineTarget {
		return commentAnchor{}, false
	}
	return commentAnchor{file: file, side: side, line: end}, true
}

func savedCommentAnchor(comment Comment) (commentAnchor, bool) {
	if comment.stale || comment.unmappable {
		return commentAnchor{}, false
	}
	return targetCommentAnchor(comment.target)
}

func replaceCommentAnchor(
	anchors commentAnchorCounts,
	before *Comment,
	after *Comment,
) commentAnchorCounts {
	var (
		oldAnchor commentAnchor
		hasOld    bool
		newAnchor commentAnchor
		hasNew    bool
	)
	if before != nil {
		oldAnchor, hasOld = savedCommentAnchor(*before)
	}
	if after != nil {
		newAnchor, hasNew = savedCommentAnchor(*after)
	}
	if hasOld == hasNew && (!hasOld || oldAnchor == newAnchor) {
		return anchors
	}
	next := maps.Clone(anchors)
	if next == nil {
		next = make(commentAnchorCounts)
	}
	if hasOld {
		if next[oldAnchor] <= 1 {
			delete(next, oldAnchor)
		} else {
			next[oldAnchor]--
		}
	}
	if hasNew {
		next[newAnchor]++
	}
	return next
}

func indexCommentAnchors(comments []Comment) commentAnchorCounts {
	anchors := make(commentAnchorCounts, min(len(comments), MaxComments))
	for index := range comments {
		if anchor, ok := savedCommentAnchor(comments[index]); ok {
			anchors[anchor]++
		}
	}
	return anchors
}

// TargetKind describes target kind.
type TargetKind uint8

// TargetReview identifies a supported target review value.
const (
	TargetReview TargetKind = iota + 1
	TargetFile
	TargetLine
)

// CommentTarget represents comment target.
type CommentTarget struct {
	path   *Path
	file   FileID
	start  LineNumber
	end    LineNumber
	kind   TargetKind
	mapped bool
	side   LineSide
}

// ReviewCommentTarget performs the review comment target operation.
func ReviewCommentTarget() CommentTarget {
	return CommentTarget{kind: TargetReview}
}

// FileCommentTarget performs the file comment target operation.
func FileCommentTarget(file FileID) (CommentTarget, error) {
	if uint32(file) >= MaxFiles {
		return CommentTarget{}, ErrInvalidComment
	}
	return CommentTarget{kind: TargetFile, file: file, mapped: true}, nil
}

// LineCommentTarget performs the line comment target operation.
func LineCommentTarget(
	file FileID,
	side LineSide,
	start, end LineNumber,
) (CommentTarget, error) {
	if uint32(file) >= MaxFiles || side != LineOld && side != LineNew ||
		start == 0 || end == 0 || end < start {
		return CommentTarget{}, ErrInvalidComment
	}
	return CommentTarget{
		kind: TargetLine, file: file, mapped: true,
		side: side, start: start, end: end,
	}, nil
}

// Kind returns the receiver's kind value.
func (target CommentTarget) Kind() TargetKind {
	return target.kind
}

// FileID returns the receiver's file id value.
func (target CommentTarget) FileID() (FileID, bool) {
	return target.file, target.mapped &&
		(target.kind == TargetFile || target.kind == TargetLine)
}

// Lines returns the receiver's lines value.
func (target CommentTarget) Lines() (LineSide, LineNumber, LineNumber, bool) {
	return target.side, target.start, target.end, target.kind == TargetLine
}

// AnchoredPath returns the receiver's anchored path value.
func (target CommentTarget) AnchoredPath() (Path, bool) {
	if target.path == nil {
		return Path{}, false
	}
	return *target.path, true
}

func (target CommentTarget) withPath(path Path) CommentTarget {
	target.path = new(path)
	return target
}

func (target CommentTarget) remap(file FileID, path Path) CommentTarget {
	target.file = file
	target.mapped = true
	target.path = new(path)
	return target
}

func (target CommentTarget) withoutFileMapping() CommentTarget {
	target.mapped = false
	return target
}

// CommentBody represents comment body.
type CommentBody string

// NewCommentBody constructs and validates comment body.
func NewCommentBody(value string) (CommentBody, error) {
	if !utf8.ValidString(value) || int64(len(value)) > MaxCommentBodyBytes ||
		strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: body must be non-whitespace UTF-8 of at most %d bytes", ErrInvalidComment, MaxCommentBodyBytes)
	}
	return CommentBody(strings.Clone(value)), nil
}

// String returns the receiver's string value.
func (body CommentBody) String() string {
	return string(body)
}

// Comment represents comment.
type Comment struct {
	body       CommentBody
	target     CommentTarget
	id         CommentID
	stale      bool
	unmappable bool
}

// ID returns the receiver's id value.
func (comment Comment) ID() CommentID {
	return comment.id
}

// Target returns the receiver's target value.
func (comment Comment) Target() CommentTarget {
	return comment.target
}

// Body returns the receiver's body value.
func (comment Comment) Body() CommentBody {
	return comment.body
}

// Stale reports the receiver's stale state.
func (comment Comment) Stale() bool {
	return comment.stale
}

// Unmappable reports the receiver's unmappable state.
func (comment Comment) Unmappable() bool {
	return comment.unmappable
}

// Draft represents draft.
type Draft struct {
	editing *CommentID
	body    string
	target  CommentTarget
}

// Target returns the receiver's target value.
func (draft Draft) Target() CommentTarget {
	return draft.target
}

// Body returns the receiver's body value.
func (draft Draft) Body() string {
	return draft.body
}

// EditingComment returns the receiver's editing comment value.
func (draft Draft) EditingComment() (CommentID, bool) {
	if draft.editing == nil {
		return 0, false
	}
	return *draft.editing, true
}
