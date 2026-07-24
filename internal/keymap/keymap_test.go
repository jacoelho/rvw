package keymap

import (
	"fmt"
	"strings"
	"testing"
)

func TestChordCanonicalFormAndDisplay(t *testing.T) {
	for _, testCase := range []struct {
		input   string
		chord   Chord
		display string
	}{
		{input: " ", chord: "space", display: "Space"},
		{input: "Return", chord: "enter", display: "Enter"},
		{input: "Ctrl+C", chord: "ctrl-c", display: "Ctrl-C"},
		{input: "Ctrl-Shift+x", chord: "ctrl-shift-x", display: "Ctrl-Shift-X"},
		{input: "Ctrl+é", chord: "ctrl-é", display: "Ctrl-É"},
		{input: "F2", chord: "f2", display: "F2"},
		{input: "G", chord: "G", display: "G"},
		{input: "g   /", chord: "g /", display: "g /"},
		{input: "x-0005", chord: "x-0005", display: "x-0005"},
	} {
		t.Run(testCase.input, func(t *testing.T) {
			chord, err := NewChord(testCase.input)
			if err != nil {
				t.Fatalf("NewChord: %v", err)
			}
			if chord != testCase.chord || chord.Display() != testCase.display {
				t.Fatalf(
					"chord = %q display %q, want %q display %q",
					chord,
					chord.Display(),
					testCase.chord,
					testCase.display,
				)
			}
		})
	}
}

func TestDefaultBindingsAreCanonicalCompleteAndUserFacing(t *testing.T) {
	resolved, err := Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve defaults: %v", err)
	}
	if len(resolved) != len(DefaultBindings()) {
		t.Fatalf("resolved %d defaults, want %d", len(resolved), len(DefaultBindings()))
	}
	for _, binding := range resolved {
		if err := ValidateBinding(binding); err != nil {
			t.Errorf("binding %+v: %v", binding, err)
		}
		if binding.Chord.Display() == "" || binding.Action.Description() == "" {
			t.Errorf("binding has empty user-facing text: %+v", binding)
		}
		if strings.Contains(binding.Action.Description(), binding.Action.String()) &&
			strings.Contains(binding.Action.String(), "-") {
			t.Errorf("binding exposes machine action name: %+v", binding)
		}
	}
}

func TestActionVocabularyIsReachableFromAContext(t *testing.T) {
	reachable := make(map[Action]struct{})
	for _, context := range Contexts() {
		for _, action := range AllowedActions(context) {
			reachable[action] = struct{}{}
		}
	}
	for _, action := range Actions() {
		if _, ok := reachable[action]; !ok {
			t.Errorf("action %q is unreachable", action)
		}
	}
}

func TestUserBindingsRejectReservedInterruptAndScopeMismatch(t *testing.T) {
	_, err := NormalizeUserBinding(Binding{
		Context: ContextNormal,
		Chord:   "Ctrl+C",
		Action:  ActionQuit,
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved interrupt error = %v", err)
	}

	scope := map[Chord]Binding{
		"q": {
			Context: ContextNormal,
			Chord:   "q",
			Action:  ActionQuit,
		},
		"ctrl-c": {
			Context: ContextNormal,
			Focus:   FocusComments,
			Chord:   "ctrl-c",
			Action:  ActionInterrupt,
		},
	}
	if err := ValidateScope(ContextNormal, FocusDiff, scope); err == nil ||
		!strings.Contains(err.Error(), "binding from focus") {
		t.Fatalf("mismatched focus error = %v", err)
	}
}

func TestResolveCapsTheEffectiveKeymap(t *testing.T) {
	bindings := make([]Binding, MaxBindings)
	for index := range bindings {
		bindings[index] = Binding{
			Context: ContextNormal,
			Chord:   Chord(fmt.Sprintf("user-%04d", index)),
			Action:  ActionQuit,
		}
	}
	if _, err := Resolve(bindings); err == nil || !strings.Contains(err.Error(), "effective") {
		t.Fatalf("effective keymap limit error = %v", err)
	}
}
