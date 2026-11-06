package server_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/server"
	"github.com/jacoelho/rvw/internal/source"
)

// benchServer is a server over the last commit of a repository built once per benchmark.
type benchServer struct {
	b    *testing.B
	h    http.Handler
	snap *source.Snapshot
	sess *review.Session
}

const benchAddr = "127.0.0.1:1"

// newBenchServer commits base then head (path to content) and serves the last commit with seed.
func newBenchServer(b *testing.B, base, head map[string]string, seed string) *benchServer {
	b.Helper()
	dir := b.TempDir()
	run := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	commit := func(files map[string]string, msg string) {
		for p, data := range files {
			full := filepath.Join(dir, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
				b.Fatal(err)
			}
		}
		run("add", "-A")
		run("commit", "-q", "--allow-empty", "-m", msg)
	}
	run("init", "-q", "-b", "main")
	commit(base, "base")
	commit(head, "head")
	ctx := context.Background()
	repo, err := source.Open(ctx, dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = repo.Close() })
	snap, err := repo.Resolve(ctx, source.Last(), slog.New(slog.DiscardHandler))
	if err != nil {
		b.Fatal(err)
	}
	store, err := review.OpenStore(b.TempDir(), "bench")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	sess, _, err := review.Start(ctx, store, snap, review.Fresh{Seed: seedOf(b, seed)})
	if err != nil {
		b.Fatal(err)
	}
	srv := server.New(server.Config{
		Assets:  fstest.MapFS{"index.html": {Data: []byte(indexHTML)}},
		Session: sess, Token: testToken,
		Deliver: func(review.Result) error { return nil },
		Log:     slog.New(slog.DiscardHandler),
	})
	return &benchServer{b: b, h: srv.HandlerForTest(benchAddr), snap: snap, sess: sess}
}

// get serves one GET in-process and returns the body, failing unless the status is 200.
func (s *benchServer) get(path string) []byte {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+benchAddr+path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		s.b.Fatalf("GET %s: status %d: %s", path, rec.Code, rec.Body.Bytes())
	}
	return rec.Body.Bytes()
}

// rewrittenLine is a ~1,000-char line whose words are unique to tag and n.
func rewrittenLine(tag string, n int) string {
	var sb strings.Builder
	for k := 0; sb.Len() < 990; k++ {
		fmt.Fprintf(&sb, "%s%d_%d ", tag, n, k)
	}
	return sb.String()[:990]
}

// BenchmarkFileRewrittenBlock serves /api/file for one file whose 2,000 lines are all rewritten:
// a 2,000-line deleted block paired with a 2,000-line added block. The control rewrites one word
// per line.
func BenchmarkFileRewrittenBlock(b *testing.B) {
	for _, c := range []struct {
		name   string
		newOne func(n int) string
	}{
		{"rewrite", func(n int) string { return rewrittenLine("b", n) }},
		{"one-word", func(n int) string { return strings.Replace(rewrittenLine("a", n), "_3 ", "_3x ", 1) }},
	} {
		b.Run(c.name, func(b *testing.B) {
			var oldB, newB strings.Builder
			for n := range 2000 {
				oldB.WriteString(rewrittenLine("a", n) + "\n")
				newB.WriteString(c.newOne(n) + "\n")
			}
			s := newBenchServer(b, map[string]string{"r.txt": oldB.String()}, map[string]string{"r.txt": newB.String()}, "")
			if body := s.get("/api/file?i=0"); !strings.Contains(string(body), `"del"`) {
				b.Fatalf("no change block in %.200s", body)
			}
			b.ReportAllocs()
			for b.Loop() {
				s.get("/api/file?i=0")
			}
		})
	}
}

// goSource returns n lines of Go source with a block comment every 50 lines.
func goSource(tag string, n int) string {
	var sb strings.Builder
	sb.WriteString("package big\n")
	for k := 2; k <= n; k++ {
		switch k % 50 {
		case 0:
			sb.WriteString("/* a block comment\n")
		case 1:
			sb.WriteString("   ends here */\n")
		default:
			fmt.Fprintf(&sb, "var %s%d = fmt.Sprintf(\"%%d\", %d) // n\n", tag, k, k)
		}
	}
	return sb.String()
}

