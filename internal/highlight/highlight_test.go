package highlight_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/highlight"
)

// TestLanguage checks that the languages of REQ-HL-001 are highlighted and nothing else is.
func TestLanguage(t *testing.T) {
	t.Parallel()
	highlighted := map[string]string{
		"main.go": "go", "pkg/sub/file_test.go": "go",
		"page.tmpl": "go-template", "x.gotmpl": "go-template", "x.gohtml": "go-template",
		"app.js": "javascript", "x.mjs": "javascript", "x.cjs": "javascript", "v.jsx": "javascript",
		"web/src/app.ts": "typescript", "x.mts": "typescript", "x.cts": "typescript",
		"web/src/view.tsx": "tsx",
		"script.sh":        "shell", "dir/run.bash": "shell", "x.zsh": "shell", ".bashrc": "shell",
		"Makefile": "makefile", "sub/Makefile": "makefile", "GNUmakefile": "makefile", "rules.mk": "makefile",
		"README.md": "markdown", "docs/x.markdown": "markdown",
		"src/lib.rs": "rust",
		"main.tf":    "hcl", "prod.tfvars": "hcl", "config.hcl": "hcl",
		"data.json": "json", "x.tf.json": "json", "tsconfig.jsonc": "json",
		"ci.yaml": "yaml", ".github/workflows/x.yml": "yaml",
		"Cargo.toml": "toml",
		"tool.py":    "python", "stubs/x.pyi": "python",
		"Dockerfile": "dockerfile", "build/Dockerfile.dev": "dockerfile", "Containerfile": "dockerfile",
		"x.dockerfile": "dockerfile",
		"web/app.css":  "css", "db/schema.sql": "sql",
	}
	plain := []string{
		"tool.rb", "x.scss", "x.less", "go.mod", "go.sum", "Dockerfile-old", ".dockerignore", "x.mdx", "x.json5", "Cargo.lock",
		"Makefile.bak", "noext", "dir.go/file.txt", "", ".go.swp",
	}
	for p, want := range highlighted {
		if got := highlight.LanguageOf(p).String(); got != want {
			t.Errorf("LanguageOf(%q) = %q, want %q", p, got, want)
		}
	}
	for _, p := range plain {
		if lang := highlight.LanguageOf(p); lang != (highlight.Language{}) {
			t.Errorf("LanguageOf(%q) = %q; want not highlighted", p, lang)
		}
	}
}

// TestLinesUnknownLanguage checks that a file in another language has no spans and no error.
func TestLinesUnknownLanguage(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"x.rb", "x.scss", "noext"} {
		lines, err := highlight.Lines(t.Context(), highlight.LanguageOf(p), "def f():\n    return 1\n", nil)
		if err != nil || lines != nil {
			t.Errorf("Lines(%q) = %v, %v; want nil, nil", p, lines, err)
		}
	}
}

