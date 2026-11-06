package source_test

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// vendoredDirs returns every changed file's displayed path mapped to its vendored directory.
func vendoredDirs(snap *source.Snapshot) map[string]string {
	out := map[string]string{}
	for i := range snap.FileCount() {
		out[snap.File(i).Path()] = snap.VendoredDir(i)
	}
	return out
}

func checkVendored(t *testing.T, snap *source.Snapshot, want map[string]string) {
	t.Helper()
	got := vendoredDirs(snap)
	if !slices.Equal(slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want))) {
		t.Fatalf("changed files = %q; the fixture needs %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("VendoredDir(%q) = %q; want %q", p, got[p], w)
		}
	}
}

// content returns distinct multi-line content for p, so that no two fixture files look alike to
// rename detection.
func content(p string) string { return strings.Repeat(p+" line\n", 8) }

// symlink records a symlink at p in the index, pointing at target.
func (r *repo) symlink(p, target string) {
	r.t.Helper()
	id := strings.TrimSpace(string(r.gitInStdin(target, "hash-object", "-w", "--stdin")))
	r.git("update-index", "--add", "--cacheinfo", "120000,"+id+","+p)
}

// TestVendoredDir covers the vendored-directory rule on the head side: node_modules always,
// vendor only beside a regular go.sum, exact names, and the outermost qualifying directory.
func TestVendoredDir(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	want := map[string]string{
		"go.sum":                     "",
		"vendor/a.go":                "vendor",
		"vendor/vendor/b.go":         "vendor",
		"vendor/node_modules/x.js":   "vendor",
		"node_modules/x/vendor/y.js": "node_modules",
		"lib/vendor/a.go":            "", // go.sum only at the root, not beside lib/vendor
		"c/go.sum":                   "",
		"c/Vendor/a.go":              "",
		"vendors/x":                  "",
		"myvendor/x":                 "",
		".vendor/x":                  "",
		"vendor.go/x":                "",
		"s/go.sum":                   "",
		"s/vendor":                   "", // the file's own name
		"m/go.sum":                   "",
		"m/vendor/a.go":              "m/vendor",
		"m/sub/go.sum":               "",
		"m/sub/vendor/b.go":          "m/sub/vendor",
		"x/go.sum":                   "", // executable
		"x/vendor/a.go":              "x/vendor",
		"l/go.sum":                   "", // symlink
		"l/vendor/a.go":              "",
		"d/go.sum/inner":             "", // go.sum is a directory
		"d/vendor/a.go":              "",
		"in/vendor/go.sum":           "", // inside vendor, not beside it
		"in/vendor/a.go":             "",
		"Gs/Go.sum":                  "",
		"Gs/vendor/a.go":             "",
		"no/node_modules/p.js":       "no/node_modules",
		"n/vendor/mod/go.sum":        "",
		"n/vendor/mod/vendor/y.go":   "n/vendor/mod/vendor",
		"n/vendor/other.go":          "",
		"n2/go.sum":                  "",
		"n2/vendor/mod/go.sum":       "n2/vendor",
		"n2/vendor/mod/vendor/y.go":  "n2/vendor",
		"g[1]/go.sum":                "",
		"g[1]/vendor/x.go":           "g[1]/vendor",
		"h1/go.sum":                  "",
		"h[1]/vendor/x.go":           "",
		"-dash/go.sum":               "",
		"-dash/vendor/x.go":          "-dash/vendor",
		"nl\nname/go.sum":            "",
		"nl\nname/vendor/x.go":       "nl\nname/vendor",
	}
	for p := range want {
		if p != "l/go.sum" {
			r.write(p, content(p))
		}
	}
	r.git("add", "-A")
	r.git("update-index", "--chmod=+x", "x/go.sum")
	r.symlink("l/go.sum", "../go.sum")
	r.git("commit", "-q", "--no-verify", "-m", "head")
	if got := r.mode("HEAD", "x/go.sum"); got != "100755" {
		t.Fatalf("x/go.sum mode %s; the fixture needs 100755", got)
	}
	checkVendored(t, resolve(t, r.dir, source.Last()), want)
}

