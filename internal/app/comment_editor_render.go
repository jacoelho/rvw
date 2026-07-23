package app

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const (
	commentEditorRowSourceBytes = 4_000
	commentEditorRowRenderBytes = commentEditorRowSourceBytes + 6
)

// commentEditorPreparedRow borrows bounded windows from the editor's canonical
// rune and grapheme buffers. Its full-slice expressions prevent a consumer
// from reaching beyond the prepared window.
type commentEditorPreparedRow struct {
	runes       []rune
	graphemes   []commentEditorGrapheme
	runeBase    uint32
	sourceBytes uint32
	cellStart   uint32
	prefix      bool
	suffix      bool
	placeholder bool
}

func (editor *commentEditor) View() string {
	return renderCommentEditorRows(
		editor.visibleRows[:editor.visibleCount:editor.visibleCount],
		editor.Width(),
		editor.Height(),
	)
}

func (editor *commentEditor) Cursor() *tea.Cursor {
	y := editor.currentRow - editor.viewportRow
	if y < 0 || y >= editor.visibleCount {
		return nil
	}
	return tea.NewCursor(min(max(editor.preparedCursorX, 0), editor.Width()-1), y)
}

func renderCommentEditorRows(rows []commentEditorPreparedRow, width, height int) string {
	var view strings.Builder
	for index := range height {
		if index != 0 {
			view.WriteByte('\n')
		}
		if index < len(rows) {
			renderCommentEditorRow(&view, rows[index], width)
			continue
		}
		view.WriteString(strings.Repeat(" ", width))
	}
	return view.String()
}

func renderCommentEditorRow(
	output *strings.Builder,
	row commentEditorPreparedRow,
	width int,
) {
	output.Grow(min(int(row.sourceBytes)+6+width, commentEditorRowRenderBytes))
	cells := 0
	if row.placeholder {
		placeholder := commentEditorPlaceholder
		visible := min(len(placeholder), width)
		output.WriteString(placeholder[:visible])
		cells = visible
	}
	if row.prefix && cells < width {
		output.WriteRune('…')
		cells++
	}
	runeStart := int(row.runeBase)
	omittedTooWide := false
	for _, grapheme := range row.graphemes {
		reserve := 0
		if row.suffix {
			reserve = 1
		}
		graphemeWidth := int(grapheme.width)
		runeEnd := int(grapheme.end)
		if graphemeWidth > width {
			if cells < width {
				output.WriteRune('…')
				cells++
			}
			omittedTooWide = true
			runeStart = runeEnd
			continue
		}
		if graphemeWidth > 0 && cells+graphemeWidth+reserve > width {
			break
		}
		if grapheme.class.isWhitespace() {
			output.WriteByte(' ')
		} else {
			output.WriteString(string(row.runes[runeStart-int(row.runeBase) : runeEnd-int(row.runeBase)]))
		}
		cells += graphemeWidth
		runeStart = runeEnd
	}
	if row.suffix && !omittedTooWide && cells < width {
		output.WriteRune('…')
		cells++
	}
	if cells < width {
		output.WriteString(strings.Repeat(" ", width-cells))
	}
}

func (editor *commentEditor) rebuildRows() int64 {
	for index := range editor.graphemes {
		editor.graphemes[index].cellEnd = 0
	}
	editor.rows = editor.rows[:0]
	overlap := int64(0)
	maximum := len(editor.graphemes) + 1
	editor.appendRowIndex(0, 0, maximum, &overlap)
	lineStart := 0
	for {
		lineEnd := lineStart
		for lineEnd < len(editor.graphemes) &&
			!editor.graphemes[lineEnd].class.isNewline() {
			lineEnd++
		}
		editor.layoutLogicalLine(lineStart, lineEnd, maximum, &overlap)
		if lineEnd == len(editor.graphemes) {
			return overlap
		}
		lineStart = lineEnd + 1
		editor.appendRowIndex(lineStart, lineStart, maximum, &overlap)
	}
}

