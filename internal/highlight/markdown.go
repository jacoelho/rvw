package highlight

import "strings"

// CommonMark with GitHub's fenced code: a fence naming a highlighted language is read as that
// language. YAML front matter, between --- lines at the start of the document, is read as YAML.
// Emphasis is not classed; no class renders it.

func scanMarkdown(o *out, s string, i, nesting int) {
	i = mdFrontMatter(o, s, i)
	for i < len(s) && !o.stopped() {
		le := lineEnd(s, i)
		k := i
		for k < le && k-i < 3 && s[k] == ' ' {
			k++
		}
		if k < le {
			c := s[k]
			n := runEnd(s, k, le, c) - k
			switch {
			case (c == '`' || c == '~') && n >= 3 && (c == '~' || strings.IndexByte(s[k+n:le], '`') < 0):
				i = mdFence(o, s, k, le, n, nesting)
				continue
			case c == '#' && n <= 6 && (k+n == le || isBlank(s[k+n])):
				o.tok(k, le, ClassKeyword)
				i = le + 1
				continue
			case isThematic(s[k:le]):
				o.tok(k, le, ClassOperator)
				i = le + 1
				continue
			}
			k = mdContainers(o, s, k, le)
		}
		for {
			e := mdInline(o, s, k, le)
			if e <= le {
				break
			}
			// An HTML comment ran past the line: go on from its end, still inline.
			k, le = e, lineEnd(s, e)
		}
		i = le + 1
	}
}

// mdFrontMatter classes the YAML front matter that starts the document at i and returns the
// index after it, or i if there is none. Front matter needs its closing --- or ... line; without
// one the opening line is a thematic break. The search for it runs once per document.
func mdFrontMatter(o *out, s string, i int) int {
	le := lineEnd(s, i)
	if strings.TrimRight(s[i:le], " \t\r") != "---" {
		return i
	}
	for j := min(le+1, len(s)); j < len(s); {
		e := lineEnd(s, j)
		if l := strings.TrimRight(s[j:e], " \t\r"); l == "---" || l == "..." {
			o.tok(i, le, ClassKeyword)
			scanYAML(o, s[:j], le+1)
			o.tok(j, j+3, ClassKeyword)
			return min(e+1, len(s))
		}
		j = e + 1
	}
	return i
}

func runEnd(s string, i, e int, c byte) int {
	for i < e && s[i] == c {
		i++
	}
	return i
}

// isThematic reports whether line is a thematic break or a setext underline.
func isThematic(line string) bool {
	line = strings.TrimRight(line, " \t\r")
	if line == "" {
		return false
	}
	c := line[0]
	if c == '=' {
		return strings.Trim(line, "=") == ""
	}
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	n := 0
	for j := range len(line) {
		switch line[j] {
		case c:
			n++
		case ' ', '\t':
		default:
			return false
		}
	}
	return n >= 3
}

// mdContainers classes block quote and list markers at k and returns the start of the content.
func mdContainers(o *out, s string, k, le int) int {
	for k < le && s[k] == '>' {
		o.tok(k, k+1, ClassOperator)
		k = skipBlanks(s, k+1)
	}
	if k >= le {
		return k
	}
	switch c := s[k]; {
	case (c == '-' || c == '*' || c == '+') && (k+1 == le || isBlank(s[k+1])):
		o.tok(k, k+1, ClassOperator)
		return k + 1
	case isDigit(c):
		d := k
		for d < le && d-k < 9 && isDigit(s[d]) {
			d++
		}
		if d < le && (s[d] == '.' || s[d] == ')') && (d+1 == le || isBlank(s[d+1])) {
			o.tok(k, d+1, ClassOperator)
			return d + 1
		}
	}
	return k
}

// mdFence classes the fenced code block whose opening fence of n bytes is at k, on the line
// ending at le, and returns the index after its closing fence.
func mdFence(o *out, s string, k, le, n, nesting int) int {
	c := s[k]
	o.tok(k, le, ClassKeyword)
	info := strings.TrimSpace(s[k+n : le])
	word := info
	if j := strings.IndexAny(info, " \t{,"); j >= 0 {
		word = info[:j]
	}
	body := min(le+1, len(s))
	bodyEnd, after := len(s), len(s)
	closeStart, closeEnd := -1, -1
	for j := body; j < len(s); {
		e := lineEnd(s, j)
		t := j
		for t < e && t-j < 3 && s[t] == ' ' {
			t++
		}
		r := runEnd(s, t, e, c)
		if r-t >= n && strings.TrimSpace(s[r:e]) == "" {
			bodyEnd, after = j, min(e+1, len(s))
			closeStart, closeEnd = t, r
			break
		}
		j = e + 1
	}
	if lang := fenceLanguage(word); lang != langNone && nesting < maxNesting {
		scan(o, lang, s[:bodyEnd], body, nesting+1)
	} else {
		o.tok(body, bodyEnd, ClassString)
	}
	if closeStart >= 0 {
		o.tok(closeStart, closeEnd, ClassKeyword)
	}
	return after
}

// mdRuns indexes a line's backtick runs by length, so each code span finds its closer without
// rescanning the line.
type mdRuns struct {
	starts map[int][]int // run starts by run length, in order
	head   map[int]int   // the first entry of starts[n] not yet passed
}

func newMDRuns(s string, i, le int) mdRuns {
	r := mdRuns{starts: map[int][]int{}, head: map[int]int{}}
	for i < le {
		if s[i] != '`' {
			i++
			continue
		}
		e := runEnd(s, i, le, '`')
		r.starts[e-i] = append(r.starts[e-i], i)
		i = e
	}
	return r
}

// closer returns the start of the first run of n backticks after p, or -1.
func (r mdRuns) closer(n, p int) int {
	list := r.starts[n]
	h := r.head[n]
	for h < len(list) && list[h] <= p {
		h++
	}
	r.head[n] = h
	if h == len(list) {
		return -1
	}
	return list[h]
}

// mdInline classes code spans, link destinations, autolinks and HTML comments from i to the
// line's end le. It returns the index it stopped at, which is past le when an HTML comment
// continues onto later lines.
func mdInline(o *out, s string, i, le int) int {
	var runs mdRuns
	noParen := le // no ) closes a link destination from this offset on
	for i < le {
		c := s[i]
		switch {
		case c == '\\':
			i += 2
		case c == '`':
			if runs.starts == nil {
				runs = newMDRuns(s, i, le)
			}
			n := runEnd(s, i, le, '`') - i
			if j := runs.closer(n, i); j >= 0 {
				o.tok(i, j+n, ClassString)
				i = j + n
			} else {
				i += n
			}
		case c == ']' && i+1 < le && s[i+1] == '(':
			if i+2 < noParen {
				if j := strings.IndexByte(s[i+2:le], ')'); j >= 0 {
					o.tok(i+2, i+2+j, ClassString)
					i += 3 + j
					continue
				}
				noParen = i + 2
			}
			i += 2
		case c == '<' && strings.HasPrefix(s[i:], "<!--"):
			e := closeEnd(s, i+4, "-->")
			o.tok(i, e, ClassComment)
			if e > le {
				return e
			}
			i = e
		case c == '<':
			j := i + 1
			for j < le && s[j] != '>' && s[j] != '<' && !isBlank(s[j]) {
				j++
			}
			if j < le && s[j] == '>' && strings.IndexByte(s[i+1:j], ':') > 0 {
				o.tok(i, j+1, ClassString)
				i = j + 1
				continue
			}
			i++
		default:
			i++
		}
	}
	return min(i, le)
}
