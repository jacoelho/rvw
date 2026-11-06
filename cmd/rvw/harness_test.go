//go:build darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Paths set by TestMain: the rvw binary under test and a directory holding the fake open.
var (
	rvwBin  string
	fakeBin string
)

// fakeOpen stands in for macOS open: it records the URL, or fails when RVW_TEST_OPEN_FAIL is set.
const fakeOpen = `#!/bin/sh
if [ -n "$RVW_TEST_OPEN_FAIL" ]; then
	echo "open-failed-marker" >&2
	exit 1
fi
printf '%s\n' "$1" >> "$RVW_TEST_OPEN_LOG"
`

const openFailMarker = "open-failed-marker"

// waitLimit bounds every wait on a child process or its output.
const waitLimit = 30 * time.Second

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "rvw-process-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	defer stopSharedBrowser()
	rvwBin = filepath.Join(dir, "rvw")
	build := exec.CommandContext(context.Background(), "go", "build", "-o", rvwBin, ".")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build rvw:", err)
		return 1
	}
	fakeBin = filepath.Join(dir, "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "open"), []byte(fakeOpen), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

// gitVars isolates git from the user's configuration and fixes identity and dates.
// Commits would start Git's detached auto-maintenance, which writes into fixture repositories
// after the test returns (repacking objects tests delete, racing TempDir cleanup).
var gitVars = []string{
	"GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_CONFIG_COUNT=2",
	"GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=false",
	"GIT_CONFIG_KEY_1=gc.auto", "GIT_CONFIG_VALUE_1=0",
	"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
	"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	"GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z",
}

// world is one isolated environment: a HOME (so drafts land in a temp dir), a repository and
// the log the fake open writes.
type world struct {
	t       *testing.T
	home    string
	repo    string
	openLog string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{t: t, home: filepath.Join(root, "home"), repo: filepath.Join(root, "repo"),
		openLog: filepath.Join(root, "open.log")}
	for _, d := range []string{w.home, w.repo} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w.git("init", "-q", "-b", "main")
	return w
}

// env is the child environment: inherited variables minus anything that redirects git, HOME or
// PATH, plus the isolated values.
func (w *world) env(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "RVW_TEST_") ||
			k == "HOME" || k == "PATH" || k == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, gitVars...)
	env = append(env, "HOME="+w.home, "PATH="+fakeBin+":"+os.Getenv("PATH"), "RVW_TEST_OPEN_LOG="+w.openLog)
	return append(env, extra...)
}

func (w *world) git(args ...string) string {
	w.t.Helper()
	cmd := exec.CommandContext(context.WithoutCancel(w.t.Context()), "git", append([]string{"-C", w.repo}, args...)...)
	cmd.Env = w.env()
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			stderr = ee.Stderr
		}
		w.t.Fatalf("git %v: %v: %s", args, err, stderr)
	}
	return strings.TrimSpace(string(out))
}

func (w *world) write(path, data string) {
	w.t.Helper()
	full := filepath.Join(w.repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) commit(msg string) {
	w.t.Helper()
	w.git("add", "-A")
	w.git("commit", "-q", "--allow-empty", "-m", msg)
}

// seedFile writes a seed document outside the repository and returns its path.
func (w *world) seedFile(name, doc string) string {
	w.t.Helper()
	p := filepath.Join(w.home, name)
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		w.t.Fatal(err)
	}
	return p
}

// lockedBuffer collects a child's stderr while the test polls it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// proc is one running rvw.
type proc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stderr *lockedBuffer
	stdout *lockedBuffer // nil when the caller supplied stdout
	done   chan struct{}
	url    string
	addr   string
	token  string
}

type procOptions struct {
	stdout io.Writer // default: a buffer, so stdout is a pipe
	extra  []string  // extra environment
}

