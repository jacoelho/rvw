package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/jacoelho/rvw/internal/keymap"
)

func TestRuntimeKeybindingMatrixRoutesAndRendersEveryDeclaredPair(t *testing.T) {
	seenActions := make(map[keymap.Action]struct{})
	for _, context := range keymap.Contexts() {
		focuses := []keymap.Focus{keymap.FocusNone}
		if context == keymap.ContextNormal {
			focuses = append(focuses, keymap.FocusFileTree, keymap.FocusDiff, keymap.FocusComments)
		}
		for _, action := range keymap.AllowedActions(context) {
			seenActions[action] = struct{}{}
			for _, focus := range focuses {
				name := fmt.Sprintf("%s/%s/%s", context, focus, action)
				t.Run(name, func(t *testing.T) {
					const chord = keymap.Chord("rvw-contract")
					model := modelForKeyContext(context)
					setModelKeybindings(t, model, keymap.Binding{
						Context: context,
						Focus:   focus,
						Chord:   chord,
						Action:  action,
					})
					if focus != keymap.FocusNone {
						model.focus = focusOwner(focus)
					}
					if got := model.keyContext(); got != context {
						t.Fatalf("model context = %q, want %q", got, context)
					}
					resolution := model.resolveAction([]string{chord.String()})
					if !resolution.exact() || resolution.prefix() || resolution.action != action {
						t.Fatalf(
							"resolved action = %q kind=%v, want %q/exact",
							resolution.action,
							resolution.kind,
							action,
						)
					}
					visible := model.bindingSummary(context)
					if !strings.Contains(visible, chord.Display()) ||
						!strings.Contains(visible, action.Description()) {
						t.Fatalf("visible binding summary = %q", visible)
					}
				})
			}
		}
	}
	for _, action := range keymap.Actions() {
		if _, exists := seenActions[action]; !exists {
			t.Errorf("valid action %q is unreachable from every runtime context", action)
		}
	}
}

func TestRuntimeKeybindingVocabularyRejectsUnknownValues(t *testing.T) {
	if _, err := keymap.NewContext("unknown"); err == nil {
		t.Fatal("unknown context was accepted")
	}
	if _, err := keymap.NewFocus("unknown"); err == nil {
		t.Fatal("unknown focus was accepted")
	}
	if _, err := keymap.NewAction("unknown"); err == nil {
		t.Fatal("unknown action was accepted")
	}
	if _, err := keymap.NewChord(" \t "); err == nil {
		t.Fatal("empty chord was accepted")
	}
}

func TestCommentEditorTabFallsThroughUnlessExplicitlyBound(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	_ = model.beginSummaryComment()

	for _, message := range []tea.KeyPressMsg{
		tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}),
	} {
		_, _ = model.Update(message)
		if model.mode != ModeCommentEdit || model.commentEditor.Value() != "" {
			t.Fatalf("unbound tab changed editor: mode=%d value=%q", model.mode, model.commentEditor.Value())
		}
	}

	setModelKeybindings(t, model, keymap.Binding{
		Context: keymap.ContextCommentEdit,
		Chord:   "tab",
		Action:  keymap.ActionInsertNewline,
	})
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "a", Code: 'a'}))
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if got := model.commentEditor.Value(); got != "a\n" {
		t.Fatalf("configured Tab override produced %q, want one newline", got)
	}
}

func TestRepeatedInterruptDoesNotCrossIntoConfirmationContext(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})
	setModelKeybindings(t, model, keymap.Binding{
		Context: keymap.ContextConfirm,
		Chord:   "ctrl-c",
		Action:  keymap.ActionInterrupt,
	})
	model.confirmation = confirmation{kind: confirmationQuit}
	model.overlay = OverlayConfirm
	before := model.View()

	repeatedInterrupt := tea.KeyPressMsg(tea.Key{
		Code:     'c',
		Mod:      tea.ModCtrl,
		IsRepeat: true,
	})
	_, _ = model.Update(repeatedInterrupt)

	if model.overlay != OverlayConfirm || model.confirmation.kind != confirmationQuit {
		t.Fatalf("repeated interrupt dismissed dirty-quit confirmation: overlay=%v confirmation=%v", model.overlay, model.confirmation.kind)
	}
	after := model.View()
	if after.Content != before.Content || after.Cursor != before.Cursor {
		t.Fatal("repeated interrupt changed the dirty-quit confirmation view")
	}
}

