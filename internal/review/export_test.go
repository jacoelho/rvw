package review

import (
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestExportInputMetadataAccountingCoversCompiledLayouts(t *testing.T) {
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

func TestExportOptionsValidateRebasedUnmappableFlag(t *testing.T) {
	options, err := NewExportOptions(ExportUnmappable)
	if err != nil {
		t.Fatal(err)
	}
	if !options.Has(ExportUnmappable) {
		t.Fatal("unmappable export option is absent")
	}
	options, err = options.Toggle(ExportUnmappable)
	if err != nil || options.Has(ExportUnmappable) {
		t.Fatalf("toggled options = %#v, %v", options, err)
	}
	if empty, err := NewExportOptions(); err != nil || empty.Has(ExportUnmappable) {
		t.Fatalf("empty options = %#v, %v", empty, err)
	}
	for _, value := range []ExportOption{0, ExportUnmappable + 1} {
		if _, err := NewExportOptions(value); err == nil {
			t.Fatalf("invalid option %d was accepted", value)
		}
	}
	invalid := ExportOptions{bits: 1 << ExportUnmappable}
	if invalid.valid() {
		t.Fatal("unknown export option bit was accepted")
	}
	if _, err := invalid.Toggle(ExportUnmappable); err == nil {
		t.Fatal("toggle accepted options with unknown bits")
	}
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	templates, err := CompileExportTemplates("{{comments}}", "{{comment}}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.PrepareMarkdown(templates, invalid); !errors.Is(err, ErrInvalidExport) {
		t.Fatalf("invalid Markdown options error = %v", err)
	}
	if _, err := current.PrepareJSON(invalid); !errors.Is(err, ErrInvalidExport) {
		t.Fatalf("invalid JSON options error = %v", err)
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

func TestPrepareMarkdownUsesTypedTemplatesForAllComments(t *testing.T) {
	current, err := NewReview(testSnapshot(t, 1, "one.go"))
	if err != nil {
		t.Fatal(err)
	}
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, "fix *this*")
	current = saveTestComment(t, current, ReviewCommentTarget(), "background")
	templates, err := CompileExportTemplates(
		"count={{comment_count}}\n{{comments}}",
		"{{index}} {{target}}: {{comment}}",
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := current.PrepareMarkdown(templates, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Format() != ExportMarkdown ||
		!strings.Contains(prepared.Body(), "count=2") ||
		!strings.Contains(prepared.Body(), `fix \*this\*`) ||
		!strings.Contains(prepared.Body(), "background") {
		t.Fatalf("prepared body:\n%s", prepared.Body())
	}
	if prepared.Bytes() != int64(len(prepared.Body())) {
		t.Fatalf("prepared bytes = %d, want %d", prepared.Bytes(), len(prepared.Body()))
	}
}

func TestPrepareMarkdownDoesNotEscapeOrdinaryPathAndProsePunctuation(t *testing.T) {
	const path = "experimental/technical-writing-discover/SKILL.md"
	current, err := NewReview(testSnapshot(t, 1, path))
	if err != nil {
		t.Fatal(err)
	}
	start, _ := NewLineNumber(2)
	end, _ := NewLineNumber(7)
	target, _ := LineCommentTarget(0, LineNew, start, end)
	current = saveTestComment(t, current, target, "Keep this deterministic.")
	templates, err := CompileExportTemplates(
		"{{comments}}",
		"{{index}}. `{{target}}` - {{comment}}",
	)
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := current.PrepareMarkdown(templates, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "1. `experimental/technical-writing-discover/SKILL.md:2-7` - Keep this deterministic."
	if prepared.Body() != want {
		t.Fatalf("prepared body = %q, want %q", prepared.Body(), want)
	}
}

func TestMarkdownEscapeRetainsStructuralListEscapes(t *testing.T) {
	const input = "1. ordered\n- unordered\nordinary.file-name"
	const want = "1\\. ordered\n\\- unordered\nordinary.file-name"
	if got := markdownEscape(input); got != want {
		t.Fatalf("markdownEscape(%q) = %q, want %q", input, got, want)
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
	current, err := NewReview(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, "quote: \"\n\u00e9")

	prepared, err := current.PrepareJSON(ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const expected = "{\"comments\":[{\"target\":{\"type\":\"file\"," +
		"\"path\":\"odd/\\u00ff\\\"\\n.go\"},\"binding\":\"active\"," +
		"\"comment\":\"quote: \\\"\\n\u00e9\"}]}\n"
	if prepared.Format() != ExportJSON || prepared.Body() != expected {
		t.Fatalf("prepared JSON:\n%s", prepared.Body())
	}
}

func TestPrepareJSONEncodesLineRangeAndAllComments(t *testing.T) {
	current, err := NewReview(testSnapshot(t, 1, "one.go"))
	if err != nil {
		t.Fatal(err)
	}
	start, _ := NewLineNumber(7)
	end, _ := NewLineNumber(9)
	target, _ := LineCommentTarget(0, LineOld, start, end)
	current = saveTestComment(t, current, target, "range")
	current = saveTestComment(t, current, ReviewCommentTarget(), "omit")

	prepared, err := current.PrepareJSON(ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const expected = "{\"comments\":[{\"target\":{\"type\":\"range\"," +
		"\"path\":\"one.go\",\"side\":\"old\",\"start\":7,\"end\":9}," +
		"\"binding\":\"active\",\"comment\":\"range\"},{\"target\":{" +
		"\"type\":\"review\"},\"binding\":\"active\",\"comment\":\"omit\"}]}\n"
	if prepared.Body() != expected {
		t.Fatalf("prepared JSON:\n%s", prepared.Body())
	}
}

func TestPrepareJSONRequiresOptionForUnmappableComments(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "gone.go"))
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, "orphaned")
	current, _, err := current.ReplaceSnapshot(testSnapshot(t, 2, "new.go"))
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := current.PrepareJSON(ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Body() != "{\"comments\":[]}\n" {
		t.Fatalf("default export included unmappable comment: %s", prepared.Body())
	}
	options, err := NewExportOptions(ExportUnmappable)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err = current.PrepareJSON(options)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prepared.Body(), `"binding":"orphaned"`) ||
		!strings.Contains(prepared.Body(), `"comment":"orphaned"`) {
		t.Fatalf("explicit export omitted unmappable comment: %s", prepared.Body())
	}
}

func TestPrepareMarkdownPreflightsCumulativeLimitAndCancellation(t *testing.T) {
	current, err := NewReview(testSnapshot(t, 1, "one.go"))
	if err != nil {
		t.Fatal(err)
	}
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, strings.Repeat("x", 10_000))

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
	current, err := NewReview(testSnapshot(t, 1, "one.go"))
	if err != nil {
		t.Fatal(err)
	}
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, "short")
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
