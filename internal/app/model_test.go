package app

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/review"
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

	_, _ = model.Update(keyMessage("c"))
	if model.mode != ModeCommentEdit {
		t.Fatalf("comment edit did not start: mode=%d", model.mode)
	}
	for _, character := range "fix this" {
		_, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: string(character), Code: character}))
	}
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	if model.mode != ModeNormal {
		t.Fatalf("comment editor mode = %d after save", model.mode)
	}
	settleModelWork(t, model, coordinator)
	comments := model.review.Comments()
	if len(comments) != 1 || comments[0].Body().String() != "fix this" {
		t.Fatalf("saved comments = %+v", comments)
	}
	side, start, end, ok := comments[0].Target().Lines()
	if !ok || side != review.LineNew || start != end || start.Uint32() != 2 {
		t.Fatalf("saved target = side %d start %d end %d ok %v", side, start, end, ok)
	}
	model.panels = model.panels.with(workspacePanelComments)
	if !strings.Contains(model.View().Content, "fix this") {
		t.Fatal("wide view does not expose the saved comment")
	}
}

func TestCommentEditorSynchronizesPastedNewAndEditedDrafts(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "original\nadded\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitForPatch(t, model, coordinator, active)
	beginTestLineComment(t, model)

	_, _ = model.Update(tea.PasteMsg{Content: "pasted\nbody"})
	draft, ok := model.review.Draft()
	if !ok || draft.Body() != "pasted\nbody" || model.commentEditor.Value() != draft.Body() {
		t.Fatalf("pasted draft = %q, editor = %q, present=%v", draft.Body(), model.commentEditor.Value(), ok)
	}
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	settleModelWork(t, model, coordinator)
	comments := model.review.Comments()
	if len(comments) != 1 || comments[0].Body().String() != "pasted\nbody" {
		t.Fatalf("saved pasted comments = %+v", comments)
	}

	model.commentCursor = 0
	model.beginEditComment()
	if model.review.CommentMutationOperation() == 0 {
		t.Fatalf(
			"editing pasted comment operation = %d",
			model.review.CommentMutationOperation(),
		)
	}
	settleModelWork(t, model, coordinator)
	if model.mode != ModeCommentEdit {
		t.Fatalf("prepared edit mode = %v", model.mode)
	}
	_, _ = model.Update(tea.PasteMsg{Content: " edited"})
	draft, ok = model.review.Draft()
	if !ok || draft.Body() != "pasted\nbody edited" || model.commentEditor.Value() != draft.Body() {
		t.Fatalf("edited pasted draft = %q, editor = %q, present=%v", draft.Body(), model.commentEditor.Value(), ok)
	}
}

func TestCommentEditorRendersPlainCommentTitle(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "original\nadded\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	active, _ := model.review.ActiveFile()
	waitForPatch(t, model, coordinator, active)
	beginTestLineComment(t, model)

	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Comment · alpha.txt") || strings.Contains(view, "[issue]") {
		t.Fatalf("comment editor title:\n%s", view)
	}
}

func TestExportPickerDefaultsAndTogglesUnmappableComments(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	model.openExportPicker(exportSelection{
		format: review.ExportMarkdown, destination: delivery.DestinationClipboard,
		options: review.DefaultExportOptions(),
	})
	if model.overlay != OverlayExport ||
		model.exportPicker.options.Has(review.ExportUnmappable) {
		t.Fatalf("default picker = %#v", model.exportPicker)
	}
	model.applyExportPickerAction(KeyActionToggleExportUnmap)
	if !model.exportPicker.options.Has(review.ExportUnmappable) {
		t.Fatalf("toggled picker = %#v", model.exportPicker)
	}
	modal, _ := model.exportModalView(80, 20, newSemanticStyles(model.settings))
	if !strings.Contains(modal, "[✓] unmappable comments") {
		t.Fatalf("picker view omitted unmappable checkbox:\n%s", modal)
	}
}

func TestCommentEditorRejectsOversizedPasteWithoutMovingLogicalCursor(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "original\nadded\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitForPatch(t, model, coordinator, active)
	beginTestLineComment(t, model)
	_, _ = model.Update(tea.PasteMsg{Content: "alpha\nomega"})
	model.commentEditor.CursorUp()
	model.commentEditor.SetCursorColumn(2)
	wantLine := model.commentEditor.Line()
	wantColumn := model.commentEditor.Column()
	wantBody := model.commentEditor.Value()

	_, _ = model.Update(tea.PasteMsg{Content: strings.Repeat("界", 21_844)})
	draft, ok := model.review.Draft()
	if !ok || model.commentEditor.Value() != wantBody || draft.Body() != wantBody {
		t.Fatalf("rejected paste = editor %q draft %q present=%v", model.commentEditor.Value(), draft.Body(), ok)
	}
	if model.commentEditor.Line() != wantLine || model.commentEditor.Column() != wantColumn {
		t.Fatalf(
			"rejected paste cursor = %d:%d, want %d:%d",
			model.commentEditor.Line(),
			model.commentEditor.Column(),
			wantLine,
			wantColumn,
		)
	}
	if model.notice == nil || !strings.Contains(string(model.notice.Text), "comment body limit reached") {
		t.Fatalf("rejected paste notice = %+v", model.notice)
	}
}

func TestCommentEditorRejectsRawOversizedPasteBeforeWidget(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "original\nadded\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitForPatch(t, model, coordinator, active)
	beginTestLineComment(t, model)
	_, _ = model.Update(tea.PasteMsg{Content: "kept"})
	_, _ = model.Update(tea.PasteMsg{Content: strings.Repeat("x", int(review.MaxCommentBodyBytes)+1)})
	draft, ok := model.review.Draft()
	if !ok || draft.Body() != "kept" || model.commentEditor.Value() != "kept" {
		t.Fatalf("raw oversized paste changed editor=%q draft=%q", model.commentEditor.Value(), draft.Body())
	}
}

