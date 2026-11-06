package source_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// snapshotView is everything a snapshot reports, read through the exported API.
type snapshotView struct {
	Base, Head string
	Files      []source.ChangedFile
	Diffs      []source.FileDiff
	Counts     map[int]source.LineCount
	Searches   map[string]source.SearchResult
	Renames    source.RenameDetection
}

// viewTexts are the searches a snapshotView records: a common word, an ASCII-cased one, and
// non-ASCII text whose case a UTF-8 locale would fold.
var viewTexts = []string{"line", "LINE", "élan", "ÉLAN"}

func view(t *testing.T, snap *source.Snapshot) snapshotView {
	t.Helper()
	v := snapshotView{Base: snap.BaseTree(), Head: snap.HeadTree(), Counts: map[int]source.LineCount{}, Renames: snap.RenameDetection()}
	for i := range snap.FileCount() {
		f := snap.File(i)
		v.Files = append(v.Files, f)
		d, err := snap.Diff(t.Context(), i)
		if err != nil {
			t.Fatalf("Diff(%s): %v", f.Path(), err)
		}
		v.Diffs = append(v.Diffs, d)
	}
	err := snap.NumStat(t.Context(), func(i int, c source.LineCount) { v.Counts[i] = c })
	if err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	v.Searches = map[string]source.SearchResult{}
	for _, text := range viewTexts {
		v.Searches[text] = search(t, snap, text)
	}
	return v
}

// TestResolveIgnoresHostileConfigurationAndEnvironment checks that repository configuration that
// changes porcelain diff or grep output, inherited Git variables and the locale leave the
// snapshot, its hunks, its line counts and its search results unchanged. It is not parallel because it sets process environment variables.
func TestResolveIgnoresHostileConfigurationAndEnvironment(t *testing.T) {
	r := newRepo(t)
	var long strings.Builder
	for i := range 40 {
		long.WriteString("line " + string(rune('a'+i%26)) + "\n")
	}
	r.write("sub/dir/code.txt", long.String())
	r.write("blank.txt", "one\n\ntwo\n\nthree\n")
	r.write("moved.txt", "keep 1\nkeep 2\nkeep 3\nkeep 4\nkeep 5\n")
	r.write("x[ab].txt", "glob\n")
	r.write("xa.txt", "plain\n")
	r.write("café.txt", "accent\n")
	r.commit("base")
	r.write("sub/dir/code.txt", strings.Replace(strings.Replace(long.String(), "line e\n", "line E\n", 1), "line t\n", "", 1))
	r.write("blank.txt", "one\n\nTWO!\n\nthree\n") // size differs: see TestUncommittedSeesSameSizeEdit
	r.remove("moved.txt")
	r.write("renamed.txt", "keep 1\nkeep 2\nkeep 3\nkeep 4\nkeep 5\n")
	r.write("x[ab].txt", "glob changed\n")
	r.write("xa.txt", "plain changed\n")
	r.write("café.txt", "accent changed\n")
	r.write("accent.txt", "élan\nÉLAN\nLINE upper\n")

	want := view(t, resolve(t, r.dir, source.Uncommitted()))
	if want.Renames != source.RenameDetectionFull {
		t.Fatal("fixture: renames not detected under a clean configuration")
	}
	if n := len(want.Searches["élan"].Matches); n != 1 {
		t.Fatalf("fixture: élan matches %d lines under a clean configuration, want 1", n)
	}

	for _, kv := range [][2]string{
		{"diff.noprefix", "true"},
		{"diff.mnemonicPrefix", "true"},
		{"diff.relative", "true"},
		{"diff.suppressBlankEmpty", "true"},
		{"diff.renames", "false"},
		{"diff.context", "0"},
		{"diff.interHunkContext", "10"},
		{"diff.external", "false"},
		{"diff.hostile.textconv", "tr a-z A-Z"},
		{"color.ui", "always"},
		{"color.diff", "always"},
		{"core.quotePath", "false"},
		{"core.pager", "false"},
		{"pager.diff-tree", "true"},
		{"alias.diff-tree", "log"},
		{"grep.patternType", "perl"},
		{"grep.lineNumber", "false"},
		{"grep.column", "false"},
		{"color.grep", "always"},
		{"submodule.recurse", "true"},
	} {
		r.git("config", kv[0], kv[1])
	}
	if err := writeFile(r.path(".git/info/attributes"), "*.txt diff=hostile\n"); err != nil {
		t.Fatal(err)
	}

	decoy := newRepo(t)
	decoy.write("decoy.txt", "decoy\n")
	decoy.commit("decoy")
	for k, v := range map[string]string{
		"GIT_DIR":                          decoy.path(".git"),
		"GIT_WORK_TREE":                    decoy.dir,
		"GIT_INDEX_FILE":                   decoy.path(".git/index"),
		"GIT_COMMON_DIR":                   decoy.path(".git"),
		"GIT_OBJECT_DIRECTORY":             decoy.path(".git/objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": decoy.path(".git/objects"),
		"GIT_EXTERNAL_DIFF":                "false",
		"GIT_DIFF_OPTS":                    "-u0",
		"GIT_CONFIG_PARAMETERS":            "'diff.noprefix'='true'",
		"GIT_CONFIG_COUNT":                 "1",
		"GIT_CONFIG_KEY_0":                 "diff.context",
		"GIT_CONFIG_VALUE_0":               "0",
		"GIT_GLOB_PATHSPECS":               "1",
		"GIT_ICASE_PATHSPECS":              "1",
		"GIT_PAGER":                        "false",
		"PAGER":                            "false",
		"LANG":                             "en_US.UTF-8",
		"LC_ALL":                           "en_US.UTF-8",
	} {
		t.Setenv(k, v)
	}

	got := view(t, resolve(t, r.path("sub/dir"), source.Uncommitted()))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hostile configuration changed the snapshot:\n got %+v\nwant %+v", got, want)
	}
}

