package source_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/source"
)

// removeOldBlob deletes the base-side loose object of path. Git cannot read it back from the
// worktree, so any command that needs it fails.
func (r *repo) removeOldBlob(path string) {
	r.t.Helper()
	r.removeBlob("HEAD~1", path)
}

// removeBlob deletes the loose object of path's blob at rev.
func (r *repo) removeBlob(rev, path string) {
	r.t.Helper()
	id := r.objectID(rev, path)
	if err := os.Remove(r.path(".git/objects/" + id[:2] + "/" + id[2:])); err != nil {
		r.t.Fatalf("remove the blob of %.20s… at %s: %v", path, rev, err)
	}
}

// missingPaths returns the paths of snap's files marked Missing.
func missingPaths(snap *source.Snapshot) map[string]bool {
	out := map[string]bool{}
	for i := range snap.FileCount() {
		if f := snap.File(i); f.Missing {
			out[f.Path()] = true
		}
	}
	return out
}

// collectCounts runs NumStat and returns its counts by changed path, failing on a file reported
// twice.
func collectCounts(t *testing.T, snap *source.Snapshot) map[string]source.LineCount {
	t.Helper()
	got := map[string]source.LineCount{}
	err := snap.NumStat(t.Context(), func(i int, c source.LineCount) {
		f := snap.File(i)
		if _, dup := got[f.Path()]; dup {
			t.Errorf("%s counted twice", f.Path())
		}
		got[f.Path()] = c
	})
	if err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	return got
}

// TestNumStatWithMissingBlobs covers plan item H: with blobs missing, every file whose blobs are
// present is counted and no file with a missing blob is. Long names make the paths several times
// a command line's pathspec budget; names that are pathspec syntax must name only themselves.
func TestNumStatWithMissingBlobs(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	const n = 1200
	long := strings.Repeat("n", 240)
	name := func(i int) string { return fmt.Sprintf("%04d-%s.txt", i, long) }
	lines := func(prefix string, i, count int) string {
		var b strings.Builder
		for k := range count {
			fmt.Fprintf(&b, "%s %d %d\n", prefix, i, k)
		}
		return b.String()
	}
	special := []string{"-dash.txt", "[br].txt", "r.txt", "sp ace.txt", ":magic.txt"}
	for i := range n {
		r.write(name(i), lines("old", i, i%4+1))
	}
	for _, p := range special {
		r.write(p, "old "+p+"\n")
	}
	r.commit("base")
	want := map[string]source.LineCount{}
	for i := range n {
		r.write(name(i), lines("new", i, i%3+1))
		want[name(i)] = source.LineCount{Added: i%3 + 1, Deleted: i%4 + 1}
	}
	for _, p := range special {
		r.write(p, "new "+p+"\n")
		want[p] = source.LineCount{Added: 1, Deleted: 1}
	}
	r.commit("head")
	// r.txt is what the glob [br].txt would also match.
	for _, p := range []string{name(7), name(n / 2), name(n - 1), "r.txt"} {
		r.removeOldBlob(p)
		delete(want, p)
	}

	snap := resolve(t, r.dir, source.Last())
	if got := snap.RenameDetection(); got != source.RenameDetectionMissingBlobs {
		t.Fatalf("RenameDetection = %v; want RenameDetectionMissingBlobs", got)
	}
	gone := map[string]bool{name(7): true, name(n / 2): true, name(n - 1): true, "r.txt": true}
	if got := missingPaths(snap); !maps.Equal(got, gone) {
		t.Errorf("files marked missing: %d %v; want %v", len(got), slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(gone)))
	}
	got := collectCounts(t, snap)
	for p, w := range want {
		if c, ok := got[p]; !ok || c != w {
			t.Errorf("%.20s… count %+v (reported %v); want %+v", p, c, ok, w)
		}
	}
	for p, c := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("%.20s… has a missing blob but was counted %+v", p, c)
		}
	}
}

