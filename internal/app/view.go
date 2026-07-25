package app

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
)

const (
	minimumWidth     = 20
	minimumHeight    = 6
	compactWidth     = 80
	normalWidth      = 120
	wideWidth        = 160
	maxRenderWidth   = 1_000
	maxRenderHeight  = 300
	viewportOverscan = 8

	diffCommentAnchorColumns = 2
	unifiedDiffContentWidth  = 60
	sideBySideContentWidth   = 100

	commentEditorChromeHeight    = 2
	commentEditorMaxHeight       = 6
	commentEditorWorkspaceHeight = 3
)

type workspaceLayout struct {
	treeWidth          int
	diffWidth          int
	commentsWidth      int
	sideBySideFallback bool
}

type commentEditorLayout struct {
	workspaceHeight int
	editorHeight    int
	editorTop       int
}

type mouseHitMap struct {
	treeRows       []treeNodeIdentity
	diffRows       []uint32
	commentRows    []int
	findRows       []int
	layout         workspaceLayout
	reviewState    uint64
	bodyHeight     int
	findX          int
	findY          int
	findWidth      int
	findHeight     int
	findResultY    int
	findGeneration uint64
}

type mouseTarget uint8

const (
	mouseTargetNone mouseTarget = iota
	mouseTargetTree
	mouseTargetDiff
	mouseTargetComment
	mouseTargetFind
)

type diffLineNumberWidths struct {
	old int
	new int
}

type paneHeaderState uint8

const (
	paneHeaderQuiet paneHeaderState = iota + 1
	paneHeaderFocused
)

var workspaceBindingLabels = map[keymap.Action]string{
	keymap.ActionMoveDown:        nextRowLabel,
	keymap.ActionCommentAtCursor: commentLabel,
	keymap.ActionFileComment:     "file comment",
	keymap.ActionEditComment:     "edit",
	keymap.ActionFocusNextPane:   "next pane",
	keymap.ActionNextFile:        "next",
	keymap.ActionPreviousFile:    "prev",
	keymap.ActionHelp:            helpLabel,
}

// View returns the receiver's view value.
func (model *Model) View() tea.View {
	width, height := model.renderSize()
	var content string
	var mouseHits mouseHitMap
	switch {
	case !model.size.Known:
		content = "rvw\nwaiting for terminal size"
	case model.belowMinimum():
		content = model.minimumView(width, height)
	case model.overlay == OverlayConfirm:
		content = model.confirmationView(width, height)
	case model.mode == ModeCommentEdit:
		content = model.commentEditorView(width, height)
	case model.overlay == OverlayFind:
		layout := model.workspaceLayout(width, height)
		workspace, _ := model.workspaceView(width, height, layout)
		styles := newSemanticStyles(model.settings)
		modal, findLayout, findRows := model.findModalView(width, height, styles)
		content = composeDialog(workspace, modal, findLayout.dialog)
		mouseHits = mouseHitMap{
			findRows:       findRows,
			findX:          findLayout.dialog.inner.x,
			findY:          findLayout.dialog.inner.y,
			findWidth:      findLayout.dialog.inner.width,
			findHeight:     findLayout.dialog.inner.height,
			findResultY:    findLayout.resultY,
			findGeneration: model.find.generation,
		}
	case model.overlay == OverlayHelp:
		content = model.helpView(width, height)
	case model.overlay == OverlayStatus:
		content = model.statusView(width, height)
	case model.overlay == OverlayExport:
		content = model.exportPickerView(width, height)
	case model.overlay == OverlaySubmit:
		content = model.publicationView(width, height)
	default:
		layout := model.workspaceLayout(width, height)
		content, mouseHits = model.workspaceView(width, height, layout)
	}
	return model.completeView(content, mouseHits)
}

