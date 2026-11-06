package server_test

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/round"
	"github.com/jacoelho/rvw/internal/source"
)

// The wire side of review rounds (contract revision 3): round states, the Since last review view,
// whitespace-only hunks and files, batch viewed marks and Still applies. Each round is a real
// session; a round is recorded in a real ledger with real retention refs, as rvw records it after
// a delivered finish.

type roundDTO struct {
	ReviewedAt time.Time `json:"reviewed_at"`
	State      string    `json:"state"`
	ReviewedWs bool      `json:"reviewed_ws"`
}

type roundFileDTO struct {
	Round   *roundDTO `json:"round"`
	OldPath string    `json:"old_path"`
	NewPath string    `json:"new_path"`
}

type earlierDTO struct {
	File   *int      `json:"file"`
	ID     string    `json:"id"`
	Author string    `json:"author"`
	Body   string    `json:"body"`
	Status string    `json:"status"`
	Target targetDTO `json:"target"`
}

type roundSessionDTO struct {
	LastReviewAt   *time.Time     `json:"last_review_at"`
	Files          []roundFileDTO `json:"files"`
	Viewed         []int          `json:"viewed"`
	Earlier        []earlierDTO   `json:"earlier"`
	HideWhitespace bool           `json:"hide_whitespace"`
}

type earlierPlacementDTO struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Side      string   `json:"side"`
	QuoteNote string   `json:"quote_note"`
	Quote     []string `json:"quote"`
	Start     int      `json:"start"`
	End       int      `json:"end"`
}

type roundDiffDTO struct {
	Scope            string                `json:"scope"`
	Large            bool                  `json:"large"`
	SinceUnavailable string                `json:"since_unavailable"`
	Hunks            []hunkDTO             `json:"hunks"`
	WsOnly           []bool                `json:"ws_only"`
	SinceAdded       [][2]int              `json:"since_added"`
	Earlier          []earlierPlacementDTO `json:"earlier"`
}

type copiedDTO struct {
	File       *int      `json:"file"`
	ID         string    `json:"id"`
	Author     string    `json:"author"`
	Body       string    `json:"body"`
	CopiedFrom string    `json:"copied_from"`
	Target     targetDTO `json:"target"`
}

func (f *fixture) roundSession() roundSessionDTO {
	f.t.Helper()
	r := f.do(http.MethodGet, "/api/session", "")
	f.mustStatus(r, http.StatusOK, "GET /api/session")
	var s roundSessionDTO
	decode(f.t, r.body, &s)
	return s
}

// index returns the index of the changed file whose display path is p.
func (s roundSessionDTO) index(t *testing.T, p string) int {
	t.Helper()
	for i, f := range s.Files {
		if f.NewPath == p || f.NewPath == "" && f.OldPath == p {
			return i
		}
	}
	t.Fatalf("no changed file %q", p)
	return -1
}

// earlier returns the earlier comment with body.
func (s roundSessionDTO) earlier(t *testing.T, body string) earlierDTO {
	t.Helper()
	for _, e := range s.Earlier {
		if e.Body == body {
			return e
		}
	}
	t.Fatalf("no earlier comment %q in %+v", body, s.Earlier)
	return earlierDTO{}
}

func (f *fixture) roundFile(query string) roundDiffDTO {
	f.t.Helper()
	r := f.do(http.MethodGet, "/api/file?"+query, "")
	f.mustStatus(r, http.StatusOK, "GET /api/file?"+query)
	var d roundDiffDTO
	decode(f.t, r.body, &d)
	return d
}

// markViewed sets or clears the viewed marks of files in one request.
func (f *fixture) markViewed(viewed bool, files ...int) response {
	f.t.Helper()
	return f.do(http.MethodPut, "/api/viewed", mustJSON(f.t, map[string]any{"files": files, "viewed": viewed}))
}

// newLedger returns an empty ledger store for f's worktree in a temporary directory.
func newLedger(t *testing.T, f *fixture) *round.Store {
	t.Helper()
	st, err := round.OpenStore(t.TempDir(), f.snap.CommonDir(), f.snap.TopLevel())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// recordRound records f's draft as delivered at at, as rvw does after a delivered finish.
func (f *fixture) recordRound(st *round.Store, at time.Time) {
	f.t.Helper()
	if err := st.Record(f.t.Context(), f.git, round.Delivered(f.snap, f.sess.Draft(), at)); err != nil {
		f.t.Fatalf("record round: %v", err)
	}
}

// renderHunks writes hunks one line per diff line: " old/new text" for context, "-old text",
// "+new text", so a mismatch shows as text.
func renderHunks(hunks []hunkDTO) string {
	var b strings.Builder
	for _, h := range hunks {
		b.WriteString(h.Header + "\n")
		for _, bl := range h.Blocks {
			for _, l := range bl.Ctx {
				fmt.Fprintf(&b, " %d/%d %s\n", l.Old, l.New, l.T)
			}
			for _, l := range bl.Del {
				fmt.Fprintf(&b, "-%d %s\n", l.Old, l.T)
			}
			for _, l := range bl.Add {
				fmt.Fprintf(&b, "+%d %s\n", l.New, l.T)
			}
		}
	}
	return b.String()
}

// digitLines returns n lines "01 tag" .. with edits replacing whole lines by number. Lines start
// with a digit, so git's default funcname pattern finds no hunk section.
func digitLines(tag string, n int, edits map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if e, ok := edits[i]; ok {
			b.WriteString(e + "\n")
			continue
		}
		fmt.Fprintf(&b, "%02d %s\n", i, tag)
	}
	return b.String()
}

var earlierIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// roundsRepo is the AC-RND-003 repository: main holds a.txt, b.txt, c.txt and g.go, tagged base;
// the worktree holds round 1's uncommitted edits of all four.
func roundsRepo(t *testing.T) *testRepo {
	t.Helper()
	r := newTestRepo(t)
	r.write("a.txt", digitLines("a", 30, nil))
	r.write("b.txt", "1 b base\n")
	r.write("c.txt", "1 c\n")
	r.write("g.go", "// Code generated by gen. DO NOT EDIT.\n\npackage g\n")
	r.commit("base")
	r.git("tag", "base")
	r.git("checkout", "-q", "-b", "feature")
	r.write("a.txt", digitLines("a", 30, map[int]string{5: "05 a r1"}))
	r.write("b.txt", "1 b r1\n")
	r.write("c.txt", "1 c\n2 c r1\n")
	r.write("g.go", "// Code generated by gen. DO NOT EDIT.\n\npackage g\n\nvar X = 1\n")
	return r
}

var branchUncommitted = func() source.Source {
	s, err := source.Branch("base", true)
	if err != nil {
		panic(err)
	}
	return s
}()

// TestRoundStatesAndSinceView covers AC-RND-003 at the HTTP API: round states, preset marks, the
// interdiff under Since last review, the lines added since review under All changes, context of
// the interdiff, and a base change.
func TestRoundStatesAndSinceView(t *testing.T) {
	t.Parallel()
	repo := roundsRepo(t)
	r1 := newFixture(t, repo, fixtureOptions{src: branchUncommitted})
	r1.serveHandler()
	if s := r1.roundSession(); s.LastReviewAt != nil || len(s.Earlier) != 0 || slices.ContainsFunc(s.Files, func(f roundFileDTO) bool { return f.Round != nil }) {
		t.Fatalf("round 1 with an empty ledger shows round facts: %+v", s)
	}
	s1 := r1.roundSession()
	all := []int{s1.index(t, "a.txt"), s1.index(t, "b.txt"), s1.index(t, "c.txt"), s1.index(t, "g.go")}
	r1.mustStatus(r1.markViewed(true, all...), http.StatusNoContent, "mark round 1 files viewed")
	ledger := newLedger(t, r1)
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour) // retention refs older than 30 days are pruned
	r1.recordRound(ledger, at)

	// Round 2: the agent edits a.txt's line 25 only.
	repo.write("a.txt", digitLines("a", 30, map[int]string{5: "05 a r1", 25: "25 a r2"}))
	r2 := newFixture(t, repo, fixtureOptions{src: branchUncommitted, ledger: ledger})
	r2.serveHandler()
	s := r2.roundSession()
	ia, ib, ic, ig := s.index(t, "a.txt"), s.index(t, "b.txt"), s.index(t, "c.txt"), s.index(t, "g.go")
	wantState := map[int]string{ia: "changed", ib: "same", ic: "same", ig: "same"}
	for i, want := range wantState {
		got := s.Files[i].Round
		if got == nil || got.State != want || got.ReviewedWs || !got.ReviewedAt.Equal(at) {
			t.Errorf("round 2 file %s: round %+v, want state %q reviewed at %v", s.Files[i].NewPath, got, want, at)
		}
	}
	if s.LastReviewAt == nil || !s.LastReviewAt.Equal(at) {
		t.Errorf("last_review_at = %v, want %v", s.LastReviewAt, at)
	}
	wantViewed := []int{ib, ic, ig}
	slices.Sort(wantViewed)
	if !slices.Equal(s.Viewed, wantViewed) {
		t.Errorf("round 2 preset viewed marks %v, want the reviewed-unchanged files %v", s.Viewed, wantViewed)
	}

	// Since last review: git diff-pairs -p of round 1's a.txt against round 2's, by hand.
	since := r2.roundFile(fmt.Sprintf("i=%d&scope=since", ia))
	wantSince := "@@ -22,7 +22,7 @@\n" +
		" 22/22 22 a\n 23/23 23 a\n 24/24 24 a\n" +
		"-25 25 a\n+25 25 a r2\n" +
		" 26/26 26 a\n 27/27 27 a\n 28/28 28 a\n"
	if since.Scope != "since" || since.SinceUnavailable != "" {
		t.Errorf("scope since of a.txt: scope %q unavailable %q, want the interdiff", since.Scope, since.SinceUnavailable)
	}
	if got := renderHunks(since.Hunks); got != wantSince {
		t.Errorf("a.txt interdiff hunks:\n%s\nwant:\n%s", got, wantSince)
	}
	if since.SinceAdded != nil {
		t.Errorf("since view carries since_added %v", since.SinceAdded)
	}
	if ws := r2.roundFile(fmt.Sprintf("i=%d&scope=since&ws=1", ia)); !slices.Equal(ws.WsOnly, []bool{false}) ||
		renderHunks(ws.Hunks) != wantSince {
		t.Errorf("since view with ws=1: ws_only %v, hunks\n%s", ws.WsOnly, renderHunks(ws.Hunks))
	}

	// All changes: the snapshot diff from base, with round 2's added line marked.
	allView := r2.roundFile(fmt.Sprintf("i=%d&scope=all", ia))
	wantAll := "@@ -2,7 +2,7 @@\n" +
		" 2/2 02 a\n 3/3 03 a\n 4/4 04 a\n-5 05 a\n+5 05 a r1\n 6/6 06 a\n 7/7 07 a\n 8/8 08 a\n" +
		"@@ -22,7 +22,7 @@\n" +
		" 22/22 22 a\n 23/23 23 a\n 24/24 24 a\n-25 25 a\n+25 25 a r2\n 26/26 26 a\n 27/27 27 a\n 28/28 28 a\n"
	if got := renderHunks(allView.Hunks); allView.Scope != "all" || got != wantAll {
		t.Errorf("a.txt under scope all (scope %q):\n%s\nwant:\n%s", allView.Scope, got, wantAll)
	}
	if want := [][2]int{{25, 25}}; !slices.Equal(allView.SinceAdded, want) {
		t.Errorf("since_added = %v, want %v", allView.SinceAdded, want)
	}

	// Context of the interdiff: the reviewed version's old lines, so line 5 is round 1's text,
	// where the snapshot diff's old line 5 is "05 a".
	r := r2.do(http.MethodGet, fmt.Sprintf("/api/file/context?i=%d&scope=since&from=4&to=6", ia), "")
	r2.mustStatus(r, http.StatusOK, "since context of a.txt")
	var ctx contextDTO
	decode(t, r.body, &ctx)
	var gotCtx []string
	for _, l := range ctx.Lines {
		gotCtx = append(gotCtx, fmt.Sprintf("%d/%d %s", l.Old, l.New, l.T))
	}
	if want := []string{"4/4 04 a", "5/5 05 a r1", "6/6 06 a"}; !slices.Equal(gotCtx, want) {
		t.Errorf("since context lines %q, want %q", gotCtx, want)
	}
	r = r2.do(http.MethodGet, fmt.Sprintf("/api/file/context?i=%d&scope=since&from=1&to=1", ib), "")
	r2.mustStatus(r, http.StatusConflict, "since context of b.txt, which has no interdiff")

	for _, q := range []string{"scope=both", "scope=SINCE", "ws=2", "ws=true"} {
		r := r2.do(http.MethodGet, fmt.Sprintf("/api/file?i=%d&%s", ia, q), "")
		r2.mustStatus(r, http.StatusBadRequest, "GET /api/file with "+q)
	}
	r = r2.do(http.MethodGet, fmt.Sprintf("/api/file/context?i=%d&scope=x&from=1&to=1", ia), "")
	r2.mustStatus(r, http.StatusBadRequest, "context with scope=x")

	// A file that is not changed since review has no interdiff: Since shows the snapshot diff.
	if d := r2.roundFile(fmt.Sprintf("i=%d&scope=since", ib)); d.Scope != "all" || d.SinceUnavailable != "" {
		t.Errorf("scope since of unchanged b.txt: scope %q unavailable %q, want all with no reason", d.Scope, d.SinceUnavailable)
	}

	// Round 3: the base moves to a commit changing b.txt's old side only.
	repo.write("b.txt", "1 b mid\n")
	repo.git("add", "b.txt")
	repo.git("commit", "-q", "-m", "mid")
	repo.write("b.txt", "1 b r1\n")
	repo.git("tag", "-f", "base")
	r3 := newFixture(t, repo, fixtureOptions{src: branchUncommitted, ledger: ledger})
	r3.serveHandler()
	s3 := r3.roundSession()
	for p, want := range map[string]string{"a.txt": "changed", "b.txt": "base", "c.txt": "same", "g.go": "same"} {
		if got := s3.Files[s3.index(t, p)].Round; got == nil || got.State != want {
			t.Errorf("round 3 %s: round %+v, want state %q", p, got, want)
		}
	}
}

