package source_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/source"
)

// Search limits, kept apart from the package's constants: the text bounds and 500 matches are
// REQ-NAV-007's; the 496-byte window (120 + 256 + 120) is Q-47's.
const (
	contractMaxTextBytes = 256
	contractMaxMatches   = 500
	contractMaxMatchText = 496
	contractContext      = 120
)

func newSearchText(tb testing.TB, text string) source.SearchText {
	tb.Helper()
	n, err := source.NewSearchText(text)
	if err != nil {
		tb.Fatalf("NewSearchText(%q): %v", text, err)
	}
	return n
}

// search runs Search for text, failing the test on an error, and checks every match's
// invariants.
func search(t *testing.T, snap *source.Snapshot, text string) source.SearchResult {
	t.Helper()
	res, err := snap.Search(t.Context(), newSearchText(t, text))
	if err != nil {
		t.Fatalf("Search(%q): %v", text, err)
	}
	checkMatches(t, snap, res, text)
	return res
}

// folds reports whether a search for text ignores ASCII case (REQ-NAV-007).
func folds(text string) bool {
	return !strings.ContainsFunc(text, func(r rune) bool { return 'A' <= r && r <= 'Z' })
}

// window is a match's whole window: its three parts in line order.
func window(m source.SearchMatch) string { return m.Before + m.Matched + m.After }

// checkMatches checks the contract of every match: a valid file and line, a window no longer than
// 496 bytes whose parts are each valid UTF-8 when the window is, a matched part that is the needle,
// and the result ordered by file then line.
func checkMatches(t *testing.T, snap *source.Snapshot, res source.SearchResult, text string) {
	t.Helper()
	for k, m := range res.Matches {
		if m.File < 0 || m.File >= snap.FileCount() || m.Line < 1 {
			t.Fatalf("match %d: file %d, line %d out of range", k, m.File, m.Line)
		}
		if n := len(m.Before) + len(m.Matched) + len(m.After); n > contractMaxMatchText {
			t.Fatalf("match %d: %d bytes of window, want at most %d", k, n, contractMaxMatchText)
		}
		if m.Matched == "" {
			t.Fatalf("match %d: empty matched part", k)
		}
		if matched := m.Matched == text || folds(text) && asciiLower(m.Matched) == asciiLower(text); !matched {
			t.Errorf("match %d: matched part %q is not %q", k, m.Matched, text)
		}
		splitRune := !utf8.ValidString(m.Before) || !utf8.ValidString(m.Matched) || !utf8.ValidString(m.After)
		if utf8.ValidString(window(m)) && splitRune {
			t.Errorf("match %d: parts %q, %q, %q split a rune", k, m.Before, m.Matched, m.After)
		}
		if k > 0 {
			p := res.Matches[k-1]
			if p.File > m.File || p.File == m.File && p.Line >= m.Line {
				t.Errorf("matches %d and %d out of order: (%d,%d) then (%d,%d)", k-1, k, p.File, p.Line, m.File, m.Line)
			}
		}
	}
}

// asciiLower lowers ASCII letters only.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// hit is where a match is: the file's display path, its side and line.
type hit struct {
	Path    string
	NewSide bool
	Line    int
}

func hitsOf(snap *source.Snapshot, res source.SearchResult) []hit {
	out := make([]hit, 0, len(res.Matches))
	for _, m := range res.Matches {
		out = append(out, hit{snap.File(m.File).Path(), m.NewSide, m.Line})
	}
	return out
}

// sortedHits orders hits as the result does, by the files' snapshot indexes, for comparison.
func sortedHits(t *testing.T, snap *source.Snapshot, hits ...hit) []hit {
	t.Helper()
	slices.SortFunc(hits, func(a, b hit) int {
		if d := fileIndex(t, snap, a.Path) - fileIndex(t, snap, b.Path); d != 0 {
			return d
		}
		return a.Line - b.Line
	})
	return hits
}

func checkHits(t *testing.T, snap *source.Snapshot, text string, want ...hit) {
	t.Helper()
	res := search(t, snap, text)
	if res.Truncated {
		t.Errorf("Search(%q) truncated", text)
	}
	if got, want := hitsOf(snap, res), sortedHits(t, snap, want...); !slices.Equal(got, want) {
		t.Errorf("Search(%q) hits:\n got %v\nwant %v", text, got, want)
	}
}

// shown is what the view shows of a file: its kind and whether it is large.
type shown struct {
	Kind  source.FileKind
	Large bool
}

// checkShowable checks every match of a search for "needle" against Facts, the view's own source.
// A matched file must be text and not large, so the view shows every line, unless approx lists it:
// then its facts must be exactly as listed. approx holds Q-47's approximations, where search
// decides per side and the view per file; a change to either side fails here, pointing at Q-47.
func checkShowable(t *testing.T, snap *source.Snapshot, approx map[string]shown) {
	t.Helper()
	for _, m := range search(t, snap, "needle").Matches {
		path := snap.File(m.File).Path()
		facts, err := snap.Facts(t.Context(), m.File)
		if err != nil {
			t.Fatalf("Facts(%d): %v", m.File, err)
		}
		want, ok := approx[path]
		if !ok {
			want = shown{Kind: source.KindText}
		}
		if got := (shown{facts.Kind, facts.Large}); got != want {
			t.Errorf("%q matched with view %+v, want %+v", path, got, want)
		}
	}
}

