package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

func TestModelRetainsOwnerAndBuildsCompleteBoundedViews(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "first\nchanged\n",
		"beta.txt":  "second\nchanged\n",
	})

	updated, command := model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})
	if updated != model {
		t.Fatal("Update returned a different root model owner")
	}
	if command != nil {
		t.Fatal("window resize unexpectedly returned a command")
	}

	first := model.View()
	second := model.View()
	if first.Content != second.Content {
		t.Fatal("View mutated observable model state")
	}
	if !first.AltScreen || !first.ReportFocus ||
		first.WindowTitle != "rvw" ||
		first.MouseMode != tea.MouseModeCellMotion ||
		!first.KeyboardEnhancements.ReportEventTypes {
		t.Fatalf("incomplete terminal view fields: %+v", first)
	}
	if lines := strings.Count(first.Content, "\n") + 1; lines != 24 {
		t.Fatalf("rendered lines = %d, want 24", lines)
	}
	if rows := model.MaterializedRows(); rows > 2*(24+viewportOverscan) {
		t.Fatalf("materialized rows = %d, exceeds viewport bound", rows)
	}
	if coordinator.Stats().Accepted != 1 {
		t.Fatalf("accepted operations = %d, want 1", coordinator.Stats().Accepted)
	}
}

func TestInitialPatchAdmissionOccursInUpdate(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	if !model.initialized {
		t.Fatal("test model did not process the initialize message")
	}
	if got := coordinator.Stats().Accepted; got != 1 {
		t.Fatalf("accepted operations after initialize = %d, want 1", got)
	}
}

func TestCancelledSyntaxWorkDoesNotReplaceTheCurrentNotice(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	for model.review.Patch(active).Status != review.PatchReady {
		if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
			t.Fatalf("patch readiness = %d", state)
		}
		_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	}
	if model.syntaxOperation == 0 {
		t.Fatal("ready patch did not request syntax work")
	}
	model.notice = &Notice{Kind: NoticeInformation, Text: "reload complete"}

	model.applySyntaxOutcome(SyntaxOutcome{
		ID:  model.syntaxOperation,
		Err: context.Canceled,
	})

	if model.syntaxOperation != 0 {
		t.Fatal("cancelled syntax work remained active")
	}
	if model.notice == nil || model.notice.Text != "reload complete" {
		t.Fatalf("cancelled syntax notice = %+v, want prior notice", model.notice)
	}
}

func TestSelectionAndActivationRemainDistinct(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{
		"alpha.txt": "changed\n",
		"beta.txt":  "changed\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 18})
	model.focus = FocusFileTree
	activeBefore, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing initial active file")
	}

	_, _ = model.Update(keyMessage("j"))
	selected, ok := model.review.SelectedFile()
	if !ok || selected == activeBefore {
		t.Fatalf("move did not select a distinct file: selected=%d active=%d", selected, activeBefore)
	}
	activeAfterMove, _ := model.review.ActiveFile()
	if activeAfterMove != activeBefore {
		t.Fatal("moving file selection activated it")
	}

	_, _ = model.Update(keyMessage("enter"))
	activeAfterEnter, _ := model.review.ActiveFile()
	if activeAfterEnter != selected {
		t.Fatalf("active file = %d, want selected file %d", activeAfterEnter, selected)
	}
}

func TestCommentEditorAnchorsSemanticLineAndRetainsChildState(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "original\nadded\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})

	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	state := model.review.Patch(active)
	lines := model.review.PatchLines(active, 0, state.Lines, 4_096)
	found := false
	for _, line := range lines {
		if line.Kind() == review.PatchLineAddition {
			model.diffCursor = line.Number
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fixture patch has no addition")
	}

	_, focusCommand := model.Update(keyMessage("c"))
	if model.mode != ModeCommentEdit || focusCommand == nil {
		t.Fatalf("comment edit did not start: mode=%d command=%v", model.mode, focusCommand)
	}
	for _, character := range "fix this" {
		_, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: string(character), Code: character}))
	}
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	if model.mode != ModeNormal {
		t.Fatalf("comment editor mode = %d after save", model.mode)
	}
	comments := model.review.Comments()
	if len(comments) != 1 || comments[0].Body().String() != "fix this" {
		t.Fatalf("saved comments = %+v", comments)
	}
	side, start, end, ok := comments[0].Target().Lines()
	if !ok || side != review.LineNew || start != end || start.Uint32() != 2 {
		t.Fatalf("saved target = side %d start %d end %d ok %v", side, start, end, ok)
	}
	if !strings.Contains(model.View().Content, "fix this") {
		t.Fatal("wide view does not expose the saved comment")
	}
}

