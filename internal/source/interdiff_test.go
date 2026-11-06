package source_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// numbered returns lines "01\n" .. "<n>\n", line k replaced by edits[k], which may hold several lines.
func numbered(n int, edits map[int]string) string {
	var b strings.Builder
	for k := 1; k <= n; k++ {
		if e, ok := edits[k]; ok {
			b.WriteString(e + "\n")
			continue
		}
		b.WriteString(itoa(k) + "\n")
	}
	return b.String()
}

func itoa(k int) string { return fmt.Sprintf("%02d", k) }

// roundsFixture is a file reviewed at version r1 and changed since, in a snapshot from base to the
// worktree: the snapshot's old side is base's blob, its new side r2's.
type roundsFixture struct {
	r        *repo
	snap     *source.Snapshot
	i        int
	base, r1 string
	r1Text   string
	r2Text   string
}

func newRoundsFixture(t *testing.T) roundsFixture {
	t.Helper()
	r := newRepo(t)
	r.write("a.txt", numbered(10, nil))
	r.commit("base")
	r.git("tag", "base")
	r1 := numbered(10, map[int]string{3: "03 r1"})
	r.write("a.txt", r1)
	r.commit("r1")
	reviewed := r.objectID("HEAD", "a.txt")
	// r2 keeps r1's line 3, inserts two lines after line 1 and edits line 8.
	r2 := numbered(10, map[int]string{1: "01\nins 1\nins 2", 3: "03 r1", 8: "08 r2"})
	r.write("a.txt", r2)
	snap := resolve(t, r.dir, branchSource(t, "base", true))
	return roundsFixture{
		r: r, snap: snap, i: fileIndex(t, snap, "a.txt"),
		base: r.objectID("base", "a.txt"), r1: reviewed, r1Text: r1, r2Text: r2,
	}
}

// hashed writes content as a blob and returns its ID.
func (r *repo) hashed(content string) string {
	r.t.Helper()
	return strings.TrimSpace(string(r.gitInStdin(content, "hash-object", "-w", "--stdin")))
}

// absentID is a valid object ID no fixture repository holds.
const absentID = "0123456789abcdef0123456789abcdef01234567"

// Interdiff diffs the reviewed version with the snapshot's new side (Definitions: interdiff):
// only the changes since review, numbered as the snapshot's new side.
func TestInterdiff(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	d, err := f.snap.Interdiff(t.Context(), f.i, source.Blob{Mode: "100644", ID: f.r1})
	if err != nil {
		t.Fatal(err)
	}
	if string(d.Old) != f.r1Text || string(d.New) != f.r2Text {
		t.Errorf("interdiff sides:\n old %q\n new %q\nwant r1 %q and r2 %q", d.Old, d.New, f.r1Text, f.r2Text)
	}
	// r1 → r2: two lines added after line 1 and line 8 replaced; line 3, changed since base, is not.
	var got []string
	for _, h := range d.Hunks {
		for _, l := range h.Lines {
			switch l.Op {
			case source.OpAdd:
				got = append(got, "+"+itoa(l.New)+" "+l.Text)
			case source.OpDelete:
				got = append(got, "-"+itoa(l.Old)+" "+l.Text)
			case source.OpContext:
			}
		}
	}
	want := []string{"+02 ins 1", "+03 ins 2", "-08 08", "+10 08 r2"}
	if !slices.Equal(got, want) {
		t.Errorf("interdiff changed lines %q, want %q", got, want)
	}
	checkHunks(t, "interdiff", d.Hunks, d.Old, d.New)

	same, err := f.snap.Interdiff(t.Context(), f.i, source.Blob{Mode: "100644", ID: f.snap.File(f.i).NewBlob})
	if err != nil || len(same.Hunks) != 0 {
		t.Errorf("interdiff from the current blob: %d hunks, %v; want none", len(same.Hunks), err)
	}
}

