package server_test

import (
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/source"
)

type sideDTO struct {
	Present bool `json:"present"`
	Lines   int  `json:"lines"`
}

type spanDTO struct {
	Class string `json:"c"`
	Text  string `json:"t"`
}

type lineDTO struct {
	T     string    `json:"t"`
	S     []spanDTO `json:"s"`
	W     [][2]int  `json:"w"`
	Old   int       `json:"old"`
	New   int       `json:"new"`
	NoEOL bool      `json:"n"`
}

type blockDTO struct {
	Ctx []lineDTO `json:"ctx"`
	Del []lineDTO `json:"del"`
	Add []lineDTO `json:"add"`
}

type gapDTO struct {
	From int `json:"from"`
	To   int `json:"to"`
}

type hunkDTO struct {
	GapBefore *gapDTO    `json:"gap_before"`
	Header    string     `json:"header"`
	Blocks    []blockDTO `json:"blocks"`
}

type fileDiffDTO struct {
	GapAfter    *gapDTO   `json:"gap_after"`
	Kind        string    `json:"kind"`
	Hunks       []hunkDTO `json:"hunks"`
	Old         sideDTO   `json:"old"`
	New         sideDTO   `json:"new"`
	Large       bool      `json:"large"`
	Highlighted bool      `json:"highlighted"`
}

type contextDTO struct {
	Lines       []lineDTO `json:"lines"`
	Highlighted bool      `json:"highlighted"`
}

func (f *fixture) fileDiff(i int) fileDiffDTO {
	f.t.Helper()
	r := f.do(http.MethodGet, "/api/file?i="+strconv.Itoa(i), "")
	f.mustStatus(r, http.StatusOK, fmt.Sprintf("GET /api/file?i=%d", i))
	var d fileDiffDTO
	decode(f.t, r.body, &d)
	return d
}

func (f *fixture) contextLines(i, from, to int) response {
	f.t.Helper()
	return f.do(http.MethodGet, fmt.Sprintf("/api/file/context?i=%d&from=%d&to=%d", i, from, to), "")
}

// largeText returns n lines of exactly 100 bytes each, newline included.
func largeText(n int) string {
	const width = 100
	var b strings.Builder
	b.Grow(n * width)
	for i := range n {
		s := fmt.Sprintf("%08d", i+1)
		b.WriteString(s)
		b.WriteString(strings.Repeat("x", width-len(s)-1))
		b.WriteByte('\n')
	}
	return b.String()
}

// addGitlink stages a submodule entry at path pointing at commit.
func (r *testRepo) addGitlink(path, commit string) {
	r.t.Helper()
	r.git("update-index", "--add", "--cacheinfo", "160000,"+commit+","+path)
}