// TestSinceUnavailable covers REQ-RND-012: a pruned reviewed blob shows the snapshot hunks with
// the reason "missing", and versions git compares as binary give "not_text".
func TestSinceUnavailable(t *testing.T) {
	t.Parallel()
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		repo := newTestRepo(t)
		repo.write("a.txt", digitLines("a", 10, nil))
		repo.commit("base")
		repo.git("tag", "base")
		repo.write("a.txt", digitLines("a", 10, map[int]string{5: "05 a r1"}))
		r1 := newFixture(t, repo, fixtureOptions{src: branchUncommitted})
		r1.serveHandler()
		r1.mustStatus(r1.markViewed(true, 0), http.StatusNoContent, "mark a.txt")
		ledger := newLedger(t, r1)
		r1.recordRound(ledger, time.Now().UTC().Truncate(time.Second))
		reviewed := r1.snap.File(0).NewBlob

		// Round 2 commits its edit, so after gc only round 1's uncommitted blob is unreachable.
		repo.write("a.txt", digitLines("a", 10, map[int]string{5: "05 a r2"}))
		repo.commit("r2")
		last, err := source.Branch("base", false)
		if err != nil {
			t.Fatal(err)
		}
		r2 := newFixture(t, repo, fixtureOptions{src: last, ledger: ledger})
		r2.serveHandler()
		if got := r2.roundSession().Files[0].Round; got == nil || got.State != "changed" {
			t.Fatalf("a.txt round %+v, want changed", got)
		}
		if d := r2.roundFile("i=0&scope=since"); d.Scope != "since" {
			t.Fatalf("with the ref kept, since view is %q", d.Scope)
		}
		for ref := range strings.SplitSeq(repo.git("for-each-ref", "--format=%(refname)", "refs/rvw/"), "\n") {
			repo.git("update-ref", "-d", ref)
		}
		repo.git("gc", "-q", "--prune=now")
		if _, err := repo.gitBytes("cat-file", "-e", reviewed); err == nil {
			t.Fatal("the reviewed blob survived gc; the test cannot show REQ-RND-012")
		}
		since := r2.roundFile("i=0&scope=since")
		all := r2.roundFile("i=0&scope=all")
		if since.Scope != "all" || since.SinceUnavailable != "missing" {
			t.Errorf("pruned reviewed blob: scope %q unavailable %q, want all, missing", since.Scope, since.SinceUnavailable)
		}
		if got, want := renderHunks(since.Hunks), renderHunks(all.Hunks); got != want || got == "" {
			t.Errorf("pruned reviewed blob hunks:\n%s\nwant the snapshot's:\n%s", got, want)
		}
		if all.SinceAdded != nil {
			t.Errorf("since_added %v with no reviewed blob", all.SinceAdded)
		}
		r := r2.do(http.MethodGet, "/api/file/context?i=0&scope=since&from=1&to=1", "")
		r2.mustStatus(r, http.StatusConflict, "since context after the reviewed blob was pruned")
	})
	t.Run("not text", func(t *testing.T) {
		t.Parallel()
		repo := newTestRepo(t)
		repo.write("a.dat", "base\n")
		repo.commit("base")
		repo.git("tag", "base")
		repo.write("a.dat", "\x00bin r1\n")
		r1 := newFixture(t, repo, fixtureOptions{src: branchUncommitted})
		r1.serveHandler()
		r1.mustStatus(r1.markViewed(true, 0), http.StatusNoContent, "mark a.dat")
		ledger := newLedger(t, r1)
		r1.recordRound(ledger, time.Now().UTC().Truncate(time.Second))
		repo.write("a.dat", "text r2\n")
		r2 := newFixture(t, repo, fixtureOptions{src: branchUncommitted, ledger: ledger})
		r2.serveHandler()
		if d := r2.roundFile("i=0&scope=since"); d.Scope != "all" || d.SinceUnavailable != "not_text" {
			t.Errorf("binary reviewed version: scope %q unavailable %q, want all, not_text", d.Scope, d.SinceUnavailable)
		}
	})
	t.Run("replaced by a submodule", func(t *testing.T) {
		t.Parallel()
		repo := newTestRepo(t)
		repo.write("a.txt", digitLines("a", 10, nil))
		repo.commit("base")
		repo.git("tag", "base")
		repo.write("a.txt", digitLines("a", 10, map[int]string{5: "05 a r1"}))
		r1 := newFixture(t, repo, fixtureOptions{src: branchUncommitted})
		r1.serveHandler()
		r1.mustStatus(r1.markViewed(true, 0), http.StatusNoContent, "mark a.txt")
		ledger := newLedger(t, r1)
		r1.recordRound(ledger, time.Now().UTC().Truncate(time.Second))

		// Round 2 turns a.txt into a gitlink: one type-change record whose old side is still text.
		repo.git("rm", "-q", "--cached", "a.txt")
		repo.addGitlink("a.txt", strings.TrimSpace(repo.git("rev-parse", "base")))
		repo.git("commit", "-q", "--no-verify", "-m", "submodule")
		last, err := source.Branch("base", false)
		if err != nil {
			t.Fatal(err)
		}
		r2 := newFixture(t, repo, fixtureOptions{src: last, ledger: ledger})
		r2.serveHandler()
		if got := r2.roundSession().Files[0].Round; got == nil || got.State != "changed" {
			t.Fatalf("a.txt round %+v, want changed", got)
		}
		for _, q := range []string{"i=0&scope=since", "i=0&scope=all"} {
			d := r2.roundFile(q)
			if d.Scope != "all" || d.SinceAdded != nil {
				t.Errorf("%s: scope %q since_added %v; want the snapshot diff and no since lines", q, d.Scope, d.SinceAdded)
			}
		}
	})
	t.Run("replaced by a symlink", func(t *testing.T) {
		t.Parallel()
		repo := newTestRepo(t)
		repo.write("a.txt", digitLines("a", 10, nil))
		repo.commit("base")
		repo.git("tag", "base")
		repo.write("a.txt", digitLines("a", 10, map[int]string{5: "05 a r1"}))
		r1 := newFixture(t, repo, fixtureOptions{src: branchUncommitted})
		r1.serveHandler()
		r1.mustStatus(r1.markViewed(true, 0), http.StatusNoContent, "mark a.txt")
		line := targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 2}
		r1.mustStatus(r1.createComment("l2", line, "line two"), http.StatusOK, "round 1 comment")
		ledger := newLedger(t, r1)
		r1.recordRound(ledger, time.Now().UTC().Truncate(time.Second))

		// The reviewed regular file is a symlink in round 2: a symlink's target is not compared
		// line by line with a file's text (REQ-RND-012).
		repo.remove("a.txt")
		if err := os.Symlink("elsewhere.txt", filepath.Join(repo.dir, "a.txt")); err != nil {
			t.Fatal(err)
		}
		r2 := newFixture(t, repo, fixtureOptions{src: branchUncommitted, ledger: ledger})
		r2.serveHandler()
		if got := r2.roundSession().Files[0].Round; got == nil || got.State != "changed" {
			t.Fatalf("a.txt round %+v, want changed", got)
		}
		since := r2.roundFile("i=0&scope=since")
		if since.Scope != "all" || since.SinceUnavailable != "not_text" {
			t.Errorf("symlink since a file was reviewed: scope %q unavailable %q, want all, not_text",
				since.Scope, since.SinceUnavailable)
		}
		all := r2.roundFile("i=0&scope=all")
		if all.SinceAdded != nil {
			t.Errorf("since_added %v for a file now a symlink", all.SinceAdded)
		}
		// Nor is the comment's line mapped into the link target (Earlier comment status: unplaced).
		if len(all.Earlier) != 1 || all.Earlier[0].Status != "unplaced" {
			t.Errorf("line comment on the replaced file placed %+v, want one unplaced", all.Earlier)
		}
	})
}