func TestCommentActivationLoadsItsFileAndRevealsItsSemanticLine(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "first\nchanged\n",
		"beta.txt":  "second\nchanged\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	waitForPatch(t, model, coordinator, 0)

	var beta review.FileID
	found := false
	for file := range model.review.Snapshot().AllFiles() {
		if file.DisplayPath() == "beta.txt" {
			beta = file.ID()
			found = true
			break
		}
	}
	if !found {
		t.Fatal("beta fixture file is missing")
	}
	line, _ := review.NewLineNumber(2)
	target, _ := review.LineCommentTarget(beta, review.LineNew, line, line)
	var err error
	model.review, err = model.review.BeginDraft(target)
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = model.review.UpdateDraft("beta line")
	if err != nil {
		t.Fatal(err)
	}
	model.review, _, err = model.review.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	model.commentCursor = 0
	model.focus = FocusComments

	_, _ = model.Update(keyMessage("enter"))
	active, ok := model.review.ActiveFile()
	if !ok || active != beta || model.focus != FocusDiff {
		t.Fatalf("activation = file %d ok %v focus %d, want beta and diff", active, ok, model.focus)
	}
	if model.foreground.patch.target == nil {
		t.Fatal("lazy comment jump did not retain its semantic target")
	}
	waitForPatch(t, model, coordinator, beta)
	wantRow, ok := model.review.PatchLineForSource(beta, review.LineNew, line)
	if !ok {
		t.Fatal("beta line is not represented by its ready patch")
	}
	if model.diffCursor != wantRow || model.foreground.patch.target != nil {
		t.Fatalf("comment jump = row %d pending %v, want row %d settled", model.diffCursor, model.foreground.patch.target != nil, wantRow)
	}
}