func TestRepeatedNavigationRemainsAvailable(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{
		"alpha.txt": "changed\n",
		"beta.txt":  "changed\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 18})
	model.focus = FocusFileTree

	_, _ = model.Update(tea.KeyPressMsg(tea.Key{
		Text:     "j",
		Code:     'j',
		IsRepeat: true,
	}))

	selected, ok := model.review.SelectedFile()
	if !ok || selected != model.fileOrder[1] {
		t.Fatalf("repeated navigation selected %d, want %d", selected, model.fileOrder[1])
	}
}

func TestModalContextsExecuteSequentialMultiStrokeBindings(t *testing.T) {
	testCases := []struct {
		inputValue func(*Model) string
		name       string
		context    keymap.Context
		mode       InteractionMode
		overlay    Overlay
	}{
		{
			name:    "comment edit",
			context: keymap.ContextCommentEdit,
			mode:    ModeCommentEdit,
			inputValue: func(model *Model) string {
				return model.commentEditor.Value()
			},
		},
		{
			name:    searchLabel,
			context: keymap.ContextSearch,
			mode:    ModeNormal,
			overlay: OverlayFind,
			inputValue: func(model *Model) string {
				return model.searchInput.Value()
			},
		},
		{
			name:    "command",
			context: keymap.ContextCommand,
			mode:    ModeCommand,
			inputValue: func(model *Model) string {
				return model.commandInput.Value()
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			settleModelWork(t, model, coordinator)
			model.commandContext = context.Background()
			model.mode = testCase.mode
			model.overlay = testCase.overlay
			setModelKeybindings(t, model, keymap.Binding{
				Context: testCase.context,
				Chord:   "g x",
				Action:  keymap.ActionCancel,
			})

			_, _ = model.Update(keyMessage("g"))
			if len(model.keys.PendingStrokes()) != 1 || model.keys.PendingStrokes()[0] != "g" {
				t.Fatalf("pending chord = %v", model.keys.PendingStrokes())
			}
			if value := testCase.inputValue(model); value != "" {
				t.Fatalf("chord prefix entered modal text: %q", value)
			}

			_, _ = model.Update(keyMessage("x"))
			if model.mode != ModeNormal || model.overlay != OverlayNone || len(model.keys.PendingStrokes()) != 0 {
				t.Fatalf(
					"completed chord left mode=%v overlay=%v chord=%v",
					model.mode,
					model.overlay,
					model.keys.PendingStrokes(),
				)
			}
			if value := testCase.inputValue(model); value != "" {
				t.Fatalf("completed chord entered modal text: %q", value)
			}
		})
	}
}

func TestCompletedOrAbandonedChordTimeoutDoesNotClearLaterRepeat(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		second string
	}{
		{name: "completed", second: "g"},
		{name: "abandoned", second: "x"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			settleModelWork(t, model, coordinator)
			setModelKeybindings(t, model, keymap.Binding{
				Context: keymap.ContextNormal,
				Chord:   "g g",
				Action:  keymap.ActionToggleWrap,
			})

			_, _ = model.Update(keyMessage("g"))
			staleGeneration := model.keys.Generation()
			_, _ = model.Update(keyMessage(testCase.second))
			_, _ = model.Update(keyMessage("5"))
			if model.keys.Repeat() != 5 {
				t.Fatalf("numeric repeat = %d before stale timeout", model.keys.Repeat())
			}
			_, _ = model.Update(chordTimeoutMsg{generation: staleGeneration})
			if model.keys.Repeat() != 5 {
				t.Fatalf("stale chord timeout cleared repeat: %d", model.keys.Repeat())
			}
		})
	}
}