func (model *Model) workspaceView(
	width, height int,
	layout workspaceLayout,
) (string, mouseHitMap) {
	styles := newSemanticStyles(model.settings)
	bodyHeight := max(height-2, 1)
	var (
		treeLines    []string
		treeRows     []treeNodeIdentity
		diffLines    []string
		diffRows     []uint32
		commentLines []string
		commentRows  []int
	)
	if layout.treeWidth != 0 {
		treeLines, treeRows = model.fileTreeLines(layout.treeWidth, bodyHeight, styles)
	}
	if layout.diffWidth != 0 {
		diffLines, diffRows = model.diffLines(layout.diffWidth, bodyHeight, styles)
	}
	if layout.commentsWidth != 0 {
		commentLines, commentRows = model.commentLines(
			layout.commentsWidth,
			bodyHeight,
			styles,
		)
	}
	separator := styles.muted.Inherit(styles.panel).Render("│")

	body := make([]string, bodyHeight)
	for row := range bodyHeight {
		diffLine := ""
		if row < len(diffLines) {
			diffLine = diffLines[row]
		}
		sections := make([]string, 0, 3)
		if layout.treeWidth != 0 {
			treeLine := ""
			if row < len(treeLines) {
				treeLine = treeLines[row]
			}
			sections = append(
				sections,
				fitStyledCellWidth(treeLine, layout.treeWidth, styles.panel),
			)
		}
		if layout.diffWidth != 0 {
			sections = append(
				sections,
				fitStyledCellWidth(diffLine, layout.diffWidth, styles.panel),
			)
		}
		if layout.commentsWidth != 0 {
			commentLine := ""
			if row < len(commentLines) {
				commentLine = commentLines[row]
			}
			sections = append(
				sections,
				fitStyledCellWidth(commentLine, layout.commentsWidth, styles.panel),
			)
		}
		body[row] = strings.Join(sections, separator)
	}
	title := model.workspaceHeaderLine(layout, styles)
	footer := model.statusLine(width, styles)
	if model.mode == ModeCommand {
		footer = model.commandBarLine(width, styles)
	}
	lines := make([]string, 0, len(body)+2)
	lines = append(lines, title)
	lines = append(lines, body...)
	lines = append(lines, footer)
	return strings.Join(lines, "\n"), mouseHitMap{
		reviewState: model.review.StateRevision(),
		bodyHeight:  bodyHeight,
		layout:      layout,
		treeRows:    treeRows,
		diffRows:    diffRows,
		commentRows: commentRows,
	}
}

func (hits mouseHitMap) command(message tea.MouseMsg) tea.Cmd {
	if hits.findGeneration != 0 {
		return hits.findCommand(message)
	}
	mouse := message.Mouse()
	if hits.bodyHeight == 0 || mouse.Y < 1 || mouse.Y > hits.bodyHeight {
		return nil
	}
	var focus FocusOwner
	diffStart := 0
	if hits.layout.treeWidth != 0 {
		if mouse.X < hits.layout.treeWidth {
			focus = FocusFileTree
		} else {
			diffStart = hits.layout.treeWidth + 1
		}
	}
	if focus == 0 && hits.layout.diffWidth != 0 {
		focus = FocusDiff
	}
	commentsStart := diffStart + hits.layout.diffWidth
	if hits.layout.diffWidth != 0 {
		commentsStart++
	}
	if hits.layout.commentsWidth != 0 &&
		(hits.layout.diffWidth == 0 || mouse.X >= commentsStart) {
		focus = FocusComments
	}
	if focus == 0 {
		return nil
	}
	action := mouseActionMsg{reviewState: hits.reviewState, focus: focus}
	switch message.(type) {
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft {
			return nil
		}
		row := mouse.Y - 1
		switch focus {
		case FocusFileTree:
			if row < len(hits.treeRows) {
				action.target = mouseTargetTree
				action.tree = hits.treeRows[row]
			}
		case FocusDiff:
			if row < len(hits.diffRows) {
				action.target = mouseTargetDiff
				action.diffLine = hits.diffRows[row]
			}
		case FocusComments:
			if row < len(hits.commentRows) && hits.commentRows[row] >= 0 {
				action.target = mouseTargetComment
				action.commentIndex = hits.commentRows[row]
			}
		}
	case tea.MouseWheelMsg:
		switch mouse.Button {
		case tea.MouseWheelUp:
			action.delta = -3
		case tea.MouseWheelDown:
			action.delta = 3
		default:
			return nil
		}
	default:
		return nil
	}
	return func() tea.Msg { return action }
}

func (hits mouseHitMap) findCommand(message tea.MouseMsg) tea.Cmd {
	mouse := message.Mouse()
	if mouse.X < hits.findX || mouse.X >= hits.findX+hits.findWidth ||
		mouse.Y < hits.findY || mouse.Y >= hits.findY+hits.findHeight {
		return nil
	}
	action := mouseActionMsg{findGeneration: hits.findGeneration}
	switch message.(type) {
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft {
			return nil
		}
		row := mouse.Y - hits.findResultY
		if row < 0 || row >= len(hits.findRows) || hits.findRows[row] < 0 {
			return nil
		}
		action.target = mouseTargetFind
		action.findIndex = hits.findRows[row]
	case tea.MouseWheelMsg:
		switch mouse.Button {
		case tea.MouseWheelUp:
			action.delta = -3
		case tea.MouseWheelDown:
			action.delta = 3
		default:
			return nil
		}
	default:
		return nil
	}
	return func() tea.Msg { return action }
}

