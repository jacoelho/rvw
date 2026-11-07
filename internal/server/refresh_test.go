package server_test

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/source"
)

// status polls GET /api/status, with check=1 when check is set.
func (f *fixture) status(check bool) statusDTO {
	f.t.Helper()
	path := "/api/status"
	if check {
		path += "?check=1"
	}
	r := f.do(http.MethodGet, path, "")
	f.mustStatus(r, http.StatusOK, "GET "+path)
	var st statusDTO
	decode(f.t, r.body, &st)
	return st
}

// refresh posts a refresh naming the fixture's generation and moves the fixture to the reply's.
func (f *fixture) refresh() refreshDTO {
	f.t.Helper()
	r := f.do(http.MethodPost, "/api/refresh", "")
	f.mustStatus(r, http.StatusOK, "POST /api/refresh")
	var out refreshDTO
	decode(f.t, r.body, &out)
	f.gen = out.Generation
	return out
}

type refreshDTO struct {
	Moved      []movedDTO `json:"moved"`
	Generation uint64     `json:"generation"`
	Unchanged  bool       `json:"unchanged"`
}

// uncommittedRepo has a.txt committed as "line 1".."line 5" and edited in the worktree: line 3
// is "line three".
func uncommittedRepo(t *testing.T) *testRepo {
	t.Helper()
	r := newTestRepo(t)
	r.write("a.txt", numberedLines("line", 5))
	r.commit("base")
	r.write("a.txt", strings.Replace(numberedLines("line", 5), "line 3\n", "line three\n", 1))
	return r
}

// TestChangeNoticeAppears covers REQ-SES-010 and REQ-SES-014: a status poll with check=1 reports
// a worktree change made while the review runs; one without never runs the check; once reported,
// the change stays reported, even when it is undone.
func TestChangeNoticeAppears(t *testing.T) {
	t.Parallel()
	repo := uncommittedRepo(t)
	f := newFixture(t, repo, fixtureOptions{src: source.Uncommitted(), checkInterval: time.Millisecond})
	f.serveRun()
	if st := f.status(true); st.Changed || st.Generation != 1 {
		t.Fatalf("before any change: changed %v, generation %d; want false, 1", st.Changed, st.Generation)
	}
	repo.write("b.txt", "new\n")
	time.Sleep(5 * time.Millisecond) // past the check interval
	for k := range 3 {
		if f.status(false).Changed {
			t.Fatalf("poll %d without check=1 reported a change: it ran the check", k)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for !f.status(true).Changed {
		if time.Now().After(deadline) {
			t.Fatal("changed still false 5 s after an untracked file was added")
		}
		time.Sleep(5 * time.Millisecond)
	}
	repo.remove("b.txt")
	time.Sleep(5 * time.Millisecond)
	if !f.status(true).Changed {
		t.Error("changed went back to false after the change was undone; want it to stay reported")
	}
}

// TestChangeCheckFailureKeepsStatusWorking: when the check cannot run, the status reply still
// succeeds, reports no change, and the failure is logged once, not once per poll.
func TestChangeCheckFailureKeepsStatusWorking(t *testing.T) {
	t.Parallel()
	repo := smallRepo(t)
	var log syncBuffer
	f := newFixture(t, repo, fixtureOptions{checkInterval: time.Millisecond, log: &log})
	f.serveRun()
	if err := os.WriteFile(filepath.Join(repo.dir, ".git", "HEAD"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for k := range 5 {
		if f.status(true).Changed {
			t.Fatalf("poll %d: changed with HEAD unreadable", k)
		}
		time.Sleep(3 * time.Millisecond)
	}
	if n := strings.Count(log.String(), "check whether the change under review changed"); n != 1 {
		t.Errorf("the failed check was logged %d times; want 1\nlog:\n%s", n, log.String())
	}
}

// TestRefreshCarriesTheDraftToANewGeneration covers REQ-SES-013, REQ-SES-015 and REQ-DRF-006 over
// HTTP: the refresh moves the review to generation 2; a line comment whose line changed becomes
// a file comment and is listed as moved; one whose line only shifted follows it; a page still
// naming generation 1 is told to reload.
func TestRefreshCarriesTheDraftToANewGeneration(t *testing.T) {
	t.Parallel()
	repo := uncommittedRepo(t)
	f := newFixture(t, repo, fixtureOptions{src: source.Uncommitted(), checkInterval: time.Millisecond})
	f.serveRun()
	f.mustStatus(f.createComment("c1", targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 3}, "on three"),
		http.StatusOK, "create c1")
	f.mustStatus(f.createComment("c2", targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 5}, "on five"),
		http.StatusOK, "create c2")

	// A new first line shifts every line by one; line 3 changes again.
	repo.write("a.txt", "line 0\n"+strings.Replace(numberedLines("line", 5), "line 3\n", "line THREE\n", 1))
	got := f.refresh()
	if got.Unchanged || got.Generation != 2 {
		t.Fatalf("refresh: unchanged %v, generation %d; want false, 2", got.Unchanged, got.Generation)
	}
	wantMoved := []movedDTO{{
		ID:   "c1",
		From: targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 3},
		To:   targetDTO{Target: "file", Path: "a.txt"},
	}}
	if !reflect.DeepEqual(got.Moved, wantMoved) {
		t.Errorf("refresh moved %+v; want %+v", got.Moved, wantMoved)
	}

	s := f.session()
	if s.Generation != 2 || !reflect.DeepEqual(s.Moved, wantMoved) {
		t.Errorf("session: generation %d, moved %+v; want 2, %+v", s.Generation, s.Moved, wantMoved)
	}
	want := map[string]commentDTO{
		"c1": {ID: "c1", Author: "human", Body: "on three", Target: targetDTO{Target: "file", Path: "a.txt"}},
		"c2": {ID: "c2", Author: "human", Body: "on five", Target: targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 6}},
	}
	for _, c := range s.Comments {
		c.Version = 0
		if w, ok := want[c.ID]; !ok || c != w {
			t.Errorf("comment %s after refresh = %+v; want %+v", c.ID, c, w)
		}
		delete(want, c.ID)
	}
	if len(want) != 0 {
		t.Errorf("comments missing after refresh: %v", want)
	}
	if st := f.status(true); st.Generation != 2 || st.Changed {
		t.Errorf("status after refresh: generation %d, changed %v; want 2, false", st.Generation, st.Changed)
	}
	// A later refresh to the same trees keeps the list, so a page loaded after it still shows it.
	if again := f.refresh(); !again.Unchanged || again.Generation != 2 {
		t.Fatalf("second refresh: unchanged %v, generation %d; want true, 2", again.Unchanged, again.Generation)
	}
	if s := f.session(); !reflect.DeepEqual(s.Moved, wantMoved) {
		t.Errorf("session after an unchanged refresh: moved %+v; want %+v", s.Moved, wantMoved)
	}
	draft, err := os.ReadFile(f.sess.DraftPath())
	if err != nil {
		t.Fatalf("read the new draft: %v", err)
	}
	if !bytes.Contains(draft, []byte("on five")) {
		t.Errorf("the new draft does not hold the carried comments:\n%s", draft)
	}
	if _, err := os.Stat(f.store.Path()); !os.IsNotExist(err) {
		t.Errorf("the replaced draft %s: stat err = %v; want it deleted", f.store.Path(), err)
	}

	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/file?i=0", ""},
		{http.MethodPut, "/api/summary", `{"summary":"x"}`},
		{http.MethodPost, "/api/refresh", ""},
	} {
		r := f.do(req.method, req.path, req.body, withHeader("Rvw-Generation", "1"))
		if r.status != http.StatusConflict || r.header.Get("Rvw-Generation") != "2" {
			t.Errorf("%s %s naming generation 1: status %d, Rvw-Generation %q; want 409, \"2\"",
				req.method, req.path, r.status, r.header.Get("Rvw-Generation"))
		}
	}
	if s := f.session(); s.Summary != "" || s.Generation != 2 {
		t.Errorf("after the stale requests: summary %q, generation %d; want them refused", s.Summary, s.Generation)
	}
	f.mustStatus(f.do(http.MethodGet, "/api/file?i=0", ""), http.StatusOK, "GET /api/file naming generation 2")
}

