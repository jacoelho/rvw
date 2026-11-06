package server_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/round"
	"github.com/jacoelho/rvw/internal/server"
	"github.com/jacoelho/rvw/internal/source"
)

// gitEnv isolates every git process, including those the source package starts in-process, from
// the developer's configuration and pins commit identities and dates.
var gitEnv = map[string]string{
	"GIT_CONFIG_GLOBAL":   "/dev/null",
	"GIT_CONFIG_NOSYSTEM": "1",
	// No detached auto-maintenance writing into fixtures after a test returns.
	"GIT_CONFIG_COUNT":    "2",
	"GIT_CONFIG_KEY_0":    "maintenance.auto",
	"GIT_CONFIG_VALUE_0":  "false",
	"GIT_CONFIG_KEY_1":    "gc.auto",
	"GIT_CONFIG_VALUE_1":  "0",
	"GIT_AUTHOR_NAME":     "Test Author",
	"GIT_AUTHOR_EMAIL":    "author@example.com",
	"GIT_AUTHOR_DATE":     "2026-01-02T03:04:05Z",
	"GIT_COMMITTER_NAME":  "Test Committer",
	"GIT_COMMITTER_EMAIL": "committer@example.com",
	"GIT_COMMITTER_DATE":  "2026-01-02T03:04:05Z",
}

func TestMain(m *testing.M) {
	// Set once for the process: t.Setenv would forbid parallel tests, and the source package's
	// git runner inherits os.Environ.
	for k, v := range gitEnv {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// testRepo is a temporary Git repository driven by the git executable.
type testRepo struct {
	t   *testing.T
	dir string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	out, err := r.gitBytes(args...)
	if err != nil {
		r.t.Fatal(err)
	}
	return strings.TrimRight(string(out), "\n")
}

func (r *testRepo) gitBytes(args ...string) ([]byte, error) {
	cmd := exec.CommandContext(context.WithoutCancel(r.t.Context()), "git", append([]string{"-C", r.dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return out, nil
}

func (r *testRepo) write(path string, data string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) remove(path string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.dir, path)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
}

// blob returns the content git stores for rev:path.
func (r *testRepo) blob(rev, path string) string {
	r.t.Helper()
	out, err := r.gitBytes("cat-file", "blob", rev+":"+path)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(out)
}

// numberedLines returns "<prefix> 1\n" .. "<prefix> n\n".
func numberedLines(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s %d\n", prefix, i)
	}
	return b.String()
}

// deliveries records what Deliver received. A hook, when set, runs after the result is recorded,
// outside the lock, and its error is the delivery's.
type deliveries struct {
	err     error
	hook    func() error
	results []review.Result
	mu      sync.Mutex
}

func (d *deliveries) deliver(r review.Result) error {
	d.mu.Lock()
	d.results = append(d.results, r)
	d.mu.Unlock()
	if d.hook != nil {
		return d.hook()
	}
	return d.err
}

func (d *deliveries) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.results)
}

// fixture is a server over a real snapshot and a real session whose draft lives in a temp dir.
type fixture struct {
	t         *testing.T
	repo      *testRepo
	git       *source.Repo // the session's repository, also the retention refs' writer
	snap      *source.Snapshot
	sess      *review.Session
	store     *review.Store
	srv       *server.Server
	delivered *deliveries
	addr      string
	token     string
}

const testToken = "test-token-0123456789"

// fixtureSeq makes each fixture's token unique, so a request can tell this fixture's server from
// another parallel test's server that reuses a freed port.
var fixtureSeq atomic.Int64

const indexHTML = "<!doctype html><title>rvw test</title>"

type fixtureOptions struct {
	deliverErr  error
	deliverHook func() error // see deliveries.hook
	src         source.Source
	seed        string        // a seed document; empty for none
	finishWait  time.Duration // Config.FinishWait
	closeGrace  time.Duration // Config.CloseGrace
	// ledger, when set, is loaded at start as rvw does: the prior rounds, the preset viewed marks
	// and the Hide whitespace setting come from it. Nil is an empty ledger.
	ledger *round.Store
	// log, when set, receives the server's log lines.
	log io.Writer
}

// seedOf parses a seed document the way rvw's --seed does.
func seedOf(tb testing.TB, doc string) review.Seed {
	tb.Helper()
	if doc == "" {
		return review.Seed{}
	}
	seed, err := review.ParseSeed(strings.NewReader(doc))
	if err != nil {
		tb.Fatal(err)
	}
	return seed
}

func newFixture(t *testing.T, repo *testRepo, opts fixtureOptions) *fixture {
	t.Helper()
	ctx := t.Context()
	r, err := source.Open(ctx, repo.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close repo: %v", err)
		}
	})
	src := opts.src
	if src == (source.Source{}) {
		src = source.Last()
	}
	snap, err := r.Resolve(ctx, src, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	store, err := review.OpenStore(t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	var ledger round.Ledger
	if opts.ledger != nil {
		if ledger, err = opts.ledger.Load(time.Now()); err != nil {
			t.Fatalf("load ledger: %v", err)
		}
	}
	prior := round.NewPrior(snap, ledger)
	fresh := review.Fresh{Seed: seedOf(t, opts.seed), Viewed: prior.PresetMarks(), HideWhitespace: prior.HideWhitespace()}
	sess, _, err := review.Start(ctx, store, snap, fresh)
	if err != nil {
		t.Fatal(err)
	}
	token := fmt.Sprintf("%s-%d", testToken, fixtureSeq.Add(1))
	f := &fixture{t: t, repo: repo, git: r, snap: snap, sess: sess, store: store, token: token,
		delivered: &deliveries{err: opts.deliverErr, hook: opts.deliverHook}}
	logOut := io.Discard
	if opts.log != nil {
		logOut = opts.log
	}
	f.srv = server.New(server.Config{
		Assets:  fstest.MapFS{"index.html": {Data: []byte(indexHTML)}},
		Session: sess, Ledger: ledger, Token: token, Deliver: f.delivered.deliver,
		Log: slog.New(slog.NewTextHandler(logOut, nil)), FinishWait: opts.finishWait, CloseGrace: opts.closeGrace,
	})
	return f
}

// serveHandler serves the handler without Run: the session never shuts the server down.
func (f *fixture) serveHandler() {
	f.t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	f.addr = ts.Listener.Addr().String()
	ts.Config.Handler = f.srv.HandlerForTest(f.addr)
	ts.Start()
	f.t.Cleanup(ts.Close)
}

type runResult struct {
	err     error
	outcome server.Outcome
}

// serveRun serves through Run on a loopback listener. The returned channel receives Run's result;
// stop cancels Run's context.
func (f *fixture) serveRun() (done <-chan runResult, stop context.CancelFunc) {
	f.t.Helper()
	ctx, cancel := context.WithCancel(f.t.Context())
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatal(err)
	}
	f.addr = ln.Addr().String()
	ch := make(chan runResult, 1)
	returned := make(chan struct{})
	go func() {
		o, err := f.srv.Run(ctx, ln)
		ch <- runResult{outcome: o, err: err}
		close(returned)
	}()
	f.t.Cleanup(func() {
		cancel()
		select {
		case <-returned:
		case <-time.After(30 * time.Second):
			f.t.Error("Run did not return after its context was cancelled")
		}
	})
	return ch, cancel
}

