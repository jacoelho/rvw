package highlight

import "strings"

// YAML. A scalar followed by ": " is a key (a type); other scalars are strings, except the
// core schema's booleans, nulls and numbers. A block scalar's lines are strings. Multi-line
// plain scalars are read line by line, which classes their continuation lines as strings too.

var yamlValues = words(`true True TRUE false False FALSE null Null NULL ~`)

type yamlScanner struct {
	o *out
	s string
	// flow counts the open [ and { of flow collections.
	flow int
	// indent is the current line's indentation.
	indent int
	// block is the indentation of the line that opened a block scalar, or -1: lines indented
	// further, and blank lines, are its content.
	block int
}

func scanYAML(o *out, s string, i int) {
	x := yamlScanner{o: o, s: s, block: -1}
	lineStart := true
	for i < len(s) && !o.stopped() {
		if lineStart {
			lineStart = false
			i = x.lineHead(i)
			continue
		}
		if s[i] == '\n' {
			lineStart = true
			i++
			continue
		}
		i = x.token(i)
	}
}

// lineHead reads the indentation at a line's start i, and the whole line when it is block scalar
// content, a document marker or a directive, and returns where the line's tokens start.
func (x *yamlScanner) lineHead(i int) int {
	s, o := x.s, x.o
	le := lineEnd(s, i)
	k := i
	for k < le && s[k] == ' ' {
		k++
	}
	if x.block >= 0 {
		if strings.TrimSpace(s[k:le]) == "" || k-i > x.block {
			o.tok(k, le, ClassString)
			return le
		}
		x.block = -1
	}
	x.indent = k - i
	if k == i && x.flow == 0 {
		switch {
		case (strings.HasPrefix(s[i:], "---") || strings.HasPrefix(s[i:], "...")) && (i+3 == len(s) || isBlank(s[i+3])):
			o.tok(i, i+3, ClassKeyword)
			return i + 3
		case s[i] == '%':
			o.tok(i, le, ClassKeyword)
			return le
		}
	}
	return k
}

// indicatorAt reports whether the byte at j ends an indicator such as "- " or ": ": a blank,
// the end of the text, or, in a flow collection, a flow indicator.
func (x *yamlScanner) indicatorAt(j int) bool {
	return j >= len(x.s) || isBlank(x.s[j]) || x.flow > 0 && strings.IndexByte(",[]{}", x.s[j]) >= 0
}

// token classes one token at i, which is not at a line break, and returns its end.
func (x *yamlScanner) token(i int) int {
	s, o := x.s, x.o
	c := s[i]
	switch {
	case isBlank(c):
		return i + 1
	case c == '#' && (i == 0 || isBlank(s[i-1])):
		e := lineEnd(s, i)
		o.tok(i, e, ClassComment)
		return e
	case (c == '-' || c == '?' || c == ':') && x.indicatorAt(i+1):
		o.tok(i, i+1, ClassOperator)
		return i + 1
	case c == '"' || c == '\'':
		e := yamlQuoteEnd(s, i)
		class := ClassString
		// A quoted key's colon may touch the next token in a flow collection, as in JSON.
		if j := skipBlanks(s, e); j < len(s) && s[j] == ':' && (x.flow > 0 || x.indicatorAt(j+1)) {
			class = ClassType
		}
		o.tok(i, e, class)
		return e
	case c == '&' || c == '*' || c == '!':
		e := i + 1
		for e < len(s) && !x.indicatorAt(e) {
			e++
		}
		class := ClassType // an anchor or alias
		if c == '!' {
			class = ClassKeyword // a tag
		}
		o.tok(i, e, class)
		return e
	case (c == '|' || c == '>') && x.flow == 0:
		// A block scalar header and its indentation and chomping indicators.
		e := i + 1
		for e < len(s) && (isDigit(s[e]) || s[e] == '+' || s[e] == '-') {
			e++
		}
		o.tok(i, e, ClassOperator)
		x.block = x.indent
		return e
	case c == '[' || c == '{':
		x.flow++
		return i + 1
	case c == ']' || c == '}':
		x.flow = max(x.flow-1, 0)
		return i + 1
	case c == ',' && x.flow > 0:
		return i + 1
	}
	return x.plain(i)
}

// plain classes the plain scalar at i: a key, a core schema value, a number or a string.
func (x *yamlScanner) plain(i int) int {
	s := x.s
	e := x.plainEnd(i)
	class := ClassString
	switch w := s[i:e]; {
	case nextIs(s, e, ':'):
		class = ClassType
	case yamlValues[w]:
		class = ClassKeyword
	case isYAMLNumber(w):
		class = ClassNumber
	}
	x.o.tok(i, e, class)
	return e
}

// plainEnd returns the end of the plain scalar at i: before ": ", " #", the line's end, or, in
// a flow collection, a flow indicator; trailing blanks excluded.
func (x *yamlScanner) plainEnd(i int) int {
	s := x.s
	e := i
	for e < len(s) && s[e] != '\n' {
		c := s[e]
		if c == ':' && x.indicatorAt(e+1) ||
			c == '#' && e > i && isBlank(s[e-1]) ||
			x.flow > 0 && strings.IndexByte(",[]{}", c) >= 0 {
			break
		}
		e++
	}
	for e > i+1 && isBlank(s[e-1]) {
		e--
	}
	return max(e, i+1)
}

// isYAMLNumber reports whether w is a core schema number: an integer, a float, 0x or 0o, or
// .inf and .nan with an optional sign.
func isYAMLNumber(w string) bool {
	v := strings.TrimLeft(w, "+-")
	if len(w)-len(v) > 1 || v == "" {
		return false
	}
	switch v {
	case ".inf", ".Inf", ".INF", ".nan", ".NaN", ".NAN":
		return true
	}
	if len(v) > 2 && v[0] == '0' && (v[1] == 'x' || v[1] == 'o') {
		for j := 2; j < len(v); j++ {
			if c := v[j] | 0x20; !isDigit(v[j]) && (v[1] == 'o' || c < 'a' || c > 'f') {
				return false
			}
		}
		return true
	}
	if v[0] == '.' {
		v = v[1:]
	}
	// A decimal number ends in a digit, so 10GB and 1x are strings.
	return v != "" && isDigit(v[0]) && isDigit(v[len(v)-1]) && numberEnd(v, 0) == len(v)
}

// yamlQuoteEnd returns the end of the quoted scalar at i: "..." with backslash escapes, or
// '...' where ” is a quote. Quoted scalars may span lines; an unclosed one ends the text.
func yamlQuoteEnd(s string, i int) int {
	if s[i] == '"' {
		return quoteEnd(s, i, '"', true)
	}
	for j := i + 1; j < len(s); j++ {
		if s[j] == '\'' {
			if j+1 < len(s) && s[j+1] == '\'' {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}