func TestRemoteCommentActivationLoadsItsFileAndRevealsItsSemanticLine(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "first\nchanged\n",
		"beta.txt":  "second\nchanged\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitForPatch(t, model, coordinator, active)

	var targetFile review.File
	for file := range model.review.Snapshot().AllFiles() {
		if file.ID() != active {
			targetFile = file
			break
		}
	}
	if targetFile.PatchHandle().SourceID() == 0 {
		t.Fatal("fixture has no second file")
	}
	line, _ := review.NewLineNumber(2)
	thread, err := review.NewRemoteThread(review.RemoteThreadDescriptor{
		ID:   "thread-1",
		Path: targetFile.IdentityPath(),
		Line: &line,
		Side: review.LineNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := model.review.Snapshot().WithRemoteThreads([]review.RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = review.NewReview(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.commentCursor = 0
	model.focus = FocusComments

	model.activateCommentSelection()
	selected, ok := model.review.ActiveFile()
	if !ok || selected != targetFile.ID() || model.focus != FocusDiff {
		t.Fatalf("activation = file %d ok %v focus %d, want target file and diff", selected, ok, model.focus)
	}
	if model.foreground.patch.target == nil {
		t.Fatal("lazy remote comment jump did not retain its semantic target")
	}
	waitForPatch(t, model, coordinator, targetFile.ID())
	wantRow, ok := model.review.PatchLineForSource(targetFile.ID(), review.LineNew, line)
	if !ok {
		t.Fatal("remote line is not represented by its ready patch")
	}
	if model.diffCursor != wantRow || model.foreground.patch.target != nil {
		t.Fatalf("remote comment jump = row %d pending %v, want row %d settled", model.diffCursor, model.foreground.patch.target != nil, wantRow)
	}
}

func TestNonLineCommentActivationUsesExplicitFallbacks(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitForPatch(t, model, coordinator, active)

	fileTarget, err := review.FileCommentTarget(active)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []review.CommentTarget{fileTarget, review.ReviewCommentTarget()} {
		model.review, err = model.review.BeginDraft(target)
		if err != nil {
			t.Fatal(err)
		}
		model.review, err = model.review.UpdateDraft("fallback")
		if err != nil {
			t.Fatal(err)
		}
		model.review, _, err = model.review.SaveDraft()
		if err != nil {
			t.Fatal(err)
		}
	}

	model.commentCursor = 1
	model.diffCursor = 4
	model.focus = FocusComments
	model.activateCommentSelection()
	if current, _ := model.review.ActiveFile(); current != active ||
		model.diffCursor != 4 || model.focus != FocusComments ||
		model.notice == nil || model.notice.Text != "review comment has no file or line" {
		t.Fatalf("review fallback = active %d row %d focus %d notice %+v", current, model.diffCursor, model.focus, model.notice)
	}

	model.commentCursor = 0
	model.activateCommentSelection()
	if current, _ := model.review.ActiveFile(); current != active ||
		model.diffCursor != 0 || model.focus != FocusDiff ||
		model.notice == nil || model.notice.Text != "file comment has no line" {
		t.Fatalf("file fallback = active %d row %d focus %d notice %+v", current, model.diffCursor, model.focus, model.notice)
	}
}

func TestRemoteCommentActivationDistinguishesMissingAndOutdatedTargets(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "first\nchanged\n",
		"beta.txt":  "second\nchanged\n",
	})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitForPatch(t, model, coordinator, active)
	var targetFile review.File
	for file := range model.review.Snapshot().AllFiles() {
		if file.ID() != active {
			targetFile = file
			break
		}
	}
	missingPath, err := review.NewPath([]byte("missing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	line, _ := review.NewLineNumber(2)
	missing, err := review.NewRemoteThread(review.RemoteThreadDescriptor{
		ID: "missing", Path: missingPath, Line: &line, Side: review.LineNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	outdated, err := review.NewRemoteThread(review.RemoteThreadDescriptor{
		ID: "outdated", Outdated: true, Path: targetFile.IdentityPath(),
		Line: &line, Side: review.LineNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := model.review.Snapshot().WithRemoteThreads(
		[]review.RemoteThread{missing, outdated},
	)
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = review.NewReview(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.diffCursor = 4
	model.focus = FocusComments

	model.commentCursor = 0
	model.activateCommentSelection()
	if current, _ := model.review.ActiveFile(); current != active ||
		model.diffCursor != 4 || model.focus != FocusComments ||
		model.notice == nil || model.notice.Text != "remote comment file is not in this review" {
		t.Fatalf("missing fallback = active %d row %d focus %d notice %+v", current, model.diffCursor, model.focus, model.notice)
	}

	model.commentCursor = 1
	model.activateCommentSelection()
	if current, _ := model.review.ActiveFile(); current != targetFile.ID() ||
		model.diffCursor != 0 || model.focus != FocusDiff ||
		model.notice == nil || model.notice.Text != "remote comment line is outdated; showing its file" {
		t.Fatalf("outdated fallback = active %d row %d focus %d notice %+v", current, model.diffCursor, model.focus, model.notice)
	}
}

func waitForPatch(
	t *testing.T,
	model *Model,
	coordinator *Coordinator,
	file review.FileID,
) {
	t.Helper()
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	for model.review.Patch(file).Status != review.PatchReady {
		if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
			t.Fatalf("patch readiness = %d", state)
		}
		_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	}
}

func TestAsyncStdoutExportQuitsOnlyAfterPreparedPayloadIsOwnedByModel(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})

	_, command := model.Update(keyMessage("M"))
	if command != nil {
		t.Fatal("export submission returned a terminal command before preparation completed")
	}
	if model.overlay != OverlayExport {
		t.Fatalf("export overlay = %d", model.overlay)
	}
	_, command = model.Update(keyMessage("enter"))
	if command != nil {
		t.Fatal("export selection returned a terminal command before preparation completed")
	}
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("export ready state = %d", state)
	}
	_, command = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	if command == nil {
		t.Fatal("prepared stdout export did not initiate controlled quit")
	}
	if message := command(); message != (tea.QuitMsg{}) {
		t.Fatalf("stdout export command returned %T", message)
	}
	body, ok := model.PendingStdout()
	if !ok || body != "_No comments._" {
		t.Fatalf("pending stdout = %q %v", body, ok)
	}
}

func TestAsyncJSONExportUsesPostProgramStdoutHandoff(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})

	_, command := model.Update(keyMessage("J"))
	if command != nil {
		t.Fatal("JSON export returned a terminal command before preparation completed")
	}
	if model.overlay != OverlayExport {
		t.Fatalf("JSON export overlay = %d", model.overlay)
	}
	_, command = model.Update(keyMessage("enter"))
	if command != nil {
		t.Fatal("JSON export selection returned a terminal command before preparation completed")
	}
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("JSON export ready state = %d", state)
	}
	_, command = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	if command == nil || command() != (tea.QuitMsg{}) {
		t.Fatal("prepared JSON export did not initiate controlled quit")
	}
	body, ok := model.PendingStdout()
	if !ok || body != "{\"comments\":[]}\n" {
		t.Fatalf("pending JSON = %q %v", body, ok)
	}
}

func TestAsyncStdoutExportDoesNotQuitWhenOperationTransitionFails(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	operationID, err := review.NewOperationID(999)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := model.review.PrepareMarkdown(
		model.settings.exportTemplates,
		review.ExportOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	model.exportOperations[operationID] = exportRequest{
		format:      review.ExportMarkdown,
		destination: delivery.DestinationStdout,
	}

	command := model.applyExportOutcome(ExportOutcome{
		ID:          operationID,
		Prepared:    prepared,
		Destination: delivery.DestinationStdout,
	})
	if command != nil || model.quitting || model.pendingStdout != nil {
		t.Fatal("invalid operation transition initiated stdout delivery")
	}
	if model.notice == nil || model.notice.Kind != NoticeError {
		t.Fatalf("operation transition notice = %+v", model.notice)
	}
}

func TestStrokeNormalizationPreservesPrintableCase(t *testing.T) {
	if normalizeStroke("c") == normalizeStroke("C") {
		t.Fatal("printable key case was collapsed")
	}
	if normalizeStroke("Ctrl+C") != "ctrl-c" || normalizeStroke("ENTER") != "enter" {
		t.Fatal("named/modifier keys were not normalized")
	}
}

func TestNumericRepeatIsBoundedAndAppliedWithoutPerStepUpdates(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{
		"a.txt": "changed\n",
		"b.txt": "changed\n",
		"c.txt": "changed\n",
		"d.txt": "changed\n",
		"e.txt": "changed\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 18})
	model.focus = FocusFileTree

	_, _ = model.Update(keyMessage("3"))
	_, _ = model.Update(keyMessage("j"))
	selected, ok := model.review.SelectedFile()
	if !ok || selected != model.fileOrder[3] {
		t.Fatalf("repeated move selected %d, want %d", selected, model.fileOrder[3])
	}

	for range 5 {
		_, _ = model.Update(keyMessage("9"))
	}
	if model.repeat != 0 || model.notice == nil ||
		model.notice.Text != "numeric repeat must not exceed 9999" {
		t.Fatalf("overflow repeat state = %d notice = %+v", model.repeat, model.notice)
	}
}

func TestEffectiveBindingsDriveRoutingAndVisibleHelp(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{
		"a.txt": "changed\n",
		"b.txt": "changed\n",
	})
	model.settings.keybindings = append(
		model.settings.keybindings,
		Keybinding{
			Context: KeyContextNormal,
			Focus:   KeyFocusFileTree,
			Chord:   "j",
			Action:  KeyActionLast,
		},
		Keybinding{
			Context: KeyContextMinimumSize,
			Chord:   "j",
			Action:  KeyActionMoveDown,
		},
	)
	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 18})
	model.focus = FocusFileTree

	_, _ = model.Update(keyMessage("j"))
	selected, ok := model.review.SelectedFile()
	if !ok || selected != model.fileOrder[len(model.fileOrder)-1] {
		t.Fatalf("focused override selected %d, want last file", selected)
	}

	model.overlay = OverlayHelp
	help := model.View().Content
	if !strings.Contains(help, "j") || !strings.Contains(help, KeyActionLast.String()) ||
		strings.Contains(help, KeyActionMoveDown.String()) {
		t.Fatalf("help did not reflect focused override:\n%s", help)
	}

	model.overlay = OverlayNone
	_, _ = model.Update(tea.WindowSizeMsg{Width: minimumWidth - 1, Height: minimumHeight})
	before, _ := model.review.SelectedFile()
	_, _ = model.Update(keyMessage("j"))
	after, _ := model.review.SelectedFile()
	if after != before {
		t.Fatal("non-quit minimum-size binding was routed")
	}
}

func TestMouseTranslationReturnsOnlyImmediateTypedActions(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{
		"a.txt": "changed\n",
		"b.txt": "changed\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	view := model.View()
	if view.OnMouse == nil {
		t.Fatal("enabled mouse has no bounded action owner")
	}

	click := view.OnMouse(tea.MouseClickMsg{
		X:      wideWidth - 1,
		Y:      1,
		Button: tea.MouseLeft,
	})
	if click == nil {
		t.Fatal("comments-pane click returned no command")
	}
	message, ok := click().(mouseActionMsg)
	if !ok || message.focus != FocusComments || message.delta != 0 {
		t.Fatalf("click translated to %#v", message)
	}
	_, _ = model.Update(message)
	if model.focus != FocusComments {
		t.Fatalf("click focus = %d, want comments", model.focus)
	}

	wheel := view.OnMouse(tea.MouseWheelMsg{
		X:      0,
		Y:      1,
		Button: tea.MouseWheelDown,
	})
	if wheel == nil {
		t.Fatal("file-pane wheel returned no command")
	}
	message, ok = wheel().(mouseActionMsg)
	if !ok || message.focus != FocusFileTree || message.delta != 3 {
		t.Fatalf("wheel translated to %#v", message)
	}
	model.focus = FocusDiff
	active, _ := model.review.ActiveFile()
	model.review, _ = model.review.ToggleFileReviewed(active)
	_, _ = model.Update(message)
	if model.focus != FocusDiff {
		t.Fatal("stale mouse action from an older review state was applied")
	}
}

func TestMouseActionForHiddenPaneDoesNotMutateInputOwnership(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		mouseX     int
		hideWidth  int
		hideHeight int
	}{
		{name: "file tree", mouseX: 0, hideWidth: compactWidth, hideHeight: 18},
		{name: "comments", mouseX: wideWidth - 1, hideWidth: normalWidth, hideHeight: 24},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			settleModelWork(t, model, coordinator)
			_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
			view := model.View()
			command := view.OnMouse(tea.MouseClickMsg{
				X: testCase.mouseX, Y: 1, Button: tea.MouseLeft,
			})
			if command == nil {
				t.Fatal("visible-pane click returned no command")
			}
			message, ok := command().(mouseActionMsg)
			if !ok {
				t.Fatalf("click message type = %T", command())
			}

			model.settings.keybindings = []Keybinding{{
				Context: KeyContextNormal,
				Focus:   KeyFocusDiff,
				Chord:   "x t",
				Action:  KeyActionToggleWrap,
			}}
			_, _ = model.Update(keyMessage("5"))
			_, _ = model.Update(keyMessage("x"))
			_, _ = model.Update(tea.WindowSizeMsg{
				Width: testCase.hideWidth, Height: testCase.hideHeight,
			})
			if model.focusVisible(message.focus) {
				t.Fatalf("mouse focus %v remained visible", message.focus)
			}

			_, _ = model.Update(message)
			if model.focus != FocusDiff || model.repeat != 5 ||
				len(model.chord.strokes) != 1 || model.chord.strokes[0] != "x" {
				t.Fatalf(
					"stale mouse changed focus=%v repeat=%d chord=%v",
					model.focus, model.repeat, model.chord.strokes,
				)
			}
		})
	}
}

