package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jacoelho/rvw/internal/review"
)

func TestWorkspacePanelTogglesSwapAtNarrowWidthsAndSplitWhenFeasible(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: treeDiffMinimumWidth - 1, Height: minimumHeight})
	if layout := model.workspaceLayout(treeDiffMinimumWidth-1, minimumHeight); layout.kind != workspaceLayoutDiffOnly {
		t.Fatalf("initial narrow layout = %+v, want diff only", layout)
	}

	model.toggleWorkspacePane(workspacePaneFiles)
	if layout := model.workspaceLayout(treeDiffMinimumWidth-1, minimumHeight); layout.kind != workspaceLayoutFilesOnly || layout.treeWidth != treeDiffMinimumWidth-1 {
		t.Fatalf("narrow file toggle layout = %+v, want full-width files", layout)
	}
	model.activateSelection()
	if layout := model.workspaceLayout(treeDiffMinimumWidth-1, minimumHeight); layout.kind != workspaceLayoutDiffOnly || model.focus != FocusDiff {
		t.Fatalf("file activation layout = %+v focus=%v, want diff", layout, model.focus)
	}

	_, _ = model.Update(tea.WindowSizeMsg{Width: treeDiffMinimumWidth, Height: minimumHeight})
	if layout := model.workspaceLayout(treeDiffMinimumWidth, minimumHeight); layout.kind != workspaceLayoutSplit || layout.treeWidth != fileTreeMinimumWidth || layout.diffWidth != unifiedDiffContentWidth {
		t.Fatalf("tree breakpoint layout = %+v", layout)
	}

	_, _ = model.Update(tea.WindowSizeMsg{Width: commentDiffMinimumWidth - 1, Height: minimumHeight})
	model.toggleWorkspacePane(workspacePaneComments)
	if layout := model.workspaceLayout(commentDiffMinimumWidth-1, minimumHeight); layout.kind != workspaceLayoutCommentsOnly {
		t.Fatalf("narrow comments layout = %+v, want exclusive comments", layout)
	}
	_, _ = model.Update(tea.WindowSizeMsg{Width: commentDiffMinimumWidth, Height: minimumHeight})
	if layout := model.workspaceLayout(commentDiffMinimumWidth, minimumHeight); layout.kind != workspaceLayoutSplit || layout.commentsWidth != commentMinimumWidth || layout.diffWidth != unifiedDiffContentWidth {
		t.Fatalf("comments breakpoint layout = %+v", layout)
	}
	_, _ = model.Update(tea.WindowSizeMsg{Width: allPanesMinimumWidth, Height: minimumHeight})
	if layout := model.workspaceLayout(allPanesMinimumWidth, minimumHeight); layout.treeWidth != fileTreeMinimumWidth || layout.commentsWidth != commentMinimumWidth || layout.diffWidth != unifiedDiffContentWidth {
		t.Fatalf("all-pane breakpoint layout = %+v", layout)
	}
}

func TestWideLayoutRespectsDisabledCommentsPreference(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	layout := model.workspaceLayout(wideWidth, 30)
	if layout.treeWidth == 0 || layout.commentsWidth != 0 {
		t.Fatalf("default wide layout = %+v, want files and no comments", layout)
	}
}

func TestSideBySideFallbackOwnsANonSelectableDiffRow(t *testing.T) {
	model, _ := modelWithPreparedPatch(t, "@@ -1 +1 @@\n-old\n+new\n")
	model.settings.diffView = DiffViewSideBySide
	lines, rows := model.diffLines(sideBySideContentWidth+diffCommentAnchorColumns-1, 5, newSemanticStyles(model.settings))
	if len(lines) == 0 || !strings.Contains(ansi.Strip(lines[0]), "Side-by-side needs 102 diff columns") {
		t.Fatalf("fallback banner = %q", lines)
	}
	if len(rows) == 0 || rows[0].selectable {
		t.Fatalf("fallback row mapping = %+v, want non-selectable banner", rows)
	}
	for _, row := range rows[1:] {
		if row.selectable && row.line == 0 {
			return
		}
	}
	t.Fatal("fallback omitted selectable patch row zero after its banner")
}

func TestSideBySideWidthsReallocateOnlyPureAdditionsAndDeletions(t *testing.T) {
	const width = 120
	for _, test := range []struct {
		kind        review.ChangeKind
		left, right int
	}{
		{review.ChangeAdded, 39, 80},
		{review.ChangeDeleted, 80, 39},
		{review.ChangeModified, 59, 60},
		{review.ChangeRenamed, 59, 60},
	} {
		left, right := sideBySideWidths(test.kind, width)
		if left != test.left || right != test.right {
			t.Errorf("%s split = %d/%d, want %d/%d", test.kind, left, right, test.left, test.right)
		}
	}
}

