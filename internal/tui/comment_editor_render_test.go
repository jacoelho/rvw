package tui

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/jacoelho/rvw/internal/change"
)

func TestCommentEditorWrapMatchesOrdinaryAndExactWidthBehavior(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		view   string
		width  int
		height int
	}{
		{
			name: "ordinary word wrap", value: "ab cd", width: 4, height: 3,
			view: "ab  \ncd  \n    ",
		},
		{
			name: "exact width trailing row", value: "abc", width: 3, height: 2,
			view: "abc\n   ",
		},
		{
			name: "overlong hard wrap", value: "abcdef", width: 3, height: 3,
			view: "abc\ndef\n   ",
		},
		{
			name: "grapheme hard wrap", value: "a\u0301b", width: 2, height: 2,
			view: "a\u0301b\n  ",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			editor := newCommentEditor(64 << 10)
			editor.SetWidth(test.width)
			editor.SetHeight(test.height)
			editor.SetValue(test.value)
			if got := editor.View(); got != test.view {
				t.Fatalf("view = %q, want %q; viewport=%d", got, test.view, editor.viewportRow)
			}
		})
	}
}

func TestCommentEditorViewUsesExactHeightAndTracksVisualCursor(t *testing.T) {
	for height := 1; height <= commentEditorMaxHeight; height++ {
		editor := newCommentEditor(64 << 10)
		editor.SetWidth(8)
		editor.SetHeight(height)
		editor.SetValue("one\ntwo\nthree")
		view := editor.View()
		if rows := strings.Count(view, "\n") + 1; rows != height {
			t.Fatalf("height %d rendered %d rows", height, rows)
		}
	}

	editor := newCommentEditor(64 << 10)
	editor.SetWidth(4)
	editor.SetHeight(2)
	editor.SetValue("aa\nbb\ncc")
	if view := editor.View(); view != "bb  \ncc  " {
		t.Fatalf("bottom viewport = %q", view)
	}
	if cursor := editor.Cursor(); cursor == nil || cursor.X != 2 || cursor.Y != 1 {
		t.Fatalf("bottom cursor = %+v", cursor)
	}
	editor.CursorUp()
	if cursor := editor.Cursor(); cursor == nil || cursor.X != 2 || cursor.Y != 0 {
		t.Fatalf("up cursor = %+v", cursor)
	}
	editor.CursorDown()
	if cursor := editor.Cursor(); cursor == nil || cursor.X != 2 || cursor.Y != 1 {
		t.Fatalf("down cursor = %+v", cursor)
	}
}

func TestCommentEditorViewBoundsRenderedRowsAndRetainedProjection(t *testing.T) {
	const cluster = "a\u0301\u0301\u0301"
	editor := newCommentEditor(64 << 10)
	editor.SetWidth(maxRenderWidth)
	editor.SetHeight(commentEditorMaxHeight)
	editor.SetValue(strings.Repeat(cluster, maxRenderWidth))

	view := editor.View()
	lines := strings.Split(view, "\n")
	if len(lines) != commentEditorMaxHeight {
		t.Fatalf("rendered rows = %d", len(lines))
	}
	for index, line := range lines {
		if len(line) > commentEditorRowRenderBytes {
			t.Fatalf("row %d retained %d bytes, limit %d", index, len(line), commentEditorRowRenderBytes)
		}
	}
	if len(view) > commentEditorMaxHeight*commentEditorRowRenderBytes+commentEditorMaxHeight-1 {
		t.Fatalf("view retained %d bytes", len(view))
	}
	if editor.retainedBytes() > editor.constructionBytes {
		t.Fatalf(
			"retained bytes = %d, construction bytes = %d",
			editor.retainedBytes(),
			editor.constructionBytes,
		)
	}
	if editor.visibleCount > commentEditorMaxHeight {
		t.Fatalf("visible descriptors = %d", editor.visibleCount)
	}
}

func TestCommentEditorViewAndCursorArePure(t *testing.T) {
	editor := focusedCommentEditor("one two three")
	editor.SetWidth(5)
	editor.SetHeight(3)
	before := snapshotCommentEditorDeep(editor)

	first := editor.View()
	firstCursor := editor.Cursor()
	second := editor.View()
	secondCursor := editor.Cursor()
	if first != second || firstCursor == nil || secondCursor == nil || *firstCursor != *secondCursor {
		t.Fatalf("pure render changed output: %q/%q, %+v/%+v", first, second, firstCursor, secondCursor)
	}
	if after := snapshotCommentEditorDeep(editor); !reflect.DeepEqual(after, before) {
		t.Fatal("render mutated editor state")
	}
}