func TestInterdiffUnavailable(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	binary := f.r.hashed("\x00bin\n")
	for _, tc := range []struct {
		name     string
		reviewed source.Blob
		want     error
	}{
		{"pruned blob", source.Blob{Mode: "100644", ID: absentID}, source.ErrMissing},
		{"binary reviewed version", source.Blob{Mode: "100644", ID: binary}, source.ErrNotText},
		{"reviewed as a symlink", source.Blob{Mode: "120000", ID: f.r1}, source.ErrNotText},
		{"reviewed as a gitlink", source.Blob{Mode: "160000", ID: f.r1}, source.ErrNotText},
	} {
		if _, err := f.snap.Interdiff(t.Context(), f.i, tc.reviewed); !errors.Is(err, tc.want) {
			t.Errorf("%s: Interdiff error %v, want %v", tc.name, err, tc.want)
		}
	}

	r := newRepo(t)
	r.write("big.txt", "small\n")
	r.commit("base")
	reviewed := r.objectID("HEAD", "big.txt")
	r.write("big.txt", strings.Repeat("b", source.LargeBytes+1)+"\n")
	snap := resolve(t, r.dir, source.Uncommitted())
	if _, err := snap.Interdiff(t.Context(), fileIndex(t, snap, "big.txt"), source.Blob{Mode: "100644", ID: reviewed}); !errors.Is(err, source.ErrNotText) {
		t.Errorf("large current version: Interdiff error %v, want ErrNotText", err)
	}

	large := f.r.hashed(strings.Repeat("l\n", source.LargeLines+1))
	if _, err := f.snap.Interdiff(t.Context(), f.i, source.Blob{Mode: "100644", ID: large}); !errors.Is(err, source.ErrNotText) {
		t.Errorf("large reviewed version: Interdiff error %v, want ErrNotText", err)
	}
}

