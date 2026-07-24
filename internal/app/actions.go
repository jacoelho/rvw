// Package app implements rvw's interactive terminal application.
package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
)

const (
	horizontalScrollStep = 8
	maxHorizontalScroll  = 4_096
)

func (model *Model) applyAction(action keymap.Action, repeat int) tea.Cmd {
	if command, handled := model.applyRestrictedAction(action, repeat); handled {
		return command
	}
	if model.foreground.reloadActive() && !actionAllowedDuringReload(action) {
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: "reload is active; this action is temporarily unavailable",
		}
		return nil
	}
	if actionSupersedesDiffTarget(action) {
		model.foreground.clearPendingDiffTarget()
	}
	if command, handled := model.applyNavigationAction(action, repeat); handled {
		return command
	}
	if command, handled := model.applyReviewAction(action); handled {
		return command
	}
	if command, handled := model.applyViewAction(action); handled {
		return command
	}
	if command, handled := model.applyCommandAction(action); handled {
		return command
	}
	model.notice = &Notice{
		Kind: NoticeWarning,
		Text: NoticeText("unsupported action: " + action.String()),
	}
	return nil
}

func (model *Model) applyRestrictedAction(action keymap.Action, repeat int) (tea.Cmd, bool) {
	if model.belowMinimum() {
		if action == keymap.ActionQuit || action == keymap.ActionInterrupt {
			if model.overlay == OverlayConfirm {
				return nil, true
			}
			return model.requestQuit(), true
		}
		return nil, true
	}
	if model.overlay == OverlayConfirm {
		return model.applyConfirmationAction(action), true
	}
	if model.mode == ModeCommentEdit {
		switch action {
		case keymap.ActionCancel, keymap.ActionInterrupt:
			model.cancelCommentEdit()
		case keymap.ActionSaveComment:
			model.saveComment()
		default:
		}
		return nil, true
	}
	if model.mode == ModeVisual {
		if actionSupersedesDiffTarget(action) {
			model.foreground.clearPendingDiffTarget()
		}
		switch action {
		case keymap.ActionCancel, keymap.ActionInterrupt:
			model.mode = ModeNormal
		case keymap.ActionMoveUp, keymap.ActionExtendUp:
			model.moveDiff(-repeat)
		case keymap.ActionMoveDown, keymap.ActionExtendDown:
			model.moveDiff(repeat)
		case keymap.ActionPageUp:
			model.moveDiff(-max(model.diffHeight(), 1) * repeat)
		case keymap.ActionPageDown:
			model.moveDiff(max(model.diffHeight(), 1) * repeat)
		case keymap.ActionCommentSelection:
			return model.beginSelectionComment(), true
		default:
		}
		return nil, true
	}
	switch model.overlay {
	case OverlayNone:
		return nil, false
	case OverlayExport:
		model.applyExportPickerAction(action)
		return nil, true
	case OverlaySubmit:
		model.applyPublicationAction(action)
		return nil, true
	case OverlayHelp:
		model.applyHelpAction(action, repeat)
		return nil, true
	case OverlayStatus:
		model.applyStatusAction(action, repeat)
		return nil, true
	default:
		if action == keymap.ActionCancel || action == keymap.ActionQuit ||
			action == keymap.ActionInterrupt {
			model.overlay = OverlayNone
		}
		return nil, true
	}
}

