package review

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

type commentExportJSON struct {
	Target  commentTargetJSON `json:"target"`
	Binding string            `json:"binding"`
	Comment string            `json:"comment"`
}

type commentTargetJSON struct {
	Type  string          `json:"type"`
	Path  *exportPathJSON `json:"path,omitempty"`
	Side  string          `json:"side,omitempty"`
	Line  *uint32         `json:"line,omitempty"`
	Start *uint32         `json:"start,omitempty"`
	End   *uint32         `json:"end,omitempty"`
}

type exportPathJSON struct {
	path Path
}

func (path exportPathJSON) MarshalJSON() ([]byte, error) {
	raw := path.path.Raw()
	encoded := make([]byte, 0, len(raw)+2)
	encoded = append(encoded, '"')
	for len(raw) != 0 {
		character, size := utf8.DecodeRune(raw)
		if character == utf8.RuneError && size == 1 {
			encoded = appendJSONUnicodeByte(encoded, raw[0])
			raw = raw[1:]
			continue
		}
		if isUnsafeJSONRune(character) {
			encoded = appendJSONUnicodeRune(encoded, character)
			raw = raw[size:]
			continue
		}
		piece, err := json.Marshal(string(character))
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, piece[1:len(piece)-1]...)
		raw = raw[size:]
	}
	encoded = append(encoded, '"')
	return encoded, nil
}

func (review Review) PrepareJSON(options ExportOptions) (PreparedExport, error) {
	return review.ExportInput().PrepareJSONCancellable(nil, options)
}

func (review Review) PrepareJSONCancellable(
	cancelled func() error,
	options ExportOptions,
) (PreparedExport, error) {
	return review.ExportInput().PrepareJSONCancellable(cancelled, options)
}

func (input ExportInput) PrepareJSONCancellable(
	cancelled func() error,
	options ExportOptions,
) (PreparedExport, error) {
	if !input.ready {
		return PreparedExport{}, fmt.Errorf("%w: export input is unavailable", ErrInvalidExport)
	}
	if !options.valid() {
		return PreparedExport{}, fmt.Errorf("%w: export options contain unknown bits", ErrInvalidExport)
	}
	selected := input.exportComments(options)
	comments := make([]commentExportJSON, 0, len(selected))
	for index, comment := range selected {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return PreparedExport{}, err
			}
		}
		target, err := exportCommentTargetJSON(comment.target)
		if err != nil {
			return PreparedExport{}, err
		}
		binding := "active"
		if comment.unmappable {
			binding = "orphaned"
		} else if comment.stale {
			binding = "stale"
		}
		comments = append(comments, commentExportJSON{
			Target:  target,
			Binding: binding,
			Comment: comment.body.String(),
		})
	}
	body, err := json.Marshal(struct {
		Comments []commentExportJSON `json:"comments"`
	}{Comments: comments})
	if err != nil {
		return PreparedExport{}, fmt.Errorf("%w: encode JSON export: %w", ErrInvalidExport, err)
	}
	if err := cancellationError(cancelled); err != nil {
		return PreparedExport{}, err
	}
	if int64(len(body)+1) > MaxPreparedExportBytes {
		return PreparedExport{}, fmt.Errorf(
			"%w: prepared body exceeds %d bytes",
			ErrInvalidExport,
			MaxPreparedExportBytes,
		)
	}
	body = append(body, '\n')
	return PreparedExport{format: ExportJSON, body: string(body)}, nil
}

func exportCommentTargetJSON(target exportCommentTarget) (commentTargetJSON, error) {
	switch target.kind {
	case TargetReview:
		return commentTargetJSON{Type: "review"}, nil
	case TargetFile:
		if !target.hasPath {
			return commentTargetJSON{}, fmt.Errorf("%w: file comment has no path", ErrInvalidExport)
		}
		return commentTargetJSON{
			Type: "file",
			Path: &exportPathJSON{path: target.path},
		}, nil
	case TargetLine:
		if !target.hasPath {
			return commentTargetJSON{}, fmt.Errorf("%w: line comment target is incomplete", ErrInvalidExport)
		}
		sideName := "new"
		if target.side == LineOld {
			sideName = "old"
		}
		startValue := target.start.Uint32()
		targetJSON := commentTargetJSON{
			Type: "line",
			Path: &exportPathJSON{path: target.path},
			Side: sideName,
			Line: &startValue,
		}
		if target.start != target.end {
			endValue := target.end.Uint32()
			targetJSON.Type = "range"
			targetJSON.Line = nil
			targetJSON.Start = &startValue
			targetJSON.End = &endValue
		}
		return targetJSON, nil
	default:
		return commentTargetJSON{}, fmt.Errorf("%w: unknown comment target", ErrInvalidExport)
	}
}

func appendJSONUnicodeByte(destination []byte, value byte) []byte {
	const hexadecimal = "0123456789abcdef"
	return append(
		destination,
		'\\', 'u', '0', '0',
		hexadecimal[value>>4],
		hexadecimal[value&0x0f],
	)
}

func appendJSONUnicodeRune(destination []byte, value rune) []byte {
	const hexadecimal = "0123456789abcdef"
	if value <= 0xffff {
		return append(
			destination,
			'\\', 'u',
			hexadecimal[value>>12&0x0f],
			hexadecimal[value>>8&0x0f],
			hexadecimal[value>>4&0x0f],
			hexadecimal[value&0x0f],
		)
	}
	value -= 0x10000
	high := rune(0xd800) + value>>10
	low := rune(0xdc00) + value&0x3ff
	destination = appendJSONUnicodeRune(destination, high)
	return appendJSONUnicodeRune(destination, low)
}

func isUnsafeJSONRune(character rune) bool {
	return character == '\u061c' || character == '\u200e' || character == '\u200f' ||
		character >= '\u202a' && character <= '\u202e' ||
		character >= '\u2066' && character <= '\u2069'
}
