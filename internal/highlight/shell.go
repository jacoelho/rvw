package highlight

import (
	"strings"
)

// POSIX shell, Bash and Zsh. In make mode the scanner reads a Makefile recipe: $(...), ${...}
// and $x are make references, and $$ is the shell's dollar.

var (
	shKeywords = words(`if then else elif fi case esac for select while until do done function time
		coproc !`)
	shBuiltins = words(`alias bg bind break builtin cd command continue declare echo eval exec exit
		export false fc fg getopts hash jobs kill let local mapfile printf pushd popd pwd read
		readarray readonly return set shift shopt source test trap true type typeset ulimit umask
		unalias unset wait`)
	// shWordEnd are the bytes that end an unquoted word.
	shWordEnd = bytesOf(" \t\r\n|&;<>()$`'\"")
	// shSpecial are the one-byte special parameters, as in $@ or $1.
	shSpecial = bytesOf("@*#?$!-0123456789")
)

type shFrameKind uint8

const (
	shSubst    shFrameKind = iota // $( or $(( up to its parenthesis
	shQuote                       // inside "..."
	shBacktick                    // inside `...`
)

type shFrame struct {
	depth int // open parentheses inside a shSubst
	kind  shFrameKind
	// arith reports that a shSubst is $((...)): << is a shift there, and )) closes it.
	arith bool
}

type heredoc struct {
	delim string
	strip bool // <<- strips leading tabs from the closing line
}

type shScanner struct {
	o     *out
	s     string
	stack []shFrame
	// heredocs are the here-documents opened on the current line; their bodies start on the next.
	heredocs []heredoc
	make     bool
	cmd      bool // the next word starts a command
	// loopIn reports that a for, case or select awaits its in.
	loopIn bool
	fn     bool // the next word names a function
}

func scanShell(o *out, s string, i int) {
	x := shScanner{o: o}
	x.scan(s, i)
}

// scan classes s[i:] as one script. A Makefile or Dockerfile reuses one scanner for all its
// commands, so the stacks' memory is allocated once per file, not once per command.
func (x *shScanner) scan(s string, i int) {
	o := x.o
	x.s, x.stack, x.heredocs = s, x.stack[:0], x.heredocs[:0]
	x.cmd, x.loopIn, x.fn = true, false, false
	for i < len(s) && !o.stopped() {
		if n := len(x.stack); n > 0 && x.stack[n-1].kind == shQuote {
			i = x.quoted(i)
		} else {
			i = x.code(i)
		}
	}
}

// command classes s[i:end] as one command, a Dockerfile's RUN, then the bodies of the
// here-documents it opens, which follow end. It returns the index after the last line read. A
// quote left open ends at end, as Docker ends the instruction there.
func (x *shScanner) command(s string, i, end int) int {
	x.scan(s[:end], i)
	next := min(end+1, len(s))
	if len(x.heredocs) == 0 {
		return next
	}
	x.s = s
	return x.readHeredocs(next)
}

func (x *shScanner) top() *shFrame {
	if len(x.stack) == 0 {
		return nil
	}
	return &x.stack[len(x.stack)-1]
}

func (x *shScanner) push(k shFrameKind) bool {
	if len(x.stack) == maxFrames {
		return false
	}
	x.stack = append(x.stack, shFrame{kind: k})
	return true
}

func (x *shScanner) pop() { x.stack = x.stack[:len(x.stack)-1] }