func TestReloadPreparationRoutesMultiStrokeQuit(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	setModelKeybindings(t, model, keymap.Binding{
		Context: keymap.ContextNormal,
		Chord:   "g q",
		Action:  keymap.ActionQuit,
	})
	model.foreground.reload = reloadFlow{
		phase:     reloadPreparing,
		operation: 10_000,
	}

	_, _ = model.Update(keyMessage("g"))
	if len(model.keys.PendingStrokes()) != 1 {
		t.Fatalf("reload-preparation chord = %v", model.keys.PendingStrokes())
	}
	_, _ = model.Update(keyMessage("q"))
	if !model.quitting {
		t.Fatal("multi-stroke quit did not execute during reload preparation")
	}
}

func TestReloadPreparationActionConsumesPriorRepeat(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	model.focus = FocusFileTree
	model.commentPaneVisibility = paneVisibilityShown
	setModelKeybindings(t, model,
		keymap.Binding{Context: keymap.ContextNormal, Focus: keymap.FocusFileTree, Chord: "g c", Action: keymap.ActionInterrupt},
		keymap.Binding{Context: keymap.ContextNormal, Focus: keymap.FocusFileTree, Chord: "j", Action: keymap.ActionFocusNextPane},
	)
	model.foreground.reload = reloadFlow{phase: reloadAcquiring, operation: 10_000}

	_, _ = model.Update(keyMessage("5"))
	if model.keys.Repeat() != 5 {
		t.Fatalf("acquiring repeat = %d, want 5", model.keys.Repeat())
	}
	model.foreground.reload.phase = reloadPreparing
	_, _ = model.Update(keyMessage("g"))
	_, _ = model.Update(keyMessage("c"))
	if model.keys.Repeat() != 0 {
		t.Fatalf("reload-preparation cancel retained repeat %d", model.keys.Repeat())
	}

	model.foreground.reload = reloadFlow{}
	_, _ = model.Update(keyMessage("j"))
	if model.focus != FocusDiff {
		t.Fatalf("post-cancel focus = %v, want one-step move to diff", model.focus)
	}
}

func TestDisallowedReloadPreparationActionConsumesPriorRepeat(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	model.focus = FocusFileTree
	model.commentPaneVisibility = paneVisibilityShown
	setModelKeybindings(t, model,
		keymap.Binding{Context: keymap.ContextNormal, Focus: keymap.FocusFileTree, Chord: "g x", Action: keymap.ActionToggleWrap},
		keymap.Binding{Context: keymap.ContextNormal, Focus: keymap.FocusFileTree, Chord: "j", Action: keymap.ActionFocusNextPane},
	)
	model.foreground.reload = reloadFlow{phase: reloadAcquiring, operation: 10_000}
	initialWrap := model.settings.wrap

	_, _ = model.Update(keyMessage("5"))
	model.foreground.reload.phase = reloadPreparing
	_, _ = model.Update(keyMessage("g"))
	_, _ = model.Update(keyMessage("x"))
	if model.settings.wrap != initialWrap {
		t.Fatal("disallowed reload-preparation action executed")
	}
	if model.keys.Repeat() != 0 {
		t.Fatalf("disallowed reload-preparation action retained repeat %d", model.keys.Repeat())
	}

	model.foreground.reload = reloadFlow{}
	_, _ = model.Update(keyMessage("j"))
	if model.focus != FocusDiff {
		t.Fatalf("post-reload focus = %v, want one-step move to diff", model.focus)
	}
}