// richRepo has a last commit with one change of every kind the UI presents differently.
func richRepo(t *testing.T) *testRepo {
	t.Helper()
	r := newTestRepo(t)
	big := largeText(12_000) // over LargeBytes
	r.write("mod.txt", numberedLines("line", 40))
	r.write("noeol.txt", "a\nb\nc\n")
	r.write("deleted.txt", "gone\n")
	r.write("old_name.txt", numberedLines("keep", 10))
	r.write("bin.dat", "\x00\x01\x02bin\n")
	r.write("big.txt", big)
	r.write("script.sh", "#!/bin/sh\necho hi\n")
	r.commit("base")
	base := r.git("rev-parse", "HEAD")

	mod := strings.Replace(numberedLines("line", 40), "line 5\n", "line five\n", 1)
	r.write("mod.txt", strings.Replace(mod, "line 30\n", "line thirty\n", 1))
	r.write("noeol.txt", "a\nb\nC")
	r.remove("deleted.txt")
	r.remove("old_name.txt")
	r.write("new_name.txt", strings.Replace(numberedLines("keep", 10), "keep 10\n", "keep ten\n", 1))
	r.write("bin.dat", "\x00\x01\x03bin\n")
	r.write("big.txt", strings.Replace(big, "00006000", "changed!", 1))
	if err := os.Chmod(filepath.Join(r.dir, "script.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.write("added.txt", "new\n")
	r.git("add", "-A")
	r.addGitlink("sub", base)
	r.git("commit", "-q", "-m", "head")
	return r
}

type pathPair struct{ old, new string }

// gitNameStatus parses `git diff --name-status -z -M` into path pair -> status letter.
func gitNameStatus(t *testing.T, r *testRepo, base, head string) map[pathPair]string {
	t.Helper()
	fields := strings.Split(strings.TrimSuffix(r.git("diff", "--name-status", "-z", "-M", base, head), "\x00"), "\x00")
	out := map[pathPair]string{}
	for k := 0; k < len(fields); {
		status := fields[k][:1]
		switch status {
		case "R", "C":
			out[pathPair{fields[k+1], fields[k+2]}] = status
			k += 3
		case "A":
			out[pathPair{"", fields[k+1]}] = status
			k += 2
		case "D":
			out[pathPair{fields[k+1], ""}] = status
			k += 2
		default:
			out[pathPair{fields[k+1], fields[k+1]}] = status
			k += 2
		}
	}
	return out
}

// TestFileListMatchesGit checks that the session's files and statuses are git's.
func TestFileListMatchesGit(t *testing.T) {
	t.Parallel()
	repo := richRepo(t)
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	s := f.session()

	if s.Source != "last commit" {
		t.Errorf("source %q, want %q", s.Source, "last commit")
	}
	if s.RenameDetection != "full" {
		t.Errorf("rename_detection %q with every blob present, want full", s.RenameDetection)
	}
	got := map[pathPair]string{}
	for _, fl := range s.Files {
		got[pathPair{fl.OldPath, fl.NewPath}] = fl.Status
	}
	want := gitNameStatus(t, repo, "HEAD~1", "HEAD")
	if len(got) != len(s.Files) {
		t.Errorf("duplicate entries in the file list: %+v", s.Files)
	}
	for k, st := range want {
		if got[k] != st {
			t.Errorf("file %q -> %q: status %q, want %q", k.old, k.new, got[k], st)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("file %q -> %q is not in git's diff", k.old, k.new)
		}
	}
}

// gitHunkHeaders returns the hunk headers git prints for one file of the last commit.
func gitHunkHeaders(t *testing.T, r *testRepo, paths ...string) []string {
	t.Helper()
	args := append([]string{"diff", "--no-color", "--no-ext-diff", "-M", "HEAD~1", "HEAD", "--"}, paths...)
	var out []string
	for l := range strings.SplitSeq(r.git(args...), "\n") {
		if strings.HasPrefix(l, "@@ ") {
			out = append(out, l)
		}
	}
	return out
}

// sideText rebuilds one side's content from lines and whether the last line ends without EOL.
func sideText(lines []string, noEOL bool) string {
	if len(lines) == 0 {
		return ""
	}
	s := strings.Join(lines, "\n")
	if !noEOL {
		s += "\n"
	}
	return s
}

// reconstruct rebuilds both sides of file i from its hunks and the context lines of its gaps,
// checking that every line number continues the previous one.
func (f *fixture) reconstruct(i int, d fileDiffDTO) (oldText, newText string) {
	f.t.Helper()
	t := f.t
	var oldLines, newLines []string
	var oldNoEOL, newNoEOL bool
	gap := func(g *gapDTO, where string) {
		if g == nil {
			return
		}
		if g.From != len(newLines)+1 {
			t.Fatalf("file %d %s gap %+v does not continue at new %d", i, where, *g, len(newLines)+1)
		}
		r := f.contextLines(i, g.From, g.To)
		f.mustStatus(r, http.StatusOK, "context for "+where+" gap")
		var c contextDTO
		decode(t, r.body, &c)
		if len(c.Lines) != g.To-g.From+1 {
			t.Fatalf("file %d %s gap %+v: %d context lines", i, where, *g, len(c.Lines))
		}
		for _, l := range c.Lines {
			if l.Old != len(oldLines)+1 || l.New != len(newLines)+1 {
				t.Fatalf("file %d context line %+v, want old %d new %d", i, l, len(oldLines)+1, len(newLines)+1)
			}
			oldLines, newLines = append(oldLines, l.T), append(newLines, l.T)
		}
		oldNoEOL, newNoEOL = false, false
	}
	for h, hk := range d.Hunks {
		gap(hk.GapBefore, fmt.Sprintf("hunk %d", h))
		for _, b := range hk.Blocks {
			for _, l := range b.Ctx {
				if l.Old != len(oldLines)+1 || l.New != len(newLines)+1 {
					t.Fatalf("file %d context line %+v, want old %d new %d", i, l, len(oldLines)+1, len(newLines)+1)
				}
				oldLines, newLines = append(oldLines, l.T), append(newLines, l.T)
				oldNoEOL, newNoEOL = l.NoEOL, l.NoEOL
			}
			for _, l := range b.Del {
				if l.Old != len(oldLines)+1 || l.New != 0 {
					t.Fatalf("file %d deleted line %+v, want old %d", i, l, len(oldLines)+1)
				}
				oldLines = append(oldLines, l.T)
				oldNoEOL = l.NoEOL
			}
			for _, l := range b.Add {
				if l.New != len(newLines)+1 || l.Old != 0 {
					t.Fatalf("file %d added line %+v, want new %d", i, l, len(newLines)+1)
				}
				newLines = append(newLines, l.T)
				newNoEOL = l.NoEOL
			}
		}
	}
	gap(d.GapAfter, "trailing")
	return sideText(oldLines, oldNoEOL), sideText(newLines, newNoEOL)
}

// TestDiffReconstructsBothSides checks that for each text file, the hunks plus the expandable gaps
// reproduce both blobs exactly, and the hunk headers are git's.
func TestDiffReconstructsBothSides(t *testing.T) {
	t.Parallel()
	repo := richRepo(t)
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	s := f.session()

	for _, tc := range []struct{ old, new string }{
		{"mod.txt", "mod.txt"},
		{"noeol.txt", "noeol.txt"},
		{"old_name.txt", "new_name.txt"},
		{"", "added.txt"},
		{"deleted.txt", ""},
	} {
		display := tc.new
		if display == "" {
			display = tc.old
		}
		i := s.fileIndex(t, display)
		d := f.fileDiff(i)
		if d.Kind != "text" || d.Large {
			t.Errorf("%s: kind %q large %v, want a text file that is not large", display, d.Kind, d.Large)
			continue
		}
		var wantOld, wantNew string
		if tc.old != "" {
			wantOld = repo.blob("HEAD~1", tc.old)
		}
		if tc.new != "" {
			wantNew = repo.blob("HEAD", tc.new)
		}
		gotOld, gotNew := f.reconstruct(i, d)
		if gotOld != wantOld {
			t.Errorf("%s: old side rebuilt as %q, want %q", display, gotOld, wantOld)
		}
		if gotNew != wantNew {
			t.Errorf("%s: new side rebuilt as %q, want %q", display, gotNew, wantNew)
		}
		if d.Old.Present != (tc.old != "") || d.New.Present != (tc.new != "") {
			t.Errorf("%s: sides present old=%v new=%v", display, d.Old.Present, d.New.Present)
		}
		var headers []string
		for _, h := range d.Hunks {
			headers = append(headers, h.Header)
		}
		paths := slices.DeleteFunc([]string{tc.old, tc.new}, func(p string) bool { return p == "" })
		if want := gitHunkHeaders(t, repo, paths...); !slices.Equal(headers, want) {
			t.Errorf("%s: hunk headers %q, want git's %q", display, headers, want)
		}
	}
}

// TestNonTextAndLargeFiles checks that binary, submodule and mode-only files have no line diff; a
// large file shows git's hunks without highlighting or expansion.
func TestNonTextAndLargeFiles(t *testing.T) {
	t.Parallel()
	repo := richRepo(t)
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	s := f.session()

	for path, kind := range map[string]string{"bin.dat": "binary", "sub": "submodule", "script.sh": "mode"} {
		d := f.fileDiff(s.fileIndex(t, path))
		if d.Kind != kind {
			t.Errorf("%s: kind %q, want %q", path, d.Kind, kind)
		}
		if len(d.Hunks) != 0 || d.GapAfter != nil {
			t.Errorf("%s: %d hunks, gap_after %v; want no line diff", path, len(d.Hunks), d.GapAfter)
		}
	}

	i := s.fileIndex(t, "big.txt")
	d := f.fileDiff(i)
	if d.Kind != "text" || !d.Large || d.Highlighted {
		t.Errorf("big.txt: kind %q large %v highlighted %v; want large text without highlighting", d.Kind, d.Large, d.Highlighted)
	}
	if d.GapAfter != nil {
		t.Errorf("big.txt: gap_after %+v; a large file has no context expansion", *d.GapAfter)
	}
	var headers []string
	for _, h := range d.Hunks {
		headers = append(headers, h.Header)
		if h.GapBefore != nil {
			t.Errorf("big.txt: gap_before %+v; a large file has no context expansion", *h.GapBefore)
		}
		for _, b := range h.Blocks {
			for _, l := range slices.Concat(b.Ctx, b.Del, b.Add) {
				if len(l.S) != 0 {
					t.Errorf("big.txt line %+v has highlight spans", l)
				}
			}
		}
	}
	if want := gitHunkHeaders(t, repo, "big.txt"); !slices.Equal(headers, want) {
		t.Errorf("big.txt: hunk headers %q, want git's %q", headers, want)
	}
	if r := f.contextLines(i, 1, 3); r.status != http.StatusConflict {
		t.Errorf("big.txt context: status %d, want 409", r.status)
	}
}

type countDTO struct {
	I       int  `json:"i"`
	Added   int  `json:"added"`
	Deleted int  `json:"deleted"`
	Binary  bool `json:"binary"`
}

type statusDTO struct {
	Counts      []countDTO `json:"counts"`
	CountsError string     `json:"counts_error"`
	Next        int        `json:"next"`
	CountsDone  bool       `json:"counts_done"`
	HeadMoved   bool       `json:"head_moved"`
}

// gitNumStat parses `git diff --numstat -z -M` into path pair -> count.
func gitNumStat(t *testing.T, r *testRepo) map[pathPair]countDTO {
	t.Helper()
	fields := strings.Split(strings.TrimSuffix(r.git("diff", "--numstat", "-z", "-M", "HEAD~1", "HEAD"), "\x00"), "\x00")
	out := map[pathPair]countDTO{}
	for k := 0; k < len(fields); k++ {
		parts := strings.SplitN(fields[k], "\t", 3)
		var c countDTO
		if parts[0] == "-" {
			c.Binary = true
		} else {
			c.Added, _ = strconv.Atoi(parts[0])
			c.Deleted, _ = strconv.Atoi(parts[1])
		}
		if parts[2] == "" { // rename: old and new paths follow
			out[pathPair{fields[k+1], fields[k+2]}] = c
			k += 2
			continue
		}
		out[pathPair{parts[2], parts[2]}] = c
	}
	return out
}

// TestLineCountsMatchNumstat checks that the background counts reach completion and equal `git diff
// --numstat` for every file.
func TestLineCountsMatchNumstat(t *testing.T) {
	t.Parallel()
	repo := richRepo(t)
	f := newFixture(t, repo, fixtureOptions{})
	f.serveRun()
	s := f.session()

	got := f.lineCounts()

	want := gitNumStat(t, repo)
	for i, fl := range s.Files {
		key := pathPair{fl.OldPath, fl.NewPath}
		if key.old == "" {
			key.old = key.new
		}
		if key.new == "" {
			key.new = key.old
		}
		w, ok := want[key]
		if !ok {
			t.Errorf("file %d %+v missing from git numstat %v", i, key, want)
			continue
		}
		w.I = i
		if got[i] != w {
			t.Errorf("file %d (%s): counts %+v, want %+v", i, key.new, got[i], w)
		}
	}
	if len(got) != len(s.Files) {
		t.Errorf("%d counts for %d files", len(got), len(s.Files))
	}
}

// contextRepo has files at the edges of context expansion.
func contextRepo(t *testing.T) *testRepo {
	t.Helper()
	r := newTestRepo(t)
	// Exactly LargeBytes without a final newline is not large: 10,485 lines of 100 bytes and a
	// last line of 76 bytes. Both sides are that size, so its gaps are expandable.
	exact := largeText(10_485) + strings.Repeat("z", 76)
	if len(exact) != source.LargeBytes {
		t.Fatalf("exact.txt is %d bytes", len(exact))
	}
	r.write("ctx.txt", numberedLines("line", 40))
	r.write("gone.txt", "gone\n")
	r.write("exact.txt", strings.Replace(exact, "00000001", "0000000a", 1))
	r.commit("base")
	base := r.git("rev-parse", "HEAD")
	r.write("ctx.txt", strings.Replace(numberedLines("line", 40), "line 20\n", "line twenty\n", 1))
	r.remove("gone.txt")
	r.write("exact.txt", exact)
	r.write("over.txt", exact+"z")
	r.write("added.txt", numberedLines("added", 10))
	r.git("add", "-A")
	r.addGitlink("sub", base)
	r.git("commit", "-q", "-m", "head")
	return r
}

// TestContextBounds checks that a context request is rejected unless its file exists and its range
// is well-formed and inside one gap.
func TestContextBounds(t *testing.T) {
	t.Parallel()
	repo := contextRepo(t)
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	s := f.session()
	ctx := s.fileIndex(t, "ctx.txt")

	for _, c := range []struct {
		query string
		want  int
	}{
		{fmt.Sprintf("i=%d&from=0&to=3", ctx), http.StatusBadRequest},
		{fmt.Sprintf("i=%d&from=5&to=4", ctx), http.StatusBadRequest},
		{fmt.Sprintf("i=%d&from=a&to=4", ctx), http.StatusBadRequest},
		{fmt.Sprintf("i=%d&from=1", ctx), http.StatusBadRequest},
		{fmt.Sprintf("i=%d&from=a&to=", ctx), http.StatusBadRequest},
		// Well-formed ranges that are not inside one gap.
		{fmt.Sprintf("i=%d&from=1&to=1000000", ctx), http.StatusUnprocessableEntity},
		{fmt.Sprintf("i=%d&from=16&to=18", ctx), http.StatusUnprocessableEntity}, // into the hunk
		{fmt.Sprintf("i=%d&from=20&to=20", ctx), http.StatusUnprocessableEntity}, // the changed line
		{fmt.Sprintf("i=%d&from=10&to=30", ctx), http.StatusUnprocessableEntity}, // across the hunk
		{fmt.Sprintf("i=%d&from=41&to=41", ctx), http.StatusUnprocessableEntity}, // past the end
		{"i=99&from=1&to=3", http.StatusNotFound},
		{"i=-1&from=1&to=3", http.StatusNotFound},
		{"from=1&to=3", http.StatusNotFound},
	} {
		r := f.do(http.MethodGet, "/api/file/context?"+c.query, "")
		if r.status != c.want {
			t.Errorf("context?%s: status %d, want %d; body %q", c.query, r.status, c.want, r.body)
		}
	}

	r := f.contextLines(ctx, 2, 4)
	f.mustStatus(r, http.StatusOK, "ctx.txt context 2..4")
	var c contextDTO
	decode(t, r.body, &c)
	var texts []string
	for k, l := range c.Lines {
		texts = append(texts, l.T)
		if l.Old != 2+k || l.New != 2+k {
			t.Errorf("ctx.txt context line %d numbered old %d new %d", 2+k, l.Old, l.New)
		}
	}
	if want := []string{"line 2", "line 3", "line 4"}; !slices.Equal(texts, want) {
		t.Errorf("ctx.txt context 2..4 = %q, want %q", texts, want)
	}

	// No gaps: a side is missing, the file is not text, or it is large.
	for _, path := range []string{"gone.txt", "added.txt", "sub", "over.txt"} {
		if r := f.contextLines(s.fileIndex(t, path), 1, 1); r.status != http.StatusConflict {
			t.Errorf("%s context: status %d, want 409; body %q", path, r.status, r.body)
		}
	}

	exact := s.fileIndex(t, "exact.txt")
	if d := f.fileDiff(exact); d.Large {
		t.Error("a side of exactly LargeBytes is reported large")
	}
	r = f.contextLines(exact, 10_485, 10_486)
	f.mustStatus(r, http.StatusOK, "exact.txt context at end of file")
	c = contextDTO{}
	decode(t, r.body, &c)
	if len(c.Lines) != 2 || c.Lines[1].T != strings.Repeat("z", 76) || c.Lines[1].New != 10_486 || c.Lines[1].Old != 10_486 {
		t.Errorf("exact.txt last lines = %+v, want line 10486 of 76 'z'", c.Lines)
	}
}

// lineCounts polls /api/status until the background line counts are done and returns them by file
// index. A count reported twice or a counts error fails the test.
func (f *fixture) lineCounts() map[int]countDTO {
	f.t.Helper()
	got := map[int]countDTO{}
	next := 0
	deadline := time.Now().Add(20 * time.Second)
	for {
		r := f.do(http.MethodGet, "/api/status?since="+strconv.Itoa(next), "")
		f.mustStatus(r, http.StatusOK, "GET /api/status")
		var st statusDTO
		decode(f.t, r.body, &st)
		for _, c := range st.Counts {
			if _, dup := got[c.I]; dup {
				f.t.Errorf("count for file %d reported twice", c.I)
			}
			got[c.I] = c
		}
		next = st.Next
		if st.CountsError != "" {
			f.t.Fatalf("counts_error %q", st.CountsError)
		}
		if st.CountsDone {
			return got
		}
		if time.Now().After(deadline) {
			f.t.Fatal("line counts did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// removeBlob deletes the loose object for rev:path. Git can read a missing new-side blob back from
// an unchanged worktree file, so a test that needs git to fail removes an old-side blob.
func (r *testRepo) removeBlob(rev, path string) {
	r.t.Helper()
	id := r.git("rev-parse", rev+":"+path)
	if err := os.Remove(filepath.Join(r.dir, ".git", "objects", id[:2], id[2:])); err != nil {
		r.t.Fatalf("remove loose object %s:%s: %v", rev, path, err)
	}
}

// TestRenameLimitOnTheWire covers plan item J at the API: when Git skips inexact rename detection,
// the session reports rename_detection "limit_exceeded".
func TestRenameLimitOnTheWire(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	const n = 1001 // inexact candidates beyond -l1000
	for i := range n {
		repo.write(fmt.Sprintf("old/%04d.txt", i), fmt.Sprintf("deleted %d\n", i))
	}
	repo.commit("base")
	if err := os.RemoveAll(filepath.Join(repo.dir, "old")); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		repo.write(fmt.Sprintf("new/%04d.txt", i), fmt.Sprintf("added %d\n", i))
	}
	repo.commit("head")

	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	if s := f.session(); s.RenameDetection != "limit_exceeded" {
		t.Errorf("rename_detection %q beyond the rename limit, want limit_exceeded", s.RenameDetection)
	}
}

// TestMissingBlob checks that a blob absent from the object database makes the file unavailable and
// disables rename detection, without failing the session.
func TestMissingBlob(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("a.txt", "a\n")
	repo.write("b.txt", "b\n")
	repo.write("x[ab].txt", "glob\n")
	repo.commit("base")
	repo.write("a.txt", "a changed\n")
	repo.write("b.txt", "b changed\n")
	repo.write("x[ab].txt", "glob changed\n")
	repo.write("xa.txt", "plain\n")
	repo.write("z.txt", "z\n")
	repo.commit("head")
	repo.removeBlob("HEAD~1", "a.txt")
	repo.removeBlob("HEAD", "z.txt")

	f := newFixture(t, repo, fixtureOptions{})
	f.serveRun()
	s := f.session()
	if s.RenameDetection != "missing_blobs" {
		t.Errorf("rename_detection %q with a blob missing, want missing_blobs", s.RenameDetection)
	}
	a := s.fileIndex(t, "a.txt")
	if d := f.fileDiff(a); d.Kind != "unavailable" || len(d.Hunks) != 0 {
		t.Errorf("a.txt: kind %q with %d hunks, want unavailable without a line diff", d.Kind, len(d.Hunks))
	}
	if r := f.contextLines(a, 1, 1); r.status != http.StatusConflict {
		t.Errorf("a.txt context: status %d, want 409", r.status)
	}
	if d := f.fileDiff(s.fileIndex(t, "b.txt")); d.Kind != "text" || len(d.Hunks) != 1 {
		t.Errorf("b.txt: kind %q with %d hunks, want its text diff", d.Kind, len(d.Hunks))
	}

	// Files with every blob present are counted; the others get no count rather than a false one.
	// x[ab].txt is named literally, so it does not also match xa.txt.
	counts := f.lineCounts()
	want := map[string]countDTO{"b.txt": {Added: 1, Deleted: 1}, "x[ab].txt": {Added: 1, Deleted: 1}, "xa.txt": {Added: 1}}
	for path, w := range want {
		i := s.fileIndex(t, path)
		w.I = i
		if got, ok := counts[i]; !ok || got != w {
			t.Errorf("%s count %+v (reported %v), want %+v", path, got, ok, w)
		}
	}
	for _, path := range []string{"a.txt", "z.txt"} {
		if c, ok := counts[s.fileIndex(t, path)]; ok {
			t.Errorf("%s has a missing blob but was counted %+v", path, c)
		}
	}
}

// TestWordRangesAreUTF16 checks that word ranges index the line as UTF-16 code units, so a non-BMP
// character before the change counts as two.
func TestWordRangesAreUTF16(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("w.txt", "\U0001F600 alpha\n")
	repo.commit("base")
	repo.write("w.txt", "\U0001F600 beta\n")
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	d := f.fileDiff(0)
	if len(d.Hunks) != 1 || len(d.Hunks[0].Blocks) != 1 {
		t.Fatalf("hunks %+v, want one change block", d.Hunks)
	}
	b := d.Hunks[0].Blocks[0]
	if len(b.Del) != 1 || len(b.Add) != 1 {
		t.Fatalf("block %+v, want one deleted and one added line", b)
	}
	// U+1F600 is two UTF-16 units and the space one, so the word starts at 3.
	if want := [][2]int{{3, 8}}; !slices.Equal(b.Del[0].W, want) {
		t.Errorf("deleted line ranges %v, want %v", b.Del[0].W, want)
	}
	if want := [][2]int{{3, 7}}; !slices.Equal(b.Add[0].W, want) {
		t.Errorf("added line ranges %v, want %v", b.Add[0].W, want)
	}
}

// Each deleted and added pair gets word changes on its own: a pair past the edit cap gets none,
// the next pair still gets its changed word, and an unpaired deleted line gets none.
func TestWordRangesPerPair(t *testing.T) {
	t.Parallel()
	words := func(prefix string) string {
		w := make([]string, 129)
		for i := range w {
			w[i] = fmt.Sprintf("%s%d", prefix, i)
		}
		return strings.Join(w, " ")
	}
	repo := newTestRepo(t)
	repo.write("w.txt", words("p")+"\nalpha beta\ngone\n")
	repo.commit("base")
	repo.write("w.txt", words("q")+"\nalpha gamma\n")
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	d := f.fileDiff(0)
	if len(d.Hunks) != 1 || len(d.Hunks[0].Blocks) != 1 {
		t.Fatalf("hunks %+v, want one change block", d.Hunks)
	}
	b := d.Hunks[0].Blocks[0]
	if len(b.Del) != 3 || len(b.Add) != 2 {
		t.Fatalf("block has %d deleted and %d added lines; want 3 and 2", len(b.Del), len(b.Add))
	}
	if b.Del[0].W != nil || b.Add[0].W != nil {
		t.Errorf("rewritten pair ranges %v, %v; want none", b.Del[0].W, b.Add[0].W)
	}
	if want := [][2]int{{6, 10}}; !slices.Equal(b.Del[1].W, want) {
		t.Errorf("second deleted line ranges %v, want %v", b.Del[1].W, want)
	}
	if want := [][2]int{{6, 11}}; !slices.Equal(b.Add[1].W, want) {
		t.Errorf("second added line ranges %v, want %v", b.Add[1].W, want)
	}
	if b.Del[2].W != nil {
		t.Errorf("unpaired deleted line ranges %v; want none", b.Del[2].W)
	}
}

// TestHighlighting covers highlighting at the API: a Go file is highlighted, and every non-empty
// line of each of its hunks, on both sides, and of an expanded gap carries spans that cover it; a
// file in an unsupported language is served without spans.
func TestHighlighting(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	var src strings.Builder
	src.WriteString("package a\n\nfunc A() int {\n\treturn 1\n}\n")
	for n := range 20 {
		fmt.Fprintf(&src, "\nvar v%d = %d\n", n, n)
	}
	src.WriteString("\nfunc B() int {\n\treturn 3\n}\n")
	goSrc := src.String()
	repo.write("a.go", goSrc)
	repo.write("a.txt", "plain\n")
	repo.commit("base")
	repo.write("a.go", strings.NewReplacer("return 1", "return 2", "return 3", "return 4").Replace(goSrc))
	repo.write("a.txt", "plain changed\n")
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	s := f.session()

	d := f.fileDiff(s.fileIndex(t, "a.go"))
	if !d.Highlighted {
		t.Error("a.go is not highlighted")
	}
	for _, h := range d.Hunks {
		for _, b := range h.Blocks {
			for _, l := range slices.Concat(b.Ctx, b.Del, b.Add) {
				var joined strings.Builder
				for _, sp := range l.S {
					joined.WriteString(sp.Text)
				}
				if l.T != "" && joined.String() != l.T {
					t.Errorf("a.go line %q: spans cover %q", l.T, joined.String())
				}
			}
		}
	}
	if len(d.Hunks) != 2 || d.Hunks[1].GapBefore == nil {
		t.Fatalf("a.go: want two hunks with a gap between them, got %+v", d.Hunks)
	}
	g := *d.Hunks[1].GapBefore
	r := f.contextLines(s.fileIndex(t, "a.go"), g.From, g.To)
	f.mustStatus(r, http.StatusOK, "context")
	var ctx contextDTO
	decode(t, r.body, &ctx)
	if !ctx.Highlighted || len(ctx.Lines) == 0 {
		t.Fatalf("a.go context: highlighted %v with %d lines", ctx.Highlighted, len(ctx.Lines))
	}
	for _, l := range ctx.Lines {
		var joined strings.Builder
		for _, sp := range l.S {
			joined.WriteString(sp.Text)
		}
		if l.T != "" && joined.String() != l.T {
			t.Errorf("a.go context line %q: spans cover %q", l.T, joined.String())
		}
	}

	d = f.fileDiff(s.fileIndex(t, "a.txt"))
	if d.Highlighted {
		t.Error("a.txt is reported highlighted")
	}
	for _, h := range d.Hunks {
		for _, b := range h.Blocks {
			for _, l := range slices.Concat(b.Ctx, b.Del, b.Add) {
				if len(l.S) != 0 {
					t.Errorf("a.txt line %q has spans", l.T)
				}
			}
		}
	}
}

// TestContextEitherSideLarge checks that a file is large when either side is large, so gap
// expansion is refused even when only the old side was large.
func TestContextEitherSideLarge(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("bytes-shrinks.txt", largeText(12_000))
	repo.write("lines-shrinks.txt", strings.Repeat("l\n", source.LargeLines+1))
	repo.write("bytes-grows.txt", numberedLines("line", 40))
	repo.write("both-small.txt", numberedLines("line", 40))
	repo.commit("base")
	repo.write("bytes-shrinks.txt", numberedLines("line", 40))
	repo.write("lines-shrinks.txt", numberedLines("line", 40))
	repo.write("bytes-grows.txt", largeText(12_000))
	repo.write("both-small.txt", strings.Replace(numberedLines("line", 40), "line 20\n", "line twenty\n", 1))
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	s := f.session()

	for _, c := range []struct {
		path  string
		large bool
	}{
		{"bytes-shrinks.txt", true},
		{"lines-shrinks.txt", true},
		{"bytes-grows.txt", true},
		{"both-small.txt", false},
	} {
		i := s.fileIndex(t, c.path)
		if got := f.fileDiff(i).Large; got != c.large {
			t.Errorf("%s: large = %v, want %v", c.path, got, c.large)
		}
		want := http.StatusOK
		if c.large {
			want = http.StatusConflict
		}
		f.mustStatus(f.contextLines(i, 1, 3), want, "context of "+c.path)
	}
}

// TestContextExpansionAcrossShiftedLines checks that lines inserted above a gap shift its
// numbering, and the context reply numbers each line on both sides with the text git stores. A
// range must lie inside one gap.
func TestContextExpansionAcrossShiftedLines(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	base := numberedLines("line", 12_000)
	repo.write("long.txt", base)
	repo.commit("base")
	head := "inserted 1\ninserted 2\ninserted 3\n" + strings.Replace(base, "line 11990\n", "line changed\n", 1)
	repo.write("long.txt", head)
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	i := f.session().fileIndex(t, "long.txt")
	oldLines := strings.Split(strings.TrimSuffix(repo.blob("HEAD~1", "long.txt"), "\n"), "\n")
	newLines := strings.Split(strings.TrimSuffix(repo.blob("HEAD", "long.txt"), "\n"), "\n")

	// With git's three lines of context the hunks show new lines 1..6 and 11990..11996, so the
	// gaps are new lines 7..11989 (old 4..11986) and 11997..12003.
	d := f.fileDiff(i)
	if len(d.Hunks) != 2 || d.Hunks[0].GapBefore != nil || d.Hunks[1].GapBefore == nil || d.GapAfter == nil {
		t.Fatalf("hunks and gaps: %+v, gap after %+v", d.Hunks, d.GapAfter)
	}
	if g := *d.Hunks[1].GapBefore; g != (gapDTO{From: 7, To: 11_989}) {
		t.Fatalf("middle gap %+v, want 7..11989", g)
	}
	if g := *d.GapAfter; g != (gapDTO{From: 11_997, To: 12_003}) {
		t.Fatalf("trailing gap %+v, want 11997..12003", g)
	}

	check := func(from, to int) {
		t.Helper()
		r := f.contextLines(i, from, to)
		f.mustStatus(r, http.StatusOK, fmt.Sprintf("context %d..%d", from, to))
		var c contextDTO
		decode(t, r.body, &c)
		if len(c.Lines) != to-from+1 {
			t.Fatalf("context %d..%d: %d lines", from, to, len(c.Lines))
		}
		for k, l := range c.Lines {
			n := from + k
			if l.New != n || l.Old != n-3 {
				t.Fatalf("context line %d numbered old %d new %d, want old %d new %d", n, l.Old, l.New, n-3, n)
			}
			if l.T != newLines[n-1] || l.T != oldLines[n-4] {
				t.Fatalf("context line %d is %q; git has new %q, old %q", n, l.T, newLines[n-1], oldLines[n-4])
			}
		}
	}
	check(7, 11_989)
	check(100, 200)
	check(11_997, 12_003)

	for _, c := range []struct {
		name     string
		from, to int
	}{
		{"inside the first hunk", 2, 5},
		{"from a gap across a hunk", 11_980, 11_995},
		{"inside the second hunk", 11_993, 11_993},
		{"past the end of the file", 12_003, 12_004},
		{"spanning two gaps", 7, 12_003},
	} {
		r := f.contextLines(i, c.from, c.to)
		if r.status != http.StatusUnprocessableEntity {
			t.Errorf("%s (%d..%d): status %d, want 422; body %q", c.name, c.from, c.to, r.status, r.body)
		}
	}
}

// TestFileStatusesOnTheWire covers the file status letters of the session reply: A, D, M, R and T
// as git names them.
func TestFileStatusesOnTheWire(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("deleted.txt", "deleted\n")
	repo.write("modified.txt", "before\n")
	repo.write("renamed_old.txt", numberedLines("renamed", 20))
	repo.write("typed.txt", "a regular file\n")
	repo.commit("base")
	repo.write("added.txt", "added\n")
	repo.remove("deleted.txt")
	repo.write("modified.txt", "after\n")
	repo.remove("renamed_old.txt")
	repo.write("renamed_new.txt", numberedLines("renamed", 20))
	repo.remove("typed.txt")
	if err := os.Symlink("modified.txt", filepath.Join(repo.dir, "typed.txt")); err != nil {
		t.Fatal(err)
	}
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()

	got := map[string]string{}
	for _, fl := range f.session().Files {
		got[fl.OldPath+" -> "+fl.NewPath] = fl.Status
	}
	want := map[string]string{
		" -> added.txt":                      "A",
		"deleted.txt -> ":                    "D",
		"modified.txt -> modified.txt":       "M",
		"renamed_old.txt -> renamed_new.txt": "R",
		"typed.txt -> typed.txt":             "T",
	}
	if !maps.Equal(got, want) {
		t.Errorf("statuses %v, want %v", got, want)
	}
}

// TestHighlightLanguageFollowsDisplayPath covers highlighting of renames: the language comes from
// the path the file is shown under, the new path, for both sides and for context lines.
func TestHighlightLanguageFollowsDisplayPath(t *testing.T) {
	t.Parallel()
	var src strings.Builder
	src.WriteString("package a\n\n")
	for k := 1; k <= 20; k++ {
		fmt.Fprintf(&src, "func F%d() int { return %d }\n", k, k)
	}
	goSrc := src.String()
	changed := strings.Replace(goSrc, "return 15 }", "return 150 }", 1)

	for _, c := range []struct {
		from, to    string
		highlighted bool
	}{
		{"x.txt", "x.go", true},
		{"y.go", "y.txt", false},
	} {
		t.Run(c.to, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			repo.write(c.from, goSrc)
			repo.commit("base")
			repo.remove(c.from)
			repo.write(c.to, changed)
			repo.commit("head")
			f := newFixture(t, repo, fixtureOptions{})
			f.serveHandler()
			s := f.session()
			if len(s.Files) != 1 || s.Files[0].Status != "R" {
				t.Fatalf("files %+v, want one rename", s.Files)
			}

			d := f.fileDiff(0)
			if d.Highlighted != c.highlighted {
				t.Errorf("highlighted = %v, want %v", d.Highlighted, c.highlighted)
			}
			var del, add []lineDTO
			for _, h := range d.Hunks {
				for _, b := range h.Blocks {
					del = append(del, b.Del...)
					add = append(add, b.Add...)
				}
			}
			if len(del) == 0 || len(add) == 0 {
				t.Fatalf("no changed lines: %+v", d.Hunks)
			}
			for _, l := range slices.Concat(del, add) {
				if !c.highlighted {
					if len(l.S) != 0 {
						t.Errorf("line %q has spans", l.T)
					}
					continue
				}
				var joined strings.Builder
				for _, sp := range l.S {
					joined.WriteString(sp.Text)
				}
				if len(l.S) == 0 || joined.String() != l.T {
					t.Errorf("line %q: spans %+v", l.T, l.S)
				}
			}

			if d.Hunks[0].GapBefore == nil {
				t.Fatalf("no gap before the hunk: %+v", d.Hunks[0])
			}
			g := *d.Hunks[0].GapBefore
			r := f.contextLines(0, g.From, g.To)
			f.mustStatus(r, http.StatusOK, "context")
			var ctx contextDTO
			decode(t, r.body, &ctx)
			if ctx.Highlighted != c.highlighted {
				t.Errorf("context highlighted = %v, want %v", ctx.Highlighted, c.highlighted)
			}
			for _, l := range ctx.Lines {
				if !c.highlighted && len(l.S) != 0 {
					t.Errorf("context line %q has spans", l.T)
				}
			}
		})
	}
}

// TestRenameWithoutHunks checks that for a pure rename the file is text with no hunks, its whole
// content is one gap, that gap's lines are served as context, and any of its lines is a comment
// target. A file index outside the snapshot is not found.
func TestRenameWithoutHunks(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("old.txt", numberedLines("line", 12))
	repo.commit("base")
	repo.git("mv", "old.txt", "new.txt")
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	i := f.session().fileIndex(t, "new.txt")

	d := f.fileDiff(i)
	if d.Kind != "text" || len(d.Hunks) != 0 || d.GapAfter == nil || *d.GapAfter != (gapDTO{From: 1, To: 12}) {
		t.Fatalf("diff: kind %q, %d hunks, gap after %+v; want text, none, 1..12", d.Kind, len(d.Hunks), d.GapAfter)
	}
	r := f.contextLines(i, 1, 12)
	f.mustStatus(r, http.StatusOK, "context 1..12")
	var c contextDTO
	decode(t, r.body, &c)
	if len(c.Lines) != 12 || c.Lines[4].T != "line 5" || c.Lines[4].Old != 5 || c.Lines[4].New != 5 {
		t.Errorf("context = %+v; want lines 1..12 numbered alike", c.Lines)
	}
	f.mustStatus(f.createComment("c1", targetDTO{Target: "line", Path: "new.txt", Side: "new", Line: 7}, "x"), http.StatusOK, "comment on line 7")
	f.mustStatus(f.createComment("c2", targetDTO{Target: "line", Path: "new.txt", Side: "new", Line: 13}, "x"), http.StatusUnprocessableEntity, "comment past the end")

	for _, q := range []string{"i=99", "i=-1"} {
		if r := f.do(http.MethodGet, "/api/file?"+q, ""); r.status != http.StatusNotFound {
			t.Errorf("file?%s: status %d, want 404", q, r.status)
		}
	}
}
