package source_test

import (
	"bytes"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

const nonUTF8Path = "bad\xff.txt"

// classificationFixture builds one commit holding every kind of change a file can be classified
// as. The submodule and the non-UTF-8 path are written with plumbing: APFS rejects non-UTF-8
// names, and a gitlink needs no checkout.
func classificationFixture(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)
	var long strings.Builder
	for i := range 30 {
		long.WriteString("rename edit line " + strings.Repeat("x", i) + "\n")
	}
	r.write("rename-same.txt", "same 1\nsame 2\nsame 3\nsame 4\n")
	r.write("rename-edit.txt", long.String())
	r.write("mode.sh", "echo mode\n")
	r.write("typechange", "regular\n")
	r.write("binary.bin", "bin\x00ary\x01\n")
	r.write("noeol.txt", "a\nb")
	r.write("deleted.txt", "gone 1\ngone 2\n")
	r.write("-dash.txt", "dash\n")
	r.write("with space.txt", "space\n")
	r.write("x[ab].txt", "glob\n")
	r.write("xa.txt", "plain\n")
	r.write(".gitattributes", "attr.txt binary\n")
	r.write("attr.txt", "text marked binary\n")
	r.write("new\nline.txt", "newline\n")
	r.write(`quo"te.txt`, "quote\n")
	root := r.commit("base")

	r.remove("rename-same.txt")
	r.write("moved-same.txt", "same 1\nsame 2\nsame 3\nsame 4\n")
	r.remove("rename-edit.txt")
	r.write("moved-edit.txt", strings.Replace(long.String(), "rename edit line xxxxx\n", "edited\n", 1))
	if err := os.Chmod(r.path("mode.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.remove("typechange")
	if err := os.Symlink("target", r.path("typechange")); err != nil {
		t.Fatal(err)
	}
	r.write("binary.bin", "bin\x00ary\x02\n")
	r.write("noeol.txt", "a\nB")
	r.write("empty.txt", "")
	r.remove("deleted.txt")
	r.write("-dash.txt", "dash changed\n")
	r.write("with space.txt", "space changed\n")
	r.write("x[ab].txt", "glob changed\n")
	r.write("xa.txt", "plain changed\n")
	r.write("attr.txt", "text marked binary, changed\n")
	r.write("new\nline.txt", "newline changed\n")
	r.write(`quo"te.txt`, "quote changed\n")
	r.git("add", "-A")
	r.git("update-index", "--add", "--cacheinfo", "160000,"+root+",sub")
	r.addBlob(nonUTF8Path, "not utf-8 path\n")
	r.git("commit", "-q", "--no-verify", "-m", "changes")
	return r
}

// addBlob stages content at path without touching the worktree.
func (r *repo) addBlob(path, content string) {
	r.t.Helper()
	id := strings.TrimSpace(string(r.gitInStdin(content, "hash-object", "-w", "--stdin")))
	r.git("update-index", "--add", "--cacheinfo", "100644,"+id+","+path)
}

// TestChangedFileClassification checks that each changed file's status, paths, modes and blobs
// equal git's, its kind is text, binary, submodule or symlink as git sees it, text hunks match
// both blobs, and line counts equal git's numstat.
func TestChangedFileClassification(t *testing.T) {
	t.Parallel()
	r := classificationFixture(t)
	snap := resolve(t, r.dir, source.Last())
	r.checkSnapshot(snap, r.tree("HEAD^"), r.tree("HEAD"))
	if got := snap.RenameDetection(); got != source.RenameDetectionFull {
		t.Errorf("RenameDetection = %v with every blob present; want RenameDetectionFull", got)
	}

	wantKind := map[string]source.FileKind{
		"moved-same.txt": source.KindText,
		"moved-edit.txt": source.KindText,
		"mode.sh":        source.KindModeOnly,
		"typechange":     source.KindText,
		"binary.bin":     source.KindBinary,
		"noeol.txt":      source.KindText,
		"empty.txt":      source.KindText,
		"deleted.txt":    source.KindText,
		"-dash.txt":      source.KindText,
		"with space.txt": source.KindText,
		"x[ab].txt":      source.KindText,
		"xa.txt":         source.KindText,
		"attr.txt":       source.KindBinary,
		"new\nline.txt":  source.KindText,
		`quo"te.txt`:     source.KindText,
		"sub":            source.KindSubmodule,
		nonUTF8Path:      source.KindText,
	}
	if snap.FileCount() != len(wantKind) {
		t.Errorf("FileCount = %d, want %d", snap.FileCount(), len(wantKind))
	}
	numstat := r.numstat(r.tree("HEAD^"), r.tree("HEAD"))
	for i := range snap.FileCount() {
		f := snap.File(i)
		t.Run(f.Path(), func(t *testing.T) {
			t.Parallel()
			r := r.on(t)
			checkEntry(t, r, f)
			d, err := snap.Diff(t.Context(), i)
			if err != nil {
				t.Fatalf("Diff: %v", err)
			}
			if want, ok := wantKind[f.Path()]; !ok || d.Facts.Kind != want {
				t.Errorf("Kind = %v, want %v", d.Facts.Kind, want)
			}
			facts, err := snap.Facts(t.Context(), i)
			if err != nil || !reflect.DeepEqual(facts, d.Facts) {
				t.Errorf("Facts = %+v, %v; want Diff's %+v", facts, err, d.Facts)
			}
			if d.Facts.Kind != source.KindText {
				if len(d.Hunks) != 0 || d.Facts.Hunks != nil {
					t.Errorf("kind %v file has %d hunks", d.Facts.Kind, len(d.Hunks))
				}
				return
			}
			if len(d.Facts.Hunks) != len(d.Hunks) {
				t.Fatalf("Facts.Hunks has %d hunks, Hunks %d", len(d.Facts.Hunks), len(d.Hunks))
			}
			for k, h := range d.Hunks {
				if h.HunkRange != d.Facts.Hunks[k] {
					t.Errorf("hunk %d: range %+v, facts %+v", k, h.HunkRange, d.Facts.Hunks[k])
				}
			}
			oldData, newData := sideBlob(t, r, "HEAD^", f.OldPath), sideBlob(t, r, "HEAD", f.NewPath)
			added, deleted := checkHunks(t, f.Path(), d.Hunks, oldData, newData)
			want := numstat[f.OldPath+"\x00"+f.NewPath]
			if added != want.Added || deleted != want.Deleted {
				t.Errorf("hunks add %d delete %d lines; git numstat %+v", added, deleted, want)
			}
			checkSide(t, "old", d.Facts.Old, d.Old, oldData, f.OldPath != "")
			checkSide(t, "new", d.Facts.New, d.New, newData, f.NewPath != "")
		})
	}

	t.Run("numstat", func(t *testing.T) {
		t.Parallel()
		got := map[int]source.LineCount{}
		err := snap.NumStat(t.Context(), func(i int, c source.LineCount) {
			if _, dup := got[i]; dup {
				t.Errorf("file %d reported twice", i)
			}
			got[i] = c
		})
		if err != nil {
			t.Fatal(err)
		}
		for i := range snap.FileCount() {
			f := snap.File(i)
			want, ok := numstat[f.OldPath+"\x00"+f.NewPath]
			if c, reported := got[i]; reported != ok || c != want {
				t.Errorf("%s: NumStat %+v (reported %t), git %+v (reported %t)", f.Path(), c, reported, want, ok)
			}
		}
		if bin := got[fileIndex(t, snap, "binary.bin")]; !bin.Binary {
			t.Errorf("binary.bin counts %+v, want Binary", bin)
		}
	})

	t.Run("submodule has no content", func(t *testing.T) {
		t.Parallel()
		i := fileIndex(t, snap, "sub")
		if _, err := linesOf(t.Context(), snap, i, true, 1, 1); err == nil {
			t.Error("Lines of a submodule: no error")
		}
	})

	t.Run("absent side has no lines", func(t *testing.T) {
		t.Parallel()
		if _, err := linesOf(t.Context(), snap, fileIndex(t, snap, "empty.txt"), false, 1, 1); err == nil {
			t.Error("Lines of an added file's old side: no error")
		}
		if _, err := linesOf(t.Context(), snap, fileIndex(t, snap, "deleted.txt"), true, 1, 1); err == nil {
			t.Error("Lines of a deleted file's new side: no error")
		}
	})
}

// checkEntry compares a changed file's modes and blobs with the trees' entries.
func checkEntry(t *testing.T, r *repo, f source.ChangedFile) {
	t.Helper()
	want := source.ChangedFile{OldPath: f.OldPath, NewPath: f.NewPath, Status: f.Status, OldMode: "000000", NewMode: "000000"}
	if f.OldPath != "" {
		want.OldMode, want.OldBlob = r.mode("HEAD^", f.OldPath), r.objectID("HEAD^", f.OldPath)
	}
	if f.NewPath != "" {
		want.NewMode, want.NewBlob = r.mode("HEAD", f.NewPath), r.objectID("HEAD", f.NewPath)
	}
	if f != want {
		t.Errorf("changed file\n got %+v\nwant %+v", f, want)
	}
}

func sideBlob(t *testing.T, r *repo, rev, path string) []byte {
	t.Helper()
	if path == "" {
		return nil
	}
	return r.blob(rev, path)
}

func checkSide(t *testing.T, name string, got source.SideFacts, content, data []byte, present bool) {
	t.Helper()
	if got.Present != present {
		t.Errorf("%s side Present = %t, want %t", name, got.Present, present)
	}
	if !present {
		return
	}
	if !bytes.Equal(content, data) || got.Size != int64(len(data)) || got.Lines != len(blobLines(data)) {
		t.Errorf("%s side = {%d bytes, Size %d, Lines %d}, blob has %d bytes and %d lines",
			name, len(content), got.Size, got.Lines, len(data), len(blobLines(data)))
	}
}

// TestDiffLiteralHunk pins one small patch as git prints it, including the no-final-newline mark.
func TestDiffLiteralHunk(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("f.txt", "one\ntwo\nthree\n")
	r.commit("base")
	r.write("f.txt", "one\n2\nthree\nfour")
	r.commit("edit")
	d, err := resolve(t, r.dir, source.Last()).Diff(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []source.Hunk{{
		OldStart: 1, OldCount: 3, NewStart: 1, NewCount: 4,
		Lines: []source.Line{
			{Op: source.OpContext, Text: "one", Old: 1, New: 1},
			{Op: source.OpDelete, Text: "two", Old: 2},
			{Op: source.OpAdd, Text: "2", New: 2},
			{Op: source.OpContext, Text: "three", Old: 3, New: 3},
			{Op: source.OpAdd, Text: "four", New: 4, NoEOL: true},
		},
	}}
	if d.Facts.Kind != source.KindText || !reflect.DeepEqual(d.Hunks, want) {
		t.Errorf("Diff = %v %+v\nwant text %+v", d.Facts.Kind, d.Hunks, want)
	}
}

// TestLineFidelity checks that CRLF, lone CR, invalid UTF-8 and a missing final newline survive
// byte for byte, and patch lines equal the lines Lines returns.
func TestLineFidelity(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	oldText := "crlf one\r\nlone\rcr\n\xff\xfe bad\nkeep\r\nplain\nlast"
	newText := "crlf ONE\r\nlone\rCR\n\xff\xfe BAD\nkeep\r\nplain\nlast changed"
	r.write("f.txt", oldText)
	r.commit("base")
	r.write("f.txt", newText)
	r.commit("edit")
	snap := resolve(t, r.dir, source.Last())
	d, err := snap.Diff(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Facts.Kind != source.KindText {
		t.Fatalf("Kind = %v, want text", d.Facts.Kind)
	}
	added, deleted := checkHunks(t, "f.txt", d.Hunks, []byte(oldText), []byte(newText))
	if added != 4 || deleted != 4 {
		t.Errorf("hunks add %d delete %d, want 4 and 4", added, deleted)
	}

	for _, h := range d.Hunks {
		oldN, newN := h.OldStart, h.NewStart
		for _, l := range h.Lines {
			if l.Op != source.OpAdd {
				checkLine(t, snap, false, oldN, l.Text)
				oldN++
			}
			if l.Op != source.OpDelete {
				checkLine(t, snap, true, newN, l.Text)
				newN++
			}
		}
	}
	for _, side := range []struct {
		content []byte
		text    string
		new     bool
	}{{d.Old, oldText, false}, {d.New, newText, true}} {
		want := blobLines([]byte(side.text))
		got, err := linesOf(t.Context(), snap, 0, side.new, 1, len(want))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Lines(new=%t) = %q, want %q", side.new, got, want)
		}
		if string(side.content) != side.text {
			t.Errorf("Diff content (new=%t) = %q, want %q", side.new, side.content, side.text)
		}
	}
}

func checkLine(t *testing.T, snap *source.Snapshot, newSide bool, n int, want string) {
	t.Helper()
	got, err := linesOf(t.Context(), snap, 0, newSide, n, n)
	if err != nil {
		t.Fatalf("Lines(new=%t, %d): %v", newSide, n, err)
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("Lines(new=%t, %d) = %q, patch line %q", newSide, n, got, want)
	}
}

// TestLinesBoundsHugeLine checks that a kept line is cut at LargeBytes and an earlier huge line is
// skipped, so a small range of a file with a multi-MiB line returns bounded, correct lines.
func TestLinesBoundsHugeLine(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	huge := strings.Repeat("y", 3<<20)
	r.write("huge.txt", huge+"\nx\n")
	r.commit("huge")
	snap := resolve(t, r.dir, source.Last())

	got, err := linesOf(t.Context(), snap, 0, true, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("Lines(2, 2) = %q, want [\"x\"]", got)
	}
	got, err = linesOf(t.Context(), snap, 0, true, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != "x" {
		t.Fatalf("Lines(1, 2) returned %d lines, want 2 ending with \"x\"", len(got))
	}
	if n := len(got[0]); n == 0 || n > source.LargeBytes || !strings.HasPrefix(huge, got[0]) {
		t.Errorf("Lines(1, 1) kept %d bytes, want a non-empty prefix of at most %d", n, source.LargeBytes)
	}
	// A quote keeps a few bytes but must know the whole length to state what it omitted.
	short, err := snap.Lines(t.Context(), 0, true, []source.LineRange{{Start: 1, End: 2}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]source.SideLine{{{Text: huge[:10], Bytes: len(huge)}, {Text: "x", Bytes: 1}}}
	if !reflect.DeepEqual(short, want) {
		t.Errorf("Lines(1, 2, keep 10) = %+v, want %+v", short, want)
	}
}

// TestLargeFileLimit covers the large-file boundaries: a side is large when it exceeds 1 MiB or
// 20,000 lines; a large file keeps its hunks and drops its content.
func TestLargeFileLimit(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("keep.txt", "k\n")
	r.commit("base")
	exactBytes := strings.Repeat(strings.Repeat("b", 1023)+"\n", 1024) // 1 MiB, 1,024 lines
	files := map[string]struct {
		content string
		large   bool
	}{
		"bytes-at-limit.txt":   {exactBytes, false},
		"bytes-over-limit.txt": {exactBytes + "z", true},
		"lines-at-limit.txt":   {strings.Repeat("l\n", source.LargeLines), false},
		"lines-over-limit.txt": {strings.Repeat("l\n", source.LargeLines+1), true},
	}
	if len(exactBytes) != 1<<20 {
		t.Fatalf("fixture: %d bytes, want 1 MiB", len(exactBytes))
	}
	for name, f := range files {
		r.write(name, f.content)
	}
	r.commit("sizes")
	snap := resolve(t, r.dir, source.Last())
	for name, f := range files {
		i := fileIndex(t, snap, name)
		d, err := snap.Diff(t.Context(), i)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.Facts.Kind != source.KindText || d.Facts.Large != f.large {
			t.Errorf("%s: Kind %v Large %t, want text Large %t", name, d.Facts.Kind, d.Facts.Large, f.large)
		}
		if len(d.Hunks) == 0 {
			t.Errorf("%s: no hunks", name)
		}
		c := d.Facts.New
		if (d.New == nil) != f.large || !c.Present || c.Size != int64(len(f.content)) {
			t.Errorf("%s: new side = {Present %t, Size %d, %d bytes of content}, large %t", name, c.Present, c.Size, len(d.New), f.large)
		}
	}
}

func TestDiffOutOfRange(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("f.txt", "f\n")
	r.commit("only")
	snap := resolve(t, r.dir, source.Last())
	for _, i := range []int{-1, snap.FileCount()} {
		if _, err := snap.Diff(t.Context(), i); err == nil {
			t.Errorf("Diff(%d): no error", i)
		}
		if _, err := linesOf(t.Context(), snap, i, true, 1, 1); err == nil {
			t.Errorf("Lines(%d): no error", i)
		}
	}
	if _, err := linesOf(t.Context(), snap, 0, true, 0, 1); err == nil {
		t.Error("Lines from line 0: no error")
	}
}

// TestLinesStopAtEndOfFile covers the Lines contract: a range past the end of a side returns
// exactly the side's lines, with or without a final newline, and no empty line after the last.
func TestLinesStopAtEndOfFile(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	texts := map[string]string{"lf.txt": "one\ntwo\n", "nolf.txt": "one\ntwo"}
	for p := range texts {
		r.write(p, "base\n")
	}
	r.commit("base")
	for p, text := range texts {
		r.write(p, text)
	}
	r.commit("edit")
	snap := resolve(t, r.dir, source.Last())
	for p, text := range texts {
		want := blobLines([]byte(text))
		got, err := linesOf(t.Context(), snap, fileIndex(t, snap, p), true, 1, len(want)+5)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Lines(1, %d) = %q, want %q", p, len(want)+5, got, want)
		}
	}
}

// TestDiffPureRename covers a rename without content changes, for which git prints no patch body:
// a binary file stays binary, and a text file has no hunks and its whole content as context.
func TestDiffPureRename(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	bin := "\x00\x01\x02 binary\n" + strings.Repeat("payload\n", 20)
	text := strings.Repeat("text line\n", 12)
	r.write("old.bin", bin)
	r.write("old.txt", text)
	r.commit("base")
	r.git("mv", "old.bin", "new.bin")
	r.git("mv", "old.txt", "new.txt")
	r.commit("head")
	snap := resolve(t, r.dir, source.Last())
	for p, want := range map[string]source.FileKind{"new.bin": source.KindBinary, "new.txt": source.KindText} {
		i := fileIndex(t, snap, p)
		if f := snap.File(i); f.Status != source.StatusRenamed {
			t.Fatalf("%s status %c; the fixture needs a rename", p, f.Status)
		}
		d, err := snap.Diff(t.Context(), i)
		if err != nil {
			t.Fatalf("Diff(%s): %v", p, err)
		}
		if d.Facts.Kind != want || len(d.Hunks) != 0 {
			t.Errorf("%s: kind %v with %d hunks; want %v with none", p, d.Facts.Kind, len(d.Hunks), want)
		}
	}
	d, err := snap.Diff(t.Context(), fileIndex(t, snap, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := d.Facts.Gaps(), []source.Gap{{From: 1, To: 12, OldFrom: 1}}; !slices.Equal(got, want) {
		t.Errorf("new.txt gaps %+v; want %+v", got, want)
	}
}