func (model *Model) fileTreeLines(
	width, height int,
	styles semanticStyles,
) ([]string, []treeNodeIdentity) {
	tree := model.currentFileTree()
	if width == 0 {
		return nil, nil
	}
	if tree.empty() {
		message := model.emptyFileProjectionText()
		style := styles.muted
		if strings.HasPrefix(message, "Error:") {
			style = styles.failure
		} else if strings.HasPrefix(message, "Loading") ||
			strings.HasPrefix(message, "Filtering") {
			style = styles.pending
		}
		return []string{style.Render(safeAndClip(message, width))}, nil
	}
	window := tree.window(model.treeCursor, height, model.collapsedTree)
	lines := make([]string, 0, len(window))
	rows := make([]treeNodeIdentity, 0, len(window))
	active, activeOK := model.review.ActiveFile()
	snapshot := model.review.Snapshot()
	for _, node := range window {
		label := node.label
		badge := " "
		badgeStyle := lipgloss.NewStyle()
		if !node.identity.directory {
			if file, ok := snapshot.File(node.identity.file); ok {
				if label == "" {
					label = file.DisplayPath()
				}
				badge = fileChangeBadge(file.Change().Kind())
				badgeStyle = fileChangeBadgeStyle(file.Change().Kind(), styles)
			}
		}
		cursor := "  "
		selected := node.identity == model.treeCursor
		if selected {
			cursor = "> "
		}
		activeMarker := " "
		if !node.identity.directory && activeOK && node.identity.file == active {
			activeMarker = "•"
		}
		reviewed := " "
		if !node.identity.directory && model.review.FileReviewed(node.identity.file) {
			reviewed = "✓"
		}
		disclosure := " "
		if node.identity.directory {
			disclosure = "▾"
			index, _ := tree.index(node.identity)
			if model.collapsedTree.isCollapsed(index) {
				disclosure = "▸"
			}
		}
		indent := strings.Repeat("  ", min(node.depth, max(width/2, 0)))
		prefix := cursor + activeMarker + reviewed + disclosure
		suffix := " " + indent + safeAndClip(label, max(width-9-len(indent), 1))
		suffix = fitCellWidth(suffix, max(width-lipgloss.Width(prefix)-1, 0))
		base := lipgloss.NewStyle()
		if model.settings.colorEnabled {
			base = styles.panel
		}
		renderedBadge := badgeStyle.Inherit(base).Render(badge)
		line := base.Render(prefix) + renderedBadge + base.Render(suffix)
		if selected && model.workspaceOwnsInput() && model.focus == FocusFileTree {
			line = styles.activeCursorRow.Render(prefix + badge + suffix)
		}
		lines = append(lines, line)
		rows = append(rows, node.identity)
	}
	return lines, rows
}

func fileChangeBadge(kind review.ChangeKind) string {
	switch kind {
	case review.ChangeAdded:
		return "A"
	case review.ChangeDeleted:
		return "D"
	case review.ChangeModified:
		return "M"
	case review.ChangeRenamed, review.ChangeRenameOnly:
		return "R"
	case review.ChangeCopied:
		return "C"
	case review.ChangeBinary:
		return "B"
	case review.ChangeModeOnly, review.ChangeObjectTypeChanged:
		return "T"
	case review.ChangeSubmodule:
		return "S"
	default:
		return "?"
	}
}

func fileChangeBadgeStyle(kind review.ChangeKind, styles semanticStyles) lipgloss.Style {
	switch kind {
	case review.ChangeAdded:
		return styles.added
	case review.ChangeDeleted:
		return styles.deleted
	default:
		return styles.heading
	}
}

func (model *Model) wrappedLine(value string, width int) []string {
	if !model.settings.wrap || width <= 1 {
		return []string{value}
	}
	if len(value) <= width && !strings.Contains(value, "\n") &&
		lipgloss.Width(value) <= width {
		return []string{value}
	}
	wrapped := lipgloss.Wrap(value, width, "")
	lines := strings.Split(wrapped, "\n")
	return lines[:min(len(lines), maxRenderHeight)]
}

