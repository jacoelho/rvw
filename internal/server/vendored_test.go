package server_test

import (
	"encoding/json/v2"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// vendoredRepo has vendored and ordinary files, and renames into and out of a vendored
// directory. Each file changes a distinct number of lines, so a count on the wrong file shows.
func vendoredRepo(t *testing.T) *testRepo {
	t.Helper()
	r := newTestRepo(t)
	r.write("src/keep.go", numberedLines("keep", 10))
	r.write("vendor/lib/a.go", numberedLines("lib", 10))
	r.write("web/node_modules/dep/i.js", numberedLines("dep", 10))
	r.write("vendor/moved.txt", numberedLines("moved", 10))
	r.write("src/to_vendor.txt", numberedLines("tovendor", 10))
	r.write("a\nb/vendor/x.go", "x\n")
	// Each vendor directory below is vendored only beside a go.sum.
	r.write("go.sum", "sum\n")
	r.write("a\nb/go.sum", "sum\n")
	r.write("pkg/vendor/n.go", numberedLines("n", 10))
	r.commit("base")
	r.write("src/keep.go", numberedLines("keep", 10)+"k1\n")
	r.write("vendor/lib/a.go", numberedLines("lib", 10)+"l1\nl2\n")
	r.write("web/node_modules/dep/i.js", numberedLines("dep", 10)+"d1\nd2\nd3\n")
	r.write("node_modules/p/index.js", "p\n")
	r.git("mv", "vendor/moved.txt", "src/moved.txt")
	r.write("src/moved.txt", numberedLines("moved", 9)+"moved changed\n")
	r.git("mv", "src/to_vendor.txt", "vendor/to_vendor.txt")
	r.write("vendor/to_vendor.txt", numberedLines("tovendor", 9)+"tovendor changed\n")
	r.write("a\nb/vendor/x.go", "x changed\n")
	r.write("pkg/vendor/n.go", numberedLines("n", 10)+"n1\nn2\nn3\nn4\n")
	r.commit("head")
	return r
}

// TestSessionReportsVendoredDirs: vendored files stay listed and commentable, and name their
// vendored directory in the form the UI shows the path, so it is a prefix of that path.
func TestSessionReportsVendoredDirs(t *testing.T) {
	t.Parallel()
	f := newFixture(t, vendoredRepo(t), fixtureOptions{})
	f.serveHandler()
	r := f.do(http.MethodGet, "/api/session", "")
	f.mustStatus(r, http.StatusOK, "GET /api/session")
	// A pointer tells an absent vendored_dir from an empty one: absent is the contract.
	var raw struct {
		Files []struct {
			VendoredDir *string `json:"vendored_dir"`
			Status      string  `json:"status"`
			OldPath     string  `json:"old_path"`
			NewPath     string  `json:"new_path"`
			Commentable bool    `json:"commentable"`
		} `json:"files"`
	}
	if err := json.Unmarshal(r.body, &raw); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, file := range raw.Files {
		path := file.NewPath
		if path == "" {
			path = file.OldPath
		}
		if (path == "src/moved.txt" || path == "vendor/to_vendor.txt") && file.Status != "R" {
			t.Errorf("%s: status %s; want R", path, file.Status)
		}
		if file.VendoredDir == nil {
			continue
		}
		got[path] = *file.VendoredDir
		if !file.Commentable && path != `a\nb/vendor/x.go` {
			t.Errorf("%s: vendored file not commentable", path)
		}
	}
	want := map[string]string{
		"vendor/lib/a.go":           "vendor",
		"web/node_modules/dep/i.js": "web/node_modules",
		"node_modules/p/index.js":   "node_modules",
		"vendor/to_vendor.txt":      "vendor",
		`a\nb/vendor/x.go`:          `a\nb/vendor`,
	}
	if len(raw.Files) != 8 || !maps.Equal(got, want) {
		t.Errorf("%d files with vendored_dir %v; want 8 files with %v and no other vendored_dir", len(raw.Files), got, want)
	}
}

// TestCountsSkipVendoredFiles: vendored files get no line count; the others keep theirs, under
// their own index. A rename out of a vendored directory is counted; one into it is not.
func TestCountsSkipVendoredFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t, vendoredRepo(t), fixtureOptions{})
	f.serveRun()
	s := f.session()
	want := map[int]countDTO{}
	for path, c := range map[string]countDTO{
		"src/keep.go":     {Added: 1},
		"src/moved.txt":   {Added: 1, Deleted: 1},
		"pkg/vendor/n.go": {Added: 4},
	} {
		i := s.fileIndex(t, path)
		c.I = i
		want[i] = c
	}
	if got := f.lineCounts(); !maps.Equal(got, want) {
		t.Errorf("counts %+v; want %+v (indexes of src/keep.go, src/moved.txt, pkg/vendor/n.go)", got, want)
	}
}

