package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/review"
)

const (
	minimumWidth     = 20
	minimumHeight    = 6
	compactWidth     = 80
	normalWidth      = 120
	wideWidth        = 180
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
		content = lipgloss.NewCompositor(
			lipgloss.NewLayer(workspace),
			lipgloss.NewLayer(modal).X(findLayout.x).Y(findLayout.y).Z(1),
		).Render()
		mouseHits = mouseHitMap{
			findRows:       findRows,
			findX:          findLayout.x,
			findY:          findLayout.y,
			findWidth:      findLayout.width,
			findHeight:     findLayout.height,
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
	treeLines, treeRows := model.fileTreeLines(layout.treeWidth, bodyHeight, styles)
	diffLines, diffRows := model.diffLines(layout.diffWidth, bodyHeight, styles)
	commentLines, commentRows := model.commentLines(
		layout.commentsWidth,
		bodyHeight,
		styles,
	)
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
		sections = append(
			sections,
			fitStyledCellWidth(diffLine, layout.diffWidth, styles.panel),
		)
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
	focus := FocusDiff
	diffStart := 0
	if hits.layout.treeWidth != 0 {
		if mouse.X < hits.layout.treeWidth {
			focus = FocusFileTree
		} else {
			diffStart = hits.layout.treeWidth + 1
		}
	}
	commentsStart := diffStart + hits.layout.diffWidth + 1
	if hits.layout.commentsWidth != 0 && mouse.X >= commentsStart {
		focus = FocusComments
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
			if row < len(hits.commentRows) {
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
	if width == 0 || tree.empty() {
		return nil, nil
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
			cursor = "› "
			if model.focus == FocusFileTree {
				cursor = "▸ "
			}
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
		if selected {
			line = styles.selected.Render(prefix + badge + suffix)
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
		if index == cursor {
			prefix = "› "
			if model.focus == FocusComments {
				prefix = "▸ "
			}
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
		if index == cursor {
			line = styles.selected.Render(line)
		}
		lines = append(lines, line)
		rows = append(rows, index)
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
		}
	}
	diffTitle := "DIFF · " + activeText
	switch model.mode {
	case ModeCommentEdit:
		diffTitle += " · COMMENT"
	case ModeCommand:
		diffTitle += " · COMMAND"
	case ModeNormal, ModeVisual:
	}
	sections := make([]string, 0, 3)
	if layout.treeWidth != 0 {
		sections = append(sections, paneHeader(
			fmt.Sprintf("FILES · %d", model.review.Snapshot().FileCount()),
			layout.treeWidth,
			model.workspacePaneHeaderState(FocusFileTree),
			styles,
		))
	}
	sections = append(sections, paneHeader(
		diffTitle,
		layout.diffWidth,
		model.workspacePaneHeaderState(FocusDiff),
		styles,
	))
	if layout.commentsWidth != 0 {
		sections = append(sections, paneHeader(
			fmt.Sprintf("COMMENTS · %d", model.commentRowCount()),
			layout.commentsWidth,
			model.workspacePaneHeaderState(FocusComments),
			styles,
		))
	}
	return strings.Join(sections, styles.muted.Inherit(styles.panel).Render("│"))
}

func (model *Model) workspacePaneHeaderState(focus FocusOwner) paneHeaderState {
	if (model.mode == ModeNormal || model.mode == ModeVisual) && model.focus == focus {
		return paneHeaderFocused
	}
	return paneHeaderQuiet
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
	status := model.operationSummary()
	_, height := model.renderSize()
	if model.size.Width > maxRenderWidth || model.size.Height > maxRenderHeight {
		status = fmt.Sprintf(
			"presentation clipped to %d×%d  %s",
			min(model.size.Width, maxRenderWidth),
			min(model.size.Height, maxRenderHeight),
			status,
		)
	}
	if model.workspaceLayout(width, height).sideBySideFallback {
		status = "unified fallback: side-by-side needs 100 columns  " + status
	}
	if model.notice != nil {
		status = string(model.notice.Text) + "  " + status
	}
	status = safeAndClip(status, width)
	if model.mode == ModeCommentEdit {
		status += "  comment editor owns input"
	} else {
		available := max(width-lipgloss.Width(status)-2, 0)
		bindings := bindingBar{
			bindings: model.normalStatusBindings(),
			base:     styles.status,
		}.Render(available, styles)
		if bindings != "" {
			status += "  " + bindings
		}
	}
	if model.notice != nil {
		switch model.notice.Kind {
		case NoticeInformation:
		case NoticeWarning:
			return fitStyledCellWidth(status, width, styles.warning.Inherit(styles.status))
		case NoticeError:
			return fitStyledCellWidth(status, width, styles.failure.Inherit(styles.status))
		}
	}
	return fitStyledCellWidth(status, width, styles.status)
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

func constrainedLines(lines []string, width, height int) string {
	lines = lines[:min(len(lines), height)]
	for index := range lines {
		lines[index] = safeAndClip(lines[index], width)
	}
	return constrainedStyledLines(lines, width, height)
}

func constrainedStyledLines(lines []string, width, height int) string {
	lines = lines[:min(len(lines), height)]
	for index := range lines {
		lines[index] = fitCellWidth(lines[index], width)
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
