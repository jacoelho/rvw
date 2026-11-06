package source

import (
	"bufio"
	"strings"
	"testing"
)

// TestGrepParsersBoundErrors: a git grep record a parser rejects yields an error whose size does
// not grow with the record, however long the path or field it quotes.
func TestGrepParsersBoundErrors(t *testing.T) {
	t.Parallel()
	// Printable filler: %q quotes it byte for byte, so the bound measures the kept length.
	huge := strings.Repeat("x", 1<<20+5)
	needle, err := NewSearchText("abc")
	if err != nil {
		t.Fatal(err)
	}
	parsers := map[string]func(string) error{
		"parseSearch": func(in string) error {
			return parseSearch(bufio.NewReader(strings.NewReader(in)), "T:", needle, func(string, SearchMatch, int) error { return nil })
		},
		"parseGrep": func(in string) error {
			return parseGrep(bufio.NewReader(strings.NewReader(in)), "T:", func(string, bool) error { return nil })
		},
	}
	for _, tc := range []struct {
		name, in string
		parsers  []string
	}{
		{"no tree prefix", huge + "\x00", []string{"parseSearch", "parseGrep"}},
		{"unterminated record", huge, []string{"parseSearch", "parseGrep"}},
		{"line not a number", "T:a.go\x00" + huge + "\x00", []string{"parseSearch"}},
		{"line unterminated", "T:a.go\x00" + huge, []string{"parseSearch"}},
		{"column not a number", "T:a.go\x001\x00" + huge + "\x00", []string{"parseSearch"}},
		{"long path, line not a number", "T:" + huge + "\x00z\x00", []string{"parseSearch"}},
		{"long path, text without LF", "T:" + huge + "\x00text", []string{"parseGrep"}},
	} {
		for _, name := range tc.parsers {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				err := parsers[name](tc.in)
				if err == nil {
					t.Fatal("no error")
				}
				msg := err.Error()
				if len(msg) >= 300 || strings.Contains(msg, strings.Repeat("x", 200)) {
					t.Fatalf("error %.400q (%d bytes); want under 300 bytes", msg, len(msg))
				}
			})
		}
	}
}
