//go:build darwin

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// longLine is 603 bytes, so quoting it cuts 103 bytes.
var longLine = "// " + strings.Repeat("x", 600)

// featureWorld has main with a one-line a.go and a feature branch that grows a.go to 30 lines,
// line 3 of them long.
func featureWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.write("a.go", "package a\n")
	w.commit("base")
	w.git("checkout", "-q", "-b", "feature")
	w.write("a.go", featureLines())
	w.commit("feature")
	return w
}

func featureLines() string {
	var b strings.Builder
	b.WriteString("package a\n")
	for i := 2; i <= 30; i++ {
		if i == 3 {
			b.WriteString(longLine + "\n")
			continue
		}
		fmt.Fprintf(&b, "// line %d\n", i)
	}
	return b.String()
}

// quotedCode is what the result quotes for range new 1-25 of a.go: 20 lines, line 3 cut.
func quotedCode() []string {
	lines := strings.Split(featureLines(), "\n")[:20]
	lines[2] = longLine[:500]
	return lines
}

const agentRangeSeed = `{"comments":[{"target":"range","path":"a.go","side":"new","start":1,"end":25,"body":"Check this block."}]}`

// reviewFeature adds a review comment, a file comment and a summary through the API.
func reviewFeature(p *proc, summary string) {
	p.t.Helper()
	p.createComment("h1", `{"target":{"target":"review"},"body":"Overall comment."}`)
	p.createComment("h2", `{"target":{"target":"file","file":0},"body":"File comment."}`)
	p.must(http.MethodPut, "/api/summary", fmt.Sprintf(`{"summary":%q}`, summary), http.StatusNoContent)
}

type finishReply struct {
	Markdown  string `json:"markdown"`
	Error     string `json:"error"`
	Delivered bool   `json:"delivered"`
}

func (p *proc) finish() finishReply {
	p.t.Helper()
	var r finishReply
	if err := json.Unmarshal(p.must(http.MethodPost, "/api/finish", "", http.StatusOK), &r); err != nil {
		p.t.Fatal(err)
	}
	return r
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestFinishPrintsMarkdown checks that, when stdout is a regular file, it receives exactly the
// Markdown result, and only at finish.
func TestFinishPrintsMarkdown(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	seed := w.seedFile("seed.json", agentRangeSeed)
	out, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	p := w.start(procOptions{stdout: out}, "branch", "main", "--seed", seed)
	p.waitURL()
	// open runs concurrently and is abandoned when rvw exits, so wait for it before finishing.
	p.waitFor("the browser to be opened", func() bool {
		data, err := os.ReadFile(w.openLog)
		return err == nil && string(data) == p.url+"\n"
	})
	reviewFeature(p, "Looks good.")
	if got := readFile(t, out.Name()); got != "" {
		t.Fatalf("stdout before finish: %q", got)
	}
	reply := p.finish()
	if code := p.exitCode(); code != 0 {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, p.stderr)
	}

	want := featureMarkdown(w)
	if got := readFile(t, out.Name()); got != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, want)
	}
	if !reply.Delivered || reply.Markdown != want {
		t.Errorf("finish reply %+v, want delivered with the printed Markdown", reply)
	}
	if resumed, _ := w.inspectDraft("branch", "main"); resumed {
		t.Error("the draft survived a delivered finish")
	}
}

// featureMarkdown is the Markdown result of `rvw branch main --seed` with agentRangeSeed in
// featureWorld after reviewFeature(p, "Looks good.").
func featureMarkdown(w *world) string {
	w.t.Helper()
	// Every written text, summary and comment bodies alike, is a block quote under its author.
	return fmt.Sprintf("# Review\n\n"+
		"Source: branch main..HEAD (base %s, head %s)\n\n"+
		"## Summary\n\n> Looks good.\n\n"+
		"## Comments\n\n"+
		"### Review\n\n(human)\n> Overall comment.\n\n"+
		"### `a.go` (file)\n\n(human)\n> File comment.\n\n"+
		"### `a.go`:1-25 (new)\n\n```\n%s\n```\n\n"+
		"… 5 more lines; 103 bytes cut from long lines not shown\n\n"+
		"(agent)\n> Check this block.\n",
		w.git("rev-parse", "main^{tree}")[:7], w.git("rev-parse", "HEAD^{tree}")[:7],
		strings.Join(quotedCode(), "\n"))
}

// TestFinishPrintsJSON checks that, with --json, stdout is one compact JSON document with its
// members in a fixed order, then a newline.
func TestFinishPrintsJSON(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	seed := w.seedFile("seed.json", agentRangeSeed)
	out, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	p := w.start(procOptions{stdout: out}, "branch", "main", "--json", "--seed", seed)
	p.waitURL()
	reviewFeature(p, "Needs work.")
	p.finish()
	if code := p.exitCode(); code != 0 {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, p.stderr)
	}

	code := make([]string, 0, 20)
	for _, l := range quotedCode() {
		code = append(code, `"`+l+`"`) // ASCII without quotes or backslashes
	}
	want := `{` +
		`"source":{"kind":"branch","branch":"main","uncommitted":false},` +
		`"base_tree":"` + w.git("rev-parse", "main^{tree}") + `",` +
		`"head_tree":"` + w.git("rev-parse", "HEAD^{tree}") + `",` +
		`"summary":"Needs work.",` +
		`"comments":[` +
		`{"author":"human","target":"review","body":"Overall comment."},` +
		`{"author":"human","target":"file","path":"a.go","body":"File comment."},` +
		`{"author":"agent","target":"range","path":"a.go","side":"new","start":1,"end":25,` +
		`"code":[` + strings.Join(code, ",") + `],"code_omitted_lines":5,"code_omitted_bytes":103,` +
		`"body":"Check this block."}]}` + "\n"
	if got := readFile(t, out.Name()); got != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, want)
	}
}

