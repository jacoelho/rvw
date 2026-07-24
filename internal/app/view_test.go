package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
)

func TestMinimumSizeOwnsInput(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	model.mode = ModeCommentEdit
	_, _ = model.Update(tea.WindowSizeMsg{Width: minimumWidth - 1, Height: minimumHeight})
	if cursor := model.View().Cursor; cursor != nil {
		t.Fatalf("minimum-size view exposed hidden editor cursor: %+v", cursor)
	}
	model.mode = ModeNormal
	before, _ := model.review.SelectedFile()

	updated, command := model.Update(keyMessage("j"))
	if updated != model || command != nil {
		t.Fatal("minimum-size ignored action changed owner or returned work")
	}
	after, _ := model.review.SelectedFile()
	if after != before {
		t.Fatalf("minimum-size move changed selection from %d to %d", before, after)
	}

	_, command = model.Update(keyMessage("q"))
	if command == nil {
		t.Fatal("minimum-size quit did not return tea.Quit")
	}
	if message := command(); message != (tea.QuitMsg{}) {
		t.Fatalf("quit command returned %T, want tea.QuitMsg", message)
	}
}

func TestCommentEditorDockKeepsCodeVisibleWithoutEscapedWidgetStyles(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})

	beginTestLineComment(t, model)
	view := model.View()
	plain := ansi.Strip(view.Content)
	if !strings.Contains(plain, "changed") {
		t.Fatalf("comment editor hid the selected code:\n%s", plain)
	}
	if !strings.Contains(plain, "Comment ·") || !strings.Contains(plain, "DIFF · alpha.txt · COMMENT") {
		t.Fatalf("comment editor omitted its visible focus state:\n%s", plain)
	}
	if strings.Contains(plain, "enter activate") || !strings.Contains(plain, "comment editor owns input") {
		t.Fatalf("comment editor exposed conflicting workspace help:\n%s", plain)
	}
	if strings.Contains(plain, `\x1B[`) {
		t.Fatalf("comment editor exposed escaped widget styling:\n%s", plain)
	}
	if lines := strings.Count(view.Content, "\n") + 1; lines != 24 {
		t.Fatalf("comment editor rendered %d lines, want 24", lines)
	}
}

func TestHelpViewPreservesWidgetStyles(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})
	model.overlay = OverlayHelp
	model.help.Styles.FullKey = lipgloss.NewStyle().Bold(true)

	view := model.View()
	plain := ansi.Strip(view.Content)
	if strings.Contains(plain, `\x1B[`) {
		t.Fatalf("help exposed escaped widget styling:\n%s", plain)
	}
	if !strings.Contains(view.Content, "\x1b[") {
		t.Fatalf("help discarded widget styling:\n%q", view.Content)
	}
}

func TestFindPalettePreservesWidgetStyles(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})
	_ = model.openFind(findCurrent)

	view := model.View()
	plain := ansi.Strip(view.Content)
	if strings.Contains(plain, `\x1B[`) {
		t.Fatalf("prompt exposed escaped widget styling:\n%s", plain)
	}
	if !strings.Contains(view.Content, "\x1b[") {
		t.Fatalf("prompt discarded widget styling:\n%q", view.Content)
	}
}

func TestCommandPromptUsesFocusedWorkspaceFooter(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})
	model.commandInput.SetVirtualCursor(false)
	_ = model.beginCommandPrompt()
	model.commandInput.SetValue("filter src")

	view := model.View()
	plain := ansi.Strip(view.Content)
	lines := strings.Split(plain, "\n")
	if len(lines) != 24 {
		t.Fatalf("command prompt rendered %d lines, want 24", len(lines))
	}
	if !strings.Contains(lines[0], "DIFF · alpha.txt · COMMAND") ||
		!strings.Contains(lines[0], "alpha.txt") {
		t.Fatalf("command prompt hid or misidentified the workspace:\n%s", plain)
	}
	if !strings.Contains(lines[len(lines)-1], ": filter src") {
		t.Fatalf("command footer is absent:\n%s", plain)
	}
	if strings.Contains(plain, "Command\n") {
		t.Fatalf("command prompt retained the full-screen heading:\n%s", plain)
	}
	if view.Cursor == nil || view.Cursor.Y != 23 {
		t.Fatalf("command cursor = %+v, want footer row 23", view.Cursor)
	}
	if !strings.Contains(view.Content, "\x1b[") {
		t.Fatalf("command footer discarded widget styling:\n%q", view.Content)
	}
}