func TestCommentEditorRenderFastPathBoundsPaddingAndOmitsTooWideGrapheme(t *testing.T) {
	editor := newCommentEditor(change.MaxCommentBodyBytes)
	editor.SetWidth(maxRenderWidth)
	editor.SetHeight(1)
	editor.SetValue(strings.Repeat("\u200b", 1_000))
	view := editor.View()
	if len(view) > commentEditorRowRenderBytes {
		t.Fatalf("zero-width row rendered %d bytes, limit %d", len(view), commentEditorRowRenderBytes)
	}

	editor.SetWidth(1)
	editor.SetValue("界")
	if got := editor.View(); got != "…" {
		t.Fatalf("too-wide grapheme view = %q, want omission marker", got)
	}
	var rendered strings.Builder
	renderCommentEditorRow(&rendered, commentEditorPreparedRow{
		runes:     []rune("界"),
		graphemes: []commentEditorGrapheme{{end: 1, width: 3, class: commentEditorGraphemeText}},
		suffix:    true,
	}, 2)
	if got := rendered.String(); got != "… " {
		t.Fatalf("too-wide truncated row = %q, want one omission marker", got)
	}
}

func TestCommentEditorMixedWidthAndNormalizedWhitespaceWrapping(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		view    string
		width   int
		cursorX int
		cursorY int
	}{
		{
			name: "mixed width one over", value: "a界b", width: 3,
			view: "a界\nb  ", cursorX: 1, cursorY: 1,
		},
		{
			name: "mixed width exact", value: "a界b", width: 4,
			view: "a界b\n    ", cursorX: 0, cursorY: 1,
		},
		{
			name: "mixed width one under", value: "a界b", width: 5,
			view: "a界b \n     ", cursorX: 4, cursorY: 0,
		},
		{
			name: "wide whitespace", value: "a\u3000b", width: 3,
			view: "a b\n   ", cursorX: 0, cursorY: 1,
		},
		{
			name: "whitespace combining cluster", value: "a\u3000\u0301b", width: 3,
			view: "a b\n   ", cursorX: 0, cursorY: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			editor := newCommentEditor(change.MaxCommentBodyBytes)
			editor.SetWidth(test.width)
			editor.SetHeight(2)
			editor.SetValue(test.value)
			if got := editor.View(); got != test.view {
				t.Fatalf("view = %q, want %q", got, test.view)
			}
			cursor := editor.Cursor()
			if cursor == nil || cursor.X != test.cursorX || cursor.Y != test.cursorY {
				t.Fatalf("cursor = %+v, want (%d,%d)", cursor, test.cursorX, test.cursorY)
			}
		})
	}

	editor := newCommentEditor(change.MaxCommentBodyBytes)
	editor.SetWidth(3)
	editor.SetHeight(2)
	editor.SetValue("a界b")
	editor.CursorUp()
	if cursor := editor.Cursor(); cursor == nil || cursor.X != 1 || cursor.Y != 0 {
		t.Fatalf("up cursor = %+v, want (1,0)", cursor)
	}
	editor.CursorDown()
	if cursor := editor.Cursor(); cursor == nil || cursor.X != 1 || cursor.Y != 1 {
		t.Fatalf("down cursor = %+v, want (1,1)", cursor)
	}
}

func TestCommentEditorDenseZeroWidthVerticalNavigationUsesIndexedCells(t *testing.T) {
	zeroWidth := "\u200b"
	count := (int(change.MaxCommentBodyBytes) - len("a\nb")) / len(zeroWidth)
	body := strings.Repeat(zeroWidth, count) + "a\nb"
	editor := newCommentEditor(change.MaxCommentBodyBytes)
	editor.SetWidth(4)
	editor.SetHeight(1)
	editor.SetValue(body)
	if len(body) != int(change.MaxCommentBodyBytes)-1 {
		t.Fatalf("fixture bytes = %d", len(body))
	}
	if editor.viewportRow != 1 {
		t.Fatalf("bottom viewport = %d, want 1", editor.viewportRow)
	}
	editor.CursorUp()
	if editor.currentRow != 0 || editor.cursorGrapheme != count+1 || editor.cursorCell != 1 ||
		editor.viewportRow != 0 {
		t.Fatalf(
			"up state = row %d grapheme %d cell %d viewport %d",
			editor.currentRow,
			editor.cursorGrapheme,
			editor.cursorCell,
			editor.viewportRow,
		)
	}
	editor.CursorDown()
	if editor.currentRow != 1 || editor.cursorCell != 1 || editor.viewportRow != 1 {
		t.Fatalf(
			"down state = row %d cell %d viewport %d",
			editor.currentRow,
			editor.cursorCell,
			editor.viewportRow,
		)
	}
}