// TestFinishToPipe covers finishing to a pipe: an agent usually reads rvw's stdout through a
// pipe, and the result must arrive there whole with exit 0.
func TestFinishToPipe(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	p := w.start(procOptions{}, "last", "--json")
	p.waitURL()
	reply := p.finish()
	if code := p.exitCode(); code != 0 {
		t.Fatalf("exit %d, want 0; finish reply %+v; stderr:\n%s", code, reply, p.stderr)
	}
	want := `{"source":{"kind":"last","uncommitted":false},` +
		`"base_tree":"` + w.git("rev-parse", "HEAD~1^{tree}") + `","head_tree":"` + w.git("rev-parse", "HEAD^{tree}") + `",` +
		`"summary":"","comments":[]}` + "\n"
	if got := p.stdout.String(); got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
}

// TestFinishWithAnUndeletableDraft: a draft that cannot be deleted after delivery does not turn a
// delivered result into a failure. rvw exits 0 with the whole result and names the draft on stderr.
func TestFinishWithAnUndeletableDraft(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	w := featureWorld(t)
	p := w.start(procOptions{}, "last")
	p.waitURL()
	p.createComment("h1", `{"target":{"target":"review"},"body":"Delivered anyway."}`)
	dir := filepath.Join(w.home, "Library", "Application Support", "rvw", "drafts")
	drafts, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts %v (%v), want exactly one", drafts, err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	reply := p.finish()
	if code := p.exitCode(); code != 0 {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, p.stderr)
	}
	if !reply.Delivered || !strings.Contains(reply.Markdown, "Delivered anyway.") {
		t.Errorf("finish reply %+v; want delivered with the comment", reply)
	}
	if got := p.stdout.String(); got != reply.Markdown {
		t.Errorf("stdout %q; want exactly the result %q", got, reply.Markdown)
	}
	if !strings.Contains(p.stderr.String(), drafts[0]) {
		t.Errorf("stderr does not name the draft %s:\n%s", drafts[0], p.stderr)
	}
}

// TestCancel checks that cancel exits 3 with nothing on stdout and no draft.
func TestCancel(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	p := w.start(procOptions{}, "last")
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"draft"}`, http.StatusNoContent)
	p.must(http.MethodPost, "/api/cancel", "", http.StatusNoContent)
	if code := p.exitCode(); code != 3 {
		t.Fatalf("exit %d, want 3; stderr:\n%s", code, p.stderr)
	}
	if out := p.stdout.String(); out != "" {
		t.Errorf("stdout after cancel: %q", out)
	}
	if resumed, s := w.inspectDraft("last"); resumed || s.Summary != "" {
		t.Errorf("after cancel the next run resumed=%v with summary %q", resumed, s.Summary)
	}
}

// TestPageClosed checks the page-closed outcome with the real 10 s grace: rvw waits out the grace,
// exits 4 with nothing on stdout, names the draft on stderr, and a rerun resumes the draft.
func TestPageClosed(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	p := w.start(procOptions{}, "last")
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"kept after close"}`, http.StatusNoContent)
	p.must(http.MethodPost, "/api/page-closed", "", http.StatusNoContent)
	posted := time.Now()

	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatalf("rvw did not exit within 15 s of the close notice; stderr:\n%s", p.stderr)
	}
	if took := time.Since(posted); took < 9*time.Second {
		t.Errorf("rvw exited %v after the close notice, before the 10 s grace", took)
	}
	if code := p.cmd.ProcessState.ExitCode(); code != 4 {
		t.Fatalf("exit %d, want 4; stderr:\n%s", code, p.stderr)
	}
	if out := p.stdout.String(); out != "" {
		t.Errorf("stdout after the page closed: %q", out)
	}
	dir := filepath.Join(w.home, "Library", "Application Support", "rvw", "drafts")
	drafts, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts %v (%v), want exactly one", drafts, err)
	}
	if rec, ok := logRecord(p.stderr.String(), pageClosedMsg); !ok || rec["draft"] != drafts[0] {
		t.Errorf("stderr lacks %q naming draft %s:\n%s", pageClosedMsg, drafts[0], p.stderr)
	}
	if resumed, s := w.inspectDraft("last"); !resumed || s.Summary != "kept after close" {
		t.Errorf("after the page closed the next run resumed=%v with summary %q", resumed, s.Summary)
	}
}

