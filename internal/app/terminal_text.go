package app

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

const sourceTabWidth = 4

type terminalTextRenderer struct {
	column int
}

func safeText(value string) string {
	return new(terminalTextRenderer).render(value)
}

func (renderer *terminalTextRenderer) render(value string) string {
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

func containsBidiControl(value string) bool {
	for _, character := range value {
		if isBidiControl(character) {
			return true
		}
	}
	return false
}
