package source_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/source"
)

// forkFixture builds a fork-point history: main advances after feature forks, so the
// merge base differs from main's tip. HEAD is feature with staged, unstaged, untracked and
// ignored changes in the worktree.
func forkFixture(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)
	r.write("a.txt", "a1\na2\na3\n")
	r.write("b.txt", "b1\nb2\n")
	r.write(".gitignore", "*.log\n")
	r.commit("base")
	r.git("checkout", "-q", "-b", "feature")
	r.write("a.txt", "a1\nA2\na3\n")
	r.write("feat.txt", "feature\n")
	r.commit("feature 1")
	r.write("b.txt", "b1\nB2\n")
	r.commit("feature 2")
	r.git("checkout", "-q", "main")
	r.write("main-only.txt", "main\n")
	r.commit("main advances")
	r.git("tag", "v-main")
	r.git("update-ref", "refs/remotes/origin/main", "main")
	r.git("update-ref", "refs/tags/-x", "main")
	r.git("checkout", "-q", "feature")

	r.write("a.txt", "a1\nA2\na3\nunstaged\n")
	r.write("b.txt", "b1\nB2\nstaged\n")
	r.git("add", "b.txt")
	r.write("untracked.txt", "new\n")
	r.write("debug.log", "ignored\n")
	return r
}

// TestResolveSources checks each review source's base and head trees against a fork-point history.
func TestResolveSources(t *testing.T) {
	t.Parallel()
	r := forkFixture(t)
	head := r.git("rev-parse", "HEAD")
	mergeBase := r.git("merge-base", "main", "HEAD")
	forkTree := r.tree(mergeBase)
	if forkTree == r.tree("main") {
		t.Fatal("fixture: merge base tree equals main's tip, so it cannot tell them apart")
	}
	uncommitted := r.uncommittedTree(r.dir)

	tests := []struct {
		name       string
		src        source.Source
		base, head string
	}{
		{"last", source.Last(), r.tree("HEAD^1"), r.tree("HEAD")},
		{"branch local", branchSource(t, "main", false), forkTree, r.tree("HEAD")},
		{"branch tag", branchSource(t, "v-main", false), forkTree, r.tree("HEAD")},
		{"branch remote-tracking", branchSource(t, "origin/main", false), forkTree, r.tree("HEAD")},
		{"branch commit id", branchSource(t, r.git("rev-parse", "main"), false), forkTree, r.tree("HEAD")},
		{"branch ref starting with dash", branchSource(t, "-x", false), forkTree, r.tree("HEAD")},
		{"branch uncommitted", branchSource(t, "main", true), forkTree, uncommitted},
		{"uncommitted", source.Uncommitted(), r.tree("HEAD"), uncommitted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := r.on(t)
			snap := resolve(t, r.dir, tc.src)
			r.checkSnapshot(snap, tc.base, tc.head)
			if snap.HeadCommit() != head {
				t.Errorf("HeadCommit = %q, want %q", snap.HeadCommit(), head)
			}
		})
	}

	t.Run("uncommitted tree includes untracked and excludes ignored files", func(t *testing.T) {
		t.Parallel()
		r := r.on(t)
		paths := r.treePaths(resolve(t, r.dir, source.Uncommitted()).HeadTree())
		if !slices.Contains(paths, "untracked.txt") {
			t.Errorf("untracked.txt missing from %v", paths)
		}
		if slices.Contains(paths, "debug.log") {
			t.Errorf("ignored debug.log present in %v", paths)
		}
	})
}

// TestResolveLastOfMergeUsesFirstParent checks that the last commit of a merge is reviewed against
// its first parent.
func TestResolveLastOfMergeUsesFirstParent(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("f.txt", "1\n")
	r.commit("root")
	r.git("checkout", "-q", "-b", "side")
	r.write("side.txt", "side\n")
	r.commit("side")
	r.git("checkout", "-q", "main")
	r.write("main.txt", "main\n")
	r.commit("main")
	r.git("merge", "-q", "--no-ff", "--no-edit", "side")
	if r.tree("HEAD^1") == r.tree("HEAD^2") {
		t.Fatal("fixture: parents have the same tree")
	}
	r.checkSnapshot(resolve(t, r.dir, source.Last()), r.tree("HEAD^1"), r.tree("HEAD"))
}

