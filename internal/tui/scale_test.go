package tui

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/change"
)

const (
	appScaleCommentCount = 512
	appScaleRenderWidth  = 180
)

var (
	benchmarkScaleReview change.Review
	benchmarkScaleIndex  WorkspaceIndexOutcome
	benchmarkScaleView   tea.View
	benchmarkScaleText   string
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

func BenchmarkRenderBoundedDialogs(b *testing.B) {
	sizes := []TerminalSize{
		{Width: 100, Height: 30, Known: true},
		{Width: maxRenderWidth, Height: maxRenderHeight, Known: true},
	}
	for _, size := range sizes {
		name := fmt.Sprintf("%dx%d", size.Width, size.Height)
		b.Run("confirmation/"+name, func(b *testing.B) {
			model, _ := appScaleModel(b)
			model.confirmation = confirmation{kind: confirmationReload}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkScaleText = model.confirmationView(size.Width, size.Height)
			}
		})
		b.Run("export/"+name, func(b *testing.B) {
			model, _ := appScaleModel(b)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkScaleText = model.exportPickerView(size.Width, size.Height)
			}
		})
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
			if testCase.view == DiffViewSideBySide &&
				model.currentWorkspaceLayout().sideBySideFallback {
				b.Fatal("side-by-side scale benchmark fell back to unified rendering")
			}
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
	model.focus = FocusFileTree

	model.activateSelection()

	if active, ok := model.review.ActiveFile(); !ok || active != next {
		t.Fatalf("active file = %d, %v; want %d", active, ok, next)
	}
	if state := model.review.Patch(previous); state.Status != change.PatchUnavailable {
		t.Fatalf("prior patch status = %d, want unavailable", state.Status)
	}
	if retained := model.review.RetainedPatchBytes(); retained != 0 {
		t.Fatalf("retained patch bytes after activation = %d, want 0", retained)
	}
	if accepted := coordinator.Stats().Accepted; accepted != 1 {
		t.Fatalf("accepted patch requests = %d, want only the activated file", accepted)
	}
}

