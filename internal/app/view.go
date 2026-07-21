package app

import (
	"fmt"
	"iter"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

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
	heading       lipgloss.Style
	muted         lipgloss.Style
	focused       lipgloss.Style
	selected      lipgloss.Style
	added         lipgloss.Style
	deleted       lipgloss.Style
	context       lipgloss.Style
	hunk          lipgloss.Style
	pending       lipgloss.Style
	reviewed      lipgloss.Style
	commentAnchor lipgloss.Style
	keyword       lipgloss.Style
	literal       lipgloss.Style
	comment       lipgloss.Style
	number        lipgloss.Style
	warning       lipgloss.Style
	failure       lipgloss.Style
	status        lipgloss.Style
	panel         lipgloss.Style
}

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
	treeRows    []treeNodeIdentity
	diffRows    []uint32
	commentRows []int
	layout      workspaceLayout
	reviewState uint64
	bodyHeight  int
}

type mouseTarget uint8

const (
	mouseTargetNone mouseTarget = iota
	mouseTargetTree
	mouseTargetDiff
	mouseTargetComment
)

type diffLineNumberWidths struct {
	old int
	new int
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
	case model.mode == ModeSearch || model.mode == ModeCommand:
		content = model.promptView(width, height)
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
	separator := styles.muted.Render("│")

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
		sections = append(sections, fitCellWidth(diffLine, layout.diffWidth))
		if layout.commentsWidth != 0 {
			commentLine := ""
			if row < len(commentLines) {
				commentLine = commentLines[row]
			}
			sections = append(sections, fitCellWidth(commentLine, layout.commentsWidth))
		}
		body[row] = styles.panel.Render(
			strings.Join(sections, separator),
		)
	}
	title := styles.panel.Render(model.titleLine(width, styles))
	status := styles.status.Render(model.statusLine(width, styles))
	lines := make([]string, 0, len(body)+2)
	lines = append(lines, title)
	lines = append(lines, body...)
	lines = append(lines, status)
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

