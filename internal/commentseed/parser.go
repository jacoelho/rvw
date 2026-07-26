// Package commentseed parses and applies exported local comments supplied on stdin.
package commentseed

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/change"
)

// ErrInvalidDocument identifies an invalid comment seed document.
var ErrInvalidDocument = errors.New("invalid comment seed document")

// Document is one validated comment-export document.
type Document struct {
	comments []seedComment
	present  bool
}

// Present reports whether stdin contained a document rather than zero bytes.
func (document Document) Present() bool { return document.present }

// CommentCount returns the number of comments in the document.
func (document Document) CommentCount() int { return len(document.comments) }

type seedComment struct {
	body   string
	target seedTarget
}

type seedTarget struct {
	path  change.Path
	start change.LineNumber
	end   change.LineNumber
	kind  change.TargetKind
	side  change.LineSide
}

// Read reads zero bytes or one bounded exact JSON comment-export document.
func Read(reader io.Reader) (Document, error) {
	if reader == nil {
		return Document{}, fmt.Errorf("%w: stdin is unavailable", ErrInvalidDocument)
	}
	contents, err := io.ReadAll(io.LimitReader(reader, change.MaxPreparedExportBytes+1))
	if err != nil {
		return Document{}, fmt.Errorf("%w: read stdin: %w", ErrInvalidDocument, err)
	}
	if len(contents) == 0 {
		return Document{}, nil
	}
	if int64(len(contents)) > change.MaxPreparedExportBytes {
		return Document{}, fmt.Errorf(
			"%w: input exceeds %d bytes",
			ErrInvalidDocument,
			change.MaxPreparedExportBytes,
		)
	}
	if !utf8.Valid(contents) {
		return Document{}, fmt.Errorf("%w: input is not valid UTF-8", ErrInvalidDocument)
	}
	parser := jsonParser{input: contents}
	comments, err := parser.parseDocument()
	if err != nil {
		return Document{}, fmt.Errorf("%w: %w", ErrInvalidDocument, err)
	}
	return Document{comments: comments, present: true}, nil
}

type jsonParser struct {
	input []byte
	pos   int
}

type jsonString struct {
	value string
	raw   []byte
}

func (parser *jsonParser) parseDocument() ([]seedComment, error) {
	parser.space()
	if err := parser.expect('{'); err != nil {
		return nil, err
	}
	var comments []seedComment
	seenComments := false
	for {
		parser.space()
		if parser.take('}') {
			break
		}
		if seenComments {
			if err := parser.expect(','); err != nil {
				return nil, err
			}
			parser.space()
		}
		key, err := parser.parseString()
		if err != nil {
			return nil, fmt.Errorf("document field: %w", err)
		}
		if key.value != "comments" {
			return nil, fmt.Errorf("unknown document field %q", key.value)
		}
		if seenComments {
			return nil, errors.New("duplicate document field \"comments\"")
		}
		seenComments = true
		parser.space()
		if expectErr := parser.expect(':'); expectErr != nil {
			return nil, expectErr
		}
		comments, err = parser.parseComments()
		if err != nil {
			return nil, err
		}
	}
	if !seenComments {
		return nil, errors.New("missing document field \"comments\"")
	}
	parser.space()
	if parser.pos != len(parser.input) {
		return nil, errors.New("trailing data after comment seed document")
	}
	return comments, nil
}

func (parser *jsonParser) parseComments() ([]seedComment, error) {
	parser.space()
	if err := parser.expect('['); err != nil {
		return nil, fmt.Errorf("comments: %w", err)
	}
	comments := make([]seedComment, 0)
	for index := 0; ; index++ {
		parser.space()
		if parser.take(']') {
			break
		}
		if index != 0 {
			if err := parser.expect(','); err != nil {
				return nil, fmt.Errorf("comment %d: %w", index+1, err)
			}
			parser.space()
		}
		if len(comments) == change.MaxComments {
			return nil, fmt.Errorf("comment count exceeds %d", change.MaxComments)
		}
		comment, err := parser.parseComment()
		if err != nil {
			return nil, fmt.Errorf("comment %d: %w", index+1, err)
		}
		comments = append(comments, comment)
	}
	var bodyBytes int64
	for _, comment := range comments {
		if int64(len(comment.body)) > change.MaxCommentBodyTotal-bodyBytes {
			return nil, fmt.Errorf("comment bodies exceed %d bytes", change.MaxCommentBodyTotal)
		}
		bodyBytes += int64(len(comment.body))
	}
	return comments, nil
}