func TestWorkspaceHeadersIdentifyTheFocusedPaneWithoutChangingGeometry(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})

	model.focus = FocusDiff
	diffHeader := strings.Split(ansi.Strip(model.View().Content), "\n")[0]
	if !strings.Contains(diffHeader, "  FILES · 1") ||
		!strings.Contains(diffHeader, "▌ DIFF · alpha.txt") {
		t.Fatalf("diff-focused header = %q", diffHeader)
	}

	model.focus = FocusFileTree
	treeHeader := strings.Split(ansi.Strip(model.View().Content), "\n")[0]
	if !strings.Contains(treeHeader, "▌ FILES · 1") ||
		!strings.Contains(treeHeader, "  DIFF · alpha.txt") {
		t.Fatalf("file-focused header = %q", treeHeader)
	}
	if lipgloss.Width(diffHeader) != normalWidth || lipgloss.Width(treeHeader) != normalWidth {
		t.Fatalf(
			"header widths = diff %d tree %d, want %d",
			lipgloss.Width(diffHeader),
			lipgloss.Width(treeHeader),
			normalWidth,
		)
	}

	model.mode = ModeVisual
	model.focus = FocusDiff
	visualHeader := strings.Split(ansi.Strip(model.View().Content), "\n")[0]
	if !strings.Contains(visualHeader, "▌ DIFF · alpha.txt") {
		t.Fatalf("visual-mode header = %q", visualHeader)
	}
}

func TestCommandPromptFooterTracksSupportedTerminalHeight(t *testing.T) {
	for _, size := range []TerminalSize{
		{Width: minimumWidth, Height: minimumHeight, Known: true},
		{Width: compactWidth, Height: 24, Known: true},
		{Width: wideWidth, Height: 30, Known: true},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			_, _ = model.Update(tea.WindowSizeMsg{Width: size.Width, Height: size.Height})
			model.commandInput.SetVirtualCursor(false)
			_ = model.beginCommandPrompt()

			view := model.View()
			lines := strings.Split(ansi.Strip(view.Content), "\n")
			if len(lines) != size.Height || !strings.HasPrefix(lines[len(lines)-1], ": ") {
				t.Fatalf("command footer at %dx%d:\n%s", size.Width, size.Height, view.Content)
			}
			if view.Cursor == nil || view.Cursor.Y != size.Height-1 {
				t.Fatalf("command cursor at %dx%d = %+v", size.Width, size.Height, view.Cursor)
			}
		})
	}
}

func TestCommentEditorDockPreservesWorkspaceAcrossHeightBoundary(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	beginTestLineComment(t, model)

	for _, height := range []int{minimumHeight, 10, 11, 12, 24, 30} {
		t.Run(fmt.Sprintf("height-%d", height), func(t *testing.T) {
			_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: height})
			view := model.View()
			plain := ansi.Strip(view.Content)
			if !strings.Contains(plain, "changed") || !strings.Contains(plain, "Comment ·") {
				t.Fatalf("height %d did not preserve code and editor:\n%s", height, plain)
			}
			if lines := strings.Count(view.Content, "\n") + 1; lines != height {
				t.Fatalf("height %d rendered %d lines", height, lines)
			}
			layout := newCommentEditorLayout(height)
			if layout.workspaceHeight < commentEditorWorkspaceHeight {
				t.Fatalf("height %d workspace height = %d", height, layout.workspaceHeight)
			}
			if layout.editorTop+layout.editorHeight != height {
				t.Fatalf("height %d editor ends at %d", height, layout.editorTop+layout.editorHeight)
			}
		})
	}
}

func TestFocusedCommentPanePreservesStyleSequences(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	target, err := review.FileCommentTarget(active)
	if err != nil {
		t.Fatal(err)
	}
	current, err := model.review.BeginDraft(target)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("comment body")
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = current.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	model.review = current

	styles := newSemanticStyles(model.settings)
	for _, test := range []struct {
		name   string
		marker string
		focus  FocusOwner
	}{
		{name: "unfocused", focus: FocusDiff, marker: "› "},
		{name: "focused", focus: FocusComments, marker: "▸ "},
	} {
		t.Run(test.name, func(t *testing.T) {
			model.focus = test.focus
			lines, _ := model.commentLines(40, 3, styles)
			if len(lines) != 1 {
				t.Fatalf("comment lines = %d, want 1", len(lines))
			}
			plain := ansi.Strip(lines[0])
			if strings.Contains(plain, `\x1B[`) {
				t.Fatalf("comment row exposed escaped styling: %q", plain)
			}
			if !strings.HasPrefix(plain, test.marker) || !strings.Contains(plain, "comment body") {
				t.Fatalf("comment row = %q", plain)
			}
		})
	}
}