// TestWhitespaceOnlyHunks covers the hunk half of AC-DIFF-004 and REQ-DIFF-001: ws=1 marks only
// hunks whose changes are whitespace alone, and leaves every hunk as ws=0 shows it.
func TestWhitespaceOnlyHunks(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("mixed.txt", digitLines("w", 20, nil))
	repo.write("x.txt", " x\n"+strings.Repeat("x\n", 10))
	repo.commit("base")
	repo.write("mixed.txt", digitLines("w", 20, map[int]string{3: "  03 w", 15: "  15 w changed"}))
	repo.write("x.txt", strings.Repeat("x\n", 10))
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	s := f.roundSession()
	for _, c := range []struct {
		path string
		want []bool
	}{
		{"mixed.txt", []bool{true, false}}, // reindent only; reindent plus a token
		{"x.txt", []bool{false}},           // the -w patch pairs the lines differently
	} {
		i := s.index(t, c.path)
		plain := f.roundFile(fmt.Sprintf("i=%d", i))
		ws := f.roundFile(fmt.Sprintf("i=%d&ws=1", i))
		if !slices.Equal(ws.WsOnly, c.want) {
			t.Errorf("%s ws_only = %v, want %v", c.path, ws.WsOnly, c.want)
		}
		if plain.WsOnly != nil {
			t.Errorf("%s without ws=1 carries ws_only %v", c.path, plain.WsOnly)
		}
		if got, want := renderHunks(ws.Hunks), renderHunks(plain.Hunks); got != want {
			t.Errorf("%s hunks with ws=1:\n%s\ndiffer from ws=0:\n%s", c.path, got, want)
		}
	}
}