// corpus holds well-formed and malformed source in every language, keyed by a file name whose
// extension picks the language.
var corpus = map[string]string{
	"main.go":         "package main\n\nimport \"fmt\"\n\n// Doc comment\n/* block\n   comment */\nfunc main() {\n\ts := `raw\nstring\n\twith tab`\n\tfmt.Println(s, 42, 'x', \"a\\\"b\", 0x1F, 1.5e-3, 3i)\n}\n",
	"crlf.go":         "package main\r\n\r\nfunc f() int {\r\n\treturn 1 // trailing\r\n}\r\n",
	"noeol.go":        "package main\n\nvar x = `a\nb`",
	"lone-cr.go":      "package main\rvar x = 1\n",
	"open.go":         "var s = \"unterminated\nvar r = 'x\n/* never closed\n",
	"utf8.go":         "var café = \"naïve ☃\" // ünïcode\nfunc 日本() {}\n",
	"empty.go":        "",
	"newline.go":      "\n",
	"page.tmpl":       "<p>{{ .Title }}</p>\n{{- range $i, $e := .Items -}}\n  {{ printf \"%d\" $i | html }}\n{{ end }}\n{{/* a\ncomment */}}\n{{ if",
	"app.ts":          "const a: number = 1;\nfunction f(x: string): string {\n  return `tmpl ${x + `in ${a}`}\nline`;\n}\n/* multi\nline */\nexport { f };\nconst r = /a[/]b/g, d = a / 2 / 1;\ntype T<K> = Array<K>;\n",
	"view.tsx":        "export const V = () => <div className=\"x\" on={() => f(<b/>)}>{1} text <>frag</></div>;\nconst y = a < b;\n",
	"app.js":          "import x from 'y';\nclass A extends B { #p = 1; get v() { return this.#p } }\nconst t = <A.B x-y='1'>{`${1}`}</A.B>;\nlet re = /\\/+/;\n",
	"open.ts":         "const s = `never ${ {a: `closed",
	"run.sh":          "#!/bin/sh\nset -eu\ncat <<EOF\nheredoc $HOME\nEOF\nfor i in 1 2; do echo \"$i ${x:-$(pwd)}\"; done\nf() { local a=$((1+2)); }\nv=`date` # c\ncat <<-'X' <<Y\n\tbody\n\tX\ny\nY\n",
	"utf8.sh":         "\\é() { :; }\nfunction \\é\necho \\☃x\n",
	"utf8.mk":         "all:\n\t\\é() { :; }\n",
	"open.sh":         "echo \"$(cat <<E\nunclosed",
	"Makefile":        "all: build\n\nbuild: a.o $(OBJS) ; @echo done\n\tgo build ./... # comment\n\t@echo $(patsubst %.c,%.o,$(SRC)) $$HOME \"$(CC)\"\n\nVAR := $(shell echo hi)\nX ?= 1 # set\nifeq ($(X),1)\nexport Y = $X\nendif\ndefine T\n\tbody: $(X)\nendef\nlong: a \\\n\tb\n",
	"README.md":       "# Title\n\nSome `code` and ``a ` b`` and [link](http://x) <https://y>.\n\n```go\nfunc main() {}\n```\n\n- item\n1. one\n> quote <!-- note\nmore -->\n\n~~~\nplain\n~~~\n---\n````md\n```sh\necho hi\n```\n````\n```unclosed",
	"lib.rs":          "//! crate doc\n#[derive(Debug)]\npub struct P<'a> { s: &'a str }\nfn main() {\n    let c = 'x'; let e = '\\n'; let u = 'é';\n    let r = r#\"raw \"quoted\"\"#; let b = b\"bytes\";\n    println!(\"{}\", 1_000u32 + 0xffi64);\n    /* outer /* inner */ still */ x.0.1;\n}\n",
	"main.tf":         "# comment\nresource \"aws_instance\" \"web\" {\n  ami = \"ami-${var.id}-%{ if x }y%{ endif }\"\n  tags = { Name = lower(\"x\") }\n  n = 1.5\n  t = list(string)\n  d = <<-EOT\n    hello ${var.name}\n    EOT\n}\n// line\n/* block */\n",
	"open.tf":         "a = \"${b\nc = <<E\nunclosed",
	"data.json":       "{\n  // c\n  \"key\": [1, -2.5e3, true, null, \"v\\\"\"],\n  /* b */ \"k2\" : {}\n}\n",
	"open.json":       "{\"a\": \"unclosed\n/* never",
	"ci.yaml":         "%YAML 1.2\n---\n# c\nname: CI\non: [push, {a: 1}]\nanchor: &x 3\nref: *x\ntag: !!str 4\n\"q k\": 'it''s'\nlist:\n  - true\n  - ~\n  - .inf\n  - 10GB\nrun: |\n  echo \"hi\"\n\n  done: x\nnext: >-\n  folded\nurl: http://x # c\n...\n",
	"open.yaml":       "a: \"unclosed\nb: 'x\n- [ {",
	"Cargo.toml":      "# c\n[package]\nname = \"rvw\"\n\"q\".k = 'lit'\nn = +1_000\nf = -inf\nd = 1979-05-27T07:32:00Z\nb = true\nm = \"\"\"\nmulti\\\"\"\"\n\"\"\"\"\nt = { x = 1, y = [1, 2] }\narr = [\n  1,\n]\n[[bin]]\nl = '''\nraw\n'''\n",
	"open.toml":       "a = \"\"\"never\n[x\nb = 'y",
	"app.py":          "#!/usr/bin/env python3\n\"\"\"Doc\nstring.\"\"\"\n@dataclass(frozen=True)\nclass P(Base):\n    x: int = 0x1F\n\n    def f(self, n=1.5e3) -> str:\n        return f\"{n!r}\" + rb'\\'' + '''tri\nple'''  # c\nmatch p:\n    case [a, *_]:\n        print(a @ b)\ntype T = list[int]\nmatch = 1\n",
	"open.py":         "s = '''never\nt = \"x\n",
	"Dockerfile":      "# syntax=docker/dockerfile:1\nARG GO=1.27\nFROM golang:${GO} AS build\nENV A=1 \\\n    B=\"two\"\nRUN --mount=type=cache,target=/root/.cache \\\n    go build -o /out ./... && echo \"$HOME\"\nRUN <<EOF\nset -e\necho hi\nEOF\nCOPY --from=build /out /app\nEXPOSE 8080\nHEALTHCHECK --interval=5s CMD [\"curl\", \"-f\"]\nONBUILD RUN make\nentrypoint [\"/app\"]\n",
	"open.Dockerfile": "RUN echo \"unclosed\nRUN cat <<E\nnever",
	"style.css":       "@charset \"utf-8\";\n@import url(base.css) screen and (orientation: landscape);\n/* c */\n:root { --gap: 4px; }\nhtml, body > .main#app::before, a:not(.x)[href^='http'] {\n  margin: -0.5em auto 10% !important;\n  color: #fff;\n  background: url( data:image/png;base64,iVB ) no-repeat, rgb(0 0 0 / 50%);\n  font: 12px/1.5 \"Helvetica Neue\", sans-serif;\n}\n@media (min-width: 600px) {\n  .card { padding: calc(var(--gap) * 2);\n    &:hover { color: red }\n    .t { a: b } }\n}\n@keyframes spin { from { transform: rotate(0deg) } 50% { opacity: .5 } }\n",
	"open.css":        "a { content: \"unclosed\n  b: url(never\n/* never",
	"schema.sql":      "-- schema\nCREATE TABLE users (\n  id BIGSERIAL PRIMARY KEY,\n  name VARCHAR(255) NOT NULL DEFAULT 'it''s',\n  meta JSONB\n);\n/* block\n   comment */\nselect u.id, count(*) AS n, e'\\n', x'ff', \"Order\", `key`\nfrom users u where u.name ilike $1 and age > :min or id = @id and t::date > '2024-01-01'\ngroup by 1 having n >= 1.5e3;\nCREATE FUNCTION f() RETURNS int AS $body$\n  SELECT 1; -- $ inside\n$body$ LANGUAGE sql;\nDO $$ BEGIN END $$;\n",
	"front.md":        "---\ntitle: x\nlist:\n  - a\n...\nBody\n",
	"open.md":         "---\r\nkey: 'unclosed\r\n",
	"open.sql":        "SELECT 'never\n$tag$ open\n/* open",
}