// ChangesFrom reports git's -U0 hunk ranges from a blob to either side of a changed file: exactly
// the lines deleted and added, which placing earlier comments relies on.
func TestChangesFrom(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	type hr = source.HunkRange
	for _, tc := range []struct {
		name    string
		newSide bool
		from    source.Blob
		want    []hr
	}{
		// @@ -1,0 +2,2 @@ and @@ -8 +10 @@
		{"r1 to the new side", true, source.Blob{Mode: "100644", ID: f.r1}, []hr{{1, 0, 2, 2}, {8, 1, 10, 1}}},
		// r1's line 3 is base's line 3 edited: @@ -3 +3 @@
		{"r1 to the old side", false, source.Blob{Mode: "100644", ID: f.r1}, []hr{{3, 1, 3, 1}}},
		{"another mode diffs lines only", true, source.Blob{Mode: "100755", ID: f.r1}, []hr{{1, 0, 2, 2}, {8, 1, 10, 1}}},
		{"the side's own blob", false, source.Blob{Mode: "100644", ID: f.base}, nil},
	} {
		got, err := f.snap.ChangesFrom(t.Context(), f.i, tc.newSide, tc.from)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: ChangesFrom = %+v, want %+v", tc.name, got, tc.want)
		}
	}

	binary := f.r.hashed("\x00bin\n")
	for _, tc := range []struct {
		name string
		from source.Blob
		want error
	}{
		{"pruned blob", source.Blob{Mode: "100644", ID: absentID}, source.ErrMissing},
		{"binary blob", source.Blob{Mode: "100644", ID: binary}, source.ErrNotText},
		{"symlink against a file", source.Blob{Mode: "120000", ID: f.r1}, source.ErrNotText},
		{"gitlink", source.Blob{Mode: "160000", ID: f.r1}, source.ErrNotText},
		{"large earlier blob", source.Blob{Mode: "100644", ID: f.r.hashed(strings.Repeat("x", source.LargeBytes+1) + "\n")}, source.ErrNotText},
	} {
		if _, err := f.snap.ChangesFrom(t.Context(), f.i, true, tc.from); !errors.Is(err, tc.want) {
			t.Errorf("%s: ChangesFrom error %v, want %v", tc.name, err, tc.want)
		}
	}

	// A large current side is not compared, by bytes or by lines; equal large blobs stay current.
	rl := newRepo(t)
	rl.write("big.txt", numbered(10, nil))
	rl.commit("base")
	smallFrom := source.Blob{Mode: "100644", ID: rl.objectID("HEAD", "big.txt")}
	for _, tc := range []struct{ name, content string }{
		{"bytes", strings.Repeat("b", source.LargeBytes+1) + "\n"},
		{"lines", strings.Repeat("b\n", source.LargeLines+1)},
	} {
		rl.write("big.txt", tc.content)
		bs := resolve(t, rl.dir, source.Uncommitted())
		bi := fileIndex(t, bs, "big.txt")
		if _, err := bs.ChangesFrom(t.Context(), bi, true, smallFrom); !errors.Is(err, source.ErrNotText) {
			t.Errorf("large current side, %s: ChangesFrom error %v, want ErrNotText", tc.name, err)
		}
		same := source.Blob{Mode: "100644", ID: rl.hashed(tc.content)}
		if got, err := bs.ChangesFrom(t.Context(), bi, true, same); err != nil || got != nil {
			t.Errorf("equal large blobs, %s: ChangesFrom = %+v, %v; want nil", tc.name, got, err)
		}
	}

	// Git's binary decision follows the side's own path: here the old path's attributes say binary.
	ra := newRepo(t)
	ra.write(".gitattributes", "*.dat binary\n")
	ra.write("old.dat", numbered(10, nil))
	ra.commit("base")
	ra.git("mv", "old.dat", "new.txt")
	ra.write("new.txt", numbered(10, map[int]string{9: "09 edited"}))
	renamed := resolve(t, ra.dir, source.Uncommitted())
	ri := fileIndex(t, renamed, "new.txt")
	if renamed.File(ri).OldPath != "old.dat" {
		t.Fatalf("fixture: %+v is not a rename from old.dat", renamed.File(ri))
	}
	earlier := source.Blob{Mode: "100644", ID: ra.hashed(numbered(10, map[int]string{2: "02 earlier"}))}
	if _, err := renamed.ChangesFrom(t.Context(), ri, false, earlier); !errors.Is(err, source.ErrNotText) {
		t.Errorf("old side under a binary path: ChangesFrom error %v, want ErrNotText", err)
	}
	if got, err := renamed.ChangesFrom(t.Context(), ri, true, earlier); err != nil || len(got) != 2 {
		t.Errorf("new side under a text path: ChangesFrom = %+v, %v; want two hunks", got, err)
	}

	r := newRepo(t)
	r.write("keep.txt", "keep\n")
	r.commit("base")
	r.write("added.txt", "new\n")
	snap := resolve(t, r.dir, source.Uncommitted())
	_, err := snap.ChangesFrom(t.Context(), fileIndex(t, snap, "added.txt"), false, source.Blob{Mode: "100644", ID: f.r1})
	if err == nil || errors.Is(err, source.ErrMissing) || errors.Is(err, source.ErrNotText) {
		t.Errorf("old side of an added file: ChangesFrom error %v, want a plain failure", err)
	}
}

// WhitespaceOnly marks exactly the hunks of a diff whose changes vanish under -w, for a snapshot
// diff and an interdiff alike (Definitions: whitespace-only).
func TestWhitespaceOnly(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a.txt", numbered(30, nil))
	r.commit("base")
	r.git("tag", "base")
	r1 := numbered(30, map[int]string{25: "25 r1"})
	r.write("a.txt", r1)
	r.commit("r1")
	reviewed := r.objectID("HEAD", "a.txt")
	// Since r1: line 3 re-indented, line 25 edited again. Since base, line 25 differs from r1 too.
	r.write("a.txt", numbered(30, map[int]string{3: "  03", 25: "25 r2"}))
	snap := resolve(t, r.dir, branchSource(t, "base", true))
	i := fileIndex(t, snap, "a.txt")

	full, err := snap.Diff(t.Context(), i)
	if err != nil {
		t.Fatal(err)
	}
	inter, err := snap.Interdiff(t.Context(), i, source.Blob{Mode: "100644", ID: reviewed})
	if err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]source.FileDiff{"snapshot diff": full, "interdiff": inter} {
		got, err := snap.WhitespaceOnly(t.Context(), d)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := []bool{true, false}; !slices.Equal(got, want) {
			t.Errorf("%s: WhitespaceOnly = %v, want %v (re-indent, edit)", name, got, want)
		}
	}
}