// BenchmarkContext serves a 20-line context expansion in the middle of a modified ~19,000-line Go
// file. The control has a small old side.
func BenchmarkContext(b *testing.B) {
	const lines = 19000
	head := goSource("v", lines)
	headLines := strings.SplitAfter(head, "\n")
	// The change is at the end, so lines 9000..9019 are a gap.
	changed := strings.Join(headLines[:lines-3], "") + "var changed = 1\n" + strings.Join(headLines[lines-2:], "")
	for _, c := range []struct{ name, base string }{
		{"large-old", head},
		{"small-old", goSource("v", 200)},
	} {
		b.Run(c.name, func(b *testing.B) {
			s := newBenchServer(b, map[string]string{"big.go": c.base}, map[string]string{"big.go": changed}, "")
			path := "/api/file/context?i=0&from=9000&to=9019"
			if body := s.get(path); !strings.Contains(string(body), `"highlighted":true`) {
				b.Fatalf("context not highlighted: %.300s", body)
			}
			b.ReportAllocs()
			for b.Loop() {
				s.get(path)
			}
		})
	}
}

// BenchmarkResult builds the result of 500 comments spread over 25 modified 10,000-line files:
// single lines and 20-line ranges, on both sides, mostly late in each file. The control puts the
// same comments on one file.
func BenchmarkResult(b *testing.B) {
	const files, lines, comments = 25, 10000, 500
	base, head := map[string]string{}, map[string]string{}
	for f := range files {
		p := fmt.Sprintf("f%02d.txt", f)
		var ob, nb strings.Builder
		for n := 1; n <= lines; n++ {
			fmt.Fprintf(&ob, "file %d line %d of the old side, padded to a typical source width\n", f, n)
			if n%1000 == 0 {
				fmt.Fprintf(&nb, "file %d line %d changed\n", f, n)
			} else {
				fmt.Fprintf(&nb, "file %d line %d of the old side, padded to a typical source width\n", f, n)
			}
		}
		base[p], head[p] = ob.String(), nb.String()
	}
	for _, c := range []struct {
		name   string
		fileOf func(k int) int
	}{
		{"spread", func(k int) int { return k % files }},
		{"one-file", func(int) int { return 0 }},
	} {
		b.Run(c.name, func(b *testing.B) {
			seed := make([]seedComment, comments)
			for k := range seed {
				side := "new"
				if k%2 == 1 {
					side = "old"
				}
				start := lines - 40 - (k*37)%(lines/2)
				t := targetDTO{Target: "line", Path: fmt.Sprintf("f%02d.txt", c.fileOf(k)), Side: side, Line: start}
				if k%3 == 0 {
					t = targetDTO{Target: "range", Path: t.Path, Side: side, Start: start, End: start + 19}
				}
				seed[k] = seedComment{targetDTO: t, Body: fmt.Sprintf("comment %d", k)}
			}
			s := newBenchServer(b, base, head, seedDoc(b, seed...))
			res, err := s.sess.Result(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			if len(res.Comments) != comments || len(res.Comments[0].Code) == 0 {
				b.Fatalf("result has %d comments", len(res.Comments))
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.sess.Result(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSearch serves a content search over 25 modified 10,000-line files whose new sides hold
// the needle on every thousandth line, and a search that matches nothing.
func BenchmarkSearch(b *testing.B) {
	const files, lines = 25, 10000
	base, head := map[string]string{}, map[string]string{}
	for f := range files {
		p := fmt.Sprintf("f%02d.txt", f)
		var ob, nb strings.Builder
		for n := 1; n <= lines; n++ {
			fmt.Fprintf(&ob, "file %d line %d of the old side, padded to a typical source width\n", f, n)
			if n%1000 == 0 {
				fmt.Fprintf(&nb, "file %d line %d changed\n", f, n)
			} else {
				fmt.Fprintf(&nb, "file %d line %d of the old side, padded to a typical source width\n", f, n)
			}
		}
		base[p], head[p] = ob.String(), nb.String()
	}
	s := newBenchServer(b, base, head, "")
	for _, c := range []struct{ name, q, want string }{
		{"matches", "changed", `"line":10000`},
		{"no-match", "absent%20text", `"matches":[]`},
	} {
		b.Run(c.name, func(b *testing.B) {
			path := "/api/search?q=" + c.q
			if body := s.get(path); !strings.Contains(string(body), c.want) {
				b.Fatalf("search reply lacks %s: %.300s", c.want, body)
			}
			b.ReportAllocs()
			for b.Loop() {
				s.get(path)
			}
		})
	}
}
