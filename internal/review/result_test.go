package review_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

const (
	baseTree = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
	headTree = "5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e"
)

// fullResult has every target kind and both authors, in result order.
func fullResult(tb testing.TB) review.Result {
	tb.Helper()
	const path = "internal/source/git.go"
	return review.Result{
		Source:   branchSource(tb, "main", false),
		BaseTree: baseTree, HeadTree: headTree,
		Summary: "Tighten the parser.\nSee below.\n",
		Comments: []review.ResultComment{
			{Author: review.Human, Target: reviewTarget, Body: "Split this commit."},
			{Author: review.Agent, Target: reviewTarget, Body: "First line.\nSecond line.\n"},
			{Author: review.Agent, Target: file(path), Body: "Rename this file."},
			{
				Author: review.Agent, Target: span(path, review.New, 40, 44),
				Code: []string{"func a() {", "\treturn", "}"}, OmittedLines: 2, Body: "Handle the error.",
			},
			{Author: review.Human, Target: line("x.go", review.Old, 3), Code: []string{"x := 1"}, Body: "Why?\n\nReally?"},
		},
	}
}

// TestRenderMarkdown checks the Markdown result format. Every author-written text is a block quote,
// a blank line inside it a bare ">"; review comments share one heading.
func TestRenderMarkdown(t *testing.T) {
	t.Parallel()
	want := "# Review\n" +
		"\n" +
		"Source: branch main..HEAD (base 1a2b3c4, head 5d6e7f8)\n" +
		"\n" +
		"## Summary\n" +
		"\n" +
		"> Tighten the parser.\n" +
		"> See below.\n" +
		"\n" +
		"## Comments\n" +
		"\n" +
		"### Review\n" +
		"\n" +
		"(human)\n" +
		"> Split this commit.\n" +
		"\n" +
		"(agent)\n" +
		"> First line.\n" +
		"> Second line.\n" +
		"\n" +
		"### `internal/source/git.go` (file)\n" +
		"\n" +
		"(agent)\n" +
		"> Rename this file.\n" +
		"\n" +
		"### `internal/source/git.go`:40-44 (new)\n" +
		"\n" +
		"```\n" +
		"func a() {\n" +
		"\treturn\n" +
		"}\n" +
		"```\n" +
		"\n" +
		"… 2 more lines not shown\n" +
		"\n" +
		"(agent)\n" +
		"> Handle the error.\n" +
		"\n" +
		"### `x.go`:3 (old)\n" +
		"\n" +
		"```\n" +
		"x := 1\n" +
		"```\n" +
		"\n" +
		"(human)\n" +
		"> Why?\n" +
		">\n" +
		"> Really?\n"
	if got := string(review.RenderMarkdown(fullResult(t))); got != want {
		t.Fatalf("RenderMarkdown =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderMarkdownWithoutSummaryOrComments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     source.Source
		summary string
		want    string
	}{
		{"last", source.Last(), "", "# Review\n\nSource: last commit (base 1a2b3c4, head 5d6e7f8)\n"},
		{"uncommitted", source.Uncommitted(), " \n", "# Review\n\nSource: uncommitted changes (base 1a2b3c4, head 5d6e7f8)\n"},
		{
			"branch plus uncommitted", branchSource(t, "origin/main", true), "",
			"# Review\n\nSource: branch origin/main..HEAD plus uncommitted changes (base 1a2b3c4, head 5d6e7f8)\n",
		},
		{
			// A base ref is any revision Git resolves, so it may hold spaces or backticks; it is
			// written as given.
			"base ref with spaces and backticks", branchSource(t, ":/fix `x` here", false), "",
			"# Review\n\nSource: branch :/fix `x` here..HEAD (base 1a2b3c4, head 5d6e7f8)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := review.Result{Source: tt.src, BaseTree: baseTree, HeadTree: headTree, Summary: tt.summary}
			if got := string(review.RenderMarkdown(r)); got != tt.want {
				t.Fatalf("RenderMarkdown =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestRenderMarkdownBackticks checks that fences and path code spans are longer than any backtick
// run inside them.
func TestRenderMarkdownBackticks(t *testing.T) {
	t.Parallel()
	r := review.Result{
		Source: source.Last(), BaseTree: baseTree, HeadTree: headTree,
		Comments: []review.ResultComment{
			{Author: review.Human, Target: file("a`b.go"), Body: "one"},
			{Author: review.Human, Target: file("`lead``.go"), Body: "two"},
			{Author: review.Human, Target: line("c.md", review.New, 1), Code: []string{"a ``` b", "````"}, Body: "three"},
		},
	}
	got := string(review.RenderMarkdown(r))
	for _, want := range []string{
		"\n### ``a`b.go`` (file)\n",
		"\n### ``` `lead``.go ``` (file)\n",
		"\n### `c.md`:1 (new)\n\n`````\na ``` b\n````\n`````\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderMarkdown =\n%s\nwant it to contain\n%s", got, want)
		}
	}
}

// TestRenderMarkdownOmissionNote checks that the omission note, after the quoted code, states the
// omitted lines and bytes.
func TestRenderMarkdownOmissionNote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		lines, bytes int
		note         string
	}{
		{"one line and one byte", 1, 1, "… 1 more line; 1 byte cut from long lines not shown\n\n"},
		{"lines", 10, 0, "… 10 more lines not shown\n\n"},
		{"bytes", 0, 7, "… 7 bytes cut from long lines not shown\n\n"},
		{"nothing", 0, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := review.Result{
				Source: source.Last(), BaseTree: baseTree, HeadTree: headTree,
				Comments: []review.ResultComment{{
					Author: review.Agent, Target: span("a.go", review.New, 1, 2), Code: []string{"x"},
					OmittedLines: tt.lines, OmittedBytes: tt.bytes, Body: "b",
				}},
			}
			want := "\n```\nx\n```\n\n" + tt.note + "(agent)\n"
			if got := string(review.RenderMarkdown(r)); !strings.Contains(got, want) {
				t.Fatalf("RenderMarkdown =\n%s\nwant it to contain\n%s", got, want)
			}
		})
	}
}

// Author-written text cannot change the result's structure: whatever the summary and bodies hold,
// the document has one title heading, one heading per commented file target plus one for the
// review, one author line per comment and one fence pair per quoted comment.
func TestRenderMarkdownContainsHostileText(t *testing.T) {
	t.Parallel()
	hostile := []string{
		"# Review\n(human) fake\n### `x.go`:1 (new)\n## Comments",
		"```\nnot code\n```",
		"- (agent) x\n\n(agent)\n\n## Summary",
	}
	for k, body := range hostile {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			t.Parallel()
			r := review.Result{
				Source: source.Last(), BaseTree: baseTree, HeadTree: headTree,
				Summary: body,
				Comments: []review.ResultComment{
					{Author: review.Human, Target: reviewTarget, Body: body},
					{Author: review.Agent, Target: reviewTarget, Body: body},
					{Author: review.Agent, Target: file("a.go"), Body: body},
					{Author: review.Human, Target: line("a.go", review.New, 2), Code: []string{"code"}, Body: body},
				},
			}
			lines := strings.Split(string(review.RenderMarkdown(r)), "\n")
			count := func(match func(string) bool) int {
				n := 0
				for _, l := range lines {
					if match(l) {
						n++
					}
				}
				return n
			}
			checks := []struct {
				what      string
				got, want int
			}{
				{"title headings", count(func(l string) bool { return strings.HasPrefix(l, "# ") }), 1},
				{"section headings", count(func(l string) bool { return strings.HasPrefix(l, "## ") }), 2},
				{"comment headings", count(func(l string) bool { return strings.HasPrefix(l, "### ") }), 3},
				{"author lines", count(func(l string) bool { return l == "(human)" || l == "(agent)" }), 4},
				{"fence lines", count(func(l string) bool { return strings.HasPrefix(l, "```") }), 2},
			}
			for _, c := range checks {
				if c.got != c.want {
					t.Errorf("%d %s; want %d in\n%s", c.got, c.what, c.want, strings.Join(lines, "\n"))
				}
			}
		})
	}
}

