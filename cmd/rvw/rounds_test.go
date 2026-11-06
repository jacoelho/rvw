//go:build darwin

package main

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// roundsWorld has main with a.go, b.go and c.go and a feature branch that changes all three.
func roundsWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	for _, f := range []string{"a.go", "b.go", "c.go"} {
		w.write(f, "package x\n")
	}
	w.commit("base")
	w.git("checkout", "-q", "-b", "feature")
	for _, f := range []string{"a.go", "b.go", "c.go"} {
		w.write(f, "package x\n\n// "+f+" one\n// "+f+" two\n")
	}
	w.commit("feature")
	return w
}

func (w *world) ledgerDir() string {
	return filepath.Join(w.home, "Library", "Application Support", "rvw", "ledgers")
}

// roundSession is the part of the session document review rounds add.
type roundSession struct {
	Files []struct {
		NewPath string `json:"new_path"`
		Round   struct {
			State string `json:"state"`
		} `json:"round"`
	} `json:"files"`
	Viewed         []int  `json:"viewed"`
	LastReviewAt   string `json:"last_review_at"`
	HideWhitespace bool   `json:"hide_whitespace"`
	Earlier        []struct {
		ID     string `json:"id"`
		Body   string `json:"body"`
		File   *int   `json:"file"`
		Status string `json:"status"`
	} `json:"earlier"`
}

func (p *proc) roundSession() roundSession {
	p.t.Helper()
	var s roundSession
	if err := json.Unmarshal(p.must(http.MethodGet, "/api/session", "", http.StatusOK), &s); err != nil {
		p.t.Fatal(err)
	}
	return s
}

// states maps each changed file's path to its round state, "" when unreviewed.
func (s roundSession) states() map[string]string {
	out := map[string]string{}
	for _, f := range s.Files {
		out[f.NewPath] = f.Round.State
	}
	return out
}

func (p *proc) finishOK() {
	p.t.Helper()
	if r := p.finish(); !r.Delivered {
		p.t.Fatalf("finish %+v; want delivered", r)
	}
	if code := p.exitCode(); code != 0 {
		p.t.Fatalf("exit %d, want 0; stderr:\n%s", code, p.stderr)
	}
}

// assertNoRound checks that nothing was recorded: no ledger and no retention ref (REQ-RND-002).
func (w *world) assertNoRound() {
	w.t.Helper()
	if ledgers, _ := filepath.Glob(filepath.Join(w.ledgerDir(), "*.json")); len(ledgers) != 0 {
		w.t.Errorf("ledgers %v after a review that was not delivered; want none", ledgers)
	}
	if refs := w.git("for-each-ref", "--format=%(refname)", "refs/rvw/"); refs != "" {
		w.t.Errorf("retention refs %q after a review that was not delivered; want none", refs)
	}
}

