package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/review"
)

const (
	horizontalScrollStep = 8
	maxHorizontalScroll  = 4_096
)

func (model *Model) applyAction(action KeyAction, repeat int) tea.Cmd {
	if model.overlay == OverlayConfirm {
		return model.applyConfirmationAction(action)
	}
	if model.belowMinimum() {
		switch action {
		case KeyActionQuit, KeyActionInterrupt:
			return model.requestQuit()
		}
		return nil
	}
	if model.mode == ModeCommentEdit {
		switch action {
		case KeyActionCancel, KeyActionInterrupt:
			model.cancelCommentEdit()
		case KeyActionSaveComment:
			model.saveComment()
		case KeyActionNextCommentKind:
			model.cycleCommentKind(1)
		case KeyActionPreviousCommentKind:
			model.cycleCommentKind(-1)
		}
		return nil
	}
	if model.mode == ModeVisual {
		switch action {
		case KeyActionCancel, KeyActionInterrupt:
			model.mode = ModeNormal
		case KeyActionMoveUp, KeyActionExtendUp:
			model.moveDiff(-repeat)
		case KeyActionMoveDown, KeyActionExtendDown:
			model.moveDiff(repeat)
		case KeyActionPageUp:
			model.moveDiff(-max(model.diffHeight(), 1) * repeat)
		case KeyActionPageDown:
			model.moveDiff(max(model.diffHeight(), 1) * repeat)
		case KeyActionCommentSelection:
			return model.beginSelectionComment()
		}
		return nil
	}
	if model.overlay != OverlayNone {
		if model.overlay == OverlayExport {
			return model.applyExportPickerAction(action)
		}
		if model.overlay == OverlaySubmit {
			return model.applyPublicationAction(action)
		}
		if model.overlay == OverlayHelp {
			model.applyHelpAction(action, repeat)
			return nil
		}
		if model.overlay == OverlayStatus {
			model.applyStatusAction(action, repeat)
			return nil
		}
		if action == KeyActionCancel || action == KeyActionQuit ||
			action == KeyActionInterrupt {
			model.overlay = OverlayNone
		}
		return nil
	}
	if model.reloadOperation != 0 && !actionAllowedDuringReload(action) {
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: "reload is active; this action is temporarily unavailable",
		}
		return nil
	}
	switch action {
	case KeyActionQuit, KeyActionInterrupt:
		return model.requestQuit()
	case KeyActionCancel:
		if model.reloadOperation != 0 {
			model.coordinator.Cancel(model.reloadOperation)
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "cancelling reload; the current source remains active",
			}
		}
	case KeyActionFocusNextPane:
		model.cycleFocus(repeat)
	case KeyActionFocusPreviousPane:
		model.cycleFocus(-repeat)
	case KeyActionMoveUp:
		return model.move(-repeat)
	case KeyActionMoveDown:
		return model.move(repeat)
	case KeyActionScrollLeft:
		model.scrollDiffHorizontally(-repeat)
	case KeyActionScrollRight:
		model.scrollDiffHorizontally(repeat)
	case KeyActionPageUp:
		return model.movePage(-repeat)
	case KeyActionPageDown:
		return model.movePage(repeat)
	case KeyActionHalfPageUp:
		model.moveDiff(-max(model.diffHeight()/2, 1) * repeat)
	case KeyActionHalfPageDown:
		model.moveDiff(max(model.diffHeight()/2, 1) * repeat)
	case KeyActionFirst:
		model.moveFirst()
	case KeyActionLast:
		model.moveLast()
	case KeyActionActivate, KeyActionAccept:
		model.activateSelection()
	case KeyActionCommentAtCursor:
		return model.beginCursorComment()
	case KeyActionFileComment:
		return model.beginFileComment()
	case KeyActionSummaryComment:
		return model.beginSummaryComment()
	case KeyActionEditComment:
		return model.beginEditComment()
	case KeyActionDeleteComment:
		model.requestCommentDelete()
	case KeyActionExpandContext:
		model.expandPatchContext()
	case KeyActionVisualLine, KeyActionVisualLines:
		model.mode = ModeVisual
		model.visualAnchor = model.diffCursor
	case KeyActionVisualHunk:
		model.beginVisualHunk()
	case KeyActionToggleFileReviewed:
		if active, ok := model.review.ActiveFile(); ok {
			if next, err := model.review.ToggleFileReviewed(active); err == nil {
				model.review = next
			}
		}
	case KeyActionToggleHunkReviewed:
		model.toggleCurrentHunkReviewed()
	case KeyActionToggleWrap:
		model.settings.wrap = !model.settings.wrap
	case KeyActionToggleView:
		if model.settings.diffView == DiffViewUnified {
			model.settings.diffView = DiffViewSideBySide
		} else {
			model.settings.diffView = DiffViewUnified
		}
	case KeyActionAlignTop:
		model.alignDiff(alignTop)
	case KeyActionAlignCenter:
		model.alignDiff(alignCenter)
	case KeyActionAlignBottom:
		model.alignDiff(alignBottom)
	case KeyActionToggleDirectory:
		model.toggleTreeDirectory(treeDirectoryToggle)
	case KeyActionExpandDirectory:
		model.toggleTreeDirectory(treeDirectoryExpand)
	case KeyActionCollapseDirectory:
		model.toggleTreeDirectory(treeDirectoryCollapse)
	case KeyActionToggleHiddenFiles:
		if model.review.ShowHidden() {
			model.review = model.review.HideHiddenFiles()
		} else {
			model.review = model.review.ShowHiddenFiles()
		}
		if model.filterQuery != "" {
			return model.startFileFilter(model.filterQuery)
		}
		model.selectFileOrder()
	case KeyActionToggleComments:
		model.commentsVisible = !model.commentsVisible
		model.clampFocus()
	case KeyActionToggleFilePanel:
		model.fileTreeVisible = !model.fileTreeVisible
		model.clampFocus()
	case KeyActionNextFile:
		model.moveAndActivateFile(repeat)
	case KeyActionPreviousFile:
		model.moveAndActivateFile(-repeat)
	case KeyActionNextHunk:
		model.moveHunk(repeat)
	case KeyActionPreviousHunk:
		model.moveHunk(-repeat)
	case KeyActionNextComment:
		model.moveComment(repeat)
	case KeyActionPreviousComment:
		model.moveComment(-repeat)
	case KeyActionNextSearchHit:
		model.moveSearchHit(repeat)
	case KeyActionPreviousSearchHit:
		model.moveSearchHit(-repeat)
	case KeyActionHelp:
		model.overlay = OverlayHelp
	case KeyActionStatusDetails:
		model.overlay = OverlayStatus
	case KeyActionReload:
		model.requestReload()
	case KeyActionSearch, KeyActionCommand, KeyActionFilter:
		if action == KeyActionSearch {
			return model.beginPrompt(ModeSearch)
		}
		if action == KeyActionFilter {
			command := model.beginPrompt(ModeCommand)
			model.commandInput.SetValue("filter ")
			return command
		}
		return model.beginPrompt(ModeCommand)
	case KeyActionCopyMarkdown:
		model.openExportPicker(exportSelection{
			format:      review.ExportMarkdown,
			destination: delivery.DestinationClipboard,
		})
	case KeyActionExportMarkdown:
		model.openExportPicker(exportSelection{
			format:      review.ExportMarkdown,
			destination: delivery.DestinationStdout,
		})
	case KeyActionExportJSON:
		model.openExportPicker(exportSelection{
			format:      review.ExportJSON,
			destination: delivery.DestinationStdout,
		})
	case KeyActionOpenSubmit:
		model.startPublicationCheck()
	default:
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText("unsupported action: " + action.String()),
		}
	}
	return nil
}