// checkCover checks that each line's spans concatenate to exactly that line's text (split on
// LF, keeping CR and tabs), with one span list per line, no empty span and no two adjacent
// spans of one class. When text is valid UTF-8, no span splits a character.
func checkCover(t *testing.T, text string, lines [][]highlight.Span) {
	t.Helper()
	want := strings.Split(text, "\n")
	if len(lines) != len(want) {
		t.Errorf("%d span lines, want %d", len(lines), len(want))
	}
	valid := utf8.ValidString(text)
	for i := range min(len(lines), len(want)) {
		var got strings.Builder
		for k, s := range lines[i] {
			if s.Text == "" {
				t.Errorf("line %d: empty span %+v", i+1, s)
			}
			if k > 0 && lines[i][k-1].Class == s.Class {
				t.Errorf("line %d: spans %d and %d share class %q", i+1, k-1, k, s.Class)
			}
			if valid && !utf8.ValidString(s.Text) {
				t.Errorf("line %d: span %q splits a character", i+1, s.Text)
			}
			got.WriteString(s.Text)
		}
		if got.String() != want[i] {
			t.Errorf("line %d spans = %q, want %q", i+1, got.String(), want[i])
		}
	}
}

// TestLinesCoverText checks the coverage property for every corpus file.
func TestLinesCoverText(t *testing.T) {
	t.Parallel()
	for name, text := range corpus {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lang := highlight.LanguageOf(name)
			if lang == (highlight.Language{}) {
				t.Fatalf("%s is not highlighted", name)
			}
			lines, err := highlight.Lines(t.Context(), lang, text, nil)
			if err != nil {
				t.Fatalf("Lines: %v", err)
			}
			checkCover(t, text, lines)
		})
	}
}

// classes returns the class of each byte of text, from lines; LF bytes have class "\n".
func classes(lines [][]highlight.Span) []string {
	var out []string
	for i, line := range lines {
		if i > 0 {
			out = append(out, "\n")
		}
		for _, s := range line {
			for range len(s.Text) {
				out = append(out, s.Class)
			}
		}
	}
	return out
}

// want is one expectation: the n-th (from 0) occurrence of text in the source has class.
type want struct {
	text  string
	class string
	n     int
}

func expect(t *testing.T, name, src string, wants []want) {
	t.Helper()
	lines, err := highlight.Lines(t.Context(), highlight.LanguageOf(name), src, nil)
	if err != nil {
		t.Fatalf("%s: Lines: %v", name, err)
	}
	byByte := classes(lines)
	for _, w := range wants {
		off := -1
		for k, from := 0, 0; k <= w.n; k++ {
			j := strings.Index(src[from:], w.text)
			if j < 0 {
				t.Fatalf("%s: occurrence %d of %q not in source", name, w.n, w.text)
			}
			off = from + j
			from = off + 1
		}
		got := map[string]bool{}
		for b := off; b < off+len(w.text); b++ {
			if c := byByte[b]; c != "\n" {
				got[c] = true
			}
		}
		if len(got) != 1 || !got[w.class] {
			t.Errorf("%s: %q (occurrence %d) has classes %v, want %q", name, w.text, w.n, keys(got), w.class)
		}
	}
}

func keys(m map[string]bool) []string {
	var ks []string
	for k := range m {
		ks = append(ks, fmt.Sprintf("%q", k))
	}
	return ks
}

const (
	k = highlight.ClassKeyword
	s = highlight.ClassString
	c = highlight.ClassComment
	m = highlight.ClassNumber
	f = highlight.ClassFunction
	y = highlight.ClassType
	o = highlight.ClassOperator
	p = "" // plain
)

