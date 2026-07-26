package tui

import (
	"math"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/change"
)

func (model *Model) beginCursorComment() tea.Cmd {
	active, ok := model.review.ActiveFile()
	if !ok {
		return nil
	}
	line := model.review.PatchLines(active, model.diffCursor, 1, 4_096)
	if len(line) != 1 {
		model.notice = &Notice{Kind: NoticeWarning, Text: "select a materialized diff line"}
		return nil
	}
	var (
		side   change.LineSide
		number change.LineNumber
	)
	if newLine, exists := line[0].NewLine(); exists {
		side, number = change.LineNew, newLine
	} else if oldLine, exists := line[0].OldLine(); exists {
		side, number = change.LineOld, oldLine
	} else {
		model.notice = &Notice{Kind: NoticeWarning, Text: "the current row cannot anchor a comment"}
		return nil
	}
	target, err := change.LineCommentTarget(active, side, number, number)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return nil
	}
	return model.beginComment(target)
}

func (model *Model) beginFileComment() tea.Cmd {
	active, ok := model.review.ActiveFile()
	if !ok {
		return nil
	}
	target, err := change.FileCommentTarget(active)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return nil
	}
	return model.beginComment(target)
}

func (model *Model) beginSummaryComment() tea.Cmd {
	return model.beginComment(change.ReviewCommentTarget())
}

func (model *Model) beginSelectionComment() tea.Cmd {
	active, ok := model.review.ActiveFile()
	if !ok {
		return nil
	}
	firstRow := min(model.visualAnchor, model.diffCursor)
	lastRow := max(model.visualAnchor, model.diffCursor)
	first := model.review.PatchLines(active, firstRow, 1, 4_096)
	last := model.review.PatchLines(active, lastRow, 1, 4_096)
	if len(first) != 1 || len(last) != 1 {
		model.notice = &Notice{Kind: NoticeWarning, Text: "selection is not materialized"}
		return nil
	}
	var side change.LineSide
	firstLine, firstNew := first[0].NewLine()
	lastLine, lastNew := last[0].NewLine()
	if firstNew && lastNew {
		side = change.LineNew
	} else {
		firstLine, firstNew = first[0].OldLine()
		lastLine, lastNew = last[0].OldLine()
		if !firstNew || !lastNew {
			model.notice = &Notice{
				Kind: NoticeWarning,
				Text: "selection must have line identity on one side of the diff",
			}
			return nil
		}
		side = change.LineOld
	}
	if lastLine < firstLine {
		firstLine, lastLine = lastLine, firstLine
	}
	target, err := change.LineCommentTarget(active, side, firstLine, lastLine)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return nil
	}
	return model.beginComment(target)
}

func (model *Model) beginComment(target change.CommentTarget) tea.Cmd {
	next, err := model.review.BeginDraft(target)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return nil
	}
	model.review = next
	model.commentEditor.Reset()
	model.commentEditor.SetValue("")
	model.resizeCommentEditor()
	model.mode = ModeCommentEdit
	model.clampDiffTop()
	model.abandonChord()
	return nil
}

func (model *Model) beginEditComment() {
	count := model.review.CommentCount()
	if !model.editableLocalCommentSelected() {
		if model.commentCursor >= count && model.review.Snapshot().RemoteThreadCount() != 0 {
			model.notice = &Notice{Kind: NoticeInformation, Text: remoteCommentsReadOnly}
		} else {
			model.notice = &Notice{Kind: NoticeInformation, Text: "no editable local comment selected"}
		}
		return
	}
	model.selectCommentIndex(model.commentCursor)
	comment, _ := model.review.CommentAt(model.commentCursor)
	model.startCommentMutation(
		func(operation change.OperationID) (change.Review, change.CommentMutation, error) {
			return model.review.StartBeginEditComment(operation, comment.ID())
		},
	)
}

func (model *Model) editableLocalCommentSelected() bool {
	return model.commentCursor >= 0 && model.commentCursor < model.review.CommentCount() &&
		model.commentRowSelected(model.commentCursor)
}

func (model *Model) cancelCommentEdit() {
	next, err := model.review.CancelDraft()
	if err == nil {
		model.review = next
	}
	model.mode = ModeNormal
}

