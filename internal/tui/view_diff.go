package tui

import (
	"fmt"
	"iter"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/tui/render"
)

func (model *Model) diffCellPresentation(
	line change.PatchLine,
	placement diffCellPlacement,
	styles semanticStyles,
) diffCellPresentation {
	return diffCellStyles(model.diffCellState(line, placement), styles)
}

func (model *Model) diffCellState(
	line change.PatchLine,
	placement diffCellPlacement,
) diffCellState {
	return diffCellState{
		kind:      line.Kind(),
		placement: placement,
		selection: model.diffCellSelection(line),
		colorMode: model.diffCellColorMode(),
	}
}

func (model *Model) diffCellSelection(line change.PatchLine) diffCellSelection {
	activeCursor := model.workspaceOwnsInput() && model.focus == FocusDiff
	if model.mode == ModeVisual {
		if line.Number == model.diffCursor {
			if activeCursor {
				return diffCellSelectionActive
			}
			return diffCellSelectionRetained
		}
		if line.Number == model.visualAnchor {
			return diffCellSelectionRetained
		}
		if line.Number > min(model.visualAnchor, model.diffCursor) &&
			line.Number < max(model.visualAnchor, model.diffCursor) {
			return diffCellSelectionRange
		}
		return diffCellSelectionNone
	}
	if line.Number != model.diffCursor {
		return diffCellSelectionNone
	}
	if activeCursor {
		return diffCellSelectionActive
	}
	return diffCellSelectionRetained
}

func (model *Model) diffCellColorMode() diffCellColorMode {
	if model.settings.colorEnabled {
		return diffCellColor
	}
	return diffCellMonochrome
}

func (model *Model) sideBySideChromePresentation(
	line change.PatchLine,
	styles semanticStyles,
) diffCellPresentation {
	selection := model.diffCellSelection(line)
	accent := lipgloss.NewStyle()
	if line.Kind() == change.PatchLineHunkHeader {
		accent = styles.hunkBand
	}
	base := diffCellBase(selection, model.diffCellColorMode(), accent, styles)
	gutter := styles.gutter.Inherit(base)
	if selection == diffCellSelectionActive {
		gutter = styles.activeCursorRow
	}
	return diffCellPresentation{
		base:   base,
		gutter: gutter,
	}
}

