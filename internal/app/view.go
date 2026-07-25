package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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

type semanticStyles struct {
	base            lipgloss.Style
	heading         lipgloss.Style
	muted           lipgloss.Style
	focused         lipgloss.Style
	selected        lipgloss.Style
	cursorLine      lipgloss.Style
	visualSelection lipgloss.Style
	border          lipgloss.Style
	borderFocused   lipgloss.Style
	added           lipgloss.Style
	deleted         lipgloss.Style
	context         lipgloss.Style
	hunk            lipgloss.Style
	pending         lipgloss.Style
	reviewed        lipgloss.Style
	commentAnchor   lipgloss.Style
	keyword         lipgloss.Style
	literal         lipgloss.Style
	comment         lipgloss.Style
	number          lipgloss.Style
	warning         lipgloss.Style
	failure         lipgloss.Style
	notice          lipgloss.Style
	status          lipgloss.Style
	panel           lipgloss.Style
	treeDirectory   lipgloss.Style
	treeFile        lipgloss.Style
}

type workspaceLayout struct {
	treeWidth          int
	diffWidth          int
	commentsWidth      int
	kind               workspaceLayoutKind
	sideBySideFallback bool
}

type commentEditorLayout struct {
	workspaceHeight int
	editorHeight    int
	editorTop       int
}

type mouseHitMap struct {
	treeRows       []treeNodeIdentity
	diffRows       []diffViewRow
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

type diffViewRow struct {
	line       uint32
	selectable bool
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
	case model.mode == ModeCommentEdit:
		content = model.commentEditorView(width, height)
	default:
		layout := model.workspaceLayout(width, height)
		workspace, hits := model.workspaceView(width, height, layout)
		mouseHits = hits

		styles := newSemanticStyles(model.settings)
		switch model.overlay {
		case OverlayNone:
			content = workspace
		case OverlayConfirm:
			modal, modalLayout := model.confirmationModalView(width, height, styles)
			content = lipgloss.NewCompositor(
				lipgloss.NewLayer(workspace),
				lipgloss.NewLayer(modal).X(modalLayout.x).Y(modalLayout.y).Z(1),
			).Render()
		case OverlayFind:
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
		case OverlayHelp:
			modal, modalLayout := model.helpModalView(width, height, styles)
			content = lipgloss.NewCompositor(
				lipgloss.NewLayer(workspace),
				lipgloss.NewLayer(modal).X(modalLayout.x).Y(modalLayout.y).Z(1),
			).Render()
		case OverlayStatus:
			modal, modalLayout := model.statusModalView(width, height, styles)
			content = lipgloss.NewCompositor(
				lipgloss.NewLayer(workspace),
				lipgloss.NewLayer(modal).X(modalLayout.x).Y(modalLayout.y).Z(1),
			).Render()
		case OverlayExport:
			modal, modalLayout := model.exportModalView(width, height, styles)
			content = lipgloss.NewCompositor(
				lipgloss.NewLayer(workspace),
				lipgloss.NewLayer(modal).X(modalLayout.x).Y(modalLayout.y).Z(1),
			).Render()
		case OverlaySubmit:
			modal, modalLayout := model.publicationModalView(width, height, styles)
			content = lipgloss.NewCompositor(
				lipgloss.NewLayer(workspace),
				lipgloss.NewLayer(modal).X(modalLayout.x).Y(modalLayout.y).Z(1),
			).Render()
		default:
			content = workspace
		}
	}
	return model.completeView(content, mouseHits)
}