// TestResolveRootAndUnborn checks that a root commit's base is the empty tree, and an
// unborn HEAD reviews the uncommitted tree against the empty tree.
func TestResolveRootAndUnborn(t *testing.T) {
	t.Parallel()

	t.Run("root commit", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.write("first.txt", "hello\n")
		r.commit("root")
		r.checkSnapshot(resolve(t, r.dir, source.Last()), emptyTree, r.tree("HEAD"))
	})

	t.Run("unborn uncommitted", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.write("new.txt", "hello\n")
		r.write("dir/other.txt", "x\n")
		want := r.uncommittedTree(r.dir)
		snap := resolve(t, r.dir, source.Uncommitted())
		r.checkSnapshot(snap, emptyTree, want)
		if snap.HeadCommit() != "" {
			t.Errorf("HeadCommit = %q, want empty for an unborn HEAD", snap.HeadCommit())
		}
	})

	t.Run("unborn needs a commit", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.write("new.txt", "hello\n")
		for _, src := range []source.Source{source.Last(), branchSource(t, "main", false), branchSource(t, "main", true)} {
			_, err := open(t, r.dir).Resolve(t.Context(), src, quiet)
			if err == nil || !strings.Contains(err.Error(), "HEAD is unborn") {
				t.Errorf("Resolve(%s) on unborn HEAD: err = %v, want it to say HEAD is unborn", src, err)
			}
		}
	})
}

// TestResolveUnresolvable covers the unreviewable-source conditions the source package detects:
// each fails with a message naming its reason (REQ-SRC-007).
func TestResolveUnresolvable(t *testing.T) {
	t.Parallel()
	r := forkFixture(t)
	orphan := newRepo(t)
	orphan.write("a.txt", "a\n")
	orphan.commit("main root")
	orphan.git("checkout", "-q", "--orphan", "other")
	orphan.write("b.txt", "b\n")
	orphan.commit("other root")
	clean := newRepo(t)
	clean.write("a.txt", "a\n")
	clean.commit("only")

	tests := []struct {
		name   string
		dir    string
		reason string
		src    source.Source
	}{
		{"--help is a ref that does not resolve", r.dir, `base ref "--help" does not resolve to a commit`, branchSource(t, "--help", false)},
		{"unknown base ref", r.dir, `base ref "no-such-ref" does not resolve to a commit`, branchSource(t, "no-such-ref", true)},
		{"no merge base", orphan.dir, `no merge base between "main" and HEAD`, branchSource(t, "main", false)},
		{"nothing to review", clean.dir, "nothing to review: uncommitted changes has no changes", source.Uncommitted()},
		{"empty branch", clean.dir, "nothing to review: branch main..HEAD has no changes", branchSource(t, "main", false)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snap, err := open(t, tc.dir).Resolve(t.Context(), tc.src, quiet)
			if err == nil || err.Error() != tc.reason {
				t.Fatalf("Resolve(%s) = %v, %v; want error %q", tc.src, snap, err, tc.reason)
			}
		})
	}

	t.Run("not inside a worktree", func(t *testing.T) {
		t.Parallel()
		repo, err := source.Open(t.Context(), t.TempDir())
		if repo != nil {
			_ = repo.Close()
		}
		if err == nil || err.Error() != "not inside a Git worktree" {
			t.Fatalf("Open outside a worktree: err = %v, want not inside a Git worktree", err)
		}
	})
}