func diffSelectionMarker(
	selection diffCellSelection,
	segment int,
	cell diffCellPresentation,
	styles semanticStyles,
) string {
	switch selection {
	case diffCellSelectionActive, diffCellSelectionRetained:
		if segment == 0 {
			return cell.gutter.Render(">")
		}
	case diffCellSelectionRange:
		return styles.selectionRail.Render("│")
	case diffCellSelectionNone:
	}
	return cell.gutter.Render(" ")
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

func diffCellPlacementForSide(line change.PatchLine, side change.LineSide) diffCellPlacement {
	applicable := false
	switch line.Kind() {
	case change.PatchLineMetadata, change.PatchLineHunkHeader:
		applicable = true
	case change.PatchLineNoNewline:
		applicable = line.AppliesToSide(side)
	case change.PatchLineContext, change.PatchLineAddition, change.PatchLineDeletion:
		switch side {
		case change.LineOld:
			_, applicable = line.OldLine()
		case change.LineNew:
			_, applicable = line.NewLine()
		}
	}
	switch side {
	case change.LineOld:
		if applicable {
			return diffCellOldApplicable
		}
		return diffCellOldNeutral
	case change.LineNew:
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
	case change.PatchUnavailable:
		return []string{styles.muted.Render(model.patchRecoveryLine(
			"Not loaded", "", width,
		))}, nil
	case change.PatchPending:
		return []string{styles.pending.Render("Loading patch…")}, nil
	case change.PatchFailed:
		return []string{styles.failure.Render(model.patchRecoveryLine(
			"Failed", state.Detail.String(), width,
		))}, nil
	case change.PatchCancelled:
		return []string{styles.warning.Render(model.patchRecoveryLine(
			"Cancelled", "", width,
		))}, nil
	case change.PatchLimited:
		return []string{styles.warning.Render(model.patchRecoveryLine(
			"Limited", state.Detail.String(), width,
		))}, nil
	case change.PatchReady:
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
		selection := model.diffCellSelection(line)
		reviewed := cell.gutter.Render(" ")
		if hunk, exists := line.Hunk(active); exists && model.review.HunkReviewed(hunk) {
			reviewed = styles.reviewed.Inherit(cell.base).Render("✓")
			if selection == diffCellSelectionActive {
				reviewed = cell.gutter.Render("✓")
			}
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
			prefix := diffSelectionMarker(selection, segmentIndex, cell, styles)
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

func (model *Model) patchRecoveryLine(state, detail string, width int) string {
	retry := "no retry"
	bindings := keymap.SelectBindings(
		model.settings.keymap.Bindings(keymap.Scope{
			Context: keymap.ContextNormal,
			Focus:   keymap.FocusDiff,
		}),
		[]keymap.Action{keymap.ActionActivate},
	)
	if len(bindings) != 0 {
		steps := make([]string, 0, 3)
		switch model.keyContext() {
		case keymap.ContextNormal:
		case keymap.ContextVisual:
			steps = append(steps, "leave visual mode")
		case keymap.ContextCommentEdit:
			steps = append(steps, "leave comment editor")
		default:
			steps = append(steps, "return to workspace")
		}
		if model.focus != FocusDiff {
			steps = append(steps, "focus diff")
		}
		steps = append(steps, bindings[0].Chord.Display()+" retry")
		retry = strings.Join(steps, ", then ")
	}
	recovery := state + "; " + retry
	recoveryWidth := lipgloss.Width(recovery)
	if detail == "" || width <= recoveryWidth+2 {
		return safeAndClip(recovery, width)
	}
	return recovery + ": " + safeAndClip(detail, width-recoveryWidth-2)
}

func (model *Model) renderUnifiedPatchLine(
	fileID change.FileID,
	line change.PatchLine,
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
		oldAnchor = model.commentAnchorCell(fileID, change.LineOld, value, cell, styles)
	}
	if value, ok := line.NewLine(); ok {
		newNumber, hasNewNumber = value.Uint32(), true
		newAnchor = model.commentAnchorCell(fileID, change.LineNew, value, cell, styles)
	}
	marker := ""
	content := raw
	if kind == change.PatchLineContext || kind == change.PatchLineAddition ||
		kind == change.PatchLineDeletion {
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
	if kind == change.PatchLineMetadata || kind == change.PatchLineHunkHeader ||
		kind == change.PatchLineNoNewline {
		contentStyle := cell.content
		if kind == change.PatchLineMetadata &&
			(strings.HasPrefix(raw, "+++") || strings.HasPrefix(raw, "---")) &&
			!cell.suppressSyntax {
			contentStyle = styles.heading.Inherit(cell.base)
		}
		renderedContent = contentStyle.Render(render.SafeText(content))
		if !model.settings.wrap {
			renderedContent = clipStyled(renderedContent, model.diffLeft, contentWidth)
		}
	} else {
		renderedContent = model.renderSyntaxContent(syntaxContent{
			raw:            content,
			spans:          model.syntaxSpans(line),
			width:          contentWidth,
			truncated:      line.Truncated,
			wrap:           model.settings.wrap,
			styles:         styles,
			cellStyle:      cell.content,
			suppressSyntax: cell.suppressSyntax,
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
	file change.FileID,
	side change.LineSide,
	line change.LineNumber,
	cell diffCellPresentation,
	styles semanticStyles,
) string {
	if !model.review.HasCommentAnchor(file, side, line) {
		return cell.gutter.Render(" ")
	}
	if cell.suppressSyntax {
		return cell.gutter.Render("●")
	}
	return styles.commentAnchor.Inherit(cell.base).Render("●")
}

func (model *Model) sideBySideDiffLines(
	fileID change.FileID,
	lines []change.PatchLine,
	width, height int,
	numberWidths diffLineNumberWidths,
	styles semanticStyles,
) ([]string, []uint32) {
	leftWidth, rightWidth := model.sideBySideWidths(fileID, width)
	rendered := make([]string, 0, min(height, len(lines)))
	rows := make([]uint32, 0, min(height, len(lines)))
	for _, line := range lines {
		if len(rendered) == height {
			break
		}
		lineCell := model.sideBySideChromePresentation(line, styles)
		leftCell := model.diffCellPresentation(
			line,
			diffCellPlacementForSide(line, change.LineOld),
			styles,
		)
		rightCell := model.diffCellPresentation(
			line,
			diffCellPlacementForSide(line, change.LineNew),
			styles,
		)
		separator := styles.gutter.Inherit(lineCell.base).Render("│")
		selection := model.diffCellSelection(line)
		if selection == diffCellSelectionActive {
			separator = styles.activeCursorRow.Render("│")
		}
		reviewed := lineCell.gutter.Render(" ")
		if hunk, ok := line.Hunk(fileID); ok && model.review.HunkReviewed(hunk) {
			reviewed = styles.reviewed.Inherit(lineCell.base).Render("✓")
			if selection == diffCellSelectionActive {
				reviewed = lineCell.gutter.Render("✓")
			}
		}
		text := line.Text.String()
		switch line.Kind() {
		case change.PatchLineContext, change.PatchLineAddition, change.PatchLineDeletion:
			if text != "" {
				text = text[1:]
			}
		case change.PatchLineMetadata, change.PatchLineHunkHeader,
			change.PatchLineNoNewline:
		}
		leftSegments := model.renderSidePatchLine(
			fileID,
			line,
			change.LineOld,
			text,
			max(leftWidth-2, 1),
			numberWidths.old,
			leftCell,
			styles,
		)
		rightSegments := model.renderSidePatchLine(
			fileID,
			line,
			change.LineNew,
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
			lineCursor := diffSelectionMarker(selection, segment, lineCell, styles)
			lineReviewed := lineCell.gutter.Render(" ")
			if segment == 0 {
				lineReviewed = reviewed
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

func (model *Model) sideBySideWidths(fileID change.FileID, width int) (int, int) {
	kind := change.ChangeModified
	if file, ok := model.review.Snapshot().File(fileID); ok {
		kind = file.Change().Kind()
	}
	return sideBySideWidths(kind, width)
}

func sideBySideWidths(kind change.ChangeKind, width int) (int, int) {
	available := max(width-1, 2)
	left := available / 2
	switch kind {
	case change.ChangeAdded:
		left = max(available/3, 1)
	case change.ChangeDeleted:
		left = max(available*2/3, 1)
	case change.ChangeModified, change.ChangeRenamed, change.ChangeCopied,
		change.ChangeBinary, change.ChangeRenameOnly, change.ChangeModeOnly,
		change.ChangeSubmodule, change.ChangeObjectTypeChanged:
	}
	return left, max(available-left, 1)
}

func (model *Model) renderSidePatchLine(
	fileID change.FileID,
	line change.PatchLine,
	side change.LineSide,
	text string,
	width, numberWidth int,
	cell diffCellPresentation,
	styles semanticStyles,
) []string {
	kind := line.Kind()
	if kind == change.PatchLineNoNewline && !line.AppliesToSide(side) {
		return nil
	}
	if kind == change.PatchLineMetadata || kind == change.PatchLineHunkHeader ||
		kind == change.PatchLineNoNewline {
		raw := line.Text.String()
		contentStyle := cell.content
		if kind == change.PatchLineMetadata &&
			(strings.HasPrefix(raw, "+++") || strings.HasPrefix(raw, "---")) &&
			!cell.suppressSyntax {
			contentStyle = styles.heading.Inherit(cell.base)
		}
		content := contentStyle.Render(render.SafeText(raw))
		if !model.settings.wrap {
			content = clipStyled(content, 0, width)
		}
		segments := model.wrappedLine(content, width)
		return segments
	}
	number := ""
	anchor := cell.gutter.Render(" ")
	var spans iter.Seq[change.SyntaxSpan]
	sourceLine, hasSourceLine := line.OldLine()
	if side == change.LineNew {
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
			raw:            text,
			spans:          spans,
			width:          contentWidth,
			truncated:      line.Truncated,
			wrap:           model.settings.wrap,
			styles:         styles,
			cellStyle:      cell.content,
			suppressSyntax: cell.suppressSyntax,
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

func newDiffLineNumberWidths(state change.PatchState) diffLineNumberWidths {
	widths := diffLineNumberWidths{old: 1, new: 1}
	if line, ok := state.MaxLineNumber(change.LineOld); ok {
		widths.old = decimalDigitWidth(line.Uint32())
	}
	if line, ok := state.MaxLineNumber(change.LineNew); ok {
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
	line change.PatchLine,
	side change.LineSide,
) iter.Seq[change.SyntaxSpan] {
	switch side {
	case change.LineOld:
		sourceLine, ok := line.OldLine()
		if !ok {
			return nil
		}
		return model.syntaxOld.Spans(sourceLine)
	case change.LineNew:
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
	styles         semanticStyles
	cellStyle      lipgloss.Style
	spans          iter.Seq[change.SyntaxSpan]
	raw            string
	width          int
	truncated      bool
	wrap           bool
	suppressSyntax bool
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
	sanitizer := render.Renderer{}
	var value string
	if content.spans == nil || content.suppressSyntax {
		value = content.cellStyle.Render(sanitizer.Render(raw))
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
	spans iter.Seq[change.SyntaxSpan],
	styles semanticStyles,
	cellStyle lipgloss.Style,
) (string, bool) {
	nextSpan, stopSpans := iter.Pull(spans)
	defer stopSpans()
	var span change.SyntaxSpan
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
	sanitizer := render.Renderer{}
	if !hasSpan {
		return cellStyle.Render(sanitizer.Render(raw)), limited
	}
	var rendered strings.Builder
	rendered.Grow(len(raw))
	var runKind change.SyntaxKind
	runStart, runEnd := 0, 0
	hasRun := false
	flushRun := func() {
		if !hasRun || runStart == runEnd {
			return
		}
		value := sanitizer.Render(raw[runStart:runEnd])
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
		kind := change.SyntaxKind(0)
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

func syntaxStyle(kind change.SyntaxKind, styles semanticStyles) lipgloss.Style {
	switch kind {
	case change.SyntaxKeyword:
		return styles.keyword
	case change.SyntaxString:
		return styles.literal
	case change.SyntaxComment:
		return styles.comment
	case change.SyntaxNumber:
		return styles.number
	default:
		return lipgloss.NewStyle()
	}
}