func (parser *jsonParser) parseComment() (seedComment, error) {
	parser.space()
	if err := parser.expect('{'); err != nil {
		return seedComment{}, err
	}
	var comment seedComment
	var targetSet, bindingSet, commentSet bool
	fields := 0
	for {
		parser.space()
		if parser.take('}') {
			break
		}
		if fields != 0 {
			if err := parser.expect(','); err != nil {
				return seedComment{}, err
			}
			parser.space()
		}
		key, err := parser.parseString()
		if err != nil {
			return seedComment{}, err
		}
		parser.space()
		if expectErr := parser.expect(':'); expectErr != nil {
			return seedComment{}, expectErr
		}
		switch key.value {
		case "target":
			if targetSet {
				return seedComment{}, errors.New("duplicate field \"target\"")
			}
			targetSet = true
			comment.target, err = parser.parseTarget()
		case "binding":
			if bindingSet {
				return seedComment{}, errors.New("duplicate field \"binding\"")
			}
			bindingSet = true
			var binding jsonString
			binding, err = parser.parseStringValue()
			if err == nil && binding.value != "active" {
				err = fmt.Errorf("binding %q is not active", binding.value)
			}
		case "comment":
			if commentSet {
				return seedComment{}, errors.New("duplicate field \"comment\"")
			}
			commentSet = true
			var body jsonString
			body, err = parser.parseStringValue()
			if err == nil {
				validated, bodyErr := change.NewCommentBody(body.value)
				if bodyErr != nil {
					err = bodyErr
				} else {
					comment.body = validated.String()
				}
			}
		default:
			return seedComment{}, fmt.Errorf("unknown field %q", key.value)
		}
		if err != nil {
			return seedComment{}, err
		}
		fields++
	}
	if !targetSet || !bindingSet || !commentSet {
		return seedComment{}, errors.New("comment requires target, binding, and comment")
	}
	return comment, nil
}

type parsedTarget struct {
	typeName string
	path     change.Path
	sideName string
	line     uint32
	start    uint32
	end      uint32
	typeSet  bool
	pathSet  bool
	sideSet  bool
	lineSet  bool
	startSet bool
	endSet   bool
}

func (parser *jsonParser) parseTarget() (seedTarget, error) {
	parser.space()
	if err := parser.expect('{'); err != nil {
		return seedTarget{}, err
	}
	var parsed parsedTarget
	fields := 0
	for {
		parser.space()
		if parser.take('}') {
			break
		}
		if fields != 0 {
			if err := parser.expect(','); err != nil {
				return seedTarget{}, err
			}
			parser.space()
		}
		key, err := parser.parseString()
		if err != nil {
			return seedTarget{}, err
		}
		parser.space()
		if expectErr := parser.expect(':'); expectErr != nil {
			return seedTarget{}, expectErr
		}
		switch key.value {
		case "type":
			if parsed.typeSet {
				return seedTarget{}, errors.New("duplicate target field \"type\"")
			}
			parsed.typeSet = true
			value, valueErr := parser.parseStringValue()
			err, parsed.typeName = valueErr, value.value
		case "path":
			if parsed.pathSet {
				return seedTarget{}, errors.New("duplicate target field \"path\"")
			}
			parsed.pathSet = true
			value, valueErr := parser.parseStringValue()
			if valueErr != nil {
				err = valueErr
			} else {
				raw, pathErr := decodeExportedPath(value.raw)
				if pathErr != nil {
					err = pathErr
				} else {
					parsed.path, err = change.NewPath(raw)
				}
			}
		case "side":
			if parsed.sideSet {
				return seedTarget{}, errors.New("duplicate target field \"side\"")
			}
			parsed.sideSet = true
			value, valueErr := parser.parseStringValue()
			err, parsed.sideName = valueErr, value.value
		case "line":
			err = parser.parseTargetNumber(&parsed.line, &parsed.lineSet, "line")
		case "start":
			err = parser.parseTargetNumber(&parsed.start, &parsed.startSet, "start")
		case "end":
			err = parser.parseTargetNumber(&parsed.end, &parsed.endSet, "end")
		default:
			return seedTarget{}, fmt.Errorf("unknown target field %q", key.value)
		}
		if err != nil {
			return seedTarget{}, err
		}
		fields++
	}
	return parsed.validate()
}