func TestScaleViewMaterializesOnlyViewportPresentationSlices(t *testing.T) {
	model, _ := appScaleModel(t)
	width, height := model.renderSize()
	layout := model.workspaceLayout(width, height)
	bodyHeight := height - 2
	styles := newSemanticStyles(model.settings)
	model.settings.wrap = false

	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("scale review has no active file")
	}
	patch := model.review.Patch(active)
	demand := model.patchLineDemand(layout.diffWidth, bodyHeight)
	if demand.count != uint32(bodyHeight+viewportOverscan) {
		t.Fatalf("patch demand = %d lines, want viewport plus overscan", demand.count)
	}
	if demand.bytes != max(int64(layout.diffWidth)*8, int64(4_096)) {
		t.Fatalf("patch byte demand = %d, want clipped-line bound", demand.bytes)
	}
	if patch.Lines <= demand.count+1 {
		t.Fatalf("scale patch = %d lines, need more than demand %d", patch.Lines, demand.count)
	}
	model.diffTop = patch.Lines - demand.count - 1
	model.diffCursor = model.diffTop
	model.commentCursor = model.commentRowCount() - 1
	tree := model.currentFileTree()
	lastTreeIndex, ok := tree.lastIndex()
	if !ok {
		t.Fatal("scale tree has no final projected node")
	}
	model.treeCursor = tree.nodes[lastTreeIndex].identity

	treeLines, treeRows := model.fileTreeLines(layout.treeWidth, bodyHeight, styles)
	diffLines, diffRows := model.diffLines(layout.diffWidth, bodyHeight, styles)
	commentLines, commentRows := model.commentLines(layout.commentsWidth, bodyHeight, styles)
	for name, count := range map[string]int{
		"tree lines":    len(treeLines),
		"tree rows":     len(treeRows),
		"diff lines":    len(diffLines),
		"diff rows":     len(diffRows),
		"comment lines": len(commentLines),
		"comment rows":  len(commentRows),
	} {
		if count > bodyHeight {
			t.Fatalf("%s materialized %d entries for a %d-row viewport", name, count, bodyHeight)
		}
	}
	if len(treeRows) != bodyHeight || !slices.Contains(treeRows, model.treeCursor) {
		t.Fatalf("deep tree window = %d rows, cursor present %t", len(treeRows), slices.Contains(treeRows, model.treeCursor))
	}
	if len(diffRows) == 0 || diffRows[0] != model.diffTop {
		t.Fatalf("deep diff window starts at %v, want %d", diffRows, model.diffTop)
	}
	if len(commentRows) != bodyHeight || !slices.Contains(commentRows, model.commentCursor) {
		t.Fatalf(
			"deep comment window = %d rows, cursor present %t",
			len(commentRows),
			slices.Contains(commentRows, model.commentCursor),
		)
	}
	for _, test := range []struct {
		name  string
		view  DiffView
		width int
		wrap  bool
	}{
		{name: "unified/no-wrap", view: DiffViewUnified, width: layout.diffWidth},
		{name: "unified/wrap", view: DiffViewUnified, width: layout.diffWidth, wrap: true},
		{name: "side-by-side/no-wrap", view: DiffViewSideBySide, width: sideBySideContentWidth + diffCommentAnchorColumns},
		{name: "side-by-side/wrap", view: DiffViewSideBySide, width: sideBySideContentWidth + diffCommentAnchorColumns, wrap: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			model.settings.diffView = test.view
			model.settings.wrap = test.wrap
			demand := model.patchLineDemand(test.width, bodyHeight)
			if demand.count != uint32(bodyHeight+viewportOverscan) {
				t.Fatalf("patch demand = %d lines, want viewport plus overscan", demand.count)
			}
			wantBytes := max(int64(test.width)*8, int64(4_096))
			if test.wrap {
				wantBytes = max(
					int64(test.width)*int64(bodyHeight+viewportOverscan)*4,
					wantBytes,
				)
			}
			if demand.bytes != wantBytes {
				t.Fatalf("patch byte demand = %d, want %d", demand.bytes, wantBytes)
			}
			lines, rows := model.diffLines(test.width, bodyHeight, styles)
			if len(lines) > bodyHeight || len(rows) > bodyHeight {
				t.Fatalf(
					"rendered %d lines and %d mappings for %d-row viewport",
					len(lines),
					len(rows),
					bodyHeight,
				)
			}
			if len(rows) == 0 || rows[0] != model.diffTop {
				t.Fatalf("deep diff window starts at %v, want %d", rows, model.diffTop)
			}
		})
	}
	view := model.View()
	if rows := strings.Count(view.Content, "\n") + 1; rows != height {
		t.Fatalf("scale view rendered %d rows, want %d", rows, height)
	}
	if bytes := len(view.Content); bytes > width*height*64 {
		t.Fatalf("scale view retained %d presentation bytes for %dx%d", bytes, width, height)
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
	model.index = index.Index
	model.allFileTree = index.FileTree
	model.visibleFileTree = index.FileTree.withProjection(index.VisibleProjection)
	model.collapsedTree = index.Collapsed
	model.notice = nil
	model.commentPaneVisibility = paneVisibilityShown
	model.focus = FocusDiff
	model.size = TerminalSize{Width: appScaleRenderWidth, Height: 30, Known: true}
	model.selectFileOrder()
	return model, coordinator
}

func appScaleReview(
	tb testing.TB,
	snapshot change.Snapshot,
	files []change.FileID,
	patch []byte,
) change.Review {
	tb.Helper()
	current, err := change.NewReview(testReviewRequest(snapshot))
	if err != nil {
		tb.Fatal(err)
	}
	pending, request, err := current.RequestPatch(files[0], change.WholeFileContext())
	if err != nil {
		tb.Fatal(err)
	}
	prepared, err := change.TakePatch(bytes.Clone(patch))
	if err != nil {
		tb.Fatal(err)
	}
	current, err = pending.ApplyPatch(change.PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    change.PatchReady,
		Prepared:  prepared,
	})
	if err != nil {
		tb.Fatal(err)
	}
	for index := range appScaleCommentCount {
		target, err := change.FileCommentTarget(files[index%len(files)])
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
