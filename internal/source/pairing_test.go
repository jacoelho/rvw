package source_test

import (
	"os"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// onlyOps reports whether d has hunks and every hunk line has op.
func onlyOps(d source.FileDiff, op source.LineOp) bool {
	if len(d.Hunks) == 0 {
		return false
	}
	for _, h := range d.Hunks {
		for _, l := range h.Lines {
			if l.Op != op {
				return false
			}
		}
	}
	return true
}

// changeIndex finds the snapshot index of the changed file with status st and paths old, new.
func changeIndex(t *testing.T, snap *source.Snapshot, st source.Status, oldPath, newPath string) int {
	t.Helper()
	for i := range snap.FileCount() {
		if f := snap.File(i); f.Status == st && f.OldPath == oldPath && f.NewPath == newPath {
			return i
		}
	}
	t.Fatalf("no changed file %c %q -> %q in snapshot %v", st, oldPath, newPath, snapshotChanges(snap))
	return -1
}

// TestPairingFollowsTheFileList: a file's patch and counts pair its sides as the changed-file
// list does. Here the list pairs foo/x with bar, an exact rename, and adds a file foo with almost
// the same content; a diff restricted to the path foo also matches foo/x and would pair it with
// foo instead.
func TestPairingFollowsTheFileList(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	content := numberedFile("line", 200)
	r.write("foo/x", content)
	r.commit("base")
	r.git("mv", "foo/x", "bar")
	if err := os.RemoveAll(r.path("foo")); err != nil {
		t.Fatal(err)
	}
	r.write("foo", strings.Replace(content, "line 100\n", "line one hundred\n", 1))
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())

	added := changeIndex(t, snap, source.StatusAdded, "", "foo")
	d, err := snap.Diff(t.Context(), added)
	if err != nil {
		t.Fatalf("Diff(A foo): %v", err)
	}
	if d.Facts.Kind != source.KindText || !onlyOps(d, source.OpAdd) {
		t.Errorf("Diff(A foo) = kind %v, %d hunks; want text with only added lines", d.Facts.Kind, len(d.Hunks))
	}
	renamed := changeIndex(t, snap, source.StatusRenamed, "foo/x", "bar")
	d, err = snap.Diff(t.Context(), renamed)
	if err != nil {
		t.Fatalf("Diff(R foo/x -> bar): %v", err)
	}
	if d.Facts.Kind != source.KindText || len(d.Hunks) != 0 {
		t.Errorf("Diff(R foo/x -> bar) = kind %v, %d hunks; want text with none", d.Facts.Kind, len(d.Hunks))
	}
	got := map[int]source.LineCount{}
	if err := snap.NumStat(t.Context(), func(i int, c source.LineCount) { got[i] = c }); err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	if c := got[added]; c != (source.LineCount{Added: 200}) {
		t.Errorf("NumStat(A foo) = %+v; want 200 added, 0 deleted", c)
	}
	if c := got[renamed]; c != (source.LineCount{}) {
		t.Errorf("NumStat(R foo/x -> bar) = %+v; want 0 added, 0 deleted", c)
	}
}

// TestPairingPastTheRenameLimit: when the list exceeded -l1000, it pairs only exact renames, so a
// file d replacing a directory holding a similar d/x is an addition and a deletion. A diff
// restricted to d sees two candidates, within the limit, and would pair them.
func TestPairingPastTheRenameLimit(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	content := numberedFile("line", 50)
	stream := renameLimitStreamWith(1001,
		[][2]string{{"d/x", content}},
		[][2]string{{"d", strings.Replace(content, "line 25\n", "line twenty-five\n", 1)}})
	r.gitInStdin(stream, "fast-import", "--quiet")
	snap := resolve(t, r.dir, source.Last())
	if got := snap.RenameDetection(); got != source.RenameDetectionLimitExceeded {
		t.Fatalf("RenameDetection = %v; want RenameDetectionLimitExceeded", got)
	}

	d, err := snap.Diff(t.Context(), changeIndex(t, snap, source.StatusAdded, "", "d"))
	if err != nil {
		t.Fatalf("Diff(A d): %v", err)
	}
	if !onlyOps(d, source.OpAdd) {
		t.Errorf("Diff(A d) = kind %v, %d hunks; want only added lines", d.Facts.Kind, len(d.Hunks))
	}
	d, err = snap.Diff(t.Context(), changeIndex(t, snap, source.StatusDeleted, "d/x", ""))
	if err != nil {
		t.Fatalf("Diff(D d/x): %v", err)
	}
	if !onlyOps(d, source.OpDelete) {
		t.Errorf("Diff(D d/x) = kind %v, %d hunks; want only deleted lines", d.Facts.Kind, len(d.Hunks))
	}
}