func (parser *jsonParser) parseTargetNumber(value *uint32, set *bool, name string) error {
	if *set {
		return fmt.Errorf("duplicate target field %q", name)
	}
	*set = true
	parser.space()
	start := parser.pos
	for parser.pos < len(parser.input) && parser.input[parser.pos] >= '0' && parser.input[parser.pos] <= '9' {
		parser.pos++
	}
	if start == parser.pos || parser.pos-start > 1 && parser.input[start] == '0' {
		return fmt.Errorf("target field %q must be an unsigned integer", name)
	}
	parsed, err := strconv.ParseUint(string(parser.input[start:parser.pos]), 10, 32)
	if err != nil {
		return fmt.Errorf("target field %q must fit uint32", name)
	}
	*value = uint32(parsed)
	return nil
}

func (target parsedTarget) validate() (seedTarget, error) {
	if !target.typeSet {
		return seedTarget{}, errors.New("target requires field \"type\"")
	}
	switch target.typeName {
	case "review":
		if target.pathSet || target.sideSet || target.lineSet || target.startSet || target.endSet {
			return seedTarget{}, errors.New("review target has target-specific fields")
		}
		return seedTarget{kind: change.TargetReview}, nil
	case "file":
		if !target.pathSet || target.sideSet || target.lineSet || target.startSet || target.endSet {
			return seedTarget{}, errors.New("file target requires only type and path")
		}
		return seedTarget{kind: change.TargetFile, path: target.path}, nil
	case "line":
		if !target.pathSet || !target.sideSet || !target.lineSet || target.startSet || target.endSet {
			return seedTarget{}, errors.New("line target requires type, path, side, and line")
		}
		line, err := change.NewLineNumber(target.line)
		if err != nil {
			return seedTarget{}, err
		}
		side, err := parseSide(target.sideName)
		if err != nil {
			return seedTarget{}, err
		}
		return seedTarget{kind: change.TargetLine, path: target.path, side: side, start: line, end: line}, nil
	case "range":
		if !target.pathSet || !target.sideSet || !target.startSet || !target.endSet || target.lineSet {
			return seedTarget{}, errors.New("range target requires type, path, side, start, and end")
		}
		start, err := change.NewLineNumber(target.start)
		if err != nil {
			return seedTarget{}, err
		}
		end, err := change.NewLineNumber(target.end)
		if err != nil {
			return seedTarget{}, err
		}
		if start >= end {
			return seedTarget{}, errors.New("range target requires start less than end")
		}
		side, err := parseSide(target.sideName)
		if err != nil {
			return seedTarget{}, err
		}
		return seedTarget{kind: change.TargetLine, path: target.path, side: side, start: start, end: end}, nil
	default:
		return seedTarget{}, fmt.Errorf("unknown target type %q", target.typeName)
	}
}

func parseSide(value string) (change.LineSide, error) {
	switch value {
	case "old":
		return change.LineOld, nil
	case "new":
		return change.LineNew, nil
	default:
		return 0, fmt.Errorf("unknown target side %q", value)
	}
}

func (parser *jsonParser) parseStringValue() (jsonString, error) {
	parser.space()
	return parser.parseString()
}

