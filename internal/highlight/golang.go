package highlight

var (
	goKeywords = words(`break case chan const continue default defer else fallthrough for func go goto
		if import interface map package range return select struct switch type var
		true false iota nil`)
	goTypes = words(`any bool byte comparable complex64 complex128 error float32 float64 int int8 int16
		int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr`)
)

func scanGo(o *out, s string, i int) {
	prev := "" // the keyword before the current word, for declared names
	for i < len(s) && !o.stopped() {
		if e, ok := commentEnd(s, i); ok {
			o.tok(i, e, ClassComment)
			i = e
			continue
		}
		c := s[i]
		switch {
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
		case isIdentStart(c):
			e := identEnd(s, i)
			w := s[i:e]
			o.tok(i, e, goWordClass(w, prev, s, e))
			if goKeywords[w] {
				prev = w
			} else {
				prev = ""
			}
			i = e
			continue
		case c == ':' && i+1 < len(s) && s[i+1] == '=':
			o.tok(i, i+2, ClassOperator)
			i += 2
		case cOperators[c]:
			o.tok(i, i+1, ClassOperator)
			i++
		default:
			i++
			if !isBlank(c) {
				prev = ""
			}
			continue
		}
		prev = ""
	}
}

// goWordClass classes the word w ending at e. A name after func, or before a parenthesis, is a
// function; a name after type is a type.
func goWordClass(w, prev, s string, e int) string {
	switch {
	case goKeywords[w]:
		return ClassKeyword
	case goTypes[w] || prev == "type":
		return ClassType
	case prev == "func" || nextIs(s, e, '('):
		return ClassFunction
	default:
		return ""
	}
}
