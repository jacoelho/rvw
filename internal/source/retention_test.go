package source_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/source"
)

// TestRetentionRefsStayInTheirNamespace: KeepTrees and DeleteRefs touch only refs under refs/rvw/
// and pass no unchecked tree ID to git (INV-PLAT-004); a refused call leaves every ref as it was.
func TestRetentionRefsStayInTheirNamespace(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a", "a\n")
	r.commit("one")
	tree := r.tree("HEAD")
	repo := open(t, r.dir)
	refs := func() string { return r.git("for-each-ref", "--format=%(refname) %(objectname)") }
	before := refs()
	at := time.Unix(1_700_000_000, 0)

	for name, ref := range map[string]string{
		"a branch":           "refs/heads/main",
		"the namespace root": "refs/rvw/",
		"a parent step":      "refs/rvw/../heads/main",
		"an empty segment":   "refs/rvw/a//b",
		"an upper-case name": "refs/rvw/A-b",
		"a trailing slash":   "refs/rvw/a/",
	} {
		if _, err := repo.KeepTrees(t.Context(), ref, tree, tree, at); err == nil {
			t.Errorf("KeepTrees on %s (%q) succeeded; want an error", name, ref)
		}
		head := strings.TrimSpace(r.git("rev-parse", "HEAD"))
		if err := repo.DeleteRefs(t.Context(), []source.RetentionRef{{Name: "refs/rvw/ok-ref", Commit: head}, {Name: ref, Commit: head}}); err == nil {
			t.Errorf("DeleteRefs with %s (%q) succeeded; want an error", name, ref)
		}
		if err := repo.UndoKeep(t.Context(), source.RefUpdate{Name: ref, Old: head, New: head}); err == nil {
			t.Errorf("UndoKeep on %s (%q) succeeded; want an error", name, ref)
		}
	}
	for _, bad := range []string{"HEAD", "main", strings.Repeat("g", 40), "--all"} {
		if _, err := repo.KeepTrees(t.Context(), "refs/rvw/x/y", bad, tree, at); err == nil {
			t.Errorf("KeepTrees with base %q succeeded; want an error", bad)
		}
		if _, err := repo.KeepTrees(t.Context(), "refs/rvw/x/y", tree, bad, at); err == nil {
			t.Errorf("KeepTrees with head %q succeeded; want an error", bad)
		}
	}
	if after := refs(); after != before {
		t.Fatalf("refused calls changed refs:\nbefore %q\nafter  %q", before, after)
	}

	ref := "refs/rvw/0123456789abcdef/" + tree + "-" + tree
	u, err := repo.KeepTrees(t.Context(), ref, tree, tree, at)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := repo.RetentionRefs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != ref || listed[0].Commit != u.New || !listed[0].Committed.Equal(at) {
		t.Fatalf("RetentionRefs = %+v; want %s at %s committed at %v", listed, ref, u.New, at)
	}
	if err := repo.DeleteRefs(t.Context(), listed); err != nil {
		t.Fatalf("DeleteRefs: %v", err)
	}
	if after := refs(); after != before {
		t.Fatalf("refs after delete = %q; want %q", after, before)
	}
}

// TestRetentionRefsSkipForeignRefs: a ref under refs/rvw/ that KeepTrees could not have written,
// by name or because it points at no commit, is not listed, so pruning neither deletes it nor
// fails on it.
func TestRetentionRefsSkipForeignRefs(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a", "a\n")
	r.commit("one")
	tree := r.tree("HEAD")
	repo := open(t, r.dir)
	ours := "refs/rvw/0123456789abcdef/" + tree + "-" + tree
	if _, err := repo.KeepTrees(t.Context(), ours, tree, tree, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	r.git("update-ref", "refs/rvw/Upper/name", "HEAD")
	r.git("update-ref", "refs/rvw/0123456789abcdef/blob", r.git("rev-parse", "HEAD:a"))
	listed, err := repo.RetentionRefs(t.Context())
	if err != nil {
		t.Fatalf("RetentionRefs with foreign refs: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != ours {
		t.Errorf("RetentionRefs = %+v; want only %s", listed, ours)
	}
}

// TestRetentionRefWritesCompareAndSwap: each retention ref write applies only while the ref holds
// what the writer last saw, so a session never undoes or deletes another session's write
// (REQ-RND-005, REQ-RND-006).
func TestRetentionRefWritesCompareAndSwap(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a", "a\n")
	r.commit("one")
	tree := r.tree("HEAD")
	repo := open(t, r.dir)
	ref := "refs/rvw/0123456789abcdef/" + tree + "-" + tree
	at := func(ref string) string {
		return strings.TrimSpace(r.git("for-each-ref", "--format=%(objectname)", ref))
	}

	first, err := repo.KeepTrees(t.Context(), ref, tree, tree, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.KeepTrees(t.Context(), ref, tree, tree, time.Unix(1_700_000_100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.Old != "" || second.Old != first.New || second.New == first.New || at(ref) != second.New {
		t.Fatalf("updates %+v then %+v, ref at %s; want absent → first → second", first, second, at(ref))
	}
	listed, err := repo.RetentionRefs(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// Another session moves the ref: neither the undo nor the stale delete may touch it.
	head := strings.TrimSpace(r.git("rev-parse", "HEAD"))
	r.git("update-ref", ref, head)
	if err := repo.UndoKeep(t.Context(), second); err == nil {
		t.Error("UndoKeep of an overwritten ref succeeded")
	}
	if err := repo.DeleteRefs(t.Context(), listed); err == nil {
		t.Error("DeleteRefs of a ref moved since listing succeeded")
	}
	if got := at(ref); got != head {
		t.Fatalf("ref at %s after refused writes; want the other session's %s", got, head)
	}

	// Undone in order, the ref returns to each earlier value and then to absent.
	r.git("update-ref", ref, second.New)
	if err := repo.UndoKeep(t.Context(), second); err != nil || at(ref) != first.New {
		t.Fatalf("UndoKeep(second) = %v, ref at %q; want %s", err, at(ref), first.New)
	}
	if err := repo.UndoKeep(t.Context(), first); err != nil || at(ref) != "" {
		t.Fatalf("UndoKeep(first) = %v, ref at %q; want it absent", err, at(ref))
	}
	if err := repo.DeleteRefs(t.Context(), listed); err == nil {
		t.Error("DeleteRefs of a ref already gone succeeded")
	}
}
