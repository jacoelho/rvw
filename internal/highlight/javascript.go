package highlight

import "strings"

// JavaScript and TypeScript, each optionally with JSX. One scanner serves all four: TypeScript
// adds keywords and types, and JSX adds tags where an expression may start.

type jsOptions struct{ ts, jsx bool }

var (
	jsKeywords = words(`break case catch class const continue debugger default delete do else export
		extends finally for function if import in instanceof let new return super switch this throw
		try typeof var void while with yield async await of static get set from as
		true false null undefined`)
	tsKeywords = words(`type interface enum namespace module declare abstract implements private
		protected public readonly keyof infer is satisfies unique override asserts accessor`)
	// jsContextual are keywords only in some positions; before a parenthesis they are calls.
	jsContextual = words(`async of static get set from as type namespace module declare abstract
		readonly is keyof infer satisfies asserts override accessor`)
	// jsValues are keywords that end an expression, so a slash after them divides.
	jsValues = words(`this super true false null undefined`)
	// jsTypeDecl are keywords followed by a type name.
	jsTypeDecl = words(`class interface type enum namespace extends implements new`)
	tsTypes    = words(`string number boolean any unknown never object bigint symbol`)
)

type jsFrameKind uint8

const (
	jsBrace    jsFrameKind = iota // a brace in code
	jsInterp                      // ${ in a template literal
	jsTagExpr                     // a brace in a JSX tag or among JSX children
	jsTemplate                    // inside a template literal
	jsTag                         // inside a JSX tag, < to >
	jsChildren                    // between a JSX opening tag and its closing tag
)

type jsFrame struct {
	kind    jsFrameKind
	closing bool // a jsTag is a closing tag
	named   bool // a jsTag has read its name
}

// maxFrames bounds the nesting of braces, template literals and JSX the scanner tracks. Deeper
// nesting is read as plain code.
const maxFrames = 1024

type jsScanner struct {
	o     *out
	s     string
	prev  string // the keyword before the current token
	stack []jsFrame
	// noRegex is the end of a line on which a regular expression failed to close. A later
	// slash on that line is an operator, so no line is searched for a closer twice.
	noRegex int
	opt     jsOptions
	// expr reports that an expression may start here, so a slash opens a regular expression
	// and a < opens a JSX tag.
	expr bool
	dot  bool // the token before is a member access
}

func scanJS(o *out, s string, i int, opt jsOptions) {
	x := jsScanner{o: o, s: s, opt: opt, expr: true, noRegex: -1}
	for i < len(s) && !o.stopped() {
		switch x.mode() {
		case jsTemplate:
			i = x.template(i)
		case jsTag:
			i = x.tag(i)
		case jsChildren:
			i = x.children(i)
		case jsBrace, jsInterp, jsTagExpr:
			i = x.code(i)
		}
	}
}

func (x *jsScanner) mode() jsFrameKind {
	if len(x.stack) == 0 {
		return jsBrace
	}
	return x.stack[len(x.stack)-1].kind
}

func (x *jsScanner) push(k jsFrameKind) bool {
	if len(x.stack) == maxFrames {
		return false
	}
	x.stack = append(x.stack, jsFrame{kind: k})
	return true
}

func (x *jsScanner) pop() { x.stack = x.stack[:len(x.stack)-1] }

func jsIdentEnd(s string, i int) int {
	for i < len(s) && (isIdentByte(s[i]) || s[i] == '$') {
		i++
	}
	return i
}

// code classes one token of code at i and returns its end.
func (x *jsScanner) code(i int) int {
	s, o := x.s, x.o
	c := s[i]
	if isBlank(c) {
		return i + 1
	}
	prev, dot := x.prev, x.dot
	x.prev, x.dot = "", false
	if e, ok := x.literal(i, prev, dot); ok {
		return e
	}
	switch {
	case isDigit(c):
		e := numberEnd(s, i)
		o.tok(i, e, ClassNumber)
		x.expr = false
		return e
	case c == '#' && i+1 < len(s) && isIdentStart(s[i+1]):
		x.expr = false
		return jsIdentEnd(s, i+1) // a private name
	case isIdentStart(c) || c == '$':
		e := jsIdentEnd(s, i)
		x.word(i, e, prev, dot)
		return e
	case c == '{':
		x.push(jsBrace)
		x.expr = true
	case c == '}':
		x.closeBrace(i)
	case c == '(' || c == '[' || c == ',' || c == ';' || c == ':':
		x.expr = true
	case c == ')' || c == ']':
		x.expr = false
	case c == '.':
		x.dot = true
		x.expr = strings.HasPrefix(s[i:], "...")
		if x.expr {
			return i + 3
		}
	case c == '<' && x.opt.jsx && x.expr && i+1 < len(s) && (isIdentStart(s[i+1]) || s[i+1] == '>'):
		x.push(jsTag)
	case cOperators[c]:
		o.tok(i, i+1, ClassOperator)
		x.expr = true
		x.dot = c == '?' && i+1 < len(s) && s[i+1] == '.'
		if x.dot {
			return i + 2
		}
	}
	return i + 1
}