// TestVendorFileAndDirectorySwap: a file named vendor is not vendored and is counted, whether it
// replaces a vendor directory or is replaced by one; the files below the directory are vendored.
// A blob missing elsewhere leaves the file counted.
func TestVendorFileAndDirectorySwap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		toFile        bool
		missingBlob   bool
		wantVendorDir map[string]string
		wantCount     countDTO
	}{
		{"a file replaces the directory", true, false,
			map[string]string{"vendor": "", "vendor/a.go": "vendor", "vendor/b.go": "vendor"}, countDTO{Added: 3}},
		{"a file replaces the directory, a blob missing", true, true,
			map[string]string{"vendor": "", "vendor/a.go": "vendor", "vendor/b.go": "vendor"}, countDTO{Added: 3}},
		{"the directory replaces a file", false, false,
			map[string]string{"vendor": "", "vendor/a.go": "vendor"}, countDTO{Deleted: 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newTestRepo(t)
			r.write("other.txt", "o\n")
			r.write("go.sum", "sum\n")
			if tc.toFile {
				r.write("vendor/a.go", "package a\n")
				r.write("vendor/b.go", "package b\n")
			} else {
				r.write("vendor", "one\ntwo\nthree\n")
			}
			r.commit("base")
			if err := os.RemoveAll(filepath.Join(r.dir, "vendor")); err != nil {
				t.Fatal(err)
			}
			if tc.toFile {
				r.write("vendor", "one\ntwo\nthree\n")
			} else {
				r.write("vendor/a.go", "package a\n")
			}
			r.write("other.txt", "o changed\n")
			r.commit("head")
			if tc.missingBlob {
				r.removeBlob("HEAD~1", "other.txt")
			}

			f := newFixture(t, r, fixtureOptions{})
			f.serveRun()
			s := f.session()
			if wantMode := map[bool]string{true: "missing_blobs", false: "full"}[tc.missingBlob]; s.RenameDetection != wantMode {
				t.Fatalf("rename_detection %q; want %q", s.RenameDetection, wantMode)
			}
			got := map[string]string{}
			for _, file := range s.Files {
				path := file.NewPath
				if path == "" {
					path = file.OldPath
				}
				if path != "other.txt" {
					got[path] = file.VendoredDir
				}
			}
			if !maps.Equal(got, tc.wantVendorDir) {
				t.Errorf("vendored_dir %v; want %v", got, tc.wantVendorDir)
			}
			want := map[int]countDTO{}
			i := s.fileIndex(t, "vendor")
			c := tc.wantCount
			c.I = i
			want[i] = c
			if !tc.missingBlob {
				j := s.fileIndex(t, "other.txt")
				want[j] = countDTO{I: j, Added: 1, Deleted: 1}
			}
			if got := f.lineCounts(); !maps.Equal(got, want) {
				t.Errorf("counts %+v; want %+v", got, want)
			}
		})
	}
}
