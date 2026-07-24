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

func (model *Model) diffCellPresentation(
	line review.PatchLine,
	placement diffCellPlacement,
	styles semanticStyles,
) diffCellPresentation {
	return diffCellStyles(model.diffCellState(line, placement), styles)
}

func (model *Model) diffCellState(
	line review.PatchLine,
	placement diffCellPlacement,
) diffCellState {
	return diffCellState{
		kind:      line.Kind(),
		placement: placement,
		selection: model.diffCellSelection(line),
		colorMode: model.diffCellColorMode(),
	}
}

func (model *Model) diffCellSelection(line review.PatchLine) diffCellSelection {
	if model.mode == ModeVisual &&
		line.Number >= min(model.visualAnchor, model.diffCursor) &&
		line.Number <= max(model.visualAnchor, model.diffCursor) {
		return diffCellRangeSelected
	}
	if line.Number == model.diffCursor {
		return diffCellCursorSelected
	}
	return diffCellNotSelected
}

func (model *Model) diffCellColorMode() diffCellColorMode {
	if model.settings.colorEnabled {
		return diffCellColor
	}
	return diffCellMonochrome
}

func (model *Model) sideBySideChromePresentation(
	line review.PatchLine,
	styles semanticStyles,
) diffCellPresentation {
	selection := model.diffCellSelection(line)
	accent := lipgloss.NewStyle()
	if line.Kind() == review.PatchLineHunkHeader {
		accent = styles.hunkBand
	}
	base := diffCellBase(selection, model.diffCellColorMode(), accent, styles)
	return diffCellPresentation{
		base:   base,
		gutter: styles.gutter.Inherit(base),
	}
}

type patchLineDemand struct {
	count uint32
	bytes int64
}

func (model *Model) patchLineDemand(width, height int) patchLineDemand {
	demand := patchLineDemand{
		count: uint32FromInt(height + viewportOverscan),
		bytes: max(int64(width)*8, 4_096),
	}
	if model.settings.wrap {
		demand.bytes = max(
			int64(width)*int64(height+viewportOverscan)*4,
			demand.bytes,
		)
	}
	return demand
}

