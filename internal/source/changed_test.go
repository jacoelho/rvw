package source_test

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/source"
)

// TestChangedReportsWhatResolvingAgainWouldChange covers REQ-SES-010's check: each case changes the
// repository after the snapshot and expects Changed to report whether resolving the same source
// again could give other trees. No case writes an object.
func TestChangedReportsWhatResolvingAgainWouldChange(t *testing.T) {
	t.Parallel()
	uncommitted := source.Uncommitted()
	cases := []struct {
		name   string
		src    source.Source
		change func(r *repo)
		want   bool
	}{
		{"nothing", uncommitted, func(*repo) {}, false},
		{"touched, same content", uncommitted, func(r *repo) {
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(r.path("a.txt"), later, later); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"new ignored file", uncommitted, func(r *repo) { r.write("new.log", "ignored\n") }, false},
		{"staged, same content", uncommitted, func(r *repo) { r.git("add", "a.txt") }, false},
		{"tracked file edited", uncommitted, func(r *repo) { r.write("a.txt", "edited\n") }, true},
		{"staged file edited", uncommitted, func(r *repo) { r.write("b.txt", "edited\n") }, true},
		{"untracked file deleted", uncommitted, func(r *repo) { r.remove("untracked.txt") }, true},
		{"new untracked file", uncommitted, func(r *repo) { r.write("another.txt", "x\n") }, true},
		{"new untracked directory", uncommitted, func(r *repo) { r.write("dir/deep/x.txt", "x\n") }, true},
		{"commit", uncommitted, func(r *repo) { r.commit("more") }, true},
		{"last: worktree edited", source.Last(), func(r *repo) { r.write("a.txt", "edited\n") }, false},
		{"last: commit", source.Last(), func(r *repo) { r.commit("more") }, true},
		{"branch: worktree edited", branchSource(t, "main", false), func(r *repo) { r.write("a.txt", "edited\n") }, false},
		{"branch: base ref moved", branchSource(t, "main", false), func(r *repo) {
			r.git("update-ref", "refs/heads/main", "main~1")
		}, true},
		{"branch: base ref deleted", branchSource(t, "main", false), func(r *repo) {
			r.git("update-ref", "-d", "refs/heads/main")
		}, true},
		{"branch with uncommitted: worktree edited", branchSource(t, "main", true), func(r *repo) {
			r.write("a.txt", "edited\n")
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := forkFixture(t).on(t)
			snap := resolve(t, r.dir, c.src)
			c.change(r)
			objects := hashTree(t, r.path(".git/objects"), func(string) bool { return false })
			for k := range 2 {
				got, err := snap.Changed(t.Context())
				if err != nil {
					t.Fatalf("Changed (call %d): %v", k+1, err)
				}
				if got != c.want {
					t.Fatalf("Changed (call %d) = %v; want %v", k+1, got, c.want)
				}
			}
			if after := hashTree(t, r.path(".git/objects"), func(string) bool { return false }); !reflect.DeepEqual(after, objects) {
				t.Errorf("Changed wrote objects: %d files before, %d after", len(objects), len(after))
			}
		})
	}
}

// TestChangedFailsOnceClosed: Close, or closing the repository, deletes the private index, so
// Changed fails with ErrClosed; Close of the snapshot leaves its other methods working.
func TestChangedFailsOnceClosed(t *testing.T) {
	t.Parallel()
	r := forkFixture(t)
	repo := open(t, r.dir)
	snap, err := repo.Resolve(t.Context(), source.Uncommitted(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	for k := range 2 {
		if err := snap.Close(); err != nil {
			t.Fatalf("Close (call %d): %v", k+1, err)
		}
	}
	if _, err := snap.Changed(t.Context()); !errors.Is(err, source.ErrClosed) {
		t.Errorf("Changed after Close: err = %v; want ErrClosed", err)
	}
	if _, err := snap.Diff(t.Context(), 0); err != nil {
		t.Errorf("Diff after the snapshot's Close: %v", err)
	}

	other, err := source.Open(t.Context(), r.dir)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := other.Resolve(t.Context(), source.Uncommitted(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := kept.Changed(t.Context()); !errors.Is(err, source.ErrClosed) {
		t.Errorf("Changed after the repository's Close: err = %v; want ErrClosed", err)
	}
	if _, err := other.Resolve(t.Context(), source.Uncommitted(), quiet); err == nil {
		t.Error("Resolve of an uncommitted source after the repository's Close succeeded")
	}
}