func TestResponsiveLayoutUsesBothWidthAndHeightBoundaries(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"a.txt": "changed\n"})
	tests := []struct {
		width, height  int
		tree, comments bool
	}{
		{width: 179, height: 30, tree: true, comments: false},
		{width: 180, height: 29, tree: true, comments: false},
		{width: 180, height: 30, tree: true, comments: true},
		{width: 120, height: 24, tree: true, comments: false},
		{width: 120, height: 23, tree: true, comments: false},
		{width: 85, height: 18, tree: true, comments: false},
		{width: 84, height: 18, tree: false, comments: false},
		{width: 85, height: 17, tree: false, comments: false},
		{width: 20, height: 6, tree: false, comments: false},
	}
	for _, test := range tests {
		_, _ = model.Update(tea.WindowSizeMsg{Width: test.width, Height: test.height})
		layout := model.workspaceLayout(test.width, test.height)
		if (layout.treeWidth != 0) != test.tree ||
			(layout.commentsWidth != 0) != test.comments {
			t.Fatalf(
				"layout %dx%d = tree %d comments %d",
				test.width,
				test.height,
				layout.treeWidth,
				layout.commentsWidth,
			)
		}
		if layout.diffWidth < 60 && (layout.treeWidth != 0 || layout.commentsWidth != 0) {
			t.Fatalf("layout %dx%d kept optional pane with %d-column diff", test.width, test.height, layout.diffWidth)
		}
	}

	model.commentsVisible = true
	layout := model.workspaceLayout(120, 24)
	if layout.treeWidth < 24 || layout.commentsWidth < 32 || layout.diffWidth < 60 {
		t.Fatalf("normal comments layout = %+v", layout)
	}
	model.settings.diffView = DiffViewSideBySide
	if !model.workspaceLayout(120, 24).sideBySideFallback ||
		model.workspaceLayout(180, 30).sideBySideFallback {
		t.Fatal("side-by-side fallback did not follow diff content width")
	}
}

