package source_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// listingRepo commits a.txt (20 lines) and c.txt (10 lines), then renames a.txt to b.txt with
// line 7 edited and appends "c extra" to c.txt. With HEAD~1:a.txt absent the listing is
// [a.txt D, b.txt A, c.txt M]; with it present, [b.txt R a.txt->b.txt, c.txt M].
func listingRepo(t *testing.T) *repo {
	t.Helper()
	lines := func(prefix string, n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "%s %d\n", prefix, i)
		}
		return b.String()
	}
	r := newRepo(t)
	r.write("a.txt", lines("a", 20))
	r.write("c.txt", lines("c", 10))
	r.commit("one")
	r.git("mv", "a.txt", "b.txt")
	r.write("b.txt", strings.Replace(lines("a", 20), "a 7\n", "a 7 edited\n", 1))
	r.write("c.txt", lines("c", 10)+"c extra\n")
	r.commit("two")
	return r
}

// TestSameFiles: SameFiles compares the listing, including which blobs are present locally, so
// a file index names the same file with the same readable content in both snapshots. It backs
// REQ-DRF-006 and the basis of REQ-SES-015 (file indices belong to one snapshot).
func TestSameFiles(t *testing.T) {
	t.Parallel()

	t.Run("a blob arriving in a partial clone", func(t *testing.T) {
		t.Parallel()
		src := listingRepo(t)
		src.git("config", "uploadpack.allowFilter", "true")
		dst := t.TempDir()
		src.gitIn(dst, nil, "clone", "-q", "--filter=blob:none", "--no-checkout", "file://"+src.dir, dst)
		cl := &repo{t: t, dir: dst}
		repo := open(t, dst)
		first, err := repo.Resolve(t.Context(), source.Last(), quiet)
		if err != nil {
			t.Fatal(err)
		}
		if first.FileCount() != 3 || first.RenameDetection() != source.RenameDetectionMissingBlobs {
			t.Fatalf("before the fetch: %d files, %v; want 3, RenameDetectionMissingBlobs", first.FileCount(), first.RenameDetection())
		}
		if got := missingPaths(first); len(got) != 3 {
			t.Fatalf("missing before the fetch = %v; want every file", got)
		}
		// Asking about the absent blobs fetches none, so nothing has changed yet.
		if changed, err := first.Changed(t.Context()); err != nil || changed {
			t.Fatalf("Changed before the fetch = %v, %v; want false", changed, err)
		}
		if got := missingPaths(first); len(got) != 3 {
			t.Fatalf("missing after Changed = %v; want every file still", got)
		}
		// The test's own git fetches the blobs on demand; the source package's runner would not.
		for _, rev := range []string{"HEAD~1:a.txt", "HEAD:b.txt", "HEAD~1:c.txt", "HEAD:c.txt"} {
			cl.git("cat-file", "-p", rev)
		}
		second, err := repo.Resolve(t.Context(), source.Last(), quiet)
		if err != nil {
			t.Fatal(err)
		}
		if second.FileCount() != 2 {
			t.Fatalf("after the fetch: %d files; want 2", second.FileCount())
		}
		if f := second.File(fileIndex(t, second, "b.txt")); f.Status != source.StatusRenamed || f.OldPath != "a.txt" {
			t.Fatalf("b.txt = %+v; want renamed from a.txt", f)
		}
		if first.BaseTree() != second.BaseTree() || first.HeadTree() != second.HeadTree() {
			t.Fatal("the trees differ across the fetch")
		}
		if first.SameFiles(second) || second.SameFiles(first) {
			t.Error("SameFiles = true across a changed listing")
		}
		// REQ-SES-010: the change check sees the listing change the trees do not show.
		if changed, err := first.Changed(t.Context()); err != nil || !changed {
			t.Errorf("Changed of the snapshot listed before the fetch = %v, %v; want true", changed, err)
		}
		if changed, err := second.Changed(t.Context()); err != nil || changed {
			t.Errorf("Changed of the snapshot listed after the fetch = %v, %v; want false", changed, err)
		}
	})

	t.Run("nothing changed", func(t *testing.T) {
		t.Parallel()
		r := listingRepo(t)
		r.write("c.txt", "dirty\n")
		for _, src := range []source.Source{source.Last(), source.Uncommitted()} {
			repo := open(t, r.dir)
			a, err := repo.Resolve(t.Context(), src, quiet)
			if err != nil {
				t.Fatal(err)
			}
			b, err := repo.Resolve(t.Context(), src, quiet)
			if err != nil {
				t.Fatal(err)
			}
			if !a.SameFiles(b) || !b.SameFiles(a) {
				t.Errorf("%s: SameFiles = false for two resolves with no change", src)
			}
		}
	})

	t.Run("a subset of the missing blobs arriving", func(t *testing.T) {
		t.Parallel()
		r := listingRepo(t)
		r.write("d.txt", "d one\n")
		r.commit("three")
		r.write("c.txt", "c changed\n")
		r.write("d.txt", "d two\n")
		r.commit("four")
		// Keep the old blobs, restore one after the first resolve.
		hideAt := func(rev, p string) (restore func()) {
			id := r.objectID(rev, p)
			obj := r.path(".git/objects/" + id[:2] + "/" + id[2:])
			aside := obj + ".aside"
			if err := os.Rename(obj, aside); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.Rename(aside, obj); err != nil {
					t.Fatal(err)
				}
			}
		}
		hide := func(p string) (restore func()) { return hideAt("HEAD~1", p) }
		restoreC := hide("c.txt")
		hide("d.txt")
		repo := open(t, r.dir)
		first, err := repo.Resolve(t.Context(), source.Last(), quiet)
		if err != nil {
			t.Fatal(err)
		}
		restoreC()
		second, err := repo.Resolve(t.Context(), source.Last(), quiet)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []*source.Snapshot{first, second} {
			if s.RenameDetection() != source.RenameDetectionMissingBlobs {
				t.Fatalf("RenameDetection = %v; want RenameDetectionMissingBlobs", s.RenameDetection())
			}
		}
		if first.FileCount() != second.FileCount() {
			t.Fatalf("file counts %d and %d differ", first.FileCount(), second.FileCount())
		}
		if len(missingPaths(first)) == len(missingPaths(second)) {
			t.Fatalf("missing %v and %v; want different subsets", missingPaths(first), missingPaths(second))
		}
		if first.SameFiles(second) || second.SameFiles(first) {
			t.Error("SameFiles = true although the Missing flags differ")
		}
		if changed, err := first.Changed(t.Context()); err != nil || !changed {
			t.Errorf("Changed after one of two absent blobs arrived = %v, %v; want true", changed, err)
		}
		if changed, err := second.Changed(t.Context()); err != nil || changed {
			t.Errorf("Changed with the other blob still absent = %v, %v; want false", changed, err)
		}

		// d.txt's old blob is still absent; with its new one absent too, the new one arriving
		// leaves d.txt Missing: the listing, and so Changed, stays.
		restoreNew := hideAt("HEAD", "d.txt")
		third, err := repo.Resolve(t.Context(), source.Last(), quiet)
		if err != nil {
			t.Fatal(err)
		}
		restoreNew()
		if changed, err := third.Changed(t.Context()); err != nil || changed {
			t.Errorf("Changed after one of a file's two absent blobs arrived = %v, %v; want false", changed, err)
		}
	})
}
