package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestRuntimeKeybindingMatrixRoutesAndRendersEveryDeclaredPair(t *testing.T) {
	seenActions := make(map[KeyAction]struct{})
	for context, actions := range contextKeyActions {
		focuses := []KeyFocus{KeyFocusNone}
		if context == KeyContextNormal {
			focuses = append(focuses, KeyFocusFileTree, KeyFocusDiff, KeyFocusComments)
		}
		for action := range actions {
			seenActions[action] = struct{}{}
			for _, focus := range focuses {
				name := fmt.Sprintf("%s/%s/%s", context, focus, action)
				t.Run(name, func(t *testing.T) {
					const chord = KeyChord("rvw-contract")
					model := modelForKeyContext(context)
					setModelKeybindings(t, model, Keybinding{
						Context: context,
						Focus:   focus,
						Chord:   chord,
						Action:  action,
					})
					if focus != KeyFocusNone {
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
					if !strings.Contains(visible, chord.String()) || !strings.Contains(visible, action.String()) {
						t.Fatalf("visible binding summary = %q", visible)
					}
				})
			}
		}
	}
	for action := range validKeyActions {
		if _, exists := seenActions[action]; !exists {
			t.Errorf("valid action %q is unreachable from every runtime context", action)
		}
	}
}

func TestRuntimeKeybindingVocabularyRejectsUnknownValues(t *testing.T) {
	if _, err := NewKeyContext("unknown"); err == nil {
		t.Fatal("unknown context was accepted")
	}
	if _, err := NewKeyFocus("unknown"); err == nil {
		t.Fatal("unknown focus was accepted")
	}
	if _, err := NewKeyAction("unknown"); err == nil {
		t.Fatal("unknown action was accepted")
	}
	if _, err := NewKeyChord(" \t "); err == nil {
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

	setModelKeybindings(t, model, Keybinding{
		Context: KeyContextCommentEdit,
		Chord:   "Tab",
		Action:  KeyActionInsertNewline,
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
	setModelKeybindings(t, model, Keybinding{
		Context: KeyContextConfirm,
		Chord:   "ctrl-c",
		Action:  KeyActionInterrupt,
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
		context    KeyContext
		mode       InteractionMode
		overlay    Overlay
	}{
		{
			name:    "comment edit",
			context: KeyContextCommentEdit,
			mode:    ModeCommentEdit,
			inputValue: func(model *Model) string {
				return model.commentEditor.Value()
			},
		},
		{
			name:    "search",
			context: KeyContextSearch,
			mode:    ModeNormal,
			overlay: OverlayFind,
			inputValue: func(model *Model) string {
				return model.searchInput.Value()
			},
		},
		{
			name:    "command",
			context: KeyContextCommand,
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
			setModelKeybindings(t, model, Keybinding{
				Context: testCase.context,
				Chord:   "g x",
				Action:  KeyActionCancel,
			})

			_, _ = model.Update(keyMessage("g"))
			if len(model.chord.strokes) != 1 || model.chord.strokes[0] != "g" {
				t.Fatalf("pending chord = %v", model.chord.strokes)
			}
			if value := testCase.inputValue(model); value != "" {
				t.Fatalf("chord prefix entered modal text: %q", value)
			}

			_, _ = model.Update(keyMessage("x"))
			if model.mode != ModeNormal || model.overlay != OverlayNone || len(model.chord.strokes) != 0 {
				t.Fatalf(
					"completed chord left mode=%v overlay=%v chord=%v",
					model.mode,
					model.overlay,
					model.chord.strokes,
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
			setModelKeybindings(t, model, Keybinding{
				Context: KeyContextNormal,
				Chord:   "g g",
				Action:  KeyActionToggleWrap,
			})

			_, _ = model.Update(keyMessage("g"))
			staleGeneration := model.chord.generation
			_, _ = model.Update(keyMessage(testCase.second))
			_, _ = model.Update(keyMessage("5"))
			if model.repeat != 5 {
				t.Fatalf("numeric repeat = %d before stale timeout", model.repeat)
			}
			_, _ = model.Update(chordTimeoutMsg{generation: staleGeneration})
			if model.repeat != 5 {
				t.Fatalf("stale chord timeout cleared repeat: %d", model.repeat)
			}
		})
	}
}

func TestReloadPreparationRoutesMultiStrokeQuit(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	setModelKeybindings(t, model, Keybinding{
		Context: KeyContextNormal,
		Chord:   "g q",
		Action:  KeyActionQuit,
	})
	model.foreground.reload = reloadFlow{
		phase:     reloadPreparing,
		operation: 10_000,
	}

	_, _ = model.Update(keyMessage("g"))
	if len(model.chord.strokes) != 1 {
		t.Fatalf("reload-preparation chord = %v", model.chord.strokes)
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
	model.commentsVisible = true
	setModelKeybindings(t, model,
		Keybinding{Context: KeyContextNormal, Focus: KeyFocusFileTree, Chord: "g c", Action: KeyActionInterrupt},
		Keybinding{Context: KeyContextNormal, Focus: KeyFocusFileTree, Chord: "j", Action: KeyActionFocusNextPane},
	)
	model.foreground.reload = reloadFlow{phase: reloadAcquiring, operation: 10_000}

	_, _ = model.Update(keyMessage("5"))
	if model.repeat != 5 {
		t.Fatalf("acquiring repeat = %d, want 5", model.repeat)
	}
	model.foreground.reload.phase = reloadPreparing
	_, _ = model.Update(keyMessage("g"))
	_, _ = model.Update(keyMessage("c"))
	if model.repeat != 0 {
		t.Fatalf("reload-preparation cancel retained repeat %d", model.repeat)
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
	model.commentsVisible = true
	setModelKeybindings(t, model,
		Keybinding{Context: KeyContextNormal, Focus: KeyFocusFileTree, Chord: "g x", Action: KeyActionToggleWrap},
		Keybinding{Context: KeyContextNormal, Focus: KeyFocusFileTree, Chord: "j", Action: KeyActionFocusNextPane},
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
	if model.repeat != 0 {
		t.Fatalf("disallowed reload-preparation action retained repeat %d", model.repeat)
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
				Keybinding{
					Context: KeyContextNormal,
					Focus:   KeyFocusFileTree,
					Chord:   "x t",
					Action:  KeyActionToggleWrap,
				},
			)
			initialWrap := model.settings.wrap

			_, _ = model.Update(keyMessage("5"))
			_, _ = model.Update(keyMessage("x"))
			testCase.changeFocusAwayAndBack(model)
			if model.focus != FocusFileTree {
				t.Fatalf("restored focus = %v, want file tree", model.focus)
			}
			if len(model.chord.strokes) != 0 || model.repeat != 0 {
				t.Fatalf("focus transition retained chord=%v repeat=%d", model.chord.strokes, model.repeat)
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
		model.commentsVisible = true
		setModelKeybindings(t, model,
			Keybinding{Context: KeyContextNormal, Focus: KeyFocusFileTree, Chord: "g g", Action: KeyActionToggleWrap},
			Keybinding{Context: KeyContextNormal, Focus: KeyFocusFileTree, Chord: "j", Action: KeyActionFocusNextPane},
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
			Keybinding{Context: KeyContextNormal, Chord: "g g", Action: KeyActionToggleWrap},
			Keybinding{Context: KeyContextNormal, Chord: "j j", Action: KeyActionToggleWrap},
		)

		_, _ = model.Update(keyMessage("5"))
		_, _ = model.Update(keyMessage("g"))
		_, _ = model.Update(keyMessage("j"))
		if model.repeat != 0 || len(model.chord.strokes) != 1 || model.chord.strokes[0] != "j" {
			t.Fatalf("retried prefix retained repeat=%d chord=%v", model.repeat, model.chord.strokes)
		}
	})
}

func modelForKeyContext(context KeyContext) *Model {
	model := &Model{
		size:  TerminalSize{Width: normalWidth, Height: 24, Known: true},
		focus: FocusDiff,
		mode:  ModeNormal,
	}
	switch context {
	case KeyContextNormal:
	case KeyContextVisual:
		model.mode = ModeVisual
	case KeyContextCommentEdit:
		model.mode = ModeCommentEdit
	case KeyContextSearch:
		model.overlay = OverlayFind
	case KeyContextCommand:
		model.mode = ModeCommand
	case KeyContextHelp:
		model.overlay = OverlayHelp
	case KeyContextStatusDetails:
		model.overlay = OverlayStatus
	case KeyContextConfirm:
		model.overlay = OverlayConfirm
	case KeyContextExportPicker:
		model.overlay = OverlayExport
	case KeyContextSubmit:
		model.overlay = OverlaySubmit
	case KeyContextMinimumSize:
		model.size.Width = minimumWidth - 1
	}
	return model
}

func focusOwner(focus KeyFocus) FocusOwner {
	switch focus {
	case KeyFocusFileTree:
		return FocusFileTree
	case KeyFocusComments:
		return FocusComments
	default:
		return FocusDiff
	}
}