func TestReloadAcquisitionAndPreparationCommitAtomicallyOffUpdate(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"a.txt": "changed\n"})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})

	local, ok := model.document.Local()
	if !ok || local.Repo == nil {
		t.Fatal("test document has no local repository")
	}
	if err := os.WriteFile(
		filepath.Join(local.Repo.String(), "b.txt"),
		[]byte("untracked\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	oldSource := model.review.Snapshot().SourceID()
	oldWorkspace := model.review.WorkspaceRevision()
	model.startReload()
	if model.foreground.reloadOperation() == 0 || model.foreground.reloadIsPreparing() {
		t.Fatalf(
			"reload acquisition state = operation %d preparing %v",
			model.foreground.reloadOperation(),
			model.foreground.reloadIsPreparing(),
		)
	}

	for attempts := 0; !model.foreground.reloadIsPreparing() && attempts < 8; attempts++ {
		if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
			t.Fatalf("reload acquisition ready state = %d", state)
		}
		_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	}
	if !model.foreground.reloadIsPreparing() {
		t.Fatal("reload did not enter off-loop reconciliation")
	}
	if model.review.Snapshot().SourceID() != oldSource {
		t.Fatal("source became observable before prepared replacement committed")
	}
	var err error
	model.review, err = model.review.ToggleFileReviewed(0)
	if err != nil {
		t.Fatal(err)
	}

	for attempts := 0; model.foreground.reloadOperation() != 0 && attempts < 8; attempts++ {
		if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
			t.Fatalf("reload preparation ready state = %d", state)
		}
		_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	}
	if model.foreground.reloadOperation() != 0 || model.foreground.reloadIsPreparing() {
		t.Fatalf(
			"reload did not settle: operation %d preparing %v",
			model.foreground.reloadOperation(),
			model.foreground.reloadIsPreparing(),
		)
	}
	if model.review.Snapshot().SourceID() == oldSource ||
		model.review.WorkspaceRevision().Uint64() != oldWorkspace.Uint64()+1 ||
		model.review.Snapshot().FileCount() != 2 {
		t.Fatalf(
			"replacement = source %d workspace %d files %d",
			model.review.Snapshot().SourceID(),
			model.review.WorkspaceRevision(),
			model.review.Snapshot().FileCount(),
		)
	}
	if len(model.allFileOrder) != 2 {
		t.Fatalf("prepared file order length = %d", len(model.allFileOrder))
	}
	if !model.review.FileReviewed(0) {
		t.Fatal("reload retry lost review state changed during preparation")
	}
}