func (model *Model) saveComment() {
	if model.startCommentMutation(
		func(operation change.OperationID) (change.Review, change.CommentMutation, error) {
			return model.review.StartSaveComment(operation)
		},
	) {
		model.mode = ModeNormal
	}
}

func (model *Model) deleteComment(id change.CommentID) {
	model.startCommentMutation(
		func(operation change.OperationID) (change.Review, change.CommentMutation, error) {
			return model.review.StartDeleteComment(operation, id)
		},
	)
}

func (model *Model) moveComment(delta int) {
	count := model.commentRowCount()
	if count == 0 {
		model.selectCommentIndex(0)
		return
	}
	if model.commentSelectionKnown && model.commentSelection.kind == 0 {
		if delta < 0 {
			model.selectCommentIndex(count - 1)
		} else {
			model.selectCommentIndex(0)
		}
		return
	}
	model.selectCommentIndex(model.commentCursor + delta)
}

func (model *Model) moveAndActivateComment(delta int) {
	model.moveComment(delta)
	model.activateCommentSelection()
}

func (model *Model) activateCommentSelection() {
	localCount := model.review.CommentCount()
	if model.commentCursor < 0 || model.commentCursor >= model.commentRowCount() {
		return
	}
	if !model.commentRowSelected(model.commentCursor) {
		return
	}
	if model.commentCursor < localCount {
		comment, ok := model.review.CommentAt(model.commentCursor)
		if !ok {
			return
		}
		model.activateLocalComment(comment)
		return
	}
	thread, ok := model.review.Snapshot().RemoteThread(model.commentCursor - localCount)
	if ok {
		model.activateRemoteThread(thread)
	}
}

func (model *Model) activateLocalComment(comment change.Comment) {
	target := comment.Target()
	if target.Kind() == change.TargetReview {
		model.foreground.clearPendingDiffTarget()
		model.notice = &Notice{Kind: NoticeWarning, Text: "review comment has no file or line"}
		return
	}
	file, mapped := target.FileID()
	if !mapped || comment.Unmappable() {
		model.foreground.clearPendingDiffTarget()
		model.notice = &Notice{Kind: NoticeWarning, Text: commentFileOutsideReview}
		return
	}
	if target.Kind() == change.TargetFile {
		model.activateCommentFile(file, NoticeInformation, "file comment has no line")
		return
	}
	side, _, end, lineTarget := target.Lines()
	if !lineTarget {
		model.foreground.clearPendingDiffTarget()
		model.notice = &Notice{Kind: NoticeWarning, Text: "comment target is invalid"}
		return
	}
	if comment.Stale() {
		model.activateCommentFile(file, NoticeWarning, "comment line is stale; showing its file")
		return
	}
	model.activateCommentLine(file, side, end)
}

func (model *Model) activateRemoteThread(thread change.RemoteThread) {
	file, mapping := thread.FileMapping()
	switch mapping {
	case change.RemoteMappingAmbiguous:
		model.foreground.clearPendingDiffTarget()
		model.notice = &Notice{Kind: NoticeWarning, Text: "remote comment path is ambiguous in this review"}
		return
	case change.RemoteMappingMissing:
		model.foreground.clearPendingDiffTarget()
		model.notice = &Notice{Kind: NoticeWarning, Text: "remote comment file is not in this review"}
		return
	case change.RemoteMappingMapped:
	default:
		model.foreground.clearPendingDiffTarget()
		model.notice = &Notice{Kind: NoticeWarning, Text: "remote comment mapping is invalid"}
		return
	}
	line, hasLine := thread.Line()
	if !hasLine {
		model.activateCommentFile(file, NoticeInformation, "remote comment has no line")
		return
	}
	if thread.Outdated() {
		model.activateCommentFile(file, NoticeWarning, "remote comment line is outdated; showing its file")
		return
	}
	model.activateCommentLine(file, thread.Side(), line)
}

func (model *Model) activateCommentFile(
	file change.FileID,
	kind NoticeKind,
	message NoticeText,
) {
	model.foreground.clearPendingDiffTarget()
	if !model.activateFile(file) {
		model.notice = &Notice{Kind: NoticeWarning, Text: commentFileOutsideReview}
		return
	}
	model.focusReturn = 0
	model.setFocus(FocusDiff)
	model.notice = &Notice{Kind: kind, Text: message}
	if model.review.Patch(file).Status == change.PatchReady {
		model.requestSyntax()
		return
	}
	model.requestActivePatch()
}

