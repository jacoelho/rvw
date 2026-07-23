package app

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/review"
)

func TestCommentEditorDefaultActionsMutateCanonicalBuffer(t *testing.T) {
	tests := []struct {
		prepare func(*commentEditor)
		message tea.KeyPressMsg
		name    string
		initial string
		value   string
		cursor  int
	}{
		{
			name: "character backward", initial: "ab", value: "ab", cursor: 1,
			message: editorKey('b', tea.ModCtrl),
		},
		{
			name: "character forward", initial: "ab", value: "ab", cursor: 1,
			prepare: func(editor *commentEditor) { editor.SetCursorColumn(0) },
			message: editorKey('f', tea.ModCtrl),
		},
		{
			name: "word backward", initial: "one two", value: "one two", cursor: 4,
			message: editorKey('b', tea.ModAlt),
		},
		{
			name: "word forward", initial: "one two", value: "one two", cursor: 3,
			prepare: func(editor *commentEditor) { editor.SetCursorColumn(0) },
			message: editorKey('f', tea.ModAlt),
		},
		{
			name: "line start", initial: "one\ntwo", value: "one\ntwo", cursor: 4,
			message: editorKey('a', tea.ModCtrl),
		},
		{
			name: "line end", initial: "one\ntwo", value: "one\ntwo", cursor: 3,
			prepare: func(editor *commentEditor) { editor.MoveToBegin() },
			message: editorKey('e', tea.ModCtrl),
		},
		{
			name: "input begin", initial: "one\ntwo", value: "one\ntwo", cursor: 0,
			message: tea.KeyPressMsg(tea.Key{Code: tea.KeyHome, Mod: tea.ModCtrl}),
		},
		{
			name: "input end", initial: "one\ntwo", value: "one\ntwo", cursor: 7,
			prepare: func(editor *commentEditor) { editor.MoveToBegin() },
			message: tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd, Mod: tea.ModCtrl}),
		},
		{
			name: "delete backward", initial: "abc", value: "ac", cursor: 1,
			prepare: func(editor *commentEditor) { editor.SetCursorColumn(2) },
			message: editorKey('h', tea.ModCtrl),
		},
		{
			name: "delete forward", initial: "abc", value: "ac", cursor: 1,
			prepare: func(editor *commentEditor) { editor.SetCursorColumn(1) },
			message: editorKey('d', tea.ModCtrl),
		},
		{
			name: "delete word backward", initial: "one two", value: "one ", cursor: 4,
			message: editorKey('w', tea.ModCtrl),
		},
		{
			name: "delete word forward", initial: "one two", value: " two", cursor: 0,
			prepare: func(editor *commentEditor) { editor.MoveToBegin() },
			message: editorKey('d', tea.ModAlt),
		},
		{
			name: "delete before cursor", initial: "one two", value: "two", cursor: 0,
			prepare: func(editor *commentEditor) { editor.SetCursorColumn(4) },
			message: editorKey('u', tea.ModCtrl),
		},
		{
			name: "delete after cursor", initial: "one two", value: "one", cursor: 3,
			prepare: func(editor *commentEditor) { editor.SetCursorColumn(3) },
			message: editorKey('k', tea.ModCtrl),
		},
		{
			name: "insert newline", initial: "onetwo", value: "one\ntwo", cursor: 4,
			prepare: func(editor *commentEditor) { editor.SetCursorColumn(3) },
			message: editorKey('m', tea.ModCtrl),
		},
		{
			name: "lowercase", initial: "MIXED", value: "mixed", cursor: 5,
			prepare: func(editor *commentEditor) { editor.MoveToBegin() },
			message: editorKey('l', tea.ModAlt),
		},
		{
			name: "uppercase", initial: "mixed", value: "MIXED", cursor: 5,
			prepare: func(editor *commentEditor) { editor.MoveToBegin() },
			message: editorKey('u', tea.ModAlt),
		},
		{
			name: "capitalize", initial: "mixed", value: "Mixed", cursor: 5,
			prepare: func(editor *commentEditor) { editor.MoveToBegin() },
			message: editorKey('c', tea.ModAlt),
		},
		{
			name: "transpose", initial: "abc", value: "acb", cursor: 3,
			message: editorKey('t', tea.ModCtrl),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			editor := focusedCommentEditor(test.initial)
			if test.prepare != nil {
				test.prepare(editor)
			}
			_, _ = editor.Update(test.message)
			if got := editor.Value(); got != test.value || editor.cursor != test.cursor {
				t.Fatalf("editor = %q at %d, want %q at %d", got, editor.cursor, test.value, test.cursor)
			}
			assertCommentEditorAccounting(t, editor)
		})
	}
}

