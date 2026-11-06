package worddiff_test

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/jacoelho/rvw/internal/worddiff"
)

// TestDiffKnownPairs covers literal cases: a one-word edit highlights only that word, in UTF-16
// code units.
func TestDiffKnownPairs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		a, b   string
		ra, rb []worddiff.Range
	}{
		{"one word", "foo bar baz", "foo qux baz", []worddiff.Range{{4, 7}}, []worddiff.Range{{4, 7}}},
		{"inserted argument", "call(a)", "call(a, b)", nil, []worddiff.Range{{6, 9}}},
		{"deleted word", "return x + y", "return x", []worddiff.Range{{8, 12}}, nil},
		{"punctuation", "a.b", "a,b", []worddiff.Range{{1, 2}}, []worddiff.Range{{1, 2}}},
		{"after accented letter", "café old", "café new", []worddiff.Range{{5, 8}}, []worddiff.Range{{5, 8}}},
		{"after combining mark", "café old", "café new", []worddiff.Range{{6, 9}}, []worddiff.Range{{6, 9}}},
		{"surrogate pair changed", "a 😀 b", "a 😃 b", []worddiff.Range{{2, 4}}, []worddiff.Range{{2, 4}}},
		{"after surrogate pair", "😀 old", "😀 new", []worddiff.Range{{3, 6}}, []worddiff.Range{{3, 6}}},
		{"CJK word", "値 is 古い", "値 is 新しい", []worddiff.Range{{5, 7}}, []worddiff.Range{{5, 8}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ra, rb, ok := worddiff.Diff(tc.a, tc.b)
			if !ok {
				t.Fatal("ok = false")
			}
			if !reflect.DeepEqual(ra, tc.ra) || !reflect.DeepEqual(rb, tc.rb) {
				t.Errorf("Diff(%q, %q) = %v, %v; want %v, %v", tc.a, tc.b, ra, rb, tc.ra, tc.rb)
			}
			checkRanges(t, tc.a, tc.b, ra, rb)
		})
	}
}

func TestDiffIdentical(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "same line", "😀 x\ty", strings.Repeat("a", worddiff.MaxChars)} {
		ra, rb, ok := worddiff.Diff(s, s)
		if !ok || len(ra) != 0 || len(rb) != 0 {
			t.Errorf("Diff(%q, same) = %v, %v, %t; want no ranges, ok", s, ra, rb, ok)
		}
	}
}

// TestDiffLengthCap covers the 1,000-character cap on either line.
func TestDiffLengthCap(t *testing.T) {
	t.Parallel()
	at := strings.Repeat("a", 999) + "b"
	over := at + "c"
	if _, _, ok := worddiff.Diff(at, strings.Repeat("a", 999)+"c"); !ok {
		t.Error("two 1,000-character lines: ok = false")
	}
	for _, pair := range [][2]string{{over, at}, {at, over}, {over, over}} {
		if ra, rb, ok := worddiff.Diff(pair[0], pair[1]); ok || ra != nil || rb != nil {
			t.Errorf("Diff(%d chars, %d chars) = %v, %v, %t; want no ranges, not ok", len(pair[0]), len(pair[1]), ra, rb, ok)
		}
	}
}

// TestDiffEditCap covers giving up after 256 edits: a rewrite well past the cap has
// no ranges, and a line well under it does.
func TestDiffEditCap(t *testing.T) {
	t.Parallel()
	words := func(prefix string, n int) string {
		w := make([]string, n)
		for i := range w {
			w[i] = prefix + string(rune('a'+i%26)) + string(rune('a'+i/26))
		}
		return strings.Join(w, " ")
	}
	a, b := words("p", 200), words("q", 200) // 400 token edits
	if len(a) > worddiff.MaxChars {
		t.Fatalf("fixture: %d characters exceeds the length cap", len(a))
	}
	if ra, rb, ok := worddiff.Diff(a, b); ok || ra != nil || rb != nil {
		t.Errorf("400-edit rewrite: %d, %d ranges, ok %t; want none, not ok", len(ra), len(rb), ok)
	}
	a, b = words("p", 50), words("q", 50) // 100 token edits
	if _, _, ok := worddiff.Diff(a, b); !ok {
		t.Error("100-edit rewrite: ok = false")
	}
}