// TestReviewRounds (AC-RND-001, AC-CMT-004): a delivered round records the files whose viewed mark
// was set and the comments. The next session, after the agent changes one file, shows the other
// reviewed file as same and pre-marks it, the changed one as changed, never-marked files as
// unreviewed, and the earlier comments. The first round has no comments.
func TestReviewRounds(t *testing.T) {
	t.Parallel()
	w := roundsWorld(t)
	const b = 1 // git orders changed files by path

	p := w.start(procOptions{}, "branch", "main")
	p.waitURL()
	if s := p.roundSession(); s.LastReviewAt != "" || len(s.Earlier) != 0 || !mapsEqual(s.states(), unreviewed) {
		t.Fatalf("first session = %+v; want no round", s)
	}
	p.must(http.MethodPut, "/api/viewed", `{"files":[0,1],"viewed":true}`, http.StatusNoContent)
	p.must(http.MethodPut, "/api/viewed", `{"files":[2],"viewed":true}`, http.StatusNoContent)
	p.must(http.MethodPut, "/api/viewed", `{"files":[2],"viewed":false}`, http.StatusNoContent)
	p.must(http.MethodPut, "/api/hide-whitespace", `{"hide":true}`, http.StatusNoContent)
	p.finishOK()
	if refs := strings.Fields(w.git("for-each-ref", "--format=%(refname)", "refs/rvw/")); len(refs) != 1 {
		t.Fatalf("retention refs %q after round 1, want one", refs)
	}

	w.write("a.go", "package x\n\n// a.go one\n// a.go two, edited\n")
	w.commit("agent edit")
	p = w.start(procOptions{}, "branch", "main")
	p.waitURL()
	s := p.roundSession()
	if want := map[string]string{"a.go": "changed", "b.go": "same", "c.go": ""}; !mapsEqual(s.states(), want) {
		t.Errorf("round 2 states = %v; want %v", s.states(), want)
	}
	if !slices.Equal(s.Viewed, []int{b}) {
		t.Errorf("round 2 viewed = %v; want only b.go (%d) pre-marked", s.Viewed, b)
	}
	if s.LastReviewAt == "" || len(s.Earlier) != 0 {
		t.Errorf("round 2 last review %q, earlier %+v; want a time and no earlier comments", s.LastReviewAt, s.Earlier)
	}
	if !s.HideWhitespace {
		t.Error("round 2 starts with whitespace shown; want round 1's Hide whitespace setting (REQ-DIFF-012)")
	}
	p.createComment("h1", `{"target":{"target":"line","file":0,"side":"new","line":4},"body":"Rename this."}`)
	p.createComment("h2", `{"target":{"target":"review"},"body":"Overall fine."}`)
	p.finishOK()

	p = w.start(procOptions{}, "branch", "main")
	p.waitURL()
	s = p.roundSession()
	bodies := map[string]string{}
	for _, e := range s.Earlier {
		bodies[e.Body] = e.Status
	}
	if want := map[string]string{"Rename this.": "current", "Overall fine.": "current"}; !mapsEqual(bodies, want) {
		t.Errorf("round 3 earlier comments = %v; want %v", bodies, want)
	}
	// Round 2 delivered b.go's preset mark and no mark on a.go, so a.go's reviewed version is
	// still round 1's.
	if want := map[string]string{"a.go": "changed", "b.go": "same", "c.go": ""}; !mapsEqual(s.states(), want) {
		t.Errorf("round 3 states = %v; want %v", s.states(), want)
	}
	// Copying an earlier comment twice adds one draft comment.
	var eid string
	for _, e := range s.Earlier {
		if e.Body == "Rename this." {
			eid = e.ID
		}
	}
	p.must(http.MethodPost, "/api/earlier/"+eid+"/copy", `{"id":"k1"}`, http.StatusOK)
	p.must(http.MethodPost, "/api/earlier/"+eid+"/copy", `{"id":"k2"}`, http.StatusOK)
	if got := p.session().Comments; len(got) != 1 || got[0].Body != "Rename this." {
		t.Errorf("draft after copying twice = %+v; want the one copy", got)
	}
	p.signal(syscall.SIGINT)
	if code := p.exitCode(); code != 130 {
		t.Fatalf("exit %d, want 130", code)
	}
}

// unreviewed is the states of roundsWorld's files with no round.
var unreviewed = map[string]string{"a.go": "", "b.go": "", "c.go": ""}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// TestRoundNotRecordedWithoutDelivery: a session that ends without delivering records no round
// (REQ-RND-002).
func TestRoundNotRecordedWithoutDelivery(t *testing.T) {
	t.Parallel()
	for name, end := range map[string]struct {
		end  func(p *proc)
		code int
	}{
		"cancelled":   {func(p *proc) { p.must(http.MethodPost, "/api/cancel", "", http.StatusNoContent) }, 3},
		"interrupted": {func(p *proc) { p.signal(syscall.SIGINT) }, 130},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := roundsWorld(t)
			p := w.start(procOptions{}, "branch", "main")
			p.waitURL()
			p.must(http.MethodPut, "/api/viewed", `{"files":[0],"viewed":true}`, http.StatusNoContent)
			p.createComment("h1", `{"target":{"target":"review"},"body":"Not delivered."}`)
			end.end(p)
			if code := p.exitCode(); code != end.code {
				t.Fatalf("exit %d, want %d", code, end.code)
			}
			w.assertNoRound()
		})
	}
}