func (w *world) start(opts procOptions, args ...string) *proc {
	w.t.Helper()
	// A test that fails early still kills the child when its context ends.
	cmd := exec.CommandContext(w.t.Context(), rvwBin, args...)
	cmd.Dir = w.repo
	cmd.Env = w.env(opts.extra...)
	p := &proc{t: w.t, cmd: cmd, stderr: &lockedBuffer{}, done: make(chan struct{})}
	cmd.Stderr = p.stderr
	if opts.stdout != nil {
		cmd.Stdout = opts.stdout
	} else {
		p.stdout = &lockedBuffer{}
		cmd.Stdout = p.stdout
	}
	if err := cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	go func() {
		_ = cmd.Wait() // the exit status is read from ProcessState
		close(p.done)
	}()
	w.t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// run starts rvw and waits for it to exit, for invocations that never serve.
func (w *world) run(args ...string) *proc {
	w.t.Helper()
	p := w.start(procOptions{}, args...)
	p.exitCode()
	return p
}

var urlPattern = regexp.MustCompile(`rvw: review at (http://(127\.0\.0\.1:\d+)/#token=(\S+))`)

// waitFor polls until cond holds, the process exits, or the deadline passes.
func (p *proc) waitFor(what string, cond func() bool) {
	p.t.Helper()
	deadline := time.NewTimer(waitLimit)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case <-p.done:
			if cond() {
				return
			}
			p.t.Fatalf("rvw exited (%d) before %s; stderr:\n%s", p.cmd.ProcessState.ExitCode(), what, p.stderr)
		case <-deadline.C:
			p.t.Fatalf("timed out waiting for %s; stderr:\n%s", what, p.stderr)
		case <-tick.C:
		}
	}
}

// waitURL waits for the review URL on stderr and records its address and token.
func (p *proc) waitURL() {
	p.t.Helper()
	var m []string
	p.waitFor("the review URL", func() bool {
		m = urlPattern.FindStringSubmatch(p.stderr.String())
		return m != nil
	})
	p.url, p.addr, p.token = m[1], m[2], m[3]
}

func (p *proc) exitCode() int {
	p.t.Helper()
	select {
	case <-p.done:
		return p.cmd.ProcessState.ExitCode()
	case <-time.After(waitLimit):
		p.t.Fatalf("rvw did not exit; stderr:\n%s", p.stderr)
		return -1
	}
}

func (p *proc) signal(sig syscall.Signal) {
	p.t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		p.t.Fatal(err)
	}
}

// holdShutdown opens a connection that sends the complete headers of an authorised PUT
// /api/summary with Expect: 100-continue but never its body, and returns once the server has
// answered 100 Continue. net/http sends that only on the handler's first body read, which follows
// the token, Origin and Content-Type checks, so on return the handler is running and blocked on the
// body: the connection is active and the server's Shutdown waits its full timeout before rvw can
// report an outcome.
func (p *proc) holdShutdown() {
	p.t.Helper()
	var d net.Dialer
	conn, err := d.DialContext(p.t.Context(), "tcp", p.addr)
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { _ = conn.Close() })
	head := "PUT /api/summary HTTP/1.1\r\n" +
		"Host: " + p.addr + "\r\n" +
		"Authorization: Bearer " + p.token + "\r\n" +
		"Origin: http://" + p.addr + "\r\n" +
		"Content-Type: application/json\r\n" +
		"Expect: 100-continue\r\n" +
		"Content-Length: 100\r\n\r\n"
	if _, err := io.WriteString(conn, head); err != nil {
		p.t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(waitLimit)); err != nil {
		p.t.Fatal(err)
	}
	// A refused request (401, 403, 415) gets a final status and no 100.
	rd := bufio.NewReader(conn)
	status, err := rd.ReadString('\n')
	if err != nil {
		p.t.Fatalf("holdShutdown: reading the interim response: %v (read %q)", err, status)
	}
	if got := strings.TrimRight(status, "\r\n"); got != "HTTP/1.1 100 Continue" {
		p.t.Fatalf("holdShutdown: status line %q, want HTTP/1.1 100 Continue", got)
	}
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			p.t.Fatalf("holdShutdown: reading the interim response: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		p.t.Fatal(err)
	}
}

