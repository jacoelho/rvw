package review

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrInvalidPath identifies an invalid path failure.
var ErrInvalidPath = errors.New("invalid review path")

// Path represents path.
type Path struct {
	raw string
}

// NewPath constructs and validates path.
func NewPath(raw []byte) (Path, error) {
	switch {
	case len(raw) == 0:
		return Path{}, fmt.Errorf("%w: path is empty", ErrInvalidPath)
	case int64(len(raw)) > MaxPathBytes:
		return Path{}, fmt.Errorf("%w: path uses %d bytes; limit is %d", ErrInvalidPath, len(raw), MaxPathBytes)
	case bytes.IndexByte(raw, 0) >= 0:
		return Path{}, fmt.Errorf("%w: path contains NUL", ErrInvalidPath)
	}

	return Path{raw: string(raw)}, nil
}

// Raw returns the receiver's raw value.
func (path Path) Raw() []byte {
	return []byte(path.raw)
}

// UTF8 returns the receiver's utf8 value.
func (path Path) UTF8() (string, bool) {
	if !utf8.ValidString(path.raw) {
		return "", false
	}
	return path.raw, true
}

// Display returns the receiver's display value.
func (path Path) Display() string {
	return escapePath(path.raw)
}

// Base64 returns the receiver's base64 value.
func (path Path) Base64() string {
	return base64.StdEncoding.EncodeToString([]byte(path.raw))
}

// Equal performs the equal operation.
func (path Path) Equal(other Path) bool {
	return path.raw == other.raw
}

func escapePath(raw string) string {
	var escaped strings.Builder
	escaped.Grow(len(raw))
	for raw != "" {
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