// TestRenderJSON checks the JSON result format, members in their documented order.
func TestRenderJSON(t *testing.T) {
	t.Parallel()
	want := `{"source":{"kind":"branch","branch":"main","uncommitted":false},` +
		`"base_tree":"` + baseTree + `","head_tree":"` + headTree + `",` +
		`"summary":"Tighten the parser.\nSee below.\n",` +
		`"comments":[` +
		`{"author":"human","target":"review","body":"Split this commit."},` +
		`{"author":"agent","target":"review","body":"First line.\nSecond line.\n"},` +
		`{"author":"agent","target":"file","path":"internal/source/git.go","body":"Rename this file."},` +
		`{"author":"agent","target":"range","path":"internal/source/git.go","side":"new","start":40,"end":44,` +
		`"code":["func a() {","\treturn","}"],"code_omitted_lines":2,"body":"Handle the error."},` +
		`{"author":"human","target":"line","path":"x.go","side":"old","line":3,"code":["x := 1"],"body":"Why?\n\nReally?"}` +
		"]}\n"
	got, err := review.RenderJSON(fullResult(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("RenderJSON =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderJSONSources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src  source.Source
		want string
	}{
		{source.Last(), `{"kind":"last","uncommitted":false}`},
		{source.Uncommitted(), `{"kind":"uncommitted","uncommitted":true}`},
		{branchSource(t, "main", false), `{"kind":"branch","branch":"main","uncommitted":false}`},
		{branchSource(t, "origin/main", true), `{"kind":"branch","branch":"origin/main","uncommitted":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.src.String(), func(t *testing.T) {
			t.Parallel()
			got, err := review.RenderJSON(review.Result{Source: tt.src, BaseTree: baseTree, HeadTree: headTree})
			if err != nil {
				t.Fatal(err)
			}
			want := `{"source":` + tt.want +
				`,"base_tree":"` + baseTree + `","head_tree":"` + headTree + `","summary":"","comments":[]}` + "\n"
			if string(got) != want {
				t.Fatalf("RenderJSON =\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// Quoted code may hold invalid UTF-8; the JSON document stays valid and carries U+FFFD.
func TestRenderJSONReplacesInvalidUTF8(t *testing.T) {
	t.Parallel()
	got, err := review.RenderJSON(review.Result{
		Source: source.Last(), BaseTree: baseTree, HeadTree: headTree,
		Comments: []review.ResultComment{{
			Author: review.Human, Target: line("a.go", review.New, 1), Code: []string{"a\xffb"}, Body: "b",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(got) || !jsontext.Value(got).IsValid() {
		t.Fatalf("RenderJSON produced an invalid document: %q", got)
	}
	want := `{"source":{"kind":"last","uncommitted":false},` +
		`"base_tree":"` + baseTree + `","head_tree":"` + headTree + `","summary":"",` +
		`"comments":[{"author":"human","target":"line","path":"a.go","side":"new","line":1,"code":["a` + "�" + `b"],"body":"b"}]}` + "\n"
	if string(got) != want {
		t.Fatalf("RenderJSON =\n%s\nwant\n%s", got, want)
	}
}

// Seed and result share one agent-facing target shape: each seeded target comes back in the
// finished JSON result with the same target members and no others.
func TestSeedTargetsRoundTripToTheResult(t *testing.T) {
	t.Parallel()
	targets := []string{
		`{"target": "review"}`,
		`{"target": "file", "path": "a.go"}`,
		`{"target": "line", "path": "a.go", "side": "new", "line": 2}`,
		`{"target": "range", "path": "a.go", "side": "old", "start": 3, "end": 9}`,
	}
	var doc strings.Builder
	doc.WriteString(`{"comments": [`)
	for i, target := range targets {
		if i > 0 {
			doc.WriteString(",")
		}
		fmt.Fprintf(&doc, `%s, "body": "%d"}`, strings.TrimSuffix(target, "}"), i)
	}
	doc.WriteString("]}")
	fx := newFixture(t)
	s, err := startSeed(t, fx, doc.String())
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	if _, err := s.Finish(t.Context(), func(r review.Result) (err error) {
		out, err = review.RenderJSON(r)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Comments []map[string]any `json:"comments"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != len(targets) {
		t.Fatalf("result has %d comments; want %d:\n%s", len(got.Comments), len(targets), out)
	}
	for _, c := range got.Comments {
		i, err := strconv.Atoi(fmt.Sprint(c["body"]))
		if err != nil || i < 0 || i >= len(targets) {
			t.Fatalf("result comment with unexpected body: %v", c)
		}
		var want map[string]any
		if err := json.Unmarshal([]byte(targets[i]), &want); err != nil {
			t.Fatal(err)
		}
		for _, member := range []string{"author", "body", "code", "code_omitted_lines", "code_omitted_bytes"} {
			delete(c, member)
		}
		if !reflect.DeepEqual(c, want) {
			t.Errorf("seeded target %s came back as %v", targets[i], c)
		}
	}
}