func (model *Model) fileTreeLines(
	width, height int,
	styles semanticStyles,
) ([]string, []treeNodeIdentity) {
	tree := model.currentFileTree()
	if width == 0 || len(tree.nodes) == 0 {
		return nil, nil
	}
	window := tree.window(model.treeCursor, height, model.collapsedTrees)
	lines := make([]string, 0, len(window))
	rows := make([]treeNodeIdentity, 0, len(window))
	active, activeOK := model.review.ActiveFile()
	for _, node := range window {
		cursor := "  "
		if node.identity == model.treeCursor {
			cursor = "› "
			if model.focus == FocusFileTree {
				cursor = styles.focused.Render("▸") + " "
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
			if _, collapsed := model.collapsedTrees[node.identity.path]; collapsed {
				disclosure = "▸"
			}
		}
		indent := strings.Repeat("  ", min(node.depth, max(width/2, 0)))
		line := cursor + activeMarker + reviewed + disclosure + indent +
			safeAndClip(node.label, max(width-7-len(indent), 1))
		if node.identity == model.treeCursor {
			line = styles.selected.Render(line)
		}
		lines = append(lines, line)
		rows = append(rows, node.identity)
	}
	return lines, rows
}

func (model *Model) diffLines(
	width, height int,
	styles semanticStyles,
) ([]string, []uint32) {
	active, ok := model.review.ActiveFile()
	if !ok {
		return []string{styles.muted.Render("No changed files.")}, nil
	}
	state := model.review.Patch(active)
	switch state.Status {
	case review.PatchUnavailable:
		return []string{styles.muted.Render("Patch is not loaded. Press Enter to retry.")}, nil
	case review.PatchPending:
		return []string{styles.pending.Render("Loading patch…")}, nil
	case review.PatchFailed:
		return []string{styles.failure.Render("Patch failed: " + safeAndClip(state.Detail.String(), width-14))}, nil
	case review.PatchCancelled:
		return []string{styles.warning.Render("Patch cancelled. Press Enter to retry.")}, nil
	case review.PatchLimited:
		return []string{styles.warning.Render("Patch limited: " + safeAndClip(state.Detail.String(), width-15))}, nil
	case review.PatchReady:
	default:
		return []string{styles.failure.Render("Invalid patch state.")}, nil
	}
	count := uint32FromInt(height + viewportOverscan)
	lineBytes := max(int64(width)*8, 4_096)
	if model.settings.wrap {
		lineBytes = max(
			int64(width)*int64(height+viewportOverscan)*4,
			lineBytes,
		)
	}
	lines := model.review.PatchLines(active, model.diffTop, count, lineBytes)
	numberWidths := newDiffLineNumberWidths(state)
	if model.settings.diffView == DiffViewSideBySide &&
		width >= sideBySideContentWidth+diffCommentAnchorColumns {
		return model.sideBySideDiffLines(active, lines, width, height, numberWidths, styles)
	}
	rendered := make([]string, 0, min(height, len(lines)))
	rows := make([]uint32, 0, min(height, len(lines)))
	for _, line := range lines {
		cursor := " "
		if line.Number == model.diffCursor {
			cursor = "›"
			if model.focus == FocusDiff {
				cursor = styles.focused.Render("▸")
			}
		}
		if model.mode == ModeVisual &&
			line.Number >= min(model.visualAnchor, model.diffCursor) &&
			line.Number <= max(model.visualAnchor, model.diffCursor) {
			cursor = "│"
		}
		reviewed := " "
		if hunk, exists := line.Hunk(active); exists && model.review.HunkReviewed(hunk) {
			reviewed = styles.reviewed.Render("✓")
		}
		contentWidth := max(width-2, 1)
		segments := model.renderUnifiedPatchLine(
			active,
			line,
			contentWidth,
			numberWidths,
			styles,
		)
		for segmentIndex, segment := range segments {
			if len(rendered) == height {
				break
			}
			prefix := " "
			if segmentIndex == 0 {
				prefix = cursor
			}
			mark := " "
			if segmentIndex == 0 {
				mark = reviewed
			}
			rendered = append(rendered, prefix+mark+fitCellWidth(segment, contentWidth))
			rows = append(rows, line.Number)
		}
		if len(rendered) == height {
			break
		}
	}
	if len(rendered) == 0 {
		return []string{styles.muted.Render("Empty patch.")}, nil
	}
	return rendered, rows
}

func stylePatchLine(text string, kind review.PatchLineKind, styles semanticStyles) string {
	switch kind {
	case review.PatchLineHunkHeader:
		return styles.hunk.Render(text)
	case review.PatchLineAddition:
		return styles.added.Render(text)
	case review.PatchLineDeletion:
		return styles.deleted.Render(text)
	case review.PatchLineContext:
		return styles.context.Render(text)
	case review.PatchLineMetadata:
		if strings.HasPrefix(text, "+++") || strings.HasPrefix(text, "---") {
			return styles.heading.Render(text)
		}
	case review.PatchLineNoNewline:
	}
	return text
}

func (model *Model) renderUnifiedPatchLine(
	fileID review.FileID,
	line review.PatchLine,
	width int,
	numberWidths diffLineNumberWidths,
	styles semanticStyles,
) []string {
	raw := line.Text.String()
	kind := line.Kind()
	oldNumber, newNumber := "", ""
	oldAnchor, newAnchor := " ", " "
	if value, ok := line.OldLine(); ok {
		oldNumber = fmt.Sprint(value.Uint32())
		oldAnchor = model.commentAnchorCell(fileID, review.LineOld, value, styles)
	}
	if value, ok := line.NewLine(); ok {
		newNumber = fmt.Sprint(value.Uint32())
		newAnchor = model.commentAnchorCell(fileID, review.LineNew, value, styles)
	}
	numberPrefix := fmt.Sprintf(
		"%*s%s %*s%s ",
		numberWidths.old,
		oldNumber,
		oldAnchor,
		numberWidths.new,
		newNumber,
		newAnchor,
	)
	marker := ""
	content := raw
	if kind == review.PatchLineContext || kind == review.PatchLineAddition ||
		kind == review.PatchLineDeletion {
		if raw != "" {
			marker, content = raw[:1], raw[1:]
		}
	}
	styledMarker := marker
	switch kind {
	case review.PatchLineMetadata,
		review.PatchLineHunkHeader,
		review.PatchLineContext,
		review.PatchLineNoNewline:
	case review.PatchLineAddition:
		styledMarker = styles.added.Render(marker)
	case review.PatchLineDeletion:
		styledMarker = styles.deleted.Render(marker)
	}
	prefix := numberPrefix + styledMarker
	prefixWidth := lipgloss.Width(prefix)
	if width <= prefixWidth {
		return []string{stylePatchLine(safeAndClip(raw, width), kind, styles)}
	}
	contentWidth := width - prefixWidth
	var renderedContent string
	if kind == review.PatchLineMetadata || kind == review.PatchLineHunkHeader ||
		kind == review.PatchLineNoNewline {
		renderedContent = safeText(content)
		if !model.settings.wrap {
			renderedContent = clipStyled(renderedContent, model.diffLeft, contentWidth)
		}
	} else {
		renderedContent = model.renderSyntaxContent(syntaxContent{
			raw:       content,
			spans:     model.syntaxSpans(line),
			width:     contentWidth,
			truncated: line.Truncated,
			wrap:      model.settings.wrap,
			styles:    styles,
		})
	}
	segments := model.wrappedLine(renderedContent, contentWidth)
	rendered := make([]string, 0, len(segments))
	for index, segment := range segments {
		segmentPrefix := strings.Repeat(" ", prefixWidth)
		if index == 0 {
			segmentPrefix = prefix
		}
		value := segmentPrefix + fitCellWidth(segment, contentWidth)
		switch {
		case kind == review.PatchLineContext:
			value = styles.context.Render(value)
		case kind == review.PatchLineHunkHeader:
			value = styles.hunk.Render(value)
		case kind == review.PatchLineMetadata &&
			(strings.HasPrefix(raw, "+++") || strings.HasPrefix(raw, "---")):
			value = styles.heading.Render(value)
		}
		rendered = append(rendered, value)
	}
	return rendered
}

func (model *Model) commentAnchorCell(
	file review.FileID,
	side review.LineSide,
	line review.LineNumber,
	styles semanticStyles,
) string {
	if !model.review.HasCommentAnchor(file, side, line) {
		return " "
	}
	return styles.commentAnchor.Render("●")
}

func (model *Model) sideBySideDiffLines(
	fileID review.FileID,
	lines []review.PatchLine,
	width, height int,
	numberWidths diffLineNumberWidths,
	styles semanticStyles,
) ([]string, []uint32) {
	leftWidth := max((width-1)/2, 1)
	rightWidth := max(width-leftWidth-1, 1)
	separator := styles.muted.Render("│")
	rendered := make([]string, 0, min(height, len(lines)))
	rows := make([]uint32, 0, min(height, len(lines)))
	for _, line := range lines {
		if len(rendered) == height {
			break
		}
		cursor := " "
		if line.Number == model.diffCursor {
			cursor = "›"
			if model.focus == FocusDiff {
				cursor = styles.focused.Render("▸")
			}
		}
		if model.mode == ModeVisual &&
			line.Number >= min(model.visualAnchor, model.diffCursor) &&
			line.Number <= max(model.visualAnchor, model.diffCursor) {
			cursor = "│"
		}
		reviewed := " "
		if hunk, ok := line.Hunk(fileID); ok && model.review.HunkReviewed(hunk) {
			reviewed = styles.reviewed.Render("✓")
		}
		text := line.Text.String()
		if line.Kind() == review.PatchLineContext ||
			line.Kind() == review.PatchLineAddition ||
			line.Kind() == review.PatchLineDeletion {
			text = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(text, " "), "+"), "-")
		}
		leftSegments := model.renderSidePatchLine(
			fileID,
			line,
			review.LineOld,
			text,
			max(leftWidth-2, 1),
			numberWidths.old,
			styles,
		)
		rightSegments := model.renderSidePatchLine(
			fileID,
			line,
			review.LineNew,
			text,
			rightWidth,
			numberWidths.new,
			styles,
		)
		segments := max(len(leftSegments), len(rightSegments))
		for segment := range segments {
			if len(rendered) == height {
				break
			}
			leftValue, rightValue := "", ""
			if segment < len(leftSegments) {
				leftValue = leftSegments[segment]
			}
			if segment < len(rightSegments) {
				rightValue = rightSegments[segment]
			}
			lineCursor, lineReviewed := " ", " "
			if segment == 0 {
				lineCursor, lineReviewed = cursor, reviewed
			}
			rendered = append(
				rendered,
				lineCursor+lineReviewed+
					fitCellWidth(leftValue, max(leftWidth-2, 1))+
					separator+
					fitCellWidth(rightValue, rightWidth),
			)
			rows = append(rows, line.Number)
		}
	}
	if len(rendered) == 0 {
		return []string{styles.muted.Render("Empty patch.")}, nil
	}
	return rendered, rows
}

