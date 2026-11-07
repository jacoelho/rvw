package highlight

import "strings"

// GNU make. A line that starts with a tab is a recipe, read as shell in make mode.

var (
	makeDirectives = words(`include -include sinclude define endef undefine ifdef ifndef ifeq ifneq
		else endif export unexport override private vpath`)
	makeFunctions = words(`subst patsubst strip findstring filter filter-out sort word words wordlist
		firstword lastword dir notdir suffix basename addsuffix addprefix join wildcard realpath
		abspath error warning info shell origin flavor foreach if or and intcmp call eval file value
		let guile`)
)

// maxRefNesting bounds the make function calls nested in one reference that are classed as
// calls; deeper ones are read as plain references.
const maxRefNesting = 32

type makeOpKind uint8

const (
	makeNoOp makeOpKind = iota
	makeAssign
	makeRule
)

func scanMakefile(o *out, s string, i int) {
	inDefine := false
	sh := shScanner{o: o, make: true}
	for i < len(s) && !o.stopped() {
		end := logicalLineEnd(s, i)
		line := s[:end]
		next := min(end+1, len(s))
		k := skipBlanks(s, i)
		we := k
		for we < end && !isBlank(s[we]) && s[we] != '(' {
			we++
		}
		w := s[k:we]
		switch {
		case inDefine:
			if w == "endef" {
				o.tok(k, we, ClassKeyword)
				inDefine = false
			}
		case s[i] == '\t':
			j := i + 1
			for j < end && strings.IndexByte("@+- \t", s[j]) >= 0 {
				j++
			}
			o.tok(i+1, j, ClassOperator)
			sh.scan(line, j)
		case k == end:
		case s[k] == '#':
			o.tok(k, end, ClassComment)
		case makeDirectives[w]:
			o.tok(k, we, ClassKeyword)
			switch w {
			case "define":
				inDefine = true
				makeExpr(o, line, we, ClassType, 0)
			case "export", "unexport", "override", "private":
				makeStatement(o, &sh, line, skipBlanks(s, we))
			default:
				makeExpr(o, line, we, "", 0)
			}
		default:
			makeStatement(o, &sh, line, k)
		}
		i = next
	}
}

// logicalLineEnd returns the end of the line at i, joined with the lines after it while it ends
// in an odd number of backslashes.
func logicalLineEnd(s string, i int) int {
	for {
		e := lineEnd(s, i)
		k := e
		if k > i && s[k-1] == '\r' {
			k--
		}
		n := 0
		for k-n > i && s[k-n-1] == '\\' {
			n++
		}
		if n%2 == 0 || e == len(s) {
			return e
		}
		i = e + 1
	}
}

// makeStatement classes an assignment, a rule or an expression from k to the end of s; sh reads
// a rule's inline recipe.
func makeStatement(o *out, sh *shScanner, s string, k int) {
	op, opEnd, kind := makeOp(s, k)
	switch kind {
	case makeAssign:
		makeExpr(o, s[:op], k, ClassType, 0)
		o.tok(op, opEnd, ClassOperator)
		makeExpr(o, s, opEnd, "", 0)
	case makeRule:
		makeExpr(o, s[:op], k, ClassFunction, 0)
		o.tok(op, opEnd, ClassOperator)
		if semi := makeExpr(o, s, opEnd, "", ';'); semi < len(s) {
			o.tok(semi, semi+1, ClassOperator)
			sh.scan(s, semi+1)
		}
	case makeNoOp:
		makeExpr(o, s, k, "", 0)
	}
}

// makeOp finds the first assignment operator or rule colon from k outside references.
func makeOp(s string, k int) (op, end int, kind makeOpKind) {
	depth := 0
	for j := k; j < len(s); j++ {
		switch c := s[j]; {
		case c == '$' && j+1 < len(s) && (s[j+1] == '(' || s[j+1] == '{'):
			depth++
			j++
		case depth > 0 && (c == ')' || c == '}'):
			depth--
		case depth > 0:
		case c == '#':
			return 0, 0, makeNoOp
		case c == '=':
			a := j
			for a > k && j-a < 3 && strings.IndexByte(":?+!", s[a-1]) >= 0 {
				a--
			}
			return a, j + 1, makeAssign
		case c == ':':
			e := j + 1
			for e < len(s) && s[e] == ':' {
				e++
			}
			if e < len(s) && s[e] == '=' {
				j = e - 1 // :=, ::= and :::= end at the = case
				continue
			}
			return j, e, makeRule
		}
	}
	return 0, 0, makeNoOp
}

// makeExpr classes text from i with class base, references as types and a comment to the end.
// It stops at the byte stop outside references, if stop is not 0, and returns where it stopped.
func makeExpr(o *out, s string, i int, base string, stop byte) int {
	a := i
	for i < len(s) {
		switch c := s[i]; {
		case c == '$':
			o.tok(a, i, base)
			i = makeRef(o, s, i, 0)
			a = i
		case c == '#':
			o.tok(a, i, base)
			o.tok(i, len(s), ClassComment)
			return len(s)
		case c == stop && stop != 0:
			o.tok(a, i, base)
			return i
		default:
			i++
		}
	}
	o.tok(a, i, base)
	return i
}

// makeRef classes the reference whose $ is at i and returns its end: $x, $(name), ${name}, or
// a function call such as $(patsubst %.c,%.o,$(SRC)).
func makeRef(o *out, s string, i, nesting int) int {
	if i+1 >= len(s) {
		return i + 1
	}
	c := s[i+1]
	switch {
	case c == '$' || c >= 0x80 || isBlank(c):
		return i + 1
	case c != '(' && c != '{':
		o.tok(i, i+2, ClassType)
		return i + 2
	}
	open, closer := c, byte(')')
	if open == '{' {
		closer = '}'
	}
	j := i + 2
	we := j
	for we < len(s) && (isASCIIIdentByte(s[we]) || s[we] == '-') {
		we++
	}
	if nesting < maxRefNesting && makeFunctions[s[j:we]] && we < len(s) && isBlank(s[we]) {
		o.tok(i, j, ClassType)
		o.tok(j, we, ClassFunction)
		depth := 0
		for k := we; k < len(s); {
			switch s[k] {
			case '$':
				k = makeRef(o, s, k, nesting+1)
				continue
			case open:
				depth++
			case closer:
				if depth == 0 {
					o.tok(k, k+1, ClassType)
					return k + 1
				}
				depth--
			}
			k++
		}
		return len(s)
	}
	depth := 0
	for k := j; k < len(s); k++ {
		switch s[k] {
		case open:
			depth++
		case closer:
			if depth == 0 {
				o.tok(i, k+1, ClassType)
				return k + 1
			}
			depth--
		case '\n':
			o.tok(i, k, ClassType)
			return k
		}
	}
	o.tok(i, len(s), ClassType)
	return len(s)
}
