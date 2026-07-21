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
		model.overlay == OverlayNone
	switch {
	case cursorVisible && model.mode == ModeCommentEdit:
		view.Cursor = model.commentEditor.Cursor()
		if view.Cursor != nil {
			_, height := model.renderSize()
			view.Cursor.Y += newCommentEditorLayout(height).editorTop
		}
	case cursorVisible && model.mode == ModeSearch:
		view.Cursor = model.searchInput.Cursor()
		if view.Cursor != nil {
			view.Cursor.Y += 2
		}
	case cursorVisible && model.mode == ModeCommand:
		view.Cursor = model.commandInput.Cursor()
		if view.Cursor != nil {
			view.Cursor.Y += 2
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
		layout.commentsWidth = min(max(width/5, 32), 40)
	case width >= normalWidth && height >= 24:
		if model.fileTreeVisible {
			layout.treeWidth = min(max(width/5, 24), 40)
		}
		if model.commentsVisible &&
			width-layout.treeWidth-32-2 >= unifiedDiffContentWidth+diffCommentAnchorColumns {
			layout.commentsWidth = 32
		}
	case width >= compactWidth && height >= 18:
		if model.commentsVisible {
			if width >= unifiedDiffContentWidth+diffCommentAnchorColumns+32+1 {
				layout.commentsWidth = 32
			}
		} else if model.fileTreeVisible && width > 60+24 {
			layout.treeWidth = 24
		}
	}
	layout.diffWidth = max(
		width-layout.treeWidth-layout.commentsWidth-
			separatorWidth(layout.treeWidth, layout.commentsWidth),
		1,
	)
	layout.sideBySideFallback =
		model.settings.diffView == DiffViewSideBySide &&
			layout.diffWidth < sideBySideContentWidth+diffCommentAnchorColumns
	return layout
}