func (model *Model) renderSidePatchLine(
	fileID review.FileID,
	line review.PatchLine,
	side review.LineSide,
	text string,
	width, numberWidth int,
	styles semanticStyles,
) []string {
	if line.Kind() == review.PatchLineMetadata || line.Kind() == review.PatchLineHunkHeader {
		content := safeText(line.Text.String())
		if !model.settings.wrap {
			content = safeAndClip(line.Text.String(), width)
		}
		segments := model.wrappedLine(content, width)
		for index, segment := range segments {
			switch {
			case line.Kind() == review.PatchLineHunkHeader:
				segments[index] = styles.hunk.Render(segment)
			case strings.HasPrefix(line.Text.String(), "+++") ||
				strings.HasPrefix(line.Text.String(), "---"):
				segments[index] = styles.heading.Render(segment)
			}
		}
		return segments
	}
	number := ""
	anchor := " "
	var spans iter.Seq[review.SyntaxSpan]
	sourceLine, hasSourceLine := line.OldLine()
	if side == review.LineNew {
		sourceLine, hasSourceLine = line.NewLine()
	}
	if hasSourceLine {
		number = fmt.Sprint(sourceLine.Uint32())
		anchor = model.commentAnchorCell(fileID, side, sourceLine, styles)
		spans = model.syntaxSpansForSide(line, side)
	}
	prefix := fmt.Sprintf("%*s%s ", numberWidth, number, anchor)
	prefixWidth := lipgloss.Width(prefix)
	if width <= prefixWidth {
		return []string{safeAndClip(text, width)}
	}
	contentWidth := width - prefixWidth
	content := ""
	if hasSourceLine {
		content = model.renderSyntaxContent(syntaxContent{
			raw:       text,
			spans:     spans,
			width:     contentWidth,
			truncated: line.Truncated,
			wrap:      model.settings.wrap,
			styles:    styles,
		})
	}
	segments := model.wrappedLine(content, contentWidth)
	rendered := make([]string, 0, len(segments))
	for index, segment := range segments {
		segmentPrefix := strings.Repeat(" ", prefixWidth)
		if index == 0 {
			segmentPrefix = prefix
		}
		value := segmentPrefix + fitCellWidth(segment, contentWidth)
		rendered = append(rendered, value)
	}
	return rendered
}