// TestSignalsKeepDraft checks that SIGINT exits 130 and SIGTERM 143; the draft survives and is
// restored on the next run, which ignores the seed instead of adding its comments again.
func TestSignalsKeepDraft(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	seed := w.seedFile("seed.json", agentRangeSeed)
	args := []string{"branch", "main", "--seed", seed}

	p := w.start(procOptions{}, args...)
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"before restore"}`, http.StatusNoContent)
	p.must(http.MethodPut, "/api/viewed", `{"files":[0],"viewed":true}`, http.StatusNoContent)
	p.signal(syscall.SIGINT)
	if code := p.exitCode(); code != 130 {
		t.Fatalf("SIGINT: exit %d, want 130; stderr:\n%s", code, p.stderr)
	}
	if out := p.stdout.String(); out != "" {
		t.Errorf("stdout after SIGINT: %q", out)
	}

	p = w.start(procOptions{}, args...)
	p.waitURL()
	if !strings.Contains(p.stderr.String(), resumedNotice) {
		t.Errorf("rerun did not resume the draft; stderr:\n%s", p.stderr)
	}
	if _, ok := logRecord(p.stderr.String(), "the seed was ignored because a draft exists"); !ok {
		t.Errorf("rerun with --seed did not say the seed was ignored; stderr:\n%s", p.stderr)
	}
	s := p.session()
	if s.Summary != "before restore" || len(s.Viewed) != 1 || s.Viewed[0] != 0 || len(s.Comments) != 1 {
		t.Errorf("restored session: summary %q, viewed %v, %d comments; want the saved summary, [0], the one seeded comment",
			s.Summary, s.Viewed, len(s.Comments))
	}
	p.must(http.MethodPut, "/api/summary", `{"summary":"after restore"}`, http.StatusNoContent)
	p.signal(syscall.SIGTERM)
	if code := p.exitCode(); code != 143 {
		t.Fatalf("SIGTERM: exit %d, want 143; stderr:\n%s", code, p.stderr)
	}

	resumed, s := w.inspectDraft(args...)
	if !resumed || s.Summary != "after restore" {
		t.Errorf("after SIGTERM: resumed=%v summary %q", resumed, s.Summary)
	}
}

// TestInvalidSeedWithDraft checks that an invalid seed fails even when a draft exists and the
// seed would be ignored.
func TestInvalidSeedWithDraft(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	p := w.start(procOptions{}, "branch", "main")
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"kept"}`, http.StatusNoContent)
	p.signal(syscall.SIGINT)
	if code := p.exitCode(); code != 130 {
		t.Fatalf("exit %d, want 130", code)
	}

	bad := w.seedFile("bad.json", `{"comments":[{"target":"line","path":"a.go","side":"new","line":31,"body":"past the end"}]}`)
	p = w.run("branch", "main", "--seed", bad)
	if code := p.cmd.ProcessState.ExitCode(); code != 1 {
		t.Errorf("exit %d, want 1; stderr:\n%s", code, p.stderr)
	}
	if _, ok := logRecord(p.stderr.String(), "review at"); ok {
		t.Errorf("an invalid seed still served a review; stderr:\n%s", p.stderr)
	}
	if resumed, s := w.inspectDraft("branch", "main"); !resumed || s.Summary != "kept" {
		t.Errorf("the draft changed: resumed=%v summary %q", resumed, s.Summary)
	}
}

// TestCorruptDraftMovedAside checks that a saved draft that cannot be read is moved aside
// with its bytes intact, stderr names where it went, and the run starts fresh from the seed.
func TestCorruptDraftMovedAside(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	p := w.start(procOptions{}, "branch", "main")
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"to be lost"}`, http.StatusNoContent)
	p.signal(syscall.SIGINT)
	if code := p.exitCode(); code != 130 {
		t.Fatalf("exit %d, want 130; stderr:\n%s", code, p.stderr)
	}

	dir := filepath.Join(w.home, "Library", "Application Support", "rvw", "drafts")
	drafts, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts %v (%v), want exactly one", drafts, err)
	}
	draft := drafts[0]
	const corrupt = "{not json"
	if err := os.WriteFile(draft, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}

	p = w.start(procOptions{}, "branch", "main", "--seed", w.seedFile("seed.json", agentRangeSeed))
	p.waitURL()
	stderr := p.stderr.String()
	rec, ok := logRecord(stderr, "the saved draft could not be restored and was moved aside")
	if !ok {
		t.Fatalf("stderr does not say the draft could not be restored:\n%s", stderr)
	}
	aside := rec["path"]
	if filepath.Dir(aside) != dir {
		t.Fatalf("the notice names %q, not a path under %s:\n%s", aside, dir, stderr)
	}
	if aside == draft {
		t.Fatalf("the draft was not moved: notice names %s", aside)
	}
	if got := readFile(t, aside); got != corrupt {
		t.Errorf("%s holds %q, want the corrupt draft's bytes %q", aside, got, corrupt)
	}
	if data, err := os.ReadFile(draft); err == nil && string(data) == corrupt {
		t.Errorf("%s still holds the corrupt draft", draft)
	}
	if strings.Contains(stderr, resumedNotice) {
		t.Errorf("the run says it resumed a draft:\n%s", stderr)
	}
	s := p.session()
	if s.Summary != "" || len(s.Comments) != 1 || s.Comments[0].Author != "agent" || s.Comments[0].Body != "Check this block." {
		t.Errorf("session after moving the draft aside: summary %q, comments %+v; want only the seeded comment",
			s.Summary, s.Comments)
	}
	p.signal(syscall.SIGINT)
	if code := p.exitCode(); code != 130 {
		t.Fatalf("exit %d, want 130; stderr:\n%s", code, p.stderr)
	}
}

// TestClosedStdout checks that, when stdout's reader has gone, finish reports the failed
// delivery, rvw exits 1 and keeps the draft.
func TestClosedStdout(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := w.start(procOptions{stdout: wr}, "last")
	// Only the child holds the pipe's write end, and nobody holds the read end.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"kept"}`, http.StatusNoContent)
	reply := p.finish()
	if reply.Delivered || reply.Error == "" {
		t.Errorf("finish reply %+v, want a reported delivery failure", reply)
	}
	if code := p.exitCode(); code != 1 {
		t.Fatalf("exit %d, want 1; stderr:\n%s", code, p.stderr)
	}
	if resumed, s := w.inspectDraft("last"); !resumed || s.Summary != "kept" {
		t.Errorf("after a failed delivery: resumed=%v summary %q; want the draft kept", resumed, s.Summary)
	}
}

