// Package highlight tokenizes source into per-line spans with hand-written scanners, one per
// language. The language list is a product decision (REQ-HL-001).
//
// Every scanner runs in time linear in its input: it reads each byte a bounded number of times,
// never backtracks, and remembers failed searches for a closing delimiter so that it does not
// repeat them. Scanners use no regular expressions. Span texts are substrings of the input, so
// output costs no copies.
package highlight

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// Span is a run of one line's text with one highlight class. Class "" is plain text.
type Span struct {
	Class string
	Text  string
}

// Classes sent to the UI. They are CSS class suffixes.
const (
	ClassKeyword  = "k"
	ClassString   = "s"
	ClassComment  = "c"
	ClassNumber   = "m"
	ClassFunction = "f"
	ClassType     = "t"
	ClassOperator = "o"
)

// Language is a highlighted language. The zero Language is not highlighted.
type Language struct{ id langID }

type langID uint8

const (
	langNone langID = iota
	langGo
	langGoTemplate
	langJavaScript // JavaScript with JSX
	langTypeScript
	langTSX
	langShell
	langMakefile
	langMarkdown
	langRust
	langHCL
	langJSON // JSON with comments
	langYAML
	langTOML
	langPython
	langDockerfile
	langCSS
	langSQL
)

var langNames = [...]string{
	langNone:       "",
	langGo:         "go",
	langGoTemplate: "go-template",
	langJavaScript: "javascript",
	langTypeScript: "typescript",
	langTSX:        "tsx",
	langShell:      "shell",
	langMakefile:   "makefile", //nolint:goconst // the name, a file name and a fence word only happen to match
	langMarkdown:   "markdown",
	langRust:       "rust",
	langHCL:        "hcl",
	langJSON:       "json",
	langYAML:       "yaml",
	langTOML:       "toml",
	langPython:     "python",
	langDockerfile: "dockerfile", //nolint:goconst // as for makefile
	langCSS:        "css",
	langSQL:        "sql",
}

// String returns the language's name, or "" for the zero Language.
func (l Language) String() string { return langNames[l.id] }

// LanguageOf returns the language of a file at path p.
func LanguageOf(p string) Language {
	base := path.Base(p)
	switch base {
	case "Makefile", "makefile", "GNUmakefile":
		return Language{langMakefile}
	case ".bashrc", ".bash_profile", ".zshrc", ".zprofile", ".profile":
		return Language{langShell}
	case "Dockerfile", "dockerfile", "Containerfile", "containerfile":
		return Language{langDockerfile}
	}
	// Dockerfile.dev and similar name a Dockerfile by their prefix.
	if strings.HasPrefix(base, "Dockerfile.") || strings.HasPrefix(base, "Containerfile.") {
		return Language{langDockerfile}
	}
	switch strings.ToLower(path.Ext(base)) {
	case ".go":
		return Language{langGo}
	case ".tmpl", ".gotmpl", ".gohtml":
		return Language{langGoTemplate}
	case ".js", ".mjs", ".cjs", ".jsx":
		return Language{langJavaScript}
	case ".ts", ".mts", ".cts":
		return Language{langTypeScript}
	case ".tsx":
		return Language{langTSX}
	case ".sh", ".bash", ".zsh":
		return Language{langShell}
	case ".mk":
		return Language{langMakefile}
	case ".md", ".markdown":
		return Language{langMarkdown}
	case ".rs":
		return Language{langRust}
	case ".tf", ".tfvars", ".hcl":
		return Language{langHCL}
	case ".json", ".jsonc":
		return Language{langJSON}
	case ".yaml", ".yml":
		return Language{langYAML}
	case ".toml":
		return Language{langTOML}
	case ".py", ".pyi", ".pyw":
		return Language{langPython}
	case ".dockerfile", ".containerfile":
		return Language{langDockerfile}
	case ".css":
		return Language{langCSS}
	case ".sql":
		return Language{langSQL}
	default:
		return Language{}
	}
}

// fenceLanguage returns the language a Markdown fence's info word names, or langNone.
func fenceLanguage(word string) langID {
	switch strings.ToLower(word) {
	case "go", "golang":
		return langGo
	case "gotmpl", "go-template", "gotemplate", "tmpl":
		return langGoTemplate
	case "js", "javascript", "jsx", "mjs", "cjs":
		return langJavaScript
	case "ts", "typescript", "mts", "cts":
		return langTypeScript
	case "tsx":
		return langTSX
	case "sh", "bash", "zsh", "shell":
		return langShell
	case "make", "makefile", "mk":
		return langMakefile
	case "md", "markdown":
		return langMarkdown
	case "rust", "rs":
		return langRust
	case "hcl", "terraform", "tf", "tfvars":
		return langHCL
	case "json", "jsonc":
		return langJSON
	case "yaml", "yml":
		return langYAML
	case "toml":
		return langTOML
	case "py", "python", "python3", "pyi":
		return langPython
	case "dockerfile", "docker", "containerfile":
		return langDockerfile
	case "css":
		return langCSS
	case "sql", "pgsql", "postgresql", "postgres", "mysql", "sqlite", "plpgsql":
		return langSQL
	default:
		return langNone
	}
}

// Lines tokenizes the whole of text as lang and returns one span list per line, split on LF.
// The whole text is read, since a token may span lines, but only the lines keep accepts get
// spans: keep is called with each 1-based line number, once, in increasing order, and a nil keep
// keeps every line. A line that is not kept, or is empty, has nil spans. Spans are substrings of
// text and do not alias each other; the caller may keep them.
//
// Highlighting is all or nothing: it returns nil with no error for the zero Language, and an
// error when ctx ends first.
func Lines(ctx context.Context, lang Language, text string, keep func(line int) bool) ([][]Span, error) {
	if lang.id == langNone {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("highlight %s: %w", lang, err)
	}
	o := newOut(ctx, text, keep)
	scan(o, lang.id, text, 0, 0)
	if o.err != nil {
		return nil, fmt.Errorf("highlight %s: %w", lang, o.err)
	}
	return o.finish(), nil
}

// maxNesting bounds embedding: a Markdown fence inside a Markdown fence, and so on.
const maxNesting = 4

// scan classes s[i:] as id. Scanners read no byte at or past len(s), so a caller embeds one
// language in another by passing s cut at the embedded range's end. nesting counts the
// embeddings around this one.
func scan(o *out, id langID, s string, i, nesting int) {
	switch id {
	case langGo:
		scanGo(o, s, i)
	case langGoTemplate:
		scanGoTemplate(o, s, i)
	case langJavaScript:
		scanJS(o, s, i, jsOptions{jsx: true})
	case langTypeScript:
		scanJS(o, s, i, jsOptions{ts: true})
	case langTSX:
		scanJS(o, s, i, jsOptions{ts: true, jsx: true})
	case langShell:
		scanShell(o, s, i)
	case langMakefile:
		scanMakefile(o, s, i)
	case langMarkdown:
		scanMarkdown(o, s, i, nesting)
	case langRust:
		scanRust(o, s, i)
	case langHCL:
		scanHCL(o, s, i)
	case langJSON:
		scanJSON(o, s, i)
	case langYAML:
		scanYAML(o, s, i)
	case langTOML:
		scanTOML(o, s, i)
	case langPython:
		scanPython(o, s, i)
	case langDockerfile:
		scanDockerfile(o, s, i)
	case langCSS:
		scanCSS(o, s, i)
	case langSQL:
		scanSQL(o, s, i)
	case langNone:
	}
}
