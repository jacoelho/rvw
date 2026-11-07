package highlight

import "strings"

// TOML. Keys before = are types, table headers keywords, and dates and times numbers.

var tomlValues = words(`true false`)

type tomlScanner struct {
	o *out
	s string
	// stack holds the open [ and { of values, up to maxFrames; deeper ones are not tracked.
	stack []byte
	// key reports that a key may start here: at a line's start outside values, or after { or
	// a comma in an inline table.
	key bool
	// lineStart reports that only blanks precede on this line.
	lineStart bool
}

func scanTOML(o *out, s string, i int) {
	x := tomlScanner{o: o, s: s, key: true, lineStart: true}
	for i < len(s) && !o.stopped() {
		i = x.next(i)
	}
}

func (x *tomlScanner) top() byte {
	if len(x.stack) == 0 {
		return 0
	}
	return x.stack[len(x.stack)-1]
}

// next classes one token at i and returns its end.
func (x *tomlScanner) next(i int) int {
	s, o := x.s, x.o
	c := s[i]
	if c == '\n' {
		x.lineStart = true
		x.key = len(x.stack) == 0
		return i + 1
	}
	if isBlank(c) {
		return i + 1
	}
	start := x.lineStart
	x.lineStart = false
	switch {
	case c == '#':
		e := lineEnd(s, i)
		o.tok(i, e, ClassComment)
		return e
	case c == '[' && start && len(x.stack) == 0:
		return x.header(i)
	case c == '"' || c == '\'':
		e := tomlStringEnd(s, i)
		class := ClassString
		if x.key {
			class = ClassType
		}
		o.tok(i, e, class)
		return e
	case c == '=':
		o.tok(i, i+1, ClassOperator)
		x.key = false
	case c == '[' || c == '{':
		if len(x.stack) < maxFrames {
			x.stack = append(x.stack, c)
		}
		x.key = c == '{'
	case c == ']' || c == '}':
		if len(x.stack) > 0 {
			x.stack = x.stack[:len(x.stack)-1]
		}
	case c == ',':
		x.key = x.top() == '{'
	case x.key && (isASCIIIdentByte(c) || c == '-'):
		e := i
		for e < len(s) && (isASCIIIdentByte(s[e]) || s[e] == '-') {
			e++
		}
		o.tok(i, e, ClassType)
		return e
	case isDigit(c) || c == '+' || c == '-' || isIdentStart(c):
		return x.value(i)
	}
	return i + 1
}

// header classes the table header at i, [name] or [[name]], up to its closer on this line.
func (x *tomlScanner) header(i int) int {
	s := x.s
	le := lineEnd(s, i)
	closer := "]"
	if strings.HasPrefix(s[i:], "[[") {
		closer = "]]"
	}
	e := le
	if j := strings.Index(s[i:le], closer); j >= 0 {
		e = i + j + len(closer)
	}
	x.o.tok(i, e, ClassKeyword)
	x.key = false
	return e
}

// value classes the number, date, time or word value at i.
func (x *tomlScanner) value(i int) int {
	s, o, c := x.s, x.o, x.s[i]
	if isDigit(c) || (c == '+' || c == '-') && i+1 < len(s) && (isDigit(s[i+1]) || s[i+1] == 'i' || s[i+1] == 'n') {
		// Numbers, inf and nan with a sign, and dates and times such as 1979-05-27T07:32:00Z.
		e := i + 1
		for e < len(s) && (isASCIIIdentByte(s[e]) || strings.IndexByte(".:+-", s[e]) >= 0) {
			e++
		}
		o.tok(i, e, ClassNumber)
		return e
	}
	if !isIdentStart(c) {
		return i + 1
	}
	e := identEnd(s, i)
	switch w := s[i:e]; {
	case tomlValues[w]:
		o.tok(i, e, ClassKeyword)
	case w == "inf" || w == "nan":
		o.tok(i, e, ClassNumber)
	}
	return e
}

// tomlStringEnd returns the end of the string whose quote is at i: basic "..." with escapes,
// literal '...', or either tripled to span lines. A one-line string left open ends at the line's
// end.
func tomlStringEnd(s string, i int) int {
	q := s[i]
	if isTripleQuote(s, i) {
		e := tripleQuoteEnd(s, i, q == '"')
		// Up to two more quotes belong to the string: """a""""" is a"".
		for n := 0; n < 2 && e < len(s) && s[e] == q; n++ {
			e++
		}
		return e
	}
	if q == '"' {
		return quoteEnd(s, i, '"', false)
	}
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\'':
			return j + 1
		case '\n':
			return j
		}
	}
	return len(s)
}
