package review

import "testing"

func TestPreparedSyntaxReturnsNilForUnavailableOrEmptyLine(t *testing.T) {
	line, err := NewLineNumber(1)
	if err != nil {
		t.Fatal(err)
	}
	var unavailable *PreparedSyntax
	if spans := unavailable.Spans(line); spans != nil {
		t.Fatal("nil prepared syntax returned a non-nil iterator")
	}
	empty := &PreparedSyntax{lines: map[LineNumber][]SyntaxSpan{line: {}}}
	if spans := empty.Spans(line); spans != nil {
		t.Fatal("empty syntax line returned a non-nil iterator")
	}
	if spans := empty.Spans(line + 1); spans != nil {
		t.Fatal("uncovered syntax line returned a non-nil iterator")
	}
}
