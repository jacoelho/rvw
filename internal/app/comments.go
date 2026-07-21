package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/review"
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
		side   review.LineSide
		number review.LineNumber
	)
	if newLine, exists := line[0].NewLine(); exists {
		side, number = review.LineNew, newLine
	} else if oldLine, exists := line[0].OldLine(); exists {
		side, number = review.LineOld, oldLine
	} else {
		model.notice = &Notice{Kind: NoticeWarning, Text: "the current row cannot anchor a comment"}
		return nil
	}
	target, err := review.LineCommentTarget(active, side, number, number)
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
	target, err := review.FileCommentTarget(active)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return nil
	}
	return model.beginComment(target)
}

func (model *Model) beginSummaryComment() tea.Cmd {
	return model.beginComment(review.ReviewCommentTarget())
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
	var side review.LineSide
	firstLine, firstNew := first[0].NewLine()
	lastLine, lastNew := last[0].NewLine()
	if firstNew && lastNew {
		side = review.LineNew
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
		side = review.LineOld
	}
	if lastLine < firstLine {
		firstLine, lastLine = lastLine, firstLine
	}
	target, err := review.LineCommentTarget(active, side, firstLine, lastLine)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return nil
	}
	return model.beginComment(target)
}

func (model *Model) beginComment(target review.CommentTarget) tea.Cmd {
	if len(model.settings.commentKinds) == 0 {
		return nil
	}
	next, err := model.review.BeginDraft(target, model.settings.commentKinds[0])
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
	return model.commentEditor.Focus()
}

func (model *Model) beginEditComment() tea.Cmd {
	count := model.review.CommentCount()
	if count == 0 || model.commentCursor >= count {
		if model.commentCursor >= count && model.review.Snapshot().RemoteThreadCount() != 0 {
			model.notice = &Notice{Kind: NoticeInformation, Text: "remote comments are read-only"}
		}
		return nil
	}
	model.commentCursor = min(max(model.commentCursor, 0), count-1)
	comment, _ := model.review.CommentAt(model.commentCursor)
	next, err := model.review.BeginEditComment(comment.ID())
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return nil
	}
	model.review = next
	draft, _ := model.review.Draft()
	model.commentEditor.SetValue(draft.Body())
	model.resizeCommentEditor()
	model.mode = ModeCommentEdit
	model.clampDiffTop()
	return model.commentEditor.Focus()
}

func (model *Model) cancelCommentEdit() {
	next, err := model.review.CancelDraft()
	if err == nil {
		model.review = next
	}
	model.commentEditor.Blur()
	model.mode = ModeNormal
}

func (model *Model) saveComment() {
	next, _, err := model.review.SaveDraft()
	if err != nil {
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: "comment must contain non-whitespace text within the configured byte limits",
		}
		return
	}
	model.review = next
	model.commentEditor.Blur()
	model.mode = ModeNormal
	model.commentCursor = max(model.review.CommentCount()-1, 0)
	model.notice = &Notice{Kind: NoticeInformation, Text: "comment saved"}
}

func (model *Model) cycleCommentKind(delta int) {
	draft, ok := model.review.Draft()
	if !ok || len(model.settings.commentKinds) == 0 {
		return
	}
	index := 0
	for position, kind := range model.settings.commentKinds {
		if kind == draft.Kind() {
			index = position
			break
		}
	}
	index = (index + delta + len(model.settings.commentKinds)) % len(model.settings.commentKinds)
	if next, err := model.review.SetDraftKind(model.settings.commentKinds[index]); err == nil {
		model.review = next
	}
}

func (model *Model) deleteComment(id review.CommentID) {
	next, err := model.review.DeleteComment(id)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.commentCursor = min(model.commentCursor, max(model.review.CommentCount()-1, 0))
}

func (model *Model) moveComment(delta int) {
	count := model.commentRowCount()
	if count == 0 {
		model.commentCursor = 0
		return
	}
	model.commentCursor = min(max(model.commentCursor+delta, 0), count-1)
}

func (model *Model) commentRowCount() int {
	return model.review.CommentCount() + model.review.Snapshot().RemoteThreadCount()
}

func (model *Model) beginVisualHunk() {
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
	model.mode = ModeVisual
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
	direction := review.NavigateNext
	if delta < 0 {
		direction = review.NavigatePrevious
		delta = -delta
	}
	if line, exists := model.review.AdjacentHunkLine(
		active,
		model.diffCursor,
		direction,
		uint16(min(delta, 9_999)),
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