func diffCellPlacementForSide(line review.PatchLine, side review.LineSide) diffCellPlacement {
	applicable := false
	switch line.Kind() {
	case review.PatchLineMetadata, review.PatchLineHunkHeader:
		applicable = true
	case review.PatchLineNoNewline:
		applicable = line.AppliesToSide(side)
	case review.PatchLineContext, review.PatchLineAddition, review.PatchLineDeletion:
		switch side {
		case review.LineOld:
			_, applicable = line.OldLine()
		case review.LineNew:
			_, applicable = line.NewLine()
		}
	}
	switch side {
	case review.LineOld:
		if applicable {
			return diffCellOldApplicable
		}
		return diffCellOldNeutral
	case review.LineNew:
		if applicable {
			return diffCellNewApplicable
		}
		return diffCellNewNeutral
	default:
		return 0
	}
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
	demand := model.patchLineDemand(width, height)
	lines := model.review.PatchLines(active, model.diffTop, demand.count, demand.bytes)
	numberWidths := newDiffLineNumberWidths(state)
	if model.settings.diffView == DiffViewSideBySide &&
		width >= sideBySideContentWidth+diffCommentAnchorColumns {
		return model.sideBySideDiffLines(active, lines, width, height, numberWidths, styles)
	}
	rendered := make([]string, 0, min(height, len(lines)))
	rows := make([]uint32, 0, min(height, len(lines)))
	for _, line := range lines {
		cell := model.diffCellPresentation(line, diffCellUnified, styles)
		cursor := cell.gutter.Render(" ")
		if line.Number == model.diffCursor {
			cursor = cell.gutter.Render("›")
			if model.focus == FocusDiff {
				cursor = styles.focused.Inherit(cell.base).Render("▸")
			}
		}
		if model.mode == ModeVisual &&
			line.Number >= min(model.visualAnchor, model.diffCursor) &&
			line.Number <= max(model.visualAnchor, model.diffCursor) {
			cursor = cell.gutter.Render("│")
		}
		reviewed := cell.gutter.Render(" ")
		if hunk, exists := line.Hunk(active); exists && model.review.HunkReviewed(hunk) {
			reviewed = styles.reviewed.Inherit(cell.base).Render("✓")
		}
		contentWidth := max(width-2, 1)
		segments := model.renderUnifiedPatchLine(
			active,
			line,
			contentWidth,
			numberWidths,
			cell,
			styles,
		)
		for segmentIndex, segment := range segments {
			if len(rendered) == height {
				break
			}
			prefix := cell.gutter.Render(" ")
			if segmentIndex == 0 {
				prefix = cursor
			}
			mark := cell.gutter.Render(" ")
			if segmentIndex == 0 {
				mark = reviewed
			}
			rendered = append(
				rendered,
				prefix+mark+fitStyledCellWidth(segment, contentWidth, cell.base),
			)
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

func (model *Model) renderUnifiedPatchLine(
	fileID review.FileID,
	line review.PatchLine,
	width int,
	numberWidths diffLineNumberWidths,
	cell diffCellPresentation,
	styles semanticStyles,
) []string {
	raw := line.Text.String()
	kind := line.Kind()
	oldNumber, newNumber := uint32(0), uint32(0)
	hasOldNumber, hasNewNumber := false, false
	oldAnchor, newAnchor := cell.gutter.Render(" "), cell.gutter.Render(" ")
	if value, ok := line.OldLine(); ok {
		oldNumber, hasOldNumber = value.Uint32(), true
		oldAnchor = model.commentAnchorCell(fileID, review.LineOld, value, cell, styles)
	}
	if value, ok := line.NewLine(); ok {
		newNumber, hasNewNumber = value.Uint32(), true
		newAnchor = model.commentAnchorCell(fileID, review.LineNew, value, cell, styles)
	}
	marker := ""
	content := raw
	if kind == review.PatchLineContext || kind == review.PatchLineAddition ||
		kind == review.PatchLineDeletion {
		if raw != "" {
			marker, content = raw[:1], raw[1:]
		}
	}
	styledMarker := cell.content.Render(marker)
	prefix := renderDiffLinePrefix(
		numberWidths,
		diffLineNumberValue{value: oldNumber, present: hasOldNumber},
		oldAnchor,
		diffLineNumberValue{value: newNumber, present: hasNewNumber},
		newAnchor,
		styledMarker,
		cell,
	)
	prefixWidth := lipgloss.Width(prefix)
	if width <= prefixWidth {
		return []string{cell.content.Render(safeAndClip(raw, width))}
	}
	contentWidth := width - prefixWidth
	var renderedContent string
	if kind == review.PatchLineMetadata || kind == review.PatchLineHunkHeader ||
		kind == review.PatchLineNoNewline {
		contentStyle := cell.content
		if kind == review.PatchLineMetadata &&
			(strings.HasPrefix(raw, "+++") || strings.HasPrefix(raw, "---")) {
			contentStyle = styles.heading.Inherit(cell.base)
		}
		renderedContent = contentStyle.Render(safeText(content))
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
			cellStyle: cell.content,
		})
	}
	segments := model.wrappedLine(renderedContent, contentWidth)
	rendered := make([]string, 0, len(segments))
	for index, segment := range segments {
		segmentPrefix := cell.gutter.Render(strings.Repeat(" ", prefixWidth))
		if index == 0 {
			segmentPrefix = prefix
		}
		value := segmentPrefix + fitStyledCellWidth(segment, contentWidth, cell.base)
		rendered = append(rendered, value)
	}
	return rendered
}

func (model *Model) commentAnchorCell(
	file review.FileID,
	side review.LineSide,
	line review.LineNumber,
	cell diffCellPresentation,
	styles semanticStyles,
) string {
	if !model.review.HasCommentAnchor(file, side, line) {
		return cell.gutter.Render(" ")
	}
	return styles.commentAnchor.Inherit(cell.base).Render("●")
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
	rendered := make([]string, 0, min(height, len(lines)))
	rows := make([]uint32, 0, min(height, len(lines)))
	for _, line := range lines {
		if len(rendered) == height {
			break
		}
		lineCell := model.sideBySideChromePresentation(line, styles)
		leftCell := model.diffCellPresentation(
			line,
			diffCellPlacementForSide(line, review.LineOld),
			styles,
		)
		rightCell := model.diffCellPresentation(
			line,
			diffCellPlacementForSide(line, review.LineNew),
			styles,
		)
		separator := styles.gutter.Inherit(lineCell.base).Render("│")
		cursor := lineCell.gutter.Render(" ")
		if line.Number == model.diffCursor {
			cursor = lineCell.gutter.Render("›")
			if model.focus == FocusDiff {
				cursor = styles.focused.Inherit(lineCell.base).Render("▸")
			}
		}
		if model.mode == ModeVisual &&
			line.Number >= min(model.visualAnchor, model.diffCursor) &&
			line.Number <= max(model.visualAnchor, model.diffCursor) {
			cursor = lineCell.gutter.Render("│")
		}
		reviewed := lineCell.gutter.Render(" ")
		if hunk, ok := line.Hunk(fileID); ok && model.review.HunkReviewed(hunk) {
			reviewed = styles.reviewed.Inherit(lineCell.base).Render("✓")
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
			leftCell,
			styles,
		)
		rightSegments := model.renderSidePatchLine(
			fileID,
			line,
			review.LineNew,
			text,
			rightWidth,
			numberWidths.new,
			rightCell,
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
			lineCursor, lineReviewed := lineCell.gutter.Render(" "), lineCell.gutter.Render(" ")
			if segment == 0 {
				lineCursor, lineReviewed = cursor, reviewed
			}
			rendered = append(
				rendered,
				lineCursor+lineReviewed+
					fitStyledCellWidth(leftValue, max(leftWidth-2, 1), leftCell.base)+
					separator+
					fitStyledCellWidth(rightValue, rightWidth, rightCell.base),
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
	cell diffCellPresentation,
	styles semanticStyles,
) []string {
	kind := line.Kind()
	if kind == review.PatchLineNoNewline && !line.AppliesToSide(side) {
		return nil
	}
	if kind == review.PatchLineMetadata || kind == review.PatchLineHunkHeader ||
		kind == review.PatchLineNoNewline {
		raw := line.Text.String()
		contentStyle := cell.content
		if kind == review.PatchLineMetadata &&
			(strings.HasPrefix(raw, "+++") || strings.HasPrefix(raw, "---")) {
			contentStyle = styles.heading.Inherit(cell.base)
		}
		content := contentStyle.Render(safeText(raw))
		if !model.settings.wrap {
			content = clipStyled(content, 0, width)
		}
		segments := model.wrappedLine(content, width)
		return segments
	}
	number := ""
	anchor := cell.gutter.Render(" ")
	var spans iter.Seq[review.SyntaxSpan]
	sourceLine, hasSourceLine := line.OldLine()
	if side == review.LineNew {
		sourceLine, hasSourceLine = line.NewLine()
	}
	if hasSourceLine {
		number = fmt.Sprint(sourceLine.Uint32())
		anchor = model.commentAnchorCell(fileID, side, sourceLine, cell, styles)
		spans = model.syntaxSpansForSide(line, side)
	}
	prefix := cell.gutter.Render(fmt.Sprintf("%*s", numberWidth, number)) +
		anchor + cell.gutter.Render(" ")
	prefixWidth := lipgloss.Width(prefix)
	if width <= prefixWidth {
		return []string{cell.content.Render(safeAndClip(text, width))}
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
			cellStyle: cell.content,
		})
	}
	segments := model.wrappedLine(content, contentWidth)
	rendered := make([]string, 0, len(segments))
	for index, segment := range segments {
		segmentPrefix := cell.gutter.Render(strings.Repeat(" ", prefixWidth))
		if index == 0 {
			segmentPrefix = prefix
		}
		value := segmentPrefix + fitStyledCellWidth(segment, contentWidth, cell.base)
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
	cellStyle lipgloss.Style
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
		value = content.cellStyle.Render(sanitizer.render(raw))
	} else {
		styled, spansLimited := renderSyntaxSpans(
			raw,
			content.spans,
			content.styles,
			content.cellStyle,
		)
		value = styled
		limited = limited || spansLimited
	}
	if content.wrap {
		if limited {
			value += content.cellStyle.Render("…")
		}
		return value
	}
	beyondViewport := lipgloss.Width(value) > model.diffLeft+content.width
	value = clipStyled(value, left, content.width)
	if limited || beyondViewport {
		if content.width == 1 {
			return content.cellStyle.Render("…")
		}
		return lipgloss.NewStyle().MaxWidth(content.width-1).Render(value) +
			content.cellStyle.Render("…")
	}
	return value
}

type diffLineNumberValue struct {
	value   uint32
	present bool
}

func renderDiffLinePrefix(
	widths diffLineNumberWidths,
	oldNumber diffLineNumberValue,
	oldAnchor string,
	newNumber diffLineNumberValue,
	newAnchor string,
	marker string,
	cell diffCellPresentation,
) string {
	return cell.gutter.Render(paddedDiffNumber(widths.old, oldNumber)) +
		oldAnchor + cell.gutter.Render(" ") +
		cell.gutter.Render(paddedDiffNumber(widths.new, newNumber)) +
		newAnchor + cell.gutter.Render(" ") + marker
}

func paddedDiffNumber(width int, number diffLineNumberValue) string {
	var storage [32]byte
	return string(appendPaddedDiffNumber(storage[:0], width, number))
}

func appendPaddedDiffNumber(
	destination []byte,
	width int,
	number diffLineNumberValue,
) []byte {
	digits := 0
	if number.present {
		digits = decimalDigitWidth(number.value)
	}
	for range max(width-digits, 0) {
		destination = append(destination, ' ')
	}
	if number.present {
		destination = strconv.AppendUint(destination, uint64(number.value), 10)
	}
	return destination
}

func renderSyntaxSpans(
	raw string,
	spans iter.Seq[review.SyntaxSpan],
	styles semanticStyles,
	cellStyle lipgloss.Style,
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
		return cellStyle.Render(sanitizer.render(raw)), limited
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
		style := cellStyle
		if runKind != 0 {
			style = syntaxStyle(runKind, styles).Inherit(cellStyle)
		}
		value = style.Render(value)
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

func fitStyledCellWidth(value string, width int, base lipgloss.Style) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) > width {
		value = clipStyled(value, 0, width)
	}
	padding := max(width-lipgloss.Width(value), 0)
	return base.Render(value) + base.Render(strings.Repeat(" ", padding))
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
