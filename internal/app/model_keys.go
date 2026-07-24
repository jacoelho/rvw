package app

import (
	"context"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
)

func (model *Model) handleKey(message tea.KeyPressMsg) tea.Cmd {
	stroke := keyStrokeFrom(message)
	if stroke.value == "" {
		return nil
	}
	if model.foreground.reloadIsPreparing() {
		if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
			if action == keymap.ActionNone {
				return command
			}
			repeat := model.takeRepeat(action)
			if action == keymap.ActionQuit || action == keymap.ActionInterrupt ||
				action == keymap.ActionCancel {
				return model.applyAction(action, repeat)
			}
		}
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: "finishing reload; only cancel or quit is available",
		}
		return nil
	}
	model.invalidateChordForOwnerChange()
	if model.acceptRepeatDigit(stroke.value) {
		return nil
	}
	if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
		if action == keymap.ActionNone {
			return command
		}
		return model.applyAction(action, model.takeRepeat(action))
	}
	model.repeat = 0
	return nil
}

// resolveKeyStroke owns chord accumulation for every key context. A pending
// prefix is consumed; when it fails to extend, the current stroke is retried as
// the start of a new chord so modal text inputs receive only unbound strokes.
func (model *Model) resolveKeyStroke(stroke keyStroke) (keymap.Action, bool, tea.Cmd) {
	if stroke.value == "" {
		return keymap.ActionNone, false, nil
	}
	model.invalidateChordForOwnerChange()
	resolution := model.resolveActionWithStroke(model.chord.strokes, stroke.value)
	if resolution.exact() {
		model.completeChord()
		return keyRepeatAction(stroke, resolution.action), true, nil
	}
	if resolution.prefix() {
		model.chord.strokes = append(model.chord.strokes, stroke.value)
		if len(model.chord.strokes) == 1 {
			model.chord.context = model.keyContext()
			model.chord.focus = model.configFocus()
		}
		model.chord.generation++
		return keymap.ActionNone, true,
			chordTimeoutCmd(model.commandContext, model.chord.generation)
	}
	model.abandonChord()
	resolution = model.resolveActionWithStroke(nil, stroke.value)
	if resolution.exact() {
		return keyRepeatAction(stroke, resolution.action), true, nil
	}
	if resolution.prefix() {
		model.chord.strokes = append(model.chord.strokes[:0], stroke.value)
		model.chord.context = model.keyContext()
		model.chord.focus = model.configFocus()
		model.chord.generation++
		return keymap.ActionNone, true,
			chordTimeoutCmd(model.commandContext, model.chord.generation)
	}
	return keymap.ActionNone, false, nil
}

func keyStrokeFrom(message tea.KeyPressMsg) keyStroke {
	return keyStroke{
		value:    normalizeStroke(message.String()),
		repeated: message.Key().IsRepeat,
	}
}

func keyRepeatAction(stroke keyStroke, action keymap.Action) keymap.Action {
	if stroke.repeated && !actionAcceptsKeyRepeat(action) {
		return keymap.ActionNone
	}
	return action
}

func (model *Model) invalidateChordForOwnerChange() {
	if len(model.chord.strokes) == 0 ||
		model.chord.context == model.keyContext() &&
			model.chord.focus == model.configFocus() {
		return
	}
	model.abandonChord()
}

func (model *Model) completeChord() {
	if len(model.chord.strokes) != 0 {
		model.chord.generation++
	}
	clear(model.chord.strokes)
	model.chord.strokes = model.chord.strokes[:0]
	model.chord.context = ""
	model.chord.focus = ""
}

func (model *Model) abandonChord() {
	model.completeChord()
	model.repeat = 0
}

func (model *Model) acceptRepeatDigit(stroke string) bool {
	if model.belowMinimum() || model.overlay != OverlayNone ||
		model.mode != ModeNormal && model.mode != ModeVisual ||
		len(model.chord.strokes) != 0 || len(stroke) != 1 ||
		stroke[0] < '0' || stroke[0] > '9' {
		return false
	}
	digit := uint16(stroke[0] - '0')
	if model.repeat == 0 && digit == 0 {
		return false
	}
	next := uint32(model.repeat)*10 + uint32(digit)
	if next > 9_999 {
		model.repeat = 0
		model.notice = &Notice{Kind: NoticeWarning, Text: "numeric repeat must not exceed 9999"}
		return true
	}
	model.repeat = uint16(next)
	return true
}

func (model *Model) takeRepeat(action keymap.Action) int {
	repeat := model.repeat
	model.repeat = 0
	if repeat == 0 || !repeatableAction(action) {
		return 1
	}
	return int(repeat)
}

func repeatableAction(action keymap.Action) bool {
	switch action {
	case keymap.ActionFocusNextPane, keymap.ActionFocusPreviousPane,
		keymap.ActionMoveUp, keymap.ActionMoveDown,
		keymap.ActionPageUp, keymap.ActionPageDown,
		keymap.ActionHalfPageUp, keymap.ActionHalfPageDown,
		keymap.ActionNextFile, keymap.ActionPreviousFile,
		keymap.ActionNextHunk, keymap.ActionPreviousHunk,
		keymap.ActionNextComment, keymap.ActionPreviousComment,
		keymap.ActionExtendUp, keymap.ActionExtendDown:
		return true
	default:
		return false
	}
}