func newDiffLineNumberWidths(state review.PatchState) diffLineNumberWidths {
	widths := diffLineNumberWidths{old: 1, new: 1}
	if line, ok := state.MaxLineNumber(review.LineOld); ok {
		widths.old = decimalDigitWidth(line.Uint32())
	}
	if line, ok := state.MaxLineNumber(review.LineNew); ok {
		widths.new = decimalDigitWidth(line.Uint32())
	}
	return widths
}

func decimalDigitWidth(value uint32) int {
	width := 1
	for value >= 10 {
		value /= 10
		width++
	}
	return width
}

func (model *Model) syntaxSpansForSide(
	line review.PatchLine,
	side review.LineSide,
) iter.Seq[review.SyntaxSpan] {
	switch side {
	case review.LineOld:
		sourceLine, ok := line.OldLine()
		if !ok {
			return nil
		}
		return model.syntaxOld.Spans(sourceLine)
	case review.LineNew:
		sourceLine, ok := line.NewLine()
		if !ok {
			return nil
		}
		return model.syntaxNew.Spans(sourceLine)
	default:
		return nil
	}
}

type syntaxContent struct {
	styles    semanticStyles
	spans     iter.Seq[review.SyntaxSpan]
	raw       string
	width     int
	truncated bool
	wrap      bool
}