func (model *Model) applyNavigationAction(action keymap.Action, repeat int) (tea.Cmd, bool) {
	switch action {
	case keymap.ActionFocusNextPane:
		model.cycleFocus(repeat)
	case keymap.ActionFocusPreviousPane:
		model.cycleFocus(-repeat)
	case keymap.ActionMoveUp:
		return model.move(-repeat), true
	case keymap.ActionMoveDown:
		return model.move(repeat), true
	case keymap.ActionScrollLeft:
		model.scrollDiffHorizontally(-repeat)
	case keymap.ActionScrollRight:
		model.scrollDiffHorizontally(repeat)
	case keymap.ActionPageUp:
		return model.movePage(-repeat), true
	case keymap.ActionPageDown:
		return model.movePage(repeat), true
	case keymap.ActionHalfPageUp:
		model.moveDiff(-max(model.diffHeight()/2, 1) * repeat)
	case keymap.ActionHalfPageDown:
		model.moveDiff(max(model.diffHeight()/2, 1) * repeat)
	case keymap.ActionFirst:
		model.moveFirst()
	case keymap.ActionLast:
		model.moveLast()
	case keymap.ActionNextFile:
		model.moveAndActivateFile(repeat)
	case keymap.ActionPreviousFile:
		model.moveAndActivateFile(-repeat)
	case keymap.ActionNextHunk:
		model.moveHunk(repeat)
	case keymap.ActionPreviousHunk:
		model.moveHunk(-repeat)
	case keymap.ActionNextComment:
		model.moveAndActivateComment(repeat)
	case keymap.ActionPreviousComment:
		model.moveAndActivateComment(-repeat)
	case keymap.ActionNextSearchHit:
		model.moveSearchHit(repeat)
	case keymap.ActionPreviousSearchHit:
		model.moveSearchHit(-repeat)
	default:
		return nil, false
	}
	return nil, true
}

func (model *Model) applyReviewAction(action keymap.Action) (tea.Cmd, bool) {
	switch action {
	case keymap.ActionActivate, keymap.ActionAccept:
		model.activateSelection()
	case keymap.ActionCommentAtCursor:
		return model.beginCursorComment(), true
	case keymap.ActionFileComment:
		return model.beginFileComment(), true
	case keymap.ActionSummaryComment:
		return model.beginSummaryComment(), true
	case keymap.ActionEditComment:
		model.beginEditComment()
	case keymap.ActionDeleteComment:
		model.requestCommentDelete()
	case keymap.ActionExpandContext:
		model.expandPatchContext()
	case keymap.ActionVisualLine, keymap.ActionVisualLines:
		model.mode = ModeVisual
		model.visualAnchor = model.diffCursor
	case keymap.ActionVisualHunk:
		model.beginVisualHunk()
	case keymap.ActionToggleFileReviewed:
		if active, ok := model.review.ActiveFile(); ok {
			if next, err := model.review.ToggleFileReviewed(active); err == nil {
				model.review = next
			}
		}
	case keymap.ActionToggleHunkReviewed:
		model.toggleCurrentHunkReviewed()
	default:
		return nil, false
	}
	return nil, true
}

func (model *Model) applyViewAction(action keymap.Action) (tea.Cmd, bool) {
	switch action {
	case keymap.ActionToggleWrap:
		model.settings.wrap = !model.settings.wrap
	case keymap.ActionToggleView:
		if model.settings.diffView == DiffViewUnified {
			model.settings.diffView = DiffViewSideBySide
		} else {
			model.settings.diffView = DiffViewUnified
		}
	case keymap.ActionAlignTop:
		model.alignDiff(alignTop)
	case keymap.ActionAlignCenter:
		model.alignDiff(alignCenter)
	case keymap.ActionAlignBottom:
		model.alignDiff(alignBottom)
	case keymap.ActionToggleDirectory:
		model.toggleTreeDirectory(treeDirectoryToggle)
	case keymap.ActionExpandDirectory:
		model.toggleTreeDirectory(treeDirectoryExpand)
	case keymap.ActionCollapseDirectory:
		model.toggleTreeDirectory(treeDirectoryCollapse)
	case keymap.ActionToggleHiddenFiles:
		if model.review.ShowHidden() {
			model.review = model.review.HideHiddenFiles()
		} else {
			model.review = model.review.ShowHiddenFiles()
		}
		if model.filterQuery != "" {
			return model.startFileFilter(model.filterQuery), true
		}
		model.selectFileOrder()
	case keymap.ActionToggleComments:
		model.commentsVisible = !model.commentsVisible
		model.clampFocus()
	case keymap.ActionToggleFilePanel:
		model.fileTreeVisible = !model.fileTreeVisible
		model.clampFocus()
	case keymap.ActionHelp:
		model.overlay = OverlayHelp
	case keymap.ActionStatusDetails:
		model.overlay = OverlayStatus
	default:
		return nil, false
	}
	return nil, true
}

