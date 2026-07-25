package app

import (
	"fmt"
	"iter"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"github.com/jacoelho/rvw/internal/review"
)

func (model *Model) diffLines(
	width, height int,
	styles semanticStyles,
) ([]string, []diffViewRow) {
	active, ok := model.review.ActiveFile()
	if !ok {
		return []string{styles.muted.Render("No changed files in this review.")}, nil
	}
	state := model.review.Patch(active)
	switch state.Status {
	case review.PatchUnavailable:
		return []string{styles.muted.Render("Patch not loaded — press Enter to load.")}, nil
	case review.PatchPending:
		return []string{styles.pending.Render("Loading patch…")}, nil
	case review.PatchFailed:
		return []string{renderNoticeLine(Notice{
			Kind: NoticeError,
			Text: NoticeText("Patch failed: " + state.Detail.String()),
		}, width, styles)}, nil
	case review.PatchCancelled:
		return []string{renderNoticeLine(Notice{
			Kind: NoticeWarning,
			Text: "Patch cancelled. Press Enter to retry.",
		}, width, styles)}, nil
	case review.PatchLimited:
		return []string{renderNoticeLine(Notice{
			Kind: NoticeWarning,
			Text: NoticeText("Patch limited: " + state.Detail.String()),
		}, width, styles)}, nil
	case review.PatchReady:
	default:
		return []string{renderNoticeLine(Notice{
			Kind: NoticeError,
			Text: "Invalid patch state.",
		}, width, styles)}, nil
	}
	rendered, rows, contentHeight := model.initialDiffRows(width, height, styles)
	if contentHeight <= 0 {
		return rendered, rows
	}
	count := uint32FromInt(contentHeight + viewportOverscan)
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
		return model.sideBySideDiffLines(active, lines, width, contentHeight, numberWidths, styles)
	}
	contentStart := len(rendered)
	for _, line := range lines {
		selected := line.Number == model.diffCursor
		inVisualRange := model.mode == ModeVisual &&
			line.Number >= min(model.visualAnchor, model.diffCursor) &&
			line.Number <= max(model.visualAnchor, model.diffCursor)
		lineStyles := styles
		if selected {
			lineStyles = styles.onCursorLine()
		}
		cursor := " "
		if selected {
			cursor = "›"
			if model.focus == FocusDiff {
				cursor = lineStyles.focused.Render("▸")
			}
		}
		if inVisualRange {
			cursor = lineStyles.visualSelection.Render("┃")
		}
		reviewed := " "
		if hunk, exists := line.Hunk(active); exists && model.review.HunkReviewed(hunk) {
			reviewed = lineStyles.reviewed.Render("✓")
		}
		contentWidth := max(width-2, 1)
		segments := model.renderUnifiedPatchLine(
			active,
			line,
			contentWidth,
			numberWidths,
			lineStyles,
		)
		for segmentIndex, segment := range segments {
			if len(rendered)-contentStart == contentHeight {
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
			value := prefix + mark + fitCellWidth(segment, contentWidth)
			if selected {
				value = renderPersistentStyle(lineStyles.cursorLine, value)
			}
			rendered = append(rendered, value)
			rows = append(rows, diffViewRow{line: line.Number, selectable: true})
		}
		if len(rendered)-contentStart == contentHeight {
			break
		}
	}
	if len(rendered) == contentStart {
		rendered = append(rendered, styles.muted.Render("Empty patch."))
		rows = append(rows, diffViewRow{})
	}
	return rendered, rows
}

func (model *Model) initialDiffRows(
	width, height int,
	styles semanticStyles,
) ([]string, []diffViewRow, int) {
	rendered := make([]string, 0, height)
	rows := make([]diffViewRow, 0, height)
	contentHeight := height
	if model.settings.diffView == DiffViewSideBySide &&
		width < sideBySideContentWidth+diffCommentAnchorColumns {
		rendered = append(rendered, sideBySideFallbackLine(width, styles))
		rows = append(rows, diffViewRow{})
		contentHeight--
	}
	return rendered, rows, contentHeight
}

