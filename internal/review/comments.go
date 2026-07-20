package review

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxComments               = 4_096
	MaxCommentBodyBytes int64 = 64 << 10
	MaxCommentBodyTotal int64 = 4 << 20
)

var ErrInvalidComment = errors.New("invalid comment")

type CommentKind string

func NewCommentKind(value string) (CommentKind, error) {
	value = strings.TrimSpace(value)
	if value == "" || int64(len(value)) > 256 || !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: kind must use 1 through 256 UTF-8 bytes", ErrInvalidComment)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("%w: kind contains a control character", ErrInvalidComment)
		}
	}
	return CommentKind(value), nil
}

func (kind CommentKind) String() string {
	return string(kind)
}

type CommentKinds struct {
	values []CommentKind
}

func NewCommentKinds(values ...CommentKind) (CommentKinds, error) {
	validated, err := validateKinds(values)
	if err != nil {
		return CommentKinds{}, err
	}
	return CommentKinds{values: validated}, nil
}

func (kinds CommentKinds) Values() []CommentKind {
	return slices.Clone(kinds.values)
}

type LineSide uint8

const (
	LineOld LineSide = iota + 1
	LineNew
)

type LineNumber uint32

func NewLineNumber(value uint32) (LineNumber, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: line number must be positive", ErrInvalidComment)
	}
	return LineNumber(value), nil
}

func (line LineNumber) Uint32() uint32 {
	return uint32(line)
}

type TargetKind uint8

const (
	TargetReview TargetKind = iota + 1
	TargetFile
	TargetLine
)

type CommentTarget struct {
	kind   TargetKind
	file   FileID
	mapped bool
	side   LineSide
	start  LineNumber
	end    LineNumber
	path   *Path
}

func ReviewCommentTarget() CommentTarget {
	return CommentTarget{kind: TargetReview}
}

func FileCommentTarget(file FileID) (CommentTarget, error) {
	if uint32(file) >= MaxFiles {
		return CommentTarget{}, ErrInvalidComment
	}
	return CommentTarget{kind: TargetFile, file: file, mapped: true}, nil
}

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

func (target CommentTarget) Kind() TargetKind {
	return target.kind
}

func (target CommentTarget) FileID() (FileID, bool) {
	return target.file, target.mapped &&
		(target.kind == TargetFile || target.kind == TargetLine)
}

func (target CommentTarget) Lines() (LineSide, LineNumber, LineNumber, bool) {
	return target.side, target.start, target.end, target.kind == TargetLine
}

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

type CommentBody string

func NewCommentBody(value string) (CommentBody, error) {
	if !utf8.ValidString(value) || int64(len(value)) > MaxCommentBodyBytes ||
		strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: body must be non-whitespace UTF-8 of at most %d bytes", ErrInvalidComment, MaxCommentBodyBytes)
	}
	return CommentBody(value), nil
}

func (body CommentBody) String() string {
	return string(body)
}

type Comment struct {
	id         CommentID
	kind       CommentKind
	target     CommentTarget
	body       CommentBody
	stale      bool
	unmappable bool
}

func (comment Comment) ID() CommentID {
	return comment.id
}

func (comment Comment) Kind() CommentKind {
	return comment.kind
}

func (comment Comment) Target() CommentTarget {
	return comment.target
}

func (comment Comment) Body() CommentBody {
	return comment.body
}

func (comment Comment) Stale() bool {
	return comment.stale
}

func (comment Comment) Unmappable() bool {
	return comment.unmappable
}

type Draft struct {
	kind    CommentKind
	target  CommentTarget
	body    string
	editing *CommentID
}

func (draft Draft) Kind() CommentKind {
	return draft.kind
}

func (draft Draft) Target() CommentTarget {
	return draft.target
}

func (draft Draft) Body() string {
	return draft.body
}

func (draft Draft) EditingComment() (CommentID, bool) {
	if draft.editing == nil {
		return 0, false
	}
	return *draft.editing, true
}

func validateKinds(kinds []CommentKind) ([]CommentKind, error) {
	if len(kinds) == 0 || len(kinds) > 32 {
		return nil, fmt.Errorf("%w: configured kinds must contain 1 through 32 values", ErrInvalidComment)
	}
	owned := slices.Clone(kinds)
	slices.Sort(owned)
	for index, kind := range owned {
		validated, err := NewCommentKind(kind.String())
		if err != nil {
			return nil, err
		}
		owned[index] = validated
		if index > 0 && owned[index-1] == validated {
			return nil, fmt.Errorf("%w: duplicate kind %q", ErrInvalidComment, kind)
		}
	}
	return owned, nil
}
