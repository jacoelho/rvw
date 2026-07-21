package review

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

const (
	scaleFileCount    = 1_024
	scaleFileLines    = 10_000
	scaleCommentCount = 512
	scaleViewportRows = 80
)

var (
	scalePatchSink       *PreparedPatch
	scalePatchLinesSink  []PatchLine
	scaleExportSink      PreparedExport
	scalePublicationSink *CheckedPublication
	scaleSearchHitsSink  int
	scaleSyntaxSink      *PreparedSyntax
	scaleReviewSink      Review
	scaleFileSink        File
)

func BenchmarkNewReview100000Files(b *testing.B) {
	snapshot := scaleReviewWithFiles(b, MaxFiles).Snapshot()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		current, err := NewReview(snapshot)
		if err != nil {
			b.Fatal(err)
		}
		scaleReviewSink = current
	}
}

func BenchmarkTraverseSnapshot100000Files(b *testing.B) {
	snapshot := scaleReviewWithFiles(b, MaxFiles).Snapshot()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		count := 0
		for file := range snapshot.AllFiles() {
			scaleFileSink = file
			count++
		}
		if count != MaxFiles {
			b.Fatalf("traversed files = %d, want %d", count, MaxFiles)
		}
	}
}

func BenchmarkTakePatchLargeFile(b *testing.B) {
	patchText := scalePatchText(scaleFileLines)
	b.ReportAllocs()
	b.SetBytes(int64(len(patchText)))
	b.ResetTimer()

	for range b.N {
		prepared, err := TakePatch(bytes.Clone(patchText))
		if err != nil {
			b.Fatal(err)
		}
		if prepared.Lines() != scaleFileLines+1 || prepared.Hunks() != 1 {
			b.Fatalf(
				"prepared patch = %d lines and %d hunks",
				prepared.Lines(),
				prepared.Hunks(),
			)
		}
		scalePatchSink = prepared
	}
}

func BenchmarkPatchViewport10000LineFileIn1000FileReview(b *testing.B) {
	current := scaleReviewWithFiles(b, scaleFileCount)
	current = applyScalePatch(b, current, 0, scaleFileLines)
	start := uint32(scaleFileLines / 2)
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		lines := current.PatchLines(
			0,
			start,
			scaleViewportRows,
			MaxSyntaxLineBytes,
		)
		if len(lines) != scaleViewportRows {
			b.Fatalf("viewport rows = %d, want %d", len(lines), scaleViewportRows)
		}
		scalePatchLinesSink = lines
	}
}

func BenchmarkPrepareMarkdownManyComments(b *testing.B) {
	current := scaleReviewWithComments(b, scaleCommentCount)
	templates, err := CompileExportTemplates(
		"# Review\n\nCount: {{comment_count}}\n\n{{comments}}\n",
		"- {{index}} {{target}}: {{comment}}",
	)
	if err != nil {
		b.Fatal(err)
	}
	options, err := NewExportOptions()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		prepared, err := current.PrepareMarkdown(templates, options)
		if err != nil {
			b.Fatal(err)
		}
		if prepared.Bytes() == 0 {
			b.Fatal("Markdown export is empty")
		}
		scaleExportSink = prepared
	}
}

func BenchmarkPrepareCheckedPublicationManyComments(b *testing.B) {
	current := scaleReviewWithComments(b, scaleCommentCount)
	policies, err := NewPublicationPolicies()
	if err != nil {
		b.Fatal(err)
	}
	options, err := NewPublicationOptions(PublicationEventComment, policies)
	if err != nil {
		b.Fatal(err)
	}
	base, err := NewObjectID(strings.Repeat("1", 40))
	if err != nil {
		b.Fatal(err)
	}
	head, err := NewObjectID(strings.Repeat("2", 40))
	if err != nil {
		b.Fatal(err)
	}
	target, err := NewPublicationTarget(PublicationTargetDescriptor{
		Repository:  "owner/repository",
		PullRequest: 42,
		Base:        base,
		Head:        head,
	})
	if err != nil {
		b.Fatal(err)
	}
	marker, err := NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	if err != nil {
		b.Fatal(err)
	}
	descriptor := CheckedPublicationDescriptor{
		Target: target,
		Marker: marker,
	}
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		prepared, err := current.PreparePublication(options)
		if err != nil {
			b.Fatal(err)
		}
		checked, err := TakeCheckedPublication(descriptor, prepared)
		if err != nil {
			b.Fatal(err)
		}
		if prepared.CommentCount() != scaleCommentCount ||
			prepared.IncludedCount() != scaleCommentCount {
			b.Fatalf(
				"prepared publication = %d inline and %d included comments",
				prepared.CommentCount(),
				prepared.IncludedCount(),
			)
		}
		scalePublicationSink = checked
	}
}

