package app

import (
	"slices"
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

const (
	commentEditorPlaceholder       = "Write a review comment"
	commentEditorRuneBytes         = int64(4)
	commentEditorGraphemeBytes     = int64(20)
	commentEditorRowIndexBytes     = int64(8)
	commentEditorPreparedRowBytes  = int64(64)
	commentEditorMaxIndexedBytes   = int64(1<<32 - 1)
	commentEditorFingerprintOffset = uint64(14695981039346656037)
	commentEditorFingerprintPrime  = uint64(1099511628211)
)

type commentEditorGraphemeClass uint8

const (
	commentEditorGraphemeText commentEditorGraphemeClass = iota
	commentEditorGraphemeWhitespace
	commentEditorGraphemeNewline
)

func (class commentEditorGraphemeClass) isWhitespace() bool {
	switch class {
	case commentEditorGraphemeText:
		return false
	case commentEditorGraphemeWhitespace, commentEditorGraphemeNewline:
		return true
	default:
		panic("unknown comment editor grapheme class")
	}
}

func (class commentEditorGraphemeClass) isNewline() bool {
	switch class {
	case commentEditorGraphemeText, commentEditorGraphemeWhitespace:
		return false
	case commentEditorGraphemeNewline:
		return true
	default:
		panic("unknown comment editor grapheme class")
	}
}

// commentEditorGrapheme describes one authoritative extended grapheme cluster.
// end is its exclusive rune boundary and cellEnd is its cumulative cell offset
// in the visual row that owns it.
type commentEditorGrapheme struct {
	end         uint32
	sourceBytes uint32
	width       uint32
	cellEnd     uint32
	class       commentEditorGraphemeClass
}

// commentEditorRowIndex identifies both the visual row boundary and the
// logical line that owns it. Logical line ends are derived from the next
// distinct logicalLineStart, so no redundant end index can become stale.
type commentEditorRowIndex struct {
	visualStart      uint32
	logicalLineStart uint32
}

type commentEditorRecovery struct {
	fingerprint    uint64
	byteCount      int64
	runeCount      int
	cursor         int
	preferredCell  int
	viewportRow    int
	viewportWidth  int
	viewportHeight int
}

// commentEditor owns one bounded comment buffer. The buffer is canonical;
// grapheme and row indices are synchronously rebuilt derived state.
type commentEditor struct {
	value             []rune
	graphemes         []commentEditorGrapheme
	rows              []commentEditorRowIndex
	visibleRows       [commentEditorMaxHeight]commentEditorPreparedRow
	byteLimit         int64
	byteCount         int64
	constructionBytes int64
	fingerprint       uint64
	cursor            int
	cursorGrapheme    int
	currentRow        int
	viewportRow       int
	cursorCell        int
	preferredCell     int
	visibleCount      int
	preparedCursorX   int
	width             int
	height            int
}

func newCommentEditor(byteLimit int64) *commentEditor {
	if byteLimit <= 0 || byteLimit > commentEditorMaxIndexedBytes {
		panic("comment editor byte limit is outside its indexed range")
	}
	editor := &commentEditor{
		byteLimit:     byteLimit,
		preferredCell: -1,
		width:         1,
		height:        1,
	}
	editor.rebuildProjection(0)
	return editor
}

func (editor *commentEditor) Value() string {
	return string(editor.value)
}

func (editor *commentEditor) Reset() {
	clear(editor.value)
	editor.value = editor.value[:0]
	editor.byteCount = 0
	editor.cursor = 0
	editor.preferredCell = -1
	editor.viewportRow = 0
	editor.rebuildProjection(0)
}

func (editor *commentEditor) SetValue(value string) {
	if int64(len(value)) > editor.byteLimit || !utf8.ValidString(value) {
		return
	}
	runes := []rune(value)
	constructionScratch := int64(cap(runes)) * commentEditorRuneBytes
	if len(runes) > cap(editor.value) {
		constructionScratch = int64(cap(editor.value)) * commentEditorRuneBytes
		editor.value = runes
	} else {
		clear(editor.value)
		editor.value = editor.value[:len(runes)]
		copy(editor.value, runes)
	}
	editor.byteCount = int64(len(value))
	editor.cursor = len(editor.value)
	editor.preferredCell = -1
	editor.rebuildProjection(constructionScratch)
}

func (editor *commentEditor) Line() int {
	line := 0
	for _, character := range editor.value[:editor.cursor] {
		if character == '\n' {
			line++
		}
	}
	return line
}

func (editor *commentEditor) Column() int {
	start, _ := editor.logicalLineGraphemeRange(editor.currentRow)
	return editor.cursor - editor.runeBoundary(start)
}

func (editor *commentEditor) SetCursorColumn(column int) {
	lineStart, lineEnd := editor.logicalLineGraphemeRange(editor.currentRow)
	start := editor.runeBoundary(lineStart)
	end := editor.runeBoundary(lineEnd)
	position := min(max(start+column, start), end)
	editor.cursorGrapheme = editor.graphemeIndexAtOrBefore(position)
	editor.cursor = editor.runeBoundary(editor.cursorGrapheme)
	editor.preferredCell = -1
	editor.refreshCursorAndViewport()
}

func (editor *commentEditor) CursorUp() {
	editor.moveVertical(-1)
}

func (editor *commentEditor) CursorDown() {
	editor.moveVertical(1)
}

func (editor *commentEditor) MoveToBegin() {
	editor.cursor = 0
	editor.cursorGrapheme = 0
	editor.preferredCell = -1
	editor.refreshCursorAndViewport()
}

func (editor *commentEditor) MoveToEnd() {
	editor.cursor = len(editor.value)
	editor.cursorGrapheme = len(editor.graphemes)
	editor.preferredCell = -1
	editor.refreshCursorAndViewport()
}

func (editor *commentEditor) SetWidth(width int) {
	width = min(max(width, 1), maxRenderWidth)
	if editor.width == width {
		return
	}
	editor.width = width
	editor.preferredCell = -1
	rowOverlap := editor.rebuildRows()
	editor.refreshCursorAndViewport()
	editor.recordConstruction(editor.retainedBytes() + rowOverlap)
}

func (editor *commentEditor) Width() int {
	return max(editor.width, 1)
}

func (editor *commentEditor) SetHeight(height int) {
	editor.height = min(max(height, 1), commentEditorMaxHeight)
	editor.reframe()
	editor.prepareVisibleRows()
}

func (editor *commentEditor) Height() int {
	return min(max(editor.height, 1), commentEditorMaxHeight)
}

func (editor *commentEditor) snapshotRecovery() commentEditorRecovery {
	return commentEditorRecovery{
		fingerprint:    editor.fingerprint,
		byteCount:      editor.byteCount,
		runeCount:      len(editor.value),
		cursor:         editor.cursor,
		preferredCell:  editor.preferredCell,
		viewportRow:    editor.viewportRow,
		viewportWidth:  editor.Width(),
		viewportHeight: editor.Height(),
	}
}

func (editor *commentEditor) restoreRecovery(snapshot commentEditorRecovery) bool {
	if snapshot.fingerprint != editor.fingerprint || snapshot.byteCount != editor.byteCount ||
		snapshot.runeCount != len(editor.value) {
		return false
	}
	editor.cursorGrapheme = editor.graphemeIndexAtOrBefore(
		min(max(snapshot.cursor, 0), len(editor.value)),
	)
	editor.cursor = editor.runeBoundary(editor.cursorGrapheme)
	editor.preferredCell = min(max(snapshot.preferredCell, -1), maxRenderWidth)
	if snapshot.viewportWidth == editor.Width() && snapshot.viewportHeight == editor.Height() {
		editor.viewportRow = min(max(snapshot.viewportRow, 0), max(len(editor.rows)-1, 0))
	} else {
		editor.viewportRow = 0
		editor.preferredCell = -1
	}
	editor.refreshCursorPosition()
	editor.reframe()
	editor.prepareVisibleRows()
	return true
}

func (editor *commentEditor) logicalLineGraphemeRange(row int) (int, int) {
	row = min(max(row, 0), len(editor.rows)-1)
	start := int(editor.rows[row].logicalLineStart)
	next := sort.Search(len(editor.rows), func(index int) bool {
		return int(editor.rows[index].logicalLineStart) > start
	})
	if next == len(editor.rows) {
		return start, len(editor.graphemes)
	}
	return start, int(editor.rows[next].logicalLineStart) - 1
}

func (editor *commentEditor) replace(
	start, end int,
	replacement []rune,
) commentEditorUpdateStatus {
	start = min(max(start, 0), len(editor.value))
	end = min(max(end, start), len(editor.value))
	constructionScratch := int64(cap(replacement)) * commentEditorRuneBytes
	editor.recordConstruction(editor.retainedBytes() + constructionScratch)
	removedBytes := runeSliceBytes(editor.value[start:end])
	replacementBytes := runeSliceBytes(replacement)
	nextBytes := editor.byteCount - removedBytes + replacementBytes
	if nextBytes > editor.byteLimit {
		return commentEditorLimited
	}
	if slices.Equal(editor.value[start:end], replacement) {
		editor.cursor = start + len(replacement)
		editor.afterHorizontalMove()
		return commentEditorUnchanged
	}

	oldLength := len(editor.value)
	newLength := oldLength - (end - start) + len(replacement)
	if newLength > cap(editor.value) {
		constructionScratch += int64(cap(editor.value)) * commentEditorRuneBytes
		next := make([]rune, newLength)
		copy(next, editor.value[:start])
		copy(next[start:], replacement)
		copy(next[start+len(replacement):], editor.value[end:])
		editor.value = next
	} else {
		previous := editor.value[:oldLength]
		editor.value = editor.value[:newLength]
		copy(editor.value[start+len(replacement):], previous[end:])
		copy(editor.value[start:start+len(replacement)], replacement)
	}
	editor.byteCount = nextBytes
	editor.cursor = start + len(replacement)
	editor.preferredCell = -1
	editor.rebuildProjection(constructionScratch)
	return commentEditorChanged
}

func (editor *commentEditor) rebuildProjection(constructionScratch int64) {
	body := string(editor.value)
	clear(editor.visibleRows[:])
	editor.visibleCount = 0
	editor.graphemes = editor.graphemes[:0]
	graphemeOverlap := int64(0)
	editor.fingerprint = commentEditorFingerprintOffset
	runeEnd := 0
	clusters := uniseg.NewGraphemes(body)
	for clusters.Next() {
		cluster := clusters.Str()
		for index := range len(cluster) {
			editor.fingerprint ^= uint64(cluster[index])
			editor.fingerprint *= commentEditorFingerprintPrime
		}
		class := commentEditorGraphemeText
		for _, character := range cluster {
			runeEnd++
			if character == '\n' {
				class = commentEditorGraphemeNewline
			} else if unicode.IsSpace(character) && class != commentEditorGraphemeNewline {
				class = commentEditorGraphemeWhitespace
			}
		}
		width := min(max(clusters.Width(), 0), maxRenderWidth)
		if class == commentEditorGraphemeWhitespace {
			width = 1
		}
		oldCapacity := cap(editor.graphemes)
		editor.graphemes = appendCommentEditorGrapheme(editor.graphemes, commentEditorGrapheme{
			end:         commentEditorUint32(runeEnd),
			sourceBytes: commentEditorUint32(len(cluster)),
			width:       commentEditorUint32(width),
			class:       class,
		}, len(editor.value))
		if cap(editor.graphemes) != oldCapacity {
			graphemeOverlap = max(
				graphemeOverlap,
				int64(oldCapacity)*commentEditorGraphemeBytes,
			)
		}
	}
	editor.cursorGrapheme = editor.graphemeIndexAtOrBefore(editor.cursor)
	editor.cursor = editor.runeBoundary(editor.cursorGrapheme)
	rowOverlap := editor.rebuildRows()
	editor.refreshCursorAndViewport()

	construction := editor.retainedBytes() + constructionScratch + int64(len(body)) +
		graphemeOverlap + rowOverlap
	editor.constructionBytes = max(construction, editor.retainedBytes())
}

func appendCommentEditorGrapheme(
	entries []commentEditorGrapheme,
	entry commentEditorGrapheme,
	maximum int,
) []commentEditorGrapheme {
	if len(entries) < cap(entries) {
		return append(entries, entry)
	}
	nextCapacity := min(max(len(entries)+1, max(cap(entries)*2, 8)), maximum)
	next := make([]commentEditorGrapheme, len(entries), nextCapacity)
	copy(next, entries)
	return append(next, entry)
}

func appendCommentEditorRowIndex(
	rows []commentEditorRowIndex,
	row commentEditorRowIndex,
	maximum int,
) []commentEditorRowIndex {
	if len(rows) < cap(rows) {
		return append(rows, row)
	}
	nextCapacity := min(max(len(rows)+1, max(cap(rows)*2, 8)), maximum)
	next := make([]commentEditorRowIndex, len(rows), nextCapacity)
	copy(next, rows)
	return append(next, row)
}

func (editor *commentEditor) graphemeIndexAtOrBefore(position int) int {
	position = min(max(position, 0), len(editor.value))
	return sort.Search(len(editor.graphemes), func(index int) bool {
		return int(editor.graphemes[index].end) > position
	})
}

func (editor *commentEditor) runeBoundary(grapheme int) int {
	grapheme = min(max(grapheme, 0), len(editor.graphemes))
	if grapheme == 0 {
		return 0
	}
	return int(editor.graphemes[grapheme-1].end)
}

func (editor *commentEditor) retainedBytes() int64 {
	return int64(cap(editor.value))*commentEditorRuneBytes +
		int64(cap(editor.graphemes))*commentEditorGraphemeBytes +
		int64(cap(editor.rows))*commentEditorRowIndexBytes +
		commentEditorMaxHeight*commentEditorPreparedRowBytes
}

func (editor *commentEditor) recordConstruction(bytes int64) {
	editor.constructionBytes = max(bytes, editor.retainedBytes())
}

func runeSliceBytes(value []rune) int64 {
	var size int64
	for _, character := range value {
		size += int64(utf8.RuneLen(character))
	}
	return size
}

func commentEditorUint32(value int) uint32 {
	if value < 0 || int64(value) > commentEditorMaxIndexedBytes {
		panic("comment editor projection exceeded its indexed range")
	}
	return uint32(value)
}
