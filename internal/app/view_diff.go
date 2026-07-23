package app

import (
	"fmt"
	"iter"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"github.com/jacoelho/rvw/internal/review"
)

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
