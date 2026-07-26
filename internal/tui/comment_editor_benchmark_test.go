package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/change"
)

const (
	commentEditorBenchmarkWidth             = 80
	commentEditorBenchmarkHeight            = 6
	commentEditorBenchmarkNavigationCycles  = 8
	commentEditorBenchmarkOperationDeadline = 5 * time.Second
)

var commentEditorBenchmarkFixtures = [...]struct {
	name  string
	bytes int
}{
	{name: "1055B", bytes: 1_055},
	{name: "8205B", bytes: 8_205},
	{name: "65535B", bytes: int(change.MaxCommentBodyBytes) - 1},
}

var (
	commentEditorBenchmarkBytesSink     int
	commentEditorBenchmarkCursorSink    *tea.Cursor
	commentEditorBenchmarkOperationSink change.OperationID
	commentEditorBenchmarkViewSink      string
)

func BenchmarkCommentEditorUpdateScaling(b *testing.B) {
	for _, fixture := range commentEditorBenchmarkFixtures {
		body := commentEditorBenchmarkBody(b, fixture.bytes)
		want := body + "b"
		b.Run(fixture.name, func(b *testing.B) {
			model, _ := newTestModel(b, map[string]string{"alpha.txt": "changed\n"})
			baseReview := model.review
			b.ReportAllocs()

			for b.Loop() {
				b.StopTimer()
				prepareCommentEditorBenchmark(b, model, baseReview, body)
				b.StartTimer()
				model.updateCommentEditor(tea.PasteMsg{Content: "b"})
				b.StopTimer()

				draft, ok := model.review.Draft()
				if !ok || model.commentEditor.Value() != want || draft.Body() != want {
					b.Fatalf(
						"updated editor/draft bytes = %d/%d, want %d",
						len(model.commentEditor.Value()),
						len(draft.Body()),
						len(want),
					)
				}
				commentEditorBenchmarkBytesSink = len(draft.Body())
				b.StartTimer()
			}
		})
	}
}

func BenchmarkCommentEditorViewScaling(b *testing.B) {
	for _, fixture := range commentEditorBenchmarkFixtures {
		body := commentEditorBenchmarkBody(b, fixture.bytes)
		b.Run(fixture.name, func(b *testing.B) {
			model, _ := newTestModel(b, map[string]string{"alpha.txt": "changed\n"})
			prepareCommentEditorBenchmark(b, model, model.review, body)
			var view string
			b.ReportAllocs()

			for b.Loop() {
				view = model.commentEditor.View()
			}
			commentEditorBenchmarkViewSink = view
			assertCommentEditorBenchmarkState(b, model, body)
			if view == "" || !strings.Contains(view, "z") {
				b.Fatalf("rendered view does not contain the fixture tail: %q", view)
			}
		})
	}
}

func BenchmarkCommentEditorModelViewScaling(b *testing.B) {
	for _, fixture := range commentEditorBenchmarkFixtures {
		body := commentEditorBenchmarkBody(b, fixture.bytes)
		b.Run(fixture.name, func(b *testing.B) {
			model, _ := newTestModel(b, map[string]string{"alpha.txt": "changed\n"})
			_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
			prepareCommentEditorBenchmark(b, model, model.review, body)
			b.ReportAllocs()

			for b.Loop() {
				view := model.View()
				commentEditorBenchmarkViewSink = view.Content
				commentEditorBenchmarkCursorSink = view.Cursor
			}
			assertCommentEditorBenchmarkState(b, model, body)
			if commentEditorBenchmarkViewSink == "" || commentEditorBenchmarkCursorSink == nil {
				b.Fatal("full model view omitted comment content or cursor")
			}
		})
	}
}

func BenchmarkCommentEditorUnicodeViewAndNavigation(b *testing.B) {
	fixtures := [...]struct {
		name    string
		pattern string
	}{
		{name: "emoji", pattern: "🙂🙂🙂🙂\n"},
		{name: "combining", pattern: "a\u0301a\u0301a\u0301a\u0301\n"},
		{name: "oversized-cluster", pattern: "a" + strings.Repeat("\u0301", 2_100) + "\n"},
	}
	for _, fixture := range fixtures {
		body := commentEditorBenchmarkPatternBody(b, fixture.pattern)
		b.Run(fixture.name, func(b *testing.B) {
			model, _ := newTestModel(b, map[string]string{"alpha.txt": "changed\n"})
			prepareCommentEditorBenchmark(b, model, model.review, body)
			b.ReportAllocs()

			for b.Loop() {
				model.commentEditor.CursorUp()
				commentEditorBenchmarkViewSink = model.commentEditor.View()
				commentEditorBenchmarkCursorSink = model.commentEditor.Cursor()
				model.commentEditor.CursorDown()
				commentEditorBenchmarkViewSink = model.commentEditor.View()
				commentEditorBenchmarkCursorSink = model.commentEditor.Cursor()
			}
			assertCommentEditorBenchmarkState(b, model, body)
		})
	}
}

