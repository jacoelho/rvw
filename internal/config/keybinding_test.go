package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/keymap"
)

func TestKeybindingContextActionMatrixAcceptsExactlyDeclaredPairs(t *testing.T) {
	for _, context := range keymap.Contexts() {
		allowed := keymap.AllowedActions(context)
		focuses := []keymap.Focus{""}
		if context == keymap.ContextNormal {
			focuses = append(focuses, keymap.FocusFileTree, keymap.FocusDiff, keymap.FocusComments)
		}
		for _, focus := range focuses {
			for _, action := range keymap.Actions() {
				binding := Keybinding{
					Context: context,
					Focus:   focus,
					Chord:   "rvw-contract",
					Action:  action,
				}
				_, err := resolveKeybindings([]Keybinding{binding})
				if slices.Contains(allowed, action) && err != nil {
					t.Errorf("accepted %q/%q/%q: %v", context, focus, action, err)
				}
				if !slices.Contains(allowed, action) && err == nil {
					t.Errorf("disallowed %q/%q/%q was accepted", context, focus, action)
				}
			}
		}
	}
}

func TestKeybindingSchemaRejectsUnknownScopeAndInvalidFocus(t *testing.T) {
	for _, binding := range []Keybinding{
		{Context: "unknown", Chord: "x", Action: keymap.ActionQuit},
		{Context: keymap.ContextNormal, Focus: "unknown", Chord: "x", Action: keymap.ActionQuit},
		{Context: keymap.ContextHelp, Focus: keymap.FocusDiff, Chord: "x", Action: keymap.ActionCancel},
	} {
		if _, err := resolveKeybindings([]Keybinding{binding}); err == nil {
			t.Errorf("resolveKeybindings(%+v) succeeded", binding)
		}
	}
}

func TestKeybindingChordBoundaries(t *testing.T) {
	for _, chord := range []keymap.Chord{"x", "x y z w", keymap.Chord(strings.Repeat("x", 128))} {
		binding, err := keymap.NormalizeUserBinding(Keybinding{
			Context: keymap.ContextNormal,
			Chord:   chord,
			Action:  keymap.ActionQuit,
		})
		if err != nil {
			t.Errorf("accepted chord %q: %v", chord, err)
			continue
		}
		if binding.Chord != chord {
			t.Errorf("normalized chord = %q, want %q", binding.Chord, chord)
		}
	}
	for _, chord := range []keymap.Chord{
		"",
		"x y z w v",
		keymap.Chord(strings.Repeat("x", 129)),
		"Ctrl-c",
		"x CTRL-C",
	} {
		if _, err := keymap.NormalizeUserBinding(Keybinding{
			Context: keymap.ContextNormal,
			Chord:   chord,
			Action:  keymap.ActionQuit,
		}); err == nil {
			t.Errorf("invalid chord %q was accepted", chord)
		}
	}
}

func TestEveryContextRequiresItsDeclaredActions(t *testing.T) {
	for _, context := range keymap.Contexts() {
		required := keymap.RequiredActions(context)
		focus := keymap.Focus("")
		if context == keymap.ContextNormal {
			focus = keymap.FocusDiff
		}
		scope := make(map[keymap.Chord]Keybinding, len(required))
		for index, action := range required {
			chord := keymap.Chord(fmt.Sprintf("required-%d", index))
			scope[chord] = Keybinding{Context: context, Focus: focus, Chord: chord, Action: action}
		}
		if err := keymap.ValidateScope(context, focus, scope); err != nil {
			t.Fatalf("complete required scope %q: %v", context, err)
		}
		for chord, binding := range scope {
			delete(scope, chord)
			if err := keymap.ValidateScope(context, focus, scope); err == nil {
				t.Errorf("context %q accepted scope missing %q", context, binding.Action)
			}
			scope[chord] = binding
		}
	}
}

func TestDefaultKeybindingsReserveInterruptInEveryContext(t *testing.T) {
	bindings, err := resolveKeybindings(nil)
	if err != nil {
		t.Fatalf("resolve default keybindings: %v", err)
	}

	contexts := make(map[keymap.Context]bool, len(keymap.Contexts()))
	for _, binding := range bindings {
		if binding.Chord == "ctrl-c" && binding.Action == keymap.ActionInterrupt {
			contexts[binding.Context] = true
		}
	}
	for _, context := range keymap.Contexts() {
		if !contexts[context] {
			t.Errorf("context %q has no reserved Ctrl-c interrupt binding", context)
		}
	}
}

func TestDefaultSubmitKeybindingsExposeStatusDetails(t *testing.T) {
	bindings, err := resolveKeybindings(nil)
	if err != nil {
		t.Fatalf("resolve default keybindings: %v", err)
	}
	for _, binding := range bindings {
		if binding.Context == keymap.ContextSubmit && binding.Chord == "f2" &&
			binding.Action == keymap.ActionStatusDetails {
			return
		}
	}
	t.Fatal("submit context has no default F2 status-details binding")
}

