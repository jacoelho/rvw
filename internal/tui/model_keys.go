package tui

import (
	"context"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/keymap"
)

type keyResolutionKind uint8

const (
	keyResolutionNone keyResolutionKind = iota
	keyResolutionExact
	keyResolutionPrefix
	keyResolutionExactPrefix
)

type keyResolution struct {
	action keymap.Action
	kind   keyResolutionKind
}

func (resolution keyResolution) exact() bool {
	return resolution.kind == keyResolutionExact || resolution.kind == keyResolutionExactPrefix
}

func (resolution keyResolution) prefix() bool {
	return resolution.kind == keyResolutionPrefix || resolution.kind == keyResolutionExactPrefix
}

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
	model.keys.TakeRepeat(keymap.ActionNone)
	return nil
}

// resolveKeyStroke owns chord accumulation for every key context. A pending
// prefix is consumed; when it fails to extend, the current stroke is retried as
// the start of a new chord so modal text inputs receive only unbound strokes.
func (model *Model) resolveKeyStroke(stroke keyStroke) (keymap.Action, bool, tea.Cmd) {
	if stroke.value == "" {
		return keymap.ActionNone, false, nil
	}
	routed := model.keys.RouteStroke(model.keyScope(), stroke.value, stroke.repeated)
	if routed.TimeoutGeneration != 0 {
		return routed.Action, routed.Consumed,
			chordTimeoutCmd(model.commandContext, routed.TimeoutGeneration)
	}
	return routed.Action, routed.Consumed, nil
}

func keyStrokeFrom(message tea.KeyPressMsg) keyStroke {
	return keyStroke{
		value:    normalizeStroke(message.String()),
		repeated: message.Key().IsRepeat,
	}
}

func (model *Model) invalidateChordForOwnerChange() {
	model.keys.ResetScope(model.keyScope())
}

func (model *Model) abandonChord() {
	model.keys.Abandon()
}

func (model *Model) acceptRepeatDigit(stroke string) bool {
	allowed := !model.belowMinimum() && model.overlay == OverlayNone &&
		(model.mode == ModeNormal || model.mode == ModeVisual)
	accepted, overflow := model.keys.AcceptNumeric(stroke, allowed)
	if overflow {
		model.notice = &Notice{Kind: NoticeWarning, Text: "numeric repeat must not exceed 9999"}
	}
	return accepted
}

func (model *Model) takeRepeat(action keymap.Action) int {
	return model.keys.TakeRepeat(action)
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
		if currentBytes > change.MaxCommentBodyBytes ||
			fragmentBytes > change.MaxCommentBodyBytes-currentBytes {
			model.setCommentBodyLimitNotice()
			return nil
		}
		if err := change.ValidateCommentTextFragment(fragment); err != nil {
			model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
			return nil
		}
	}
	draft, ok := model.review.Draft()
	if !ok {
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText(change.ErrInvalidReviewTransition.Error()),
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
			"comment body limit reached: " + strconv.FormatInt(change.MaxCommentBodyBytes, 10) +
				" bytes",
		),
	}
}

func (model *Model) resolveAction(strokes []string) keyResolution {
	match := model.settings.keymap.Resolve(model.keyScope(), strokes)
	kind := keyResolutionNone
	switch {
	case match.Exact && match.Prefix:
		kind = keyResolutionExactPrefix
	case match.Exact:
		kind = keyResolutionExact
	case match.Prefix:
		kind = keyResolutionPrefix
	}
	return keyResolution{action: match.Action, kind: kind}
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

func (model *Model) keyScope() keymap.Scope {
	return keymap.Scope{Context: model.keyContext(), Focus: model.configFocus()}
}

func (model *Model) effectiveBindings(contextName keymap.Context) []keymap.Binding {
	return model.settings.keymap.Bindings(keymap.Scope{Context: contextName, Focus: model.configFocus()})
}

func (model *Model) bindingSummary(contextName keymap.Context) string {
	return model.settings.keymap.Summary(keymap.Scope{Context: contextName, Focus: model.configFocus()})
}

func (model *Model) normalStatusBindings() []keymap.Binding {
	return model.settings.keymap.NormalStatusBindings(model.configFocus())
}

func (model *Model) commentEditorBindings() []keymap.Binding {
	return model.settings.keymap.CommentEditorBindings()
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