func TestRemoteCommentRowPreservesRenderedText(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	file, ok := model.review.Snapshot().File(active)
	if !ok {
		t.Fatal("missing active file descriptor")
	}
	line, _ := review.NewLineNumber(2)
	comment, err := review.NewRemoteComment(review.RemoteCommentDescriptor{
		ID: "comment", Body: "remote body",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := review.NewRemoteThread(review.RemoteThreadDescriptor{
		ID: "thread", Path: file.IdentityPath(), Line: &line, Side: review.LineNew,
		Comments: []review.RemoteComment{comment},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := model.review.Snapshot().WithRemoteThreads([]review.RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = review.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	model.focus = FocusDiff

	lines, _ := model.commentLines(80, 3, newSemanticStyles(model.settings))
	if len(lines) != 1 {
		t.Fatalf("remote comment lines = %d, want 1", len(lines))
	}
	if got, want := ansi.Strip(lines[0]),
		"› [remote open] alpha.txt:2 — deleted user: remote body"; got != want {
		t.Fatalf("remote comment row = %q, want %q", got, want)
	}
}

func beginTestLineComment(t *testing.T, model *Model) {
	t.Helper()
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	state := model.review.Patch(active)
	for _, line := range model.review.PatchLines(active, 0, state.Lines, 4_096) {
		if line.Kind() != review.PatchLineAddition {
			continue
		}
		model.diffCursor = line.Number
		model.diffTop = line.Number
		_ = model.beginCursorComment()
		return
	}
	t.Fatal("fixture patch has no commentable addition")
}

func TestResizeClampsAndRestoresFocusForHiddenPanes(t *testing.T) {
	for _, focus := range []FocusOwner{FocusFileTree, FocusComments} {
		t.Run(fmt.Sprint(focus), func(t *testing.T) {
			model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
			model.focus = focus

			_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 18})
			if model.focus != FocusDiff || model.focusReturn != focus {
				t.Fatalf("compact focus = %v return = %v, want diff then %v", model.focus, model.focusReturn, focus)
			}

			_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
			if model.focus != focus || model.focusReturn != 0 {
				t.Fatalf("restored focus = %v return = %v, want %v then none", model.focus, model.focusReturn, focus)
			}
		})
	}
}

func TestMinimumSizeRetainsPendingConfirmation(t *testing.T) {
	for _, pending := range []confirmation{
		{kind: confirmationQuit},
		{kind: confirmationDeleteComment, comment: 1},
		{kind: confirmationReload},
	} {
		t.Run(fmt.Sprint(pending.kind), func(t *testing.T) {
			model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			model.confirmation = pending
			model.overlay = OverlayConfirm

			_, _ = model.Update(tea.WindowSizeMsg{Width: minimumWidth - 1, Height: minimumHeight})
			if model.keyContext() != keymap.ContextMinimumSize {
				t.Fatalf("key context = %q, want %q", model.keyContext(), keymap.ContextMinimumSize)
			}
			if content := model.View().Content; !strings.Contains(content, "decision pending") ||
				!strings.Contains(content, "resize to decide") || strings.Contains(content, "Confirmation") {
				t.Fatalf("minimum view did not own pending decision:\n%s", content)
			}

			for _, key := range []string{"y", "n", "esc", "q"} {
				_, command := model.Update(keyMessage(key))
				if command != nil {
					t.Fatalf("key %q returned a command while decision was blocked", key)
				}
				if model.overlay != OverlayConfirm || model.confirmation != pending {
					t.Fatalf("key %q changed pending confirmation to overlay=%v confirmation=%+v", key, model.overlay, model.confirmation)
				}
			}

			_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})
			if model.keyContext() != keymap.ContextConfirm || model.overlay != OverlayConfirm || model.confirmation != pending {
				t.Fatalf("resize did not restore confirmation: context=%q overlay=%v confirmation=%+v", model.keyContext(), model.overlay, model.confirmation)
			}
			if content := model.View().Content; !strings.Contains(content, "Confirmation") {
				t.Fatalf("restored view omitted confirmation:\n%s", content)
			}
		})
	}
}

func TestConfirmedQuitPreservesLastViewUntilTerminalRestoration(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})
	model.confirmation = confirmation{kind: confirmationQuit}
	model.overlay = OverlayConfirm
	before := model.View()

	command := model.applyConfirmationAction(keymap.ActionConfirmYes)
	if command == nil {
		t.Fatal("confirmed quit returned no command")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("confirmed quit did not return tea.Quit")
	}
	if !model.quitting {
		t.Fatal("confirmed quit did not enter quitting state")
	}
	after := model.View()
	if after.Content != before.Content || after.AltScreen != before.AltScreen ||
		after.Cursor != before.Cursor {
		t.Fatalf("confirmed quit changed the final view before restoration:\nbefore: %+v\nafter: %+v", before, after)
	}
}