// TestClasses checks the class of chosen tokens in each language against the language's
// grammar: what a keyword, a string, a comment, a call or a type is.
func TestClasses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, src string
		wants     []want
	}{
		{"x.go", "package main\n\ntype T struct{}\n\nfunc (r *T) Name(n int) string {\n\tv := len(n) // c\n\treturn fmt.Sprint(v, nil, 0x1F, `raw`, 'r', \"s\")\n}\n", []want{
			{"package", k, 0}, {"T", y, 0}, {"func", k, 0}, {"r", p, 1}, {"Name", f, 0},
			{"int", y, 0}, {"string", y, 0}, {":=", o, 0}, {"len", f, 0}, {"// c", c, 0},
			{"Sprint", f, 0}, {"nil", k, 0}, {"0x1F", m, 0}, {"`raw`", s, 0}, {"'r'", s, 0}, {"\"s\"", s, 0},
		}},
		{"x.tmpl", "<b>{{ if .Ok }}{{ printf \"%d\" $n | html }}{{ end }}</b>{{/* note */}}", []want{
			{"<b>", p, 0}, {"{{", k, 0}, {"if", k, 0}, {"Ok", p, 0}, {"printf", f, 0}, {"\"%d\"", s, 0},
			{"$n", y, 0}, {"|", o, 0}, {"end", k, 0}, {"{{/* note */}}", c, 0},
		}},
		{"x.ts", "interface I { a: string }\nconst r = /x\\/y/g;\nconst d = a / b / c;\nclass C extends D { m() { return x.default(1) } }\nlet t = `a ${b} c`;\n", []want{
			{"interface", k, 0}, {"I", y, 0}, {"string", y, 0}, {"/x\\/y/g", s, 0}, {"/", o, 3},
			{"C", y, 0}, {"D", y, 0}, {"m", f, 0}, {"default", f, 0}, {"`a ", s, 0}, {"${", o, 0},
			{"b", p, 1}, {" c`", s, 0},
		}},
		{"x.ts", "const v = <T>x;\n", []want{{"T", p, 0}}},
		{"x.tsx", "const v = <List items={[1]} on=\"x\">hi {name}</List>;\nconst w = a < b;\n", []want{
			{"List", y, 0}, {"items", p, 0}, {"1", m, 0}, {"\"x\"", s, 0}, {"hi", p, 0}, {"name", p, 0},
			{"<", o, 2}, {"const", k, 1},
		}},
		{"x.js", "function g() { return /re/.test(s) }\nconst string = 1;\n", []want{
			{"g", f, 0}, {"/re/", s, 0}, {"test", f, 0}, {"string", p, 0},
		}},
		{"x.sh", "x=$((1 << 4))\nif true; then echo $(( (2) )); fi\n", []want{
			{"$((", k, 0}, {"4", m, 0}, {"))", k, 0}, {"if", k, 0}, {"then", k, 0}, {"echo", y, 0}, {"))", k, 1}, {"fi", k, 0},
		}},
		{"x.sh", "#!/bin/sh\nif [ -n \"$X\" ]; then echo \"a ${B} $(pwd)\"; fi\nN=3 # n\ncat <<EOF\nif $Y\nEOF\nfn1() { :; }\nfor i in 1 2; do :; done\necho if\n", []want{
			{"#!/bin/sh", c, 0}, {"if", k, 0}, {"\"", s, 0}, {"$X", y, 0}, {"then", k, 0}, {"echo", y, 0},
			{"${B}", y, 0}, {"$(", k, 0}, {"pwd", y, 0}, {"fi", k, 0}, {"N", y, 0}, {"3", p, 0},
			{"# n", c, 0}, {"if $Y", s, 0}, {"EOF", s, 1}, {"fn1", f, 0}, {"in", k, 1}, {"2", m, 0},
			{"if", p, 2},
		}},
		{"Makefile", "CC := gcc # c\nall: main.o $(OBJ)\n\t@$(CC) -o $@ $$HOME $(patsubst %.c,%.o,$(SRC))\nifdef X\nendif\n", []want{
			{"CC", y, 0}, {":=", o, 0}, {"# c", c, 0}, {"all", f, 0}, {"main.o", p, 0}, {"$(OBJ)", y, 0},
			{"@", o, 0}, {"$(CC)", y, 0}, {"$@", y, 0}, {"$$HOME", y, 0}, {"patsubst", f, 0}, {"$(SRC)", y, 0},
			{"ifdef", k, 0}, {"endif", k, 0},
		}},
		{"x.md", "# Head `x`\n\nText `code` [a](http://u) <https://v>\n\n```go\nfunc f() {}\n```\n\n```\nfunc g() {}\n```\n", []want{
			{"# Head `x`", k, 0}, {"Text", p, 0}, {"`code`", s, 0}, {"http://u", s, 0}, {"<https://v>", s, 0},
			{"```go", k, 0}, {"func", k, 0}, {"f", f, 1}, {"func g() {}", s, 0},
		}},
		{"x.md", "---\ntitle: \"Hi\"\ntags: [a, 1]\n---\n# Head\n\n```md\n---\nk: 2\n...\n```\n", []want{
			{"---", k, 0}, {"title", y, 0}, {"\"Hi\"", s, 0}, {"1", m, 0}, {"---", k, 1}, {"# Head", k, 0},
			{"---", k, 2}, {"k", y, 0}, {"2", m, 0}, {"...", k, 0},
		}},
		// Without a closing line, the opening --- is a thematic break and the rest is Markdown.
		{"x.md", "---\ntitle: x\n# Head\n", []want{{"---", o, 0}, {"title: x", p, 0}, {"# Head", k, 0}}},
		{"x.json", "{\"key\": [1, -2.5e3, true, null, \"v\"], \"k\" : {}} // c\n", []want{
			{"\"key\"", y, 0}, {"1", m, 0}, {"-2.5e3", m, 0}, {"true", k, 0}, {"null", k, 0},
			{"\"v\"", s, 0}, {"\"k\"", y, 0}, {"// c", c, 0}, {":", p, 0},
		}},
		{"x.yaml", "---\nname: CI # c\non: [push, {a: 1}]\nanchor: &x 3\nref: *x\ntag: !!str 4\n\"q k\": 'it''s'\n- true\n- 10GB\n- .inf\nrun: |\n  echo: \"hi\"\n\n  more\nnext: x\nurl: http://x:8\n", []want{
			{"---", k, 0}, {"name", y, 0}, {":", o, 0}, {"CI", s, 0}, {"# c", c, 0}, {"push", s, 0},
			{"a", y, 1}, {"1", m, 0}, {"&x", y, 0}, {"3", m, 0}, {"*x", y, 0}, {"!!str", k, 0}, {"4", m, 0},
			{"\"q k\"", y, 0}, {"'it''s'", s, 0}, {"-", o, 3}, {"true", k, 0}, {"10GB", s, 0}, {".inf", m, 0},
			{"|", o, 0}, {"echo: \"hi\"", s, 0}, {"more", s, 0}, {"next", y, 0}, {"x", s, 3}, {"http://x:8", s, 0},
		}},
		{"x.toml", "# c\n[package]\nname = \"rvw\"\n\"q\".k = 'lit'\nn = +1_000\nf = -inf\nd = 1979-05-27T07:32:00Z\nb = true\nm = \"\"\"\na = 1\n\"\"\"\nt = { x = 1, y = [2, 3] }\narr = [\n  4,\n]\n[[bin]]\n", []want{
			{"# c", c, 0}, {"[package]", k, 0}, {"name", y, 0}, {"=", o, 0}, {"\"rvw\"", s, 0}, {"\"q\"", y, 0},
			{"k", y, 1}, {"'lit'", s, 0}, {"+1_000", m, 0}, {"-inf", m, 0}, {"1979-05-27T07:32:00Z", m, 0},
			{"true", k, 0}, {"\"\"\"\na = 1\n\"\"\"", s, 0}, {"x", y, 0}, {"y", y, 0}, {"2", m, 0},
			{"4", m, 0}, {"[[bin]]", k, 0},
		}},
		{"x.rs", "#[derive(Debug)]\nstruct S<'a> { v: Vec<u8> }\nfn main() { let c = 'x'; println!(\"{}\", r#\"r\"#); x.len() /* a /* b */ c */ }\n", []want{
			{"#", k, 0}, {"struct", k, 0}, {"S", y, 0}, {"'a", y, 0}, {"Vec", y, 0}, {"u8", y, 0},
			{"main", f, 0}, {"'x'", s, 0}, {"println!", f, 0}, {"r#\"r\"#", s, 0}, {"len", f, 0},
			{"/* a /* b */ c */", c, 0},
		}},
		{"x.py", "@app.route(\"/\")\nclass C(Base):\n    def run(self, x=1) -> int:\n        return rb'\\d' + f\"{x}\" + '''a\nb''' # note\nmatch x:\n    case _:\n        pass\nmatch = len(x)\ntype T = int\n", []want{
			{"@app.route", f, 0}, {"\"/\"", s, 0}, {"class", k, 0}, {"C", y, 0}, {"def", k, 0}, {"run", f, 0},
			{"1", m, 0}, {"int", y, 0}, {"rb'\\d'", s, 0}, {"f\"{x}\"", s, 0}, {"'''a", s, 0}, {"b'''", s, 0},
			{"# note", c, 0}, {"match", k, 0}, {"case", k, 0}, {"pass", k, 0}, {"match", p, 1}, {"len", f, 0},
			{"type", k, 0}, {"T", y, 0},
		}},
		{"Dockerfile", "FROM golang:${GO} AS build\nENV A=1 \\\n    B=\"two\"\nRUN --mount=type=cache go build \\\n    && echo \"$HOME\" # sh\nRUN <<EOF\necho hi\nEOF\nEXPOSE 8080\nonbuild CMD [\"run\", \"-f\"]\n# c\n", []want{
			{"FROM", k, 0}, {"${GO}", y, 0}, {"AS", k, 0}, {"ENV", k, 0}, {"A", y, 1}, {"1", m, 0}, {"B", y, 0},
			{"\"two\"", s, 0}, {"RUN", k, 0}, {"--mount", y, 0}, {"echo", y, 0}, {"$HOME", y, 0}, {"# sh", c, 0},
			{"<<", o, 0}, {"echo hi", s, 0}, {"EXPOSE", k, 0}, {"8080", m, 0}, {"onbuild", k, 0}, {"CMD", k, 0},
			{"\"run\"", s, 0}, {"# c", c, 0},
		}},
		{"Dockerfile", "RUN apt-get update \\\r\n    && apt-get install -y \"foo\" $X\r\nENV A=1\r\n", []want{
			{"RUN", k, 0}, {"\"foo\"", s, 0}, {"$X", y, 0}, {"ENV", k, 0}, {"A", y, 0},
		}},
		{"Dockerfile", "RUN echo it's\nFROM alpine AS b\nRUN cat <<E $'a\nbody\nE\nCOPY a b\n", []want{
			{"'s", s, 0}, {"FROM", k, 0}, {"AS", k, 0}, {"RUN", k, 1}, {"body", s, 0}, {"COPY", k, 0},
		}},
		{"x.css", "@media screen and (min-width: 600px) {\n  ul > li.item#main:hover::after, & [data-x='y'] {\n    margin: -0.5em 10% !important;\n    color: #fff; background: url(a/b.png) rgb(0 0 0 / 50%), url(data:x;base64,AA:BB) top;\n    --gap: 4px; content: \"q\" /* c */\n  }\n  .card { padding: 2px; &:hover { color: red } }\n}\n", []want{
			{"@media", k, 0}, {"and", k, 0}, {"min-width", y, 0}, {"600px", m, 0}, {"ul", k, 0}, {">", o, 0},
			{"li", k, 0}, {".item", y, 0}, {"#main", y, 0}, {":hover", f, 0}, {"::after", f, 0}, {"&", k, 0},
			{"'y'", s, 0}, {"margin", y, 0}, {"-0.5em", m, 0}, {"10%", m, 0}, {"!important", k, 0},
			{"#fff", m, 0}, {"url", f, 0}, {"a/b.png", s, 0}, {"data:x;base64,AA:BB", s, 0}, {"top", p, 0}, {"rgb", f, 0}, {"50%", m, 0}, {"--gap", y, 0},
			{"\"q\"", s, 0}, {"/* c */", c, 0}, {".card", y, 0}, {"padding", y, 0}, {"&", k, 1},
			{":hover", f, 1}, {"color", y, 1}, {"red", p, 0},
		}},
		{"x.sql", "-- c\nSELECT u.name, count(*) AS n, E'a\\'b', 'it''s'\nfrom users u where u.select = s.my_func($1) and x::int > :min\n  and id = @id and \"Order\" = 1.5;\nCREATE TABLE t (id bigint, v VARCHAR(10));\nDO $f$ SELECT 1 $f$;\n", []want{
			{"-- c", c, 0}, {"SELECT", k, 0}, {"name", p, 0}, {"count", f, 0}, {"AS", k, 0}, {"E'a\\'b'", s, 0},
			{"'it''s'", s, 0}, {"from", k, 0}, {"where", k, 0}, {"select", p, 0}, {"my_func", f, 0}, {"$1", y, 0}, {"int", y, 0},
			{":min", y, 0}, {"@id", y, 0}, {"\"Order\"", p, 0}, {"1.5", m, 0}, {"CREATE", k, 0}, {"TABLE", k, 0},
			{"bigint", y, 0}, {"VARCHAR", y, 0}, {"10", m, 0}, {"$f$ SELECT 1 $f$", s, 0},
		}},
		{"x.tf", "a = 4 / 2 // half\n/* one */ locals {\n}\n", []want{
			{"/", o, 0}, {"// half", c, 0}, {"/* one */", c, 0}, {"locals", k, 0},
		}},
		{"x.tf", "resource \"r\" \"n\" {\n  a = \"x-${var.y}\"\n  b = max(1, 2)\n  c = <<EOT\nhi ${z}\nEOT\n  # c\n}\n", []want{
			{"resource", k, 0}, {"\"r\"", s, 0}, {"a", p, 1}, {"\"x-", s, 0}, {"${", k, 0}, {"var", p, 0},
			{"max", f, 0}, {"1", m, 0}, {"<<EOT", s, 0}, {"hi ", s, 0}, {"z", p, 0}, {"EOT", s, 1}, {"# c", c, 0},
		}},
	}
	for _, tc := range cases {
		expect(t, tc.name, tc.src, tc.wants)
	}
}

