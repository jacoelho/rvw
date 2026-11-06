// Package highlight tokenizes Go, TypeScript, shell and Makefile source into per-line spans.
// The language list is a product decision, not whatever Chroma ships.
package highlight

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
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

// lexerMakefile is Chroma's lexer name for Makefiles.
const lexerMakefile = "makefile"

// Language is a highlighted language. The zero Language is not highlighted.
type Language struct{ lexer string }

// LanguageOf returns the language of a file at path p.
func LanguageOf(p string) Language {
	base := path.Base(p)
	switch base {
	case "Makefile", "makefile", "GNUmakefile":
		return Language{lexerMakefile}
	}
	switch strings.ToLower(path.Ext(base)) {
	case ".go":
		return Language{"go"}
	case ".ts", ".mts", ".cts":
		return Language{"typescript"}
	case ".tsx":
		return Language{"tsx"}
	case ".sh", ".bash", ".zsh":
		return Language{"bash"}
	case ".mk":
		return Language{lexerMakefile}
	default:
		return Language{}
	}
}

// checkEvery is how many tokens pass between context checks.
const checkEvery = 512

// Lines tokenizes the whole of text as lang and returns one span list per line. Highlighting is
// all or nothing: it returns nil with no error for the zero Language, and an error when
// tokenizing fails or ctx ends first.
func Lines(ctx context.Context, lang Language, text string) (lines [][]Span, err error) {
	if lang.lexer == "" {
		return nil, nil
	}
	lexer := lexers.Get(lang.lexer)
	if lexer == nil {
		return nil, fmt.Errorf("no lexer for %s", lang.lexer)
	}
	defer func() {
		// Chroma reports tokenizer errors by panicking from the iterator.
		if r := recover(); r != nil {
			lines, err = nil, fmt.Errorf("highlight %s: %v", lang.lexer, r)
		}
	}()
	it, err := lexer.Tokenise(&chroma.TokeniseOptions{State: "root"}, text)
	if err != nil {
		return nil, fmt.Errorf("highlight %s: %w", lang.lexer, err)
	}
	want := strings.Count(text, "\n") + 1
	lines = make([][]Span, 1, want)
	// Adjacent tokens of one class merge into one span. The span being built is accumulated in a
	// builder, so a long line of same-class tokens costs linear, not quadratic, time.
	var pending strings.Builder
	pendingClass := ""
	flush := func() {
		if pending.Len() > 0 {
			last := len(lines) - 1
			lines[last] = append(lines[last], Span{Class: pendingClass, Text: pending.String()})
			pending.Reset()
		}
	}
	n := 0
	for tok := it(); tok != chroma.EOF; tok = it() {
		if n++; n%checkEvery == 0 {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("highlight %s: %w", lang.lexer, err)
			}
		}
		class := classOf(tok.Type)
		for part, rest, more := strings.Cut(tok.Value, "\n"); ; part, rest, more = strings.Cut(rest, "\n") {
			if part != "" {
				if class != pendingClass {
					flush()
					pendingClass = class
				}
				pending.WriteString(part)
			}
			if !more {
				break
			}
			flush()
			if len(lines) == want {
				break // a lexer-added trailing newline
			}
			lines = append(lines, nil)
		}
	}
	flush()
	for len(lines) < want {
		lines = append(lines, nil)
	}
	return lines, nil
}

func classOf(t chroma.TokenType) string {
	switch {
	case t.InCategory(chroma.Comment):
		return ClassComment
	case t.InCategory(chroma.Keyword):
		if t == chroma.KeywordType {
			return ClassType
		}
		return ClassKeyword
	case t.InSubCategory(chroma.LiteralString):
		return ClassString
	case t.InSubCategory(chroma.LiteralNumber):
		return ClassNumber
	case t == chroma.NameFunction || t == chroma.NameFunctionMagic:
		return ClassFunction
	case t == chroma.NameBuiltin || t == chroma.NameClass || t == chroma.KeywordType:
		return ClassType
	case t.InCategory(chroma.Operator):
		return ClassOperator
	default:
		return ""
	}
}
