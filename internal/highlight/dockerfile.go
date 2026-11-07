package highlight

import (
	"strings"
)

// Dockerfile. An instruction is a keyword; RUN, CMD, ENTRYPOINT and SHELL read their arguments
// as a shell command, here-documents included, or as JSON in exec form. --flags are types, as are
// ENV, ARG and LABEL keys and $variables. The escape parser directive is not read: the escape
// character is always a backslash.

// dockerInstructions maps each instruction to itself, so that a lookup by an upper-cased copy
// returns a string that outlives the copy.
var dockerInstructions = func() map[string]string {
	m := map[string]string{}
	for w := range strings.FieldsSeq(`ADD ARG CMD COPY ENTRYPOINT ENV EXPOSE FROM HEALTHCHECK LABEL
		MAINTAINER ONBUILD RUN SHELL STOPSIGNAL USER VOLUME WORKDIR`) {
		m[w] = w
	}
	return m
}()

func scanDockerfile(o *out, s string, i int) {
	sh := shScanner{o: o}
	for i < len(s) && !o.stopped() {
		k := skipBlanks(s, i)
		le := lineEnd(s, k)
		switch {
		case k == le:
			i = min(le+1, len(s))
		case s[k] == '#':
			o.tok(k, le, ClassComment)
			i = min(le+1, len(s))
		default:
			i = dockerStatement(o, &sh, s, k)
		}
	}
}

// dockerStatement classes the instruction at k and returns the index after its last line.
func dockerStatement(o *out, sh *shScanner, s string, k int) int {
	end := logicalLineEnd(s, k)
	next := min(end+1, len(s))
	var w string
	for {
		we := k
		for we < end && isASCIIIdentByte(s[we]) {
			we++
		}
		if w = lookupFold(dockerInstructions, s[k:we]); w == "" {
			return next
		}
		o.tok(k, we, ClassKeyword)
		k = dockerFlags(o, s[:end], skipBlanks(s, we))
		// ONBUILD and HEALTHCHECK are followed by the instruction they hold.
		if w != "ONBUILD" && w != "HEALTHCHECK" {
			break
		}
	}
	switch w {
	case "RUN", "CMD", "ENTRYPOINT", "SHELL":
		if k < end && s[k] == '[' {
			scanJSON(o, s[:end], k)
			return next
		}
		return sh.command(s, k, end)
	default:
		dockerArgs(o, s[:end], k, w == "ENV" || w == "ARG" || w == "LABEL", w == "FROM")
		return next
	}
}

// dockerFlags classes the --name=value flags from i and returns where the arguments after them
// start.
func dockerFlags(o *out, s string, i int) int {
	for i+1 < len(s) && s[i] == '-' && s[i+1] == '-' {
		e := i
		for e < len(s) && !isBlank(s[e]) {
			e++
		}
		if eq := strings.IndexByte(s[i:e], '='); eq >= 0 {
			o.tok(i, i+eq, ClassType)
			o.tok(i+eq, i+eq+1, ClassOperator)
		} else {
			o.tok(i, e, ClassType)
		}
		i = skipBlanks(s, e)
	}
	return i
}

// dockerArgs classes the arguments from i to the end of s. keyed reports that a word before = is
// a key; from, that AS is a keyword.
func dockerArgs(o *out, s string, i int, keyed, from bool) {
	for i < len(s) {
		c := s[i]
		switch {
		case isBlank(c):
			i++
		case c == '\\':
			i = escapeEnd(s, i)
		case c == '"' || c == '\'':
			e := quoteEnd(s, i, c, true)
			o.tok(i, e, ClassString)
			i = e
		case c == '$':
			i = dockerVar(o, s, i)
		default:
			e := i
			for e < len(s) && !isBlank(s[e]) && strings.IndexByte("\"'$=\\", s[e]) < 0 {
				e++
			}
			switch w := s[i:e]; {
			case e == i: // a lone =
				e++
			case keyed && e < len(s) && s[e] == '=':
				o.tok(i, e, ClassType)
				o.tok(e, e+1, ClassOperator)
				e++
			case from && strings.EqualFold(w, "AS"):
				o.tok(i, e, ClassKeyword)
			case isNumber(w):
				o.tok(i, e, ClassNumber)
			}
			i = e
		}
	}
}

// dockerVar classes the $name or ${name...} at i and returns its end.
func dockerVar(o *out, s string, i int) int {
	if i+1 >= len(s) {
		return i + 1
	}
	e := i + 1
	switch {
	case s[e] == '{':
		e = braceEnd(s, e+1)
	case isIdentStart(s[e]) && s[e] < 0x80:
		for e < len(s) && isASCIIIdentByte(s[e]) {
			e++
		}
	default:
		return e
	}
	o.tok(i, e, ClassType)
	return e
}