func newTestModel(t testing.TB, changed map[string]string) (*Model, *Coordinator) {
	t.Helper()
	fixture := newSourceFixture(t, changed)
	workContext, cancelWork := context.WithCancel(t.Context())
	deliveryManager := newTestDeliveryManager(t)
	coordinator, err := NewCoordinator(workContext, fixture.service, deliveryManager)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	receiver, err := NewReadyReceiver(t.Context())
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	contextLines, err := review.NewContextLines(3)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	exportTemplates, err := review.CompileExportTemplates(
		"{{comments}}",
		"{{index}} {{comment}}",
	)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	features, err := NewSettingFeatures(FeatureMouse, FeatureWrap)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	publicationPolicies, err := review.NewPublicationPolicies()
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	publicationOptions, err := review.NewPublicationOptions(
		review.PublicationEventComment,
		publicationPolicies,
	)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	syntaxSelector, err := review.NewSyntaxSelector("test")
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	settings, err := NewSettings(SettingsRequest{
		Features:        features,
		DiffView:        DiffViewUnified,
		ContextLines:    contextLines,
		SyntaxSelector:  syntaxSelector,
		ExportTemplates: exportTemplates,
		Publication:     publicationOptions,
		Keybindings: []Keybinding{
			{Context: KeyContextNormal, Chord: "q", Action: KeyActionQuit},
			{Context: KeyContextNormal, Chord: "j", Action: KeyActionMoveDown},
			{Context: KeyContextNormal, Chord: "enter", Action: KeyActionActivate},
			{Context: KeyContextNormal, Chord: "c", Action: KeyActionCommentAtCursor},
			{Context: KeyContextNormal, Chord: "M", Action: KeyActionExportMarkdown},
			{Context: KeyContextNormal, Chord: "J", Action: KeyActionExportJSON},
			{Context: KeyContextNormal, Chord: "?", Action: KeyActionHelp},
			{Context: KeyContextCommentEdit, Chord: "ctrl-s", Action: KeyActionSaveComment},
			{Context: KeyContextCommentEdit, Chord: "esc", Action: KeyActionCancel},
			{Context: KeyContextHelp, Chord: "q", Action: KeyActionCancel},
			{Context: KeyContextExportPicker, Chord: "enter", Action: KeyActionExportSelected},
			{Context: KeyContextExportPicker, Chord: "esc", Action: KeyActionCancel},
			{Context: KeyContextMinimumSize, Chord: "q", Action: KeyActionQuit},
		},
	})
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	model, err := NewModel(workContext, fixture.document, settings, coordinator, receiver)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	if got := coordinator.Stats().Accepted; got != 0 {
		cancelWork()
		t.Fatalf("model construction accepted %d operations, want 0", got)
	}
	updated, command := model.Update(initializeMsg{})
	if updated != model || command != nil {
		cancelWork()
		t.Fatalf("initialize update = (%T, %v), want same model and no command", updated, command)
	}
	indexContext, cancelIndex := context.WithTimeout(t.Context(), 5*time.Second)
	if state := coordinator.WaitReady(indexContext); state != ReadyAvailable {
		cancelIndex()
		cancelWork()
		t.Fatalf("workspace index readiness = %v, want available", state)
	}
	cancelIndex()
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	t.Cleanup(func() {
		receiverContext, cancelReceiver := context.WithTimeout(context.Background(), time.Second)
		defer cancelReceiver()
		if err := receiver.StopAndWait(receiverContext); err != nil {
			t.Error(err)
		}
		coordinator.StopAdmissionAndCancel()
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := coordinator.Shutdown(shutdownContext); err != nil {
			t.Error(err)
		}
		cancelWork()
	})
	return model, coordinator
}