// TestLinesWholeFileContext checks that tokenizing the whole file classifies lines inside
// a multi-line raw string as string and lines inside a block comment as comment.
func TestLinesWholeFileContext(t *testing.T) {
	t.Parallel()
	text := "package main\n\nvar s = `first\nfunc notCode() { return 1 }\nlast`\n\n/*\nvar notCode = 2\n*/\n"
	expect(t, "x.go", text, []want{
		{"func notCode() { return 1 }", s, 0}, {"var notCode = 2", c, 0}, {"package", k, 0},
	})
}

// TestLinesCancelled covers the all-or-nothing rule: when ctx has ended, a file yields an error
// and no spans.
func TestLinesCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	text := "package main\n" + strings.Repeat("var x = f(1, 2, 3) + g(\"s\")\n", 2000)
	lines, err := highlight.Lines(ctx, highlight.LanguageOf("big.go"), text, nil)
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
		lines, err := highlight.Lines(t.Context(), highlight.LanguageOf("a.go"), text, nil)
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

// budget is the server's highlighting budget per side (REQ-HL-002).
const budget = 2 * time.Second

// largeFile is just under the large-file limit, the largest input the server highlights.
const largeFile = 1<<20 - 1024

// repeatTo repeats unit up to about n bytes.
func repeatTo(unit string, n int) string { return strings.Repeat(unit, n/len(unit)) }