func (model *Model) activateCommentLine(
	fileID change.FileID,
	side change.LineSide,
	line change.LineNumber,
) {
	file, exists := model.review.Snapshot().File(fileID)
	if !exists {
		model.foreground.clearPendingDiffTarget()
		model.notice = &Notice{Kind: NoticeWarning, Text: commentFileOutsideReview}
		return
	}
	target := newSourceLineTarget(
		model.review.WorkspaceRevision(),
		file.PatchHandle(),
		side,
		line,
	)
	model.foreground.clearPendingDiffTarget()
	if !model.activateFile(fileID) {
		model.notice = &Notice{Kind: NoticeWarning, Text: commentFileOutsideReview}
		return
	}
	model.focusReturn = 0
	model.setFocus(FocusDiff)
	if model.review.Patch(fileID).Status == change.PatchReady {
		model.applyDiffTarget(target)
		return
	}
	if !model.foreground.setPendingDiffTarget(target) {
		model.notice = &Notice{Kind: NoticeError, Text: "comment navigation target is invalid"}
		return
	}
	model.requestActivePatch()
}

func (model *Model) commentRowCount() int {
	return model.review.CommentCount() + model.review.Snapshot().RemoteThreadCount()
}

func (model *Model) beginVisualHunk() {
	if !model.beginVisualSelection() {
		return
	}
	active, ok := model.review.ActiveFile()
	if !ok {
		return
	}
	lines := model.review.PatchLines(active, model.diffCursor, 1, 4_096)
	if len(lines) != 1 {
		return
	}
	hunk, ok := lines[0].Hunk(active)
	if !ok {
		model.notice = &Notice{Kind: NoticeWarning, Text: "current row is not in a hunk"}
		return
	}
	first, last, ok := model.review.HunkRows(hunk)
	if !ok {
		model.notice = &Notice{Kind: NoticeWarning, Text: "hunk has no commentable rows"}
		return
	}
	model.visualAnchor = first
	model.diffCursor = last
	model.moveDiff(0)
}

func (model *Model) beginVisualSelection() bool {
	if model.focus != FocusDiff {
		model.notice = &Notice{Kind: NoticeWarning, Text: "visual selection requires diff focus"}
		return false
	}
	active, ok := model.review.ActiveFile()
	if !ok || model.review.Patch(active).Status != change.PatchReady {
		model.notice = &Notice{Kind: NoticeWarning, Text: "visual selection requires a ready patch"}
		return false
	}
	model.mode = ModeVisual
	model.visualAnchor = model.diffCursor
	model.abandonChord()
	return true
}

func (model *Model) toggleCurrentHunkReviewed() {
	active, ok := model.review.ActiveFile()
	if !ok {
		return
	}
	lines := model.review.PatchLines(active, model.diffCursor, 1, 4_096)
	if len(lines) != 1 {
		return
	}
	hunk, ok := lines[0].Hunk(active)
	if !ok {
		model.notice = &Notice{Kind: NoticeWarning, Text: "current row is not in a hunk"}
		return
	}
	if next, err := model.review.ToggleHunkReviewed(hunk); err == nil {
		model.review = next
	}
}

func (model *Model) moveHunk(delta int) {
	active, ok := model.review.ActiveFile()
	if !ok || delta == 0 {
		return
	}
	direction := change.NavigateNext
	if delta < 0 {
		direction = change.NavigatePrevious
		if delta == math.MinInt {
			delta = math.MaxInt
		} else {
			delta = -delta
		}
	}
	if line, exists := model.review.AdjacentHunkLine(
		active,
		model.diffCursor,
		direction,
		uint16FromInt(min(delta, 9_999)),
	); exists {
		model.diffCursor = line
		model.moveDiff(0)
	}
}

func (model *Model) moveAndActivateFile(delta int) {
	model.moveFile(delta)
	model.activateSelection()
}

func (model *Model) resizeCommentEditor() {
	width, height := model.renderSize()
	layout := newCommentEditorLayout(height)
	model.commentEditor.SetWidth(max(width, 1))
	model.commentEditor.SetHeight(layout.editorHeight)
}