func TestMinimumHeightPrioritizesPendingConfirmationWarning(t *testing.T) {
	for height := 1; height < minimumHeight; height++ {
		t.Run(fmt.Sprintf("height-%d", height), func(t *testing.T) {
			model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			model.confirmation = confirmation{kind: confirmationQuit}
			model.overlay = OverlayConfirm
			_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: height})

			content := model.View().Content
			if !strings.Contains(content, "decision pending") ||
				!strings.Contains(content, "resize to decide") {
				t.Fatalf("height %d hid pending-decision warning: %q", height, content)
			}
		})
	}
}

func TestWrappedDiffRetainsLongLineTailInUnifiedAndSideBySideViews(t *testing.T) {
	const tail = "TAIL-MARKER"
	longLine := strings.Repeat("segment ", 30) + tail + "\n"
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": longLine})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})

	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 24})
	if content := model.View().Content; !strings.Contains(content, tail) {
		t.Fatalf("wrapped unified view omitted long-line tail:\n%s", content)
	}
	model.settings.wrap = false
	if content := model.View().Content; strings.Contains(content, tail) {
		t.Fatalf("clipped unified view unexpectedly retained offscreen tail:\n%s", content)
	}

	model.settings.wrap = true
	model.settings.diffView = DiffViewSideBySide
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	if content := model.View().Content; !strings.Contains(content, tail) {
		t.Fatalf("wrapped side-by-side view omitted long-line tail:\n%s", content)
	}
}

func TestDiffRenderingPreservesUnicodeAndExpandsTabs(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "12\tcafé 界 e\u0301 👩‍💻\n",
	})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 24})

	content := model.View().Content
	if strings.Contains(content, `\u{0009}`) {
		t.Fatalf("diff rendered a tab as an escaped control:\n%s", content)
	}
	if !strings.Contains(content, "12  café 界 e\u0301 👩‍💻") {
		t.Fatalf("diff did not preserve printable Unicode or expand its tab:\n%s", content)
	}
}

func TestDiffLineNumberGuttersUsePatchDigitWidths(t *testing.T) {
	var contents strings.Builder
	for line := 1; line <= 1_000; line++ {
		fmt.Fprintf(&contents, "line-%04d\n", line)
	}
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": contents.String()})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	widths := newDiffLineNumberWidths(model.review.Patch(active))
	if widths.old != 1 || widths.new != 4 {
		t.Fatalf("line number widths = old %d new %d, want 1 and 4", widths.old, widths.new)
	}

	styles := newSemanticStyles(model.settings)
	model.settings.diffView = DiffViewUnified
	unified, _ := model.diffLines(compactWidth, 12, styles)
	unifiedRow := renderedLineContaining(t, unified, "+line-0001")
	if want := strings.Repeat(" ", 8) + "1  +line-0001"; !strings.HasPrefix(unifiedRow, want) {
		t.Fatalf("unified first source row = %q, want prefix %q", unifiedRow, want)
	}

	model.settings.diffView = DiffViewSideBySide
	sideBySide, _ := model.diffLines(normalWidth, 12, styles)
	sideRow := renderedLineContaining(t, sideBySide, "line-0001")
	_, right, ok := strings.Cut(sideRow, "│")
	if !ok || !strings.HasPrefix(right, "   1  line-0001") {
		t.Fatalf("side-by-side first source row = %q", sideRow)
	}
}