func sideBySideFallbackLine(width int, styles semanticStyles) string {
	return renderNoticeLine(Notice{
		Kind: NoticeWarning,
		Text: NoticeText(fmt.Sprintf(
			"Side-by-side needs %d diff columns; showing unified.",
			sideBySideContentWidth+diffCommentAnchorColumns,
		)),
	}, width, styles)
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
	oldNumber, newNumber := uint32(0), uint32(0)
	hasOldNumber, hasNewNumber := false, false
	oldAnchor, newAnchor := " ", " "
	if value, ok := line.OldLine(); ok {
		oldNumber, hasOldNumber = value.Uint32(), true
		oldAnchor = model.commentAnchorCell(fileID, review.LineOld, value, styles)
	}
	if value, ok := line.NewLine(); ok {
		newNumber, hasNewNumber = value.Uint32(), true
		newAnchor = model.commentAnchorCell(fileID, review.LineNew, value, styles)
	}
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
	prefix := nativeDiffLinePrefix(
		numberWidths, oldNumber, hasOldNumber, oldAnchor,
		newNumber, hasNewNumber, newAnchor, styledMarker,
	)
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
		case kind == review.PatchLineContext && model.settings.colorEnabled:
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
) ([]string, []diffViewRow) {
	leftWidth, rightWidth := model.sideBySideWidths(fileID, width)
	rendered := make([]string, 0, min(height, len(lines)))
	rows := make([]diffViewRow, 0, min(height, len(lines)))
	for _, line := range lines {
		if len(rendered) == height {
			break
		}
		selected := line.Number == model.diffCursor
		inVisualRange := model.mode == ModeVisual &&
			line.Number >= min(model.visualAnchor, model.diffCursor) &&
			line.Number <= max(model.visualAnchor, model.diffCursor)
		lineStyles := styles
		if selected {
			lineStyles = styles.onCursorLine()
		}
		cursor := " "
		if selected {
			cursor = "›"
			if model.focus == FocusDiff {
				cursor = lineStyles.focused.Render("▸")
			}
		}
		if inVisualRange {
			cursor = lineStyles.visualSelection.Render("┃")
		}
		reviewed := " "
		if hunk, ok := line.Hunk(fileID); ok && model.review.HunkReviewed(hunk) {
			reviewed = lineStyles.reviewed.Render("✓")
		}
		text := line.Text.String()
		switch line.Kind() {
		case review.PatchLineContext, review.PatchLineAddition, review.PatchLineDeletion:
			if text != "" {
				text = text[1:]
			}
		case review.PatchLineMetadata, review.PatchLineHunkHeader,
			review.PatchLineNoNewline:
		}
		leftSegments := model.renderSidePatchLine(
			fileID,
			line,
			review.LineOld,
			text,
			max(leftWidth-2, 1),
			numberWidths.old,
			lineStyles,
		)
		rightSegments := model.renderSidePatchLine(
			fileID,
			line,
			review.LineNew,
			text,
			rightWidth,
			numberWidths.new,
			lineStyles,
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
			separatorStyle := lineStyles.border
			if model.focus == FocusDiff {
				separatorStyle = lineStyles.borderFocused
			}
			value := lineCursor + lineReviewed +
				fitCellWidth(leftValue, max(leftWidth-2, 1)) +
				separatorStyle.Render("┃") +
				fitCellWidth(rightValue, rightWidth)
			if selected {
				value = renderPersistentStyle(lineStyles.cursorLine, value)
			}
			rendered = append(rendered, value)
			rows = append(rows, diffViewRow{line: line.Number, selectable: true})
		}
	}
	if len(rendered) == 0 {
		return []string{styles.muted.Render("Empty patch.")}, nil
	}
	return rendered, rows
}