// TestSignalDuringDelivery checks that a signal arriving while the result is
// being written does not interrupt the review. The delivery decides the outcome, so a delivery
// that completes exits 0 with the whole result rather than 130 with part of it.
func TestSignalDuringDelivery(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Fill the pipe so the child's write blocks until the test reads.
	if err := wr.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	filler := 0
	for chunk := make([]byte, 4096); ; {
		n, err := wr.Write(chunk)
		filler += n
		if err != nil {
			break
		}
	}
	if err := wr.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	p := w.start(procOptions{stdout: wr}, "last", "--json")
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	p.waitURL()

	finished := make(chan int, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+p.addr+"/api/finish", http.NoBody)
		if err != nil {
			finished <- -1
			return
		}
		req.Header.Set("Authorization", "Bearer "+p.token)
		req.Header.Set("Origin", "http://"+p.addr)
		req.Header.Set("Rvw-Generation", "1")
		req.Header.Set("Rvw-Page", testPage)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			finished <- -1
			return
		}
		_ = resp.Body.Close()
		finished <- resp.StatusCode
	}()
	// Mutations answer 410 once finish has begun; resetting the empty summary changes nothing
	// before then. Building this result takes microseconds, so after the short pause delivery
	// has started and is blocked on the full pipe.
	p.waitFor("the finish to start", func() bool {
		status, _ := p.do(http.MethodPut, "/api/summary", `{"summary":""}`)
		return status == http.StatusGone
	})
	time.Sleep(100 * time.Millisecond)
	p.signal(syscall.SIGINT)
	// Give the signal time to reach the server before the delivery unblocks. Too short a wait
	// lets the test pass vacuously, never fail spuriously.
	time.Sleep(300 * time.Millisecond)

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if code := p.exitCode(); code != 0 {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, p.stderr)
	}
	if status := <-finished; status != http.StatusOK {
		t.Errorf("finish status %d, want 200", status)
	}
	want := `{"source":{"kind":"last","uncommitted":false},` +
		`"base_tree":"` + w.git("rev-parse", "HEAD~1^{tree}") + `","head_tree":"` + w.git("rev-parse", "HEAD^{tree}") + `",` +
		`"summary":"","comments":[]}` + "\n"
	if len(out) < filler || string(out[filler:]) != want {
		t.Errorf("stdout after the %d filler bytes: %q; want %q", filler, out[min(filler, len(out)):], want)
	}
	if resumed, _ := w.inspectDraft("last"); resumed {
		t.Error("a delivered review left a draft")
	}
}

// secondSignalLimit bounds the exit after a second signal while Shutdown is held, well inside the
// server's 5-second shutdown timeout.
const secondSignalLimit = 3 * time.Second

// TestSecondSignalAfterDelivery: a second signal after the result was delivered, while Shutdown is
// still waiting, exits 0 with the whole result and reports no failure.
func TestSecondSignalAfterDelivery(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	seed := w.seedFile("seed.json", agentRangeSeed)
	p := w.start(procOptions{}, "branch", "main", "--seed", seed)
	p.waitURL()
	reviewFeature(p, "Looks good.")
	want := featureMarkdown(w)
	p.holdShutdown()
	p.finish()
	p.waitFor("the delivered result", func() bool { return p.stdout.String() == want })
	p.signalTwice("second signal after delivery", syscall.SIGINT, 0, secondSignalLimit)
	if got := p.stdout.String(); got != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, want)
	}
	for _, bad := range []string{"interrupted", "draft is kept", "stopped while writing"} {
		if strings.Contains(p.stderr.String(), bad) {
			t.Errorf("stderr reports %q after a delivered review:\n%s", bad, p.stderr)
		}
	}
	if resumed, _ := w.inspectDraft("branch", "main"); resumed {
		t.Error("the draft survived a delivered review")
	}
}

