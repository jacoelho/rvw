package review

import (
	"errors"
	"fmt"
	"strings"
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

type commentAnchorMembership struct {
	anchor  commentAnchor
	present bool
}

func (state *commentState) commentAnchorMembership(comment *Comment) commentAnchorMembership {
	if comment == nil {
		return commentAnchorMembership{}
	}
	if comment.flags.has(commentFlagStale) || comment.flags.has(commentFlagUnmappable) {
		return commentAnchorMembership{}
	}
	if state.pending != nil {
		if binding, bound := state.pending.bindings[comment.id]; bound && binding.deleted {
			return commentAnchorMembership{}
		}
	}
	anchor, present := targetCommentAnchor(comment.target)
	return commentAnchorMembership{anchor: anchor, present: present}
}

func (state *commentState) commentAnchorMembershipByID(id CommentID) commentAnchorMembership {
	index := commentIndex(state.comments, id)
	if index < 0 {
		return commentAnchorMembership{}
	}
	return state.commentAnchorMembership(&state.comments[index])
}

func (state *commentState) rebuildCommentAnchorsCancellable(cancelled func() error) error {
	if err := cancellationError(cancelled); err != nil {
		return err
	}
	anchors := make(commentAnchorCounts)
	for index := range state.comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return err
			}
		}
		membership := state.commentAnchorMembership(&state.comments[index])
		if membership.present {
			anchors[membership.anchor]++
		}
	}
	state.commentAnchors = anchors
	return nil
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
	nonSpace, err := scanCommentText(nil, value)
	if err != nil || !nonSpace {
		return "", fmt.Errorf("%w: body must be non-whitespace UTF-8 of at most %d bytes", ErrInvalidComment, MaxCommentBodyBytes)
	}
	return CommentBody(strings.Clone(value)), nil
}

// String returns the receiver's string value.
func (body CommentBody) String() string {
	return string(body)
}

type commentFlag uint8

const (
	commentFlagStale commentFlag = iota + 1
	commentFlagUnmappable
)

const commentFlagMask = 1<<commentFlagStale | 1<<commentFlagUnmappable

type commentFlags struct {
	bits uint8
}

func (flags commentFlags) valid() bool {
	return flags.bits&^commentFlagMask == 0
}

func (flags commentFlags) has(value commentFlag) bool {
	if !flags.valid() || value != commentFlagStale && value != commentFlagUnmappable {
		return false
	}
	return flags.bits&(1<<value) != 0
}

func (flags commentFlags) markStale() commentFlags {
	flags.bits |= 1 << commentFlagStale
	return flags
}

func (flags commentFlags) markUnmappable() commentFlags {
	flags.bits |= 1 << commentFlagUnmappable
	return flags
}

func (flags commentFlags) clearUnmappable() commentFlags {
	flags.bits &^= 1 << commentFlagUnmappable
	return flags
}

// Comment represents comment.
type Comment struct {
	body   CommentBody
	kind   CommentKind
	target CommentTarget
	id     CommentID
	flags  commentFlags
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

// Kind returns the comment's configured kind.
func (comment Comment) Kind() CommentKind {
	return comment.kind
}

// Stale reports the receiver's stale state.
func (comment Comment) Stale() bool {
	return comment.flags.has(commentFlagStale)
}

// Unmappable reports the receiver's unmappable state.
func (comment Comment) Unmappable() bool {
	return comment.flags.has(commentFlagUnmappable)
}

// Draft represents draft.
type Draft struct {
	editing *CommentID
	body    string
	kind    CommentKind
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

// Kind returns the draft's configured kind.
func (draft Draft) Kind() CommentKind {
	return draft.kind
}

// EditingComment returns the receiver's editing comment value.
func (draft Draft) EditingComment() (CommentID, bool) {
	if draft.editing == nil {
		return 0, false
	}
	return *draft.editing, true
}
