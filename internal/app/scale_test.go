package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/review"
)

const appScaleCommentCount = 512

var (
	benchmarkScaleReview review.Review
	benchmarkScaleIndex  WorkspaceIndexOutcome
	benchmarkScaleView   tea.View
	benchmarkWrappedLine []string
)

func BenchmarkConstructReviewAtScale(b *testing.B) {
	patch := appScalePatchText()
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		snapshot, files := largeTreeFixture(b)
		current := appScaleReview(b, snapshot, files, patch)
		index, err := buildWorkspaceIndex(context.Background(), snapshot)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkScaleReview = current
		benchmarkScaleIndex = index
	}
}

func BenchmarkRenderReviewAtScale(b *testing.B) {
	model, _ := appScaleModel(b)
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		benchmarkScaleView = model.View()
	}
}

func BenchmarkRenderReviewAtMaxGeometry(b *testing.B) {
	model, _ := appScaleModel(b)
	model.size = TerminalSize{Width: maxRenderWidth, Height: maxRenderHeight, Known: true}
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		benchmarkScaleView = model.View()
	}
}

func BenchmarkRenderReviewModesAtScale(b *testing.B) {
	testCases := []struct {
		name string
		view DiffView
		wrap bool
	}{
		{name: "unified/wrap", view: DiffViewUnified, wrap: true},
		{name: "unified/no-wrap", view: DiffViewUnified},
		{name: "side-by-side/wrap", view: DiffViewSideBySide, wrap: true},
		{name: "side-by-side/no-wrap", view: DiffViewSideBySide},
	}
	for _, testCase := range testCases {
		b.Run(testCase.name, func(b *testing.B) {
			model, _ := appScaleModel(b)
			model.settings.diffView = testCase.view
			model.settings.wrap = testCase.wrap
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkScaleView = model.View()
			}
		})
	}
}

func BenchmarkWrappedLineCases(b *testing.B) {
	testCases := []struct {
		name  string
		value string
		width int
	}{
		{name: "fitting-ascii", value: "func retainedViewport() error", width: 80},
		{
			name: "styled-unicode",
			value: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render(
				"résumé λ-value",
			),
			width: 80,
		},
		{name: "ansi", value: "\x1b[31mred text\x1b[0m", width: 80},
		{name: "wide-grapheme", value: "界🙂‍↕️界🙂‍↕️", width: 80},
		{name: "long-overflow", value: strings.Repeat("overflow ", 64), width: 40},
	}
	for _, testCase := range testCases {
		b.Run(testCase.name, func(b *testing.B) {
			model := &Model{}
			model.settings.wrap = true
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkWrappedLine = model.wrappedLine(testCase.value, testCase.width)
			}
		})
	}
}

func BenchmarkNavigateReviewAtScale(b *testing.B) {
	model, _ := appScaleModel(b)
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		model.moveFile(1)
		model.moveFile(-1)
	}
}

func BenchmarkNavigateAndRenderReviewAtScale(b *testing.B) {
	model, _ := appScaleModel(b)
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		model.moveFile(1)
		benchmarkScaleView = model.View()
		model.moveFile(-1)
		benchmarkScaleView = model.View()
	}
}

func BenchmarkConstructReviewStagesAtScale(b *testing.B) {
	patch := appScalePatchText()
	b.Run("snapshot", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			snapshot, _ := largeTreeFixture(b)
			_ = snapshot
		}
	})
	b.Run("review-state", func(b *testing.B) {
		snapshot, files := largeTreeFixture(b)
		b.ReportAllocs()
		for b.Loop() {
			benchmarkScaleReview = appScaleReview(b, snapshot, files, patch)
		}
	})
	b.Run("workspace-index", func(b *testing.B) {
		snapshot, _ := largeTreeFixture(b)
		b.ReportAllocs()
		for b.Loop() {
			index, err := buildWorkspaceIndex(context.Background(), snapshot)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkScaleIndex = index
		}
	})
}

