package highlight

import "strings"

// Go text/template and html/template source: the text outside actions is plain.

var tmplKeywords = words(`if else end range with define template block break continue nil true false`)

func scanGoTemplate(o *out, s string, i int) {
	for i < len(s) && !o.stopped() {
		j := strings.Index(s[i:], "{{")
		if j < 0 {
			return
		}
		a := i + j
		k := a + 2
		if k+1 < len(s) && s[k] == '-' && isBlank(s[k+1]) {
			k++ // the left trim marker
		}
		if b := skipSpace(s, k); strings.HasPrefix(s[b:], "/*") {
			e := closeEnd(s, closeEnd(s, b+2, "*/"), "}}")
			o.tok(a, e, ClassComment)
			i = e
			continue
		}
		o.tok(a, k, ClassKeyword)
		i = templateAction(o, s, k)
	}
}

// skipSpace skips white space, line breaks included.
func skipSpace(s string, i int) int {
	for i < len(s) && isBlank(s[i]) {
		i++
	}
	return i
}

// templateAction classes an action's pipeline from i and returns the index after its closing
// delimiter, or len(s) if it has none.
func templateAction(o *out, s string, i int) int {
	for i < len(s) {
		c := s[i]
		switch {
		case c == '}' && strings.HasPrefix(s[i:], "}}"):
			o.tok(i, i+2, ClassKeyword)
			return i + 2
		case c == '-' && strings.HasPrefix(s[i:], "-}}") && i > 0 && isBlank(s[i-1]):
			o.tok(i, i+3, ClassKeyword)
			return i + 3
		case c == '"' || c == '\'':
			e := quoteEnd(s, i, c, false)
			o.tok(i, e, ClassString)
			i = e
		case c == '`':
			e := closeEnd(s, i+1, "`")
			o.tok(i, e, ClassString)
			i = e
		case isDigit(c):
			e := numberEnd(s, i)
			o.tok(i, e, ClassNumber)
			i = e
		case c == '$':
			e := identEnd(s, i+1)
			o.tok(i, e, ClassType)
			i = e
		case c == '.':
			i = identEnd(s, i+1) // a field or method name
		case isIdentStart(c):
			e := identEnd(s, i)
			class := ClassFunction // a bare word in a pipeline names a function
			if tmplKeywords[s[i:e]] {
				class = ClassKeyword
			}
			o.tok(i, e, class)
			i = e
		case c == '|' || c == '=' || c == ':' && i+1 < len(s) && s[i+1] == '=':
			o.tok(i, i+1, ClassOperator)
			i++
		default:
			i++
		}
	}
	return len(s)
}