func TestCommentEditorAcceptsTextAndBracketedPasteAtomically(t *testing.T) {
	editor := focusedCommentEditor("ab")
	editor.SetCursorColumn(1)
	_, _ = editor.Update(tea.KeyPressMsg(tea.Key{Code: '界', Text: "界🙂"}))
	_, _ = editor.Update(tea.PasteMsg{Content: "\nend"})
	if got := editor.Value(); got != "a界🙂\nendb" {
		t.Fatalf("editor value = %q", got)
	}
	assertCommentEditorAccounting(t, editor)

	limited := newCommentEditor(4)
	limited.SetValue("keep")
	limited.MoveToBegin()
	status, _ := limited.Update(tea.PasteMsg{Content: "x"})
	if status != commentEditorLimited {
		t.Fatalf("rejected insertion status = %d, want limited", status)
	}
	if limited.Value() != "keep" || limited.cursor != 0 {
		t.Fatalf("rejected insertion changed editor to %q at %d", limited.Value(), limited.cursor)
	}
}

func TestCommentEditorTransformExpansionAtLimitIsAtomic(t *testing.T) {
	const body = "\u023f " // U+023F uppercases from two UTF-8 bytes to three.
	editor := newCommentEditor(int64(len(body)))
	editor.SetWidth(2)
	editor.SetHeight(2)
	editor.SetValue(body)
	editor.MoveToBegin()
	beforeValue := editor.Value()
	beforeGraphemes := append([]commentEditorGrapheme(nil), editor.graphemes...)
	beforeRows := append([]commentEditorRowIndex(nil), editor.rows...)
	beforeCursor := editor.cursor
	beforeCursorGrapheme := editor.cursorGrapheme
	beforeFingerprint := editor.fingerprint
	beforeByteCount := editor.byteCount

	status, command := editor.Update(editorKey('u', tea.ModAlt))
	if status != commentEditorLimited || command != nil {
		t.Fatalf("expanding transform = status %d command %v, want limited/nil", status, command)
	}
	if editor.Value() != beforeValue || editor.cursor != beforeCursor ||
		editor.cursorGrapheme != beforeCursorGrapheme ||
		editor.fingerprint != beforeFingerprint || editor.byteCount != beforeByteCount ||
		!reflect.DeepEqual(editor.graphemes, beforeGraphemes) ||
		!reflect.DeepEqual(editor.rows, beforeRows) {
		t.Fatal("rejected expanding transform changed canonical or projected editor state")
	}
}

func TestCommentEditorTransformExpansionReportsLimitWithoutChangingDraft(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "original\nadded\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitForPatch(t, model, coordinator, active)
	beginTestLineComment(t, model)
	body := "\u023f " + strings.Repeat("a", int(review.MaxCommentBodyBytes)-3)
	model.updateCommentEditor(tea.PasteMsg{Content: body})
	model.commentEditor.MoveToBegin()
	model.updateCommentEditor(editorKey('u', tea.ModAlt))

	draft, ok := model.review.Draft()
	if !ok || draft.Body() != body || model.commentEditor.Value() != body ||
		model.commentEditor.cursor != 0 {
		t.Fatalf(
			"limited transform changed editor/draft: editor=%d draft=%d cursor=%d active=%t",
			len(model.commentEditor.Value()),
			len(draft.Body()),
			model.commentEditor.cursor,
			ok,
		)
	}
	if model.notice == nil || !strings.Contains(string(model.notice.Text), "comment body limit reached") {
		t.Fatalf("limited transform notice = %+v", model.notice)
	}
}