// code classes one token of code at i and returns its end.
func (x *shScanner) code(i int) int {
	s, o := x.s, x.o
	c := s[i]
	top := x.top()
	switch {
	case c == '\n':
		x.cmd = true
		if len(x.heredocs) > 0 {
			return x.readHeredocs(i + 1)
		}
	case c == ' ' || c == '\t' || c == '\r':
	case c == '\\':
		return escapeEnd(s, i)
	case c == '#' && (i == 0 || isBlank(s[i-1]) || strings.IndexByte(";|&(", s[i-1]) >= 0):
		e := lineEnd(s, i)
		o.tok(i, e, ClassComment)
		return e
	case c == '\'':
		e := closeEnd(s, i+1, "'")
		o.tok(i, e, ClassString)
		x.cmd = false
		return e
	case c == '"':
		if x.push(shQuote) {
			o.tok(i, i+1, ClassString)
		}
		x.cmd = false
	case c == '$':
		x.cmd = false
		return x.dollar(i, i)
	case c == '`':
		o.tok(i, i+1, ClassKeyword)
		if top != nil && top.kind == shBacktick {
			x.pop()
			x.cmd = false
		} else if x.push(shBacktick) {
			x.cmd = true
		}
	case c == '(':
		if top != nil && top.kind == shSubst {
			top.depth++
		}
		x.cmd = true
	case c == ')':
		return x.closeParen(i)
	case c == '<' && strings.HasPrefix(s[i:], "<<") && !strings.HasPrefix(s[i:], "<<<") &&
		(top == nil || !top.arith):
		return x.heredocStart(i)
	case c == '|' || c == '&' || c == ';':
		o.tok(i, i+1, ClassOperator)
		x.cmd = true
	case c == '<' || c == '>':
		o.tok(i, i+1, ClassOperator)
	default:
		return x.word(i)
	}
	return i + 1
}

// closeParen classes the ) at i, which closes a substitution or a nested parenthesis, or ends a
// case pattern, and returns its end.
func (x *shScanner) closeParen(i int) int {
	s, top := x.s, x.top()
	if top != nil && top.kind == shSubst {
		if top.depth == 0 {
			e := i + 1
			if top.arith && e < len(s) && s[e] == ')' {
				e++
			}
			x.o.tok(i, e, ClassKeyword)
			x.pop()
			x.cmd = false
			return e
		}
		top.depth--
	}
	x.cmd = true // a case pattern ends
	return i + 1
}

func (x *shScanner) word(i int) int {
	s, o := x.s, x.o
	e := i
	for e < len(s) && !shWordEnd[s[e]] {
		if s[e] == '\\' {
			e = escapeEnd(s, e)
		} else {
			e++
		}
	}
	e = min(e, len(s))
	w := s[i:e]
	class := ""
	cmd := x.cmd
	x.cmd = false
	switch {
	case x.fn:
		class, x.fn = ClassFunction, false
	case cmd && shKeywords[w]:
		class = ClassKeyword
		x.cmd = true
		x.loopIn = w == "for" || w == "case" || w == "select"
		x.fn = w == "function"
	case x.loopIn && w == "in":
		class, x.loopIn = ClassKeyword, false
	case cmd && isAssignment(w):
		k := strings.IndexByte(w, '=')
		o.tok(i, i+k, ClassType)
		o.tok(i+k, i+k+1, ClassOperator)
		x.cmd = true // an assignment may prefix a command
		return e
	case cmd && (strings.HasPrefix(s[skipBlanks(s, e):], "()")):
		class = ClassFunction
	case cmd && shBuiltins[w]:
		class = ClassType
	case w == "{" || w == "}":
		x.cmd = true
	case isNumber(w):
		class = ClassNumber
	}
	o.tok(i, e, class)
	return e
}

// isAssignment reports whether w starts with NAME=.
func isAssignment(w string) bool {
	k := strings.IndexByte(w, '=')
	if k <= 0 || isDigit(w[0]) {
		return false
	}
	for j := range k {
		if !isASCIIIdentByte(w[j]) {
			return false
		}
	}
	return true
}

func isNumber(w string) bool {
	if w == "" {
		return false
	}
	for j := range len(w) {
		if !isDigit(w[j]) {
			return false
		}
	}
	return true
}