func BenchmarkSearchLargePatch(b *testing.B) {
	patch, err := TakePatch(scaleSearchPatchText(scaleFileLines))
	if err != nil {
		b.Fatal(err)
	}
	query, err := NewSearchQuery("review-needle")
	if err != nil {
		b.Fatal(err)
	}
	preparedQuery := PrepareSearchQuery(query)
	fileID, err := NewFileID(0)
	if err != nil {
		b.Fatal(err)
	}
	expectedHits := (scaleFileLines + 96) / 97
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		cursor := SearchCursor{}
		hitCount := 0
		for {
			batch := ScanSearchBatch(patch, fileID, preparedQuery, cursor)
			hitCount += len(batch.Hits)
			if batch.Done {
				break
			}
			cursor = batch.Cursor
		}
		if hitCount != expectedHits {
			b.Fatalf("search hits = %d, want %d", hitCount, expectedHits)
		}
		scaleSearchHitsSink = hitCount
	}
}

func BenchmarkPrepareSyntaxLargeFile(b *testing.B) {
	blob := scaleSyntaxBlob(scaleFileLines)
	objectID, err := NewObjectID(strings.Repeat("3", 40))
	if err != nil {
		b.Fatal(err)
	}
	selector, err := NewSyntaxSelector("Go")
	if err != nil {
		b.Fatal(err)
	}
	identity, err := NewSyntaxIdentity(objectID, selector)
	if err != nil {
		b.Fatal(err)
	}
	lines := make([]LineNumber, MaxSyntaxLines)
	for index := range lines {
		line, err := NewLineNumber(
			uint32(index*(scaleFileLines/MaxSyntaxLines) + 1),
		)
		if err != nil {
			b.Fatal(err)
		}
		lines[index] = line
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(blob)))
	b.ResetTimer()

	for range b.N {
		prepared, err := PrepareSyntax(identity, lines, blob)
		if err != nil {
			b.Fatal(err)
		}
		if !prepared.Covers(lines[len(lines)-1]) {
			b.Fatal("prepared syntax does not cover the final demanded line")
		}
		scaleSyntaxSink = prepared
	}
}

func TestScalePatchViewportMaterializesOnlyRequestedRows(t *testing.T) {
	current := scaleReviewWithFiles(t, scaleFileCount)
	current = applyScalePatch(t, current, 0, scaleFileLines)
	if got := current.Snapshot().FileCount(); got != scaleFileCount {
		t.Fatalf("snapshot files = %d, want %d", got, scaleFileCount)
	}
	lines := current.PatchLines(
		0,
		scaleFileLines/2,
		scaleViewportRows,
		MaxSyntaxLineBytes,
	)
	if len(lines) != scaleViewportRows {
		t.Fatalf("viewport rows = %d, want %d", len(lines), scaleViewportRows)
	}
	if state := current.Patch(0); state.Lines != scaleFileLines+1 {
		t.Fatalf("retained patch lines = %d, want %d", state.Lines, scaleFileLines+1)
	}
	if got, want := current.RetainedPatchBytes(), current.Patch(0).RetainedBytes(); got != want {
		t.Fatalf("retained patch bytes = %d, want only active patch bytes %d", got, want)
	}
	for file := 1; file < scaleFileCount; file++ {
		fileID, err := NewFileID(uint32(file))
		if err != nil {
			t.Fatal(err)
		}
		if state := current.Patch(fileID); state.Status != PatchUnavailable {
			t.Fatalf("file %d patch status = %d, want unavailable", file, state.Status)
		}
	}
}