func (model *Model) applyCommandAction(action keymap.Action) (tea.Cmd, bool) {
	switch action {
	case keymap.ActionQuit, keymap.ActionInterrupt:
		return model.requestQuit(), true
	case keymap.ActionCancel:
		if operationID := model.foreground.reloadOperation(); operationID != 0 {
			if !model.cancelReloadCollapseCapture() {
				model.coordinator.Cancel(operationID)
				model.notice = &Notice{
					Kind: NoticeInformation,
					Text: "cancelling reload; the current source remains active",
				}
			}
		}
	case keymap.ActionReload:
		model.requestReload()
	case keymap.ActionSearch:
		return model.openFind(findCurrent), true
	case keymap.ActionSearchVisible:
		return model.openFind(findReview), true
	case keymap.ActionFindFile:
		return model.openFind(findFiles), true
	case keymap.ActionFilter:
		command := model.beginCommandPrompt()
		model.commandInput.SetValue("filter ")
		return command, true
	case keymap.ActionCommand:
		return model.beginCommandPrompt(), true
	case keymap.ActionCopyMarkdown:
		model.openExportPicker(exportSelection{
			format:      review.ExportMarkdown,
			destination: delivery.DestinationClipboard,
			options:     review.DefaultExportOptions(),
		})
	case keymap.ActionExportMarkdown:
		model.openExportPicker(exportSelection{
			format:      review.ExportMarkdown,
			destination: delivery.DestinationStdout,
			options:     review.DefaultExportOptions(),
		})
	case keymap.ActionExportJSON:
		model.openExportPicker(exportSelection{
			format:      review.ExportJSON,
			destination: delivery.DestinationStdout,
			options:     review.DefaultExportOptions(),
		})
	case keymap.ActionOpenSubmit:
		model.openPublication()
	default:
		return nil, false
	}
	return nil, true
}

func actionSupersedesDiffTarget(action keymap.Action) bool {
	switch action {
	case keymap.ActionMoveUp, keymap.ActionMoveDown,
		keymap.ActionPageUp, keymap.ActionPageDown,
		keymap.ActionHalfPageUp, keymap.ActionHalfPageDown,
		keymap.ActionFirst, keymap.ActionLast,
		keymap.ActionActivate, keymap.ActionAccept,
		keymap.ActionExpandContext,
		keymap.ActionNextFile, keymap.ActionPreviousFile,
		keymap.ActionNextHunk, keymap.ActionPreviousHunk,
		keymap.ActionNextComment, keymap.ActionPreviousComment,
		keymap.ActionNextSearchHit, keymap.ActionPreviousSearchHit:
		return true
	default:
		return false
	}
}

func actionAllowedDuringReload(action keymap.Action) bool {
	switch action {
	case keymap.ActionQuit, keymap.ActionInterrupt, keymap.ActionCancel,
		keymap.ActionFocusNextPane, keymap.ActionFocusPreviousPane,
		keymap.ActionMoveUp, keymap.ActionMoveDown,
		keymap.ActionScrollLeft, keymap.ActionScrollRight,
		keymap.ActionPageUp, keymap.ActionPageDown,
		keymap.ActionHalfPageUp, keymap.ActionHalfPageDown,
		keymap.ActionFirst, keymap.ActionLast,
		keymap.ActionNextFile, keymap.ActionPreviousFile,
		keymap.ActionNextHunk, keymap.ActionPreviousHunk,
		keymap.ActionNextComment, keymap.ActionPreviousComment,
		keymap.ActionToggleWrap, keymap.ActionToggleView,
		keymap.ActionAlignTop, keymap.ActionAlignCenter, keymap.ActionAlignBottom,
		keymap.ActionToggleHiddenFiles, keymap.ActionToggleComments,
		keymap.ActionToggleFilePanel, keymap.ActionHelp, keymap.ActionStatusDetails:
		return true
	default:
		return false
	}
}

