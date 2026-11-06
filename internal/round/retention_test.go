package round_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/round"
	"github.com/jacoelho/rvw/internal/source"
)

// linkedWorktree returns a repository with one commit and a linked worktree at <tmp>/wt, holding
// an untracked file whose blob nothing in Git references.
func linkedWorktree(t *testing.T) (mainRepo, wt *repo) {
	t.Helper()
	mainRepo = newRepo(t)
	mainRepo.write("tracked.txt", "tracked\n")
	mainRepo.commit("base")
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "wt")
	mainRepo.git("worktree", "add", "-q", "-b", "wt", dir)
	wt = mainRepo.at(dir)
	wt.write("untracked.txt", "only in the linked worktree\n")
	return mainRepo, wt
}

// deliver records a delivery marking every changed file of snap viewed.
func deliver(t *testing.T, s *round.Store, sr *source.Repo, snap *source.Snapshot, at time.Time) {
	t.Helper()
	view := review.Draft{}
	for i := range snap.FileCount() {
		view.Viewed = append(view.Viewed, review.Mark{File: i})
	}
	d := round.Delivered(snap, view, at)
	record(t, s, sr, d)
}

func worktreeID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:16]
}

// AC-RND-002: a delivered snapshot's trees survive git gc through one retention ref per snapshot,
// written without hooks, signing, reflog or the inherited identity, and nothing else changes.
func TestRetentionRefKeepsSnapshot(t *testing.T) {
	mainRepo, wt := linkedWorktree(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hooks := t.TempDir()
	hook := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	commonDir := filepath.Join(mainRepo.dir, ".git")
	for _, kv := range [][2]string{
		{"commit.gpgSign", "true"}, {"gpg.program", "false"},
		{"core.logAllRefUpdates", "always"}, {"core.hooksPath", hooks},
	} {
		mainRepo.git("config", kv[0], kv[1])
	}
	t.Setenv("GIT_COMMITTER_DATE", "2001-02-03T04:05:06Z")
	t.Setenv("GIT_AUTHOR_DATE", "2001-02-03T04:05:06Z")
	t.Setenv("GIT_AUTHOR_NAME", "Mallory")
	t.Setenv("GIT_AUTHOR_EMAIL", "mallory@example.com")
	for _, k := range []string{"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}

	state := func() string {
		return mainRepo.git("for-each-ref", "--exclude=refs/rvw/") + "\n" +
			string(readFile(t, filepath.Join(commonDir, "config"))) +
			string(readFile(t, filepath.Join(commonDir, "index"))) +
			string(readFile(t, filepath.Join(commonDir, "worktrees", "wt", "index")))
	}
	before := state()

	ledgers := t.TempDir()
	wtRepo := wt.open()
	snap := wt.uncommitted(wtRepo)
	if snap.TopLevel() != wt.dir {
		t.Fatalf("fixture: top level %s, want %s", snap.TopLevel(), wt.dir)
	}
	wtStore := openStoreIn(t, ledgers, snap.CommonDir(), snap.TopLevel())
	at := t0.Add(250 * time.Millisecond)
	deliver(t, wtStore, wtRepo, snap, at)
	blob := snap.File(fileIndex(t, snap, "untracked.txt")).NewBlob

	ref := "refs/rvw/" + worktreeID(wt.dir) + "/" + snap.BaseTree() + "-" + snap.HeadTree()
	if got := mainRepo.git("for-each-ref", "--format=%(refname)", "refs/rvw/"); got != ref {
		t.Fatalf("retention refs = %q, want %q", got, ref)
	}
	if got, want := snap.BaseTree(), wt.git("rev-parse", "HEAD^{tree}"); got != want {
		t.Errorf("fixture: base tree %s, want HEAD's %s", got, want)
	}
	wantTree := "040000 tree " + snap.BaseTree() + "\tbase\n040000 tree " + snap.HeadTree() + "\thead"
	if got := mainRepo.git("cat-file", "-p", ref+"^{tree}"); got != wantTree {
		t.Errorf("retention commit tree:\n%s\nwant\n%s", got, wantTree)
	}
	unix := strconv.FormatInt(t0.Unix(), 10)
	if got, want := mainRepo.git("log", "-1", "--format=%an|%ae|%at|%cn|%ce|%ct", ref),
		"rvw|rvw@localhost|"+unix+"|rvw|rvw@localhost|"+unix; got != want {
		t.Errorf("retention commit identity and dates %q, want %q", got, want)
	}
	if c := mainRepo.git("cat-file", "commit", ref); strings.Contains(c, "gpgsig") {
		t.Errorf("retention commit is signed:\n%s", c)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Error("a reference-transaction hook ran")
	}
	if _, err := os.Stat(filepath.Join(commonDir, "logs", "refs", "rvw")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a reflog was written for a retention ref: %v", err)
	}
	if got := state(); got != before {
		t.Error("refs other than refs/rvw/, the config or an index changed")
	}
	mainRepo.git("gc", "-q", "--prune=now")
	if !mainRepo.gitOK("cat-file", "-e", blob) {
		t.Fatal("gc pruned the reviewed untracked blob despite the retention ref")
	}
	// gc packs refs, which runs the hook itself; only rvw's later ref updates are checked below.
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	// A round in the main worktree leaves the linked worktree's ref.
	mainRepo.write("main-only.txt", "main\n")
	mainSR := mainRepo.open()
	mainSnap := mainRepo.uncommitted(mainSR)
	deliver(t, openStoreIn(t, ledgers, mainSnap.CommonDir(), mainSnap.TopLevel()), mainSR, mainSnap, t0.Add(day))
	if !mainRepo.gitOK("rev-parse", "--verify", "-q", ref) {
		t.Error("the main worktree's round deleted the linked worktree's ref")
	}

	// A later round reviewing the file again no longer names the first snapshot: its ref goes, and
	// with it what kept the blob.
	wt.write("untracked.txt", "edited\n")
	deliver(t, wtStore, wtRepo, wt.uncommitted(wtRepo), t0.Add(2*day))
	if mainRepo.gitOK("rev-parse", "--verify", "-q", ref) {
		t.Error("ref of a snapshot the ledger no longer names was kept")
	}
	// Deleting runs no hook and leaves no reflog either.
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Error("a reference-transaction hook ran while recording the later rounds or pruning")
	}
	if _, err := os.Stat(filepath.Join(commonDir, "logs", "refs", "rvw")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a reflog was written for a retention ref: %v", err)
	}
	mainRepo.git("gc", "-q", "--prune=now")
	if mainRepo.gitOK("cat-file", "-e", blob) {
		t.Error("fixture: the old blob survived gc after its ref was deleted, so the ref proved nothing")
	}
}

// Retention refs of any worktree age out TTL after their last delivery, counted in whole seconds.
func TestRetentionRefsAge(t *testing.T) {
	t.Parallel()
	mainRepo, wt := linkedWorktree(t)
	ledgers := t.TempDir()
	wtRepo := wt.open()
	snap := wt.uncommitted(wtRepo)
	wtStore := openStoreIn(t, ledgers, snap.CommonDir(), snap.TopLevel())
	ref := "refs/rvw/" + worktreeID(wt.dir) + "/" + snap.BaseTree() + "-" + snap.HeadTree()

	deliver(t, wtStore, wtRepo, snap, t0.Add(day))
	// Delivering the same snapshot again re-dates its ref.
	deliver(t, wtStore, wtRepo, snap, t0.Add(29*day))

	mainRepo.write("main-only.txt", "main\n")
	mainSR := mainRepo.open()
	mainSnap := mainRepo.uncommitted(mainSR)
	mainStore := openStoreIn(t, ledgers, mainSnap.CommonDir(), mainSnap.TopLevel())
	deliver(t, mainStore, mainSR, mainSnap, t0.Add(31*day))
	if !mainRepo.gitOK("rev-parse", "--verify", "-q", ref) {
		t.Fatal("ref delivered again on day 29 was deleted on day 31")
	}
	deliver(t, mainStore, mainSR, mainSnap, t0.Add(29*day+round.TTL+time.Second))
	if mainRepo.gitOK("rev-parse", "--verify", "-q", ref) {
		t.Error("another worktree's ref older than TTL was kept")
	}
}