func (model *Model) renderSyntaxContent(content syntaxContent) string {
	if content.width <= 0 {
		return ""
	}
	raw := content.raw
	left := model.diffLeft
	maxBytes := max(int64(content.width+left)*8, int64(4_096))
	if content.wrap {
		left = 0
		maxBytes = max(
			int64(content.width)*int64(maxRenderHeight)*8,
			maxBytes,
		)
	}
	limited := content.truncated || int64(len(raw)) > maxBytes
	if int64(len(raw)) > maxBytes {
		raw = raw[:int(maxBytes)]
	}
	var rendered strings.Builder
	rendered.Grow(len(raw))
	sanitizer := terminalTextRenderer{}
	if content.spans == nil {
		rendered.WriteString(sanitizer.render(raw))
	} else {
		styled, spansLimited := renderSyntaxSpans(raw, content.spans, content.styles)
		rendered.WriteString(styled)
		limited = limited || spansLimited
	}
	value := rendered.String()
	if content.wrap {
		if limited {
			value += "…"
		}
		return value
	}
	beyondViewport := lipgloss.Width(value) > model.diffLeft+content.width
	value = clipStyled(value, left, content.width)
	if limited || beyondViewport {
		if content.width == 1 {
			return "…"
		}
		return lipgloss.NewStyle().MaxWidth(content.width-1).Render(value) + "…"
	}
	return value
}

func renderSyntaxSpans(
	raw string,
	spans iter.Seq[review.SyntaxSpan],
	styles semanticStyles,
) (string, bool) {
	nextSpan, stopSpans := iter.Pull(spans)
	defer stopSpans()
	var span review.SyntaxSpan
	hasSpan := false
	spanEnd := 0
	limited := false
	advanceSpan := func() {
		hasSpan = false
		for {
			candidate, ok := nextSpan()
			if !ok {
				return
			}
			start, end := int(candidate.Start), int(candidate.End)
			if start < spanEnd || start > len(raw) || end <= start {
				continue
			}
			if end > len(raw) {
				limited = true
				return
			}
			span = candidate
			spanEnd = end
			hasSpan = true
			return
		}
	}
	advanceSpan()
	sanitizer := terminalTextRenderer{}
	if !hasSpan {
		return sanitizer.render(raw), limited
	}
	var rendered strings.Builder
	rendered.Grow(len(raw))
	var runKind review.SyntaxKind
	runStart, runEnd := 0, 0
	hasRun := false
	flushRun := func() {
		if !hasRun || runStart == runEnd {
			return
		}
		value := sanitizer.render(raw[runStart:runEnd])
		if runKind != 0 {
			value = syntaxStyle(runKind, styles).Render(value)
		}
		rendered.WriteString(value)
	}
	graphemes := uniseg.NewGraphemes(raw)
	for graphemes.Next() {
		start, end := graphemes.Positions()
		for hasSpan && int(span.End) <= start {
			advanceSpan()
		}
		kind := review.SyntaxKind(0)
		if hasSpan && int(span.Start) <= start && start < int(span.End) {
			kind = span.Kind
		}
		if hasRun && kind != runKind {
			flushRun()
			hasRun = false
		}
		if !hasRun {
			runStart = start
		}
		runEnd = end
		runKind = kind
		hasRun = true
	}
	flushRun()
	for hasSpan {
		advanceSpan()
	}
	return rendered.String(), limited
}

func clipStyled(value string, left, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Cut(value, max(left, 0), max(left, 0)+width)
}