// TestVendoredDirDisplayedSide covers the tree go.sum is looked up in: the base tree for a
// deleted file, the head tree otherwise, with renames judged on the new path.
func TestVendoredDirDisplayedSide(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	for _, p := range []string{
		"p/go.sum", "p/vendor/del.go", "p/vendor/mod.go",
		"q/vendor/del.go", "q/vendor/mod.go",
		"r/go.sum", "r/vendor/f.go", "r/in/g.go",
		"s/go.sum", "s/vendor/del.go", "s/vendor/mod.go",
	} {
		r.write(p, content(p))
	}
	r.commit("base")
	r.remove("p/go.sum")
	r.remove("p/vendor/del.go")
	r.write("p/vendor/mod.go", "changed\n")
	r.write("p/vendor/new.go", content("p/vendor/new.go"))
	r.write("q/go.sum", content("q/go.sum"))
	r.remove("q/vendor/del.go")
	r.write("q/vendor/mod.go", "changed\n")
	r.remove("r/vendor/f.go")
	r.write("r/out/f.go", content("r/vendor/f.go"))
	r.remove("r/in/g.go")
	r.write("r/vendor/g.go", content("r/in/g.go"))
	r.remove("s/go.sum")
	r.remove("s/vendor/del.go")
	r.write("s/vendor/mod.go", "changed\n")
	r.git("add", "-A")
	r.symlink("s/go.sum", "../r/go.sum")
	r.git("commit", "-q", "--no-verify", "-m", "head")
	snap := resolve(t, r.dir, source.Last())
	for _, p := range []string{"r/out/f.go", "r/vendor/g.go"} {
		if f := snap.File(fileIndex(t, snap, p)); f.Status != source.StatusRenamed {
			t.Fatalf("%s status %c; the fixture needs a rename", p, f.Status)
		}
	}
	checkVendored(t, snap, map[string]string{
		"p/go.sum":        "",
		"p/vendor/del.go": "p/vendor",
		"p/vendor/mod.go": "",
		"p/vendor/new.go": "",
		"q/go.sum":        "",
		"q/vendor/del.go": "",
		"q/vendor/mod.go": "q/vendor",
		"r/out/f.go":      "",
		"r/vendor/g.go":   "r/vendor",
		"s/go.sum":        "",
		"s/vendor/del.go": "s/vendor",
		"s/vendor/mod.go": "",
	})
}

// TestVendoredDirUncommitted covers the uncommitted source, whose head tree holds the worktree,
// untracked files included.
func TestVendoredDirUncommitted(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	for _, p := range []string{"p/go.sum", "p/vendor/del.go", "p/vendor/mod.go"} {
		r.write(p, content(p))
	}
	r.commit("base")
	r.remove("p/go.sum")
	r.remove("p/vendor/del.go")
	r.write("p/vendor/mod.go", "changed\n")
	r.write("q/go.sum", content("q/go.sum"))
	r.write("q/vendor/a.go", content("q/vendor/a.go"))
	checkVendored(t, resolve(t, r.dir, source.Uncommitted()), map[string]string{
		"p/go.sum":        "",
		"p/vendor/del.go": "p/vendor",
		"p/vendor/mod.go": "",
		"q/go.sum":        "",
		"q/vendor/a.go":   "q/vendor",
	})
}

// TestVendoredDirNeedsNoBlobs checks that finding go.sum reads trees, not blobs, so a missing
// go.sum blob neither fails the snapshot nor changes the rule.
func TestVendoredDirNeedsNoBlobs(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("go.sum", content("go.sum"))
	r.commit("base")
	r.write("vendor/a.go", content("vendor/a.go"))
	r.commit("head")
	r.removeBlob("HEAD", "go.sum")
	checkVendored(t, resolve(t, r.dir, source.Last()), map[string]string{"vendor/a.go": "vendor"})
}

// TestVendoredDirBatches covers pathspec batching: more go.sum candidates than one batch holds.
func TestVendoredDirBatches(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("keep.txt", "base\n")
	r.commit("base")
	long := strings.Repeat("n", 240)
	want := map[string]string{}
	for i := range 300 {
		m := fmt.Sprintf("%04d-%s", i, long)
		want[m+"/vendor/x.go"] = ""
		r.write(m+"/vendor/x.go", "x\n")
		if i%3 == 0 {
			r.write(m+"/go.sum", "sum\n")
			want[m+"/go.sum"] = ""
			want[m+"/vendor/x.go"] = m + "/vendor"
		}
	}
	r.commit("head")
	checkVendored(t, resolve(t, r.dir, source.Last()), want)
}