type diffAlignment uint8

const (
	alignTop diffAlignment = iota + 1
	alignCenter
	alignBottom
)

func (model *Model) scrollDiffHorizontally(direction int) {
	if model.focus != FocusDiff || direction == 0 {
		return
	}
	model.diffLeft = min(
		max(model.diffLeft+direction*horizontalScrollStep, 0),
		maxHorizontalScroll,
	)
}

func (model *Model) alignDiff(alignment diffAlignment) {
	active, ok := model.review.ActiveFile()
	if !ok {
		return
	}
	lines := model.review.Patch(active).Lines
	if lines == 0 {
		model.diffTop = 0
		return
	}
	height := uint32FromInt(max(model.diffHeight(), 1))
	switch alignment {
	case alignTop:
		model.diffTop = model.diffCursor
	case alignCenter:
		if model.diffCursor > height/2 {
			model.diffTop = model.diffCursor - height/2
		} else {
			model.diffTop = 0
		}
	case alignBottom:
		if model.diffCursor >= height-1 {
			model.diffTop = model.diffCursor - height + 1
		} else {
			model.diffTop = 0
		}
	default:
		return
	}
	maximum := uint32(0)
	if lines > height {
		maximum = lines - height
	}
	model.diffTop = min(model.diffTop, maximum)
	model.requestSyntax()
}

func (model *Model) expandPatchContext() {
	if model.settings.patchContext.WholeFile() {
		model.notice = &Notice{Kind: NoticeInformation, Text: "whole-file context is already active"}
		return
	}
	lines, ok := model.settings.patchContext.Lines()
	if !ok {
		return
	}
	value := lines.Uint16()
	if value == 10_000 {
		model.setPatchContext(review.WholeFileContext())
		return
	}
	nextValue := min(max(value*2, value+1), uint16(10_000))
	next, err := review.NewContextLines(nextValue)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	context, err := review.ContextAround(next)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	model.setPatchContext(context)
}

func (model *Model) applyHelpAction(action keymap.Action, repeat int) {
	page := max(model.diffHeight()-4, 1)
	limit := len(model.effectiveBindings(keymap.ContextNormal))
	switch action {
	case keymap.ActionCancel, keymap.ActionInterrupt, keymap.ActionQuit:
		model.overlay = OverlayNone
	case keymap.ActionNextHelpPage, keymap.ActionHelpPageDown:
		model.helpTop += page * repeat
	case keymap.ActionPreviousHelpPage, keymap.ActionHelpPageUp:
		model.helpTop -= page * repeat
	case keymap.ActionHelpLineDown:
		model.helpTop += repeat
	case keymap.ActionHelpLineUp:
		model.helpTop -= repeat
	case keymap.ActionHelpHome:
		model.helpTop = 0
	case keymap.ActionHelpEnd:
		model.helpTop = limit
	default:
	}
	model.helpTop = min(max(model.helpTop, 0), max(limit-page, 0))
}

func (model *Model) applyStatusAction(action keymap.Action, repeat int) {
	_, height := model.renderSize()
	page := max(model.statusOperationRows(height), 1)
	switch action {
	case keymap.ActionCancel, keymap.ActionInterrupt, keymap.ActionQuit:
		model.overlay = OverlayNone
	case keymap.ActionMoveDown:
		model.statusTop += repeat
	case keymap.ActionMoveUp:
		model.statusTop -= repeat
	case keymap.ActionPageDown:
		model.statusTop += page * repeat
	case keymap.ActionPageUp:
		model.statusTop -= page * repeat
	default:
	}
	model.statusTop = min(
		max(model.statusTop, 0),
		max(model.review.OperationCount()-page, 0),
	)
}