func (model *Model) workspaceView(
	width, height int,
	layout workspaceLayout,
) (string, mouseHitMap) {
	styles := newSemanticStyles(model.settings)
	bodyHeight := max(height-2, 1)
	var treeLines, diffLines, commentLines []string
	var treeRows []treeNodeIdentity
	var diffRows []diffViewRow
	var commentRows []int
	if layout.treeWidth != 0 {
		treeLines, treeRows = model.fileTreeLines(layout.treeWidth, bodyHeight, styles)
	}
	if layout.diffWidth != 0 {
		diffLines, diffRows = model.diffLines(layout.diffWidth, bodyHeight, styles)
	}
	if layout.commentsWidth != 0 {
		commentLines, commentRows = model.commentLines(layout.commentsWidth, bodyHeight, styles)
	}
	separator := styles.border.Render("│")

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
			sections = append(sections, fitCellWidth(treeLine, layout.treeWidth))
		}
		if layout.diffWidth != 0 {
			sections = append(sections, fitCellWidth(diffLine, layout.diffWidth))
		}
		if layout.commentsWidth != 0 {
			commentLine := ""
			if row < len(commentLines) {
				commentLine = commentLines[row]
			}
			sections = append(sections, fitCellWidth(commentLine, layout.commentsWidth))
		}
		body[row] = strings.Join(sections, separator)
		if model.settings.colorEnabled {
			body[row] = styles.panel.Render(body[row])
		}
	}
	title := model.titleLine(width, styles)
	footer := model.statusLine(width, styles)
	if model.settings.colorEnabled {
		title = styles.panel.Render(title)
		footer = styles.status.Render(footer)
	}
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
	commentsStart := diffStart + hits.layout.diffWidth
	if hits.layout.diffWidth != 0 && hits.layout.commentsWidth != 0 {
		commentsStart++
	}
	if hits.layout.commentsWidth != 0 &&
		(hits.layout.diffWidth == 0 || mouse.X >= commentsStart) {
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
			if row < len(hits.diffRows) && hits.diffRows[row].selectable {
				action.target = mouseTargetDiff
				action.diffLine = hits.diffRows[row].line
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
	isCursor := func(node treeNode) bool { return node.identity == model.treeCursor }
	isFocused := model.focus == FocusFileTree

	for _, node := range window {
		label := node.label
		if label == "" && !node.identity.directory {
			if file, ok := model.review.Snapshot().File(node.identity.file); ok {
				label = file.DisplayPath()
			}
		}

		var line strings.Builder
		line.Grow(width + 32)

		indentWidth := node.depth * 2
		if indentWidth > 0 {
			line.WriteString(strings.Repeat(" ", min(indentWidth, max(width/2, 0))))
		}

		if node.identity.directory {
			index, _ := tree.index(node.identity)
			if model.collapsedTree.isCollapsed(index) {
				line.WriteString(styles.muted.Render("▸"))
			} else {
				line.WriteString(styles.muted.Render("▾"))
			}
			line.WriteString(" ")
			line.WriteString(styles.treeDirectory.Render("📁"))
			line.WriteString(" ")
		} else {
			line.WriteString("  ")
			icon := fileIcon(label)
			if activeOK && node.identity.file == active {
				line.WriteString(styles.focused.Render(icon))
			} else {
				line.WriteString(styles.muted.Render(icon))
			}
			line.WriteString(" ")
		}

		prefixWidth := lipgloss.Width(line.String())
		suffixWidth := 0

		var suffix string
		if !node.identity.directory {
			if model.review.FileReviewed(node.identity.file) {
				suffix = " " + styles.reviewed.Render("✓")
				suffixWidth = 2
			} else if activeOK && node.identity.file == active {
				suffix = " " + styles.focused.Render("●")
				suffixWidth = 2
			}
		}

		labelWidth := max(width-prefixWidth-suffixWidth, 1)
		clippedLabel := safeAndClip(label, labelWidth)

		if node.identity.directory {
			line.WriteString(styles.treeDirectory.Render(clippedLabel))
		} else {
			line.WriteString(styles.treeFile.Render(clippedLabel))
		}
		line.WriteString(strings.Repeat(" ", max(labelWidth-lipgloss.Width(clippedLabel), 0)))
		line.WriteString(suffix)

		result := line.String()
		if isCursor(node) {
			if isFocused {
				result = styles.selected.Render(fitCellWidth(result, width))
			} else {
				result = styles.cursorLine.Render(fitCellWidth(result, width))
			}
		}
		lines = append(lines, result)
		rows = append(rows, node.identity)
	}
	return lines, rows
}

func fileIcon(name string) string {
	ext := ""
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		ext = strings.ToLower(name[idx:])
	}
	switch ext {
	case ".go":
		return "󰟓"
	case ".rs":
		return "󱘗"
	case ".py":
		return "󰌠"
	case ".js":
		return "󰌞"
	case ".ts":
		return "󰛦"
	case ".tsx", ".jsx":
		return "󰜈"
	case ".html":
		return "󰌝"
	case ".css":
		return "󰌜"
	case ".json":
		return "󰘦"
	case ".yaml", ".yml":
		return "󰈙"
	case ".toml":
		return "󰈙"
	case ".md":
		return "󰍔"
	case ".txt":
		return "󰈙"
	case ".sh", ".bash", ".zsh":
		return "󰆍"
	case ".sql":
		return "󰆼"
	case ".rb":
		return "󰴭"
	case ".java":
		return "󰬷"
	case ".c", ".h":
		return "󰙱"
	case ".cpp", ".cc", ".hpp":
		return "󰙲"
	case ".swift":
		return "󰛥"
	case ".kt":
		return "󱈙"
	case ".lock":
		return "󰌾"
	case ".sum", ".mod":
		return "󰏗"
	case ".gitignore", ".gitattributes":
		return "󰊢"
	default:
		return "󰈔"
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
			return []string{renderNoticeLine(Notice{
				Kind: NoticeWarning,
				Text: NoticeText(
					"Remote comments " + remote.Status.String() + ": " + remote.Detail.String(),
				),
			}, width, styles)}, nil
		}
		return []string{styles.muted.Render("No comments — select lines and press c to add one.")}, nil
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
				prefix = styles.focused.Render("▸") + " "
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
			renderNoticeLine(Notice{Kind: NoticeError, Text: "Comment editor unavailable"}, width, styles),
			strings.Repeat(" ", width),
			model.commentEditor.View(),
		}, "\n")
	}
	target := commentTargetText(draft.Target())
	title := commentEditorTitleLine("Comment · "+target, width)
	help := model.commentEditorBindingSummary()
	return strings.Join([]string{
		workspace,
		fitCellWidth(styles.focused.Render(title), width),
		fitCellWidth(styles.muted.Render(safeAndClip(help, width)), width),
		model.commentEditor.View(),
	}, "\n")
}

