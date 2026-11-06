package highlight_test

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/highlight"
)

// TestLanguage checks that Go, TypeScript, shell and Makefile source are highlighted and nothing
// else is.
func TestLanguage(t *testing.T) {
	t.Parallel()
	highlighted := []string{
		"main.go", "pkg/sub/file_test.go",
		"web/src/app.ts", "web/src/view.tsx",
		"script.sh", "dir/run.bash",
		"Makefile", "sub/Makefile", "GNUmakefile", "rules.mk",
	}
	plain := []string{
		"app.js", "tool.py", "README.md", "go.mod", "go.sum", "data.json", "Dockerfile",
		"Makefile.bak", "noext", "dir.go/file.txt", "", ".go.swp",
	}
	for _, p := range highlighted {
		if highlight.LanguageOf(p) == (highlight.Language{}) {
			t.Errorf("Language(%q) is empty; want highlighted", p)
		}
	}
	for _, p := range plain {
		if lang := highlight.LanguageOf(p); lang != (highlight.Language{}) {
			t.Errorf("Language(%q) = %q; want not highlighted", p, lang)
		}
	}
}

// TestLinesUnknownLanguage checks that a file in another language has no spans and no error.
func TestLinesUnknownLanguage(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"x.py", "README.md", "noext"} {
		lines, err := highlight.Lines(t.Context(), highlight.LanguageOf(p), "def f():\n    return 1\n")
		if err != nil || lines != nil {
			t.Errorf("Lines(%q) = %v, %v; want nil, nil", p, lines, err)
		}
	}
}

var corpus = map[string]string{
	"main.go":    "package main\n\nimport \"fmt\"\n\n// Doc comment\n/* block\n   comment */\nfunc main() {\n\ts := `raw\nstring\n\twith tab`\n\tfmt.Println(s, 42, 'x', \"a\\\"b\")\n}\n",
	"crlf.go":    "package main\r\n\r\nfunc f() int {\r\n\treturn 1 // trailing\r\n}\r\n",
	"noeol.go":   "package main\n\nvar x = `a\nb`",
	"lone-cr.go": "package main\rvar x = 1\n",
	"app.ts":     "const a: number = 1;\nfunction f(x: string): string {\n  return `tmpl ${x}\nline`;\n}\n/* multi\nline */\nexport { f };\n",
	"view.tsx":   "export const V = () => <div className=\"x\">{1}</div>;\n",
	"run.sh":     "#!/bin/sh\nset -eu\ncat <<EOF\nheredoc $HOME\nEOF\nfor i in 1 2; do echo \"$i\"; done\n",
	"Makefile":   "all: build\n\nbuild:\n\tgo build ./... # comment\n\nVAR := $(shell echo hi)\n",
	"empty.go":   "",
	"newline.go": "\n",
}

// TestLinesCoverText checks that for every highlighted file, each line's spans concatenate to
// exactly that line's text (split on LF, keeping CR and tabs), with one span list per line.
// Input is valid UTF-8: callers replace invalid bytes first, as Chroma decodes text as runes.
func TestLinesCoverText(t *testing.T) {
	t.Parallel()
	for name, text := range corpus {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lines, err := highlight.Lines(t.Context(), highlight.LanguageOf(name), text)
			if err != nil {
				t.Fatalf("Lines: %v", err)
			}
			want := strings.Split(text, "\n")
			if len(lines) != len(want) {
				t.Errorf("%d span lines, want %d", len(lines), len(want))
			}
			for i := range min(len(lines), len(want)) {
				var got strings.Builder
				for k, s := range lines[i] {
					if s.Text == "" {
						t.Errorf("line %d: empty span %+v", i+1, s)
					}
					// Adjacent spans of one class are merged, so the wire carries one span per run.
					if k > 0 && lines[i][k-1].Class == s.Class {
						t.Errorf("line %d: spans %d and %d share class %q", i+1, k-1, k, s.Class)
					}
					got.WriteString(s.Text)
				}
				if got.String() != want[i] {
					t.Errorf("line %d spans = %q, want %q", i+1, got.String(), want[i])
				}
			}
		})
	}
}

// TestLinesWholeFileContext checks that tokenizing the whole file classifies lines inside
// a multi-line raw string as string and lines inside a block comment as comment.
func TestLinesWholeFileContext(t *testing.T) {
	t.Parallel()
	text := "package main\n\nvar s = `first\nfunc notCode() { return 1 }\nlast`\n\n/*\nvar notCode = 2\n*/\n"
	lines, err := highlight.Lines(t.Context(), highlight.LanguageOf("x.go"), text)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		line  int
		class string
	}{{4, highlight.ClassString}, {8, highlight.ClassComment}} {
		spans := lines[c.line-1]
		if len(spans) == 0 {
			t.Errorf("line %d has no spans", c.line)
		}
		for _, s := range spans {
			if s.Class != c.class {
				t.Errorf("line %d span %+v, want class %q", c.line, s, c.class)
			}
		}
	}
	if !hasClass(lines[0], highlight.ClassKeyword) {
		t.Errorf("line 1 %+v has no keyword span", lines[0])
	}
}

func hasClass(spans []highlight.Span, class string) bool {
	for _, s := range spans {
		if s.Class == class {
			return true
		}
	}
	return false
}

// TestLinesCancelled covers the all-or-nothing rule: when ctx has ended, a large file
// yields an error and no spans.
func TestLinesCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	text := "package main\n" + strings.Repeat("var x = f(1, 2, 3) + g(\"s\")\n", 2000)
	lines, err := highlight.Lines(ctx, highlight.LanguageOf("big.go"), text)
	if err == nil || lines != nil {
		t.Errorf("Lines with a cancelled context = %d lines, %v; want nil and an error", len(lines), err)
	}
}

// TestLinesLinearInLineLength covers the bounded cost of highlighting: merging the spans of one
// long line allocates in proportion to its length. Allocation, unlike time, does not depend on
// machine load; a quadratic merge makes the ratio for a 4x longer line far exceed 4. Not
// parallel, so no other test allocates while TotalAlloc is read.
func TestLinesLinearInLineLength(t *testing.T) { //nolint:paralleltest // measures process-wide allocation
	alloc := func(n int) uint64 {
		text := "var x = " + strings.Repeat("a.", n/2) + "a\n"
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		lines, err := highlight.Lines(t.Context(), highlight.LanguageOf("a.go"), text)
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatalf("Lines: %v", err)
		}
		if len(lines) != 2 {
			t.Fatalf("%d lines, want 2", len(lines))
		}
		return after.TotalAlloc - before.TotalAlloc
	}
	short, long := alloc(10_000), alloc(40_000)
	if ratio := float64(long) / float64(short); ratio > 8 {
		t.Errorf("allocation grew %.1fx for a 4x longer line (%d to %d bytes), want at most 8x", ratio, short, long)
	}
}