// signalTwice sends SIGINT, checks that rvw is still running a moment later, then sends then
// every 50 ms until rvw exits. It fails the test unless rvw exits with want within limit of the
// first signal.
func (p *proc) signalTwice(scenario string, then syscall.Signal, want int, limit time.Duration) {
	p.t.Helper()
	start := time.Now()
	p.signal(syscall.SIGINT)
	select {
	case <-p.done:
		p.t.Fatalf("%s: rvw exited %d %v after the first signal, before the second; stderr:\n%s",
			scenario, p.cmd.ProcessState.ExitCode(), time.Since(start), p.stderr)
	case <-time.After(300 * time.Millisecond):
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(waitLimit)
	defer deadline.Stop()
	for {
		_ = p.cmd.Process.Signal(then) // fails only once rvw has exited
		select {
		case <-p.done:
			elapsed := time.Since(start)
			if code := p.cmd.ProcessState.ExitCode(); code != want || elapsed >= limit {
				p.t.Fatalf("%s: exit %d after %v; want %d within %v; stderr:\n%s", scenario, code, elapsed, want, limit, p.stderr)
			}
			return
		case <-deadline.C:
			p.t.Fatalf("%s: rvw did not exit %v after the first signal; stderr:\n%s", scenario, waitLimit, p.stderr)
		case <-tick.C:
		}
	}
}

// do sends an authorised same-origin API request.
func (p *proc) do(method, path, body string) (int, []byte) {
	p.t.Helper()
	return p.doWith(method, path, body, nil)
}

// createComment creates comment id; the precondition refuses an id already taken.
func (p *proc) createComment(id, body string) {
	p.t.Helper()
	path := "/api/comments/" + id
	if status, data := p.doWith(http.MethodPut, path, body, http.Header{"If-None-Match": {"*"}}); status != http.StatusOK {
		p.t.Fatalf("PUT %s: status %d, want 200; body %s", path, status, data)
	}
}

func (p *proc) doWith(method, path, body string, header http.Header) (int, []byte) {
	p.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(p.t.Context(), method, "http://"+p.addr+path, rd)
	if err != nil {
		p.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	if method != http.MethodGet {
		req.Header.Set("Origin", "http://"+p.addr)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	maps.Copy(req.Header, header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		p.t.Fatal(err)
	}
	return resp.StatusCode, data
}

func (p *proc) must(method, path, body string, want int) []byte {
	p.t.Helper()
	status, data := p.do(method, path, body)
	if status != want {
		p.t.Fatalf("%s %s: status %d, want %d; body %s", method, path, status, want, data)
	}
	return data
}

type sessionView struct {
	Viewed   []int  `json:"viewed"`
	Source   string `json:"source"`
	Summary  string `json:"summary"`
	Comments []struct {
		ID     string `json:"id"`
		Author string `json:"author"`
		Body   string `json:"body"`
	} `json:"comments"`
}

func (p *proc) session() sessionView {
	p.t.Helper()
	var s sessionView
	if err := json.Unmarshal(p.must(http.MethodGet, "/api/session", "", http.StatusOK), &s); err != nil {
		p.t.Fatal(err)
	}
	return s
}

const resumedNotice = "rvw: resuming the saved draft"

// inspectDraft reruns rvw with args, reports whether it resumed a draft and what the session
// holds, and interrupts it again (which keeps any draft as it was).
func (w *world) inspectDraft(args ...string) (resumed bool, s sessionView) {
	w.t.Helper()
	p := w.start(procOptions{}, args...)
	p.waitURL()
	s = p.session()
	p.signal(syscall.SIGINT)
	if code := p.exitCode(); code != 130 {
		w.t.Fatalf("inspecting rerun exited %d; stderr:\n%s", code, p.stderr)
	}
	return strings.Contains(p.stderr.String(), resumedNotice), s
}
