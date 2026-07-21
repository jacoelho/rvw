package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestKeybindingContextActionMatrixAcceptsExactlyDeclaredPairs(t *testing.T) {
	for context := range keyContexts {
		allowed, exists := contextActions[context]
		if !exists {
			t.Fatalf("accepted context %q has no action matrix", context)
		}
		focuses := []KeyFocus{""}
		if context == KeyContextNormal {
			focuses = append(focuses, KeyFocusFileTree, KeyFocusDiff, KeyFocusComments)
		}
		for _, focus := range focuses {
			for action := range keyActions {
				binding := Keybinding{
					Context: context,
					Focus:   focus,
					Chord:   "rvw-contract",
					Action:  action,
				}
				_, err := resolveKeybindings([]Keybinding{binding})
				if allowed[action] && err != nil {
					t.Errorf("accepted %q/%q/%q: %v", context, focus, action, err)
				}
				if !allowed[action] && err == nil {
					t.Errorf("disallowed %q/%q/%q was accepted", context, focus, action)
				}
			}
		}
	}

	for context, allowed := range contextActions {
		if !keyContexts[context] {
			t.Errorf("action matrix contains unknown context %q", context)
		}
		for action := range allowed {
			if !keyActions[action] {
				t.Errorf("context %q accepts action absent from vocabulary: %q", context, action)
			}
		}
	}
}

func TestKeybindingSchemaRejectsUnknownScopeAndInvalidFocus(t *testing.T) {
	for _, binding := range []Keybinding{
		{Context: "unknown", Chord: "x", Action: KeyActionQuit},
		{Context: KeyContextNormal, Focus: "unknown", Chord: "x", Action: KeyActionQuit},
		{Context: KeyContextHelp, Focus: KeyFocusDiff, Chord: "x", Action: KeyActionCancel},
	} {
		if _, err := resolveKeybindings([]Keybinding{binding}); err == nil {
			t.Errorf("resolveKeybindings(%+v) succeeded", binding)
		}
	}
}

func TestKeybindingChordBoundaries(t *testing.T) {
	for _, chord := range []KeyChord{"x", "x y z w", KeyChord(strings.Repeat("x", 128))} {
		binding, err := normalizeBinding(Keybinding{Context: KeyContextNormal, Chord: chord})
		if err != nil {
			t.Errorf("accepted chord %q: %v", chord, err)
			continue
		}
		if binding.Chord != chord {
			t.Errorf("normalized chord = %q, want %q", binding.Chord, chord)
		}
	}
	for _, chord := range []KeyChord{
		"",
		"x y z w v",
		KeyChord(strings.Repeat("x", 129)),
		"Ctrl-c",
		"x CTRL-C",
	} {
		if _, err := normalizeBinding(Keybinding{Context: KeyContextNormal, Chord: chord}); err == nil {
			t.Errorf("invalid chord %q was accepted", chord)
		}
	}
}

func TestEveryContextRequiresItsDeclaredActions(t *testing.T) {
	for context, required := range requiredActions {
		focus := KeyFocus("")
		if context == KeyContextNormal {
			focus = KeyFocusDiff
		}
		scope := make(map[KeyChord]Keybinding, len(required))
		for index, action := range required {
			chord := KeyChord(fmt.Sprintf("required-%d", index))
			scope[chord] = Keybinding{Context: context, Focus: focus, Chord: chord, Action: action}
		}
		if err := validateRuntimeKeyScope(context, focus, scope); err != nil {
			t.Fatalf("complete required scope %q: %v", context, err)
		}
		for chord, binding := range scope {
			delete(scope, chord)
			if err := validateRuntimeKeyScope(context, focus, scope); err == nil {
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

	contexts := make(map[KeyContext]bool, len(keyContexts))
	for _, binding := range bindings {
		if binding.Chord == "Ctrl-c" && binding.Action == KeyActionInterrupt {
			contexts[binding.Context] = true
		}
	}
	for context := range keyContexts {
		if !contexts[context] {
			t.Errorf("context %q has no reserved Ctrl-c interrupt binding", context)
		}
	}
}

func TestKeybindingPrefixAmbiguityIsRejectedInEitherOrder(t *testing.T) {
	short := Keybinding{Context: KeyContextNormal, Chord: "g", Action: KeyActionQuit}
	long := Keybinding{Context: KeyContextNormal, Chord: "g g", Action: KeyActionInterrupt}
	for _, bindings := range [][]Keybinding{{short, long}, {long, short}} {
		scope := make(map[KeyChord]Keybinding, len(bindings))
		for _, binding := range bindings {
			scope[binding.Chord] = binding
		}
		if err := validateRuntimeKeyScope(KeyContextNormal, "", scope); err == nil {
			t.Errorf("prefix-conflicting order %+v was accepted", bindings)
		}
	}

	nonconflicting := map[KeyChord]Keybinding{
		"g g": {Context: KeyContextNormal, Chord: "g g", Action: KeyActionQuit},
		"g h": {Context: KeyContextNormal, Chord: "g h", Action: KeyActionInterrupt},
	}
	if err := validateRuntimeKeyScope(KeyContextNormal, "", nonconflicting); err != nil {
		t.Fatalf("non-conflicting chords: %v", err)
	}
}
