package highlight

import "strings"

// HCL, as in Terraform. Quoted strings and here-documents are templates: ${...} and %{...}
// hold expressions.

var (
	hclKeywords = words(`true false null for in if else endif endfor`)
	hclTypes    = words(`string number bool list map set object tuple any`)
)

type hclFrameKind uint8

const (
	hclBrace  hclFrameKind = iota // a brace in an expression
	hclInterp                     // ${ or %{ in a template
	hclQuote                      // inside "..."
	hclDoc                        // a here-document's body, up to the end of the text it is given
)

type hclScanner struct {
	o *out
	// doc reads the here-documents' bodies, one after another; reusing it allocates its memory
	// once per file, not once per here-document.
	doc      *hclScanner
	s        string
	stack    []hclFrameKind
	heredocs []heredoc
	// lineStart reports that no token precedes on this line, so a word is an attribute name or
	// a block type.
	lineStart bool
	// inDoc reports that the scanner reads a here-document's body. There << is an operator:
	// opening a here-document inside one would rescan the rest of the text per level.
	inDoc bool
}

func scanHCL(o *out, s string, i int) {
	x := hclScanner{o: o, s: s, lineStart: true}
	x.run(i)
}

func (x *hclScanner) run(i int) {
	for i < len(x.s) && !x.o.stopped() {
		switch x.mode() {
		case hclQuote, hclDoc:
			i = x.template(i)
		case hclBrace, hclInterp:
			i = x.code(i)
		}
	}
}

func (x *hclScanner) mode() hclFrameKind {
	if len(x.stack) == 0 {
		return hclBrace
	}
	return x.stack[len(x.stack)-1]
}

func (x *hclScanner) push(k hclFrameKind) bool {
	if len(x.stack) == maxFrames {
		return false
	}
	x.stack = append(x.stack, k)
	return true
}

func hclIdentEnd(s string, i int) int {
	for i < len(s) && (isIdentByte(s[i]) || s[i] == '-') {
		i++
	}
	return i
}

// code classes one token of an expression at i and returns its end.
func (x *hclScanner) code(i int) int {
	s, o := x.s, x.o
	c := s[i]
	if c == '\n' {
		x.lineStart = true
		if len(x.heredocs) > 0 {
			return x.readHeredocs(i + 1)
		}
		return i + 1
	}
	if isBlank(c) {
		return i + 1
	}
	e, comment := commentEnd(s, i)
	if c == '#' {
		e, comment = lineEnd(s, i), true
	}
	if comment {
		// A comment leaves lineStart as it is: the line's next word may still be a name.
		o.tok(i, e, ClassComment)
		return e
	}
	start := x.lineStart
	x.lineStart = false
	switch {
	case c == '"':
		if x.push(hclQuote) {
			o.tok(i, i+1, ClassString)
		}
	case c == '<' && !x.inDoc && strings.HasPrefix(s[i:], "<<"):
		return x.heredocStart(i)
	case isDigit(c):
		e := numberEnd(s, i)
		o.tok(i, e, ClassNumber)
		return e
	case isIdentStart(c):
		e := hclIdentEnd(s, i)
		w := s[i:e]
		class := ""
		switch {
		case hclKeywords[w]:
			class = ClassKeyword
		case hclTypes[w]:
			class = ClassType
		case nextIs(s, e, '('):
			class = ClassFunction
		case start:
			// A block type is followed by labels or its body; an attribute by =.
			if j := skipBlanks(s, e); j < len(s) && (s[j] == '"' || s[j] == '{' || isIdentStart(s[j])) {
				class = ClassKeyword
			}
		}
		o.tok(i, e, class)
		return e
	case c == '{':
		x.push(hclBrace)
	case c == '}':
		if len(x.stack) > 0 {
			if x.mode() == hclInterp {
				o.tok(i, i+1, ClassKeyword)
			}
			x.stack = x.stack[:len(x.stack)-1]
		}
	case cOperators[c]:
		o.tok(i, i+1, ClassOperator)
	}
	return i + 1
}

// template classes a template's text from i up to its end or its next interpolation.
func (x *hclScanner) template(i int) int {
	s, o := x.s, x.o
	quoted := x.mode() == hclQuote
	for j := i; j < len(s); j++ {
		switch c := s[j]; c {
		case '\\':
			if quoted {
				j++
			}
		case '"', '\n':
			if quoted {
				e := j
				if c == '"' {
					e++
				}
				o.tok(i, e, ClassString)
				x.stack = x.stack[:len(x.stack)-1]
				return e
			}
		case '$', '%':
			if j+2 < len(s) && s[j+1] == c && s[j+2] == '{' {
				j += 2 // $${ and %%{ are literal
				continue
			}
			if j+1 < len(s) && s[j+1] == '{' && x.push(hclInterp) {
				o.tok(i, j, ClassString)
				o.tok(j, j+2, ClassKeyword)
				return j + 2
			}
		}
	}
	o.tok(i, len(s), ClassString)
	return len(s)
}

// heredocStart reads the << or <<- at i and its delimiter.
func (x *hclScanner) heredocStart(i int) int {
	s, o := x.s, x.o
	j := i + 2
	strip := j < len(s) && s[j] == '-'
	if strip {
		j++
	}
	e := identEnd(s, j)
	if e == j {
		o.tok(i, i+2, ClassOperator)
		return i + 2
	}
	o.tok(i, e, ClassString)
	x.heredocs = append(x.heredocs, heredoc{delim: s[j:e]})
	return e
}

// readHeredocs classes the bodies of the line's here-documents, which start at i, as templates,
// and returns the index after the last closing line. HCL allows indentation before any closing
// delimiter.
func (x *hclScanner) readHeredocs(i int) int {
	s, o := x.s, x.o
	for _, h := range x.heredocs {
		bodyEnd, closeAt, after := len(s), -1, len(s)
		for j := i; j < len(s); {
			le := lineEnd(s, j)
			if strings.TrimSpace(s[j:le]) == h.delim {
				bodyEnd, closeAt, after = j, j, min(le+1, len(s))
				break
			}
			j = le + 1
		}
		if x.doc == nil {
			x.doc = &hclScanner{o: o, inDoc: true}
		}
		doc := x.doc
		doc.s, doc.stack, doc.lineStart = s[:bodyEnd], append(doc.stack[:0], hclDoc), false
		doc.run(i)
		if closeAt >= 0 {
			o.tok(closeAt, lineEnd(s, closeAt), ClassString)
		}
		i = after
	}
	x.heredocs = x.heredocs[:0]
	return i
}