func BenchmarkCommentEditorNavigationRefreshScaling(b *testing.B) {
	for _, fixture := range commentEditorBenchmarkFixtures {
		body := commentEditorBenchmarkBody(b, fixture.bytes)
		b.Run(fixture.name, func(b *testing.B) {
			model, _ := newTestModel(b, map[string]string{"alpha.txt": "changed\n"})
			prepareCommentEditorBenchmark(b, model, model.review, body)
			var view string
			renderedBytes := 0
			b.ReportAllocs()
			b.ReportMetric(2*commentEditorBenchmarkNavigationCycles, "views/op")

			for b.Loop() {
				for range commentEditorBenchmarkNavigationCycles {
					model.commentEditor.CursorUp()
					view = model.commentEditor.View()
					renderedBytes += len(view)
					model.commentEditor.CursorDown()
					view = model.commentEditor.View()
					renderedBytes += len(view)
				}
			}
			commentEditorBenchmarkViewSink = view
			commentEditorBenchmarkBytesSink = renderedBytes
			assertCommentEditorBenchmarkState(b, model, body)
			if view == "" || !strings.Contains(view, "z") {
				b.Fatalf("refreshed view does not contain the fixture tail: %q", view)
			}
		})
	}
}

func BenchmarkCommentEditorProductionNavigation(b *testing.B) {
	fixtures := [...]struct {
		name  string
		bytes int
	}{
		{name: "1B", bytes: 1},
		{name: "8192B", bytes: 8 << 10},
		{name: "65535B", bytes: int(change.MaxCommentBodyBytes) - 1},
	}
	for _, fixture := range fixtures {
		body := commentEditorBenchmarkBody(b, fixture.bytes)
		b.Run(fixture.name, func(b *testing.B) {
			model, _ := newTestModel(b, map[string]string{"alpha.txt": "changed\n"})
			prepareCommentEditorBenchmark(b, model, model.review, body)
			b.ReportAllocs()

			for b.Loop() {
				model.updateCommentEditor(editorKey('p', tea.ModCtrl))
				model.updateCommentEditor(editorKey('n', tea.ModCtrl))
			}
			commentEditorBenchmarkCursorSink = model.commentEditor.Cursor()
			assertCommentEditorBenchmarkState(b, model, body)
		})
	}
}

func BenchmarkCommentEditorProjectionRebuild(b *testing.B) {
	const bodyBytes = int(change.MaxCommentBodyBytes) - 1
	fixtures := [...]struct {
		name string
		body func(testing.TB) string
	}{
		{
			name: "ascii",
			body: func(tb testing.TB) string {
				tb.Helper()
				return commentEditorBenchmarkBody(tb, bodyBytes)
			},
		},
		{
			name: "emoji",
			body: func(tb testing.TB) string {
				tb.Helper()
				return commentEditorBenchmarkPatternBody(tb, "🙂🙂🙂🙂\n")
			},
		},
		{
			name: "combining",
			body: func(tb testing.TB) string {
				tb.Helper()
				return commentEditorBenchmarkPatternBody(tb, "a\u0301a\u0301a\u0301a\u0301\n")
			},
		},
		{
			name: "oversized-cluster",
			body: func(tb testing.TB) string {
				tb.Helper()
				return commentEditorBenchmarkPatternBody(
					tb,
					"a"+strings.Repeat("\u0301", 2_100)+"\n",
				)
			},
		},
	}
	for _, fixture := range fixtures {
		body := fixture.body(b)
		b.Run(fixture.name, func(b *testing.B) {
			editor := newCommentEditor(change.MaxCommentBodyBytes)
			editor.SetWidth(commentEditorBenchmarkWidth)
			editor.SetHeight(commentEditorBenchmarkHeight)
			b.ReportAllocs()

			for b.Loop() {
				editor.SetValue(body)
			}
			commentEditorBenchmarkBytesSink = int(editor.byteCount)
			if editor.Value() != body {
				b.Fatalf("rebuilt editor bytes = %d, want %d", editor.byteCount, len(body))
			}
		})
	}
}

