package tui

import (
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

type commentEditorClipboardRequest struct{}

type commentEditorUpdateStatus uint8

const (
	commentEditorUnchanged commentEditorUpdateStatus = iota
	commentEditorChanged
	commentEditorLimited
)

var defaultCommentEditorKeys = textarea.DefaultKeyMap()

func (editor *commentEditor) Update(message tea.Msg) (commentEditorUpdateStatus, tea.Cmd) {
	switch message := message.(type) {
	case tea.PasteMsg:
		return editor.insertText(message.Content), nil
	case tea.KeyPressMsg:
		return editor.updateKey(message)
	default:
		return commentEditorUnchanged, nil
	}
}

func (editor *commentEditor) updateKey(
	message tea.KeyPressMsg,
) (commentEditorUpdateStatus, tea.Cmd) {
	keys := defaultCommentEditorKeys
	switch {
	case key.Matches(message, keys.DeleteAfterCursor):
		return editor.deleteAfterCursor(), nil
	case key.Matches(message, keys.DeleteBeforeCursor):
		return editor.deleteBeforeCursor(), nil
	case key.Matches(message, keys.DeleteCharacterBackward):
		return editor.deleteCharacterBackward(), nil
	case key.Matches(message, keys.DeleteCharacterForward):
		return editor.deleteCharacterForward(), nil
	case key.Matches(message, keys.DeleteWordBackward):
		return editor.deleteWordBackward(), nil
	case key.Matches(message, keys.DeleteWordForward):
		return editor.deleteWordForward(), nil
	case key.Matches(message, keys.InsertNewline):
		return editor.replace(editor.cursor, editor.cursor, []rune{'\n'}), nil
	case key.Matches(message, keys.LineEnd):
		_, end := editor.logicalLineGraphemeRange(editor.currentRow)
		editor.cursor = editor.runeBoundary(end)
		editor.afterHorizontalMove()
	case key.Matches(message, keys.LineStart):
		start, _ := editor.logicalLineGraphemeRange(editor.currentRow)
		editor.cursor = editor.runeBoundary(start)
		editor.afterHorizontalMove()
	case key.Matches(message, keys.CharacterForward):
		editor.characterForward()
	case key.Matches(message, keys.LineNext):
		editor.moveVertical(1)
	case key.Matches(message, keys.WordForward):
		editor.wordForward()
	case key.Matches(message, keys.Paste):
		return commentEditorUnchanged, func() tea.Msg { return commentEditorClipboardRequest{} }
	case key.Matches(message, keys.CharacterBackward):
		editor.characterBackward()
	case key.Matches(message, keys.LinePrevious):
		editor.moveVertical(-1)
	case key.Matches(message, keys.WordBackward):
		editor.wordBackward()
	case key.Matches(message, keys.InputBegin):
		editor.MoveToBegin()
	case key.Matches(message, keys.InputEnd):
		editor.MoveToEnd()
	case key.Matches(message, keys.PageUp):
		editor.pageUp()
	case key.Matches(message, keys.PageDown):
		editor.pageDown()
	case key.Matches(message, keys.LowercaseWordForward):
		return editor.transformWordForward(unicode.ToLower), nil
	case key.Matches(message, keys.UppercaseWordForward):
		return editor.transformWordForward(unicode.ToUpper), nil
	case key.Matches(message, keys.CapitalizeWordForward):
		return editor.capitalizeWordForward(), nil
	case key.Matches(message, keys.TransposeCharacterBackward):
		return editor.transposeCharacterBackward(), nil
	default:
		return editor.insertText(message.Text), nil
	}
	return commentEditorUnchanged, nil
}

func (editor *commentEditor) insertText(value string) commentEditorUpdateStatus {
	if value == "" || !utf8.ValidString(value) {
		return commentEditorUnchanged
	}
	if int64(len(value)) > editor.byteLimit-editor.byteCount {
		return commentEditorLimited
	}
	return editor.replace(editor.cursor, editor.cursor, []rune(value))
}

func (editor *commentEditor) characterBackward() {
	if editor.cursorGrapheme == 0 {
		return
	}
	editor.cursorGrapheme--
	editor.cursor = editor.runeBoundary(editor.cursorGrapheme)
	editor.afterHorizontalMove()
}

func (editor *commentEditor) characterForward() {
	if editor.cursorGrapheme == len(editor.graphemes) {
		return
	}
	editor.cursorGrapheme++
	editor.cursor = editor.runeBoundary(editor.cursorGrapheme)
	editor.afterHorizontalMove()
}

func (editor *commentEditor) wordBackward() {
	index := editor.cursorGrapheme
	if index == 0 {
		return
	}
	index--
	for index > 0 && editor.graphemes[index].class.isWhitespace() {
		index--
	}
	for index > 0 && !editor.graphemes[index-1].class.isWhitespace() {
		index--
	}
	editor.cursorGrapheme = index
	editor.cursor = editor.runeBoundary(index)
	editor.afterHorizontalMove()
}

func (editor *commentEditor) wordForward() {
	index := editor.cursorGrapheme
	for index < len(editor.graphemes) && editor.graphemes[index].class.isWhitespace() {
		index++
	}
	for index < len(editor.graphemes) && !editor.graphemes[index].class.isWhitespace() {
		index++
	}
	editor.cursorGrapheme = index
	editor.cursor = editor.runeBoundary(index)
	editor.afterHorizontalMove()
}

func (editor *commentEditor) deleteCharacterBackward() commentEditorUpdateStatus {
	if editor.cursorGrapheme == 0 {
		return commentEditorUnchanged
	}
	return editor.replace(
		editor.runeBoundary(editor.cursorGrapheme-1),
		editor.cursor,
		nil,
	)
}

func (editor *commentEditor) deleteCharacterForward() commentEditorUpdateStatus {
	if editor.cursorGrapheme == len(editor.graphemes) {
		return commentEditorUnchanged
	}
	return editor.replace(
		editor.cursor,
		editor.runeBoundary(editor.cursorGrapheme+1),
		nil,
	)
}

func (editor *commentEditor) deleteWordBackward() commentEditorUpdateStatus {
	lineStart, _ := editor.logicalLineGraphemeRange(editor.currentRow)
	if editor.cursorGrapheme == lineStart {
		if lineStart == 0 {
			return commentEditorUnchanged
		}
		return editor.replace(editor.runeBoundary(lineStart-1), editor.cursor, nil)
	}

	start := editor.cursorGrapheme - 1
	for start > lineStart && editor.graphemes[start].class.isWhitespace() {
		start--
	}
	for start > lineStart && !editor.graphemes[start-1].class.isWhitespace() {
		start--
	}
	return editor.replace(editor.runeBoundary(start), editor.cursor, nil)
}

func (editor *commentEditor) deleteWordForward() commentEditorUpdateStatus {
	_, lineEnd := editor.logicalLineGraphemeRange(editor.currentRow)
	if editor.cursorGrapheme == lineEnd {
		if lineEnd == len(editor.graphemes) {
			return commentEditorUnchanged
		}
		return editor.replace(editor.cursor, editor.runeBoundary(lineEnd+1), nil)
	}

	end := editor.cursorGrapheme
	for end < lineEnd && editor.graphemes[end].class.isWhitespace() {
		end++
	}
	for end < lineEnd && !editor.graphemes[end].class.isWhitespace() {
		end++
	}
	return editor.replace(editor.cursor, editor.runeBoundary(end), nil)
}

func (editor *commentEditor) deleteBeforeCursor() commentEditorUpdateStatus {
	lineStart, _ := editor.logicalLineGraphemeRange(editor.currentRow)
	if editor.cursorGrapheme == lineStart {
		if lineStart == 0 {
			return commentEditorUnchanged
		}
		return editor.replace(editor.runeBoundary(lineStart-1), editor.cursor, nil)
	}
	return editor.replace(editor.runeBoundary(lineStart), editor.cursor, nil)
}

func (editor *commentEditor) deleteAfterCursor() commentEditorUpdateStatus {
	_, lineEnd := editor.logicalLineGraphemeRange(editor.currentRow)
	if editor.cursorGrapheme == lineEnd {
		if lineEnd == len(editor.graphemes) {
			return commentEditorUnchanged
		}
		return editor.replace(editor.cursor, editor.runeBoundary(lineEnd+1), nil)
	}
	return editor.replace(editor.cursor, editor.runeBoundary(lineEnd), nil)
}

func (editor *commentEditor) transformWordForward(
	transform func(rune) rune,
) commentEditorUpdateStatus {
	start, end := editor.wordForwardRange()
	runeStart := editor.runeBoundary(start)
	runeEnd := editor.runeBoundary(end)
	replacement := append([]rune(nil), editor.value[runeStart:runeEnd]...)
	for index := range replacement {
		replacement[index] = transform(replacement[index])
	}
	return editor.replace(runeStart, runeEnd, replacement)
}

func (editor *commentEditor) capitalizeWordForward() commentEditorUpdateStatus {
	start, end := editor.wordForwardRange()
	runeStart := editor.runeBoundary(start)
	runeEnd := editor.runeBoundary(end)
	if runeStart == runeEnd {
		editor.cursor = runeEnd
		editor.afterHorizontalMove()
		return commentEditorUnchanged
	}
	replacement := append([]rune(nil), editor.value[runeStart:runeEnd]...)
	replacement[0] = unicode.ToTitle(replacement[0])
	return editor.replace(runeStart, runeEnd, replacement)
}

func (editor *commentEditor) wordForwardRange() (int, int) {
	start := editor.cursorGrapheme
	for start < len(editor.graphemes) && editor.graphemes[start].class.isWhitespace() {
		start++
	}
	end := start
	for end < len(editor.graphemes) && !editor.graphemes[end].class.isWhitespace() {
		end++
	}
	return start, end
}

func (editor *commentEditor) transposeCharacterBackward() commentEditorUpdateStatus {
	lineStart, lineEnd := editor.logicalLineGraphemeRange(editor.currentRow)
	if editor.cursorGrapheme == lineStart || lineEnd-lineStart < 2 {
		return commentEditorUnchanged
	}
	right := editor.cursorGrapheme
	rightEnd := right + 1
	if right == lineEnd {
		right--
		rightEnd = lineEnd
	}
	left := right - 1
	leftStart := editor.runeBoundary(left)
	middle := editor.runeBoundary(right)
	runeEnd := editor.runeBoundary(rightEnd)
	replacement := make([]rune, runeEnd-leftStart)
	rightLength := runeEnd - middle
	copy(replacement, editor.value[middle:runeEnd])
	copy(replacement[rightLength:], editor.value[leftStart:middle])
	return editor.replace(leftStart, runeEnd, replacement)
}

func (editor *commentEditor) afterHorizontalMove() {
	editor.cursorGrapheme = editor.graphemeIndexAtOrBefore(editor.cursor)
	editor.cursor = editor.runeBoundary(editor.cursorGrapheme)
	editor.preferredCell = -1
	editor.refreshCursorAndViewport()
}