// TestNumStatWithEveryBlobMissing covers plan item H's edge: with no file to count, NumStat runs
// no command, since a command without paths would count, and fail on, every file.
func TestNumStatWithEveryBlobMissing(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("base")
	r.write("a.txt", "a changed\n")
	r.commit("head")
	r.removeOldBlob("a.txt")
	if got := collectCounts(t, resolve(t, r.dir, source.Last())); len(got) != 0 {
		t.Errorf("counts %v; want none", got)
	}
}

// A blob shared by several files is missing for every file that names it, on either side, and
// for no other file.
func TestMissingSharedBlob(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("s1.txt", "shared\n")
	r.write("s2.txt", "shared\n")
	r.write("u.txt", "unrelated\n")
	r.commit("base")
	r.write("s1.txt", "one\n")
	r.write("s2.txt", "two\n")
	r.write("u.txt", "unrelated changed\n")
	r.write("added.txt", "shared\n")
	r.commit("head")
	r.removeOldBlob("s1.txt")

	snap := resolve(t, r.dir, source.Last())
	want := map[string]bool{"s1.txt": true, "s2.txt": true, "added.txt": true}
	if got := missingPaths(snap); !maps.Equal(got, want) {
		t.Errorf("files marked missing: %v; want %v", got, want)
	}
	if got := snap.RenameDetection(); got != source.RenameDetectionMissingBlobs {
		t.Errorf("RenameDetection = %v; want RenameDetectionMissingBlobs", got)
	}
}

// A submodule's commit is never in the superproject's object store, so a gitlink to an absent
// commit marks nothing missing.
func TestSubmoduleCommitIsNotAMissingBlob(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("base")
	r.write("a.txt", "a changed\n")
	r.git("add", "-A")
	r.git("update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("ab", 20)+",sub")
	r.git("commit", "-q", "--no-verify", "-m", "head")

	snap := resolve(t, r.dir, source.Last())
	if snap.FileCount() != 2 {
		t.Fatalf("FileCount = %d; want 2", snap.FileCount())
	}
	if got := missingPaths(snap); len(got) != 0 {
		t.Errorf("files marked missing: %v; want none", got)
	}
	if got := snap.RenameDetection(); got != source.RenameDetectionFull {
		t.Errorf("RenameDetection = %v; want RenameDetectionFull", got)
	}
}

// Resolving with a cancelled context fails with the context's error and leaves the repository
// usable.
func TestResolveCancelled(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("base")
	r.write("a.txt", "a changed\n")
	r.commit("head")
	r.removeOldBlob("a.txt")
	repo := open(t, r.dir)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := repo.Resolve(ctx, source.Last(), quiet); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve with a cancelled context = %v; want context.Canceled", err)
	}
	snap, err := repo.Resolve(t.Context(), source.Last(), quiet)
	if err != nil {
		t.Fatalf("Resolve after a cancelled one: %v", err)
	}
	if got := missingPaths(snap); !maps.Equal(got, map[string]bool{"a.txt": true}) {
		t.Errorf("files marked missing: %v; want a.txt", got)
	}
}

