package highlight

import "strings"

// CSS. A statement inside a block is a rule when a { ends it and a declaration otherwise, so
// nested rules and declarations are told apart by reading ahead once to the statement's end.
// In a selector, element names and & are keywords, classes and IDs types, and pseudo-classes
// functions; in a declaration or an at-rule's prelude, a name before : is a type, numbers with
// their units and hex colours are numbers, and a name before ( is a function.

var cssPreludeWords = words(`and or not only`)

func scanCSS(o *out, s string, i int) {
	depth := 0 // blocks open; the top level holds only rules and at-rules
	for i < len(s) && !o.stopped() {
		c := s[i]
		switch {
		case isBlank(c) || c == ';':
			i++
		case c == '/' && nextByte(s, i) == '*':
			e := closeEnd(s, i+2, "*/")
			o.tok(i, e, ClassComment)
			i = e
		case c == '{':
			depth++
			i++
		case c == '}':
			depth = max(depth-1, 0)
			i++
		default:
			end := cssStatementEnd(s, i)
			switch {
			case c == '@':
				e := cssIdentEnd(s, i+1)
				o.tok(i, e, ClassKeyword)
				cssValues(o, s[:end], e, true)
			case depth == 0 || end < len(s) && s[end] == '{':
				cssSelector(o, s[:end], i)
			default:
				cssValues(o, s[:end], i, false)
			}
			i = end
		}
	}
}

func nextByte(s string, i int) byte {
	if i+1 < len(s) {
		return s[i+1]
	}
	return 0
}

// cssStatementEnd returns the index of the {, ; or } that ends the statement at i, outside
// strings, comments and unquoted url() arguments, which hold ; in data URIs, or len(s).
func cssStatementEnd(s string, i int) int {
	for i < len(s) {
		if isCSSIdentStart(s, i) {
			e := cssIdentEnd(s, i)
			if a, ok := cssURLArg(s, i, e); ok {
				e = closeEnd(s, a, ")")
			}
			i = e
			continue
		}
		switch c := s[i]; c {
		case '{', ';', '}':
			return i
		case '"', '\'':
			i = quoteEnd(s, i, c, false)
		case '\\':
			i = escapeEnd(s, i)
		case '/':
			if nextByte(s, i) == '*' {
				i = closeEnd(s, i+2, "*/")
			} else {
				i++
			}
		default:
			i++
		}
	}
	return len(s)
}

// cssURLArg returns where the unquoted argument of the url( whose name is s[i:e] starts, or
// false when s[i:e] is not url or its argument is quoted.
func cssURLArg(s string, i, e int) (int, bool) {
	if e >= len(s) || s[e] != '(' || !strings.EqualFold(s[i:e], "url") {
		return 0, false
	}
	a := skipBlanks(s, e+1)
	return a, a < len(s) && s[a] != '"' && s[a] != '\''
}

func isCSSIdentStart(s string, i int) bool {
	return isIdentStart(s[i]) || s[i] == '-' && i+1 < len(s) && (isIdentStart(s[i+1]) || s[i+1] == '-')
}

func cssIdentEnd(s string, i int) int {
	for i < len(s) {
		switch c := s[i]; {
		case isIdentByte(c) || c == '-':
			i++
		case c == '\\':
			i = escapeEnd(s, i)
		default:
			return i
		}
	}
	return i
}

// cssCommon classes a comment, string or number at i and returns its end, or i if none starts
// there.
func cssCommon(o *out, s string, i int) int {
	c := s[i]
	switch {
	case c == '/' && nextByte(s, i) == '*':
		e := closeEnd(s, i+2, "*/")
		o.tok(i, e, ClassComment)
		return e
	case c == '"' || c == '\'':
		e := quoteEnd(s, i, c, false)
		o.tok(i, e, ClassString)
		return e
	case isDigit(c) || c == '.' && isDigit(nextByte(s, i)):
		e := cssNumberEnd(s, i)
		o.tok(i, e, ClassNumber)
		return e
	}
	return i
}

