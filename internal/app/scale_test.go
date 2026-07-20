package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/review"
)

const appScaleCommentCount = 512

var (
	benchmarkScaleReview review.Review
	benchmarkScaleIndex  WorkspaceIndexOutcome
	benchmarkScaleView   tea.View
)

func BenchmarkConstructReviewAtScale(b *testing.B) {
	patch := appScalePatchText()
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		snapshot, files := largeTreeFixture(b, largeTreeFileCount)
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

func BenchmarkNavigateReviewAtScale(b *testing.B) {
	model, _ := appScaleModel(b)
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		model.moveFile(1)
		model.moveFile(-1)
	}
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
	if len(model.patchOperations) != 0 {
		waitContext, cancelWait := context.WithTimeout(tb.Context(), 5*time.Second)
		if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
			cancelWait()
			tb.Fatalf("initial patch readiness = %d, want available", state)
		}
		cancelWait()
		_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	}

	snapshot, files := largeTreeFixture(tb, largeTreeFileCount)
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
	kind, err := review.NewCommentKind("issue")
	if err != nil {
		tb.Fatal(err)
	}
	current, err := review.NewReview(snapshot, []review.CommentKind{kind})
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
		current, err = current.BeginDraft(target, kind)
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