// TestLinesPathological checks that inputs built to defeat a backtracking or rescanning lexer
// finish within the budget at the largest size the server highlights. A scanner that rescans
// the rest of a line or file per opener takes minutes on these.
func TestLinesPathological(t *testing.T) {
	t.Parallel()
	backticks := func() string {
		var b strings.Builder
		for n := 1; b.Len() < largeFile; n++ {
			b.WriteString(strings.Repeat("`", n%2000+1) + "x")
		}
		return b.String()
	}()
	cases := []struct{ name, file, text string }{
		{"long-make-line", "Makefile", repeatTo("a.c ", largeFile)},
		{"make-refs", "Makefile", repeatTo("$(a $(", largeFile)},
		{"make-continued", "Makefile", repeatTo("x \\\n", largeFile)},
		{"shell-escapes", "x.sh", "x=\"" + repeatTo("\\\\\\\"", largeFile) + "\" $"},
		{"shell-substs", "x.sh", repeatTo("$(", largeFile)},
		{"shell-quoted-heredocs", "x.sh", repeatTo("<<'a'", largeFile)},
		{"recipe-quoted-heredocs", "Makefile", "x:\n\t" + repeatTo("<<\"a\" ", largeFile)},
		{"shell-heredocs", "x.sh", repeatTo("<<E ", largeFile/2) + "\n" + repeatTo("x\n", largeFile/2)},
		{"js-regex", "x.js", repeatTo("=/[", largeFile)},
		{"js-templates", "x.js", repeatTo("`${", largeFile)},
		{"jsx-tags", "x.jsx", repeatTo("<a ", largeFile)},
		{"ts-braces", "x.ts", repeatTo("{(", largeFile)},
		{"tsx-generics", "x.tsx", repeatTo("<T,>(a: `${<b {...c}>", largeFile)},
		{"md-backticks", "x.md", backticks},
		{"md-links", "x.md", repeatTo("](", largeFile)},
		{"md-autolinks", "x.md", repeatTo("<a:", largeFile)},
		{"md-fences", "x.md", repeatTo("```go\n", largeFile)},
		{"md-comments", "x.md", repeatTo("<!--", largeFile)},
		{"md-front-matter", "x.md", "---\n" + repeatTo("a: 1\n", largeFile)},
		{"md-front-matters", "x.md", repeatTo("````md\n---\n", largeFile)},
		{"rust-raw", "x.rs", repeatTo("r##\"", largeFile)},
		{"rust-comments", "x.rs", repeatTo("/*", largeFile)},
		{"rust-quotes", "x.rs", repeatTo("'a", largeFile)},
		{"hcl-interp", "x.tf", repeatTo("\"${", largeFile)},
		{"hcl-heredocs", "x.tf", repeatTo("<<E\n", largeFile)},
		{"hcl-doc-in-doc", "x.tf", repeatTo("a = \"${b\nc = <<E\n", largeFile)},
		{"json-strings", "x.json", repeatTo("\"\\", largeFile)},
		{"json-comments", "x.json", repeatTo("/*", largeFile)},
		{"yaml-quotes", "x.yaml", repeatTo("'a: \"", largeFile)},
		{"yaml-flow", "x.yaml", repeatTo("[{a: ", largeFile)},
		{"yaml-blocks", "x.yaml", repeatTo("a: |\n b\n", largeFile)},
		{"yaml-plain", "x.yaml", repeatTo("a :b #", largeFile)},
		{"toml-strings", "x.toml", repeatTo("a = \"\"\"\\", largeFile)},
		{"toml-literals", "x.toml", repeatTo("'''a'", largeFile)},
		{"toml-headers", "x.toml", repeatTo("[[a\n", largeFile)},
		{"toml-nesting", "x.toml", repeatTo("a = [{", largeFile)},
		{"python-triples", "x.py", repeatTo("'''a\"\"\"", largeFile)},
		{"python-quotes", "x.py", repeatTo("'a\\", largeFile)},
		{"python-prefixes", "x.py", repeatTo("rb'", largeFile)},
		{"python-decorators", "x.py", repeatTo("@a.", largeFile)},
		{"python-soft", "x.py", repeatTo("match ", largeFile)},
		{"dockerfile-onbuild", "Dockerfile", repeatTo("ONBUILD ", largeFile)},
		{"dockerfile-heredocs", "Dockerfile", repeatTo("RUN <<E\n", largeFile)},
		{"dockerfile-continued", "Dockerfile", repeatTo("RUN a \\\n", largeFile)},
		{"dockerfile-json", "Dockerfile", repeatTo("CMD [", largeFile)},
		{"dockerfile-vars", "Dockerfile", repeatTo("ENV ${", largeFile)},
		{"dockerfile-flags", "Dockerfile", repeatTo("COPY --", largeFile)},
		{"css-strings", "x.css", repeatTo("a{b:\"\\", largeFile)},
		{"css-comments", "x.css", repeatTo("/*/", largeFile)},
		{"css-urls", "x.css", repeatTo("a{b:url(", largeFile)},
		{"css-blocks", "x.css", repeatTo("{a:", largeFile)},
		{"css-selectors", "x.css", repeatTo("a:[", largeFile)},
		{"sql-strings", "x.sql", repeatTo("'a''", largeFile)},
		{"sql-dollars", "x.sql", repeatTo("$a$", largeFile)},
		{"sql-prefixed", "x.sql", repeatTo("E'\\", largeFile)},
		{"sql-names", "x.sql", repeatTo("\"a\"\"", largeFile)},
		{"sql-comments", "x.sql", repeatTo("/*-", largeFile)},
		{"go-strings", "x.go", repeatTo("\"\\", largeFile)},
		{"tmpl-actions", "x.tmpl", repeatTo("{{\"", largeFile)},
		{"go-idents", "x.go", repeatTo("a ", largeFile)},
	}
	for _, tc := range cases {
		text := tc.text
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			defer cancel()
			start := time.Now()
			lines, err := highlight.Lines(ctx, highlight.LanguageOf(tc.file), text, nil)
			if err != nil {
				t.Fatalf("Lines(%d bytes) after %v: %v; want done within %v", len(text), time.Since(start), err, budget)
			}
			if elapsed := time.Since(start); elapsed > budget {
				t.Fatalf("Lines(%d bytes) took %v, want at most %v", len(text), elapsed, budget)
			}
			checkCover(t, text, lines)
		})
	}
}