// TestSecondSignalAfterCancel: a second signal after a cancel, while Shutdown is still waiting,
// exits 3 with nothing on stdout and no draft (REQ-SES-005).
func TestSecondSignalAfterCancel(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	p := w.start(procOptions{}, "last")
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"draft"}`, http.StatusNoContent)
	p.holdShutdown()
	p.must(http.MethodPost, "/api/cancel", "", http.StatusNoContent)
	p.signalTwice("second signal after cancel", syscall.SIGINT, 3, secondSignalLimit)
	if out := p.stdout.String(); out != "" {
		t.Errorf("stdout after cancel: %q", out)
	}
	if n := countRecords(p.stderr.String(), "review cancelled"); n != 1 {
		t.Errorf("stderr reports the cancel %d times; want once:\n%s", n, p.stderr)
	}
	if resumed, _ := w.inspectDraft("last"); resumed {
		t.Error("the draft survived a cancel")
	}
}

// TestSecondSignalDuringDelivery: a second signal while the result is being written exits 1 and
// keeps the draft, since part of the result may be on stdout (REQ-SES-007). The design it replaced
// also exited 1 here; the test guards against mapping an undelivered review to the signal status.
func TestSecondSignalDuringDelivery(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Fill the pipe so the child's write blocks; the test never reads it.
	if err := wr.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	for chunk := make([]byte, 4096); ; {
		if _, err := wr.Write(chunk); err != nil {
			break
		}
	}
	if err := wr.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	p := w.start(procOptions{stdout: wr}, "last")
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"kept"}`, http.StatusNoContent)
	drafts, err := filepath.Glob(filepath.Join(w.home, "Library", "Application Support", "rvw", "drafts", "*.json"))
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts %v (%v), want exactly one", drafts, err)
	}
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+p.addr+"/api/finish", http.NoBody)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+p.token)
		req.Header.Set("Origin", "http://"+p.addr)
		req.Header.Set("Rvw-Generation", "1")
		req.Header.Set("Rvw-Page", testPage)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	// Mutations answer 410 once finish has begun; repeating the saved summary changes nothing
	// before then. Building this result takes microseconds, so after the pause delivery has
	// started and is blocked on the full pipe.
	p.waitFor("the finish to start", func() bool {
		status, _ := p.do(http.MethodPut, "/api/summary", `{"summary":"kept"}`)
		return status == http.StatusGone
	})
	time.Sleep(100 * time.Millisecond)
	p.signalTwice("second signal during delivery", syscall.SIGINT, 1, 10*time.Second)
	const keptMsg = "the review result was not delivered; the draft is kept"
	if rec, ok := logRecord(p.stderr.String(), keptMsg); !ok || rec["draft"] != drafts[0] {
		t.Errorf("stderr does not say %q naming draft %s:\n%s", keptMsg, drafts[0], p.stderr)
	}
	if resumed, s := w.inspectDraft("last"); !resumed || s.Summary != "kept" {
		t.Errorf("after a second signal during delivery: resumed=%v summary %q; want the draft kept", resumed, s.Summary)
	}
}