func (model *Model) commentLines(
	width, height int,
	styles semanticStyles,
) ([]string, []int) {
	if width == 0 {
		return nil, nil
	}
	localCount := model.review.CommentCount()
	count := model.commentRowCount()
	if count == 0 {
		remote := model.review.Snapshot().RemoteThreadSummary()
		if remote.Status == review.RemoteThreadsUnavailable ||
			remote.Status == review.RemoteThreadsLimited {
			return []string{styles.warning.Render(safeAndClip(
				"Remote comments "+remote.Status.String()+": "+remote.Detail.String(),
				width,
			))}, nil
		}
		return []string{styles.muted.Render("No comments.")}, nil
	}
	cursor := min(max(model.commentCursor, 0), count-1)
	start := max(cursor-height/2, 0)
	start = min(start, max(count-height, 0))
	lines := make([]string, 0, min(height, count-start))
	rows := make([]int, 0, min(height, count-start))
	snapshot := model.review.Snapshot()
	pendingReady := snapshot.PendingReviewSummary().Status ==
		review.PendingReviewReady
	for index := start; index < min(start+height, count); index++ {
		prefix := "  "
		if index == cursor && model.commentRowSelected(index) {
			prefix = "> "
		}
		var line string
		if index < localCount {
			comment, ok := model.review.CommentAt(index)
			if !ok {
				continue
			}
			target := commentTargetText(comment.Target())
			if pendingReady {
				state := model.review.PendingBindingState(comment.ID()).String()
				target = "[" + state + "] " + target
			}
			line = model.commentPrefix(prefix, width)
			line = appendSafeAndClip(line, target+" — ", width)
			remaining := max(width-lipgloss.Width(line), 0)
			if remaining != 0 {
				line += safeAndClip(comment.Body().String(), remaining)
			}
		} else {
			thread, ok := snapshot.RemoteThread(index - localCount)
			if !ok {
				continue
			}
			state := "open"
			if thread.Resolved() {
				state = "resolved"
			} else if thread.Outdated() {
				state = "outdated"
			}
			location := thread.Path().Display()
			if sourceLine, lineOK := thread.Line(); lineOK {
				location += fmt.Sprintf(":%d", sourceLine.Uint32())
			}
			line = clipStyled(prefix, 0, width)
			line = appendSafeAndClip(
				line,
				"[remote "+state+"] "+location+" — ",
				width,
			)
			comment, commentOK := thread.Comment(0)
			if commentOK {
				author := comment.Author()
				if author == "" {
					author = "deleted user"
				}
				remaining := max(width-lipgloss.Width(line), 0)
				if remaining != 0 {
					line += safeAndClip(author+": "+comment.Body(), remaining)
				}
			}
		}
		line = fitCellWidth(line, width)
		if index == cursor && model.workspaceOwnsInput() && model.focus == FocusComments {
			line = styles.activeCursorRow.Render(line)
		}
		lines = append(lines, line)
		rows = append(rows, index)
	}
	remote := snapshot.RemoteThreadSummary()
	if len(lines) < height && (remote.Status == review.RemoteThreadsUnavailable ||
		remote.Status == review.RemoteThreadsLimited) {
		lines = append(lines, styles.warning.Render(safeAndClip(
			"Remote comments "+remote.Status.String()+": "+remote.Detail.String(),
			width,
		)))
		rows = append(rows, -1)
	}
	return lines, rows
}

func (model *Model) commentPrefix(prefix string, width int) string {
	if !model.settings.colorEnabled {
		return prefix
	}
	return clipStyled(prefix, 0, width)
}

func appendSafeAndClip(line, value string, width int) string {
	remaining := max(width-lipgloss.Width(line), 0)
	if remaining == 0 {
		return line
	}
	return line + safeAndClip(value, remaining)
}

func commentTargetText(target review.CommentTarget) string {
	switch target.Kind() {
	case review.TargetReview:
		return "review"
	case review.TargetFile:
		if path, ok := target.AnchoredPath(); ok {
			return path.Display()
		}
		return "file"
	case review.TargetLine:
		path := "line"
		if value, ok := target.AnchoredPath(); ok {
			path = value.Display()
		}
		side, start, end, ok := target.Lines()
		if !ok {
			return path
		}
		prefix := ""
		if side == review.LineOld {
			prefix = "~"
		}
		if start == end {
			return fmt.Sprintf("%s:%s%d", path, prefix, start.Uint32())
		}
		return fmt.Sprintf("%s:%s%d-%d", path, prefix, start.Uint32(), end.Uint32())
	default:
		return unknownValue
	}
}