func TestCommentEditorClipboardBindingReturnsTypedRequestWithoutReading(t *testing.T) {
	editor := focusedCommentEditor("kept")
	status, command := editor.Update(editorKey('v', tea.ModCtrl))
	if status != commentEditorUnchanged || editor.Value() != "kept" || command == nil {
		t.Fatalf("clipboard key changed editor or omitted request: %q, %v", editor.Value(), command)
	}
	if _, ok := command().(commentEditorClipboardRequest); !ok {
		t.Fatalf("clipboard command returned %T", command())
	}
}

func TestCommentEditorDefaultVerticalAndPageActionsUseVisualRows(t *testing.T) {
	editor := focusedCommentEditor("zero\none\ntwo\nthree\nfour")
	editor.SetWidth(8)
	editor.SetHeight(2)
	_, _ = editor.Update(editorKey('p', tea.ModCtrl))
	if editor.Line() != 3 {
		t.Fatalf("line previous moved to logical line %d", editor.Line())
	}
	_, _ = editor.Update(editorKey('n', tea.ModCtrl))
	if editor.Line() != 4 {
		t.Fatalf("line next moved to logical line %d", editor.Line())
	}
	_, _ = editor.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	if editor.Line() != 3 {
		t.Fatalf("first page up moved to logical line %d", editor.Line())
	}
	_, _ = editor.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	if editor.Line() != 1 {
		t.Fatalf("second page up moved to logical line %d", editor.Line())
	}
	_, _ = editor.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
	if editor.Line() != 2 {
		t.Fatalf("first page down moved to logical line %d", editor.Line())
	}
}

func TestCommentEditorDeletionMergesLogicalLines(t *testing.T) {
	tests := []struct {
		prepare func(*commentEditor)
		message tea.KeyPressMsg
		name    string
	}{
		{
			name: "backspace at line start",
			prepare: func(editor *commentEditor) {
				editor.MoveToEnd()
				editor.SetCursorColumn(0)
			},
			message: editorKey('h', tea.ModCtrl),
		},
		{
			name: "delete at line end",
			prepare: func(editor *commentEditor) {
				editor.MoveToBegin()
				editor.SetCursorColumn(3)
			},
			message: editorKey('d', tea.ModCtrl),
		},
		{
			name: "kill at line end",
			prepare: func(editor *commentEditor) {
				editor.MoveToBegin()
				editor.SetCursorColumn(3)
			},
			message: editorKey('k', tea.ModCtrl),
		},
		{
			name: "erase at line start",
			prepare: func(editor *commentEditor) {
				editor.MoveToEnd()
				editor.SetCursorColumn(0)
			},
			message: editorKey('u', tea.ModCtrl),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			editor := focusedCommentEditor("one\ntwo")
			test.prepare(editor)
			_, _ = editor.Update(test.message)
			if got := editor.Value(); got != "onetwo" || editor.cursor != 3 {
				t.Fatalf("merged editor = %q at %d", got, editor.cursor)
			}
		})
	}
}

