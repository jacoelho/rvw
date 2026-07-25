package app

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/review"
)

func (model *Model) handleKey(message tea.KeyPressMsg) tea.Cmd {
	stroke := keyStrokeFrom(message)
	if stroke.value == "" {
		return nil
	}
	if model.foreground.reloadIsPreparing() {
		if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
			if action == KeyActionNone {
				return command
			}
			repeat := model.takeRepeat(action)
			if action == KeyActionQuit || action == KeyActionInterrupt ||
				action == KeyActionCancel {
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
		if action == KeyActionNone {
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
func (model *Model) resolveKeyStroke(stroke keyStroke) (KeyAction, bool, tea.Cmd) {
	if stroke.value == "" {
		return KeyActionNone, false, nil
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
		return KeyActionNone, true,
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
		return KeyActionNone, true,
			chordTimeoutCmd(model.commandContext, model.chord.generation)
	}
	return KeyActionNone, false, nil
}

func keyStrokeFrom(message tea.KeyPressMsg) keyStroke {
	return keyStroke{
		value:    normalizeStroke(message.String()),
		repeated: message.Key().IsRepeat,
	}
}

func keyRepeatAction(stroke keyStroke, action KeyAction) KeyAction {
	if stroke.repeated && !actionAcceptsKeyRepeat(action) {
		return KeyActionNone
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

func (model *Model) takeRepeat(action KeyAction) int {
	repeat := model.repeat
	model.repeat = 0
	if repeat == 0 || !repeatableAction(action) {
		return 1
	}
	return int(repeat)
}

func repeatableAction(action KeyAction) bool {
	switch action {
	case KeyActionFocusNextPane, KeyActionFocusPreviousPane,
		KeyActionMoveUp, KeyActionMoveDown,
		KeyActionPageUp, KeyActionPageDown,
		KeyActionHalfPageUp, KeyActionHalfPageDown,
		KeyActionNextFile, KeyActionPreviousFile,
		KeyActionNextHunk, KeyActionPreviousHunk,
		KeyActionNextComment, KeyActionPreviousComment,
		KeyActionExtendUp, KeyActionExtendDown:
		return true
	default:
		return false
	}
}

func actionAcceptsKeyRepeat(action KeyAction) bool {
	switch action {
	case KeyActionFocusNextPane, KeyActionFocusPreviousPane,
		KeyActionMoveUp, KeyActionMoveDown,
		KeyActionScrollLeft, KeyActionScrollRight,
		KeyActionPageUp, KeyActionPageDown,
		KeyActionHalfPageUp, KeyActionHalfPageDown,
		KeyActionNextFile, KeyActionPreviousFile,
		KeyActionNextHunk, KeyActionPreviousHunk,
		KeyActionNextComment, KeyActionPreviousComment,
		KeyActionNextSearchHit, KeyActionPreviousSearchHit,
		KeyActionExtendUp, KeyActionExtendDown,
		KeyActionDeleteBackward, KeyActionInsertNewline,
		KeyActionNextHelpPage, KeyActionPreviousHelpPage,
		KeyActionHelpLineUp, KeyActionHelpLineDown,
		KeyActionHelpPageUp, KeyActionHelpPageDown:
		return true
	default:
		return false
	}
}

func (model *Model) handleCommentEditorKey(message tea.KeyPressMsg) tea.Cmd {
	stroke := keyStrokeFrom(message)
	if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
		if action == KeyActionNone {
			return command
		}
		switch action {
		case KeyActionDeleteBackward:
			return model.updateCommentEditor(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
		case KeyActionInsertNewline:
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

func (model *Model) keyContext() KeyContext {
	if model.belowMinimum() {
		return KeyContextMinimumSize
	}
	if model.overlay == OverlayConfirm {
		return KeyContextConfirm
	}
	switch {
	case model.mode == ModeVisual:
		return KeyContextVisual
	case model.mode == ModeCommentEdit:
		return KeyContextCommentEdit
	case model.overlay == OverlayFind:
		return KeyContextSearch
	case model.mode == ModeCommand:
		return KeyContextCommand
	case model.overlay == OverlayHelp:
		return KeyContextHelp
	case model.overlay == OverlayStatus:
		return KeyContextStatusDetails
	case model.overlay == OverlayExport:
		return KeyContextExportPicker
	case model.overlay == OverlaySubmit:
		return KeyContextSubmit
	default:
		return KeyContextNormal
	}
}

func (model *Model) effectiveBindings(contextName KeyContext) []Keybinding {
	return model.settings.keymap.effectiveBindings(contextName, model.configFocus())
}

func (model *Model) bindingSummary(contextName KeyContext) string {
	return model.settings.keymap.bindingSummary(contextName, model.configFocus())
}

func (model *Model) normalStatusBindingSummary() string {
	return model.settings.keymap.normalStatusSummary(model.configFocus())
}

func (model *Model) commentEditorBindingSummary() string {
	return model.settings.keymap.commentEditorSummary()
}

type actionHint struct {
	action KeyAction
	label  string
}

func (model *Model) actionHintSummary(contextName KeyContext, hints ...actionHint) string {
	var summary strings.Builder
	for _, hint := range hints {
		chord, ok := model.settings.keymap.primaryChord(
			contextName,
			model.configFocus(),
			hint.action,
		)
		if !ok {
			continue
		}
		if summary.Len() != 0 {
			summary.WriteString("  ")
		}
		summary.WriteString(chord.String())
		summary.WriteByte(' ')
		summary.WriteString(hint.label)
	}
	return summary.String()
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
	if value == " " {
		return "space"
	}
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "+", "-")
	if len([]rune(value)) == 1 {
		return value
	}
	switch strings.ToLower(value) {
	case keyReturn:
		return keyEnter
	case "escape":
		return keyEscape
	default:
		return strings.ToLower(value)
	}
}