func (parser *jsonParser) parseString() (jsonString, error) {
	if parser.pos >= len(parser.input) || parser.input[parser.pos] != '"' {
		return jsonString{}, errors.New("expected JSON string")
	}
	start := parser.pos
	parser.pos++
	for parser.pos < len(parser.input) {
		switch parser.input[parser.pos] {
		case '"':
			parser.pos++
			raw := parser.input[start:parser.pos]
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return jsonString{}, fmt.Errorf("invalid JSON string: %w", err)
			}
			return jsonString{value: value, raw: raw}, nil
		case '\\':
			parser.pos += 2
		default:
			parser.pos++
		}
	}
	return jsonString{}, errors.New("unterminated JSON string")
}

func (parser *jsonParser) expect(value byte) error {
	if parser.pos >= len(parser.input) || parser.input[parser.pos] != value {
		return fmt.Errorf("expected %q at byte %d", value, parser.pos)
	}
	parser.pos++
	return nil
}

func (parser *jsonParser) take(value byte) bool {
	if parser.pos >= len(parser.input) || parser.input[parser.pos] != value {
		return false
	}
	parser.pos++
	return true
}

func (parser *jsonParser) space() {
	for parser.pos < len(parser.input) {
		switch parser.input[parser.pos] {
		case ' ', '\t', '\n', '\r':
			parser.pos++
		default:
			return
		}
	}
}

func decodeExportedPath(token []byte) ([]byte, error) {
	if len(token) < 2 || token[0] != '"' || token[len(token)-1] != '"' {
		return nil, errors.New("path is not a JSON string")
	}
	decoded := make([]byte, 0, len(token)-2)
	for index := 1; index < len(token)-1; {
		if token[index] != '\\' {
			_, size := utf8.DecodeRune(token[index : len(token)-1])
			decoded = append(decoded, token[index:index+size]...)
			index += size
			continue
		}
		index++
		if index >= len(token)-1 {
			return nil, errors.New("path has incomplete JSON escape")
		}
		switch token[index] {
		case '"', '\\', '/':
			decoded = append(decoded, token[index])
			index++
		case 'b':
			decoded = append(decoded, '\b')
			index++
		case 'f':
			decoded = append(decoded, '\f')
			index++
		case 'n':
			decoded = append(decoded, '\n')
			index++
		case 'r':
			decoded = append(decoded, '\r')
			index++
		case 't':
			decoded = append(decoded, '\t')
			index++
		case 'u':
			value, next, err := decodeHexEscape(token, index)
			if err != nil {
				return nil, err
			}
			index = next
			if value >= 0xd800 && value <= 0xdbff {
				if index+2 > len(token)-1 || token[index] != '\\' || token[index+1] != 'u' {
					return nil, errors.New("path has unpaired high surrogate")
				}
				low, lowNext, lowErr := decodeHexEscape(token, index+1)
				if lowErr != nil || low < 0xdc00 || low > 0xdfff {
					return nil, errors.New("path has invalid surrogate pair")
				}
				decoded = utf8.AppendRune(decoded, utf16.DecodeRune(rune(value), rune(low)))
				index = lowNext
				continue
			}
			if value >= 0xdc00 && value <= 0xdfff {
				return nil, errors.New("path has unpaired low surrogate")
			}
			if value <= 0xff {
				decoded = append(decoded, byte(value))
			} else {
				decoded = utf8.AppendRune(decoded, rune(value))
			}
		default:
			return nil, fmt.Errorf("path has unknown JSON escape \\%c", token[index])
		}
	}
	return decoded, nil
}

func decodeHexEscape(token []byte, uIndex int) (uint16, int, error) {
	if uIndex+5 > len(token)-1 || token[uIndex] != 'u' {
		return 0, uIndex, errors.New("path has incomplete Unicode escape")
	}
	value, err := strconv.ParseUint(string(token[uIndex+1:uIndex+5]), 16, 16)
	if err != nil {
		return 0, uIndex, errors.New("path has invalid Unicode escape")
	}
	return uint16(value), uIndex + 5, nil
}