// TestNumStatAcrossAFileAndDirectorySwap: a missing blob leaves only its own file uncounted. A
// file that replaced a directory holding the missing blob, or was replaced by one, is counted, as
// is a sibling sharing its name as a string prefix.
func TestNumStatAcrossAFileAndDirectorySwap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, base, head, missing, missingRev string
		a                                     source.LineCount
	}{
		{"a file replaces a directory", "a/b", "a", "a/b", "HEAD~1", source.LineCount{Added: 1}},
		{"a file replaces a nested directory", "a/b/c", "a", "a/b/c", "HEAD~1", source.LineCount{Added: 1}},
		{"a directory replaces a file", "a", "a/x", "a/x", "HEAD", source.LineCount{Deleted: 1}},
		{"a nested directory replaces a file", "a", "a/x/y", "a/x/y", "HEAD", source.LineCount{Deleted: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t)
			r.write(tc.base, "before\n")
			r.write("ab", "x\n")
			r.write("m.txt", "m\n")
			r.commit("base")
			if err := os.RemoveAll(r.path("a")); err != nil {
				t.Fatal(err)
			}
			r.write(tc.head, "after\n")
			r.write("ab", "x changed\n")
			r.write("m.txt", "m changed\n")
			r.commit("head")
			r.removeBlob(tc.missingRev, tc.missing)

			snap := resolve(t, r.dir, source.Last())
			if got := missingPaths(snap); !maps.Equal(got, map[string]bool{tc.missing: true}) {
				t.Fatalf("files marked missing: %v; want %s", got, tc.missing)
			}
			got := collectCounts(t, snap)
			changed := source.LineCount{Added: 1, Deleted: 1}
			want := map[string]source.LineCount{"a": tc.a, "ab": changed, "m.txt": changed}
			if !maps.Equal(got, want) {
				t.Errorf("counts %v; want %v", got, want)
			}
		})
	}
}

// TestNumStatYieldsInListOrder: counts arrive in ascending file order, each for its own file,
// across a rename, a binary file and plain edits.
func TestNumStatYieldsInListOrder(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.write("bin.dat", "\x00\x01\x02old")
	r.write("old-name.txt", strings.Repeat("kept line\n", 20))
	r.write("z.txt", "z\n")
	r.commit("base")
	r.write("a.txt", "a\nb\n")
	r.write("bin.dat", "\x00\x01\x02new")
	r.remove("old-name.txt")
	r.write("new-name.txt", strings.Repeat("kept line\n", 20)+"added\n")
	r.write("z.txt", "")
	r.commit("head")

	snap := resolve(t, r.dir, source.Last())
	want := map[string]source.LineCount{
		"a.txt":        {Added: 1},
		"bin.dat":      {Binary: true},
		"new-name.txt": {Added: 1},
		"z.txt":        {Deleted: 1},
	}
	var order []int
	err := snap.NumStat(t.Context(), func(i int, c source.LineCount) {
		order = append(order, i)
		if p := snap.File(i).Path(); c != want[p] {
			t.Errorf("file %d %s: count %+v; want %+v", i, p, c, want[p])
		}
	})
	if err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	if len(order) != len(want) {
		t.Errorf("yield order %v; want %d files", order, len(want))
	}
	for k := 1; k < len(order); k++ {
		if order[k] <= order[k-1] {
			t.Errorf("yield order %v is not strictly ascending", order)
		}
	}
	if k := slices.IndexFunc(order, func(i int) bool { return snap.File(i).Status == source.StatusRenamed }); k < 0 {
		t.Errorf("no rename was counted; files %v", snapshotChanges(snap))
	}
}

// TestNumStatCancelledBetweenFiles: cancelling inside the first yield stops NumStat before the
// next count, with the context's error, promptly and without leaving diff-pairs running. Counts
// are parsed one record at a time and ctx is checked between them. Not parallel: the PID log
// changes PATH.
func TestNumStatCancelledBetweenFiles(t *testing.T) {
	r := newRepo(t)
	for i := range 3 {
		r.write(fmt.Sprintf("f%d.txt", i), "old\n")
	}
	r.commit("base")
	for i := range 3 {
		r.write(fmt.Sprintf("f%d.txt", i), "new\n")
	}
	r.commit("head")
	pidLog := logGitPIDs(t, `*" diff-pairs "*`)
	snap := resolve(t, r.dir, source.Last())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	yields := 0
	done := make(chan error, 1)
	go func() {
		done <- snap.NumStat(ctx, func(int, source.LineCount) {
			yields++
			cancel()
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("NumStat cancelled in its first yield = %v; want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("NumStat cancelled in its first yield did not return")
	}
	if yields != 1 {
		t.Errorf("%d yields; want 1", yields)
	}
	processesGone(t, readPIDs(t, pidLog))
}