func TestCommentAnchorsRemainSideSpecificStableAndVisibleWithoutColor(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "original\nadded content that is deliberately long enough to wrap across rows\n",
	})
	waitForPatch(t, model, coordinator, 0)
	active, _ := model.review.ActiveFile()
	lineOne, _ := review.NewLineNumber(1)
	lineTwo, _ := review.NewLineNumber(2)
	for _, target := range []review.CommentTarget{
		mustLineTarget(t, active, review.LineOld, lineOne),
		mustLineTarget(t, active, review.LineNew, lineOne),
		mustLineTarget(t, active, review.LineNew, lineTwo),
	} {
		current, err := model.review.BeginDraft(target)
		if err != nil {
			t.Fatal(err)
		}
		current, err = current.UpdateDraft("anchor")
		if err != nil {
			t.Fatal(err)
		}
		current, _, err = current.SaveDraft()
		if err != nil {
			t.Fatal(err)
		}
		model.review = current
	}

	model.settings.colorEnabled = false
	styles := newSemanticStyles(model.settings)
	model.settings.diffView = DiffViewUnified
	model.settings.wrap = true
	unified, rows := model.diffLines(62, 20, styles)
	contextRow := renderedLineContaining(t, unified, "original")
	if strings.Count(contextRow, "●") != 2 || !strings.Contains(contextRow, "1● 1●") {
		t.Fatalf("unified context markers are not side-specific: %q", contextRow)
	}
	additionIndex := -1
	for index, row := range unified {
		if strings.Contains(ansi.Strip(row), "added content") {
			additionIndex = index
			break
		}
	}
	if additionIndex < 0 || strings.Count(ansi.Strip(unified[additionIndex]), "●") != 1 {
		t.Fatalf("unified addition marker is missing or duplicated: %q", unified)
	}
	for index := additionIndex + 1; index < len(unified) && rows[index] == rows[additionIndex]; index++ {
		plain := ansi.Strip(unified[index])
		if strings.Contains(plain, "●") || !strings.HasPrefix(plain, strings.Repeat(" ", 9)) {
			t.Fatalf("wrapped continuation reused marker cells: %q", plain)
		}
	}
	if marker := model.commentAnchorCell(active, review.LineNew, lineTwo, styles); !strings.Contains(marker, "\x1b[1m") || ansi.Strip(marker) != "●" {
		t.Fatalf("monochrome marker lacks bold non-color emphasis: %q", marker)
	}

	model.settings.diffView = DiffViewSideBySide
	model.settings.wrap = false
	if fallback, _ := model.diffLines(
		sideBySideContentWidth+diffCommentAnchorColumns-1,
		12,
		styles,
	); strings.Contains(renderedLineContaining(t, fallback, "original"), "│") {
		t.Fatal("side-by-side activated below its content-width requirement")
	}
	sideBySide, _ := model.diffLines(
		sideBySideContentWidth+diffCommentAnchorColumns,
		12,
		styles,
	)
	row := renderedLineContaining(t, sideBySide, "original")
	left, right, ok := strings.Cut(row, "│")
	if !ok || !strings.Contains(left, "1● original") ||
		!strings.Contains(right, "1● original") {
		t.Fatalf("side-by-side markers are not attached to their halves: %q", row)
	}
}

func mustLineTarget(
	t *testing.T,
	file review.FileID,
	side review.LineSide,
	line review.LineNumber,
) review.CommentTarget {
	t.Helper()
	target, err := review.LineCommentTarget(file, side, line, line)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestDecimalDigitWidthBoundaries(t *testing.T) {
	for _, test := range []struct {
		value uint32
		want  int
	}{
		{value: 0, want: 1},
		{value: 9, want: 1},
		{value: 10, want: 2},
		{value: 99, want: 2},
		{value: 100, want: 3},
		{value: 999_999, want: 6},
		{value: 1_000_000, want: 7},
	} {
		if got := decimalDigitWidth(test.value); got != test.want {
			t.Fatalf("decimal width of %d = %d, want %d", test.value, got, test.want)
		}
	}
}

func renderedLineContaining(t *testing.T, lines []string, fragment string) string {
	t.Helper()
	for _, line := range lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, fragment) {
			return plain
		}
	}
	t.Fatalf("rendered lines omitted %q: %q", fragment, lines)
	return ""
}