func TestCommentEditorRecoveryRestoresOnlyMatchingBody(t *testing.T) {
	const body = "zero\none\ntwo\nthree"
	editor := newCommentEditor(change.MaxCommentBodyBytes)
	editor.SetWidth(4)
	editor.SetHeight(2)
	editor.SetValue(body)
	editor.CursorUp()
	wantCursor := editor.cursor
	wantPreferred := editor.preferredCell
	wantViewport := editor.viewportRow
	snapshot := editor.snapshotRecovery()

	editor.Reset()
	editor.SetValue(body)
	if !editor.restoreRecovery(snapshot) {
		t.Fatal("matching body recovery was rejected")
	}
	if editor.cursor != wantCursor || editor.preferredCell != wantPreferred ||
		editor.viewportRow != wantViewport {
		t.Fatalf(
			"restored cursor/preferred/viewport = %d/%d/%d, want %d/%d/%d",
			editor.cursor,
			editor.preferredCell,
			editor.viewportRow,
			wantCursor,
			wantPreferred,
			wantViewport,
		)
	}

	editor.SetValue(body + "!")
	before := editor.cursor
	if editor.restoreRecovery(snapshot) {
		t.Fatal("different body recovery succeeded")
	}
	if editor.cursor != before {
		t.Fatalf("rejected recovery moved cursor to %d, want %d", editor.cursor, before)
	}

	editor.SetValue(body)
	clamped := snapshot
	clamped.cursor = len(editor.value) + 100
	clamped.preferredCell = maxRenderWidth + 100
	clamped.viewportRow = len(editor.rows) + 100
	if !editor.restoreRecovery(clamped) || editor.cursor > len(editor.value) ||
		editor.preferredCell > maxRenderWidth || editor.viewportRow >= len(editor.rows) {
		t.Fatalf(
			"clamped recovery = cursor %d preferred %d viewport %d rows %d",
			editor.cursor,
			editor.preferredCell,
			editor.viewportRow,
			len(editor.rows),
		)
	}
}

func TestCommentEditorRecoveryResetsPreferredColumnAcrossViewportResize(t *testing.T) {
	editor := newCommentEditor(change.MaxCommentBodyBytes)
	editor.SetWidth(4)
	editor.SetHeight(3)
	editor.SetValue("aa\nbb")
	editor.CursorUp()
	if editor.preferredCell < 0 {
		t.Fatal("vertical movement did not establish a preferred cell")
	}
	snapshot := editor.snapshotRecovery()

	editor.SetWidth(3)
	if editor.preferredCell != -1 {
		t.Fatalf("resize preserved stale preferred cell %d", editor.preferredCell)
	}
	editor.CursorDown()
	if editor.preferredCell != editor.cursorCell {
		t.Fatalf(
			"post-resize navigation preferred/cursor cell = %d/%d",
			editor.preferredCell,
			editor.cursorCell,
		)
	}
	if !editor.restoreRecovery(snapshot) {
		t.Fatal("matching body recovery was rejected after resize")
	}
	if editor.preferredCell != -1 || editor.viewportRow != 0 {
		t.Fatalf(
			"resized recovery preferred/viewport = %d/%d, want -1/0",
			editor.preferredCell,
			editor.viewportRow,
		)
	}
}

