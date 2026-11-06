package server_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/server"
)

// gatedGit puts a git shim first on PATH. While the returned gate file exists, every patch command
// (one with a standalone -p) appends a line to the log and waits until the gate is removed; every
// other command runs the real git at once. The gate starts open; hold creates it.
//
// It sets PATH for the whole process, so its tests cannot run in parallel.
type gatedGit struct {
	t         *testing.T
	gate, log string
}

func newGatedGit(t *testing.T) *gatedGit {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	g := &gatedGit{t: t, gate: filepath.Join(dir, "gate"), log: filepath.Join(dir, "log")}
	script := "#!/bin/sh\n" +
		"case \" $* \" in *\" -p \"*)\n" +
		"  if [ -e '" + g.gate + "' ]; then\n" +
		"    echo x >> '" + g.log + "'\n" +
		"    while [ -e '" + g.gate + "' ]; do sleep 0.05; done\n" +
		"  fi;;\n" +
		"esac\n" +
		"exec '" + gitPath + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(g.release)
	return g
}

func (g *gatedGit) hold() {
	g.t.Helper()
	if err := os.WriteFile(g.gate, nil, 0o600); err != nil {
		g.t.Fatal(err)
	}
}

func (g *gatedGit) release() {
	if err := os.Remove(g.gate); err != nil && !os.IsNotExist(err) {
		g.t.Error(err)
	}
}