// repoState is what must stay unchanged after building an uncommitted snapshot: index bytes, refs,
// configuration, status, every file under the Git directory except objects, and the worktree's
// files.
type repoState struct {
	Index    string
	Refs     string
	Status   string
	GitFiles map[string]string
	Worktree map[string]string
}

func (r *repo) state(worktree string) repoState {
	r.t.Helper()
	index := strings.TrimSpace(string(r.gitIn(worktree, nil, "rev-parse", "--path-format=absolute", "--git-path", "index")))
	common := strings.TrimSpace(string(r.gitIn(worktree, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")))
	noLocks := []string{"GIT_OPTIONAL_LOCKS=0"}
	return repoState{
		Index: hashFile(r.t, index),
		Refs: string(r.gitIn(worktree, noLocks, "for-each-ref", "--format=%(refname) %(objectname) %(symref)")) +
			string(r.gitIn(worktree, noLocks, "rev-parse", "HEAD")) +
			string(r.gitIn(worktree, noLocks, "symbolic-ref", "-q", "HEAD")),
		Status:   string(r.gitIn(worktree, noLocks, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignored")),
		GitFiles: hashTree(r.t, common, func(rel string) bool { return rel == "objects" }),
		Worktree: hashTree(r.t, worktree, func(rel string) bool { return rel == ".git" }),
	}
}

func hashFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashTree maps every file under root, except skipped top-level entries, to its mode and hash.
func hashTree(t *testing.T, root string, skip func(rel string) bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel != "." && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h := info.Mode().String()
		if d.Type().IsRegular() {
			h += " " + hashFile(t, p)
		}
		out[rel] = h
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestUncommittedSnapshotLeavesRepositoryUnchanged checks that building the uncommitted tree from
// a private index copy leaves the repository's repoState unchanged. It is not parallel because it
// points TMPDIR at a directory it inspects.
func TestUncommittedSnapshotLeavesRepositoryUnchanged(t *testing.T) {
	variants := []struct {
		name  string
		setup func(r *repo) (worktree string)
	}{
		{"plain", func(r *repo) string { return r.dir }},
		{"split index", func(r *repo) string {
			r.git("config", "core.splitIndex", "true")
			r.git("update-index", "--split-index")
			r.write("after-split.txt", "x\n")
			r.git("add", "after-split.txt")
			return r.dir
		}},
		{"linked worktree", func(r *repo) string {
			wt := r.path("../linked")
			r.git("worktree", "add", "-q", "-b", "linked", wt, "feature")
			if err := writeFile(wt+"/a.txt", "changed in linked\n"); err != nil {
				r.t.Fatal(err)
			}
			if err := writeFile(wt+"/linked-new.txt", "untracked\n"); err != nil {
				r.t.Fatal(err)
			}
			return wt
		}},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			r := forkFixture(t)
			r.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			worktree := v.setup(r)
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			before := r.state(worktree)
			for _, src := range []source.Source{source.Uncommitted(), branchSource(t, "main", true)} {
				resolve(t, worktree, src)
				if after := r.state(worktree); !reflect.DeepEqual(after, before) {
					t.Errorf("%s changed the repository:\nbefore %+v\n after %+v", src, before, after)
				}
			}
			ref, err := open(t, worktree).OriginHead(t.Context())
			if err != nil {
				t.Fatalf("OriginHead: %v", err)
			}
			resolve(t, worktree, branchSource(t, ref, true))
			if after := r.state(worktree); !reflect.DeepEqual(after, before) {
				t.Errorf("OriginHead and resolving its target changed the repository:\nbefore %+v\n after %+v", before, after)
			}
			entries, err := os.ReadDir(tmp)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("temporary files remain in TMPDIR: %v", entries)
			}
		})
	}
}