func actionAcceptsKeyRepeat(action keymap.Action) bool {
	switch action {
	case keymap.ActionFocusNextPane, keymap.ActionFocusPreviousPane,
		keymap.ActionMoveUp, keymap.ActionMoveDown,
		keymap.ActionScrollLeft, keymap.ActionScrollRight,
		keymap.ActionPageUp, keymap.ActionPageDown,
		keymap.ActionHalfPageUp, keymap.ActionHalfPageDown,
		keymap.ActionNextFile, keymap.ActionPreviousFile,
		keymap.ActionNextHunk, keymap.ActionPreviousHunk,
		keymap.ActionNextComment, keymap.ActionPreviousComment,
		keymap.ActionNextSearchHit, keymap.ActionPreviousSearchHit,
		keymap.ActionExtendUp, keymap.ActionExtendDown,
		keymap.ActionDeleteBackward, keymap.ActionInsertNewline,
		keymap.ActionNextHelpPage, keymap.ActionPreviousHelpPage,
		keymap.ActionHelpLineUp, keymap.ActionHelpLineDown,
		keymap.ActionHelpPageUp, keymap.ActionHelpPageDown:
		return true
	default:
		return false
	}
}

func (model *Model) handleCommentEditorKey(message tea.KeyPressMsg) tea.Cmd {
	stroke := keyStrokeFrom(message)
	if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
		if action == keymap.ActionNone {
			return command
		}
		switch action {
		case keymap.ActionDeleteBackward:
			return model.updateCommentEditor(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
		case keymap.ActionInsertNewline:
			return model.updateCommentEditor(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		default:
			return model.applyAction(action, 1)
		}
	}
	return model.updateCommentEditor(message)
}

func (model *Model) updateCommentEditor(message tea.Msg) tea.Cmd {
	fragment := ""
	switch message := message.(type) {
	case tea.PasteMsg:
		fragment = message.Content
	case tea.KeyPressMsg:
		fragment = message.Text
	}
	if fragment != "" {
		currentBytes := model.commentEditor.byteCount
		fragmentBytes := int64(len(fragment))
		if currentBytes > review.MaxCommentBodyBytes ||
			fragmentBytes > review.MaxCommentBodyBytes-currentBytes {
			model.setCommentBodyLimitNotice()
			return nil
		}
		if err := review.ValidateCommentTextFragment(fragment); err != nil {
			model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
			return nil
		}
	}
	draft, ok := model.review.Draft()
	if !ok {
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText(review.ErrInvalidReviewTransition.Error()),
		}
		return nil
	}
	recovery := model.commentEditor.snapshotRecovery()
	status, command := model.commentEditor.Update(message)
	switch status {
	case commentEditorUnchanged:
		return command
	case commentEditorLimited:
		model.setCommentBodyLimitNotice()
		return command
	case commentEditorChanged:
	default:
		panic("unknown comment editor update status")
	}
	value := model.commentEditor.Value()
	next, err := model.review.UpdateDraft(value)
	if err != nil {
		model.commentEditor.SetValue(draft.Body())
		model.commentEditor.restoreRecovery(recovery)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return command
	}
	model.review = next
	return command
}

func (model *Model) setCommentBodyLimitNotice() {
	model.notice = &Notice{
		Kind: NoticeWarning,
		Text: NoticeText(
			"comment body limit reached: " + strconv.FormatInt(review.MaxCommentBodyBytes, 10) +
				" bytes",
		),
	}
}

func (model *Model) resolveAction(strokes []string) keyResolution {
	return model.settings.keymap.resolve(model.keyContext(), model.configFocus(), strokes)
}

func (model *Model) resolveActionWithStroke(
	strokes []string,
	stroke string,
) keyResolution {
	if len(strokes) >= maxKeyChordStrokes {
		return keyResolution{}
	}
	var candidate [maxKeyChordStrokes]string
	copy(candidate[:], strokes)
	candidate[len(strokes)] = stroke
	return model.resolveAction(candidate[:len(strokes)+1])
}

func (model *Model) keyContext() keymap.Context {
	if model.overlay == OverlayConfirm {
		return keymap.ContextConfirm
	}
	if model.belowMinimum() {
		return keymap.ContextMinimumSize
	}
	switch {
	case model.mode == ModeVisual:
		return keymap.ContextVisual
	case model.mode == ModeCommentEdit:
		return keymap.ContextCommentEdit
	case model.overlay == OverlayFind:
		return keymap.ContextSearch
	case model.mode == ModeCommand:
		return keymap.ContextCommand
	case model.overlay == OverlayHelp:
		return keymap.ContextHelp
	case model.overlay == OverlayStatus:
		return keymap.ContextStatusDetails
	case model.overlay == OverlayExport:
		return keymap.ContextExportPicker
	case model.overlay == OverlaySubmit:
		return keymap.ContextSubmit
	default:
		return keymap.ContextNormal
	}
}

func (model *Model) effectiveBindings(contextName keymap.Context) []keymap.Binding {
	return model.settings.keymap.effectiveBindings(contextName, model.configFocus())
}

func (model *Model) bindingSummary(contextName keymap.Context) string {
	return model.settings.keymap.bindingSummary(contextName, model.configFocus())
}

func (model *Model) normalStatusBindings() []keymap.Binding {
	return model.settings.keymap.normalStatusBindings(model.configFocus())
}

func (model *Model) commentEditorBindings() []keymap.Binding {
	return model.settings.keymap.commentEditorBindings()
}

func chordTimeoutCmd(ctx context.Context, generation uint64) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(chordTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			return chordTimeoutMsg{generation: generation}
		case <-ctx.Done():
			return nil
		}
	}
}

func normalizeStroke(value string) string {
	return keymap.NormalizeStroke(value)
}