// literal classes the comment, regular expression, string or template literal starting at i,
// if one does, and returns its end.
func (x *jsScanner) literal(i int, prev string, dot bool) (int, bool) {
	s, o := x.s, x.o
	c := s[i]
	if e, ok := commentEnd(s, i); ok {
		o.tok(i, e, ClassComment)
		x.prev, x.dot = prev, dot // a comment does not separate a keyword from its name
		return e, true
	}
	switch {
	case c == '/' && x.expr && i > x.noRegex:
		if e, ok := regexEnd(s, i); ok {
			o.tok(i, e, ClassString)
			x.expr = false
			return e, true
		}
		x.noRegex = lineEnd(s, i)
		o.tok(i, i+1, ClassOperator)
		return i + 1, true
	case c == '"' || c == '\'':
		e := quoteEnd(s, i, c, false)
		o.tok(i, e, ClassString)
		x.expr = false
		return e, true
	case c == '`':
		if !x.push(jsTemplate) {
			e := closeEnd(s, i+1, "`")
			o.tok(i, e, ClassString)
			return e, true
		}
		o.tok(i, i+1, ClassString)
		return i + 1, true
	}
	return 0, false
}

func (x *jsScanner) word(i, e int, prev string, dot bool) {
	s, w := x.s, x.s[i:e]
	call := nextIs(s, e, '(')
	class := ""
	x.expr = false
	switch {
	case dot:
		if call {
			class = ClassFunction
		}
	case jsKeywords[w] || x.opt.ts && tsKeywords[w]:
		if jsContextual[w] && call {
			class = ClassFunction
			break
		}
		class = ClassKeyword
		x.prev = w
		x.expr = !jsValues[w]
	case x.opt.ts && tsTypes[w] || jsTypeDecl[prev]:
		class = ClassType
	case prev == "function" || call:
		class = ClassFunction
	}
	x.o.tok(i, e, class)
}

// closeBrace pops the frame a closing brace at i ends.
func (x *jsScanner) closeBrace(i int) {
	x.expr = true
	if len(x.stack) == 0 {
		return
	}
	switch x.mode() {
	case jsInterp:
		x.o.tok(i, i+1, ClassOperator)
		x.expr = false
	case jsTagExpr:
		x.expr = false
	case jsBrace, jsTemplate, jsTag, jsChildren:
	}
	x.pop()
}

// regexEnd returns the end of the regular expression literal at i, with its flags, and whether
// it closes on its line.
func regexEnd(s string, i int) (int, bool) {
	class := false
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			if j+1 < len(s) && s[j+1] == '\n' {
				return 0, false
			}
			j++
		case '[':
			class = true
		case ']':
			class = false
		case '/':
			if !class {
				return identEnd(s, j+1), true
			}
		case '\n':
			return 0, false
		}
	}
	return 0, false
}

// template classes a template literal's text from i up to its end or its next ${.
func (x *jsScanner) template(i int) int {
	s, o := x.s, x.o
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '`':
			o.tok(i, j+1, ClassString)
			x.pop()
			x.expr = false
			return j + 1
		case '$':
			if j+1 < len(s) && s[j+1] == '{' && x.push(jsInterp) {
				o.tok(i, j, ClassString)
				o.tok(j, j+2, ClassOperator)
				x.expr = true
				return j + 2
			}
		}
	}
	o.tok(i, len(s), ClassString)
	return len(s)
}

// tag classes one token inside a JSX tag at i and returns its end.
func (x *jsScanner) tag(i int) int {
	s, o := x.s, x.o
	top := &x.stack[len(x.stack)-1]
	c := s[i]
	switch {
	case isBlank(c):
	case c == '/' && i+1 < len(s) && s[i+1] == '>':
		x.pop()
		x.afterTag()
		return i + 2
	case c == '/':
		top.closing = true
	case c == '>':
		closing := top.closing
		x.pop()
		if !closing {
			x.push(jsChildren)
			return i + 1
		}
		if x.mode() == jsChildren && len(x.stack) > 0 {
			x.pop()
		}
		x.afterTag()
	case isIdentStart(c) || c == '$':
		e := i
		for e < len(s) && (isIdentByte(s[e]) || s[e] == '$' || s[e] == '-' || s[e] == '.' || s[e] == ':') {
			e++
		}
		if !top.named {
			top.named = true
			o.tok(i, e, ClassType)
		}
		return e
	case c == '"' || c == '\'':
		e := quoteEnd(s, i, c, true)
		o.tok(i, e, ClassString)
		return e
	case c == '{':
		x.push(jsTagExpr)
		x.expr = true
	case c == '=':
		o.tok(i, i+1, ClassOperator)
	}
	return i + 1
}

// afterTag sets the state after a JSX element that ends at the code level.
func (x *jsScanner) afterTag() {
	if x.mode() != jsChildren {
		x.expr = false
	}
}

// children skips JSX text from i to the next tag or expression.
func (x *jsScanner) children(i int) int {
	s := x.s
	j := strings.IndexAny(s[i:], "<{")
	if j < 0 {
		return len(s)
	}
	j += i
	if s[j] == '<' {
		x.push(jsTag)
	} else {
		x.push(jsTagExpr)
		x.expr = true
	}
	return j + 1
}