// TestRefreshToTheSameTrees: a refresh that finds nothing changed keeps the generation, the draft
// and its versions, so open pages keep working.
func TestRefreshToTheSameTrees(t *testing.T) {
	t.Parallel()
	repo := uncommittedRepo(t)
	f := newFixture(t, repo, fixtureOptions{src: source.Uncommitted()})
	f.serveRun()
	f.mustStatus(f.createComment("c1", targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 3}, "on three"),
		http.StatusOK, "create c1")
	before := f.session()
	got := f.refresh()
	if !got.Unchanged || got.Generation != 1 || len(got.Moved) != 0 {
		t.Fatalf("refresh = %+v; want unchanged in generation 1, nothing moved", got)
	}
	after := f.session()
	if !reflect.DeepEqual(after.Comments, before.Comments) || after.Generation != 1 {
		t.Errorf("after an unchanged refresh: generation %d, comments %+v; want 1, %+v",
			after.Generation, after.Comments, before.Comments)
	}
	if _, err := os.Stat(f.store.Path()); err != nil {
		t.Errorf("the draft after an unchanged refresh: %v; want it kept", err)
	}
}

// TestRequestsNameTheirGeneration covers REQ-SES-015's request side: a bound request naming no
// generation is refused, and so is a refresh naming another, which then refreshes nothing.
func TestRequestsNameTheirGeneration(t *testing.T) {
	t.Parallel()
	repo := uncommittedRepo(t)
	f := newFixture(t, repo, fixtureOptions{src: source.Uncommitted()})
	f.serveRun()
	r := f.do(http.MethodGet, "/api/file?i=0", "", func(r *http.Request) { r.Header.Del("Rvw-Generation") })
	f.mustStatus(r, http.StatusBadRequest, "GET /api/file naming no generation")
	repo.write("b.txt", "new\n")
	r = f.do(http.MethodPost, "/api/refresh", "", withHeader("Rvw-Generation", "7"))
	if r.status != http.StatusConflict || r.header.Get("Rvw-Generation") != "1" {
		t.Errorf("refresh naming generation 7: status %d, Rvw-Generation %q; want 409, \"1\"", r.status, r.header.Get("Rvw-Generation"))
	}
	if s := f.session(); s.Generation != 1 || len(s.Files) != 1 {
		t.Errorf("after the refused refresh: generation %d, %d files; want 1, 1", s.Generation, len(s.Files))
	}
}

// TestRefreshAfterTheEnd: once the session has ended, a refresh is refused with 410. The handler is
// served without Run, which would stop serving at the end.
func TestRefreshAfterTheEnd(t *testing.T) {
	t.Parallel()
	repo := uncommittedRepo(t)
	f := newFixture(t, repo, fixtureOptions{src: source.Uncommitted()})
	f.serveHandler()
	f.mustStatus(f.do(http.MethodPost, "/api/cancel", ""), http.StatusNoContent, "cancel")
	repo.write("b.txt", "new\n")
	f.mustStatus(f.do(http.MethodPost, "/api/refresh", ""), http.StatusGone, "refresh after cancel")
}

// syncBuffer is a log writer the server's goroutines and the test share.
type syncBuffer struct {
	b  bytes.Buffer
	mu sync.Mutex
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