func newTestDeliveryManager(t testing.TB) *delivery.Manager {
	t.Helper()
	manager, err := delivery.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Close(closeContext); err != nil {
			t.Error(err)
		}
	})
	return manager
}

type sourceFixture struct {
	service  *source.Service
	document *source.Document
}

func newSourceFixture(t testing.TB, changed map[string]string) sourceFixture {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "rvw@example.test")
	runGit(t, root, "config", "user.name", "rvw test")
	for name := range changed {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "initial")
	for name, contents := range changed {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workingDirectory, err := source.NewWorkingDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := source.NewService(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	document, err := service.Open(t.Context(), source.WorktreeTarget(), source.OpenOptions{
		IncludeUntracked: true,
		BuiltinDefaults:  true,
		Gitignore:        true,
		RVWIgnore:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := document.Close(); err != nil {
			t.Error(err)
		}
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := service.Close(shutdownContext); err != nil {
			t.Error(err)
		}
	})
	return sourceFixture{service: service, document: document}
}

func runGit(t testing.TB, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func keyMessage(value string) tea.KeyPressMsg {
	code := []rune(value)[0]
	switch value {
	case "enter":
		code = tea.KeyEnter
	}
	return tea.KeyPressMsg(tea.Key{Text: value, Code: code})
}
