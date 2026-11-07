package highlight

import "unicode/utf8"

var (
	rustKeywords = words(`as async await break const continue crate dyn else enum extern false fn for
		if impl in let loop match mod move mut pub ref return self static struct super trait true
		type union unsafe use where while yield macro_rules`)
	rustTypes    = words(`bool char str i8 i16 i32 i64 i128 isize u8 u16 u32 u64 u128 usize f32 f64 Self`)
	rustTypeDecl = words(`struct enum trait type union`)
)

func scanRust(o *out, s string, i int) {
	prev := ""
	for i < len(s) && !o.stopped() {
		c := s[i]
		if isBlank(c) {
			i++
			continue
		}
		p := prev
		prev = ""
		switch {
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			e := lineEnd(s, i)
			o.tok(i, e, ClassComment)
			i = e
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			e := rustBlockEnd(s, i)
			o.tok(i, e, ClassComment)
			i = e
		case c == '"':
			e := quoteEnd(s, i, '"', true)
			o.tok(i, e, ClassString)
			i = e
		case c == '\'':
			i = rustQuote(o, s, i, i)
		case isDigit(c):
			e := numberEnd(s, i)
			o.tok(i, e, ClassNumber)
			i = e
		case c == '#' && i+1 < len(s) && (s[i+1] == '[' || s[i+1] == '!'):
			o.tok(i, i+1, ClassKeyword)
			i++
		case isIdentStart(c):
			i, prev = rustWord(o, s, i, p)
		case c == ':' && i+1 < len(s) && s[i+1] == ':':
			i += 2
		case cOperators[c]:
			o.tok(i, i+1, ClassOperator)
			i++
		default:
			i++
		}
	}
}

// rustWord classes the word at i, or the string or character literal it prefixes, and returns
// its end and the word if it is a keyword.
func rustWord(o *out, s string, i int, prev string) (int, string) {
	e := identEnd(s, i)
	w := s[i:e]
	next := byte(0)
	if e < len(s) {
		next = s[e]
	}
	switch {
	case (w == "b" || w == "c") && next == '"':
		end := quoteEnd(s, e, '"', true)
		o.tok(i, end, ClassString)
		return end, ""
	case w == "b" && next == '\'':
		return rustQuote(o, s, i, e), ""
	case (w == "r" || w == "br" || w == "cr") && (next == '"' || next == '#'):
		h := e
		for h < len(s) && s[h] == '#' {
			h++
		}
		if h < len(s) && s[h] == '"' {
			end := closeEnd(s, h+1, `"`+s[e:h])
			o.tok(i, end, ClassString)
			return end, ""
		}
		if w == "r" && h == e+1 {
			return h, "" // a raw identifier's r#
		}
	}
	class := ""
	switch {
	case rustKeywords[w]:
		o.tok(i, e, ClassKeyword)
		return e, w
	case rustTypes[w]:
		class = ClassType
	case next == '!' && (e+1 >= len(s) || s[e+1] != '='):
		o.tok(i, e+1, ClassFunction) // a macro
		return e + 1, ""
	case prev == "fn" || nextIs(s, e, '('):
		class = ClassFunction
	case rustTypeDecl[prev] || w[0] >= 'A' && w[0] <= 'Z':
		class = ClassType
	}
	o.tok(i, e, class)
	return e, ""
}

// rustQuote classes the character literal or lifetime whose quote is at q; the token starts
// at a, before q for a byte literal.
func rustQuote(o *out, s string, a, q int) int {
	if q+1 >= len(s) {
		return q + 1
	}
	if s[q+1] == '\\' {
		e := quoteEnd(s, q, '\'', false)
		o.tok(a, e, ClassString)
		return e
	}
	_, size := utf8.DecodeRuneInString(s[q+1:])
	if c := q + 1 + size; c < len(s) && s[c] == '\'' && s[q+1] != '\n' {
		o.tok(a, c+1, ClassString)
		return c + 1
	}
	if e := identEnd(s, q+1); e > q+1 {
		o.tok(q, e, ClassType) // a lifetime
		return e
	}
	return q + 1
}

// rustBlockEnd returns the end of the nested block comment at i.
func rustBlockEnd(s string, i int) int {
	depth := 0
	for j := i; j+1 < len(s); j++ {
		switch {
		case s[j] == '/' && s[j+1] == '*':
			depth++
			j++
		case s[j] == '*' && s[j+1] == '/':
			depth--
			j++
			if depth == 0 {
				return j + 1
			}
		}
	}
	return len(s)
}