// FuzzLines checks the coverage property, and that no scanner panics, on arbitrary input in
// every language.
func FuzzLines(f *testing.F) {
	names := []string{"x.go", "x.tmpl", "x.js", "x.ts", "x.tsx", "x.sh", "Makefile", "x.md", "x.rs", "x.tf", "x.json", "x.yaml", "x.toml", "x.py", "Dockerfile", "x.css", "x.sql"}
	for name, text := range corpus {
		for i, n := range names {
			if highlight.LanguageOf(name) == highlight.LanguageOf(n) {
				f.Add(uint8(i), text)
			}
		}
	}
	f.Fuzz(func(t *testing.T, which uint8, text string) {
		lang := highlight.LanguageOf(names[int(which)%len(names)])
		lines, err := highlight.Lines(t.Context(), lang, text, nil)
		if err != nil {
			t.Fatalf("Lines(%s): %v", lang, err)
		}
		checkCover(t, text, lines)
	})
}

// BenchmarkLines measures each language on its corpus files repeated to about 1 MiB, the largest
// side the server highlights.
func BenchmarkLines(b *testing.B) {
	for name, text := range corpus {
		big := strings.Repeat(text+"\n", largeFile/(len(text)+1)+1)
		lang := highlight.LanguageOf(name)
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(big)))
			for b.Loop() {
				if _, err := highlight.Lines(b.Context(), lang, big, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkLinesKeepFew measures the server's usual request: a whole side tokenized for the
// few lines a hunk shows, here one line in a hundred.
func BenchmarkLinesKeepFew(b *testing.B) {
	text := corpus["main.go"]
	big := strings.Repeat(text+"\n", largeFile/(len(text)+1)+1)
	keep := func(n int) bool { return n%100 == 0 }
	b.SetBytes(int64(len(big)))
	for b.Loop() {
		if _, err := highlight.Lines(b.Context(), highlight.LanguageOf("x.go"), big, keep); err != nil {
			b.Fatal(err)
		}
	}
}

// TestLinesKeep checks keep's contract on every corpus file: it is called once per line in
// increasing order, kept lines have exactly the spans of a full run, and other lines have none.
// TestLinesSpanCap checks that a line with more tokens than the cap gets at most 500 spans (the
// contract's REQ-HL-001), still covering the line, and that the cap is per line.
func TestLinesSpanCap(t *testing.T) {
	t.Parallel()
	const maxSpans = 500
	long := strings.Repeat("a+", 2000)
	text := long + "\nfunc f() {}\n"
	lines, err := highlight.Lines(t.Context(), highlight.LanguageOf("x.go"), text, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkCover(t, text, lines)
	first := lines[0]
	if len(first) > maxSpans || len(first) < maxSpans-1 {
		t.Fatalf("long line has %d spans, want %d or %d", len(first), maxSpans-1, maxSpans)
	}
	if got := first[1]; got.Class != highlight.ClassOperator || got.Text != "+" {
		t.Errorf("span 2 = %+v, want the classed operator +", got)
	}
	if last := first[len(first)-1]; last.Class != "" || len(last.Text) < len(long)/2 {
		t.Errorf("last span = class %q, %d bytes; want the plain rest of the line", last.Class, len(last.Text))
	}
	if got := lines[1]; len(got) < 4 || got[0] != (highlight.Span{Class: highlight.ClassKeyword, Text: "func"}) {
		t.Errorf("line 2 spans = %q, want it highlighted afresh", got)
	}
}

func TestLinesKeep(t *testing.T) {
	t.Parallel()
	for name, text := range corpus {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lang := highlight.LanguageOf(name)
			full, err := highlight.Lines(t.Context(), lang, text, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Growing one line's spans must leave the next line's alone.
			for i := range len(full) - 1 {
				next := fmt.Sprint(full[i+1])
				_ = append(full[i], highlight.Span{Class: "x", Text: "x"})
				if got := fmt.Sprint(full[i+1]); got != next {
					t.Fatalf("appending to line %d changed line %d to %s", i+1, i+2, got)
				}
			}
			var calls []int
			keep := func(n int) bool {
				calls = append(calls, n)
				return n%2 == 1
			}
			some, err := highlight.Lines(t.Context(), lang, text, keep)
			if err != nil {
				t.Fatal(err)
			}
			if len(some) != len(full) || len(calls) != len(full) {
				t.Fatalf("%d lines and %d keep calls, want %d", len(some), len(calls), len(full))
			}
			for i, n := range calls {
				if n != i+1 {
					t.Fatalf("keep call %d got line %d, want %d", i, n, i+1)
				}
			}
			for i := range full {
				want := full[i]
				if (i+1)%2 == 0 {
					want = nil
				}
				if fmt.Sprint(some[i]) != fmt.Sprint(want) {
					t.Errorf("line %d: spans %q, want %q", i+1, some[i], want)
				}
			}
		})
	}
}
