package review

import (
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestExportInputMetadataAccountingCoversCompiledLayouts(t *testing.T) {
	if exportKindMetadataBytes < int64(unsafe.Sizeof(CommentKind(""))) {
		t.Fatalf(
			"kind metadata charge = %d, compiled size = %d",
			exportKindMetadataBytes,
			unsafe.Sizeof(CommentKind("")),
		)
	}
	if exportCommentMetadataBytes < int64(unsafe.Sizeof(exportComment{})) {
		t.Fatalf(
			"comment metadata charge = %d, compiled size = %d",
			exportCommentMetadataBytes,
			unsafe.Sizeof(exportComment{}),
		)
	}
	if exportTokenMetadataBytes < int64(unsafe.Sizeof(exportToken{})) {
		t.Fatalf(
			"template token metadata charge = %d, compiled size = %d",
			exportTokenMetadataBytes,
			unsafe.Sizeof(exportToken{}),
		)
	}
}

func TestCompiledExportTemplatesSeparateReadinessFromRetainedBytes(t *testing.T) {
	markdown := strings.Repeat("{{comments}}", 4_096)
	comment := "{{comment}}"
	templates, err := CompileExportTemplates(markdown, comment)
	if err != nil {
		t.Fatal(err)
	}
	if !templates.Ready() {
		t.Fatal("placeholder-only templates are not ready")
	}
	wantBytes := int64(len(markdown)+len(comment)) +
		int64(4_097)*exportTokenMetadataBytes
	if templates.RetainedBytes() != wantBytes {
		t.Fatalf("retained bytes = %d, want %d", templates.RetainedBytes(), wantBytes)
	}
	if cap(templates.markdown) != len(templates.markdown) ||
		cap(templates.comment) != len(templates.comment) {
		t.Fatalf(
			"compiled token capacities = (%d, %d), lengths = (%d, %d)",
			cap(templates.markdown), cap(templates.comment),
			len(templates.markdown), len(templates.comment),
		)
	}
	if (ExportTemplates{}).Ready() || (ExportTemplates{}).RetainedBytes() != 0 {
		t.Fatal("zero export templates are ready")
	}
}

func TestCompileExportTemplatesRejectsEmptyAndTooManyTokens(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		markdown string
		comment  string
	}{
		{name: "empty Markdown", comment: "{{comment}}"},
		{name: "empty comment", markdown: "{{comments}}"},
		{
			name:     "trailing literal is token 4097",
			markdown: strings.Repeat("{{comments}}", 4_096) + "x",
			comment:  "{{comment}}",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := CompileExportTemplates(testCase.markdown, testCase.comment); err == nil {
				t.Fatal("invalid templates were accepted")
			}
		})
	}
}

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

func TestExportInputExcludesPatchAndSearchStateAndPreservesOutput(t *testing.T) {
	issue, _ := NewCommentKind("issue")
	current, err := NewReview(testSnapshot(t, 1, "one.go"), []CommentKind{issue})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, issue, "short")
	baseline := current.ExportInput()

	pendingPatch, patchRequest, err := current.RequestPatch(0, WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	preparedPatch, err := TakePatch([]byte("@@ -1 +1 @@\n-old\n+short\n"))
	if err != nil {
		t.Fatal(err)
	}
	current, err = pendingPatch.ApplyPatch(PatchResult{
		Workspace: patchRequest.Workspace,
		Handle:    patchRequest.Handle,
		Status:    PatchReady,
		Prepared:  preparedPatch,
	})
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := NewOperationID(1)
	query, _ := NewSearchQuery("short")
	searching, searchRequest, err := current.StartSearch(
		operation,
		query,
		SearchAllFiles,
		WholeFileContext(),
	)
	if err != nil {
		t.Fatal(err)
	}
	batch := ScanSearchBatch(preparedPatch, 0, PrepareSearchQuery(query), SearchCursor{})
	searchResult, err := TakeSearchResult(SearchResultDescriptor{
		Operation: operation,
		Workspace: searchRequest.Workspace,
		Source:    searchRequest.Source,
		Status:    SearchComplete,
	}, batch.Hits)
	if err != nil {
		t.Fatal(err)
	}
	current, err = searching.ApplySearch(searchResult)
	if err != nil {
		t.Fatal(err)
	}
	if current.RetainedPatchBytes() == 0 || current.SearchSummary().HitCount == 0 {
		t.Fatal("test review does not retain patch and search payloads")
	}

	projected := current.ExportInput()
	if projected.RetainedBytes() != baseline.RetainedBytes() {
		t.Fatalf(
			"export input bytes changed with patch/search state: %d to %d",
			baseline.RetainedBytes(),
			projected.RetainedBytes(),
		)
	}
	templates, err := CompileExportTemplates("{{comments}}", "{{comment}}")
	if err != nil {
		t.Fatal(err)
	}
	fromReview, err := current.PrepareMarkdown(templates, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fromInput, err := projected.PrepareMarkdownCancellable(nil, templates, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if fromInput != fromReview {
		t.Fatalf("projected Markdown = %+v, review Markdown = %+v", fromInput, fromReview)
	}
	reviewJSON, err := current.PrepareJSON(ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inputJSON, err := projected.PrepareJSONCancellable(nil, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inputJSON != reviewJSON {
		t.Fatalf("projected JSON = %+v, review JSON = %+v", inputJSON, reviewJSON)
	}
}
