package highlight

// Python 3. A string's prefix (r, b, f, u, t and their pairs) belongs to the string; an f-string is
// classed whole, its replacement fields included. A decorator is a function; the soft keywords
// match, case and type are keywords when they start a line and a name or literal follows.

var (
	pyKeywords = words(`False None True and as assert async await break class continue def del elif
		else except finally for from global if import in is lambda nonlocal not or pass raise
		return try while with yield`)
	pySoftKeywords = words(`match case type`)
	pyTypes        = words(`bool bytearray bytes complex dict float frozenset int list memoryview object
		range set slice str tuple type BaseException Exception ArithmeticError AssertionError
		AttributeError ImportError IndexError KeyError LookupError NameError NotImplementedError
		OSError RuntimeError StopIteration TypeError ValueError ZeroDivisionError`)
)

func scanPython(o *out, s string, i int) {
	prev := ""        // the keyword before the current word, for declared names
	lineStart := true // only blanks precede on this line
	for i < len(s) && !o.stopped() {
		c := s[i]
		if c == '\n' {
			lineStart = true
			i++
			continue
		}
		if isBlank(c) {
			i++
			continue
		}
		start := lineStart
		lineStart = false
		p := prev
		prev = ""
		switch {
		case c == '#':
			e := lineEnd(s, i)
			o.tok(i, e, ClassComment)
			i = e
		case c == '"' || c == '\'':
			e := pyStringEnd(s, i)
			o.tok(i, e, ClassString)
			i = e
		case isDigit(c) || c == '.' && i+1 < len(s) && isDigit(s[i+1]):
			e := numberEnd(s, i)
			o.tok(i, e, ClassNumber)
			i = e
		case c == '@' && start:
			e := i + 1
			for e < len(s) && (isIdentByte(s[e]) || s[e] == '.') {
				e++
			}
			o.tok(i, e, ClassFunction)
			i = e
		case isIdentStart(c):
			i, prev = pyWord(o, s, i, p, start)
		case cOperators[c]:
			o.tok(i, i+1, ClassOperator)
			i++
		default:
			i++
		}
	}
}

// pyWord classes the word at i, or the string it prefixes, and returns its end and the word if
// it is a keyword. start reports that the word starts its line.
func pyWord(o *out, s string, i int, prev string, start bool) (int, string) {
	e := identEnd(s, i)
	w := s[i:e]
	if e < len(s) && (s[e] == '"' || s[e] == '\'') && isStringPrefix(w) {
		end := pyStringEnd(s, e)
		o.tok(i, end, ClassString)
		return end, ""
	}
	class := ""
	switch {
	case pyKeywords[w]:
		o.tok(i, e, ClassKeyword)
		return e, w
	case start && pySoftKeywords[w] && softKeywordUse(s, e):
		o.tok(i, e, ClassKeyword)
		return e, w
	case prev == "def":
		class = ClassFunction
	case prev == "class" || prev == "type":
		class = ClassType
	case nextIs(s, e, '('):
		class = ClassFunction
	case pyTypes[w]:
		class = ClassType
	}
	o.tok(i, e, class)
	return e, ""
}

// isStringPrefix reports whether w is a string prefix such as r, rb or f, in any case.
func isStringPrefix(w string) bool {
	if len(w) > 2 {
		return false
	}
	for j := range len(w) {
		switch w[j] | 0x20 {
		case 'r', 'b', 'f', 'u', 't':
		default:
			return false
		}
	}
	return true
}

// softKeywordUse reports whether the soft keyword ending at e starts a statement: a name, literal
// or bracket follows, not an operator such as = or a call's (.
func softKeywordUse(s string, e int) bool {
	j := skipBlanks(s, e)
	if j == e || j >= len(s) {
		return false
	}
	c := s[j]
	return isIdentStart(c) || isDigit(c) || c == '"' || c == '\'' || c == '[' || c == '-' || c == '*'
}

// pyStringEnd returns the end of the string whose quote is at i: a triple-quoted string spans
// lines, a single-quoted one ends at its line's end when left open. A backslash escapes the next
// byte in every string, raw ones included, as Python's lexer reads them.
func pyStringEnd(s string, i int) int {
	if isTripleQuote(s, i) {
		return tripleQuoteEnd(s, i, true)
	}
	return quoteEnd(s, i, s[i], false)
}
