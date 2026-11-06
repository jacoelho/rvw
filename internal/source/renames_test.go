package source_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// renameLimitStream is a git fast-import stream of two commits on main. Between them, exact.txt
// moves unchanged to moved.txt, and n distinct files are deleted while n others are added: more
// inexact rename candidates than -l1000 allows when n exceeds 1000.
func renameLimitStream(n int) string { return renameLimitStreamWith(n, nil, nil) }

// renameLimitStreamWith is renameLimitStream with extra files in the base and head commits.
func renameLimitStreamWith(n int, base, head [][2]string) string {
	var b strings.Builder
	file := func(path, content string) {
		fmt.Fprintf(&b, "M 100644 inline %s\ndata %d\n%s\n", path, len(content), content)
	}
	commit := func(msg string) {
		fmt.Fprintf(&b, "commit refs/heads/main\ncommitter T <t@example.com> 0 +0000\ndata %d\n%s\n", len(msg), msg)
	}
	exact := "the same content on both sides\n"
	commit("base")
	file("exact.txt", exact)
	for i := range n {
		file(fmt.Sprintf("old/%04d.txt", i), fmt.Sprintf("deleted %d\n", i))
	}
	for _, f := range base {
		file(f[0], f[1])
	}
	commit("head")
	// The branch continues from base within one stream.
	b.WriteString("deleteall\n")
	file("moved.txt", exact)
	for i := range n {
		file(fmt.Sprintf("new/%04d.txt", i), fmt.Sprintf("added %d\n", i))
	}
	for _, f := range head {
		file(f[0], f[1])
	}
	return b.String()
}

// TestRenameLimitExceeded covers plan item J: when Git skips inexact rename detection because of
// -l1000, the snapshot says so, and exact renames are still detected.
func TestRenameLimitExceeded(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	const n = 1001
	r.gitInStdin(renameLimitStream(n), "fast-import", "--quiet")

	snap := resolve(t, r.dir, source.Last())
	if got := snap.RenameDetection(); got != source.RenameDetectionLimitExceeded {
		t.Errorf("RenameDetection = %v; want RenameDetectionLimitExceeded", got)
	}
	if got, want := snap.FileCount(), 2*n+1; got != want {
		t.Errorf("FileCount = %d; want %d", got, want)
	}
	var renames []source.ChangedFile
	for i := range snap.FileCount() {
		if f := snap.File(i); f.Status == source.StatusRenamed {
			renames = append(renames, f)
		}
	}
	if len(renames) != 1 || renames[0].OldPath != "exact.txt" || renames[0].NewPath != "moved.txt" {
		t.Errorf("renames %+v; want exactly exact.txt -> moved.txt", renames)
	}
}

// TestRenameLimitNotReached is the boundary: with candidates within -l1000, detection is full.
func TestRenameLimitNotReached(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.gitInStdin(renameLimitStream(1000), "fast-import", "--quiet")
	if got := resolve(t, r.dir, source.Last()).RenameDetection(); got != source.RenameDetectionFull {
		t.Errorf("RenameDetection = %v; want RenameDetectionFull", got)
	}
}
