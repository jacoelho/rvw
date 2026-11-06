package server_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func (f *fixture) headMoved() bool {
	f.t.Helper()
	r := f.do(http.MethodGet, "/api/status", "")
	f.mustStatus(r, http.StatusOK, "GET /api/status")
	var st statusDTO
	decode(f.t, r.body, &st)
	return st.HeadMoved
}

// TestHeadMovedNoticeAppears checks that a commit made while the review runs is reported by the
// status reply within ten seconds.
func TestHeadMovedNoticeAppears(t *testing.T) {
	t.Parallel()
	repo := smallRepo(t)
	f := newFixture(t, repo, fixtureOptions{})
	f.serveRun()
	if f.headMoved() {
		t.Fatal("head_moved before HEAD moved")
	}
	repo.write("later.txt", "later\n")
	repo.commit("later")

	deadline := time.Now().Add(10 * time.Second)
	for !f.headMoved() {
		if time.Now().After(deadline) {
			t.Fatal("head_moved still false 10s after a commit")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestHeadMovedSurvivesUnreadableHead checks that when git cannot read HEAD the status reply still
// succeeds and keeps the last known answer. The test makes HEAD unparsable: a HEAD naming a missing
// branch is not a git failure, and rvw currently reports it as moved.
func TestHeadMovedSurvivesUnreadableHead(t *testing.T) {
	t.Parallel()
	repo := smallRepo(t)
	f := newFixture(t, repo, fixtureOptions{})
	f.serveRun()
	if err := os.WriteFile(filepath.Join(repo.dir, ".git", "HEAD"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for k := range 5 {
		if f.headMoved() {
			t.Fatalf("poll %d: head_moved with HEAD unreadable and never seen moving", k)
		}
	}
}
