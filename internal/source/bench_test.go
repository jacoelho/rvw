package source_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// BenchmarkResolve resolves the last commit of a repository whose last commit modifies n small
// files, with every blob present or with a few old blobs removed.
func BenchmarkResolve(b *testing.B) {
	for _, n := range []int{1000, 5000} {
		for _, missing := range []bool{false, true} {
			b.Run(fmt.Sprintf("files=%d/missing=%v", n, missing), func(b *testing.B) {
				dir, err := filepath.EvalSymlinks(b.TempDir())
				if err != nil {
					b.Fatal(err)
				}
				ctx := context.Background()
				git := func(args ...string) string {
					out, err := gitOutput(ctx, dir, nil, nil, args...)
					if err != nil {
						b.Fatal(err)
					}
					return strings.TrimSpace(string(out))
				}
				write := func(side string) {
					for i := range n {
						p := filepath.Join(dir, fmt.Sprintf("d%02d/f%05d.txt", i%50, i))
						if err := writeFile(p, fmt.Sprintf("%s %d\nshared\n", side, i)); err != nil {
							b.Fatal(err)
						}
					}
					git("add", "-A")
					git("commit", "-q", "--no-verify", "-m", side)
				}
				git("init", "-q", "-b", "main")
				write("old")
				write("new")
				want := source.RenameDetectionFull
				if missing {
					want = source.RenameDetectionMissingBlobs
					for _, i := range []int{7, n / 2, n - 1} {
						id := git("rev-parse", fmt.Sprintf("HEAD~1:d%02d/f%05d.txt", i%50, i))
						if err := os.Remove(filepath.Join(dir, ".git/objects", id[:2], id[2:])); err != nil {
							b.Fatal(err)
						}
					}
				}
				repo, err := source.Open(ctx, dir)
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = repo.Close() })
				check := func() {
					snap, err := repo.Resolve(ctx, source.Last(), quiet)
					if err != nil {
						b.Fatal(err)
					}
					if snap.FileCount() != n || snap.RenameDetection() != want {
						b.Fatalf("%d files, rename detection %v; want %d, %v", snap.FileCount(), snap.RenameDetection(), n, want)
					}
				}
				check()
				b.ReportAllocs()
				for b.Loop() {
					check()
				}
			})
		}
	}
}