func (editor *commentEditor) layoutLogicalLine(
	start, end, maximumRows int,
	overlap *int64,
) {
	rowWidth := 0
	position := start
	for position < end {
		if editor.graphemes[position].class.isWhitespace() {
			if rowWidth+1 > editor.Width() {
				editor.appendRowIndex(position, start, maximumRows, overlap)
				rowWidth = 0
			}
			rowWidth++
			editor.graphemes[position].cellEnd = commentEditorUint32(rowWidth)
			position++
			continue
		}

		wordStart := position
		wordWidth := 0
		for position < end &&
			!editor.graphemes[position].class.isWhitespace() {
			wordWidth += int(editor.graphemes[position].width)
			position++
		}
		if rowWidth > 0 && rowWidth+wordWidth > editor.Width() {
			editor.appendRowIndex(wordStart, start, maximumRows, overlap)
			rowWidth = 0
		}
		for index := wordStart; index < position; index++ {
			width := int(editor.graphemes[index].width)
			if rowWidth > 0 && rowWidth+width > editor.Width() {
				editor.appendRowIndex(index, start, maximumRows, overlap)
				rowWidth = 0
			}
			rowWidth += width
			editor.graphemes[index].cellEnd = commentEditorUint32(rowWidth)
		}
	}
	if rowWidth == editor.Width() {
		editor.appendRowIndex(end, start, maximumRows, overlap)
	}
}

func (editor *commentEditor) appendRowIndex(
	visualStart, logicalLineStart, maximum int,
	overlap *int64,
) {
	oldCapacity := cap(editor.rows)
	editor.rows = appendCommentEditorRowIndex(editor.rows, commentEditorRowIndex{
		visualStart:      commentEditorUint32(visualStart),
		logicalLineStart: commentEditorUint32(logicalLineStart),
	}, maximum)
	if cap(editor.rows) != oldCapacity {
		*overlap = max(*overlap, int64(oldCapacity)*commentEditorRowIndexBytes)
	}
}

func (editor *commentEditor) rowGraphemeRange(row int) (int, int) {
	row = min(max(row, 0), len(editor.rows)-1)
	start := int(editor.rows[row].visualStart)
	end := len(editor.graphemes)
	if row+1 < len(editor.rows) {
		end = int(editor.rows[row+1].visualStart)
		if end > start && editor.graphemes[end-1].class.isNewline() {
			end--
		}
	}
	return start, end
}
func (editor *commentEditor) rowForGrapheme(grapheme int) int {
	grapheme = min(max(grapheme, 0), len(editor.graphemes))
	return max(sort.Search(len(editor.rows), func(index int) bool {
		return int(editor.rows[index].visualStart) > grapheme
	})-1, 0)
}

func (editor *commentEditor) refreshCursorAndViewport() {
	editor.refreshCursorPosition()
	editor.reframe()
	editor.prepareVisibleRows()
}

func (editor *commentEditor) refreshCursorPosition() {
	editor.currentRow = editor.rowForGrapheme(editor.cursorGrapheme)
	start, end := editor.rowGraphemeRange(editor.currentRow)
	editor.cursorGrapheme = min(max(editor.cursorGrapheme, start), end)
	editor.cursor = editor.runeBoundary(editor.cursorGrapheme)
	editor.cursorCell = 0
	if editor.cursorGrapheme > start {
		editor.cursorCell = int(editor.graphemes[editor.cursorGrapheme-1].cellEnd)
	}
}

func (editor *commentEditor) reframe() {
	height := editor.Height()
	lastViewport := max(len(editor.rows)-height, 0)
	switch {
	case editor.currentRow < editor.viewportRow:
		editor.viewportRow = editor.currentRow
	case editor.currentRow >= editor.viewportRow+height:
		editor.viewportRow = editor.currentRow - height + 1
	}
	editor.viewportRow = min(max(editor.viewportRow, 0), lastViewport)
}

func (editor *commentEditor) moveVertical(delta int) {
	if delta == 0 {
		return
	}
	if editor.preferredCell < 0 {
		editor.preferredCell = editor.cursorCell
	}
	target := min(max(editor.currentRow+delta, 0), len(editor.rows)-1)
	start, end := editor.rowGraphemeRange(target)
	offset := sort.Search(end-start, func(offset int) bool {
		return int(editor.graphemes[start+offset].cellEnd) > editor.preferredCell
	})
	editor.cursorGrapheme = start + offset
	editor.cursor = editor.runeBoundary(editor.cursorGrapheme)
	editor.currentRow = target
	editor.refreshCursorAndViewport()
}

func (editor *commentEditor) pageUp() {
	if editor.currentRow != editor.viewportRow {
		editor.moveVertical(editor.viewportRow - editor.currentRow)
		return
	}
	editor.moveVertical(-editor.Height())
}