func TestCommentEditorProjectionOwnersStayWithinAcceptedBodyFormula(t *testing.T) {
	const bodyBytes = 4_096
	editor := newCommentEditor(bodyBytes)
	editor.SetWidth(17)
	editor.SetHeight(commentEditorMaxHeight)
	editor.SetValue(strings.Repeat("a", bodyBytes))

	if cap(editor.value) != len(editor.value) {
		t.Fatalf("fresh canonical owner = length %d capacity %d", len(editor.value), cap(editor.value))
	}
	if cap(editor.graphemes) > len(editor.value) {
		t.Fatalf("grapheme capacity = %d, maximum %d", cap(editor.graphemes), len(editor.value))
	}
	if cap(editor.rows) > len(editor.graphemes)+1 {
		t.Fatalf(
			"row-index capacity = %d, maximum %d",
			cap(editor.rows),
			len(editor.graphemes)+1,
		)
	}
	for index := range editor.visibleCount {
		row := editor.visibleRows[index]
		if cap(row.runes) != len(row.runes) || cap(row.graphemes) != len(row.graphemes) ||
			row.sourceBytes > commentEditorRowSourceBytes {
			t.Fatalf(
				"visible row %d window = runes %d/%d graphemes %d/%d source %d",
				index,
				len(row.runes),
				cap(row.runes),
				len(row.graphemes),
				cap(row.graphemes),
				row.sourceBytes,
			)
		}
	}
	if got := int64(unsafe.Sizeof(commentEditorGrapheme{})); got != commentEditorGraphemeBytes {
		t.Fatalf("grapheme owner bytes = %d, accounted %d", got, commentEditorGraphemeBytes)
	}
	if got := int64(unsafe.Sizeof(commentEditorRowIndex{})); got != commentEditorRowIndexBytes {
		t.Fatalf("row-index owner bytes = %d, accounted %d", got, commentEditorRowIndexBytes)
	}
	if got := int64(unsafe.Sizeof(commentEditorPreparedRow{})); got != commentEditorPreparedRowBytes {
		t.Fatalf("prepared-row bytes = %d, accounted %d", got, commentEditorPreparedRowBytes)
	}
	wantRetained := int64(cap(editor.value))*int64(unsafe.Sizeof(rune(0))) +
		int64(cap(editor.graphemes))*int64(unsafe.Sizeof(commentEditorGrapheme{})) +
		int64(cap(editor.rows))*int64(unsafe.Sizeof(commentEditorRowIndex{})) +
		int64(len(editor.visibleRows))*int64(unsafe.Sizeof(commentEditorPreparedRow{}))
	if got := editor.retainedBytes(); got != wantRetained {
		t.Fatalf("retained bytes = %d, actual owners %d", got, wantRetained)
	}
}

type commentEditorDeepSnapshot struct {
	value             []rune
	graphemes         []commentEditorGrapheme
	rows              []commentEditorRowIndex
	visibleRows       [commentEditorMaxHeight]commentEditorPreparedRowSnapshot
	valueOwner        [3]uintptr
	graphemeOwner     [3]uintptr
	rowOwner          [3]uintptr
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

type commentEditorPreparedRowSnapshot struct {
	runes       []rune
	graphemes   []commentEditorGrapheme
	runeOwner   [3]uintptr
	graphOwner  [3]uintptr
	runeBase    uint32
	sourceBytes uint32
	cellStart   uint32
	prefix      bool
	suffix      bool
	placeholder bool
}

func snapshotCommentEditorDeep(editor *commentEditor) commentEditorDeepSnapshot {
	snapshot := commentEditorDeepSnapshot{
		value:             append([]rune(nil), editor.value...),
		graphemes:         append([]commentEditorGrapheme(nil), editor.graphemes...),
		rows:              append([]commentEditorRowIndex(nil), editor.rows...),
		valueOwner:        commentEditorRuneOwner(editor.value),
		graphemeOwner:     commentEditorGraphemeOwner(editor.graphemes),
		rowOwner:          commentEditorRowOwner(editor.rows),
		byteLimit:         editor.byteLimit,
		byteCount:         editor.byteCount,
		constructionBytes: editor.constructionBytes,
		fingerprint:       editor.fingerprint,
		cursor:            editor.cursor,
		cursorGrapheme:    editor.cursorGrapheme,
		currentRow:        editor.currentRow,
		viewportRow:       editor.viewportRow,
		cursorCell:        editor.cursorCell,
		preferredCell:     editor.preferredCell,
		visibleCount:      editor.visibleCount,
		preparedCursorX:   editor.preparedCursorX,
		width:             editor.width,
		height:            editor.height,
	}
	for index, row := range editor.visibleRows {
		snapshot.visibleRows[index] = commentEditorPreparedRowSnapshot{
			runes:       append([]rune(nil), row.runes...),
			graphemes:   append([]commentEditorGrapheme(nil), row.graphemes...),
			runeOwner:   commentEditorRuneOwner(row.runes),
			graphOwner:  commentEditorGraphemeOwner(row.graphemes),
			runeBase:    row.runeBase,
			sourceBytes: row.sourceBytes,
			cellStart:   row.cellStart,
			prefix:      row.prefix,
			suffix:      row.suffix,
			placeholder: row.placeholder,
		}
	}
	return snapshot
}

func commentEditorRuneOwner(value []rune) [3]uintptr {
	return [3]uintptr{uintptr(unsafe.Pointer(unsafe.SliceData(value))), uintptr(len(value)), uintptr(cap(value))}
}

func commentEditorGraphemeOwner(value []commentEditorGrapheme) [3]uintptr {
	return [3]uintptr{uintptr(unsafe.Pointer(unsafe.SliceData(value))), uintptr(len(value)), uintptr(cap(value))}
}

func commentEditorRowOwner(value []commentEditorRowIndex) [3]uintptr {
	return [3]uintptr{uintptr(unsafe.Pointer(unsafe.SliceData(value))), uintptr(len(value)), uintptr(cap(value))}
}