func TestDiffRenderingEscapesHostileTextInEveryLayout(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "safe\x1b\u0085\u202e" + string([]byte{0xff}) + "\n",
	})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})

	for _, test := range []struct {
		name     string
		expected string
		width    int
		view     DiffView
		wrap     bool
	}{
		{
			name: "unified wrapped", expected: `\x1B\u{0085}\u{202E}\xFF`,
			width: compactWidth, view: DiffViewUnified, wrap: true,
		},
		{
			name: "unified clipped", expected: `\x1B\u{0085}\u{202E}\xFF`,
			width: compactWidth, view: DiffViewUnified,
		},
		{
			name: "side-by-side wrapped", expected: `\x1B\u{0085}\u{202E}\xFF`,
			width: wideWidth, view: DiffViewSideBySide, wrap: true,
		},
		{
			name: "side-by-side clipped", expected: `\x1B\u{0085}\u{202E}\xFF`,
			width: wideWidth, view: DiffViewSideBySide,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model.settings.diffView = test.view
			model.settings.wrap = test.wrap
			_, _ = model.Update(tea.WindowSizeMsg{Width: test.width, Height: 24})
			content := ansi.Strip(model.View().Content)
			if !utf8.ValidString(content) {
				t.Fatal("view contains invalid UTF-8")
			}
			for _, unsafe := range []string{"\x1b", "\u0085", "\u202e", string([]byte{0xff})} {
				if strings.Contains(content, unsafe) {
					t.Fatalf("view emitted unsafe text %q:\n%s", unsafe, content)
				}
			}
			if !strings.Contains(content, test.expected) {
				t.Fatalf("view omitted visible escapes %q:\n%s", test.expected, content)
			}
		})
	}
}

func TestSyntaxRenderingDoesNotSplitGraphemeForStyle(t *testing.T) {
	model := Model{}
	raw := "1️⃣\tvalue\x1b"
	spans := func(yield func(review.SyntaxSpan) bool) {
		yield(review.SyntaxSpan{Start: 0, End: 1, Kind: review.SyntaxNumber})
	}
	rendered := model.renderSyntaxContent(syntaxContent{
		raw:    raw,
		spans:  spans,
		width:  80,
		wrap:   true,
		styles: semanticStyles{number: lipgloss.NewStyle().Bold(true)},
	})
	want := lipgloss.NewStyle().Bold(true).Render("1️⃣") + `  value\x1B`
	if rendered != want {
		t.Fatalf("syntax render = %q, want exact grapheme styling %q", rendered, want)
	}
}

func TestRenderSyntaxSpansStopsAtViewportBoundary(t *testing.T) {
	raw := "plain"
	visible := review.SyntaxSpan{Start: 0, End: 1, Kind: review.SyntaxNumber}
	styles := semanticStyles{number: lipgloss.NewStyle().Bold(true)}
	want, wantLimited := renderSyntaxSpans(raw, func(yield func(review.SyntaxSpan) bool) {
		yield(visible)
	}, styles)

	pulls := 0
	stopped := false
	spans := func(yield func(review.SyntaxSpan) bool) {
		defer func() {
			stopped = true
		}()
		pulls++
		if !yield(visible) {
			return
		}
		for index := len(raw) + 1; index < len(raw)+1_024; index++ {
			pulls++
			if !yield(review.SyntaxSpan{
				Start: uint32(index),
				End:   uint32(index + 1),
			}) {
				return
			}
		}
	}
	got, gotLimited := renderSyntaxSpans(raw, spans, styles)
	if got != want || gotLimited != wantLimited {
		t.Fatalf(
			"syntax render = (%q, %t), want visible-only result (%q, %t)",
			got,
			gotLimited,
			want,
			wantLimited,
		)
	}
	if pulls != 2 {
		t.Fatalf("syntax span pulls = %d, want visible span plus boundary span", pulls)
	}
	if !stopped {
		t.Fatal("syntax span iterator was not stopped")
	}
}