// held is the number of patch commands that reached the gate.
func (g *gatedGit) held() int {
	data, err := os.ReadFile(g.log)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		g.t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func (g *gatedGit) waitHeld(n int) {
	g.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for g.held() < n {
		if time.Now().After(deadline) {
			g.t.Fatalf("%d patch commands reached the gate; want %d", g.held(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSessionEndCancelsRequests covers plan item F: when the session ends, requests still running
// git are cancelled, so Run shuts down without waiting for them, and the request that ended the
// session is still answered.
func TestSessionEndCancelsRequests(t *testing.T) {
	for _, tc := range []struct {
		end  func(f *fixture, stop context.CancelFunc)
		name string
		want server.Outcome
	}{
		{name: "cancel", want: server.Cancelled, end: func(f *fixture, _ context.CancelFunc) {
			f.mustStatus(f.do(http.MethodPost, "/api/cancel", ""), http.StatusNoContent, "cancel")
		}},
		{name: "signal", want: server.Interrupted, end: func(_ *fixture, stop context.CancelFunc) { stop() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGatedGit(t)
			f := newFixture(t, smallRepo(t), fixtureOptions{})
			done, stop := f.serveRun()
			g.hold()
			go func() { _, _ = f.send(http.MethodGet, "/api/file?i=0", "") }()
			g.waitHeld(1)

			tc.end(f, stop)
			// Without cancellation, Shutdown waits for the held request until its deadline and Run
			// reports that it could not shut down.
			res := waitRun(t, done)
			if res.outcome != tc.want || res.err != nil {
				t.Errorf("Run = %v, %v; want %v, nil", res.outcome, res.err, tc.want)
			}
		})
	}
}

// TestCommentValidationTakesAFileSlot covers plan item E: validating a line comment reads the
// file's diff, so it waits for a file slot like a file view; a review comment and an edit need
// none, and the session stays responsive meanwhile. It relies on the server's two file slots.
func TestCommentValidationTakesAFileSlot(t *testing.T) {
	g := newGatedGit(t)
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	_, _ = f.serveRun()
	g.hold()
	for range 2 {
		go func() { _, _ = f.send(http.MethodGet, "/api/file?i=0", "") }()
	}
	g.waitHeld(2)

	f.mustStatus(f.createComment("r1", targetDTO{Target: "review"}, "x"), http.StatusOK, "a review comment while both slots are held")
	f.mustStatus(f.editComment("r1", "x, edited"), http.StatusOK, "an edit while both slots are held")

	put := make(chan response, 1)
	go func() {
		r, err := f.send(http.MethodPut, "/api/comments/l1", putCommentBody(t, placementDTO{Target: "line", File: new(0), Side: "new", Line: 3}, "y"), createOnly)
		if err != nil {
			r.status = -1
		}
		put <- r
	}()
	time.Sleep(500 * time.Millisecond)
	if n := g.held(); n != 2 {
		t.Fatalf("%d patch commands ran while both file slots were held; want 2", n)
	}
	g.release()
	select {
	case r := <-put:
		f.mustStatus(r, http.StatusOK, "the line comment after the slots were freed")
	case <-time.After(10 * time.Second):
		t.Fatal("the line comment did not complete after the slots were freed")
	}
	ids := commentIDs(f.session().Comments)
	if strings.Join(ids, ",") != "l1,r1" && strings.Join(ids, ",") != "r1,l1" {
		t.Errorf("session comments %v; want l1 and r1", ids)
	}
}

// TestSlotWaitEndsWithAReply: a request waiting for a file slot when the session ends is answered
// 503 with a reason, not an empty 200, and never runs git. Each handler that runs git waits for a
// slot. It relies on the server's two file slots.
func TestSlotWaitEndsWithAReply(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		opts                     []reqOption
	}{
		{name: "file", method: http.MethodGet, path: "/api/file?i=0"},
		{name: "context", method: http.MethodGet, path: "/api/file/context?i=0&from=1&to=1"},
		{name: "line comment", method: http.MethodPut, path: "/api/comments/l1",
			body: `{"target":{"target":"line","file":0,"side":"new","line":3},"body":"y"}`, opts: []reqOption{createOnly}},
		{name: "quotes", method: http.MethodGet, path: "/api/quotes?id=r1"},
		{name: "result", method: http.MethodGet, path: "/api/result.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGatedGit(t)
			f := newFixture(t, smallRepo(t), fixtureOptions{})
			done, stop := f.serveRun()
			g.hold()
			for range 2 {
				go func() { _, _ = f.send(http.MethodGet, "/api/file?i=0", "") }()
			}
			g.waitHeld(2)

			type reply struct {
				r   response
				err error
			}
			third := make(chan reply, 1)
			go func() {
				r, err := f.send(tc.method, tc.path, tc.body, tc.opts...)
				third <- reply{r, err}
			}()
			// Long enough for the third request to reach the slot wait.
			time.Sleep(300 * time.Millisecond)
			stop()
			var got reply
			select {
			case got = <-third:
			case <-time.After(10 * time.Second):
				t.Fatal("the request waiting for a slot was not answered after the session ended")
			}
			if got.err != nil {
				t.Fatalf("the request waiting for a slot got a connection error, not a reply: %v", got.err)
			}
			if n := g.held(); n != 2 {
				t.Errorf("%d patch commands ran; want the 2 that held the slots", n)
			}
			if got.r.status != http.StatusServiceUnavailable || strings.TrimSpace(string(got.r.body)) == "" {
				t.Errorf("status %d, body %q; want 503 with a reason", got.r.status, got.r.body)
			}
			if res := waitRun(t, done); res.err != nil {
				t.Errorf("Run: %v", res.err)
			}
		})
	}
}

// TestInvalidSideIsRefused: a line or range comment whose side is neither old nor new is refused
// with 422 and a reason naming the sides, and nothing is stored.
func TestInvalidSideIsRefused(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("a.txt", numberedLines("line", 5))
	repo.commit("base")
	repo.write("a.txt", strings.Replace(numberedLines("line", 5), "line 3\n", "line three\n", 1))
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	a := f.session().fileIndex(t, "a.txt")
	for _, side := range []string{"x", ""} {
		for kind, target := range map[string]map[string]any{
			"line":  {"target": "line", "file": a, "side": side, "line": 1},
			"range": {"target": "range", "file": a, "side": side, "start": 1, "end": 2},
		} {
			body := mustJSON(t, map[string]any{"target": target, "body": "b"})
			r := f.do(http.MethodPut, "/api/comments/c", body, createOnly)
			if r.status != http.StatusUnprocessableEntity {
				t.Errorf("%s, side %q: status %d, want 422; body %q", kind, side, r.status, r.body)
				continue
			}
			if reason := string(r.body); !strings.Contains(reason, `"old"`) || !strings.Contains(reason, `"new"`) {
				t.Errorf("%s, side %q: reason %q does not name the sides", kind, side, reason)
			}
		}
	}
	if cs := f.session().Comments; len(cs) != 0 {
		t.Errorf("refused creates stored %d comments", len(cs))
	}
}