func TestCommentEditorMovementAndDeletionRespectGraphemeBoundaries(t *testing.T) {
	editor := focusedCommentEditor("a\u0301👩\u200d💻b")
	editor.MoveToBegin()
	editor.characterForward()
	if editor.cursor != 2 {
		t.Fatalf("combining grapheme cursor = %d, want 2", editor.cursor)
	}
	editor.characterForward()
	if editor.cursor != 5 {
		t.Fatalf("ZWJ grapheme cursor = %d, want 5", editor.cursor)
	}
	editor.deleteCharacterBackward()
	if got := editor.Value(); got != "a\u0301b" || editor.cursor != 2 {
		t.Fatalf("grapheme deletion = %q at %d", got, editor.cursor)
	}
	editor.SetCursorColumn(1)
	if editor.cursor != 0 {
		t.Fatalf("cursor stopped inside combining grapheme at %d", editor.cursor)
	}
}

func TestCommentEditorUnicodePrependUsesOneAuthoritativeBoundary(t *testing.T) {
	editor := focusedCommentEditor("\u0600aB")
	if len(editor.graphemes) != 2 {
		t.Fatalf("graphemes = %d, want 2", len(editor.graphemes))
	}
	editor.MoveToBegin()
	editor.characterForward()
	if editor.cursor != 2 {
		t.Fatalf("forward cursor = %d, want 2", editor.cursor)
	}
	editor.characterBackward()
	if editor.cursor != 0 {
		t.Fatalf("backward cursor = %d, want 0", editor.cursor)
	}
	editor.characterForward()
	editor.deleteCharacterBackward()
	if got := editor.Value(); got != "B" || editor.cursor != 0 {
		t.Fatalf("prepend deletion = %q at %d", got, editor.cursor)
	}
}

func TestCommentEditorOversizedGraphemeNavigationAndRendering(t *testing.T) {
	cluster := "a" + strings.Repeat("\u0301", 2_100)
	editor := focusedCommentEditor(cluster + "b")
	if len(cluster) <= commentEditorRowSourceBytes || len(editor.graphemes) != 2 {
		t.Fatalf("fixture bytes/graphemes = %d/%d", len(cluster), len(editor.graphemes))
	}
	if got := int(editor.graphemes[0].sourceBytes); got != len(cluster) {
		t.Fatalf("oversized source bytes = %d, want %d", got, len(cluster))
	}

	editor.MoveToBegin()
	if view := editor.View(); len(strings.Split(view, "\n")[0]) > commentEditorRowRenderBytes {
		t.Fatalf("begin view row exceeded bound: %d", len(strings.Split(view, "\n")[0]))
	}
	editor.characterForward()
	if editor.cursor != len([]rune(cluster)) {
		t.Fatalf("forward cursor = %d", editor.cursor)
	}
	editor.characterBackward()
	if editor.cursor != 0 {
		t.Fatalf("backward cursor = %d", editor.cursor)
	}
	editor.deleteCharacterForward()
	if got := editor.Value(); got != "b" || editor.cursor != 0 {
		t.Fatalf("oversized deletion = %q at %d", got, editor.cursor)
	}
}

func TestCommentEditorPointerUpdateCannotDiscardMutation(t *testing.T) {
	editor := focusedCommentEditor("a")
	alias := editor
	_, _ = editor.Update(tea.KeyPressMsg(tea.Key{Code: 'b', Text: "b"}))
	if got := alias.Value(); got != "ab" {
		t.Fatalf("aliased editor value = %q, want %q", got, "ab")
	}
}

func focusedCommentEditor(value string) *commentEditor {
	editor := newCommentEditor(64 << 10)
	editor.SetWidth(20)
	editor.SetHeight(3)
	editor.SetValue(value)
	return editor
}

func editorKey(code rune, modifier tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: modifier})
}

func assertCommentEditorAccounting(t *testing.T, editor *commentEditor) {
	t.Helper()
	if editor.byteCount != int64(len(editor.Value())) {
		t.Fatalf("byte accounting = %d, want %d", editor.byteCount, len(editor.Value()))
	}
	if editor.constructionBytes < editor.retainedBytes() {
		t.Fatalf(
			"construction bytes = %d, retained bytes %d",
			editor.constructionBytes,
			editor.retainedBytes(),
		)
	}
}