// TestOriginHeadResolves checks origin/HEAD's target as the base ref: OriginHead names it, and
// Resolve of a branch source on that name reviews from the merge base with it.
func TestOriginHeadResolves(t *testing.T) {
	t.Parallel()
	setOriginHead := func(r *repo, target string) { r.git("symbolic-ref", "refs/remotes/origin/HEAD", target) }

	t.Run("symbolic to a remote-tracking branch", func(t *testing.T) {
		t.Parallel()
		r := forkFixture(t)
		setOriginHead(r, "refs/remotes/origin/main")
		for _, unc := range []bool{false, true} {
			repo := open(t, r.dir)
			ref, err := repo.OriginHead(t.Context())
			if err != nil {
				t.Fatalf("OriginHead: %v", err)
			}
			snap := resolve(t, r.dir, branchSource(t, ref, unc))
			want := resolve(t, r.dir, branchSource(t, "origin/main", unc))
			if snap.BaseTree() != want.BaseTree() || snap.HeadTree() != want.HeadTree() {
				t.Errorf("uncommitted=%v: trees %s..%s; want those of origin/main, %s..%s",
					unc, snap.BaseTree(), snap.HeadTree(), want.BaseTree(), want.HeadTree())
			}
			if got := snap.Source().BaseRef(); got != "origin/main" {
				t.Errorf("uncommitted=%v: BaseRef = %q, want origin/main", unc, got)
			}
		}
	})

	t.Run("not set", func(t *testing.T) {
		t.Parallel()
		missing := forkFixture(t)
		nonSymbolic := forkFixture(t)
		nonSymbolic.git("update-ref", "refs/remotes/origin/HEAD", "main")
		for name, r := range map[string]*repo{"missing": missing, "not symbolic": nonSymbolic} {
			if ref, err := open(t, r.dir).OriginHead(t.Context()); !errors.Is(err, source.ErrOriginHeadUnset) {
				t.Errorf("%s: OriginHead = %q, %v; want ErrOriginHeadUnset", name, ref, err)
			}
		}
	})

	t.Run("local branch shares the short name", func(t *testing.T) {
		t.Parallel()
		r := forkFixture(t)
		setOriginHead(r, "refs/remotes/origin/main")
		r.git("branch", "origin/main", "HEAD~1")
		remoteBase := r.tree(r.git("merge-base", "refs/remotes/origin/main", "HEAD"))
		if remoteBase == r.tree(r.git("merge-base", "refs/heads/origin/main", "HEAD")) {
			t.Fatal("fixture: the local and remote-tracking branches give the same base")
		}
		ref, err := open(t, r.dir).OriginHead(t.Context())
		if err != nil {
			t.Fatalf("OriginHead: %v", err)
		}
		if snap := resolve(t, r.dir, branchSource(t, ref, false)); snap.BaseTree() != remoteBase {
			t.Errorf("OriginHead = %q resolves to base %s; want the remote-tracking branch's %s", ref, snap.BaseTree(), remoteBase)
		}
	})

	t.Run("dangling", func(t *testing.T) {
		t.Parallel()
		r := forkFixture(t)
		setOriginHead(r, "refs/remotes/origin/gone")
		repo := open(t, r.dir)
		ref, err := repo.OriginHead(t.Context())
		if err != nil {
			t.Fatalf("OriginHead: %v", err)
		}
		_, err = repo.Resolve(t.Context(), branchSource(t, ref, false), quiet)
		if err == nil || !strings.Contains(err.Error(), `base ref "origin/gone" does not resolve`) ||
			strings.Contains(err.Error(), source.ErrOriginHeadUnset.Error()) {
			t.Errorf("Resolve(%q) err = %v; want the base ref not to resolve", ref, err)
		}
	})

	t.Run("at the current commit", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.write("a.txt", "a\n")
		r.commit("only")
		r.git("update-ref", "refs/remotes/origin/main", "HEAD")
		setOriginHead(r, "refs/remotes/origin/main")
		repo := open(t, r.dir)
		ref, err := repo.OriginHead(t.Context())
		if err != nil {
			t.Fatalf("OriginHead: %v", err)
		}
		_, err = repo.Resolve(t.Context(), branchSource(t, ref, false), quiet)
		if want := "nothing to review: branch origin/main..HEAD has no changes"; err == nil || err.Error() != want {
			t.Errorf("Resolve(%q) err = %v; want %q", ref, err, want)
		}
	})

	t.Run("unborn HEAD", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.write("new.txt", "hello\n")
		commit := r.git("commit-tree", emptyTree, "-m", "remote")
		r.git("update-ref", "refs/remotes/origin/main", commit)
		setOriginHead(r, "refs/remotes/origin/main")
		repo := open(t, r.dir)
		ref, err := repo.OriginHead(t.Context())
		if err != nil {
			t.Fatalf("OriginHead: %v", err)
		}
		_, err = repo.Resolve(t.Context(), branchSource(t, ref, true), quiet)
		if err == nil || !strings.Contains(err.Error(), "HEAD is unborn") {
			t.Errorf("Resolve(%q) on unborn HEAD: err = %v, want it to say HEAD is unborn", ref, err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		r := forkFixture(t)
		setOriginHead(r, "refs/remotes/origin/main")
		repo := open(t, r.dir)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		ref, err := repo.OriginHead(ctx)
		if !errors.Is(err, context.Canceled) || errors.Is(err, source.ErrOriginHeadUnset) {
			t.Errorf("OriginHead with a cancelled context = %q, %v; want context.Canceled", ref, err)
		}
	})
}