func (model *Model) commentEditorView(width, height int) string {
	layout := newCommentEditorLayout(height)
	workspace, _ := model.workspaceView(
		width,
		layout.workspaceHeight,
		model.workspaceLayout(width, layout.workspaceHeight),
	)
	styles := newSemanticStyles(model.settings)
	draft, ok := model.review.Draft()
	if !ok {
		return strings.Join([]string{
			workspace,
			fitCellWidth(styles.failure.Render("Comment editor unavailable"), width),
			strings.Repeat(" ", width),
			model.commentEditor.View(),
		}, "\n")
	}
	title := "Comment · " + commentTargetText(draft.Target())
	help := bindingBar{bindings: model.commentEditorBindings()}.Render(width, styles)
	return strings.Join([]string{
		workspace,
		fitCellWidth(styles.focused.Render(safeAndClip(title, width)), width),
		fitCellWidth(help, width),
		model.commentEditor.View(),
	}, "\n")
}

func newCommentEditorLayout(height int) commentEditorLayout {
	editorHeight := min(
		commentEditorMaxHeight,
		max(height-commentEditorWorkspaceHeight-commentEditorChromeHeight, 1),
	)
	workspaceHeight := max(height-commentEditorChromeHeight-editorHeight, 1)
	return commentEditorLayout{
		workspaceHeight: workspaceHeight,
		editorHeight:    editorHeight,
		editorTop:       workspaceHeight + commentEditorChromeHeight,
	}
}

func (model *Model) workspaceHeaderLine(layout workspaceLayout, styles semanticStyles) string {
	activeText := "no active file"
	if active, ok := model.review.ActiveFile(); ok {
		if file, exists := model.review.Snapshot().File(active); exists {
			activeText = file.DisplayPath()
			if layout.treeWidth == 0 {
				if position, found := slices.BinarySearch(model.fileOrder, active); found && len(model.fileOrder) > 1 {
					activeText = fmt.Sprintf("%d/%d · %s", position+1, len(model.fileOrder), activeText)
				}
			}
			activeText += fmt.Sprintf(
				" · %d/%d reviewed",
				model.review.ReviewedFileCount(),
				model.review.Snapshot().FileCount(),
			)
		}
	}
	diffTitle := "DIFF · " + activeText
	switch model.mode {
	case ModeCommentEdit:
		diffTitle += " · COMMENT"
	case ModeCommand:
		diffTitle += " · COMMAND"
	case ModeVisual:
		diffTitle += " · VISUAL"
	case ModeNormal:
	}
	sections := make([]string, 0, 3)
	if layout.treeWidth != 0 {
		visibleFiles := len(model.fileOrder)
		totalFiles := model.review.Snapshot().FileCount()
		fileCount := fmt.Sprint(totalFiles)
		if visibleFiles != totalFiles {
			fileCount = fmt.Sprintf("%d/%d", visibleFiles, totalFiles)
		}
		title := "FILES · " + fileCount + model.fileProjectionHeaderSuffix()
		sections = append(sections, paneHeader(
			title,
			layout.treeWidth,
			model.workspacePaneHeaderState(FocusFileTree),
			styles,
		))
	}
	if layout.diffWidth != 0 {
		sections = append(sections, paneHeader(
			diffTitle,
			layout.diffWidth,
			model.workspacePaneHeaderState(FocusDiff),
			styles,
		))
	}
	if layout.commentsWidth != 0 {
		sections = append(sections, paneHeader(
			fmt.Sprintf("COMMENTS · %d%s", model.commentRowCount(), model.commentSourceHeaderSuffix()),
			layout.commentsWidth,
			model.workspacePaneHeaderState(FocusComments),
			styles,
		))
	}
	return strings.Join(sections, styles.muted.Inherit(styles.panel).Render("│"))
}

func (model *Model) emptyFileProjectionText() string {
	switch model.fileProjection.workspace {
	case projectionInitial, projectionLoading:
		return "Loading changed files…"
	case projectionFailed:
		return "Error: changed files unavailable — " + string(model.fileProjection.workspaceDetail)
	case projectionReady:
	}
	if model.fileProjection.requestedQuery != model.fileProjection.appliedQuery {
		switch model.fileProjection.filter {
		case projectionLoading:
			return "Filtering files…"
		case projectionFailed:
			return "Error: file filter failed — " + string(model.fileProjection.filterDetail)
		case projectionInitial, projectionReady:
		}
	}
	if model.fileProjection.appliedQuery != "" {
		return "No files match the filter."
	}
	return "No changed files."
}

func (model *Model) fileProjectionHeaderSuffix() string {
	switch model.fileProjection.workspace {
	case projectionInitial, projectionLoading:
		return " · LOADING"
	case projectionFailed:
		return " · ! ERROR"
	case projectionReady:
	}
	if model.fileProjection.requestedQuery != model.fileProjection.appliedQuery {
		switch model.fileProjection.filter {
		case projectionLoading:
			return " · FILTERING"
		case projectionFailed:
			return " · ! FILTER ERROR"
		case projectionInitial, projectionReady:
		}
	}
	return ""
}