type wsStatusDTO struct {
	WhitespaceError string `json:"whitespace_error"`
	Whitespace      []int  `json:"whitespace"`
	WhitespaceDone  bool   `json:"whitespace_done"`
}

// TestWhitespaceOnlyFiles covers the file half of AC-DIFF-004 at GET /api/status: only a file
// changed in indentation alone is reported, and ws_known=1 leaves the list out.
func TestWhitespaceOnlyFiles(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	body := digitLines("r", 12, nil)
	repo.write("indent.txt", "a\nb\n")
	repo.write("old_name.txt", body)
	repo.write("mode.sh", "echo a\n")
	repo.write("bin.dat", "\x00 a\n")
	repo.write("joined.txt", "a\nb\n")
	repo.write("real.txt", "a\n")
	repo.commit("base")
	repo.write("indent.txt", "  a\n  b\n")
	repo.remove("old_name.txt")
	repo.write("new_name.txt", strings.Replace(body, "01 r", "01  r", 1))
	repo.write("mode.sh", "  echo a\n")
	repo.git("update-index", "--chmod=+x", "mode.sh")
	repo.write("bin.dat", "\x00  a\n")
	repo.write("joined.txt", "a b\n")
	repo.write("real.txt", "b\n")
	repo.git("add", "-A")
	repo.git("update-index", "--chmod=+x", "mode.sh")
	repo.git("commit", "-q", "-m", "head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveRun()
	s := f.roundSession()
	if i := s.index(t, "new_name.txt"); s.Files[i].OldPath != "old_name.txt" {
		t.Fatalf("the fixture's rename was not detected: %+v", s.Files[i])
	}

	deadline := time.Now().Add(20 * time.Second)
	var st wsStatusDTO
	for {
		r := f.do(http.MethodGet, "/api/status", "")
		f.mustStatus(r, http.StatusOK, "GET /api/status")
		st = wsStatusDTO{}
		decode(t, r.body, &st)
		if st.WhitespaceDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("whitespace-only files not done after 20s: last status %s", r.body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.WhitespaceError != "" {
		t.Errorf("whitespace_error %q", st.WhitespaceError)
	}
	if want := []int{s.index(t, "indent.txt")}; !slices.Equal(st.Whitespace, want) {
		t.Errorf("whitespace = %v, want only indent.txt %v (files %+v)", st.Whitespace, want, s.Files)
	}
	r := f.do(http.MethodGet, "/api/status?ws_known=1", "")
	f.mustStatus(r, http.StatusOK, "GET /api/status?ws_known=1")
	st = wsStatusDTO{}
	decode(t, r.body, &st)
	if st.Whitespace != nil || st.WhitespaceDone {
		t.Errorf("ws_known=1 status still carries the whitespace list: %s", r.body)
	}
	f.mustStatus(f.do(http.MethodGet, "/api/status?ws_known=yes", ""), http.StatusBadRequest, "ws_known=yes")
}

// TestViewedBatch covers REQ-NAV-010 at PUT /api/viewed: one request sets or clears several marks,
// all or none, and a mark set while whitespace is hidden records it.
func TestViewedBatch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.commit("base")
	for i := range 3 {
		repo.write(fmt.Sprintf("f%d.txt", i), fmt.Sprintf("file %d\n", i))
	}
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	viewed := func() []int { return f.roundSession().Viewed }

	f.mustStatus(f.markViewed(true, 0, 1, 3), http.StatusUnprocessableEntity, "batch with index 3 of 3 files")
	f.mustStatus(f.markViewed(true, 0, -1), http.StatusUnprocessableEntity, "batch with index -1")
	if got := viewed(); len(got) != 0 {
		t.Fatalf("a rejected batch marked %v", got)
	}
	f.mustStatus(f.markViewed(true), http.StatusBadRequest, "empty batch")
	f.mustStatus(f.markViewed(true, 0, 1, 2, 0), http.StatusBadRequest, "batch longer than the file list")
	f.mustStatus(f.do(http.MethodPut, "/api/viewed", `{"file":0,"viewed":true}`), http.StatusBadRequest, "revision 2 body")

	f.mustStatus(f.markViewed(true, 0, 2), http.StatusNoContent, "mark 0 and 2")
	if got := viewed(); !slices.Equal(got, []int{0, 2}) {
		t.Errorf("viewed = %v, want [0 2]", got)
	}
	f.mustStatus(f.markViewed(false, 0), http.StatusNoContent, "clear 0")
	if got := viewed(); !slices.Equal(got, []int{2}) {
		t.Errorf("viewed = %v, want [2]", got)
	}

	f.mustStatus(f.do(http.MethodPut, "/api/hide-whitespace", `{"hide":true}`), http.StatusNoContent, "hide whitespace")
	if !f.roundSession().HideWhitespace {
		t.Error("hide_whitespace not set")
	}
	f.mustStatus(f.markViewed(true, 1), http.StatusNoContent, "mark 1 with whitespace hidden")
	marks := f.sess.Draft().Viewed
	if len(marks) != 2 || marks[0].File != 1 || !marks[0].Whitespace || marks[1].File != 2 || marks[1].Whitespace {
		t.Errorf("draft marks %+v, want file 1 with whitespace hidden and file 2 without", marks)
	}
}

// TestViewedThousandFiles covers AC-NAV-004's last sentence: one request marks 1,000 files.
func TestViewedThousandFiles(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.commit("base")
	const n = 1000
	files := make([]int, n)
	for i := range n {
		repo.write(fmt.Sprintf("dir/f%04d.txt", i), strconv.Itoa(i)+"\n")
		files[i] = i
	}
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	f.mustStatus(f.markViewed(true, files...), http.StatusNoContent, "mark 1000 files")
	if got := f.roundSession().Viewed; !slices.Equal(got, files) {
		t.Errorf("%d files viewed, want all %d", len(got), n)
	}
}

// TestStillApplies covers AC-CMT-004 at the HTTP API: earlier comment statuses and placements,
// the copy fallback chain, copy idempotence and the result leaving earlier comments out.
func TestStillApplies(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("a.txt", digitLines("a", 30, nil))
	repo.write("gone.txt", "1 gone\n")
	repo.commit("base")
	repo.git("tag", "base")
	repo.write("a.txt", digitLines("a", 30, map[int]string{2: "02 a r1"}))
	repo.write("gone.txt", "1 gone r1\n")
	r1 := newFixture(t, repo, fixtureOptions{src: branchUncommitted})
	r1.serveHandler()
	for _, c := range []struct {
		id   string
		t    targetDTO
		body string
	}{
		{"l10", targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 10}, "line ten holds"},
		{"l25", targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 25}, "line twenty-five"},
		{"fa", targetDTO{Target: "file", Path: "a.txt"}, "file note on a"},
		{"rv", targetDTO{Target: "review"}, "overall from round one"},
		{"lg", targetDTO{Target: "line", Path: "gone.txt", Side: "new", Line: 1}, "gone line"},
	} {
		r1.mustStatus(r1.createComment(c.id, c.t, c.body), http.StatusOK, "round 1 comment "+c.id)
	}
	ledger := newLedger(t, r1)
	r1.recordRound(ledger, time.Now().UTC().Truncate(time.Second))

	// Round 2: three lines inserted after line 2, line 25 edited, gone.txt reverted.
	edited := digitLines("a", 30, map[int]string{2: "02 a r1\nins 1\nins 2\nins 3", 25: "25 a r2"})
	repo.write("a.txt", edited)
	repo.write("gone.txt", "1 gone\n")
	r2 := newFixture(t, repo, fixtureOptions{src: branchUncommitted, ledger: ledger})
	r2.serveHandler()
	s := r2.roundSession()
	ia := s.index(t, "a.txt")
	if len(s.Files) != 1 {
		t.Fatalf("round 2 files %+v, want a.txt only", s.Files)
	}
	for _, c := range []struct {
		body, status string
		file         *int
	}{
		{"line ten holds", "", &ia}, // pending until a.txt is requested
		{"line twenty-five", "", &ia},
		{"file note on a", "current", &ia},
		{"overall from round one", "current", nil},
		{"gone line", "unplaced", nil},
	} {
		e := s.earlier(t, c.body)
		if !earlierIDPattern.MatchString(e.ID) || e.Author != "human" || e.Status != c.status ||
			(e.File == nil) != (c.file == nil) || e.File != nil && *e.File != *c.file {
			t.Errorf("earlier %q = %+v, want status %q file %v", c.body, e, c.status, c.file)
		}
	}
	if s.earlier(t, "line ten holds").Target != (targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 10}) {
		t.Errorf("earlier target %+v, want round 1's line 10", s.earlier(t, "line ten holds").Target)
	}

	l10, l25 := s.earlier(t, "line ten holds").ID, s.earlier(t, "line twenty-five").ID
	got := map[string]earlierPlacementDTO{}
	for _, p := range r2.roundFile(fmt.Sprintf("i=%d", ia)).Earlier {
		got[p.ID] = p
	}
	if p := got[l10]; p.Status != "current" || p.Side != "new" || p.Start != 13 || p.End != 13 || p.Quote != nil {
		t.Errorf("line 10 comment placed %+v, want current at 13", p)
	}
	if p := got[l25]; p.Status != "outdated" || p.Start != 25 || p.End != 25 || !slices.Equal(p.Quote, []string{"25 a"}) {
		t.Errorf("line 25 comment placed %+v, want outdated at its round 1 line with quote [25 a]", p)
	}
	if len(got) != 2 {
		t.Errorf("a.txt placements %+v, want its two line comments", got)
	}

	copyEarlier := func(eid, id string) copiedDTO {
		t.Helper()
		r := r2.do(http.MethodPost, "/api/earlier/"+eid+"/copy", mustJSON(t, map[string]string{"id": id}))
		r2.mustStatus(r, http.StatusOK, "copy "+eid+" as "+id)
		var c copiedDTO
		decode(t, r.body, &c)
		return c
	}
	wantTargets := map[string]targetDTO{
		"k10": {Target: "line", Path: "a.txt", Side: "new", Line: 13},
		"k25": {Target: "file", Path: "a.txt"},
		"kgo": {Target: "review"},
	}
	for id, eid := range map[string]string{"k10": l10, "k25": l25, "kgo": s.earlier(t, "gone line").ID} {
		c := copyEarlier(eid, id)
		if c.ID != id || c.CopiedFrom != eid || c.Author != "human" || c.Target != wantTargets[id] {
			t.Errorf("copy of %s as %s = %+v, want target %+v", eid, id, c, wantTargets[id])
		}
	}
	if c := copyEarlier(l10, "again"); c.ID != "k10" {
		t.Errorf("copying %s again returned %+v, want the existing k10", l10, c)
	}
	comments := r2.session().Comments
	if ids := commentIDs(comments); len(ids) != 3 || slices.Contains(ids, "again") {
		t.Errorf("draft comments %v after a repeated copy, want k10, k25 and kgo", ids)
	}
	r2.mustStatus(r2.deleteAt("k10", r2.session().version(t, "k10")), http.StatusNoContent, "delete k10")
	if c := copyEarlier(l10, "k10b"); c.ID != "k10b" || c.Target != wantTargets["k10"] {
		t.Errorf("copy after deleting the copy = %+v, want a new comment at line 13", c)
	}
	r := r2.do(http.MethodPost, "/api/earlier/0123456789abcdef/copy", `{"id":"x"}`)
	r2.mustStatus(r, http.StatusNotFound, "copy of an unknown earlier comment")

	r = r2.do(http.MethodGet, "/api/result.md", "")
	r2.mustStatus(r, http.StatusOK, "result.md")
	md := string(r.body)
	for _, absent := range []string{"overall from round one", "file note on a", l10, l25, "copied_from"} {
		if strings.Contains(md, absent) {
			t.Errorf("the review result contains earlier-comment text %q:\n%s", absent, md)
		}
	}
	if !strings.Contains(md, "line ten holds") {
		t.Errorf("the review result lacks the copied comment:\n%s", md)
	}
}

