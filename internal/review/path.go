package review

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidPath = errors.New("invalid review path")

type Path struct {
	raw string
}

func NewPath(raw []byte) (Path, error) {
	switch {
	case len(raw) == 0:
		return Path{}, fmt.Errorf("%w: path is empty", ErrInvalidPath)
	case int64(len(raw)) > MaxPathBytes:
		return Path{}, fmt.Errorf("%w: path uses %d bytes; limit is %d", ErrInvalidPath, len(raw), MaxPathBytes)
	case strings.IndexByte(string(raw), 0) >= 0:
		return Path{}, fmt.Errorf("%w: path contains NUL", ErrInvalidPath)
	}

	return Path{raw: string(raw)}, nil
}

func (path Path) Raw() []byte {
	return []byte(path.raw)
}

func (path Path) UTF8() (string, bool) {
	if !utf8.ValidString(path.raw) {
		return "", false
	}
	return path.raw, true
}

func (path Path) Display() string {
	return escapePath(path.raw)
}

func (path Path) Base64() string {
	return base64.StdEncoding.EncodeToString([]byte(path.raw))
}

func (path Path) Equal(other Path) bool {
	return path.raw == other.raw
}

func escapePath(raw string) string {
	var escaped strings.Builder
	escaped.Grow(len(raw))
	for len(raw) > 0 {
		r, size := utf8.DecodeRuneInString(raw)
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&escaped, `\x%02X`, raw[0])
		case r == '\\':
			escaped.WriteString(`\\`)
		case unicode.IsControl(r):
			if r <= 0xff {
				fmt.Fprintf(&escaped, `\x%02X`, r)
			} else {
				fmt.Fprintf(&escaped, `\u{%04X}`, r)
			}
		case isBidirectionalControl(r):
			fmt.Fprintf(&escaped, `\u{%04X}`, r)
		default:
			escaped.WriteRune(r)
		}
		raw = raw[size:]
	}
	return escaped.String()
}

func isBidirectionalControl(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		r >= '\u202a' && r <= '\u202e' ||
		r >= '\u2066' && r <= '\u2069'
}
