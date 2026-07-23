package review

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const encodedCommentKindScalarBytes = 8

// commentContent is the semantic value synchronized with an RVW-owned GitHub
// comment. wire text and ownership markers are retained separately.
type commentContent struct {
	kind CommentKind
	body string
}

func commentContentFrom(comment Comment) commentContent {
	return commentContent{kind: comment.kind, body: comment.body.String()}
}

func cloneCommentContent(content commentContent) commentContent {
	content.body = strings.Clone(content.body)
	return content
}

func encodeCommentWire(content commentContent, policies CommentPolicies) (string, error) {
	if !policies.valid() || content.kind.label == "" {
		return "", ErrInvalidCommentKinds
	}
	if int64(len(content.body)) > MaxCommentBodyBytes {
		return "", fmt.Errorf("%w: GitHub comment exceeds %d bytes", ErrInvalidComment, MaxCommentBodyBytes)
	}
	if !policies.Has(CommentTypePrefixForGitHub) {
		return strings.Clone(content.body), nil
	}
	prefixBytes := int64(3)
	for _, character := range content.kind.label {
		switch {
		case commentKindWireEscaped(character):
			prefixBytes += encodedCommentKindScalarBytes
		case character == '\\' || character == ']':
			prefixBytes += 2
		default:
			prefixBytes += int64(utf8.RuneLen(character))
		}
	}
	if prefixBytes > int64(MaxEncodedCommentKindPrefixBytes) ||
		prefixBytes+int64(len(content.body)) > MaxCommentBodyBytes {
		return "", fmt.Errorf("%w: encoded GitHub comment exceeds %d bytes", ErrInvalidComment, MaxCommentBodyBytes)
	}
	var builder strings.Builder
	builder.Grow(int(prefixBytes) + len(content.body))
	builder.WriteByte('[')
	for _, character := range content.kind.label {
		switch {
		case commentKindWireEscaped(character):
			writeCommentKindWireEscape(&builder, character)
		case character == '\\' || character == ']':
			builder.WriteByte('\\')
			builder.WriteRune(character)
		default:
			builder.WriteRune(character)
		}
	}
	builder.WriteString("] ")
	builder.WriteString(content.body)
	return builder.String(), nil
}

// decodeOwnedCommentWire decodes only text whose RVW ownership marker was
// recognized by the caller. Unknown or malformed prefixes remain ordinary
// comment text under the lexical first configured kind.
func decodeOwnedCommentWire(wire string, kinds CommentKinds) commentContent {
	fallback := commentContent{kind: kinds.first(), body: wire}
	if len(wire) < 4 || wire[0] != '[' {
		return fallback
	}
	label := make([]byte, 0, min(len(wire), MaxCommentKindBytes))
	for offset := 1; offset < len(wire); {
		switch wire[offset] {
		case ']':
			if offset+1 >= len(wire) || wire[offset+1] != ' ' {
				return fallback
			}
			kind, ok := kinds.Lookup(string(label))
			if !ok {
				return fallback
			}
			return commentContent{kind: kind, body: wire[offset+2:]}
		case '\\':
			if offset+1 >= len(wire) {
				return fallback
			}
			switch wire[offset+1] {
			case '\\', ']':
				label = append(label, wire[offset+1])
				offset += 2
			case 'u':
				character, ok := decodeCommentKindWireEscape(wire[offset:])
				if !ok {
					return fallback
				}
				label = utf8.AppendRune(label, character)
				offset += encodedCommentKindScalarBytes
			default:
				return fallback
			}
		default:
			character, width := utf8.DecodeRuneInString(wire[offset:])
			if character == utf8.RuneError || commentKindWireEscaped(character) {
				return fallback
			}
			label = append(label, wire[offset:offset+width]...)
			offset += width
		}
		if len(label) > MaxCommentKindBytes {
			return fallback
		}
	}
	return fallback
}

func commentKindWireEscaped(character rune) bool {
	return character == utf8.RuneError || isBidirectionalControl(character)
}

func writeCommentKindWireEscape(builder *strings.Builder, character rune) {
	const hexadecimal = "0123456789ABCDEF"
	builder.WriteString(`\u{`)
	for shift := 12; shift >= 0; shift -= 4 {
		builder.WriteByte(hexadecimal[character>>shift&0x0f])
	}
	builder.WriteByte('}')
}

func decodeCommentKindWireEscape(value string) (rune, bool) {
	if len(value) < encodedCommentKindScalarBytes || value[0:3] != `\u{` || value[7] != '}' {
		return 0, false
	}
	var character rune
	for index := 3; index < 7; index++ {
		character <<= 4
		switch digit := value[index]; {
		case digit >= '0' && digit <= '9':
			character |= rune(digit - '0')
		case digit >= 'A' && digit <= 'F':
			character |= rune(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return character, commentKindWireEscaped(character)
}
