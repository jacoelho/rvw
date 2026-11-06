package worddiff_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/worddiff"
)

// words returns count space-separated words named prefix0, prefix1, ...
func words(prefix string, count int) string {
	w := make([]string, count)
	for i := range w {
		w[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return strings.Join(w, " ")
}

// BenchmarkDiff covers the word-diff costs of one line pair: a similar pair, a rewrite just
// within the edit cap, rewrites past it, and a short rewritten line, the common case.
func BenchmarkDiff(b *testing.B) {
	similar := words("w", 130)
	cases := []struct {
		name   string
		a, b   string
		wantOK bool
	}{
		// Three token edits in an ~800-char line.
		{"similar", similar, strings.Replace(similar, "w64", "x64 y", 1), true},
		// 127 replaced words: 254 edits.
		{"at-cap", words("a", 127), words("b", 127), true},
		// 129 replaced words: 258 edits.
		{"over-cap", words("a", 129), words("b", 129), false},
		// A ~1,000-char line rewritten whole.
		{"rewrite-1000", words("a", 166), words("b", 166), false},
		// An ~80-char line rewritten whole.
		{"rewrite-80", words("aaa", 13), words("bbb", 13), true},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			if _, _, ok := worddiff.Diff(c.a, c.b); ok != c.wantOK {
				b.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			b.ReportAllocs()
			for b.Loop() {
				worddiff.Diff(c.a, c.b)
			}
		})
	}
}