// TestSearchSides covers which side is searched: the new side of added, modified and renamed
// files, the old side of a deleted file, and never an unchanged file.
func TestSearchSides(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("mod.txt", "keep\nplain\n")
	r.write("del.txt", "a\nb\nneedle in deleted\n")
	r.write("old-only.txt", numberedFile("stable", 20)+"needle before the rename\n")
	r.write("new-too.txt", numberedFile("steady", 20)+"plain before the rename\n")
	r.write("unchanged.txt", "needle unchanged\n")
	r.commit("base")
	r.write("mod.txt", "keep\nneedle modified\n")
	r.remove("del.txt")
	r.remove("old-only.txt")
	r.write("old-only-moved.txt", numberedFile("stable", 20)+"plain after the rename\n")
	r.remove("new-too.txt")
	r.write("new-too-moved.txt", numberedFile("steady", 20)+"needle after the rename\n")
	r.write("add.txt", "first\nneedle added\n")
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	for _, p := range []string{"old-only-moved.txt", "new-too-moved.txt"} {
		if f := snap.File(fileIndex(t, snap, p)); f.Status != source.StatusRenamed {
			t.Fatalf("fixture: %s has status %c, want a rename", p, f.Status)
		}
	}
	checkHits(t, snap, "needle",
		hit{"mod.txt", true, 2},
		hit{"del.txt", false, 3},
		hit{"new-too-moved.txt", true, 21},
		hit{"add.txt", true, 2},
	)
}

// TestSearchReadsTheSnapshot covers that a search reads the snapshot's trees: worktree edits made
// after Resolve change nothing, for an uncommitted and a last-commit snapshot.
func TestSearchReadsTheSnapshot(t *testing.T) {
	t.Parallel()
	t.Run("uncommitted", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.write("a.txt", "a\n")
		r.write("c.txt", "c\n")
		r.commit("base")
		r.write("a.txt", "a\nneedle a\n")
		r.write("b.txt", "needle b\n")
		snap := resolve(t, r.dir, source.Uncommitted())
		r.write("a.txt", "a\n")
		r.write("b.txt", "b\nneedle b moved\n")
		r.write("c.txt", "needle c\n")
		checkHits(t, snap, "needle", hit{"a.txt", true, 2}, hit{"b.txt", true, 1})
	})
	t.Run("last with a dirty worktree", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.write("a.txt", "a\n")
		r.commit("base")
		r.write("a.txt", "a\nneedle a\n")
		r.commit("head")
		r.write("a.txt", "needle a\nmoved\n")
		snap := resolve(t, r.dir, source.Last())
		r.write("a.txt", "a\n")
		checkHits(t, snap, "needle", hit{"a.txt", true, 2})
	})
}

// TestSearchPathNames covers path mapping: names that a glob, magic pathspec, option or a split
// at ':' or LF would misread each map to their own file. Unchanged decoys that such a misreading
// would select hold the needle too.
func TestSearchPathNames(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	blob := func(content string) string {
		return strings.TrimSpace(string(r.gitInStdin(content, "hash-object", "-w", "--stdin")))
	}
	add := func(path, content string) {
		r.git("update-index", "--add", "--cacheinfo", "100644,"+blob(content)+","+path)
	}
	add("x", "needle decoy x\n")
	add("abc.txt", "needle decoy abc\n")
	r.git("commit", "-q", "--no-verify", "-m", "base")
	names := []string{"sp ace.txt", "a:b.txt", "[x].txt", "*.txt", ":(glob)x", "-lead.txt", "caf\xe9.txt", "nl\nname.txt"}
	var want []hit
	for k, p := range names {
		add(p, fmt.Sprintf("needle %d\n", k))
		want = append(want, hit{p, true, 1})
	}
	r.git("commit", "-q", "--no-verify", "-m", "head")
	snap := resolve(t, r.dir, source.Last())
	if snap.FileCount() != len(names) {
		t.Fatalf("fixture: %d changed files, want %d", snap.FileCount(), len(names))
	}
	checkHits(t, snap, "needle", want...)
	for _, m := range search(t, snap, "needle").Matches {
		k := slices.Index(names, snap.File(m.File).Path())
		if want := fmt.Sprintf("needle %d", k); window(m) != want {
			t.Errorf("%q: text %q, want %q", snap.File(m.File).Path(), window(m), want)
		}
	}
}

// TestSearchTextSemantics covers the match rule: a fixed string, ASCII smart-case, whitespace
// kept, and needles that look like options or patterns taken literally.
func TestSearchTextSemantics(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	r.write("t.txt", strings.Join([]string{
		"FOO upper",
		"foo lower",
		"abc",
		"a.c dot",
		"x -xx dash",
		"a foo spaced",
		"afoo joined",
		`star * back \ help --help`,
		"",
	}, "\n"))
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	at := func(lines ...int) []hit {
		var out []hit
		for _, l := range lines {
			out = append(out, hit{"t.txt", true, l})
		}
		return out
	}
	for _, tc := range []struct {
		needle string
		want   []hit
	}{
		{"foo", at(1, 2, 6, 7)},
		{"Foo", nil},
		{"FOO", at(1)},
		{"a.c", at(4)},
		{"-xx", at(5)},
		{" foo", at(6)},
		{"* b", at(8)},
		{`\ h`, at(8)},
		{"--help", at(8)},
	} {
		checkHits(t, snap, tc.needle, tc.want...)
	}
}