// startIdleHeld starts a review with a saved draft and a held Shutdown.
func startIdleHeld(t *testing.T) (*world, *proc) {
	t.Helper()
	w := featureWorld(t)
	p := w.start(procOptions{}, "last")
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"kept"}`, http.StatusNoContent)
	p.holdShutdown()
	return w, p
}

// TestSecondSignalWhileIdle: a second signal before any finish exits with the signal status,
// writes nothing and keeps the draft. It passes on the design it replaced too; it guards the
// interrupted path.
func TestSecondSignalWhileIdle(t *testing.T) {
	t.Parallel()
	w, p := startIdleHeld(t)
	p.signalTwice("second signal while idle", syscall.SIGINT, 130, secondSignalLimit)
	if out := p.stdout.String(); out != "" {
		t.Errorf("stdout after the signals: %q", out)
	}
	if resumed, s := w.inspectDraft("last"); !resumed || s.Summary != "kept" {
		t.Errorf("after two signals: resumed=%v summary %q; want the draft kept", resumed, s.Summary)
	}
}

// TestSecondSignalMixedWhileIdle: the first signal decides the signal status, so SIGINT then
// SIGTERM exits 130.
func TestSecondSignalMixedWhileIdle(t *testing.T) {
	t.Parallel()
	_, p := startIdleHeld(t)
	p.signalTwice("SIGINT then SIGTERM while idle", syscall.SIGTERM, 130, secondSignalLimit)
}

// TestSecondInstance checks that a second rvw for the same snapshot exits 1 naming the first
// session's URL, and that another snapshot is independent.
func TestSecondInstance(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	first := w.start(procOptions{}, "branch", "main")
	first.waitURL()

	second := w.run("branch", "main")
	if code := second.cmd.ProcessState.ExitCode(); code != 1 {
		t.Errorf("second instance exit %d, want 1", code)
	}
	if !strings.Contains(second.stderr.String(), first.url) {
		t.Errorf("second instance stderr does not name %s:\n%s", first.url, second.stderr)
	}

	other := w.start(procOptions{}, "last")
	other.waitURL()
	other.must(http.MethodPost, "/api/cancel", "", http.StatusNoContent)
	if code := other.exitCode(); code != 3 {
		t.Errorf("other snapshot exit %d, want 3", code)
	}
	first.must(http.MethodPost, "/api/cancel", "", http.StatusNoContent)
	if code := first.exitCode(); code != 3 {
		t.Errorf("first instance exit %d, want 3", code)
	}
}

// TestBrowserOpenFails checks that a failing browser open is reported with the URL and the
// session keeps serving.
const openFailMsg = "could not open the browser; open the URL yourself"

func TestBrowserOpenFails(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	p := w.start(procOptions{extra: []string{"RVW_TEST_OPEN_FAIL=1"}}, "last")
	p.waitURL()
	p.waitFor("the open failure", func() bool {
		_, ok := logRecord(p.stderr.String(), openFailMsg)
		return ok
	})
	if rec, _ := logRecord(p.stderr.String(), openFailMsg); !strings.Contains(rec["err"], openFailMarker) || rec["url"] != p.url {
		t.Errorf("open failure record %v, want open's output and the URL %s", rec, p.url)
	}
	p.session()
	p.must(http.MethodPost, "/api/cancel", "", http.StatusNoContent)
	if code := p.exitCode(); code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
}

// repoState captures everything a review must leave untouched (NFR: refs, index, worktree,
// configuration).
func (w *world) repoState() map[string]string {
	w.t.Helper()
	gitDir := filepath.Join(w.repo, ".git")
	st := map[string]string{
		"status": w.git("--no-optional-locks", "status", "--porcelain=v2", "--branch", "--untracked-files=all"),
		// Retention refs are the one ref change a finished session makes (INV-PLAT-004).
		"refs":   w.git("for-each-ref", "--format=%(refname) %(objectname)", "--exclude=refs/rvw/"),
		"index":  readFile(w.t, filepath.Join(gitDir, "index")),
		"HEAD":   readFile(w.t, filepath.Join(gitDir, "HEAD")),
		"config": readFile(w.t, filepath.Join(gitDir, "config")),
	}
	err := filepath.WalkDir(w.repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == gitDir {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		rel, _ := filepath.Rel(w.repo, path)
		st["file "+rel] = info.Mode().String() + " " + hex.EncodeToString(sum[:]) + " " + info.ModTime().String()
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

// TestRepositoryUnchanged checks that a full session over committed, staged, unstaged and
// untracked changes leaves refs outside refs/rvw/, index, worktree and configuration as they were,
// and adds one retention ref.
func TestRepositoryUnchanged(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	w.write("staged.txt", "staged\n")
	w.git("add", "staged.txt")
	w.write("a.go", featureLines()+"// unstaged\n")
	w.write("untracked.txt", "untracked\n")
	before := w.repoState()

	// A regular file: delivery to a pipe is TestFinishPrintsJSON's concern.
	out, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	p := w.start(procOptions{stdout: out}, "branch", "main", "--uncommitted")
	p.waitURL()
	p.must(http.MethodGet, "/api/file?i=0", "", http.StatusOK)
	p.createComment("c1", `{"target":{"target":"line","file":0,"side":"new","line":31},"body":"Why?"}`)
	p.must(http.MethodPut, "/api/viewed", `{"files":[0],"viewed":true}`, http.StatusNoContent)
	p.finish()
	if code := p.exitCode(); code != 0 {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, p.stderr)
	}

	after := w.repoState()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed:\nbefore %q\nafter  %q", k, v, after[k])
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("%s appeared", k)
		}
	}
	if refs := strings.Fields(w.git("for-each-ref", "--format=%(refname)", "refs/rvw/")); len(refs) != 1 {
		t.Errorf("retention refs %q, want one", refs)
	}
}

// TestArguments checks that usage errors exit 2 with the usage text, help exits 0, and the token
// after branch is the base ref when it starts with "-" but is not an option.
func TestArguments(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	for _, c := range []struct {
		args      []string
		message   string // on stderr, when set
		absent    string // not on stderr, when set
		code      int
		wantUsage bool
	}{
		{args: nil, code: 2, wantUsage: true},
		{args: []string{"bogus"}, code: 2, wantUsage: true},
		{args: []string{"last", "extra"}, code: 2, wantUsage: true},
		{args: []string{"last", "--uncommitted"}, code: 2, wantUsage: true},
		{args: []string{"uncommitted", "--uncommitted"}, code: 2, wantUsage: true},
		{args: []string{"last", "--frobnicate"}, code: 2, wantUsage: true},
		{args: []string{"last", "--seed"}, code: 2, wantUsage: true},
		{args: []string{"last", "--seed="}, code: 2, wantUsage: true},
		{args: []string{"branch", ""}, code: 2, wantUsage: true, message: "branch needs a base ref"},
		{args: []string{"-h"}, code: 0, wantUsage: true},
		{args: []string{"--help"}, code: 0, wantUsage: true},
		{args: []string{"branch", "main", "-h"}, code: 0, wantUsage: true},
		{args: []string{"branch", "--help"}, code: 0, wantUsage: true},
		// -x is not an option, so it is the base ref; it names no commit, so resolving fails.
		{args: []string{"branch", "-x"}, code: 1, wantUsage: false, message: `base ref "-x"`, absent: "unknown option"},
	} {
		p := w.run(c.args...)
		code := p.cmd.ProcessState.ExitCode()
		stderr := p.stderr.String()
		if code != c.code {
			t.Errorf("rvw %v: exit %d, want %d; stderr:\n%s", c.args, code, c.code, stderr)
		}
		if got := strings.Contains(stderr, "Usage:"); got != c.wantUsage {
			t.Errorf("rvw %v: usage on stderr = %v, want %v; stderr:\n%s", c.args, got, c.wantUsage, stderr)
		}
		if !stderrSays(stderr, c.message) {
			t.Errorf("rvw %q: stderr does not say %q:\n%s", c.args, c.message, stderr)
		}
		if c.absent != "" && strings.Contains(stderr, c.absent) {
			t.Errorf("rvw %q: stderr says %q:\n%s", c.args, c.absent, stderr)
		}
		if out := p.stdout.String(); out != "" {
			t.Errorf("rvw %v wrote stdout %q", c.args, out)
		}
	}
}

// originHeadWorld is featureWorld with origin/main at main and origin/HEAD pointing to it.
func originHeadWorld(t *testing.T) *world {
	t.Helper()
	w := featureWorld(t)
	w.git("update-ref", "refs/remotes/origin/main", "main")
	w.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return w
}

// TestBranchDefaultsToOriginHead checks that rvw branch without a base ref reviews from
// origin/HEAD's target and reports the resolved ref in the UI and the result, and that every
// spelling of the same base commit (bare, main, origin/main) shares one draft, because the key
// holds the trees only (REQ-DRF-002, Q-38).
func TestBranchDefaultsToOriginHead(t *testing.T) {
	t.Parallel()
	w := originHeadWorld(t)

	p := w.start(procOptions{}, "branch")
	p.waitURL()
	if s := p.session(); !strings.Contains(s.Source, "origin/main") {
		t.Errorf("session source %q does not name origin/main", s.Source)
	}
	p.finish()
	if code := p.exitCode(); code != 0 {
		t.Fatalf("rvw branch: exit %d, want 0; stderr:\n%s", code, p.stderr)
	}
	if out := p.stdout.String(); !strings.Contains(out, "Source: branch origin/main..HEAD") {
		t.Errorf("rvw branch: stdout does not name origin/main as the base:\n%s", out)
	}

	p = w.start(procOptions{}, "branch", "--json")
	p.waitURL()
	p.finish()
	if code := p.exitCode(); code != 0 {
		t.Fatalf("rvw branch --json: exit %d, want 0; stderr:\n%s", code, p.stderr)
	}
	if out := p.stdout.String(); !strings.Contains(out, `"branch":"origin/main"`) {
		t.Errorf("rvw branch --json: stdout does not name origin/main as the branch:\n%s", out)
	}

	// main and origin/main name the same commit, so all three spellings share one draft.
	p = w.start(procOptions{}, "branch", "main")
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"from main"}`, http.StatusNoContent)
	p.signal(syscall.SIGINT)
	if code := p.exitCode(); code != 130 {
		t.Fatalf("rvw branch main interrupted: exit %d, want 130; stderr:\n%s", code, p.stderr)
	}
	for _, args := range [][]string{{"branch", "main"}, {"branch", "origin/main"}, {"branch"}} {
		if resumed, s := w.inspectDraft(args...); !resumed || s.Summary != "from main" {
			t.Errorf("rvw %s after rvw branch main: resumed=%v summary %q; want the draft resumed", strings.Join(args, " "), resumed, s.Summary)
		}
	}

	p = w.start(procOptions{}, "branch", "origin/main")
	p.waitURL()
	p.must(http.MethodPut, "/api/summary", `{"summary":"kept"}`, http.StatusNoContent)
	p.signal(syscall.SIGINT)
	if code := p.exitCode(); code != 130 {
		t.Fatalf("rvw branch origin/main interrupted: exit %d, want 130; stderr:\n%s", code, p.stderr)
	}
	for _, args := range [][]string{{"branch"}, {"branch", "main"}} {
		if resumed, s := w.inspectDraft(args...); !resumed || s.Summary != "kept" {
			t.Errorf("rvw %s after rvw branch origin/main: resumed=%v summary %q; want the draft resumed", strings.Join(args, " "), resumed, s.Summary)
		}
	}
}