func (model *Model) commentSourceHeaderSuffix() string {
	remote := model.review.Snapshot().RemoteThreadSummary()
	if model.foreground.reloadActive() && remote.Status != review.RemoteThreadsNotRequested {
		return " · REMOTE LOADING"
	}
	switch remote.Status {
	case review.RemoteThreadsUnavailable, review.RemoteThreadsLimited:
		return " · ! REMOTE " + strings.ToUpper(remote.Status.String())
	case review.RemoteThreadsNotRequested, review.RemoteThreadsReady:
		return ""
	default:
		return " · ! REMOTE ERROR"
	}
}

func (model *Model) workspacePaneHeaderState(focus FocusOwner) paneHeaderState {
	if model.workspaceOwnsInput() && model.focus == focus {
		return paneHeaderFocused
	}
	return paneHeaderQuiet
}

func (model *Model) workspaceOwnsInput() bool {
	return model.overlay == OverlayNone && (model.mode == ModeNormal || model.mode == ModeVisual)
}

func paneHeader(
	title string,
	width int,
	state paneHeaderState,
	styles semanticStyles,
) string {
	if width <= 0 {
		return ""
	}
	marker := "  "
	style := styles.paneHeader
	if state == paneHeaderFocused {
		marker = "▌ "
		style = styles.focusedPaneHeader
	}
	titleWidth := max(width-lipgloss.Width(marker), 0)
	return style.Inherit(styles.panel).Render(
		fitCellWidth(marker+safeAndClip(title, titleWidth), width),
	)
}

func (model *Model) statusLine(width int, styles semanticStyles) string {
	if model.notice != nil {
		return renderNoticeLine(*model.notice, width, styles)
	}
	status := model.operationSummary()
	if conditions := model.workspaceConditionSummary(); conditions != "" {
		status = conditions + "  " + status
	}
	_, height := model.renderSize()
	layout := model.workspaceLayout(width, height)
	if model.size.Width > maxRenderWidth || model.size.Height > maxRenderHeight {
		status = fmt.Sprintf(
			"presentation clipped to %d×%d  %s",
			min(model.size.Width, maxRenderWidth),
			min(model.size.Height, maxRenderHeight),
			status,
		)
	}
	if layout.sideBySideFallback {
		status = "unified fallback: side-by-side needs 100 columns  " + status
	}
	if model.mode == ModeCommentEdit {
		status = safeAndClip(status+"  comment editor owns input", width)
	} else {
		bindingBudget := min(max(width/2, 0), 48)
		if width >= compactWidth {
			bindingBudget = min(width-20, 72)
		}
		bindings := bindingBar{
			bindings: model.workspaceStatusBindings(layout),
			labels:   workspaceBindingLabels,
		}.RenderWithBase(bindingBudget, styles, styles.status)
		if bindings != "" {
			statusWidth := max(width-lipgloss.Width(bindings)-2, 0)
			status = fitCellWidth(safeAndClip(status, statusWidth), statusWidth) + "  " + bindings
		} else {
			status = safeAndClip(status, width)
		}
	}
	return fitStyledCellWidth(status, width, styles.status)
}

func renderNoticeLine(notice Notice, width int, styles semanticStyles) string {
	prefix := ""
	style := styles.status
	switch notice.Kind {
	case NoticeInformation:
	case NoticeSuccess:
		prefix = "Success: "
		style = styles.success.Inherit(styles.status)
	case NoticeWarning:
		prefix = "Warning: "
		style = styles.warning.Inherit(styles.status)
	case NoticeError:
		prefix = "Error: "
		style = styles.failure.Inherit(styles.status)
	}
	return fitStyledCellWidth(safeAndClip(prefix+string(notice.Text), width), width, style)
}

func (model *Model) workspaceConditionSummary() string {
	conditions := make([]string, 0, 3)
	switch model.fileProjection.workspace {
	case projectionInitial, projectionLoading:
		conditions = append(conditions, "Loading files…")
	case projectionFailed:
		conditions = append(conditions, "Error: file index unavailable")
	case projectionReady:
	}
	if model.fileProjection.requestedQuery != model.fileProjection.appliedQuery {
		switch model.fileProjection.filter {
		case projectionLoading:
			conditions = append(conditions, "Filtering files…")
		case projectionFailed:
			conditions = append(conditions, "Error: file filter failed")
		case projectionInitial, projectionReady:
		}
	}
	remote := model.review.Snapshot().RemoteThreadSummary()
	if model.foreground.reloadActive() && remote.Status != review.RemoteThreadsNotRequested {
		conditions = append(conditions, "Loading remote comments; prior results retained")
		return strings.Join(conditions, " · ")
	}
	switch remote.Status {
	case review.RemoteThreadsUnavailable, review.RemoteThreadsLimited:
		condition := "Remote comments " + remote.Status.String()
		if remote.Count != 0 {
			condition += "; prior results retained"
		}
		conditions = append(conditions, condition)
	case review.RemoteThreadsNotRequested, review.RemoteThreadsReady:
	default:
		conditions = append(conditions, "Error: remote comments failed")
	}
	return strings.Join(conditions, " · ")
}