func TestNoColorCursorLineUsesReverseVideoWithoutFaintText(t *testing.T) {
	model, _ := modelWithPreparedPatch(t, "@@ -1 +1 @@\n-old\n+new\n")
	styles := newSemanticStyles(model.settings)
	if styles.muted.GetFaint() || styles.hunk.GetFaint() || styles.pending.GetFaint() || styles.comment.GetFaint() {
		t.Fatal("no-color semantic styles retain faint rendering")
	}
	lines, rows := model.diffLines(compactWidth, 5, styles)
	index := rowIndex(t, rows, model.diffCursor)
	if !strings.Contains(lines[index], "\x1b[7m") {
		t.Fatalf("cursor row lacks reverse-video emphasis: %q", lines[index])
	}
	if !strings.Contains(lines[index], "\x1b[m\x1b[7m") {
		t.Fatalf("cursor row does not restore reverse video after nested styles: %q", lines[index])
	}
	warning := ansi.Strip(renderNoticeLine(Notice{Kind: NoticeWarning, Text: "warning"}, 20, styles))
	errorLine := ansi.Strip(renderNoticeLine(Notice{Kind: NoticeError, Text: "error"}, 20, styles))
	if !strings.HasPrefix(warning, "⚠ warning") || !strings.HasPrefix(errorLine, "✗ error") {
		t.Fatalf("no-color severity labels = %q and %q", warning, errorLine)
	}
}

func TestTitlePrioritizesPathAndReviewedProgress(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	model.review, _ = model.review.ToggleFileReviewed(active)
	title := ansi.Strip(model.titleLine(normalWidth, newSemanticStyles(model.settings)))
	if !strings.Contains(title, "alpha.txt") || !strings.Contains(title, "1 files") || strings.Contains(title, "source:") {
		t.Fatalf("title hierarchy = %q", title)
	}
	if clipped := safeMiddleClip("a/very/long/path/distinguishing-name.go", 18); !strings.HasSuffix(clipped, "hing-name.go") || !strings.Contains(clipped, "…") {
		t.Fatalf("middle-clipped path = %q", clipped)
	}
}

func TestNoticeOwnsFooterUntilNextHandledActionAndRemainsInStatusDetails(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	model.notice = &Notice{Kind: NoticeWarning, Text: "action unavailable"}
	styles := newSemanticStyles(model.settings)
	footer := ansi.Strip(model.statusLine(normalWidth, styles))
	if !strings.HasPrefix(footer, "⚠ action unavailable") || strings.Contains(footer, "work ") {
		t.Fatalf("notice footer = %q", footer)
	}
	_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})
	if model.notice == nil {
		t.Fatal("resize acknowledged a notice")
	}
	model.applyAction(KeyActionToggleWrap, 1)
	if model.notice != nil || model.lastNotice == nil {
		t.Fatalf("notice lifecycle current=%+v last=%+v", model.notice, model.lastNotice)
	}
	if details := strings.Join(model.statusFactLines(), "\n"); !strings.Contains(details, "⚠ action unavailable") {
		t.Fatalf("status details omitted acknowledged notice:\n%s", details)
	}
}

func TestFindAndConfirmationHintsUseEffectiveBindings(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	setModelKeybindings(t, model,
		Keybinding{Context: KeyContextSearch, Chord: "ctrl-n", Action: KeyActionNextSearchScope},
		Keybinding{Context: KeyContextSearch, Chord: "k", Action: KeyActionMoveUp},
		Keybinding{Context: KeyContextSearch, Chord: "j", Action: KeyActionMoveDown},
		Keybinding{Context: KeyContextSearch, Chord: "enter", Action: KeyActionAccept},
		Keybinding{Context: KeyContextSearch, Chord: "esc", Action: KeyActionCancel},
		Keybinding{Context: KeyContextConfirm, Chord: "ctrl-y", Action: KeyActionConfirmYes},
		Keybinding{Context: KeyContextConfirm, Chord: "n", Action: KeyActionConfirmNo},
		Keybinding{Context: KeyContextConfirm, Chord: "esc", Action: KeyActionCancel},
	)
	model.find = findPalette{
		candidates: []review.FileID{0},
		phase:      findReady,
		scope:      findFiles,
	}
	find, layout, _ := model.findModalView(80, 20, newSemanticStyles(model.settings))
	plainFind := ansi.Strip(find)
	if !strings.Contains(plainFind, "ctrl-n scope") || !strings.Contains(plainFind, "enter select") || strings.Contains(plainFind, "Tab scope") || !strings.Contains(plainFind, "╭") {
		t.Fatalf("find hints do not follow effective bindings:\n%s", plainFind)
	}
	if lipgloss.Width(find) != layout.width || lipgloss.Height(find) != layout.height {
		t.Fatalf("find panel size = %dx%d, want %dx%d", lipgloss.Width(find), lipgloss.Height(find), layout.width, layout.height)
	}
	model.confirmation = confirmation{kind: confirmationQuit}
	confirmModal, confirmLayout := model.confirmationModalView(60, 12, newSemanticStyles(model.settings))
	confirm := ansi.Strip(confirmModal)
	if !strings.Contains(confirm, "ctrl-y Yes") || !strings.Contains(confirm, "n No") || !strings.Contains(confirm, "esc Cancel") || !confirmLayout.bordered {
		t.Fatalf("confirmation panel does not use effective bindings:\n%s", confirm)
	}
}

func TestFindReadyStatusIncludesCursorPosition(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"a.txt": "changed\n", "b.txt": "changed\n", "c.txt": "changed\n"})
	model.find = findPalette{
		candidates: []review.FileID{0, 1, 2},
		cursor:     1,
		phase:      findReady,
		scope:      findFiles,
	}
	if status := model.findStatusText(); status != "2 of 3" {
		t.Fatalf("ready find status = %q", status)
	}
}