// dollar classes the expansion whose $ is at d; the token starts at a, which is before d when
// a make recipe doubles the dollar.
func (x *shScanner) dollar(a, d int) int {
	s, o := x.s, x.o
	if x.make && a == d {
		if d+1 < len(s) && s[d+1] == '$' {
			return x.dollar(d, d+1)
		}
		return makeRef(o, s, d, 0)
	}
	if d+1 >= len(s) {
		o.tok(a, d+1, "")
		return d + 1
	}
	switch c := s[d+1]; {
	case c == '{':
		e := braceEnd(s, d+2)
		o.tok(a, e, ClassType)
		return e
	case c == '(':
		e := d + 2
		arith := e < len(s) && s[e] == '('
		if arith {
			e++
		}
		if x.push(shSubst) {
			x.top().arith = arith
			o.tok(a, e, ClassKeyword)
			x.cmd = true
		}
		return e
	case c == '\'':
		e := quoteEnd(s, d+1, '\'', true)
		o.tok(a, e, ClassString)
		return e
	case isIdentStart(c) && c < 0x80:
		e := d + 1
		for e < len(s) && isASCIIIdentByte(s[e]) {
			e++
		}
		o.tok(a, e, ClassType)
		return e
	case shSpecial[c]:
		o.tok(a, d+2, ClassType)
		return d + 2
	default:
		return d + 1
	}
}

// braceEnd returns the end of a ${...} expansion whose body starts at i: after its matching
// brace, or at the line's end if it has none.
func braceEnd(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return j + 1
			}
			depth--
		case '\n':
			return j
		}
	}
	return len(s)
}

// quoted classes a double-quoted string's text from i up to its end or its next expansion.
func (x *shScanner) quoted(i int) int {
	s, o := x.s, x.o
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j = escapeEnd(s, j) - 1
		case '"':
			o.tok(i, j+1, ClassString)
			x.pop()
			return j + 1
		case '$':
			if j+1 < len(s) && (s[j+1] == '{' || s[j+1] == '(' || shSpecial[s[j+1]] || isIdentStart(s[j+1]) && s[j+1] < 0x80) {
				o.tok(i, j, ClassString)
				return x.dollar(j, j)
			}
		case '`':
			if len(x.stack) < maxFrames {
				o.tok(i, j, ClassString)
				o.tok(j, j+1, ClassKeyword)
				x.push(shBacktick)
				x.cmd = true
				return j + 1
			}
		}
	}
	o.tok(i, len(s), ClassString)
	return len(s)
}

// heredocStart reads the << or <<- at i and its delimiter word.
func (x *shScanner) heredocStart(i int) int {
	s, o := x.s, x.o
	j := i + 2
	strip := j < len(s) && s[j] == '-'
	if strip {
		j++
	}
	o.tok(i, j, ClassOperator)
	j = skipBlanks(s, j)
	if j >= len(s) {
		return j
	}
	var delim string
	var end int
	switch q := s[j]; q {
	case '\'', '"':
		// Searching only up to the quote or the line's end keeps many delimiters on one line
		// linear.
		k := j + 1
		for k < len(s) && s[k] != q && s[k] != '\n' {
			k++
		}
		if k == len(s) || s[k] != q {
			return j
		}
		delim, end = s[j+1:k], k+1
	default:
		a := j
		if q == '\\' {
			a++
		}
		end = a
		for end < len(s) && !shWordEnd[s[end]] {
			end++
		}
		delim = s[a:end]
	}
	if delim == "" {
		return j
	}
	o.tok(j, end, ClassString)
	x.heredocs = append(x.heredocs, heredoc{delim: delim, strip: strip})
	return end
}

// readHeredocs classes the bodies of the line's here-documents, which start at i, and returns
// the index after the last closing line.
func (x *shScanner) readHeredocs(i int) int {
	s, o := x.s, x.o
	for _, h := range x.heredocs {
		start := i
		for i < len(s) {
			le := lineEnd(s, i)
			line := strings.TrimSuffix(s[i:le], "\r")
			if h.strip {
				line = strings.TrimLeft(line, "\t")
			}
			next := min(le+1, len(s))
			if line == h.delim {
				o.tok(start, le, ClassString)
				start = -1
				i = next
				break
			}
			i = next
		}
		if start >= 0 {
			o.tok(start, len(s), ClassString)
		}
	}
	x.heredocs = x.heredocs[:0]
	return i
}