func (model *Model) workspaceStatusBindings(layout workspaceLayout) []keymap.Binding {
	if model.mode == ModeVisual {
		return bindingsForActions(model.effectiveBindings(keymap.ContextVisual), []keymap.Action{
			keymap.ActionExtendDown,
			keymap.ActionCommentSelection,
			keymap.ActionCancel,
		})
	}
	if _, active := model.review.ActiveFile(); !active {
		return bindingsForActions(model.effectiveBindings(keymap.ContextNormal), []keymap.Action{
			keymap.ActionReload,
			keymap.ActionHelp,
		})
	}
	if layout.treeWidth != 0 || layout.commentsWidth != 0 {
		if model.focus == FocusComments && !model.editableLocalCommentSelected() {
			return bindingsForActions(model.effectiveBindings(keymap.ContextNormal), []keymap.Action{
				keymap.ActionHelp,
				keymap.ActionFocusNextPane,
				keymap.ActionFileComment,
			})
		}
		return model.normalStatusBindings()
	}
	if len(model.fileOrder) < 2 {
		return bindingsForActions(model.effectiveBindings(keymap.ContextNormal), []keymap.Action{
			keymap.ActionMoveDown,
			keymap.ActionHelp,
			keymap.ActionCommentAtCursor,
		})
	}
	actions := make([]keymap.Action, 0, 4)
	if active, ok := model.review.ActiveFile(); ok {
		if position, found := slices.BinarySearch(model.fileOrder, active); found {
			if position < len(model.fileOrder)-1 {
				actions = append(actions, keymap.ActionNextFile)
			}
			if position > 0 {
				actions = append(actions, keymap.ActionPreviousFile)
			}
		}
	}
	actions = append(actions, keymap.ActionHelp, keymap.ActionCommentAtCursor)
	return bindingsForActions(model.effectiveBindings(keymap.ContextNormal), actions)
}

func (model *Model) renderSize() (int, int) {
	if !model.size.Known {
		return minimumWidth, minimumHeight
	}
	return min(max(model.size.Width, 1), maxRenderWidth),
		min(max(model.size.Height, 1), maxRenderHeight)
}

func (model *Model) belowMinimum() bool {
	return model.size.Known &&
		(model.size.Width < minimumWidth || model.size.Height < minimumHeight)
}

func (model *Model) showFileTree() bool {
	if !model.size.Known {
		return false
	}
	width, height := model.renderSize()
	return model.workspaceLayout(width, height).treeWidth != 0
}

func (model *Model) currentWorkspaceLayout() workspaceLayout {
	width, height := model.renderSize()
	return model.workspaceLayout(width, height)
}

func (model *Model) showDiffPane() bool {
	if !model.size.Known {
		return false
	}
	return model.currentWorkspaceLayout().diffWidth != 0
}

func (model *Model) showCommentsPane() bool {
	if !model.size.Known {
		return false
	}
	width, height := model.renderSize()
	return model.workspaceLayout(width, height).commentsWidth != 0
}

func (model *Model) diffHeight() int {
	if !model.size.Known {
		return 1
	}
	height := min(model.size.Height, maxRenderHeight)
	if model.mode == ModeCommentEdit {
		height = newCommentEditorLayout(height).workspaceHeight
	}
	return max(height-2, 1)
}

func separatorWidth(treeWidth, commentsWidth int) int {
	width := 0
	if treeWidth != 0 {
		width++
	}
	if commentsWidth != 0 {
		width++
	}
	return width
}

func constrainedStyledLines(lines []string, width, height int) string {
	lines = lines[:min(len(lines), height)]
	for index := range lines {
		lines[index] = fitCellWidth(lines[index], width)
	}
	return strings.Join(lines, "\n")
}

type dialogSize struct {
	width, height int
}

type cellRect struct {
	x, y          int
	width, height int
}

type dialogGeometryMode uint8

const (
	dialogGeometryFullScreen dialogGeometryMode = iota + 1
	dialogGeometryBordered
)

type dialogLayout struct {
	mode  dialogGeometryMode
	outer cellRect
	inner cellRect
}