// TestDiffRangeProperties checks every result over a seeded corpus: ranges are sorted,
// non-empty, disjoint, in bounds and never split a surrogate pair, and removing the ranged
// spans from both lines leaves the same common text.
func TestDiffRangeProperties(t *testing.T) {
	t.Parallel()
	alphabet := []string{"foo", "bar", "x", "_y1", " ", "  ", "\t", ".", "(", ")", "😀", "é", "é", "名前", "-", "→", "42"}
	rng := rand.New(rand.NewPCG(1, 2))
	line := func() string {
		var b strings.Builder
		for range rng.IntN(25) {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	for range 2000 {
		a := line()
		b := mutate(rng, a, alphabet)
		ra, rb, ok := worddiff.Diff(a, b)
		if !ok {
			continue
		}
		checkRanges(t, a, b, ra, rb)
		if a == b && (len(ra) != 0 || len(rb) != 0) {
			t.Errorf("identical %q: ranges %v %v", a, ra, rb)
		}
	}
}

// mutate replaces, inserts or deletes a few alphabet tokens of s.
func mutate(rng *rand.Rand, s string, alphabet []string) string {
	for range rng.IntN(4) {
		pos := 0
		if s != "" {
			pos = rng.IntN(len(s) + 1)
			for pos < len(s) && !utf8Start(s[pos]) {
				pos++
			}
		}
		tok := alphabet[rng.IntN(len(alphabet))]
		switch rng.IntN(3) {
		case 0:
			s = s[:pos] + tok + s[pos:]
		case 1:
			end := pos
			for end < len(s) && end < pos+3 {
				end++
			}
			for end < len(s) && !utf8Start(s[end]) {
				end++
			}
			s = s[:pos] + s[end:]
		default:
			s = s[:pos] + tok + strings.TrimPrefix(s[pos:], tok)
		}
	}
	return s
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func checkRanges(t *testing.T, a, b string, ra, rb []worddiff.Range) {
	t.Helper()
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	ca, okA := common(ua, ra)
	cb, okB := common(ub, rb)
	if !okA || !okB {
		t.Errorf("Diff(%q, %q): invalid ranges %v, %v", a, b, ra, rb)
		return
	}
	if ca != cb {
		t.Errorf("Diff(%q, %q) = %v, %v: unranged text %q != %q", a, b, ra, rb, ca, cb)
	}
}

// common removes the ranges from u and reports whether they are well formed.
func common(u []uint16, rs []worddiff.Range) (string, bool) {
	var out []uint16
	pos := 0
	for _, r := range rs {
		if r[0] < pos || r[1] <= r[0] || r[1] > len(u) || splitsPair(u, r[0]) || splitsPair(u, r[1]) {
			return "", false
		}
		out = append(out, u[pos:r[0]]...)
		pos = r[1]
	}
	out = append(out, u[pos:]...)
	return string(utf16.Decode(out)), true
}

func splitsPair(u []uint16, i int) bool {
	return i > 0 && i < len(u) && utf16.IsSurrogate(rune(u[i])) && u[i] >= 0xDC00
}

// TestDiffEditCapBoundary rewrites every word of a line, so the only common tokens are the
// separating spaces and the shortest edit script is known: 2 edits per word plus 1 per extra
// token. 256 edits is the last count that gets word changes.
func TestDiffEditCapBoundary(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		a, b   string
		edits  int
		wantOK bool
	}{
		{"256 edits", words("p", 128), words("q", 128), 256, true},
		{"257 edits", words("p", 128), words("q", 128) + "!", 257, false},
		{"258 edits", words("p", 129), words("q", 129), 258, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if len(c.a) > worddiff.MaxChars || len(c.b) > worddiff.MaxChars {
				t.Fatalf("fixture exceeds the length cap: %d, %d characters", len(c.a), len(c.b))
			}
			ra, rb, ok := worddiff.Diff(c.a, c.b)
			if !c.wantOK {
				if ok || ra != nil || rb != nil {
					t.Errorf("%d edits: %d, %d ranges, ok %t; want none, not ok", c.edits, len(ra), len(rb), ok)
				}
				return
			}
			if !ok {
				t.Fatalf("%d edits: ok = false", c.edits)
			}
			if want := wordRanges(c.a); !reflect.DeepEqual(ra, want) {
				t.Errorf("old ranges = %v; want every word %v", ra, want)
			}
			if want := wordRanges(c.b); !reflect.DeepEqual(rb, want) {
				t.Errorf("new ranges = %v; want every word %v", rb, want)
			}
		})
	}
}

// wordRanges returns the span of every space-separated word of an ASCII line.
func wordRanges(s string) []worddiff.Range {
	var out []worddiff.Range
	pos := 0
	for w := range strings.SplitSeq(s, " ") {
		out = append(out, worddiff.Range{pos, pos + len(w)})
		pos += len(w) + 1
	}
	return out
}

// TestDiffIsMinimal checks Diff against a longest-common-subsequence oracle. Lines alternate
// single word characters and single punctuation characters, so every character is its own
// token and one UTF-16 unit: the characters left outside the ranges are the common tokens.
// Diff must keep as many as the LCS and report ok exactly when the edit distance is within 256.
func TestDiffIsMinimal(t *testing.T) {
	t.Parallel()
	const wordChars, otherChars = "xyz", ".,-"
	rng := rand.New(rand.NewPCG(3, 4))
	line := func(n int) []byte {
		out := make([]byte, n)
		for i := range out {
			if i%2 == 0 {
				out[i] = wordChars[rng.IntN(len(wordChars))]
			} else {
				out[i] = otherChars[rng.IntN(len(otherChars))]
			}
		}
		return out
	}
	var sawOK, sawCapped bool
	for i := range 400 {
		a := line(rng.IntN(400))
		var b []byte
		if i%2 == 0 {
			b = line(rng.IntN(400))
		} else {
			// A nearby line: a with some characters replaced by ones of the same class.
			b = slices.Clone(a)
			for range rng.IntN(len(b)/4 + 1) {
				if len(b) > 0 {
					j := rng.IntN(len(b))
					b[j] = line(j + 1)[j]
				}
			}
		}
		lcs := lcsLen(a, b)
		wantOK := len(a)+len(b)-2*lcs <= 256
		ra, rb, ok := worddiff.Diff(string(a), string(b))
		if ok != wantOK {
			t.Fatalf("Diff(%q, %q): ok = %t; want %t (edit distance %d)", a, b, ok, wantOK, len(a)+len(b)-2*lcs)
		}
		if !ok {
			sawCapped = true
			continue
		}
		sawOK = true
		checkRanges(t, string(a), string(b), ra, rb)
		if keptA, keptB := len(a)-width(ra), len(b)-width(rb); keptA != lcs || keptB != lcs {
			t.Fatalf("Diff(%q, %q) keeps %d and %d tokens; want the LCS length %d", a, b, keptA, keptB, lcs)
		}
	}
	if !sawOK || !sawCapped {
		t.Fatalf("corpus covers ok %t, capped %t; want both", sawOK, sawCapped)
	}
}

func width(rs []worddiff.Range) int {
	n := 0
	for _, r := range rs {
		n += r[1] - r[0]
	}
	return n
}

// lcsLen is the textbook dynamic-programming longest common subsequence length.
func lcsLen(a, b []byte) int {
	prev, cur := make([]int, len(b)+1), make([]int, len(b)+1)
	for i := range a {
		for j := range b {
			if a[i] == b[j] {
				cur[j+1] = prev[j] + 1
			} else {
				cur[j+1] = max(prev[j+1], cur[j])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