func (editor *commentEditor) pageDown() {
	last := min(editor.viewportRow+editor.Height()-1, len(editor.rows)-1)
	if editor.currentRow != last {
		editor.moveVertical(last - editor.currentRow)
		return
	}
	editor.moveVertical(editor.Height())
}

func (editor *commentEditor) prepareVisibleRows() {
	clear(editor.visibleRows[:])
	editor.preparedCursorX = 0
	editor.visibleCount = min(editor.Height(), len(editor.rows)-editor.viewportRow)
	for offset := range editor.visibleCount {
		rowIndex := editor.viewportRow + offset
		editor.visibleRows[offset] = editor.prepareRow(rowIndex)
	}
	if editor.currentRow < editor.viewportRow ||
		editor.currentRow >= editor.viewportRow+editor.visibleCount {
		return
	}
	row := editor.visibleRows[editor.currentRow-editor.viewportRow]
	editor.preparedCursorX = max(editor.cursorCell-int(row.cellStart), 0)
	if row.prefix {
		editor.preparedCursorX++
	}
}

func (editor *commentEditor) prepareRow(rowIndex int) commentEditorPreparedRow {
	start, end := editor.rowGraphemeRange(rowIndex)
	windowStart, windowEnd, sourceBytes := editor.selectRenderWindow(start, end, rowIndex)
	runeStart := editor.runeBoundary(windowStart)
	runeEnd := editor.runeBoundary(windowEnd)
	row := commentEditorPreparedRow{
		runes:       editor.value[runeStart:runeEnd:runeEnd],
		graphemes:   editor.graphemes[windowStart:windowEnd:windowEnd],
		runeBase:    commentEditorUint32(runeStart),
		sourceBytes: commentEditorUint32(sourceBytes),
		cellStart:   0,
		prefix:      windowStart > start,
		suffix:      windowEnd < end,
		placeholder: len(editor.value) == 0,
	}
	if windowStart > start {
		row.cellStart = editor.graphemes[windowStart-1].cellEnd
	}
	return row
}

func (editor *commentEditor) selectRenderWindow(start, end, rowIndex int) (int, int, int) {
	total := 0
	for index := start; index < end; index++ {
		total += int(editor.graphemes[index].sourceBytes)
		if total > commentEditorRowSourceBytes {
			break
		}
	}
	if total <= commentEditorRowSourceBytes && editor.renderedWindowBytes(
		start,
		end,
		start,
		end,
		total,
	) <= commentEditorRowRenderBytes {
		return start, end, total
	}
	anchor := start
	if rowIndex == editor.currentRow {
		anchor = min(max(editor.cursorGrapheme, start), end)
	}
	windowStart, windowEnd, used := anchor, anchor, 0
	for windowEnd < end && used < commentEditorRowSourceBytes/2 {
		size := int(editor.graphemes[windowEnd].sourceBytes)
		if used+size > commentEditorRowSourceBytes {
			break
		}
		used += size
		windowEnd++
	}
	for windowStart > start {
		size := int(editor.graphemes[windowStart-1].sourceBytes)
		if used+size > commentEditorRowSourceBytes {
			break
		}
		used += size
		windowStart--
	}
	for windowEnd < end {
		size := int(editor.graphemes[windowEnd].sourceBytes)
		if used+size > commentEditorRowSourceBytes {
			break
		}
		used += size
		windowEnd++
	}
	for windowStart < windowEnd && editor.renderedWindowBytes(
		start,
		end,
		windowStart,
		windowEnd,
		used,
	) > commentEditorRowRenderBytes {
		if windowEnd > anchor {
			windowEnd--
			used -= int(editor.graphemes[windowEnd].sourceBytes)
			continue
		}
		used -= int(editor.graphemes[windowStart].sourceBytes)
		windowStart++
	}
	return windowStart, windowEnd, used
}

func (editor *commentEditor) renderedWindowBytes(
	rowStart, rowEnd, windowStart, windowEnd, sourceBytes int,
) int {
	cells := 0
	for index := windowStart; index < windowEnd; index++ {
		cells += int(editor.graphemes[index].width)
	}
	markers := 0
	markerBytes := 0
	if windowStart > rowStart {
		markers++
		markerBytes += len("…")
	}
	if windowEnd < rowEnd {
		markers++
		markerBytes += len("…")
	}
	return sourceBytes + markerBytes + max(editor.Width()-cells-markers, 0)
}