// TestBranchDefaultsToOriginHeadWithUncommitted checks that rvw branch --uncommitted without a
// base ref reviews from the merge base with origin/HEAD's target to the uncommitted tree
// (REQ-SRC-004, Q-38). The seeded comment on a file that exists only in the worktree is the
// membership probe: a snapshot without the uncommitted changes rejects the seed.
func TestBranchDefaultsToOriginHeadWithUncommitted(t *testing.T) {
	t.Parallel()
	w := originHeadWorld(t)
	w.write("u.txt", "only uncommitted\n")
	seed := w.seedFile("seed.json", `{"comments":[{"target":"line","path":"u.txt","side":"new","line":1,"body":"Check this file."}]}`)

	p := w.start(procOptions{}, "branch", "--uncommitted", "--json", "--seed", seed)
	p.waitURL()
	p.finish()
	if code := p.exitCode(); code != 0 {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, p.stderr)
	}

	out := p.stdout.String()
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("stdout does not end with a newline: %q", out)
	}
	var got struct {
		Source struct {
			Kind        string `json:"kind"`
			Branch      string `json:"branch"`
			Uncommitted bool   `json:"uncommitted"`
		} `json:"source"`
		BaseTree string `json:"base_tree"`
		HeadTree string `json:"head_tree"`
		Summary  string `json:"summary"`
		Comments []struct {
			Author string   `json:"author"`
			Target string   `json:"target"`
			Path   string   `json:"path"`
			Side   string   `json:"side"`
			Line   int      `json:"line"`
			Code   []string `json:"code"`
			Body   string   `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if got.Source.Kind != "branch" || got.Source.Branch != "origin/main" || !got.Source.Uncommitted {
		t.Errorf("source %+v, want kind branch, branch origin/main, uncommitted", got.Source)
	}
	if want := w.git("rev-parse", "main^{tree}"); got.BaseTree != want {
		t.Errorf("base_tree %s, want main's tree %s", got.BaseTree, want)
	}
	if got.HeadTree == w.git("rev-parse", "HEAD^{tree}") {
		t.Errorf("head_tree %s is HEAD's tree; want the uncommitted tree", got.HeadTree)
	}
	if s := w.git("show", got.HeadTree+":u.txt"); s != "only uncommitted" {
		t.Errorf("head tree u.txt %q, want the uncommitted content", s)
	}
	if s := w.git("show", got.HeadTree+":a.go"); s != strings.TrimSpace(featureLines()) {
		t.Errorf("head tree a.go is not the committed branch content:\n%s", s)
	}
	if got.Summary != "" {
		t.Errorf("summary %q, want empty", got.Summary)
	}
	if len(got.Comments) != 1 {
		t.Fatalf("comments %+v, want the one seeded comment", got.Comments)
	}
	c := got.Comments[0]
	if c.Author != "agent" || c.Target != "line" || c.Path != "u.txt" || c.Side != "new" || c.Line != 1 ||
		len(c.Code) != 1 || c.Code[0] != "only uncommitted" || c.Body != "Check this file." {
		t.Errorf("comment %+v, want the seeded agent line comment on u.txt quoting %q", c, "only uncommitted")
	}
}

// TestBranchWithoutOriginHead checks that rvw branch without a base ref fails before serving
// when origin/HEAD is not set, and says to name a base ref.
func TestBranchWithoutOriginHead(t *testing.T) {
	t.Parallel()
	w := featureWorld(t)
	p := w.run("branch")
	stderr := p.stderr.String()
	if code := p.cmd.ProcessState.ExitCode(); code != 1 {
		t.Errorf("exit %d, want 1; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"origin/HEAD is not set", "name a base ref"} {
		if !stderrSays(stderr, want) {
			t.Errorf("stderr does not say %q:\n%s", want, stderr)
		}
	}
	if urlPattern.MatchString(stderr) {
		t.Errorf("rvw served a review page:\n%s", stderr)
	}
	if _, err := os.Stat(w.openLog); !os.IsNotExist(err) {
		t.Errorf("rvw opened a browser (open log: %v)", err)
	}
	if out := p.stdout.String(); out != "" {
		t.Errorf("stdout %q, want empty", out)
	}
}

// TestUnresolvableSource checks REQ-SRC-007: each unreviewable source exits 1 with its reason on
// stderr, before serving or opening the review page.
func TestUnresolvableSource(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		setup  func(w *world)
		name   string
		reason string
		args   []string
	}{
		{
			name: "not inside a worktree", args: []string{"last"}, reason: "not inside a Git worktree",
			setup: func(w *world) {
				if err := os.RemoveAll(filepath.Join(w.repo, ".git")); err != nil {
					w.t.Fatal(err)
				}
			},
		},
		{name: "unborn HEAD", args: []string{"last"}, reason: "HEAD is unborn", setup: func(*world) {}},
		{
			name: "no changes", args: []string{"uncommitted"}, reason: "has no changes",
			setup: func(w *world) { w.write("a.txt", "a\n"); w.commit("only") },
		},
		{
			name: "base ref does not resolve", args: []string{"branch", "no-such-ref"},
			reason: `base ref "no-such-ref" does not resolve to a commit`,
			setup:  func(w *world) { w.write("a.txt", "a\n"); w.commit("only") },
		},
		{
			name: "no merge base", args: []string{"branch", "main"}, reason: `no merge base between "main" and HEAD`,
			setup: func(w *world) {
				w.write("a.txt", "a\n")
				w.commit("main root")
				w.git("checkout", "-q", "--orphan", "other")
				w.write("b.txt", "b\n")
				w.commit("other root")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			tc.setup(w)
			p := w.run(tc.args...)
			stderr := p.stderr.String()
			if code := p.cmd.ProcessState.ExitCode(); code != 1 {
				t.Errorf("exit %d, want 1; stderr:\n%s", code, stderr)
			}
			if !stderrSays(stderr, tc.reason) {
				t.Errorf("stderr does not say %q:\n%s", tc.reason, stderr)
			}
			if urlPattern.MatchString(stderr) {
				t.Errorf("rvw served a review page:\n%s", stderr)
			}
			if _, err := os.Stat(w.openLog); !os.IsNotExist(err) {
				t.Errorf("rvw opened a browser (open log: %v)", err)
			}
			if out := p.stdout.String(); out != "" {
				t.Errorf("stdout %q, want empty", out)
			}
		})
	}
}
