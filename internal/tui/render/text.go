// Package render owns terminal-safe text and shared cell primitives.
package render

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

const sourceTabWidth = 4

// Renderer sanitizes consecutive text segments while preserving their shared
// terminal column for tab expansion.
type Renderer struct {
	column int
}

// SafeText escapes terminal controls and expands tabs in one text segment.
func SafeText(value string) string {
	return new(Renderer).Render(value)
}

// Render sanitizes a segment beginning at the renderer's current column.
func (renderer *Renderer) Render(value string) string {
	if terminalTextIsSafe(value) {
		renderer.column += lipgloss.Width(value)
		return value
	}
	var safe strings.Builder
	safe.Grow(len(value))
	plainStart := 0
	flushPlain := func(end int) {
		if plainStart == end {
			return
		}
		plain := value[plainStart:end]
		safe.WriteString(plain)
		renderer.column += lipgloss.Width(plain)
	}
	for offset := 0; offset < len(value); {
		character, size := utf8.DecodeRuneInString(value[offset:])
		if character != '\t' &&
			(character != utf8.RuneError || size != 1) &&
			!unicode.IsControl(character) &&
			!isBidiControl(character) {
			offset += size
			continue
		}

		flushPlain(offset)
		switch {
		case character == '\t':
			spaces := sourceTabWidth - renderer.column%sourceTabWidth
			safe.WriteString(strings.Repeat(" ", spaces))
			renderer.column += spaces
		case character == utf8.RuneError && size == 1:
			writeByteEscape(&safe, value[offset])
			renderer.column += 4
		case character >= 0 && character <= unicode.MaxASCII:
			writeByteEscape(&safe, byte(character))
			renderer.column += 4
		default:
			renderer.column += writeRuneEscape(&safe, character)
		}
		offset += size
		plainStart = offset
	}
	flushPlain(len(value))
	return safe.String()
}

func terminalTextIsSafe(value string) bool {
	for offset := 0; offset < len(value); {
		character, size := utf8.DecodeRuneInString(value[offset:])
		if character == '\t' || character == utf8.RuneError && size == 1 ||
			unicode.IsControl(character) || isBidiControl(character) {
			return false
		}
		offset += size
	}
	return true
}

func writeByteEscape(output *strings.Builder, value byte) {
	const hexadecimal = "0123456789ABCDEF"
	output.WriteString(`\x`)
	output.WriteByte(hexadecimal[value>>4])
	output.WriteByte(hexadecimal[value&0x0f])
}

func writeRuneEscape(output *strings.Builder, value rune) int {
	const hexadecimal = "0123456789ABCDEF"
	var digits [6]byte
	position := len(digits)
	for value != 0 {
		position--
		digits[position] = hexadecimal[value&0x0f]
		value >>= 4
	}
	for len(digits)-position < 4 {
		position--
		digits[position] = '0'
	}
	output.WriteString(`\u{`)
	output.Write(digits[position:])
	output.WriteByte('}')
	return len(digits) - position + 4
}

func isBidiControl(character rune) bool {
	return character == '\u061c' || character == '\u200e' || character == '\u200f' ||
		character >= '\u202a' && character <= '\u202e' ||
		character >= '\u2066' && character <= '\u2069'
}

// ContainsBidiControl reports whether value contains a bidirectional control.
func ContainsBidiControl(value string) bool {
	for _, character := range value {
		if isBidiControl(character) {
			return true
		}
	}
	return false
}
