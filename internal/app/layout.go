package app

import tea "charm.land/bubbletea/v2"

const (
	fileTreeMinimumWidth    = 24
	commentMinimumWidth     = 32
	treeDiffMinimumWidth    = unifiedDiffContentWidth + fileTreeMinimumWidth + 1
	commentDiffMinimumWidth = unifiedDiffContentWidth + commentMinimumWidth + 1
	allPanesMinimumWidth    = unifiedDiffContentWidth + fileTreeMinimumWidth +
		commentMinimumWidth + 2
)

type workspacePanel uint8

const (
	workspacePanelFiles workspacePanel = iota + 1
	workspacePanelComments
)

type workspacePanels struct {
	bits uint8
}

func newWorkspacePanels(values ...workspacePanel) workspacePanels {
	var panels workspacePanels
	for _, value := range values {
		panels = panels.with(value)
	}
	return panels
}

func (panels workspacePanels) has(panel workspacePanel) bool {
	return panels.bits&panelBit(panel) != 0
}

func (panels workspacePanels) with(panel workspacePanel) workspacePanels {
	return workspacePanels{bits: panels.bits | panelBit(panel)}
}

func (panels workspacePanels) without(panel workspacePanel) workspacePanels {
	return workspacePanels{bits: panels.bits &^ panelBit(panel)}
}

func (panels workspacePanels) toggle(panel workspacePanel) workspacePanels {
	if panels.has(panel) {
		return panels.without(panel)
	}
	return panels.with(panel)
}

func panelBit(panel workspacePanel) uint8 {
	switch panel {
	case workspacePanelFiles:
		return 1 << 0
	case workspacePanelComments:
		return 1 << 1
	default:
		panic("unknown workspace panel")
	}
}

type workspacePane uint8

const (
	workspacePaneDiff workspacePane = iota + 1
	workspacePaneFiles
	workspacePaneComments
)

type workspaceLayoutKind uint8

const (
	workspaceLayoutDiffOnly workspaceLayoutKind = iota + 1
	workspaceLayoutFilesOnly
	workspaceLayoutCommentsOnly
	workspaceLayoutSplit
)

type boundedPanelRequest struct {
	minimumWidth  int
	minimumHeight int
	maximumWidth  int
	maximumHeight int
	inset         int
}

type boundedPanelLayout struct {
	x           int
	y           int
	width       int
	height      int
	innerWidth  int
	innerHeight int
	bordered    bool
}

func newBoundedPanelLayout(
	width, height int,
	request boundedPanelRequest,
) boundedPanelLayout {
	layout := boundedPanelLayout{width: width, height: height}
	if width >= request.minimumWidth && height >= request.minimumHeight {
		layout.width = min(width-request.inset*2, request.maximumWidth)
		layout.height = min(height-request.inset*2, request.maximumHeight)
		layout.x = (width - layout.width) / 2
		layout.y = (height - layout.height) / 2
		layout.bordered = true
	}
	border := 0
	if layout.bordered {
		border = 1
	}
	layout.innerWidth = max(layout.width-border*2, 1)
	layout.innerHeight = max(layout.height-border*2, 1)
	return layout
}

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
	_ = height
	layout := workspaceLayout{kind: workspaceLayoutDiffOnly, diffWidth: width}
	if model.activePane != workspacePaneDiff && !paneCanSplit(model.activePane, width) {
		return model.exclusiveWorkspaceLayout(width)
	}
	files := model.panels.has(workspacePanelFiles)
	comments := model.panels.has(workspacePanelComments)
	switch {
	case files && comments && width >= allPanesMinimumWidth:
		layout.treeWidth = min(max(width/5, fileTreeMinimumWidth), 40)
		layout.commentsWidth = min(max(width/5, commentMinimumWidth), 40)
	case files && comments:
		switch {
		case model.preferredPane == workspacePaneComments && width >= commentDiffMinimumWidth:
			layout.commentsWidth = min(max(width/5, commentMinimumWidth), 40)
		case width >= treeDiffMinimumWidth:
			layout.treeWidth = min(max(width/5, fileTreeMinimumWidth), 40)
		case width >= commentDiffMinimumWidth:
			layout.commentsWidth = min(max(width/5, commentMinimumWidth), 40)
		default:
			return model.exclusiveWorkspaceLayout(width)
		}
	case files && width >= treeDiffMinimumWidth:
		layout.treeWidth = min(max(width/5, fileTreeMinimumWidth), 40)
	case comments && width >= commentDiffMinimumWidth:
		layout.commentsWidth = min(max(width/5, commentMinimumWidth), 40)
	case width < treeDiffMinimumWidth:
		return model.exclusiveWorkspaceLayout(width)
	}
	if layout.treeWidth != 0 || layout.commentsWidth != 0 {
		layout.kind = workspaceLayoutSplit
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

func (model *Model) exclusiveWorkspaceLayout(width int) workspaceLayout {
	switch model.activePane {
	case workspacePaneFiles:
		if model.panels.has(workspacePanelFiles) {
			return workspaceLayout{kind: workspaceLayoutFilesOnly, treeWidth: width}
		}
	case workspacePaneComments:
		if model.panels.has(workspacePanelComments) {
			return workspaceLayout{kind: workspaceLayoutCommentsOnly, commentsWidth: width}
		}
	case workspacePaneDiff:
	}
	layout := workspaceLayout{kind: workspaceLayoutDiffOnly, diffWidth: width}
	layout.sideBySideFallback = model.settings.diffView == DiffViewSideBySide &&
		width < sideBySideContentWidth+diffCommentAnchorColumns
	return layout
}

func paneCanSplit(pane workspacePane, width int) bool {
	switch pane {
	case workspacePaneDiff:
		return true
	case workspacePaneFiles:
		return width >= treeDiffMinimumWidth
	case workspacePaneComments:
		return width >= commentDiffMinimumWidth
	default:
		return false
	}
}

func panelForPane(pane workspacePane) workspacePanel {
	switch pane {
	case workspacePaneFiles:
		return workspacePanelFiles
	case workspacePaneComments:
		return workspacePanelComments
	default:
		panic("workspace pane has no panel preference")
	}
}

func focusForPane(pane workspacePane) FocusOwner {
	switch pane {
	case workspacePaneFiles:
		return FocusFileTree
	case workspacePaneComments:
		return FocusComments
	default:
		return FocusDiff
	}
}
