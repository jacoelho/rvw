// Package worddiff finds the changed words between a deleted line and the added line paired with
// it, using Myers' O(ND) difference algorithm over word tokens.
package worddiff

import (
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxChars is the longest line, in characters, that gets word changes.
const MaxChars = 1000

// maxEdits bounds the search. Lines needing more token edits are rewrites, and highlighting
// their words adds nothing to the line's own deleted or added colour.
const maxEdits = 256

// Range is a changed span [start, end) in UTF-16 code units, the unit JavaScript strings index by.
type Range [2]int

// Diff returns the changed ranges of a and b. ok is false when either line is longer than
// MaxChars or the lines differ by more than maxEdits tokens.
func Diff(a, b string) (ra, rb []Range, ok bool) {
	if utf8.RuneCountInString(a) > MaxChars || utf8.RuneCountInString(b) > MaxChars {
		return nil, nil, false
	}
	ta, tb := tokens(a), tokens(b)
	keepA, keepB, ok := myers(ta, tb)
	if !ok {
		return nil, nil, false
	}
	return ranges(ta, keepA), ranges(tb, keepB), true
}

// tokens splits s into runs of letters, digits and '_', runs of white space, and single other
// characters.
func tokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		j := i + size
		if c := class(r); c != other {
			for j < len(s) {
				r2, size2 := utf8.DecodeRuneInString(s[j:])
				if class(r2) != c {
					break
				}
				j += size2
			}
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

const (
	word = iota
	space
	other
)

func class(r rune) int {
	switch {
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return word
	case unicode.IsSpace(r):
		return space
	default:
		return other
	}
}

// myers marks the tokens of a and b that belong to a shortest edit script's common subsequence.
//
// A first pass finds the edit distance D without keeping history, so a pair over maxEdits costs
// one frontier array. A second pass records the frontier of each step d < D: only diagonals
// -d, -d+2, ..., d, which are the ones backtracking reads, (D+1)D/2 values in all.
func myers(a, b []string) (keepA, keepB []bool, ok bool) {
	n, m := len(a), len(b)
	if n-m > maxEdits || m-n > maxEdits {
		return nil, nil, false
	}
	limit := min(n+m, maxEdits)
	off := limit + 1
	v := make([]int, 2*limit+3)
	dist := -1
	for d := 0; d <= limit && dist < 0; d++ {
		if myersStep(a, b, v, off, d) {
			dist = d
		}
	}
	if dist < 0 {
		return nil, nil, false
	}
	clear(v)
	trace := make([]int, 0, (dist+1)*dist/2)
	for d := range dist {
		myersStep(a, b, v, off, d)
		for k := -d; k <= d; k += 2 {
			trace = append(trace, v[off+k])
		}
	}
	keepA, keepB = backtrack(trace, dist, n, m)
	return keepA, keepB, true
}

// myersStep extends every diagonal of step d in v, reporting whether one reaches the end of both
// sequences.
func myersStep(a, b []string, v []int, off, d int) bool {
	n, m := len(a), len(b)
	for k := -d; k <= d; k += 2 {
		var x int
		if k == -d || k != d && v[off+k-1] < v[off+k+1] {
			x = v[off+k+1]
		} else {
			x = v[off+k-1] + 1
		}
		y := x - k
		for x < n && y < m && a[x] == b[y] {
			x++
			y++
		}
		v[off+k] = x
		if x >= n && y >= m {
			return true
		}
	}
	return false
}

// backtrack walks trace, the frontiers of steps 0..dist-1, back from the end of both sequences.
func backtrack(trace []int, dist, n, m int) (keepA, keepB []bool) {
	keepA, keepB = make([]bool, n), make([]bool, m)
	x, y := n, m
	for d := dist; d > 0; d-- {
		// Step d-1's frontier holds diagonals -(d-1), -(d-1)+2, ..., d-1.
		prev := trace[(d-1)*d/2:]
		at := func(k int) int { return prev[(k+d-1)/2] }
		k := x - y
		var prevK int
		if k == -d || k != d && at(k-1) < at(k+1) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			keepA[x], keepB[y] = true, true
		}
		if x == prevX {
			y--
		} else {
			x--
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		keepA[x], keepB[y] = true, true
	}
	return keepA, keepB
}

// ranges merges adjacent changed tokens into UTF-16 ranges.
func ranges(toks []string, keep []bool) []Range {
	var out []Range
	pos := 0
	for i, t := range toks {
		w := utf16Len(t)
		if !keep[i] {
			if n := len(out); n > 0 && out[n-1][1] == pos {
				out[n-1][1] = pos + w
			} else {
				out = append(out, Range{pos, pos + w})
			}
		}
		pos += w
	}
	return out
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