// sized returns content of exactly n bytes: line, then lines of fill, which differs per file so
// rename detection pairs none of them.
func sized(line string, fill byte, n int) string {
	var b strings.Builder
	b.WriteString(line)
	for b.Len() < n {
		b.WriteString(strings.Repeat(string(fill), min(79, n-b.Len()-1)))
		b.WriteByte('\n')
	}
	return b.String()
}

// TestSearchSkips covers the sides a search skips (binary, over 1 MiB) and keeps (exactly 1 MiB,
// a NUL past git's binary check, a small new side of a large file, over 20,000 lines), with
// every other file still matched and no error.
func TestSearchSkips(t *testing.T) {
	t.Parallel()
	const mib = 1 << 20
	r := newRepo(t)
	r.write("shrunk.txt", sized("plain\n", 'p', mib+10))
	r.write("deleted-big.txt", sized("needle deleted big\n", 'd', mib+10))
	r.commit("base")
	r.write("ok.txt", "needle ok\n")
	r.write("binary.dat", "\x00needle binary\n")
	r.write("late-nul.txt", strings.Repeat("a\n", 4001)+"needle\x00tail\n")
	r.write("exact.txt", sized("needle exact\n", 'e', mib))
	r.write("over.txt", sized("needle over\n", 'o', mib+1))
	r.write("shrunk.txt", "needle shrunk\n")
	r.remove("deleted-big.txt")
	r.write("many-lines.txt", strings.Repeat("l\n", 20_000)+"needle many\n")
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	checkHits(t, snap, "needle",
		hit{"ok.txt", true, 1},
		hit{"late-nul.txt", true, 4002},
		hit{"exact.txt", true, 1},
		hit{"shrunk.txt", true, 1},
		hit{"many-lines.txt", true, 20_001},
	)
	for _, m := range search(t, snap, "needle").Matches {
		if snap.File(m.File).Path() == "late-nul.txt" && window(m) != "needle\x00tail" {
			t.Errorf("late-nul.txt: text %q, want the line with its NUL", window(m))
		}
	}
	checkShowable(t, snap, map[string]shown{
		"many-lines.txt": {source.KindText, true},
		"exact.txt":      {source.KindText, true},
		"shrunk.txt":     {source.KindText, true},
	})
}

// TestSearchSkipsMissingBlobs covers a blobless clone: files with a missing blob are skipped
// without a fetch or an error, and files whose blobs are present still match.
func TestSearchSkipsMissingBlobs(t *testing.T) {
	t.Parallel()
	src := newRepo(t)
	src.git("config", "uploadpack.allowFilter", "true")
	src.write("a.txt", "needle a\n")
	src.commit("base")
	src.write("a.txt", "needle a two\n")
	src.write("b.txt", "needle b\n")
	src.commit("head")

	dst := &repo{t: t, dir: filepath.Join(t.TempDir(), "clone")}
	dst.gitIn(src.dir, nil, "clone", "-q", "--filter=blob:none", "--no-checkout", "file://"+src.dir, dst.dir)
	blob := func(content string) string {
		return strings.TrimSpace(string(dst.gitInStdin(content, "hash-object", "-w", "--stdin")))
	}
	// The commit is built from objects alone, so nothing reads, or fetches, a missing blob.
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "index"), "GIT_NO_LAZY_FETCH=1"}
	dst.gitIn(dst.dir, env, "read-tree", "HEAD")
	dst.gitIn(dst.dir, env, "update-index", "--add", "--cacheinfo", "100644,"+blob("needle a local\n")+",a.txt")
	dst.gitIn(dst.dir, env, "update-index", "--add", "--cacheinfo", "100644,"+blob("x\nneedle c local\n")+",c.txt")
	tree := strings.TrimSpace(string(dst.gitIn(dst.dir, env, "write-tree", "--missing-ok")))
	commit := strings.TrimSpace(string(dst.gitIn(dst.dir, env, "commit-tree", "-p", "HEAD", "-m", "local", tree)))
	dst.git("update-ref", "HEAD", commit)

	snap := resolve(t, dst.dir, source.Last())
	if got := missingPaths(snap); !got["a.txt"] || got["c.txt"] {
		t.Fatalf("fixture: missing %v, want a.txt only", got)
	}
	checkHits(t, snap, "needle", hit{"c.txt", true, 2})
	checkShowable(t, snap, nil)
}