func syntaxStyle(kind review.SyntaxKind, styles semanticStyles) lipgloss.Style {
	switch kind {
	case review.SyntaxKeyword:
		return styles.keyword
	case review.SyntaxString:
		return styles.literal
	case review.SyntaxComment:
		return styles.comment
	case review.SyntaxNumber:
		return styles.number
	default:
		return lipgloss.NewStyle()
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
			line = clipStyled(prefix, 0, width)
			line = appendSafeAndClip(
				line,
				target+" — ",
				width,
			)
			remaining := max(width-lipgloss.Width(line), 0)
			if remaining != 0 {
				line += safeAndClip(comment.Body().String(), remaining)
			}
		} else {
			threads := model.review.Snapshot().RemoteThreads(index-localCount, 1)
			if len(threads) != 1 {
				continue
			}
			thread := threads[0]
			state := "open"
			if thread.Resolved() {
				state = "resolved"
			} else if thread.Outdated() {
				state = "outdated"
			}
			location := thread.Path().Display()
			if sourceLine, ok := thread.Line(); ok {
				location += fmt.Sprintf(":%d", sourceLine.Uint32())
			}
			line = clipStyled(prefix, 0, width)
			line = appendSafeAndClip(
				line,
				"[remote "+state+"] "+location+" — ",
				width,
			)
			comments := thread.Comments(0, 1)
			if len(comments) != 0 {
				author := comments[0].Author()
				if author == "" {
					author = "deleted user"
				}
				remaining := max(width-lipgloss.Width(line), 0)
				if remaining != 0 {
					line += safeAndClip(author+": "+comments[0].Body(), remaining)
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
		return strings.Join(append(
			[]string{
				workspace,
				fitCellWidth(styles.failure.Render("Comment editor unavailable"), width),
				strings.Repeat(" ", width),
			},
			fittedStyledLines(model.commentEditor.View(), width, layout.editorHeight)...,
		), "\n")
	}
	title := "Comment · " + commentTargetText(draft.Target())
	help := model.actionSummary(
		KeyContextCommentEdit,
		KeyActionSaveComment,
		KeyActionInsertNewline,
		KeyActionCancel,
	)
	lines := append(
		[]string{
			workspace,
			fitCellWidth(styles.focused.Render(safeAndClip(title, width)), width),
			fitCellWidth(styles.muted.Render(safeAndClip(help, width)), width),
		},
		fittedStyledLines(model.commentEditor.View(), width, layout.editorHeight)...,
	)
	return strings.Join(lines, "\n")
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

func fittedStyledLines(value string, width, height int) []string {
	lines := strings.Split(value, "\n")
	fitted := make([]string, height)
	for index := range height {
		if index < len(lines) {
			fitted[index] = fitCellWidth(lines[index], width)
			continue
		}
		fitted[index] = strings.Repeat(" ", width)
	}
	return fitted
}

func (model *Model) titleLine(width int, styles semanticStyles) string {
	activeText := "no active file"
	if active, ok := model.review.ActiveFile(); ok {
		if file, exists := model.review.Snapshot().File(active); exists {
			activeText = file.DisplayPath()
		}
	}
	focus := "diff"
	switch {
	case model.mode == ModeCommentEdit:
		focus = "comment"
	case model.focus == FocusFileTree:
		focus = "files"
	case model.focus == FocusComments:
		focus = "comments"
	}
	title := fmt.Sprintf(
		"rvw  [focus:%s]  %s  [source:%d workspace:%d]",
		focus,
		activeText,
		model.review.Snapshot().SourceID().Uint64(),
		model.review.WorkspaceRevision().Uint64(),
	)
	return fitCellWidth(styles.heading.Render(safeAndClip(title, width)), width)
}

func (model *Model) statusLine(width int, styles semanticStyles) string {
	status := model.operationSummary()
	if model.mode == ModeCommentEdit {
		status += "  comment editor owns input"
	} else {
		status += "  " + model.actionSummary(
			KeyContextNormal,
			KeyActionFocusNextPane,
			KeyActionActivate,
			KeyActionHelp,
			KeyActionQuit,
		)
	}
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
		switch model.notice.Kind {
		case NoticeInformation:
		case NoticeWarning:
			return fitCellWidth(styles.warning.Render(safeAndClip(status, width)), width)
		case NoticeError:
			return fitCellWidth(styles.failure.Render(safeAndClip(status, width)), width)
		}
	}
	return fitCellWidth(styles.muted.Render(safeAndClip(status, width)), width)
}

func (model *Model) bindingSummary(contextName KeyContext) string {
	var values []string
	for _, binding := range model.effectiveBindings(contextName) {
		values = append(values, binding.Chord.String()+" "+binding.Action.String())
		if len(values) == 4 {
			break
		}
	}
	return strings.Join(values, "  ")
}

func (model *Model) actionSummary(contextName KeyContext, actions ...KeyAction) string {
	var values []string
	bindings := model.effectiveBindings(contextName)
	for _, action := range actions {
		for _, binding := range bindings {
			if binding.Action == action {
				values = append(values, binding.Chord.String()+" "+action.String())
				break
			}
		}
	}
	return strings.Join(values, "  ")
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
		lines[index] = fitCellWidth(safeAndClip(lines[index], width), width)
	}
	return strings.Join(lines, "\n")
}

func fitCellWidth(value string, width int) string {
	if width <= 0 {
		return ""
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
	safeValue := safeText(value)
	if limited || lipgloss.Width(safeValue) > width {
		if width == 1 {
			return "…"
		}
		return lipgloss.NewStyle().MaxWidth(width-1).Render(safeValue) + "…"
	}
	return safeValue
}