// TestEarlierCommentOnLargeFile: a file that is large now has its earlier comments shown unplaced
// and no lines added since review, instead of failing the file (REQ-PERF-002).
func TestEarlierCommentOnLargeFile(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("a.txt", digitLines("a", 10, nil))
	repo.commit("base")
	repo.git("tag", "base")
	repo.write("a.txt", digitLines("a", 10, map[int]string{5: "05 a r1"}))
	r1 := newFixture(t, repo, fixtureOptions{src: branchUncommitted})
	r1.serveHandler()
	r1.mustStatus(r1.markViewed(true, 0), http.StatusNoContent, "mark a.txt")
	line := targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 5}
	r1.mustStatus(r1.createComment("l5", line, "line five"), http.StatusOK, "round 1 comment")
	ledger := newLedger(t, r1)
	r1.recordRound(ledger, time.Now().UTC().Truncate(time.Second))

	repo.write("a.txt", strings.Repeat("b", source.LargeBytes+1)+"\n")
	r2 := newFixture(t, repo, fixtureOptions{src: branchUncommitted, ledger: ledger})
	r2.serveHandler()
	all := r2.roundFile("i=0&scope=all")
	if !all.Large || all.SinceAdded != nil || len(all.Earlier) != 1 || all.Earlier[0].Status != "unplaced" {
		t.Errorf("scope all: large %v since_added %v earlier %+v; want large, no lines, one unplaced",
			all.Large, all.SinceAdded, all.Earlier)
	}
	if since := r2.roundFile("i=0&scope=since"); since.SinceUnavailable != "not_text" {
		t.Errorf("scope since: unavailable %q, want not_text", since.SinceUnavailable)
	}
}