// TestUnwritableLedger: a ledger that cannot be written is reported on stderr and changes neither
// the exit status nor the delivered result (REQ-RND-006).
func TestUnwritableLedger(t *testing.T) {
	t.Parallel()
	w := roundsWorld(t)
	parent := filepath.Dir(w.ledgerDir())
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file where the ledger directory belongs.
	if err := os.WriteFile(w.ledgerDir(), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := w.start(procOptions{}, "branch", "main")
	p.waitURL()
	p.must(http.MethodPut, "/api/viewed", `{"files":[0],"viewed":true}`, http.StatusNoContent)
	p.createComment("h1", `{"target":{"target":"review"},"body":"Looks right."}`)
	p.finishOK()
	if !strings.Contains(p.stdout.String(), "Looks right.") {
		t.Errorf("stdout lacks the review:\n%s", p.stdout.String())
	}
	if !strings.Contains(p.stderr.String(), "ledger") {
		t.Errorf("stderr does not report the ledger failure:\n%s", p.stderr)
	}
	if refs := w.git("for-each-ref", "--format=%(refname)", "refs/rvw/"); refs != "" {
		t.Errorf("retention refs %q without a ledger; want none", refs)
	}
}

// TestLedgerWriteFailsAfterDelivery: a ledger that cannot be written when the round is recorded,
// after delivery, is reported on stderr and changes neither the exit status nor the delivered
// result (REQ-RND-006).
func TestLedgerWriteFailsAfterDelivery(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	w := roundsWorld(t)
	p := w.start(procOptions{}, "branch", "main")
	p.waitURL()
	// The session has read the ledger directory; recording cannot create its new ledger file.
	if err := os.Chmod(w.ledgerDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(w.ledgerDir(), 0o700) })
	p.must(http.MethodPut, "/api/viewed", `{"files":[0],"viewed":true}`, http.StatusNoContent)
	p.createComment("h1", `{"target":{"target":"review"},"body":"Delivered anyway."}`)
	p.finishOK()
	if !strings.Contains(p.stdout.String(), "Delivered anyway.") {
		t.Errorf("stdout lacks the review:\n%s", p.stdout.String())
	}
	if !strings.Contains(p.stderr.String(), "review ledger") {
		t.Errorf("stderr does not report the ledger write failure:\n%s", p.stderr)
	}
	if ledgers, _ := filepath.Glob(filepath.Join(w.ledgerDir(), "*.json")); len(ledgers) != 0 {
		t.Errorf("ledgers %v after a failed write; want none", ledgers)
	}
}

// TestInvalidLedgerMovedAside: an invalid ledger is moved aside, keeping its bytes, the path is
// named on stderr, and the session starts with no round (REQ-RND-007).
func TestInvalidLedgerMovedAside(t *testing.T) {
	t.Parallel()
	w := roundsWorld(t)
	p := w.start(procOptions{}, "branch", "main")
	p.waitURL()
	p.must(http.MethodPut, "/api/viewed", `{"files":[0,1,2],"viewed":true}`, http.StatusNoContent)
	p.finishOK()
	ledgers, err := filepath.Glob(filepath.Join(w.ledgerDir(), "*.json"))
	if err != nil || len(ledgers) != 1 {
		t.Fatalf("ledgers %v (%v); want one", ledgers, err)
	}
	const garbage = `{"version":1,"last_at":`
	if err := os.WriteFile(ledgers[0], []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}

	p = w.start(procOptions{}, "branch", "main")
	p.waitURL()
	s := p.roundSession()
	if s.LastReviewAt != "" || len(s.Viewed) != 0 || !mapsEqual(s.states(), unreviewed) {
		t.Errorf("session after an invalid ledger = %+v; want no round", s)
	}
	moved, err := filepath.Glob(filepath.Join(w.ledgerDir(), "*.discarded-*"))
	if err != nil || len(moved) != 1 {
		t.Fatalf("moved-aside ledgers %v (%v); want one", moved, err)
	}
	if got := readFile(t, moved[0]); got != garbage {
		t.Errorf("moved-aside ledger holds %q; want the original bytes %q", got, garbage)
	}
	if !strings.Contains(p.stderr.String(), moved[0]) {
		t.Errorf("stderr does not name %s:\n%s", moved[0], p.stderr)
	}
	p.signal(syscall.SIGINT)
	if code := p.exitCode(); code != 130 {
		t.Fatalf("exit %d, want 130", code)
	}
}
