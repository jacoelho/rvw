package server_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pairingRepo moves foo/x unchanged to bar and adds a file foo with almost foo/x's content, and
// replaces d/x with a file d of unrelated content. A diff restricted to foo also matches foo/x
// and would pair it with foo.
func pairingRepo(t *testing.T) *testRepo {
	t.Helper()
	repo := newTestRepo(t)
	content := numberedLines("line", 200)
	repo.write("foo/x", content)
	repo.write("d/x", numberedLines("old d", 20))
	repo.commit("base")
	repo.git("mv", "foo/x", "bar")
	for _, dir := range []string{"foo", "d"} {
		if err := os.RemoveAll(filepath.Join(repo.dir, dir)); err != nil {
			t.Fatal(err)
		}
	}
	repo.write("foo", strings.Replace(content, "line 100\n", "line one hundred\n", 1))
	repo.write("d", "unrelated\n")
	repo.commit("head")
	return repo
}

// TestFileDiffFollowsTheFileList: each file's diff pairs its sides as the session's file list
// does.
func TestFileDiffFollowsTheFileList(t *testing.T) {
	t.Parallel()
	f := newFixture(t, pairingRepo(t), fixtureOptions{})
	f.serveHandler()
	s := f.session()
	var got []string
	for _, fd := range s.Files {
		got = append(got, fd.Status+" "+fd.OldPath+">"+fd.NewPath)
	}
	slices.Sort(got)
	want := []string{"A >d", "A >foo", "D d/x>", "R foo/x>bar"}
	if !slices.Equal(got, want) {
		t.Fatalf("files %q; want %q", got, want)
	}
	for _, tc := range []struct {
		path string
		only string // the one block member every line is in
	}{{"foo", "add"}, {"d/x", "del"}} {
		d := f.fileDiff(s.fileIndex(t, tc.path))
		n := 0
		for _, h := range d.Hunks {
			for _, b := range h.Blocks {
				lines := map[string]int{"ctx": len(b.Ctx), "del": len(b.Del), "add": len(b.Add)}
				for member, k := range lines {
					if member != tc.only && k > 0 {
						t.Errorf("%s has %d %s lines; want only %s", tc.path, k, member, tc.only)
					}
				}
				n += lines[tc.only]
			}
		}
		if n == 0 {
			t.Errorf("%s has no %s lines", tc.path, tc.only)
		}
	}
}