func TestActivatingFileDropsPriorLargePatchAtScale(t *testing.T) {
	model, coordinator := appScaleModel(t)
	previous, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("scale review has no active file")
	}
	next := model.fileOrder[1]
	current, err := model.review.SelectFile(next)
	if err != nil {
		t.Fatal(err)
	}
	model.review = current

	model.activateSelection()

	if active, ok := model.review.ActiveFile(); !ok || active != next {
		t.Fatalf("active file = %d, %v; want %d", active, ok, next)
	}
	if state := model.review.Patch(previous); state.Status != review.PatchUnavailable {
		t.Fatalf("prior patch status = %d, want unavailable", state.Status)
	}
	if retained := model.review.RetainedPatchBytes(); retained != 0 {
		t.Fatalf("retained patch bytes after activation = %d, want 0", retained)
	}
	if accepted := coordinator.Stats().Accepted; accepted != 1 {
		t.Fatalf("accepted patch requests = %d, want only the activated file", accepted)
	}
}

func appScaleModel(tb testing.TB) (*Model, *Coordinator) {
	tb.Helper()
	model, coordinator := newTestModel(tb, map[string]string{"seed.go": "changed\n"})
	if model.foreground.patchCount() != 0 {
		waitContext, cancelWait := context.WithTimeout(tb.Context(), 5*time.Second)
		if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
			cancelWait()
			tb.Fatalf("initial patch readiness = %d, want available", state)
		}
		cancelWait()
		_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	}

	snapshot, files := largeTreeFixture(tb)
	current := appScaleReview(tb, snapshot, files, appScalePatchText())
	index, err := buildWorkspaceIndex(context.Background(), snapshot)
	if err != nil {
		tb.Fatal(err)
	}
	model.review = current
	model.allFileOrder = index.AllFiles
	model.visibleFileOrder = index.VisibleFiles
	model.allFileTree = index.AllFileTree
	model.visibleFileTree = index.VisibleFileTree
	model.pathFiles = index.PathFiles
	model.ambiguousPaths = index.AmbiguousPaths
	model.notice = nil
	model.commentsVisible = true
	model.focus = FocusDiff
	model.size = TerminalSize{Width: wideWidth, Height: 30, Known: true}
	model.selectFileOrder()
	return model, coordinator
}

func appScaleReview(
	tb testing.TB,
	snapshot review.Snapshot,
	files []review.FileID,
	patch []byte,
) review.Review {
	tb.Helper()
	current, err := review.NewReview(snapshot)
	if err != nil {
		tb.Fatal(err)
	}
	pending, request, err := current.RequestPatch(files[0], review.WholeFileContext())
	if err != nil {
		tb.Fatal(err)
	}
	prepared, err := review.TakePatch(bytes.Clone(patch))
	if err != nil {
		tb.Fatal(err)
	}
	current, err = pending.ApplyPatch(review.PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    review.PatchReady,
		Prepared:  prepared,
	})
	if err != nil {
		tb.Fatal(err)
	}
	for index := range appScaleCommentCount {
		target, err := review.FileCommentTarget(files[index%len(files)])
		if err != nil {
			tb.Fatal(err)
		}
		current, err = current.BeginDraft(target)
		if err != nil {
			tb.Fatal(err)
		}
		current, err = current.UpdateDraft(fmt.Sprintf(
			"Comment %04d exercises the combined scale fixture.",
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

func appScalePatchText() []byte {
	var patch strings.Builder
	fmt.Fprintf(
		&patch,
		"@@ -1,%d +1,%d @@\n",
		largeTreeFileLines,
		largeTreeFileLines,
	)
	for index := range largeTreeFileLines {
		fmt.Fprintf(
			&patch,
			" line %05d: func retainedViewport() { value := %d }\n",
			index+1,
			index,
		)
	}
	return []byte(patch.String())
}