func (model *Model) sideBySideWidths(fileID review.FileID, width int) (int, int) {
	file, ok := model.review.Snapshot().File(fileID)
	kind := review.ChangeModified
	if ok {
		kind = file.Change().Kind()
	}
	return sideBySideWidths(kind, width)
}

func sideBySideWidths(kind review.ChangeKind, width int) (int, int) {
	available := max(width-1, 2)
	left := available / 2
	switch kind {
	case review.ChangeAdded:
		left = max(available/3, 1)
	case review.ChangeDeleted:
		left = max(available-available/3, 1)
	case review.ChangeModified, review.ChangeRenamed, review.ChangeCopied,
		review.ChangeBinary, review.ChangeRenameOnly, review.ChangeModeOnly,
		review.ChangeSubmodule, review.ChangeObjectTypeChanged:
	}
	right := max(available-left, 1)
	return left, right
}

func renderPersistentStyle(style lipgloss.Style, value string) string {
	const sentinel = "\x00"
	styled := style.Render(sentinel)
	prefix, suffix, ok := strings.Cut(styled, sentinel)
	if !ok || prefix == "" {
		return value
	}
	value = strings.ReplaceAll(value, ansi.ResetStyle, ansi.ResetStyle+prefix)
	value = strings.ReplaceAll(value, "\x1b[0m", "\x1b[0m"+prefix)
	return prefix + value + suffix
}

func (model *Model) renderSidePatchLine(
	fileID review.FileID,
	line review.PatchLine,
	side review.LineSide,
	text string,
	width, numberWidth int,
	styles semanticStyles,
) []string {
	kind := line.Kind()
	if kind == review.PatchLineNoNewline && !line.AppliesToSide(side) {
		return nil
	}
	if kind == review.PatchLineMetadata || kind == review.PatchLineHunkHeader ||
		kind == review.PatchLineNoNewline {
		raw := line.Text.String()
		content := safeText(raw)
		if !model.settings.wrap {
			content = safeAndClip(raw, width)
		}
		segments := model.wrappedLine(content, width)
		for index, segment := range segments {
			switch {
			case kind == review.PatchLineHunkHeader:
				segments[index] = styles.hunk.Render(segment)
			case strings.HasPrefix(raw, "+++") || strings.HasPrefix(raw, "---"):
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
	sanitizer := terminalTextRenderer{}
	var value string
	if content.spans == nil {
		value = sanitizer.render(raw)
	} else {
		styled, spansLimited := renderSyntaxSpans(raw, content.spans, content.styles)
		value = styled
		limited = limited || spansLimited
	}
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

func nativeDiffLinePrefix(
	widths diffLineNumberWidths,
	oldNumber uint32,
	hasOld bool,
	oldAnchor string,
	newNumber uint32,
	hasNew bool,
	newAnchor string,
	marker string,
) string {
	var storage [96]byte
	prefix := storage[:0]
	prefix = appendPaddedDiffNumber(prefix, widths.old, oldNumber, hasOld)
	prefix = append(prefix, oldAnchor...)
	prefix = append(prefix, ' ')
	prefix = appendPaddedDiffNumber(prefix, widths.new, newNumber, hasNew)
	prefix = append(prefix, newAnchor...)
	prefix = append(prefix, ' ')
	prefix = append(prefix, marker...)
	return string(prefix)
}

func appendPaddedDiffNumber(
	destination []byte,
	width int,
	value uint32,
	present bool,
) []byte {
	digits := 0
	if present {
		digits = decimalDigitWidth(value)
	}
	for range max(width-digits, 0) {
		destination = append(destination, ' ')
	}
	if present {
		destination = strconv.AppendUint(destination, uint64(value), 10)
	}
	return destination
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
			if start > len(raw) {
				return
			}
			if start < spanEnd || end <= start {
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
		} else {
			value = styles.base.Render(value)
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
