package highlight

import (
	"strings"
	"unicode/utf8"
)

// Lexical pieces several languages share. Each function starts at a known byte and returns the
// end of what it recognised; none looks further than the construct it reads.

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isIdentStart reports whether c starts a word. Bytes of 0x80 and above count, so a word keeps
// whole UTF-8 sequences.
func isIdentStart(c byte) bool { return c == '_' || c|0x20 >= 'a' && c|0x20 <= 'z' || c >= 0x80 }

func isIdentByte(c byte) bool { return isIdentStart(c) || isDigit(c) }

func identEnd(s string, i int) int {
	for i < len(s) && isIdentByte(s[i]) {
		i++
	}
	return i
}

// isBlank reports whether c is white space, line breaks included.
func isBlank(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// skipBlanks skips spaces and tabs.
func skipBlanks(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// lineEnd returns the index of the LF ending the line holding i, or len(s).
func lineEnd(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(s)
}

// isASCIIIdentByte reports whether c continues an ASCII-only name, as in a shell variable.
func isASCIIIdentByte(c byte) bool { return isIdentByte(c) && c < 0x80 }

// escapeEnd returns the end of the escape whose backslash is at i: the next character, or a CRLF
// line break as a whole, so an escaped line break is a continuation in both line endings.
func escapeEnd(s string, i int) int {
	if i+2 < len(s) && s[i+1] == '\r' && s[i+2] == '\n' {
		return i + 3
	}
	_, n := utf8.DecodeRuneInString(s[i+1:])
	return i + 1 + n
}

// nextIs reports whether the first byte after blanks at i is c: ( after a called name, : after a
// key.
func nextIs(s string, i int, c byte) bool {
	i = skipBlanks(s, i)
	return i < len(s) && s[i] == c
}

// commentEnd returns the end of the // or /* */ comment at i, or false when none starts there.
func commentEnd(s string, i int) (int, bool) {
	if s[i] != '/' || i+1 >= len(s) {
		return 0, false
	}
	switch s[i+1] {
	case '/':
		return lineEnd(s, i), true
	case '*':
		return closeEnd(s, i+2, "*/"), true
	}
	return 0, false
}

func digitsEnd(s string, i int) int {
	for i < len(s) && (isDigit(s[i]) || s[i] == '_') {
		i++
	}
	return i
}

// numberEnd reads a C-family number starting with the digit at i: a radix prefix, digit
// separators, a fraction, an exponent and a suffix such as 10n, 3i or 1u32.
func numberEnd(s string, i int) int {
	if s[i] == '0' && i+1 < len(s) {
		switch s[i+1] | 0x20 {
		case 'x', 'b', 'o':
			return identEnd(s, i+2)
		}
	}
	j := digitsEnd(s, i)
	if j+1 < len(s) && s[j] == '.' && isDigit(s[j+1]) {
		j = digitsEnd(s, j+1)
	}
	if j < len(s) && s[j]|0x20 == 'e' {
		k := j + 1
		if k < len(s) && (s[k] == '+' || s[k] == '-') {
			k++
		}
		if k < len(s) && isDigit(s[k]) {
			j = digitsEnd(s, k)
		}
	}
	return identEnd(s, j)
}

// quoteEnd reads a string opened by the quote q at i, with backslash escapes, and returns the
// index after its closing quote. An unclosed string ends at the line's end unless multiline,
// else at the end of s.
func quoteEnd(s string, i int, q byte, multiline bool) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case q:
			return j + 1
		case '\\':
			j++
		case '\n':
			if !multiline {
				return j
			}
		}
	}
	return len(s)
}

// tripleQuoteEnd returns the index after the closing triple quote of the string whose opening
// triple quote is at i, or len(s). With escapes, a backslash escapes the next byte.
func tripleQuoteEnd(s string, i int, escapes bool) int {
	q := s[i]
	for j := i + 3; j+2 < len(s); j++ {
		switch s[j] {
		case '\\':
			if escapes {
				j++
			}
		case q:
			if s[j+1] == q && s[j+2] == q {
				return j + 3
			}
		}
	}
	return len(s)
}

// isTripleQuote reports whether a triple quote, such as """, starts at i.
func isTripleQuote(s string, i int) bool {
	return i+2 < len(s) && s[i+1] == s[i] && s[i+2] == s[i]
}

// closeEnd returns the index after the first closer at or after i, or len(s).
func closeEnd(s string, i int, closer string) int {
	if j := strings.Index(s[i:], closer); j >= 0 {
		return i + j + len(closer)
	}
	return len(s)
}

type wordSet map[string]bool

// lookupFold returns m's value for w upper-cased in ASCII, for languages whose words ignore case.
// A word longer than any key is not looked up, so w is copied to a stack buffer, not the heap.
func lookupFold[V any](m map[string]V, w string) V {
	var b [24]byte
	if len(w) > len(b) {
		var zero V
		return zero
	}
	for j := range len(w) {
		c := w[j]
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		b[j] = c
	}
	return m[string(b[:len(w)])] // the conversion in a map index does not allocate
}

// words builds a set from space-separated words.
func words(list string) wordSet {
	f := strings.Fields(list)
	m := make(wordSet, len(f))
	for _, w := range f {
		m[w] = true
	}
	return m
}

type byteSet [256]bool

func bytesOf(chars string) (b byteSet) {
	for i := range len(chars) {
		b[chars[i]] = true
	}
	return b
}

// cOperators are the operator bytes of C-family languages. The colon is left out: it is a
// separator in labels, cases, types and object literals more often than an operator.
var cOperators = bytesOf("+-*/%&|^<>=!~?")