func TestBlockingPresentationPreventsPasteMutation(t *testing.T) {
	conditions := map[string]func(*Model){
		"minimum size": func(model *Model) {
			_, _ = model.Update(tea.WindowSizeMsg{
				Width:  minimumWidth - 1,
				Height: minimumHeight - 1,
			})
		},
		"overlay": func(model *Model) {
			model.overlay = OverlayStatus
		},
	}
	for name, block := range conditions {
		t.Run(name, func(t *testing.T) {
			commentModel, coordinator := newTestModel(t, map[string]string{
				"alpha.txt": "original\nadded\n",
			})
			_, _ = commentModel.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
			active, ok := commentModel.review.ActiveFile()
			if !ok {
				t.Fatal("missing active file")
			}
			waitForPatch(t, commentModel, coordinator, active)
			beginTestLineComment(t, commentModel)
			_, _ = commentModel.Update(tea.PasteMsg{Content: "visible"})
			block(commentModel)
			_, _ = commentModel.Update(tea.PasteMsg{Content: " hidden"})
			draft, ok := commentModel.review.Draft()
			if !ok || draft.Body() != "visible" || commentModel.commentEditor.Value() != "visible" {
				t.Fatalf(
					"blocked paste changed hidden draft: editor=%q draft=%q present=%v",
					commentModel.commentEditor.Value(),
					draft.Body(),
					ok,
				)
			}

			promptModel, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			_ = promptModel.openFind(findCurrent)
			promptModel.searchInput.SetValue("visible")
			block(promptModel)
			_, _ = promptModel.Update(tea.PasteMsg{Content: " hidden"})
			if promptModel.searchInput.Value() != "visible" {
				t.Fatalf("blocked paste changed hidden prompt to %q", promptModel.searchInput.Value())
			}
		})
	}
}

func TestPromptPasteEnforcesByteLimitBeforeAndAfterWidgetUpdate(t *testing.T) {
	for _, find := range []bool{true, false} {
		model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		input := &model.commandInput
		label := "command"
		var command tea.Cmd
		if find {
			label = "find"
			command = model.openFind(findCurrent)
			input = &model.searchInput
		} else {
			command = model.beginCommandPrompt()
		}
		if command == nil {
			t.Fatalf("%s prompt did not focus", label)
		}
		input.SetValue("prefix")
		input.SetCursor(2)
		_, _ = model.Update(tea.PasteMsg{Content: strings.Repeat("x", maxPromptBytes+1)})
		if input.Value() != "prefix" || input.Position() != 2 {
			t.Fatalf("%s oversized raw paste changed prompt to %q at %d", label, input.Value(), input.Position())
		}

		previous := strings.Repeat("é", maxPromptBytes/2-8)
		input.SetValue(previous)
		input.SetCursor(7)
		model.notice = nil
		_, _ = model.Update(tea.PasteMsg{Content: strings.Repeat("é", 16)})
		if input.Value() != previous || input.Position() != 7 {
			t.Fatalf("%s byte-expanding paste changed prompt bytes=%d cursor=%d", label, len(input.Value()), input.Position())
		}
		if find {
			if !strings.Contains(model.find.failure, "query exceeds") {
				t.Fatalf("find prompt limit failure = %q", model.find.failure)
			}
		} else if model.notice == nil || !strings.Contains(string(model.notice.Text), "prompt input limit reached") {
			t.Fatalf("command prompt limit notice = %+v", model.notice)
		}
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
	model.review, err = review.NewReview(testReviewRequest(snapshot))
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
	model.review, err = review.NewReview(testReviewRequest(snapshot))
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
	prepared, ok := model.PendingStdout()
	if !ok || prepared.Format() != review.ExportMarkdown || prepared.Body() != "_No comments._" {
		t.Fatalf("pending stdout = (%d, %q) %v", prepared.Format(), prepared.Body(), ok)
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
	prepared, ok := model.PendingStdout()
	if !ok || prepared.Format() != review.ExportJSON || prepared.Body() != "{\"comments\":[]}\n" {
		t.Fatalf("pending JSON = (%d, %q) %v", prepared.Format(), prepared.Body(), ok)
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
		review.DefaultExportOptions(),
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
	setModelKeybindings(t, model, Keybinding{
		Context: KeyContextNormal,
		Focus:   KeyFocusFileTree,
		Chord:   "j",
		Action:  KeyActionLast,
	})
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
	model.panels = model.panels.with(workspacePanelComments)
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
		{name: "comments", mouseX: wideWidth - 1, hideWidth: compactWidth, hideHeight: 24},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			settleModelWork(t, model, coordinator)
			_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
			if testCase.name == "comments" {
				model.panels = model.panels.with(workspacePanelComments)
			}
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

			setModelKeybindings(t, model, Keybinding{
				Context: KeyContextNormal,
				Focus:   KeyFocusDiff,
				Chord:   "x t",
				Action:  KeyActionToggleWrap,
			})
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

func TestResponsiveLayoutUsesContentWidthBoundaries(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"a.txt": "changed\n"})
	tests := []struct {
		width, height  int
		tree, comments bool
	}{
		{width: 179, height: 30, tree: true, comments: false},
		{width: 180, height: 29, tree: true, comments: false},
		{width: 180, height: 30, tree: true, comments: false},
		{width: 120, height: 24, tree: true, comments: false},
		{width: 120, height: 23, tree: true, comments: false},
		{width: 85, height: 18, tree: true, comments: false},
		{width: 84, height: 18, tree: false, comments: false},
		{width: 85, height: 17, tree: true, comments: false},
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

	model.panels = model.panels.with(workspacePanelComments)
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