// TestSearchSkipsSubmodules covers a changed gitlink: skipped without an error.
func TestSearchSkipsSubmodules(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("base")
	r.write("a.txt", "needle a\n")
	r.git("add", "-A")
	r.git("update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("ab", 20)+",sub")
	r.git("commit", "-q", "--no-verify", "-m", "head")
	snap := resolve(t, r.dir, source.Last())
	checkHits(t, snap, "needle", hit{"a.txt", true, 1})
	checkShowable(t, snap, nil)
}

// TestSearchSkipsModeOnly covers regular files that change only their mode, which the view shows
// without lines: skipped alone and renamed, while a mode change with an edit and a type change
// whose blob stays the same, both shown with lines, are searched. The type change goes from a
// symlink to a regular file because git grep reads no symlink blob.
func TestSearchSkipsModeOnly(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("mode.sh", "needle mode\n")
	r.write("moving.sh", numberedFile("move", 20)+"needle moved\n")
	r.write("edited.sh", "needle edited\n")
	// A symlink whose target is exactly the later file's content, so the blob stays the same.
	if err := os.Symlink("needle link", r.path("link")); err != nil {
		t.Fatal(err)
	}
	r.commit("base")
	chmod := func(p string) {
		t.Helper()
		if err := os.Chmod(r.path(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	chmod("mode.sh")
	if err := os.Rename(r.path("moving.sh"), r.path("moved.sh")); err != nil {
		t.Fatal(err)
	}
	chmod("moved.sh")
	r.write("edited.sh", "needle edited\nmore\n")
	chmod("edited.sh")
	r.remove("link")
	r.write("link", "needle link")
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	for p, want := range map[string]source.Status{"moved.sh": source.StatusRenamed, "link": source.StatusType} {
		if f := snap.File(fileIndex(t, snap, p)); f.Status != want {
			t.Fatalf("fixture: %s has status %c, want %c", p, f.Status, want)
		}
	}
	if facts, err := snap.Facts(t.Context(), fileIndex(t, snap, "link")); err != nil || facts.Kind != source.KindText {
		t.Fatalf("fixture: link has kind %v, %v; want text", facts.Kind, err)
	}
	checkHits(t, snap, "needle", hit{"edited.sh", true, 1}, hit{"link", true, 1})
	checkShowable(t, snap, nil)
}

// TestSearchOtherSideBinary covers Q-47's approximation for binary sides: search decides per
// searched side, so a text side is listed even when the other side makes the view show the file
// as binary, while a binary searched side, new or deleted, is not.
func TestSearchOtherSideBinary(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("bin-to-text.txt", "bin\x00ary\n")
	r.write("text-to-bin.txt", "needle old\n")
	r.write("deleted.bin", "\x00needle deleted\n")
	r.commit("base")
	r.write("bin-to-text.txt", "needle in text\n")
	r.write("text-to-bin.txt", "\x00needle new\n")
	r.remove("deleted.bin")
	r.write("ok.txt", "needle ok\n")
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	checkHits(t, snap, "needle", hit{"bin-to-text.txt", true, 1}, hit{"ok.txt", true, 1})
	checkShowable(t, snap, map[string]shown{"bin-to-text.txt": {Kind: source.KindBinary}})
}

// TestSearchSkipsSymlinks covers that symlink sides are not searched (REQ-NAV-007): neither an
// added symlink nor a regular file replaced by one is listed, though the view shows their targets
// as text lines.
func TestSearchSkipsSymlinks(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("became-link", "plain\n")
	r.commit("base")
	r.remove("became-link")
	for p, target := range map[string]string{"became-link": "needle became", "added-link": "needle added"} {
		if err := os.Symlink(target, r.path(p)); err != nil {
			t.Fatal(err)
		}
	}
	r.write("ok.txt", "needle ok\n")
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	for _, p := range []string{"became-link", "added-link"} {
		if facts, err := snap.Facts(t.Context(), fileIndex(t, snap, p)); err != nil || facts.Kind != source.KindText {
			t.Fatalf("fixture: %s has kind %v, %v; want text", p, facts.Kind, err)
		}
	}
	checkHits(t, snap, "needle", hit{"ok.txt", true, 1})
}

// TestSearchModeOnlyOutsideCap covers that mode-only files use up none of the 500 matches: more
// of them than the cap, all holding the needle, leave an edited file matched and the result whole.
func TestSearchModeOnlyOutsideCap(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	for i := range contractMaxMatches + 1 {
		r.write(fmt.Sprintf("m%03d.sh", i), fmt.Sprintf("needle %d\n", i))
	}
	r.write("zz-edited.txt", "x\n")
	r.commit("base")
	for i := range contractMaxMatches + 1 {
		if err := os.Chmod(r.path(fmt.Sprintf("m%03d.sh", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.write("zz-edited.txt", "x\nneedle edited\n")
	r.commit("head")
	checkHits(t, resolve(t, r.dir, source.Last()), "needle", hit{"zz-edited.txt", true, 2})
}

// TestSearchIgnoreCase covers path matching under core.ignoreCase, the macOS default: an
// unchanged file whose path differs from a changed one only in case is not searched.
func TestSearchIgnoreCase(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.git("config", "core.ignoreCase", "true")
	// The macOS filesystem cannot hold both names, so the trees are built in the index.
	blob := func(content string) string {
		return strings.TrimSpace(string(r.gitInStdin(content, "hash-object", "-w", "--stdin")))
	}
	r.git("update-index", "--add", "--cacheinfo", "100644,"+blob("needle unchanged\n")+",gen.go")
	r.git("update-index", "--add", "--cacheinfo", "100644,"+blob("package x\n")+",Gen.go")
	r.git("commit", "-q", "--no-verify", "-m", "base")
	r.git("update-index", "--cacheinfo", "100644,"+blob("package x\n// needle changed\n")+",Gen.go")
	r.git("commit", "-q", "--no-verify", "-m", "head")
	snap := resolve(t, r.dir, source.Last())
	checkHits(t, snap, "needle", hit{"Gen.go", true, 2})
}

// TestSearchBatches covers more pathspecs than one git command takes: every file is searched
// and mapped to its index.
func TestSearchBatches(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	long := strings.Repeat("n", 70)
	var want []hit
	for i := range 1200 {
		p := fmt.Sprintf("%04d-%s.txt", i, long)
		if i%3 == 0 {
			r.write(p, fmt.Sprintf("x\nneedle %d\n", i))
			want = append(want, hit{p, true, 2})
		} else {
			r.write(p, fmt.Sprintf("x %d\n", i))
		}
	}
	r.commit("head")
	checkHits(t, resolve(t, r.dir, source.Last()), "needle", want...)
}

// TestSearchMatchCap covers the 500-match limit: more matches give the first 500 lines and Truncated,
// exactly 500 give all of them and no truncation, and the same truncated search returns the same
// result.
func TestSearchMatchCap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		lines     int
		truncated bool
	}{
		{600, true},
		{contractMaxMatches, false},
	} {
		t.Run(fmt.Sprint(tc.lines), func(t *testing.T) {
			t.Parallel()
			r := newRepo(t)
			r.write("keep.txt", "base\n")
			r.commit("base")
			r.write("n.txt", numberedFile("needle", tc.lines))
			r.commit("head")
			snap := resolve(t, r.dir, source.Last())
			res := search(t, snap, "needle")
			if len(res.Matches) != contractMaxMatches || res.Truncated != tc.truncated {
				t.Fatalf("%d matches, truncated %t; want %d, %t", len(res.Matches), res.Truncated, contractMaxMatches, tc.truncated)
			}
			for k, m := range res.Matches {
				if m.Line != k+1 {
					t.Fatalf("match %d is line %d; want the first 500 lines in order", k, m.Line)
				}
			}
			if again := search(t, snap, "needle"); !reflect.DeepEqual(again, res) {
				t.Error("the same search returned a different result")
			}
		})
	}
}

// TestSearchTruncatedFavoursNewSides covers REQ-NAV-007's truncation order: files with a new side
// are searched before deleted files, so a deleted file that comes first in changed-file order loses
// its matches when the cap is reached in a later file.
func TestSearchTruncatedFavoursNewSides(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a-deleted.txt", "needle 1\nneedle 2\nneedle 3\n")
	r.commit("base")
	r.remove("a-deleted.txt")
	r.write("b-big.txt", numberedFile("needle", contractMaxMatches))
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	if fileIndex(t, snap, "a-deleted.txt") > fileIndex(t, snap, "b-big.txt") {
		t.Fatal("fixture: the deleted file must come first")
	}
	res := search(t, snap, "needle")
	if !res.Truncated || len(res.Matches) != contractMaxMatches {
		t.Fatalf("%d matches, truncated %t; want %d, truncated", len(res.Matches), res.Truncated, contractMaxMatches)
	}
	for k, h := range hitsOf(snap, res) {
		if want := (hit{"b-big.txt", true, k + 1}); h != want {
			t.Fatalf("match %d is %v; want %v", k, h, want)
		}
	}
}

// TestSearchMatchCapAcrossBatches covers the match limit reached in the second of three batches:
// exactly 500 matches, in order, and no third git grep.
func TestSearchMatchCapAcrossBatches(t *testing.T) {
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	long := strings.Repeat("n", 70)
	for i := range 2500 {
		content := fmt.Sprintf("x %d\n", i)
		if i%3 == 0 {
			content = fmt.Sprintf("needle %d\n", i)
		}
		r.write(fmt.Sprintf("%04d-%s.txt", i, long), content)
	}
	r.commit("head")
	logPath := logGitPIDs(t, `*" --column "*`)
	snap := resolve(t, r.dir, source.Last())
	res := search(t, snap, "needle")
	if len(res.Matches) != contractMaxMatches || !res.Truncated {
		t.Fatalf("%d matches, truncated %t; want %d, true", len(res.Matches), res.Truncated, contractMaxMatches)
	}
	// Batches hold about 800 of these paths, so the first holds about 270 matches.
	if last := res.Matches[len(res.Matches)-1].File; last < 810 {
		t.Fatalf("the last match is file %d; the fixture needs it in the second batch", last)
	}
	if n := logLines(t, logPath); n != 2 {
		t.Errorf("%d git grep runs, want 2", n)
	}
}

// TestSearchOutputCap covers the 16 MiB output limit: a partial result with Truncated, no git
// process left, and a working next search.
func TestSearchOutputCap(t *testing.T) {
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	for i := range 20 {
		r.write(fmt.Sprintf("big%02d.txt", i), strings.Repeat("x", 450<<10)+"needle"+strings.Repeat("y", 450<<10)+"\n")
	}
	r.write("small.txt", "needle small\n")
	r.commit("head")
	logPath := logGitPIDs(t, `*" --column "*`)
	snap := resolve(t, r.dir, source.Last())
	res := search(t, snap, "needle")
	if !res.Truncated || len(res.Matches) == 0 || len(res.Matches) >= contractMaxMatches {
		t.Fatalf("%d matches, truncated %t; want some, and truncated", len(res.Matches), res.Truncated)
	}
	// The kept matches are the files found first: a prefix of big00, big01, …, without small.txt.
	for k, h := range hitsOf(snap, res) {
		if want := (hit{fmt.Sprintf("big%02d.txt", k), true, 1}); h != want {
			t.Fatalf("match %d is %v; want %v", k, h, want)
		}
	}
	processesGone(t, readPIDs(t, logPath))
	search(t, snap, "small")
}

// TestSearchOutputCapAcrossBatches covers that the 16 MiB output limit counts the whole search,
// not each git grep: two batches of about 10 MiB of matching lines each give a truncated result
// that keeps the first batch's matches and part of the second's.
func TestSearchOutputCapAcrossBatches(t *testing.T) {
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	// Paths of about 930 bytes put about 70 in a 64 KiB batch, so 130 files make two batches. They
	// are longer than macOS allows on disk, so the commit is built from objects alone.
	add := func(path, content string) {
		blob := strings.TrimSpace(string(r.gitInStdin(content, "hash-object", "-w", "--stdin")))
		r.git("update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
	}
	dir := strings.Repeat("d", 230) + "/" + strings.Repeat("e", 230) + "/" + strings.Repeat("f", 230) + "/" + strings.Repeat("g", 230)
	const files, bigEach = 130, 10
	var big []string
	for i := range files {
		p := fmt.Sprintf("%s/%03d.txt", dir, i)
		if i < bigEach || i >= files-bigEach {
			// One line of about 1,000,000 bytes, under the 1 MiB large-file limit.
			add(p, fmt.Sprintf("needle %d ", i)+strings.Repeat("x", 1_000_000)+"\n")
			big = append(big, p)
		} else {
			add(p, fmt.Sprintf("x %d\n", i))
		}
	}
	r.git("commit", "-q", "--no-verify", "-m", "head")
	logPath := logGitPIDs(t, `*" --column "*`)
	snap := resolve(t, r.dir, source.Last())
	res := search(t, snap, "needle")
	if n := logLines(t, logPath); n != 2 {
		t.Fatalf("fixture: %d git grep runs, want 2", n)
	}
	if !res.Truncated || len(res.Matches) <= bigEach || len(res.Matches) >= 2*bigEach {
		t.Fatalf("%d matches, truncated %t; want more than %d, fewer than %d, and truncated", len(res.Matches), res.Truncated, bigEach, 2*bigEach)
	}
	for k, h := range hitsOf(snap, res) {
		if want := (hit{big[k], true, 1}); h != want {
			t.Fatalf("match %d is %v; want %v", k, h, want)
		}
	}
}

// TestSearchGitFailure covers Q-47's rule that a git failure is an error, not truncation: a
// failing git grep gives the error and no result, also after an earlier batch matched. Not
// parallel: the shim changes PATH.
func TestSearchGitFailure(t *testing.T) {
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	long := strings.Repeat("n", 70)
	for i := range 1200 {
		r.write(fmt.Sprintf("%04d-%s.txt", i, long), fmt.Sprintf("needle %d\n", i%100))
	}
	r.commit("head")
	dir := t.TempDir()
	first, failAll := filepath.Join(dir, "first"), filepath.Join(dir, "fail-all")
	// With fail-all every git grep fails; without it, the first runs and every later one fails.
	shimGit(t, `*" --column "*) if [ -e '`+failAll+`' ] || [ -e '`+first+`' ]; then echo 'fatal: boom' >&2; exit 2; fi; : > '`+first+`';;`)
	snap := resolve(t, r.dir, source.Last())
	check := func(t *testing.T) {
		t.Helper()
		res, err := snap.Search(t.Context(), newSearchText(t, "needle 7"))
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("Search with a failing git grep: %v; want git's error", err)
		}
		if res.Matches != nil || res.Truncated {
			t.Fatalf("Search with a failing git grep = %d matches, truncated %t; want no result", len(res.Matches), res.Truncated)
		}
	}
	t.Run("first batch", func(t *testing.T) {
		if err := os.WriteFile(failAll, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(failAll)
		check(t)
	})
	t.Run("second batch", func(t *testing.T) {
		if err := os.Remove(first); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		check(t)
	})
}

// TestSearchWindow covers the window kept of a long line: at most 496 bytes, the whole match,
// cut flags set where the line goes on, and cuts that never split a rune.
func TestSearchWindow(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("n", contractMaxTextBytes-6) + "needle"
	if len(long) != contractMaxTextBytes {
		t.Fatal("fixture: the long needle is not 256 bytes")
	}
	// Runes of 3 and 4 bytes on both sides, shifted by 0 to 6 bytes, put the window's cuts
	// inside runes at every phase.
	pad := strings.Repeat("€𝄞", 60)
	type line struct {
		name, text, needle string
		cutStart, cutEnd   bool
	}
	lines := []line{
		{"middle", strings.Repeat("a", 100<<10) + "needle" + strings.Repeat("b", 100<<10), "needle", true, true},
		{"long needle", strings.Repeat("a", 100<<10) + long + strings.Repeat("b", 100<<10), long, true, true},
		{"line start", "needle" + strings.Repeat("b", 1000), "needle", false, true},
		{"line end", strings.Repeat("a", 1000) + "needle", "needle", true, false},
	}
	for k := range 7 {
		lines = append(lines, line{fmt.Sprintf("runes %d", k), pad + strings.Repeat("a", k) + "needle" + strings.Repeat("b", k) + pad, "needle", true, true})
	}
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	for k, l := range lines {
		r.write(fmt.Sprintf("w%02d.txt", k), l.text+"\n")
	}
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	for k, l := range lines {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			var m *source.SearchMatch
			res := search(t, snap, l.needle)
			for j := range res.Matches {
				if snap.File(res.Matches[j].File).Path() == fmt.Sprintf("w%02d.txt", k) {
					m = &res.Matches[j]
				}
			}
			if m == nil {
				t.Fatal("no match")
			}
			if m.CutStart != l.cutStart || m.CutEnd != l.cutEnd {
				t.Errorf("cuts %t, %t; want %t, %t", m.CutStart, m.CutEnd, l.cutStart, l.cutEnd)
			}
			w := window(*m)
			if !utf8.ValidString(w) {
				t.Errorf("text %q is not valid UTF-8", w)
			}
			if !strings.Contains(l.text, w) {
				t.Error("the window is not part of the line")
			}
			if !m.CutStart && !strings.HasPrefix(l.text, w) || !m.CutEnd && !strings.HasSuffix(l.text, w) {
				t.Error("an uncut side does not reach the line's end")
			}
			// A cut side keeps 120 bytes of context (Q-47), less at most 3 to stay off a rune's
			// continuation bytes; ASCII context keeps exactly 120.
			for _, side := range []struct {
				name string
				cut  bool
				text string
			}{{"before", m.CutStart, m.Before}, {"after", m.CutEnd, m.After}} {
				lo := contractContext
				if strings.HasPrefix(l.name, "runes") {
					lo -= 3
				}
				if side.cut && (len(side.text) < lo || len(side.text) > contractContext) {
					t.Errorf("%s keeps %d bytes; want %d to %d", side.name, len(side.text), lo, contractContext)
				}
			}
			if l.name == "long needle" && len(w) != contractMaxMatchText {
				t.Errorf("window of %d bytes; want %d", len(w), contractMaxMatchText)
			}
		})
	}
}

// TestSearchOrder covers the result order: by file index then line, though deleted files are
// searched after the rest.
func TestSearchOrder(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a-deleted.txt", "needle 1\nx\nneedle 3\n")
	r.commit("base")
	r.remove("a-deleted.txt")
	r.write("b-added.txt", "needle 1\nneedle 2\n")
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	checkHits(t, snap, "needle",
		hit{"a-deleted.txt", false, 1}, hit{"a-deleted.txt", false, 3},
		hit{"b-added.txt", true, 1}, hit{"b-added.txt", true, 2},
	)
	if fileIndex(t, snap, "a-deleted.txt") > fileIndex(t, snap, "b-added.txt") {
		t.Fatal("fixture: the deleted file must come first")
	}
}

// TestSearchLineNumbers checks each match's line number against Lines, over line breaks git and
// rvw could count differently: CRLF, a lone CR, a form feed, U+2028, U+0085 and no final LF.
func TestSearchLineNumbers(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	r.write("crlf.txt", "a\r\nneedle 2\r\nb\r\nNEEDLE 4\r\n")
	r.write("cr.txt", "a\rb\nneedle 2\ra\nc\n")
	r.write("ff.txt", "a\fb\nneedle 2\fneedle\n")
	r.write("u2028.txt", "a b\nc\u0085d\nneedle 3\n")
	r.write("noeol.txt", "a\nb\nneedle 3")
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	for _, text := range []string{"needle", "NEEDLE"} {
		res := search(t, snap, text)
		if len(res.Matches) != map[string]int{"needle": 6, "NEEDLE": 1}[text] {
			t.Errorf("Search(%q): %d matches", text, len(res.Matches))
		}
		for _, m := range res.Matches {
			got, err := linesOf(t.Context(), snap, m.File, m.NewSide, m.Line, m.Line)
			if err != nil {
				t.Fatal(err)
			}
			line := got[0]
			if folds(text) {
				line = asciiLower(line)
			}
			if !strings.Contains(line, text) {
				t.Errorf("%s line %d is %q, which does not hold %q", snap.File(m.File).Path(), m.Line, got[0], text)
			}
		}
	}
}

// TestSearchFoldsASCIIOnly covers folding under the pinned locale: only ASCII letters fold,
// whatever the environment, so a match is always the needle's length.
func TestSearchFoldsASCIIOnly(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	r.write("f.txt", strings.Join([]string{"K-line", "ÉCOLE", "École", "école", "STRASSE", "élan", "ÉLAN", ""}, "\n"))
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	at := func(lines ...int) []hit {
		var out []hit
		for _, l := range lines {
			out = append(out, hit{"f.txt", true, l})
		}
		return out
	}
	for _, tc := range []struct {
		needle string
		want   []hit
	}{
		{"k-line", nil},
		{"école", at(4)},
		// No ASCII uppercase, so ASCII letters fold: "COLE" matches, É matches only É.
		{"École", at(2, 3)},
		{"straße", nil},
		{"élan", at(6)},
	} {
		checkHits(t, snap, tc.needle, tc.want...)
	}
}

// TestSearchCancellation covers a search whose context ends while git grep runs: the context's
// error and no result, no git process left, and a working next search. A context that is already
// cancelled wins over a match limit the search would reach. Not parallel: the shim changes PATH.
func TestSearchCancellation(t *testing.T) {
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	r.write("n.txt", numberedFile("needle", 600))
	r.commit("head")
	gate := filepath.Join(t.TempDir(), "gate")
	logPath := shimGit(t, `*" --column "*) echo $$ >> "$LOG"; if [ -e '`+gate+`' ]; then while [ -e '`+gate+`' ]; do sleep 0.05; done; exit 1; fi;;`)
	snap := resolve(t, r.dir, source.Last())

	t.Run("deadline", func(t *testing.T) {
		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(gate)
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		res, err := snap.Search(ctx, newSearchText(t, "needle"))
		if !errors.Is(err, context.DeadlineExceeded) || res.Matches != nil || res.Truncated {
			t.Fatalf("Search past its deadline = %+v, %v; want no result and DeadlineExceeded", res, err)
		}
		processesGone(t, readPIDs(t, logPath))
		if err := os.Remove(gate); err != nil {
			t.Fatal(err)
		}
		search(t, snap, "needle")
	})
	t.Run("cancelled over a match limit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		res, err := snap.Search(ctx, newSearchText(t, "needle"))
		if !errors.Is(err, context.Canceled) || res.Matches != nil || res.Truncated {
			t.Fatalf("cancelled Search = %d matches, truncated %t, %v; want no result and Canceled", len(res.Matches), res.Truncated, err)
		}
	})
}

// TestSearchBudget covers the 2 s budget: a search whose second git grep never finishes returns
// within the budget plus slack, keeping the first batch's matches and Truncated, leaves no git
// process, and the next search works. It runs once. Not parallel: the shim changes PATH.
func TestSearchBudget(t *testing.T) {
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	long := strings.Repeat("n", 70)
	var want []hit
	for i := range 1200 {
		p := fmt.Sprintf("%04d-%s.txt", i, long)
		if i < 10 {
			r.write(p, "needle\n")
			want = append(want, hit{p, true, 1})
		} else {
			r.write(p, fmt.Sprintf("x %d\n", i))
		}
	}
	r.commit("head")
	dir := t.TempDir()
	first, gate := filepath.Join(dir, "first"), filepath.Join(dir, "gate")
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The first git grep runs; while the gate exists, every later one blocks.
	logPath := shimGit(t, `*" --column "*) echo $$ >> "$LOG"; if [ -e '`+first+`' ] && [ -e '`+gate+`' ]; then while [ -e '`+gate+`' ]; do sleep 0.05; done; exit 1; fi; : > '`+first+`'; exec "$REAL_GIT" "$@";;`)
	snap := resolve(t, r.dir, source.Last())
	start := time.Now()
	res, err := snap.Search(t.Context(), newSearchText(t, "needle"))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Search past its budget: %v", err)
	}
	if elapsed < 1900*time.Millisecond || elapsed > 5*time.Second {
		t.Errorf("Search took %v; want about 2s", elapsed)
	}
	if got := hitsOf(snap, res); !res.Truncated || !slices.Equal(got, want) {
		t.Errorf("Search past its budget = %v, truncated %t; want %v, truncated", got, res.Truncated, want)
	}
	processesGone(t, readPIDs(t, logPath))
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	if res := search(t, snap, "needle"); res.Truncated || len(res.Matches) != len(want) {
		t.Errorf("next Search: %d matches, truncated %t; want %d", len(res.Matches), res.Truncated, len(want))
	}
}

// TestSearchZeroText covers SearchText not made by NewSearchText: refused.
func TestSearchZeroText(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("a")
	snap := resolve(t, r.dir, source.Last())
	if _, err := snap.Search(t.Context(), source.SearchText{}); !errors.Is(err, source.ErrInvalidSearchText) {
		t.Fatalf("Search(SearchText{}) = %v; want ErrInvalidSearchText", err)
	}
}

// TestNewSearchText covers the search-text rule (REQ-NAV-007): at least 3 runes, at most 256 bytes, valid
// UTF-8 without NUL, CR or LF.
func TestNewSearchText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text string
		ok         bool
	}{
		{"empty", "", false},
		{"2 runes", "ab", false},
		{"257 bytes", strings.Repeat("a", contractMaxTextBytes+1), false},
		{"86 three-byte runes", strings.Repeat("€", 86), false},
		{"NUL", "ab\x00c", false},
		{"LF", "ab\nc", false},
		{"CR", "ab\rc", false},
		{"invalid UTF-8", "ab\xffc", false},
		{"3 runes", "a€b", true},
		{"3 spaces", "   ", true},
		{"256 bytes", strings.Repeat("a", contractMaxTextBytes), true},
		{"85 three-byte runes", strings.Repeat("€", 85), true},
	} {
		_, err := source.NewSearchText(tc.text)
		if tc.ok && err != nil || !tc.ok && !errors.Is(err, source.ErrInvalidSearchText) {
			t.Errorf("%s: NewSearchText = %v; want ok %t", tc.name, err, tc.ok)
		}
	}
}