func commentEditorTitleLine(title string, width int) string {
	if width <= 0 {
		return ""
	}
	prefix := "── "
	label := safeAndClip(title, max(width-lipgloss.Width(prefix)-1, 1))
	line := prefix + label + " "
	if remaining := width - lipgloss.Width(line); remaining > 0 {
		line += strings.Repeat("─", remaining)
	}
	return safeAndClip(line, width)
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

func (model *Model) titleLine(width int, styles semanticStyles) string {
	activeText := "no active file"
	if active, ok := model.review.ActiveFile(); ok {
		if file, exists := model.review.Snapshot().File(active); exists {
			activeText = file.DisplayPath()
		}
	}
	focus := "diff"
	focusIcon := "󰈔"
	switch {
	case model.mode == ModeCommentEdit:
		focus = "comment"
		focusIcon = "󰆈"
	case model.mode == ModeCommand:
		focus = "command"
		focusIcon = "󰘳"
	case model.focus == FocusFileTree:
		focus = "files"
		focusIcon = "󰙅"
	case model.focus == FocusComments:
		focus = "comments"
		focusIcon = "󰆈"
	}

	reviewed := model.review.ReviewedFileCount()
	total := model.review.Snapshot().FileCount()
	var progress string
	if int(reviewed) == total && total != 0 {
		progress = fmt.Sprintf(" ✓ %d files", total)
	} else {
		progress = fmt.Sprintf(" %d/%d", reviewed, total)
	}

	prefix := fmt.Sprintf("rvw %s %s  ", focusIcon, focus)
	progressStyled := styles.muted.Render(progress)
	available := width - lipgloss.Width(prefix) - lipgloss.Width(progressStyled)
	if available < 8 {
		progressStyled = ""
		available = width - lipgloss.Width(prefix)
	}

	title := prefix + safeMiddleClip(activeText, max(available, 1)) + progressStyled
	return fitCellWidth(styles.heading.Render(safeAndClip(title, width)), width)
}

func (model *Model) statusLine(width int, styles semanticStyles) string {
	if model.notice != nil {
		return renderNoticeLine(*model.notice, width, styles)
	}
	if model.size.Width > maxRenderWidth || model.size.Height > maxRenderHeight {
		return renderNoticeLine(Notice{
			Kind: NoticeWarning,
			Text: NoticeText(fmt.Sprintf(
				"presentation clipped to %d×%d",
				min(model.size.Width, maxRenderWidth),
				min(model.size.Height, maxRenderHeight),
			)),
		}, width, styles)
	}
	status := model.operationSummary()
	if model.mode == ModeCommentEdit {
		status += "  comment editor owns input"
	} else {
		summary := model.normalStatusBindingSummary()
		_, height := model.renderSize()
		switch model.workspaceLayout(width, height).kind {
		case workspaceLayoutFilesOnly:
			summary = model.actionHintSummary(KeyContextNormal, actionHint{
				action: KeyActionToggleFilePanel,
				label:  "close files",
			})
		case workspaceLayoutCommentsOnly:
			summary = model.actionHintSummary(KeyContextNormal, actionHint{
				action: KeyActionToggleComments,
				label:  "close comments",
			})
		case workspaceLayoutDiffOnly, workspaceLayoutSplit:
		}
		status += "  " + summary
	}
	return fitCellWidth(styles.muted.Render(safeAndClip(status, width)), width)
}

func renderNoticeLine(notice Notice, width int, styles semanticStyles) string {
	prefix := "ℹ "
	style := styles.muted
	switch notice.Kind {
	case NoticeInformation:
	case NoticeWarning:
		prefix = "⚠ "
		style = styles.warning
	case NoticeError:
		prefix = "✗ "
		style = styles.failure
	}
	return fitCellWidth(style.Render(safeAndClip(prefix+string(notice.Text), width)), width)
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
	bodyHeight := max(height-2, 1)
	width, _ := model.renderSize()
	if model.workspaceLayout(width, height).sideBySideFallback {
		bodyHeight--
	}
	return max(bodyHeight, 1)
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

func safeMiddleClip(value string, width int) string {
	if width <= 0 {
		return ""
	}
	maxBytes := max(int64(width)*8, int64(4_096))
	if int64(len(value)) > maxBytes {
		value = value[len(value)-int(maxBytes):]
	}
	safeValue := safeText(value)
	valueWidth := lipgloss.Width(safeValue)
	if valueWidth <= width {
		return safeValue
	}
	if width == 1 {
		return "…"
	}
	prefixWidth := (width - 1) / 3
	suffixWidth := width - 1 - prefixWidth
	return ansi.Cut(safeValue, 0, prefixWidth) + "…" +
		ansi.Cut(safeValue, valueWidth-suffixWidth, valueWidth)
}

func plainASCIIText(value string) bool {
	for index := range len(value) {
		if value[index] < 0x20 || value[index] >= 0x7f {
			return false
		}
	}
	return true
}