// TestResolveFromLinkedWorktreeAndSubdirectory checks that the worktree containing the start
// directory is reviewed, whole, whichever directory rvw starts in.
func TestResolveFromLinkedWorktreeAndSubdirectory(t *testing.T) {
	t.Parallel()
	r := forkFixture(t)
	r.write("sub/dir/inner.txt", "inner\n")
	r.commit("subdirectory")
	r.write("top.txt", "top-level change\n")
	r.write("sub/dir/inner.txt", "inner changed\n")

	t.Run("subdirectory", func(t *testing.T) {
		t.Parallel()
		r := r.on(t)
		want := r.uncommittedTree(r.dir)
		for _, src := range []source.Source{source.Uncommitted(), branchSource(t, "main", true), source.Last()} {
			fromRoot := resolve(t, r.dir, src)
			fromSub := resolve(t, r.path("sub/dir"), src)
			if fromSub.BaseTree() != fromRoot.BaseTree() || fromSub.HeadTree() != fromRoot.HeadTree() {
				t.Errorf("%s: subdirectory trees %s..%s, root %s..%s", src,
					fromSub.BaseTree(), fromSub.HeadTree(), fromRoot.BaseTree(), fromRoot.HeadTree())
			}
			if got, want := snapshotChanges(fromSub), snapshotChanges(fromRoot); !slices.Equal(got, want) {
				t.Errorf("%s: subdirectory files %v, root %v", src, got, want)
			}
		}
		snap := resolve(t, r.path("sub/dir"), source.Uncommitted())
		r.checkSnapshot(snap, r.tree("HEAD"), want)
		if !slices.Contains(r.treePaths(snap.HeadTree()), "top.txt") {
			t.Error("a change outside the start directory is missing from the uncommitted tree")
		}
	})

	t.Run("linked worktree", func(t *testing.T) {
		t.Parallel()
		r := r.on(t)
		wt := r.path("../linked")
		r.git("worktree", "add", "-q", "-b", "linked", wt, "feature")
		if err := writeFile(wt+"/a.txt", "linked change\n"); err != nil {
			t.Fatal(err)
		}
		if err := writeFile(wt+"/deep/new.txt", "linked untracked\n"); err != nil {
			t.Fatal(err)
		}
		repo := open(t, wt+"/deep")
		snap, err := repo.Resolve(t.Context(), source.Uncommitted(), quiet)
		if err != nil {
			t.Fatal(err)
		}
		if snap.TopLevel() != wt {
			t.Errorf("TopLevel = %q, want %q", snap.TopLevel(), wt)
		}
		r.checkSnapshot(snap, r.tree("linked"), r.uncommittedTree(wt))
		if got := snapshotChanges(snap); len(got) != 2 {
			t.Errorf("linked worktree changes = %v, want a.txt and deep/new.txt only", got)
		}
	})
}

// TestUncommittedSeesSameSizeEdit covers the uncommitted tree against Git's racy-clean case: a
// tracked file rewritten with the same size and the same mtime second as its index entry, where
// that second is also the index file's own mtime. git status reports the edit; the uncommitted
// tree must contain it. core.trustctime=false keeps the fixture independent of ctime, which a
// test cannot set.
func TestUncommittedSeesSameSizeEdit(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.git("config", "core.trustctime", "false")
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	r.write("f.txt", "one\ntwo\n")
	setMtime(t, r.path("f.txt"), t0)
	r.commit("base")
	r.write("f.txt", "one\nTWO\n")
	setMtime(t, r.path("f.txt"), t0)
	setMtime(t, r.path(".git/index"), t0)
	if st := string(r.gitIn(r.dir, []string{"GIT_OPTIONAL_LOCKS=0"}, "status", "--porcelain")); st != " M f.txt\n" {
		t.Fatalf("fixture: git status = %q, want the edit reported", st)
	}
	want := r.uncommittedTree(r.dir)
	if want == r.tree("HEAD") {
		t.Fatal("fixture: oracle tree equals HEAD")
	}
	snap, err := open(t, r.dir).Resolve(t.Context(), source.Uncommitted(), quiet)
	if err != nil {
		t.Fatalf("Resolve(uncommitted) = %v; git status reports f.txt modified", err)
	}
	r.checkSnapshot(snap, r.tree("HEAD"), want)
}

func setMtime(t *testing.T, p string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}