func BenchmarkCommentEditorSaveMaximumBody(b *testing.B) {
	body := commentEditorBenchmarkBody(b, int(change.MaxCommentBodyBytes)-1)
	model, coordinator := newTestModel(b, map[string]string{"alpha.txt": "changed\n"})
	baseReview := model.review
	b.ReportAllocs()

	for b.Loop() {
		b.StopTimer()
		prepareCommentEditorBenchmark(b, model, baseReview, body)
		b.StartTimer()
		model.saveComment()
		b.StopTimer()

		operation := model.review.CommentMutationOperation()
		if model.mode != ModeNormal || operation == 0 ||
			model.review.CommentMutationKind() != change.CommentMutationSave {
			b.Fatalf(
				"save mode/operation/kind = %v/%d/%v",
				model.mode,
				operation,
				model.review.CommentMutationKind(),
			)
		}
		awaitCommentEditorBenchmarkSave(b, coordinator, operation)
		commentEditorBenchmarkOperationSink = operation
		b.StartTimer()
	}
}

func commentEditorBenchmarkBody(tb testing.TB, size int) string {
	tb.Helper()
	row := strings.Repeat("a", 64) + "\n"
	body := strings.Repeat(row, size/len(row)) + strings.Repeat("a", size%len(row))
	if len(body) != size || body == "" || body[len(body)-1] == '\n' {
		tb.Fatalf("invalid comment editor benchmark fixture size %d", size)
	}
	return body[:len(body)-1] + "z"
}

func commentEditorBenchmarkPatternBody(tb testing.TB, pattern string) string {
	tb.Helper()
	const size = int(change.MaxCommentBodyBytes) - 1
	if size < 1 || pattern == "" || len(pattern) > size-1 {
		tb.Fatalf("invalid patterned fixture size/pattern = %d/%d", size, len(pattern))
	}
	prefixBytes := size - 1
	body := strings.Repeat(pattern, prefixBytes/len(pattern)) +
		strings.Repeat("a", prefixBytes%len(pattern)) + "z"
	if len(body) != size {
		tb.Fatalf("patterned fixture bytes = %d, want %d", len(body), size)
	}
	return body
}

func prepareCommentEditorBenchmark(
	tb testing.TB,
	model *Model,
	baseReview change.Review,
	body string,
) {
	tb.Helper()
	current, err := baseReview.BeginDraft(change.ReviewCommentTarget())
	if err != nil {
		tb.Fatal(err)
	}
	current, err = current.UpdateDraft(body)
	if err != nil {
		tb.Fatal(err)
	}
	model.review = current
	model.mode = ModeCommentEdit
	model.commentEditor.Reset()
	model.commentEditor.SetWidth(commentEditorBenchmarkWidth)
	model.commentEditor.SetHeight(commentEditorBenchmarkHeight)
	model.commentEditor.SetValue(body)
	model.commentEditor.MoveToEnd()
	_, _ = model.commentEditor.Update(
		tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}),
	)
	assertCommentEditorBenchmarkState(tb, model, body)
}

func assertCommentEditorBenchmarkState(tb testing.TB, model *Model, body string) {
	tb.Helper()
	draft, ok := model.review.Draft()
	wantLine := strings.Count(body, "\n")
	wantColumn := len(body) - strings.LastIndex(body, "\n") - 1
	if !ok || model.commentEditor.Value() != body || draft.Body() != body ||
		model.commentEditor.Width() <= 0 ||
		model.commentEditor.Width() > commentEditorBenchmarkWidth ||
		model.commentEditor.Height() != commentEditorBenchmarkHeight ||
		model.commentEditor.Line() != wantLine || model.commentEditor.Column() != wantColumn {
		tb.Fatalf(
			"fixture = editor/draft %d/%d bytes, content size %dx%d, cursor %d:%d, want %d/%d bytes, outer width <=%d, height %d, cursor %d:%d, active %t",
			len(model.commentEditor.Value()),
			len(draft.Body()),
			model.commentEditor.Width(),
			model.commentEditor.Height(),
			model.commentEditor.Line(),
			model.commentEditor.Column(),
			len(body),
			len(body),
			commentEditorBenchmarkWidth,
			commentEditorBenchmarkHeight,
			wantLine,
			wantColumn,
			ok,
		)
	}
}

func awaitCommentEditorBenchmarkSave(
	tb testing.TB,
	coordinator *Coordinator,
	operation change.OperationID,
) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(tb.Context(), commentEditorBenchmarkOperationDeadline)
	defer cancel()
	for {
		if state := coordinator.WaitReady(ctx); state != ReadyAvailable {
			tb.Fatalf("save operation %d readiness = %v", operation, state)
		}
		outcome, ok := coordinator.ClaimNext()
		if !ok {
			continue
		}
		if outcome.operationID() != operation {
			continue
		}
		mutation, mutationOK := outcome.(commentMutationOutcome)
		if !mutationOK || mutation.ID != operation {
			tb.Fatalf("save outcome = %T for operation %d", outcome, operation)
		}
		return
	}
}