func TestRenderSyntaxSpansPreservesBoundarySemantics(t *testing.T) {
	raw := "abc"
	number := lipgloss.NewStyle().Bold(true)
	styles := semanticStyles{number: number}
	for _, test := range []struct {
		name    string
		spans   []review.SyntaxSpan
		want    string
		limited bool
	}{
		{
			name: "empty and reversed spans",
			spans: []review.SyntaxSpan{
				{Start: 0, End: 0, Kind: review.SyntaxNumber},
				{Start: 2, End: 1, Kind: review.SyntaxNumber},
				{Start: 2, End: 3, Kind: review.SyntaxNumber},
			},
			want: "ab" + number.Render("c"),
		},
		{
			name: "overlapping span",
			spans: []review.SyntaxSpan{
				{Start: 0, End: 2, Kind: review.SyntaxNumber},
				{Start: 1, End: 3, Kind: review.SyntaxString},
			},
			want: number.Render("ab") + "c",
		},
		{
			name:    "span crosses end",
			spans:   []review.SyntaxSpan{{Start: 2, End: 4, Kind: review.SyntaxNumber}},
			want:    raw,
			limited: true,
		},
		{
			name:    "span starts at end",
			spans:   []review.SyntaxSpan{{Start: 3, End: 4, Kind: review.SyntaxNumber}},
			want:    raw,
			limited: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, limited := renderSyntaxSpans(raw, func(yield func(review.SyntaxSpan) bool) {
				for _, span := range test.spans {
					if !yield(span) {
						return
					}
				}
			}, styles)
			if got != test.want || limited != test.limited {
				t.Fatalf(
					"syntax render = (%q, %t), want (%q, %t)",
					got,
					limited,
					test.want,
					test.limited,
				)
			}
		})
	}
}

func TestSyntaxRenderingWithoutSpansUsesPlainSanitizedPath(t *testing.T) {
	model := Model{}
	content := syntaxContent{
		raw:   "plain\ttext\x1b",
		width: 80,
		wrap:  true,
	}
	if got, want := model.renderSyntaxContent(content), `plain   text\x1B`; got != want {
		t.Fatalf("plain syntax render = %q, want %q", got, want)
	}
}

func TestPublicationPreflightRendersCanonicalPayloadAndDecision(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	target, _ := review.FileCommentTarget(0)
	current, err := model.review.BeginDraft(target)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("full publication comment")
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = current.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	policies, _ := review.NewPublicationPolicies()
	options, _ := review.NewPublicationOptions(review.PublicationEventComment, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := review.NewObjectID(strings.Repeat("1", 40))
	head, _ := review.NewObjectID(strings.Repeat("2", 40))
	publicationTarget, err := review.NewPublicationTarget(
		review.PublicationTargetDescriptor{
			Repository:  "owner/repository",
			PullRequest: 7,
			Base:        base,
			Head:        head,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	marker, _ := review.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	checked, err := review.TakeCheckedPublication(
		review.CheckedPublicationDescriptor{
			Target: publicationTarget,
			Marker: marker,
		},
		prepared,
	)
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := review.NewOperationID(99)
	current, request, err := current.StartPublicationCheck(operation, options)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.ApplyPublicationCheck(review.PublicationCheckResult{
		Operation: request.Operation,
		Workspace: request.Workspace,
		Source:    request.Source,
		Checked:   checked,
	})
	if err != nil {
		t.Fatal(err)
	}
	model.review = current
	model.overlay = OverlaySubmit
	_, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 25})
	payloadView := model.View().Content
	if !strings.Contains(payloadView, "[payload] canonical GitHub request") ||
		!strings.Contains(payloadView, `"commit_id":"`+head.String()+`"`) {
		t.Fatalf("preflight omitted canonical payload:\n%s", payloadView)
	}

	model.publicationCursor = 1
	commentView := model.View().Content
	if !strings.Contains(commentView, "[summary] full publication comment") ||
		!strings.Contains(commentView, "Full comment #0") {
		t.Fatalf("preflight omitted comment decision/detail:\n%s", commentView)
	}
}

func TestWrappedLineFittingFastPathPreservesContent(t *testing.T) {
	model := &Model{}
	model.settings.wrap = true
	testCases := []struct {
		name  string
		value string
	}{
		{name: "ascii", value: "short source line"},
		{
			name: "styled unicode",
			value: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render(
				"résumé λ-value",
			),
		},
		{name: "ansi", value: "\x1b[31mred text\x1b[0m"},
		{name: "wide grapheme", value: "界🙂‍↕️界🙂‍↕️"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			lines := model.wrappedLine(testCase.value, 80)
			if len(lines) != 1 || lines[0] != testCase.value {
				t.Fatalf("wrapped fitting line = %#v", lines)
			}
		})
	}
	if lines := model.wrappedLine("left\nright", 80); len(lines) != 2 ||
		lines[0] != "left" || lines[1] != "right" {
		t.Fatalf("embedded newline wrapping = %#v", lines)
	}
	for index, line := range model.wrappedLine(strings.Repeat("overflow ", 8), 12) {
		if width := lipgloss.Width(line); width > 12 {
			t.Fatalf("overflow line %d width = %d", index, width)
		}
	}
}
