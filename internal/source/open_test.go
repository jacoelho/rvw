package source_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/source"
)

// shimGit puts a git on PATH that runs script's case arms before the real git. The script sees the
// arguments as " $* "; $LOG names a file the arms may append to, and $REAL_GIT the real git, which
// an arm may exec itself. It returns the log's path.
// Callers cannot run in parallel: PATH is process-wide.
func shimGit(t *testing.T, arms string) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log")
	script := "#!/bin/sh\nLOG='" + logPath + "'\nREAL_GIT='" + gitPath + "'\ncase \" $* \" in\n" + arms + "\nesac\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func logLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

// TestOpenCancelledWhileLocating covers fix 9: a cancellation while git locates the worktree is
// reported as the cancellation, not as "not inside a Git worktree".
func TestOpenCancelledWhileLocating(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("a")
	gate := filepath.Join(t.TempDir(), "gate")
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := shimGit(t, `*" --show-toplevel "*) echo x >> "$LOG"; while [ -e '`+gate+`' ]; do sleep 0.05; done;;`)
	t.Cleanup(func() { _ = os.Remove(gate) })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		repo *source.Repo
		err  error
	}
	done := make(chan result, 1)
	go func() {
		repo, err := source.Open(ctx, r.dir)
		done <- result{repo, err}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for logLines(t, logPath) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("git rev-parse --show-toplevel never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	res := <-done
	if res.repo != nil {
		_ = res.repo.Close()
	}
	if !errors.Is(res.err, context.Canceled) {
		t.Errorf("Open cancelled mid-flight: err = %v, want context.Canceled", res.err)
	}
}

// TestOpenKeepsGitRefusal covers fix 9: a git refusal other than "not a git repository" fails Open
// with git's own message, not as a user error about the directory.
func TestOpenKeepsGitRefusal(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("a")
	const refusal = "fatal: detected dubious ownership in repository"
	shimGit(t, `*" --show-toplevel "*) echo '`+refusal+`' >&2; exit 128;;`)

	repo, err := source.Open(t.Context(), r.dir)
	if repo != nil {
		_ = repo.Close()
	}
	if err == nil || !strings.Contains(err.Error(), refusal) {
		t.Errorf("Open refused by git: err = %v, want it to contain %q", err, refusal)
	}
}

// TestCloseIsFinal covers fix 10: after Close, reads fail with ErrClosed and start no new git
// object reader.
func TestCloseIsFinal(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "one\ntwo\n")
	r.commit("a")
	r.write("a.txt", "one\nthree\n")
	r.commit("b")
	logPath := shimGit(t, `*" cat-file --batch-command "*) echo x >> "$LOG";;`)
	repo := open(t, r.dir)
	snap, err := repo.Resolve(t.Context(), source.Last(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	i := fileIndex(t, snap, "a.txt")
	if _, err := snap.Diff(t.Context(), i); err != nil {
		t.Fatalf("Diff before Close: %v", err)
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := snap.Diff(t.Context(), i); !errors.Is(err, source.ErrClosed) {
		t.Errorf("Diff after Close: err = %v, want ErrClosed", err)
	}
	if _, err := linesOf(t.Context(), snap, i, true, 1, 2); !errors.Is(err, source.ErrClosed) {
		t.Errorf("Lines after Close: err = %v, want ErrClosed", err)
	}
	if n := logLines(t, logPath); n != 1 {
		t.Errorf("object reader started %d times, want 1: a read after Close restarted it", n)
	}
}

// TestCloseDuringReads covers fix 10 under -race: reads racing Close either succeed or fail with
// ErrClosed.
func TestCloseDuringReads(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a.txt", numberedFile("line", 200))
	r.commit("a")
	repo := open(t, r.dir)
	snap, err := repo.Resolve(t.Context(), source.Last(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	i := fileIndex(t, snap, "a.txt")

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				if _, err := linesOf(t.Context(), snap, i, true, 1, 200); err != nil && !errors.Is(err, source.ErrClosed) {
					t.Errorf("Lines racing Close: %v", err)
					return
				}
			}
		})
	}
	if err := repo.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	wg.Wait()
}

// TestOpenChecksGitVersion covers the Git floor, 2.50, accepted 2026-11-05 (Q-33): an
// older git is refused with its version in the message, and 2.50 is accepted, Apple's suffix
// included. Not parallel: the shim changes PATH.
func TestOpenChecksGitVersion(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("a")
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{"git version 2.49.3", false},
		{"git version 2.50.0", true},
		{"git version 2.50.0 (Apple Git-155)", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			shimGit(t, `*" version "*) echo '`+tc.version+`'; exit 0;;`)
			repo, err := source.Open(t.Context(), r.dir)
			if repo != nil {
				_ = repo.Close()
			}
			switch {
			case tc.ok && err != nil:
				t.Errorf("Open with %q: %v; want success", tc.version, err)
			case !tc.ok && (err == nil || !strings.Contains(err.Error(), "2.49.3")):
				t.Errorf("Open with %q: err = %v; want a refusal naming 2.49.3", tc.version, err)
			}
		})
	}
}