func TestSubmitKeybindingsRejectRemovedAdvancedActions(t *testing.T) {
	for _, action := range []string{
		"toggle-stale-head-override",
		"toggle-submit-exclusion",
		"toggle-submit-summary",
	} {
		if _, err := resolveKeybindings([]Keybinding{{
			Context: keymap.ContextSubmit,
			Chord:   "x",
			Action:  keymap.Action(action),
		}}); err == nil {
			t.Errorf("removed submit action %q was accepted", action)
		}
	}

	for _, action := range []keymap.Action{
		keymap.ActionMoveUp,
		keymap.ActionMoveDown,
		keymap.ActionPageUp,
		keymap.ActionPageDown,
	} {
		if keymap.ActionAllowed(keymap.ContextSubmit, action) {
			t.Errorf("generic action %q remains available in submit context", action)
		}
	}
}

func TestDefaultVisualKeybindingsPreserveNavigationAlternatives(t *testing.T) {
	bindings, err := resolveKeybindings(nil)
	if err != nil {
		t.Fatalf("resolve default keybindings: %v", err)
	}

	want := map[keymap.Context]map[keymap.Chord]keymap.Action{
		keymap.ContextNormal: {
			"j":    keymap.ActionMoveDown,
			"down": keymap.ActionMoveDown,
			"k":    keymap.ActionMoveUp,
			"up":   keymap.ActionMoveUp,
		},
		keymap.ContextVisual: {
			"j":    keymap.ActionExtendDown,
			"down": keymap.ActionExtendDown,
			"k":    keymap.ActionExtendUp,
			"up":   keymap.ActionExtendUp,
		},
	}
	for _, binding := range bindings {
		if actions, exists := want[binding.Context]; exists && actions[binding.Chord] == binding.Action {
			delete(actions, binding.Chord)
		}
	}
	for context, missing := range want {
		for chord, action := range missing {
			t.Errorf("default %q binding %q does not perform %q", context, chord, action)
		}
	}
}

func TestDefaultFindKeybindingsExposeScopesAndResultNavigation(t *testing.T) {
	bindings, err := resolveKeybindings(nil)
	if err != nil {
		t.Fatalf("resolve default keybindings: %v", err)
	}

	want := map[keymap.Context]map[keymap.Chord]keymap.Action{
		keymap.ContextNormal: {
			"/":      keymap.ActionSearch,
			"g /":    keymap.ActionSearchVisible,
			"ctrl-p": keymap.ActionFindFile,
		},
		keymap.ContextSearch: {
			"tab":       keymap.ActionNextSearchScope,
			"shift-tab": keymap.ActionPreviousSearchScope,
			"up":        keymap.ActionMoveUp,
			"down":      keymap.ActionMoveDown,
			"ctrl-p":    keymap.ActionMoveUp,
			"ctrl-n":    keymap.ActionMoveDown,
			"pgup":      keymap.ActionPageUp,
			"pgdown":    keymap.ActionPageDown,
		},
	}
	for _, binding := range bindings {
		if actions, exists := want[binding.Context]; exists && actions[binding.Chord] == binding.Action {
			delete(actions, binding.Chord)
		}
	}
	for context, missing := range want {
		for chord, action := range missing {
			t.Errorf("default %q binding %q does not perform %q", context, chord, action)
		}
	}
}

func TestKeybindingPrefixAmbiguityIsRejectedInEitherOrder(t *testing.T) {
	short := Keybinding{Context: keymap.ContextNormal, Chord: "g", Action: keymap.ActionQuit}
	long := Keybinding{Context: keymap.ContextNormal, Chord: "g g", Action: keymap.ActionInterrupt}
	for _, bindings := range [][]Keybinding{{short, long}, {long, short}} {
		scope := make(map[keymap.Chord]Keybinding, len(bindings))
		for _, binding := range bindings {
			scope[binding.Chord] = binding
		}
		if err := keymap.ValidateScope(keymap.ContextNormal, "", scope); err == nil {
			t.Errorf("prefix-conflicting order %+v was accepted", bindings)
		}
	}

	nonconflicting := map[keymap.Chord]Keybinding{
		"g g": {Context: keymap.ContextNormal, Chord: "g g", Action: keymap.ActionQuit},
		"g h": {Context: keymap.ContextNormal, Chord: "g h", Action: keymap.ActionInterrupt},
	}
	if err := keymap.ValidateScope(keymap.ContextNormal, "", nonconflicting); err != nil {
		t.Fatalf("non-conflicting chords: %v", err)
	}
}
