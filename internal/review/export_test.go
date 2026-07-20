package review

import (
	"errors"
	"strings"
	"testing"
)

func TestPrepareMarkdownUsesTypedTemplatesAndSelectionPolicy(t *testing.T) {
	issue, _ := NewCommentKind("issue")
	note, _ := NewCommentKind("note")
	current, err := NewReview(testSnapshot(t, 1, "one.go"), []CommentKind{issue, note})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, issue, "fix *this*")
	current = saveTestComment(t, current, ReviewCommentTarget(), note, "background")
	templates, err := CompileExportTemplates(
		"types={{comment_types}} count={{comment_count}}\n{{comments}}",
		"{{index}} [{{kind_upper}}] {{target}}: {{comment}}",
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := current.PrepareMarkdown(templates, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Format() != ExportMarkdown ||
		!strings.Contains(prepared.Body(), "types=ISSUE count=1") ||
		!strings.Contains(prepared.Body(), `fix \*this\*`) ||
		strings.Contains(prepared.Body(), "background") {
		t.Fatalf("prepared body:\n%s", prepared.Body())
	}
	if prepared.Bytes() != int64(len(prepared.Body())) {
		t.Fatalf("prepared bytes = %d, want %d", prepared.Bytes(), len(prepared.Body()))
	}
}

func TestCompileExportTemplatesRejectsUnknownPlaceholder(t *testing.T) {
	if _, err := CompileExportTemplates("{{unknown}}", "{{comment}}"); err == nil {
		t.Fatal("unknown placeholder was accepted")
	}
}

func TestPrepareJSONPreservesCommentSemanticsAndUnsafePathBytes(t *testing.T) {
	sourceID, _ := NewSourceID(1)
	builder, err := NewSnapshotBuilder(sourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	path, err := NewPath([]byte{'o', 'd', 'd', '/', 0xff, '"', '\n', '.', 'g', 'o'})
	if err != nil {
		t.Fatal(err)
	}
	change, _ := NewUnclassifiedChange(ChangeAdded, 1, 0)
	flags, _ := NewFlags(FileTraits{}, HiddenReasons{})
	if _, err := builder.AddFile(nil, &path, change, flags); err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	issue, _ := NewCommentKind("issue")
	current, err := NewReview(snapshot, []CommentKind{issue})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, issue, "quote: \"\n\u00e9")

	prepared, err := current.PrepareJSON(ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const expected = "{\"comments\":[{\"kind\":\"issue\",\"target\":{\"type\":\"file\"," +
		"\"path\":\"odd/\\u00ff\\\"\\n.go\"},\"binding\":\"active\"," +
		"\"comment\":\"quote: \\\"\\n\u00e9\"}]}\n"
	if prepared.Format() != ExportJSON || prepared.Body() != expected {
		t.Fatalf("prepared JSON:\n%s", prepared.Body())
	}
}

func TestPrepareJSONEncodesLineRangeAndSelectionPolicy(t *testing.T) {
	issue, _ := NewCommentKind("issue")
	note, _ := NewCommentKind("note")
	current, err := NewReview(testSnapshot(t, 1, "one.go"), []CommentKind{issue, note})
	if err != nil {
		t.Fatal(err)
	}
	start, _ := NewLineNumber(7)
	end, _ := NewLineNumber(9)
	target, _ := LineCommentTarget(0, LineOld, start, end)
	current = saveTestComment(t, current, target, issue, "range")
	current = saveTestComment(t, current, ReviewCommentTarget(), note, "omit")

	prepared, err := current.PrepareJSON(ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const expected = "{\"comments\":[{\"kind\":\"issue\",\"target\":{\"type\":\"range\"," +
		"\"path\":\"one.go\",\"side\":\"old\",\"start\":7,\"end\":9}," +
		"\"binding\":\"active\",\"comment\":\"range\"}]}\n"
	if prepared.Body() != expected {
		t.Fatalf("prepared JSON:\n%s", prepared.Body())
	}
}

func TestPrepareMarkdownPreflightsCumulativeLimitAndCancellation(t *testing.T) {
	issue, _ := NewCommentKind("issue")
	current, err := NewReview(testSnapshot(t, 1, "one.go"), []CommentKind{issue})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, issue, strings.Repeat("x", 10_000))

	repeatedComment := strings.Repeat("{{comment}}", 2_048)
	templates, err := CompileExportTemplates("{{comments}}", repeatedComment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.PrepareMarkdown(templates, ExportOptions{}); !errors.Is(err, ErrInvalidExport) {
		t.Fatalf("cumulative export limit error = %v, want ErrInvalidExport", err)
	}

	cancelled := errors.New("cancelled by test")
	templates, err = CompileExportTemplates("{{comments}}", "{{comment}}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.PrepareMarkdownCancellable(
		func() error { return cancelled },
		templates,
		ExportOptions{},
	); !errors.Is(err, cancelled) {
		t.Fatalf("cancellation error = %v, want sentinel", err)
	}
}
