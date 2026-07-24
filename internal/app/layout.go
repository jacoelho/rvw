package app

import tea "charm.land/bubbletea/v2"

func (model *Model) completeView(content string, mouseHits mouseHitMap) tea.View {
	view := tea.NewView(content)
	view.AltScreen = true
	view.ReportFocus = true
	view.DisableBracketedPasteMode = false
	view.WindowTitle = "rvw"
	view.KeyboardEnhancements.ReportEventTypes = true
	if model.settings.mouse {
		view.MouseMode = tea.MouseModeCellMotion
		view.OnMouse = mouseHits.command
	} else {
		view.MouseMode = tea.MouseModeNone
	}
	if model.coordinatorStats.Accepted != 0 {
		view.ProgressBar = tea.NewProgressBar(tea.ProgressBarIndeterminate, 0)
	}
	cursorVisible := model.size.Known &&
		!model.belowMinimum() &&
		(model.overlay == OverlayNone || model.overlay == OverlayFind)
	switch {
	case cursorVisible && model.overlay == OverlayFind:
		view.Cursor = model.searchInput.Cursor()
		if view.Cursor != nil {
			width, height := model.renderSize()
			layout := newFindModalLayout(width, height)
			view.Cursor.X += layout.inputX
			view.Cursor.Y += layout.inputY
		}
	case cursorVisible && model.mode == ModeCommentEdit:
		view.Cursor = model.commentEditor.Cursor()
		if view.Cursor != nil {
			_, height := model.renderSize()
			view.Cursor.Y += newCommentEditorLayout(height).editorTop
		}
	case cursorVisible && model.mode == ModeCommand:
		view.Cursor = model.commandInput.Cursor()
		if view.Cursor != nil {
			_, height := model.renderSize()
			view.Cursor.Y += height - 1
		}
	}
	return view
}

func (model *Model) workspaceLayout(width, height int) workspaceLayout {
	layout := workspaceLayout{diffWidth: width}
	switch {
	case width >= wideWidth && height >= 30:
		if model.fileTreeVisible {
			layout.treeWidth = min(max(width/4, 24), 40)
		}
		if model.commentPaneVisibility != paneVisibilityHidden {
			layout.commentsWidth = min(max(width/5, 32), 40)
		}
	case width >= normalWidth && height >= 24:
		if model.fileTreeVisible {
			layout.treeWidth = min(max(width/5, 24), 40)
		}
		if model.commentPaneVisibility == paneVisibilityShown &&
			width-layout.treeWidth-32-2 >= unifiedDiffContentWidth+diffCommentAnchorColumns {
			layout.commentsWidth = 32
		}
	case width >= compactWidth && height >= 18:
		if model.commentPaneVisibility == paneVisibilityShown {
			if width >= unifiedDiffContentWidth+diffCommentAnchorColumns+32+1 {
				layout.commentsWidth = 32
			}
		} else if model.fileTreeVisible && width > 60+24 {
			layout.treeWidth = 24
		}
	}
	layout.diffWidth = max(width-layout.treeWidth-layout.commentsWidth-
		separatorWidth(layout.treeWidth, layout.commentsWidth), 1)
	switch model.singlePaneOverride {
	case FocusFileTree:
		if model.fileTreeVisible && layout.treeWidth == 0 {
			layout.treeWidth = width
			layout.diffWidth = 0
			layout.commentsWidth = 0
		}
	case FocusComments:
		if model.commentPaneVisibility != paneVisibilityHidden && layout.commentsWidth == 0 {
			layout.treeWidth = 0
			layout.diffWidth = 0
			layout.commentsWidth = width
		}
	case 0, FocusDiff:
	}
	layout.sideBySideFallback =
		model.settings.diffView == DiffViewSideBySide &&
			layout.diffWidth != 0 &&
			layout.diffWidth < sideBySideContentWidth+diffCommentAnchorColumns
	return layout
}

func (layout workspaceLayout) paneCount() int {
	count := 0
	if layout.treeWidth != 0 {
		count++
	}
	if layout.diffWidth != 0 {
		count++
	}
	if layout.commentsWidth != 0 {
		count++
	}
	return count
}