func actionAllowedDuringReload(action KeyAction) bool {
	switch action {
	case KeyActionQuit, KeyActionInterrupt, KeyActionCancel,
		KeyActionFocusNextPane, KeyActionFocusPreviousPane,
		KeyActionMoveUp, KeyActionMoveDown,
		KeyActionScrollLeft, KeyActionScrollRight,
		KeyActionPageUp, KeyActionPageDown,
		KeyActionHalfPageUp, KeyActionHalfPageDown,
		KeyActionFirst, KeyActionLast,
		KeyActionNextFile, KeyActionPreviousFile,
		KeyActionNextHunk, KeyActionPreviousHunk,
		KeyActionNextComment, KeyActionPreviousComment,
		KeyActionToggleWrap, KeyActionToggleView,
		KeyActionAlignTop, KeyActionAlignCenter, KeyActionAlignBottom,
		KeyActionToggleHiddenFiles, KeyActionToggleComments,
		KeyActionToggleFilePanel, KeyActionHelp, KeyActionStatusDetails:
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
	height := uint32(max(model.diffHeight(), 1))
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

func (model *Model) applyHelpAction(action KeyAction, repeat int) {
	page := max(model.diffHeight()-4, 1)
	limit := len(model.effectiveBindings(KeyContextNormal))
	switch action {
	case KeyActionCancel, KeyActionInterrupt, KeyActionQuit:
		model.overlay = OverlayNone
	case KeyActionNextHelpPage, KeyActionHelpPageDown:
		model.helpTop += page * repeat
	case KeyActionPreviousHelpPage, KeyActionHelpPageUp:
		model.helpTop -= page * repeat
	case KeyActionHelpLineDown:
		model.helpTop += repeat
	case KeyActionHelpLineUp:
		model.helpTop -= repeat
	case KeyActionHelpHome:
		model.helpTop = 0
	case KeyActionHelpEnd:
		model.helpTop = limit
	}
	model.helpTop = min(max(model.helpTop, 0), max(limit-page, 0))
}

func (model *Model) applyStatusAction(action KeyAction, repeat int) {
	_, height := model.renderSize()
	page := max(model.statusOperationRows(height), 1)
	switch action {
	case KeyActionCancel, KeyActionInterrupt, KeyActionQuit:
		model.overlay = OverlayNone
	case KeyActionMoveDown:
		model.statusTop += repeat
	case KeyActionMoveUp:
		model.statusTop -= repeat
	case KeyActionPageDown:
		model.statusTop += page * repeat
	case KeyActionPageUp:
		model.statusTop -= page * repeat
	}
	model.statusTop = min(
		max(model.statusTop, 0),
		max(model.review.OperationCount()-page, 0),
	)
}
