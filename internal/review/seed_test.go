package review_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// TestParseSeedRejectsMalformedDocuments checks that a seed document that is unusable as a whole,
// including one over the size cap, fails to parse.
func TestParseSeedRejectsMalformedDocuments(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"not JSON":                      `comments`,
		"trailing data":                 `{"comments": []} {}`,
		"missing comments":              `{}`,
		"null comments":                 `{"comments": null}`,
		"comments not an array":         `{"comments": {}}`,
		"unknown top-level member":      `{"comments": [], "version": 1}`,
		"duplicate top-level member":    `{"comments": [], "comments": []}`,
		"duplicate comment member":      `{"comments": [{"target": "review", "body": "x", "body": "y"}]}`,
		"invalid UTF-8 in the document": "{\"comments\": [{\"target\": \"review\", \"body\": \"\xff\"}]}",
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			seed, err := review.ParseSeed(strings.NewReader(doc))
			if _, ok := errors.AsType[*review.SeedError](err); !ok {
				t.Fatalf("ParseSeed = %v, %v; want a *SeedError", seed, err)
			}
		})
	}
}

// TestStartRejectsMalformedSeedComments checks that Start reports a malformed or over-limit
// comment, naming it.
func TestStartRejectsMalformedSeedComments(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	tests := map[string]string{
		"unknown comment member":          `{"target": "review", "body": "x", "author": "agent"}`,
		"line is a string":                `{"target": "line", "path": "a.go", "side": "new", "line": "3", "body": "x"}`,
		"body is a number":                `{"target": "review", "body": 3}`,
		"comment not an object":           `"review"`,
		"missing body":                    `{"target": "review"}`,
		"null body":                       `{"target": "review", "body": null}`,
		"missing target":                  `{"body": "x"}`,
		"unknown target":                  `{"target": "hunk", "path": "a.go", "body": "x"}`,
		"review with path":                `{"target": "review", "path": "a.go", "body": "x"}`,
		"file without path":               `{"target": "file", "body": "x"}`,
		"file with side":                  `{"target": "file", "path": "a.go", "side": "new", "body": "x"}`,
		"file with line":                  `{"target": "file", "path": "a.go", "line": 1, "body": "x"}`,
		"line without path":               `{"target": "line", "side": "new", "line": 1, "body": "x"}`,
		"line without side":               `{"target": "line", "path": "a.go", "line": 1, "body": "x"}`,
		"line with unknown side":          `{"target": "line", "path": "a.go", "side": "both", "line": 1, "body": "x"}`,
		"line zero":                       `{"target": "line", "path": "a.go", "side": "new", "line": 0, "body": "x"}`,
		"negative line":                   `{"target": "line", "path": "a.go", "side": "new", "line": -1, "body": "x"}`,
		"line with start and end":         `{"target": "line", "path": "a.go", "side": "new", "line": 1, "start": 1, "end": 2, "body": "x"}`,
		"range with line":                 `{"target": "range", "path": "a.go", "side": "new", "line": 1, "start": 1, "end": 2, "body": "x"}`,
		"range start equals end":          `{"target": "range", "path": "a.go", "side": "new", "start": 3, "end": 3, "body": "x"}`,
		"range start after end":           `{"target": "range", "path": "a.go", "side": "new", "start": 4, "end": 3, "body": "x"}`,
		"range without end":               `{"target": "range", "path": "a.go", "side": "new", "start": 3, "body": "x"}`,
		"empty body":                      `{"target": "review", "body": ""}`,
		"whitespace-only body":            `{"target": "review", "body": " \t\r\n "}`,
		"body over 64 KiB":                reviewComment(strings.Repeat("x", review.MaxBodyBytes+1)),
		"body over 64 KiB with a newline": reviewComment(strings.Repeat("x", review.MaxBodyBytes) + `\n`),
	}
	for name, comment := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := startSeed(t, fx, seedOf(reviewComment("fine"), comment))
			assertSeedProblems(t, err, 2)
		})
	}
}