func waitRun(t *testing.T, done <-chan runResult) runResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return")
		return runResult{}
	}
}

// reqOption adjusts a request before it is sent.
type reqOption func(*http.Request)

func withoutToken() reqOption { return func(r *http.Request) { r.Header.Del("Authorization") } }

func withToken(tok string) reqOption {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

// withForeignHost sends another site's Host header, as a DNS-rebinding page would.
func withForeignHost() reqOption { return func(r *http.Request) { r.Host = "evil.example:80" } }

func withOrigin(o string) reqOption {
	return func(r *http.Request) {
		if o == "" {
			r.Header.Del("Origin")
			return
		}
		r.Header.Set("Origin", o)
	}
}

func withSecFetchSite(v string) reqOption { return withHeader("Sec-Fetch-Site", v) }

func withHeader(k, v string) reqOption { return func(r *http.Request) { r.Header.Set(k, v) } }

// createOnly is the PUT /api/comments precondition that creates a comment that must not exist.
var createOnly = withHeader("If-None-Match", "*")

// editAt is the PUT /api/comments precondition that edits comment version v.
func editAt(v int64) reqOption { return withHeader("If-Match", fmt.Sprintf("%q", fmt.Sprint(v))) }

func withContentType(ct string) reqOption {
	return func(r *http.Request) { r.Header.Set("Content-Type", ct) }
}

type response struct {
	header http.Header
	body   []byte
	status int
}

// do sends an authorised same-origin request; options remove or replace parts of it.
func (f *fixture) do(method, path, body string, opts ...reqOption) response {
	f.t.Helper()
	r, err := f.send(method, path, body, opts...)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) send(method, path, body string, opts ...reqOption) (response, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(f.t.Context(), method, "http://"+f.addr+path, rd)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", "http://"+f.addr)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, err
	}
	return response{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

func (f *fixture) mustStatus(r response, want int, what string) {
	f.t.Helper()
	if r.status != want {
		f.t.Fatalf("%s: status %d, want %d; body %q", what, r.status, want, r.body)
	}
}

// Wire shapes the tests read. They decode leniently: members not named here are not asserted.
type sessionDTO struct {
	Viewed   []int        `json:"viewed"`
	Source   string       `json:"source"`
	Summary  string       `json:"summary"`
	Files    []fileDTO    `json:"files"`
	Comments []commentDTO `json:"comments"`

	RenameDetection  string `json:"rename_detection"`
	StatusIntervalMs int64  `json:"status_interval_ms"`
}

type fileDTO struct {
	Status      string `json:"status"`
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	VendoredDir string `json:"vendored_dir"`
	Commentable bool   `json:"commentable"`
}

type targetDTO struct {
	Target string `json:"target"`
	Path   string `json:"path,omitzero"`
	Side   string `json:"side,omitzero"`
	Line   int    `json:"line,omitzero"`
	Start  int    `json:"start,omitzero"`
	End    int    `json:"end,omitzero"`
}

type commentDTO struct {
	ID      string    `json:"id"`
	Author  string    `json:"author"`
	Body    string    `json:"body"`
	Target  targetDTO `json:"target"`
	Version int64     `json:"version"`
}

// placementDTO is where a create request places a comment: a file index, never a path.
type placementDTO struct {
	File   *int   `json:"file,omitzero"`
	Target string `json:"target"`
	Side   string `json:"side,omitzero"`
	Line   int    `json:"line,omitzero"`
	Start  int    `json:"start,omitzero"`
	End    int    `json:"end,omitzero"`
}

func (f *fixture) session() sessionDTO {
	f.t.Helper()
	r := f.do(http.MethodGet, "/api/session", "")
	f.mustStatus(r, http.StatusOK, "GET /api/session")
	var s sessionDTO
	decode(f.t, r.body, &s)
	return s
}

// fileIndex returns the index of the changed file whose display path is p.
func (s sessionDTO) fileIndex(t *testing.T, p string) int {
	t.Helper()
	for i, f := range s.Files {
		if f.NewPath == p || f.NewPath == "" && f.OldPath == p {
			return i
		}
	}
	t.Fatalf("no changed file %q in %+v", p, s.Files)
	return -1
}

func decode(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	// Deterministic: callers compare encodings, and v2 orders map members randomly by default.
	data, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// seedComment is one comment of a seed document.
type seedComment struct {
	targetDTO
	Body string `json:"body"`
}

func seedDoc(tb testing.TB, comments ...seedComment) string {
	tb.Helper()
	data, err := json.Marshal(map[string][]seedComment{"comments": comments})
	if err != nil {
		tb.Fatal(err)
	}
	return string(data)
}

func putCommentBody(t *testing.T, target placementDTO, body string) string {
	t.Helper()
	return mustJSON(t, map[string]any{"target": target, "body": body})
}

// placement places t by the index of the changed file whose path on t's side is t.Path, or whose
// display path is t.Path for a file target or a side the file lacks.
func (s sessionDTO) placement(tb testing.TB, t targetDTO) placementDTO {
	tb.Helper()
	p := placementDTO{Target: t.Target, Side: t.Side, Line: t.Line, Start: t.Start, End: t.End}
	if t.Target == "review" {
		return p
	}
	i := -1
	for k, f := range s.Files {
		if t.Side == "old" && f.OldPath == t.Path || t.Side == "new" && f.NewPath == t.Path {
			i = k
		}
	}
	if i < 0 {
		for k, f := range s.Files {
			if f.NewPath == t.Path || f.NewPath == "" && f.OldPath == t.Path {
				i = k
			}
		}
	}
	if i < 0 {
		tb.Fatalf("no changed file %q in %+v", t.Path, s.Files)
	}
	p.File = &i
	return p
}

// version returns the version of comment id the session reports.
func (s sessionDTO) version(tb testing.TB, id string) int64 {
	tb.Helper()
	for _, c := range s.Comments {
		if c.ID == id {
			return c.Version
		}
	}
	tb.Fatalf("no comment %q in %+v", id, s.Comments)
	return 0
}