// cssNumberEnd returns the end of the number at i with its unit or %.
func cssNumberEnd(s string, i int) int {
	e := digitsEnd(s, i)
	if e+1 < len(s) && s[e] == '.' && isDigit(s[e+1]) {
		e = digitsEnd(s, e+1)
	}
	if e < len(s) && s[e] == '%' {
		return e + 1
	}
	return cssIdentEnd(s, e)
}

// cssSelector classes the selector from i to the end of s.
func cssSelector(o *out, s string, i int) {
	for i < len(s) {
		if e := cssCommon(o, s, i); e > i {
			i = e
			continue
		}
		c := s[i]
		switch {
		case (c == '.' || c == '#') && i+1 < len(s) && isCSSIdentStart(s, i+1):
			e := cssIdentEnd(s, i+1)
			o.tok(i, e, ClassType)
			i = e
		case c == ':':
			e := i + 1
			if e < len(s) && s[e] == ':' {
				e++
			}
			if e < len(s) && isCSSIdentStart(s, e) {
				e = cssIdentEnd(s, e)
				o.tok(i, e, ClassFunction)
			}
			i = e
		case c == '[':
			i = cssAttribute(o, s, i+1)
		case c == '*' || c == '&':
			o.tok(i, i+1, ClassKeyword)
			i++
		case c == '>' || c == '+' || c == '~' || c == ',':
			o.tok(i, i+1, ClassOperator)
			i++
		case isCSSIdentStart(s, i):
			e := cssIdentEnd(s, i)
			o.tok(i, e, ClassKeyword)
			i = e
		case c == '\\':
			i = escapeEnd(s, i)
		default:
			i++
		}
	}
}

// cssAttribute classes an attribute selector's strings from i and returns the index after its ].
func cssAttribute(o *out, s string, i int) int {
	for i < len(s) {
		switch c := s[i]; c {
		case ']':
			return i + 1
		case '"', '\'':
			e := quoteEnd(s, i, c, false)
			o.tok(i, e, ClassString)
			i = e
		default:
			i++
		}
	}
	return i
}

// cssValues classes a declaration or, with prelude, an at-rule's prelude from i to the end of s.
func cssValues(o *out, s string, i int, prelude bool) {
	for i < len(s) {
		if e := cssCommon(o, s, i); e > i {
			i = e
			continue
		}
		c := s[i]
		switch {
		case (c == '-' || c == '+') && i+1 < len(s) && (isDigit(s[i+1]) || s[i+1] == '.' && isDigit(nextByte(s, i+1))):
			e := cssNumberEnd(s, i+1)
			o.tok(i, e, ClassNumber)
			i = e
		case c == '#' && i+1 < len(s) && isIdentByte(s[i+1]):
			e := cssIdentEnd(s, i+1)
			o.tok(i, e, ClassNumber)
			i = e
		case c == '!' && i+1 < len(s) && isIdentStart(s[i+1]):
			e := identEnd(s, i+1)
			o.tok(i, e, ClassKeyword)
			i = e
		case isCSSIdentStart(s, i):
			i = cssValueWord(o, s, i, prelude)
		case c == '\\':
			i = escapeEnd(s, i)
		default:
			i++
		}
	}
}

// cssValueWord classes the name at i: a property before :, a function before (, whose url()
// argument is a string, or a media query's and, or, not and only.
func cssValueWord(o *out, s string, i int, prelude bool) int {
	e := cssIdentEnd(s, i)
	switch {
	case e < len(s) && s[e] == '(':
		o.tok(i, e, ClassFunction)
		if a, ok := cssURLArg(s, i, e); ok {
			end := closeEnd(s, a, ")")
			if end > a && s[end-1] == ')' {
				end--
			}
			o.tok(a, end, ClassString)
			return end
		}
	case nextIs(s, e, ':'):
		o.tok(i, e, ClassType)
	case prelude && cssPreludeWords[s[i:e]]:
		o.tok(i, e, ClassKeyword)
	}
	return e
}