type dialogSpec struct {
	preferredInner dialogSize
	borderMinimum  dialogSize
}

func contentSizedDialog(
	maximum dialogSize,
	body, controls []string,
) dialogSize {
	maximum.height = min(maximum.height, max(len(body)+len(controls), 1))
	return maximum
}

func newDialogLayout(
	width, height int,
	spec dialogSpec,
) dialogLayout {
	fullScreen := cellRect{
		width:  max(width, 0),
		height: max(height, 0),
	}
	if width < spec.borderMinimum.width || height < spec.borderMinimum.height {
		return dialogLayout{
			mode:  dialogGeometryFullScreen,
			outer: fullScreen,
			inner: fullScreen,
		}
	}
	inner := cellRect{
		width:  min(spec.preferredInner.width, max(width-4, 1)),
		height: min(spec.preferredInner.height, max(height-4, 1)),
	}
	outer := cellRect{
		width:  inner.width + 2,
		height: inner.height + 2,
	}
	outer.x = max((width-outer.width)/2, 0)
	outer.y = max((height-outer.height)/2, 0)
	inner.x = outer.x + 1
	inner.y = outer.y + 1
	return dialogLayout{
		mode:  dialogGeometryBordered,
		outer: outer,
		inner: inner,
	}
}

func standardDialogOverlay(
	background string,
	body, controls []string,
	width, height int,
	spec dialogSpec,
	styles semanticStyles,
) string {
	layout := newDialogLayout(width, height, spec)
	if layout.inner.width == 0 || layout.inner.height == 0 {
		return ""
	}
	content := blockingSurface(
		body,
		controls,
		layout.inner.width,
		layout.inner.height,
		styles,
	)
	return composeDialog(background, frameDialogContent(content, layout, styles), layout)
}

func frameDialogContent(
	content string,
	layout dialogLayout,
	styles semanticStyles,
) string {
	if layout.mode == dialogGeometryFullScreen {
		return styles.panel.Render(content)
	}
	return styles.panel.
		Width(layout.outer.width).
		Height(layout.outer.height).
		Border(lipgloss.NormalBorder(), true).
		BorderForeground(styles.focused.GetForeground()).
		Render(content)
}

func composeDialog(background, dialog string, layout dialogLayout) string {
	if layout.mode == dialogGeometryFullScreen {
		return dialog
	}
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(background),
		lipgloss.NewLayer(dialog).X(layout.outer.x).Y(layout.outer.y).Z(1),
	).Render()
}

func blockingSurface(
	body, controls []string,
	width, height int,
	styles semanticStyles,
) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	controls = controls[:min(len(controls), height)]
	bodyRows := max(height-len(controls), 0)
	body = body[:min(len(body), bodyRows)]
	lines := make([]string, 0, height)
	for _, line := range body {
		lines = append(lines, fitStyledCellWidth(line, width, styles.panel))
	}
	for len(lines) < bodyRows {
		lines = append(lines, fitStyledCellWidth("", width, styles.panel))
	}
	for _, line := range controls {
		lines = append(lines, fitStyledCellWidth(line, width, styles.panel))
	}
	return strings.Join(lines, "\n")
}

func fitCellWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if plainASCIIText(value) {
		if len(value) >= width {
			return value[:width]
		}
		var fitted strings.Builder
		fitted.Grow(width)
		fitted.WriteString(value)
		for range width - len(value) {
			fitted.WriteByte(' ')
		}
		return fitted.String()
	}
	value = lipgloss.NewStyle().MaxWidth(width).Render(value)
	padding := width - lipgloss.Width(value)
	if padding > 0 {
		value += strings.Repeat(" ", padding)
	}
	return value
}

func safeAndClip(value string, width int) string {
	if width <= 0 {
		return ""
	}
	maxBytes := max(int64(width)*8, int64(4_096))
	limited := false
	if int64(len(value)) > maxBytes {
		value = value[:int(maxBytes)]
		limited = true
	}
	if plainASCIIText(value) {
		if !limited && len(value) <= width {
			return value
		}
		if width == 1 {
			return "…"
		}
		return value[:min(len(value), width-1)] + "…"
	}
	safeValue := safeText(value)
	if limited || lipgloss.Width(safeValue) > width {
		if width == 1 {
			return "…"
		}
		return lipgloss.NewStyle().MaxWidth(width-1).Render(safeValue) + "…"
	}
	return safeValue
}

func plainASCIIText(value string) bool {
	for index := range len(value) {
		if value[index] < 0x20 || value[index] >= 0x7f {
			return false
		}
	}
	return true
}