func TestStartAcceptsEveryTargetKind(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, err := startSeed(t, fx, `{"comments": [
  {"target": "review", "body": "overall"},
  {"target": "file", "path": "a.go", "body": "file"},
  {"target": "line", "path": "a.go", "side": "new", "line": 2, "body": "line"},
  {"target": "range", "path": "a.go", "side": "old", "start": 3, "end": 9, "body": "range"}
]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []seeded{
		{reviewTarget, "overall"},
		{file(pathA), "file"},
		{span(pathA, review.Old, 3, 9), "range"},
		{line(pathA, review.New, 2), "line"},
	}
	if got := agentComments(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("seeded comments =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseSeedAcceptsAnEmptySeed(t *testing.T) {
	t.Parallel()
	if got := parseSeed(t, `{"comments": []}`); got.Len() != 0 {
		t.Fatalf("ParseSeed has %d comments; want none", got.Len())
	}
	fx := newFixture(t)
	s, err := startSeed(t, fx, `{"comments": []}`)
	if err != nil {
		t.Fatalf("Start with an empty seed: %v", err)
	}
	if n := len(s.Draft().Comments); n != 0 {
		t.Fatalf("an empty seed added %d comments", n)
	}
}

func TestParseSeedSizeLimit(t *testing.T) {
	t.Parallel()
	base := `{"comments": []}`
	atLimit := base + strings.Repeat(" ", review.MaxSeedBytes-len(base))
	if _, err := review.ParseSeed(strings.NewReader(atLimit)); err != nil {
		t.Fatalf("a seed of exactly %d bytes: %v", review.MaxSeedBytes, err)
	}
	_, err := review.ParseSeed(strings.NewReader(atLimit + " "))
	if _, ok := errors.AsType[*review.SeedError](err); !ok {
		t.Fatalf("a seed of %d bytes: err = %v; want a *SeedError", review.MaxSeedBytes+1, err)
	}
}

func TestStartSeedBodyLimitBoundary(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	body := strings.Repeat("x", review.MaxBodyBytes)
	s, err := startSeed(t, fx, seedOf(reviewComment(body)))
	if err != nil {
		t.Fatalf("a %d-byte body: %v", review.MaxBodyBytes, err)
	}
	if got := agentComments(t, s); len(got) != 1 || got[0].body != body {
		t.Fatalf("a %d-byte body was not kept intact", review.MaxBodyBytes)
	}
}

// TestStartNormalisesSeedLineEndings checks that seed bodies are stored with LF line endings.
func TestStartNormalisesSeedLineEndings(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, err := startSeed(t, fx, seedOf(reviewComment(`one\r\ntwo\rthree\n`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := agentComments(t, s); len(got) != 1 || got[0].body != "one\ntwo\nthree\n" {
		t.Fatalf("seeded = %+v; want body %q", got, "one\ntwo\nthree\n")
	}
}

// TestStartNamesEachMalformedSeedComment checks that the error names each rejected comment.
func TestStartNamesEachMalformedSeedComment(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	_, err := startSeed(t, fx, seedOf(
		reviewComment("fine"),
		`{"target": "review", "body": "  "}`,
		reviewComment("fine"),
		`{"target": "range", "path": "a.go", "side": "new", "start": 2, "end": 2, "body": "x"}`,
	))
	assertSeedProblems(t, err, 2, 4)
}

// TestStartValidatesSeedTargets checks seed targets against the snapshot when the seed is applied.
func TestStartValidatesSeedTargets(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	tests := []struct {
		name   string
		target review.Target
		valid  bool
	}{
		{"review", reviewTarget, true},
		{"file", file(pathA), true},
		{"first line", line(pathA, review.New, 1), true},
		{"last line", line(pathA, review.New, 10), true},
		{"line beyond the file", line(pathA, review.New, 11), false},
		{"line beyond the old side", line(pathA, review.Old, 11), false},
		{"whole old side", span(pathA, review.Old, 1, 10), true},
		{"range ending beyond the file", span(pathA, review.New, 9, 11), false},
		{"unchanged line outside the hunks", line(pathA, review.New, 8), true},
		{"another file's changed line", line(pathB, review.New, 4), true},
		{"old side of a modified file", span(pathB, review.Old, 3, 10), true},
		{"path that did not change", file("unchanged.go"), false},
		{"line on a path that did not change", line("unchanged.go", review.New, 1), false},
		{"added file, new side", line(pathAdded, review.New, 3), true},
		{"added file, old side", line(pathAdded, review.Old, 1), false},
		{"deleted file, old side", line(pathDeleted, review.Old, 3), true},
		{"deleted file, new side", line(pathDeleted, review.New, 1), false},
		{"deleted file comment by old path", file(pathDeleted), true},
		{"renamed file, old side by old path", line(pathRenOld, review.Old, 1), true},
		{"renamed file, new side by new path", line(pathRenNew, review.New, 1), true},
		{"renamed file, old side by new path", line(pathRenNew, review.Old, 1), false},
		{"renamed file, new side by old path", line(pathRenOld, review.New, 1), false},
		{"renamed file comment by new path", file(pathRenNew), true},
		{"renamed file comment by old path", file(pathRenOld), false},
		{"binary file comment", file(pathBinary), true},
		{"binary file line", line(pathBinary, review.New, 1), false},
		{"path with a control character, file", file(pathTab), false},
		{"path with a control character, line", line(pathTab, review.New, 1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			st := openStore(t, dir, fx.key(fx.last))
			s, _, err := review.Start(t.Context(), st, fx.last, review.Fresh{Seed: review.NewSeed(
				review.SeedComment{Target: reviewTarget, Body: "valid"},
				review.SeedComment{Target: tt.target, Body: "seeded"},
			)})
			if !tt.valid {
				assertSeedProblems(t, err, 2)
				if fileExists(t, st.Path()) {
					t.Fatal("a rejected seed wrote a draft")
				}
				return
			}
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			got := s.Draft().Comments
			if len(got) != 2 || !slices.ContainsFunc(got, func(c review.Comment) bool {
				return c.Target == tt.target && c.Body == "seeded" && c.Author == review.Agent
			}) {
				t.Fatalf("comments = %+v; want the seeded comment by agent", got)
			}
		})
	}
}

func TestStartNamesEveryRejectedSeedComment(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	st := openStore(t, t.TempDir(), fx.key(fx.last))
	_, _, err := review.Start(t.Context(), st, fx.last, review.Fresh{Seed: review.NewSeed(
		review.SeedComment{Target: line(pathA, review.New, 99), Body: "x"},
		review.SeedComment{Target: reviewTarget, Body: "x"},
		review.SeedComment{Target: file("missing.go"), Body: "x"},
	)})
	assertSeedProblems(t, err, 1, 3)
}

// Start validates the seed as a whole: shape problems and target problems are reported together.
func TestSeedReportsShapeAndTargetProblemsTogether(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	doc := seedOf(
		`{"target": "line", "path": "a.go", "side": "new", "line": 99, "body": "beyond the file"}`,
		`{"target": "review", "body": " "}`,
	)
	_, err := startSeed(t, fx, doc)
	assertSeedProblems(t, err, 1, 2)
}

// Comment limits through the seed: each limit's boundary is accepted and one more is rejected.
func TestStartSeedCommentCountLimit(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	comments := reviewComments(review.MaxComments, "x")
	seed := review.NewSeed(comments...)

	s, _ := fx.start(t, t.TempDir(), seed)
	if n := len(s.Draft().Comments); n != review.MaxComments {
		t.Fatalf("seeded %d comments; want %d", n, review.MaxComments)
	}

	st := openStore(t, t.TempDir(), fx.key(fx.last))
	_, _, err := review.Start(t.Context(), st, fx.last, review.Fresh{Seed: review.NewSeed(append(comments, review.SeedComment{Target: reviewTarget, Body: "x"})...)})
	if _, ok := errors.AsType[*review.SeedError](err); !ok {
		t.Fatalf("Start with %d comments: err = %v; want a *SeedError", review.MaxComments+1, err)
	}
	if fileExists(t, st.Path()) {
		t.Fatal("a rejected seed wrote a draft")
	}
}

func TestStartSeedTotalBytesLimit(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	body := strings.Repeat("x", review.MaxBodyBytes)
	comments := reviewComments(review.MaxTotalBytes/review.MaxBodyBytes, body)
	seed := review.NewSeed(comments...)

	fx.start(t, t.TempDir(), seed)

	st := openStore(t, t.TempDir(), fx.key(fx.last))
	_, _, err := review.Start(t.Context(), st, fx.last, review.Fresh{Seed: review.NewSeed(append(comments, review.SeedComment{Target: reviewTarget, Body: "x"})...)})
	if _, ok := errors.AsType[*review.SeedError](err); !ok {
		t.Fatalf("Start with bodies over %d bytes: err = %v; want a *SeedError", review.MaxTotalBytes, err)
	}
}

// The seed is validated before the draft is loaded, so an invalid seed fails even when a draft
// exists.
func TestStartRejectsInvalidSeedWhenDraftExists(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	tests := map[string]review.Seed{
		"invalid target":  review.NewSeed(review.SeedComment{Target: line(pathA, review.New, 99), Body: "beyond the file"}),
		"too many":        reviewSeed(review.MaxComments+1, "x"),
		"non-commentable": review.NewSeed(review.SeedComment{Target: file(pathTab), Body: "x"}),
		"malformed":       parseSeed(t, seedOf(`{"target": "review", "body": " "}`)),
	}
	for name, seed := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			s, st := fx.start(t, dir, review.NewSeed())
			if err := s.SetSummary("draft"); err != nil {
				t.Fatal(err)
			}
			s.End()
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}

			draft, err := os.ReadFile(st.Path())
			if err != nil {
				t.Fatal(err)
			}
			st = openStore(t, dir, fx.key(fx.last))
			_, _, err = review.Start(t.Context(), st, fx.last, review.Fresh{Seed: seed})
			if _, ok := errors.AsType[*review.SeedError](err); !ok {
				t.Errorf("Start with a draft and an invalid seed: err = %v; want a *SeedError", err)
			}
			assertDraftUntouched(t, st, draft)
		})
	}
}

func reviewComment(body string) string {
	return `{"target": "review", "body": "` + body + `"}`
}

func seedOf(comments ...string) string {
	return `{"comments": [` + strings.Join(comments, ",\n") + `]}`
}

func reviewSeed(n int, body string) review.Seed {
	return review.NewSeed(reviewComments(n, body)...)
}

func reviewComments(n int, body string) []review.SeedComment {
	seed := make([]review.SeedComment, n)
	for i := range seed {
		seed[i] = review.SeedComment{Target: reviewTarget, Body: body}
	}
	return seed
}

// startSeed parses doc, which must be a usable document, and starts a session with it.
func startSeed(t *testing.T, fx *fixture, doc string) (*review.Session, error) {
	t.Helper()
	seed := parseSeed(t, doc)
	st := openStore(t, t.TempDir(), fx.key(fx.last))
	s, _, err := review.Start(t.Context(), st, fx.last, review.Fresh{Seed: seed})
	if err != nil {
		return nil, err
	}
	return s, nil
}

type seeded struct {
	target review.Target
	body   string
}

// agentComments returns the session's comments in result order, requiring each to be by agent.
func agentComments(t *testing.T, s *review.Session) []seeded {
	t.Helper()
	var out []seeded
	for _, c := range s.Draft().Comments {
		if c.Author != review.Agent {
			t.Errorf("comment %s author = %s; want agent", c.ID, c.Author)
		}
		out = append(out, seeded{c.Target, c.Body})
	}
	return out
}

// assertSeedProblems checks that err is a *SeedError naming exactly the given 1-based comments.
func assertSeedProblems(t *testing.T, err error, comments ...int) {
	t.Helper()
	se, ok := errors.AsType[*review.SeedError](err)
	if !ok {
		t.Fatalf("err = %v; want a *SeedError", err)
	}
	if len(se.Problems) != len(comments) {
		t.Fatalf("problems = %q; want one for each of comments %v", se.Problems, comments)
	}
	for i, n := range comments {
		if name := fmt.Sprintf("comment %d:", n); !strings.HasPrefix(se.Problems[i], name) {
			t.Errorf("problem %d = %q; want it to name %q", i, se.Problems[i], name)
		}
	}
}

// TestStartSeedOnAFileNamedLikeARenamedDirectory: a seed comment validates against the file's diff
// as the file list pairs it. foo/x moves unchanged to bar and a file foo with almost its content
// is added; a diff restricted to foo would pair foo/x with foo.
func TestStartSeedOnAFileNamedLikeARenamedDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	content := numbered("line", 200)
	if err := os.Mkdir(filepath.Join(dir, "foo"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "foo/x", content)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")
	git(t, dir, "mv", "foo/x", "bar")
	if err := os.RemoveAll(filepath.Join(dir, "foo")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "foo", slices.Concat(content[:99], []string{"line one hundred"}, content[100:]))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "head")
	repo, err := source.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	snap := resolve(t, repo, source.Last())
	st := openStore(t, t.TempDir(), review.DraftKey(snap))
	seed := parseSeed(t, `{"comments": [{"target": "line", "path": "foo", "side": "new", "line": 100, "body": "here"}]}`)
	if _, _, err := review.Start(t.Context(), st, snap, review.Fresh{Seed: seed}); err != nil {
		t.Fatalf("Start with a seed comment on foo: %v", err)
	}
}