// TestFileServedWhenRoundDecorationsFail: a git failure while computing a file's per-file round
// decorations is logged and the file is served without them. The failure is a commit ID recorded
// where a blob is expected, which git rejects with a plain error.
func TestFileServedWhenRoundDecorationsFail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, logged string
	}{
		{"placement fails", `msg="earlier comment not placed"`},
		{"added-since fails", `msg="lines added since review not computed" path=a.txt`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t)
			repo.write("a.txt", digitLines("a", 10, nil))
			repo.commit("base")
			repo.git("tag", "base")
			repo.write("a.txt", digitLines("a", 10, map[int]string{5: "05 a r1"}))
			r1 := newFixture(t, repo, fixtureOptions{src: branchUncommitted})
			r1.serveHandler()
			r1.mustStatus(r1.markViewed(true, 0), http.StatusNoContent, "mark a.txt")
			line := targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 5}
			r1.mustStatus(r1.createComment("l5", line, "line five"), http.StatusOK, "round 1 comment")
			line2 := targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 2}
			r1.mustStatus(r1.createComment("l2", line2, "line two"), http.StatusOK, "round 1 second comment")
			d := round.Delivered(r1.snap, r1.sess.Draft(), time.Now().UTC().Truncate(time.Second))
			commit := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
			if tc.name == "placement fails" {
				d.Comments[0].Blob.ID = commit
			} else {
				key := round.KeyOf(r1.snap.File(0))
				rv := d.Reviewed[key]
				rv.Version.New.ID = commit
				d.Reviewed[key] = rv
			}
			ledger := newLedger(t, r1)
			if err := ledger.Record(t.Context(), r1.git, d); err != nil {
				t.Fatal(err)
			}

			// Round 2 edits line 8 of a.txt; line 5 keeps its r1 text.
			repo.write("a.txt", digitLines("a", 10, map[int]string{5: "05 a r1", 8: "08 a r2"}))
			var logs bytes.Buffer
			r2 := newFixture(t, repo, fixtureOptions{src: branchUncommitted, ledger: ledger, log: &logs})
			r2.serveHandler()
			earlier := r2.roundSession().Earlier
			if len(earlier) != 2 {
				t.Fatalf("session earlier comments %+v, want both comments", earlier)
			}
			resp := r2.do(http.MethodGet, "/api/file?i=0&scope=all", "")
			r2.mustStatus(resp, http.StatusOK, "GET /api/file")
			var got roundDiffDTO
			decode(t, resp.body, &got)
			if !strings.Contains(logs.String(), tc.logged) {
				t.Errorf("log %q lacks %q", logs.String(), tc.logged)
			}
			if tc.name == "placement fails" {
				// Only the comment whose placement failed is unplaced (Definitions: unplaced). The
				// draft lists comments by position, so the broken one is line 2's.
				broken, kept := earlier[0].ID, earlier[1].ID
				if !strings.Contains(logs.String(), `msg="earlier comment not placed" id=`+broken+" path=a.txt ") {
					t.Errorf("log %q does not name earlier comment %s", logs.String(), broken)
				}
				if len(got.Earlier) != 2 ||
					got.Earlier[0].ID != broken || got.Earlier[0].Status != "unplaced" ||
					got.Earlier[1].ID != kept || got.Earlier[1].Status != "current" || got.Earlier[1].Start != 5 {
					t.Errorf("earlier %+v, want %s unplaced and %s current on line 5", got.Earlier, broken, kept)
				}
				// Still applies on the broken comment fails and copies nothing; the other still copies
				// onto its line (REQ-CMT-011).
				body := func(id string) string { return mustJSON(t, map[string]string{"id": id}) }
				r2.mustStatus(r2.do(http.MethodPost, "/api/earlier/"+broken+"/copy", body("c-broken")),
					http.StatusInternalServerError, "copy broken")
				r2.mustStatus(r2.do(http.MethodPost, "/api/earlier/"+kept+"/copy", body("c-kept")),
					http.StatusOK, "copy kept")
				cs := r2.sess.Draft().Comments
				if len(cs) != 1 || cs[0].ID != "c-kept" || cs[0].Target.Kind() != review.TargetLine {
					t.Errorf("draft comments %+v, want only c-kept on a line", cs)
				} else if _, l := cs[0].Target.Lines(); l != 5 {
					t.Errorf("c-kept on line %d, want 5", l)
				}
				if want := [][2]int{{8, 8}}; !slices.Equal(got.SinceAdded, want) {
					t.Errorf("since_added %v, want %v", got.SinceAdded, want)
				}
				return
			}
			if got.SinceAdded != nil {
				t.Errorf("since_added %v, want none", got.SinceAdded)
			}
			if len(got.Earlier) != 2 || got.Earlier[0].Status == "unplaced" || got.Earlier[1].Status == "unplaced" {
				t.Errorf("earlier %+v, want the comments' real placements", got.Earlier)
			}
		})
	}
}
