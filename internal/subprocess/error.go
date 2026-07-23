package subprocess

import (
	"bytes"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

func finishResult(
	request Request,
	result Result,
	_ error,
	runErr error,
	waited *waitState,
	stdout boundedCapture,
	stderr *capture,
) (Result, error) {
	if completed := waited.completed(); completed {
		result.Stdout = stdout.TakeBytes()
		result.Stderr = stderr.TakeBytes()
		if streamErr := stdout.StreamError(); streamErr != nil {
			return result, streamErr
		}
		if streamErr := stderr.StreamError(); streamErr != nil {
			return result, streamErr
		}
	}
	if runErr != nil {
		return result, runErr
	}
	if !slices.Contains(request.AcceptedExits, result.ExitCode) {
		stderr, truncated := terminalSafeErrorDetail(result.Stderr)
		result.Stderr = nil
		return result, &ExitError{
			Name:      operationName(request),
			ExitCode:  result.ExitCode,
			Stderr:    stderr,
			Truncated: truncated,
		}
	}
	return result, nil
}

func terminalSafeErrorDetail(value []byte) (string, bool) {
	value = bytes.TrimSpace(value)
	var detail strings.Builder
	detail.Grow(min(len(value), int(MaxErrorDetailBytes)))
	truncated := false
	for len(value) != 0 {
		character, size := utf8.DecodeRune(value)
		piece := value[:size]
		var escaped [12]byte
		escapedBytes := escaped[:0]
		switch {
		case character == utf8.RuneError && size == 1:
			escapedBytes = appendHexByteEscape(escapedBytes, value[0])
		case unicode.IsControl(character) || isBidiControl(character):
			escapedBytes = appendUnicodeEscape(escapedBytes, character)
		default:
			escapedBytes = append(escapedBytes, piece...)
		}
		if int64(detail.Len()+len(escapedBytes)) > MaxErrorDetailBytes {
			truncated = true
			break
		}
		_, _ = detail.Write(escapedBytes)
		value = value[size:]
	}
	return detail.String(), truncated
}

func appendHexByteEscape(target []byte, value byte) []byte {
	const hexadecimal = "0123456789ABCDEF"
	return append(target, '\\', 'x', hexadecimal[value>>4], hexadecimal[value&0x0f])
}

func appendUnicodeEscape(target []byte, value rune) []byte {
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
	target = append(target, '\\', 'u', '{')
	target = append(target, digits[position:]...)
	return append(target, '}')
}

func isBidiControl(character rune) bool {
	return character == '\u061c' || character == '\u200e' || character == '\u200f' ||
		character >= '\u202a' && character <= '\u202e' ||
		character >= '\u2066' && character <= '\u2069'
}