func applyScalePatch(tb testing.TB, current Review, fileID FileID, lineCount int) Review {
	tb.Helper()
	context := WholeFileContext()
	pending, request, err := current.RequestPatch(fileID, context)
	if err != nil {
		tb.Fatal(err)
	}
	prepared, err := TakePatch(scalePatchText(lineCount))
	if err != nil {
		tb.Fatal(err)
	}
	current, err = pending.ApplyPatch(PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    PatchReady,
		Prepared:  prepared,
	})
	if err != nil {
		tb.Fatal(err)
	}
	return current
}

func scaleReviewWithComments(tb testing.TB, count int) Review {
	tb.Helper()
	current := scaleReview(tb)
	for index := range count {
		line, err := NewLineNumber(uint32(index%scaleFileLines + 1))
		if err != nil {
			tb.Fatal(err)
		}
		target, err := LineCommentTarget(0, LineNew, line, line)
		if err != nil {
			tb.Fatal(err)
		}
		current, err = current.BeginDraft(target)
		if err != nil {
			tb.Fatal(err)
		}
		current, err = current.UpdateDraft(fmt.Sprintf(
			"Comment %04d: verify the boundary behavior and preserve the review invariant.",
			index+1,
		))
		if err != nil {
			tb.Fatal(err)
		}
		current, _, err = current.SaveDraft()
		if err != nil {
			tb.Fatal(err)
		}
	}
	return current
}

func scaleReview(tb testing.TB) Review {
	tb.Helper()
	return scaleReviewWithFiles(tb, 1)
}

func scaleReviewWithFiles(tb testing.TB, fileCount int) Review {
	tb.Helper()
	sourceID, err := NewSourceID(1)
	if err != nil {
		tb.Fatal(err)
	}
	builder, err := NewSnapshotBuilder(sourceID, fileCount)
	if err != nil {
		tb.Fatal(err)
	}
	change, err := NewUnclassifiedChange(ChangeAdded, scaleFileLines, 0)
	if err != nil {
		tb.Fatal(err)
	}
	flags, err := NewFlags(FileTraits{}, HiddenReasons{})
	if err != nil {
		tb.Fatal(err)
	}
	for index := range fileCount {
		path, err := NewPath([]byte(fmt.Sprintf(
			"pkg/%03d/file-%04d.go",
			index/32,
			index,
		)))
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := builder.AddFile(nil, &path, change, flags); err != nil {
			tb.Fatal(err)
		}
	}
	snapshot, err := builder.Finish()
	if err != nil {
		tb.Fatal(err)
	}
	current, err := NewReview(snapshot)
	if err != nil {
		tb.Fatal(err)
	}
	return current
}

func scalePatchText(lineCount int) []byte {
	var patch strings.Builder
	fmt.Fprintf(&patch, "@@ -1,%d +1,%d @@\n", lineCount, lineCount)
	for index := range lineCount {
		fmt.Fprintf(
			&patch,
			" line %05d: func retainedViewport() { value := %d }\n",
			index+1,
			index,
		)
	}
	return []byte(patch.String())
}

func scaleSearchPatchText(lineCount int) []byte {
	var patch strings.Builder
	fmt.Fprintf(&patch, "@@ -1,%d +1,%d @@\n", lineCount, lineCount)
	for index := range lineCount {
		if index%97 == 0 {
			fmt.Fprintf(
				&patch,
				" line %05d contains review-needle for batched search\n",
				index+1,
			)
			continue
		}
		fmt.Fprintf(
			&patch,
			" line %05d contains ordinary searchable source text\n",
			index+1,
		)
	}
	return []byte(patch.String())
}

func scaleSyntaxBlob(lineCount int) []byte {
	var blob strings.Builder
	for index := range lineCount {
		fmt.Fprintf(
			&blob,
			"func value%d() string { return \"item-%d\" } // retained syntax line\n",
			index,
			index,
		)
	}
	return []byte(blob.String())
}