func TestPendingChordCannotCrossFocusOwner(t *testing.T) {
	for _, testCase := range []struct {
		changeFocusAwayAndBack func(*Model)
		name                   string
	}{
		{
			name: "mouse",
			changeFocusAwayAndBack: func(model *Model) {
				_, _ = model.Update(mouseActionMsg{
					reviewState: model.review.StateRevision(),
					focus:       FocusDiff,
				})
				_, _ = model.Update(mouseActionMsg{
					reviewState: model.review.StateRevision(),
					focus:       FocusFileTree,
				})
			},
		},
		{
			name: "resize clamp",
			changeFocusAwayAndBack: func(model *Model) {
				_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 18})
				_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			settleModelWork(t, model, coordinator)
			_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
			model.focus = FocusFileTree
			setModelKeybindings(t, model,
				keymap.Binding{
					Context: keymap.ContextNormal,
					Focus:   keymap.FocusFileTree,
					Chord:   "x t",
					Action:  keymap.ActionToggleWrap,
				},
			)
			initialWrap := model.settings.wrap

			_, _ = model.Update(keyMessage("5"))
			_, _ = model.Update(keyMessage("x"))
			testCase.changeFocusAwayAndBack(model)
			if model.focus != FocusFileTree {
				t.Fatalf("restored focus = %v, want file tree", model.focus)
			}
			if len(model.keys.PendingStrokes()) != 0 || model.keys.Repeat() != 0 {
				t.Fatalf("focus transition retained chord=%v repeat=%d", model.keys.PendingStrokes(), model.keys.Repeat())
			}
			_, _ = model.Update(keyMessage("t"))
			if model.settings.wrap != initialWrap {
				t.Fatal("chord prefix survived focus transition away and back")
			}

			_, _ = model.Update(keyMessage("x"))
			_, _ = model.Update(keyMessage("t"))
			if model.settings.wrap == initialWrap {
				t.Fatal("same-focus chord did not execute")
			}
		})
	}
}

func TestAbandonedChordClearsNumericRepeat(t *testing.T) {
	t.Run("mismatch to single stroke", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
		model.focus = FocusFileTree
		model.commentPaneVisibility = paneVisibilityShown
		setModelKeybindings(t, model,
			keymap.Binding{Context: keymap.ContextNormal, Focus: keymap.FocusFileTree, Chord: "g g", Action: keymap.ActionToggleWrap},
			keymap.Binding{Context: keymap.ContextNormal, Focus: keymap.FocusFileTree, Chord: "j", Action: keymap.ActionFocusNextPane},
		)

		_, _ = model.Update(keyMessage("5"))
		_, _ = model.Update(keyMessage("g"))
		_, _ = model.Update(keyMessage("j"))
		if model.focus != FocusDiff {
			t.Fatalf("focus = %v, want one-step move to diff", model.focus)
		}
	})

	t.Run("mismatch to new prefix", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		setModelKeybindings(t, model,
			keymap.Binding{Context: keymap.ContextNormal, Chord: "g g", Action: keymap.ActionToggleWrap},
			keymap.Binding{Context: keymap.ContextNormal, Chord: "j j", Action: keymap.ActionToggleWrap},
		)

		_, _ = model.Update(keyMessage("5"))
		_, _ = model.Update(keyMessage("g"))
		_, _ = model.Update(keyMessage("j"))
		if model.keys.Repeat() != 0 || len(model.keys.PendingStrokes()) != 1 || model.keys.PendingStrokes()[0] != "j" {
			t.Fatalf("retried prefix retained repeat=%d chord=%v", model.keys.Repeat(), model.keys.PendingStrokes())
		}
	})
}

func modelForKeyContext(context keymap.Context) *Model {
	model := &Model{
		size:  TerminalSize{Width: normalWidth, Height: 24, Known: true},
		focus: FocusDiff,
		mode:  ModeNormal,
	}
	switch context {
	case keymap.ContextNormal:
	case keymap.ContextVisual:
		model.mode = ModeVisual
	case keymap.ContextCommentEdit:
		model.mode = ModeCommentEdit
	case keymap.ContextSearch:
		model.overlay = OverlayFind
	case keymap.ContextCommand:
		model.mode = ModeCommand
	case keymap.ContextHelp:
		model.overlay = OverlayHelp
	case keymap.ContextStatusDetails:
		model.overlay = OverlayStatus
	case keymap.ContextConfirm:
		model.overlay = OverlayConfirm
	case keymap.ContextExportPicker:
		model.overlay = OverlayExport
	case keymap.ContextSubmit:
		model.overlay = OverlaySubmit
	case keymap.ContextMinimumSize:
		model.size.Width = minimumWidth - 1
	}
	return model
}

func focusOwner(focus keymap.Focus) FocusOwner {
	switch focus {
	case keymap.FocusFileTree:
		return FocusFileTree
	case keymap.FocusComments:
		return FocusComments
	default:
		return FocusDiff
	}
}
