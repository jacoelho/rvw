package highlight

// JSON, with the comments JSONC allows. A string followed by a colon is a key.

var jsonValues = words(`true false null`)

func scanJSON(o *out, s string, i int) {
	for i < len(s) && !o.stopped() {
		if e, ok := commentEnd(s, i); ok {
			o.tok(i, e, ClassComment)
			i = e
			continue
		}
		c := s[i]
		switch {
		case c == '"':
			e := quoteEnd(s, i, '"', false)
			class := ClassString
			if nextIs(s, e, ':') {
				class = ClassType
			}
			o.tok(i, e, class)
			i = e
		case isDigit(c) || c == '-' && i+1 < len(s) && isDigit(s[i+1]):
			d := i
			if c == '-' {
				d++
			}
			e := numberEnd(s, d)
			o.tok(i, e, ClassNumber)
			i = e
		case isIdentStart(c):
			e := identEnd(s, i)
			if jsonValues[s[i:e]] {
				o.tok(i, e, ClassKeyword)
			}
			i = e
		default:
			i++
		}
	}
}
