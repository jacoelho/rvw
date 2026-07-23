package app

import (
	"fmt"
	"strings"
	"testing"
)

var (
	compiledActionSink     KeyAction
	compiledBindingsSink   []Keybinding
	compiledResolutionSink keyResolution
	compiledStringSink     string
)

func setModelKeybindings(tb testing.TB, model *Model, bindings ...Keybinding) {
	tb.Helper()
	keymap, err := compileKeybindings(bindings)
	if err != nil {
		tb.Fatalf("compile keybindings: %v", err)
	}
	model.settings.keymap = keymap
}

func TestCompiledKeybindingsPreserveOrderAndFocusOverrides(t *testing.T) {
	raw := []Keybinding{
		{
			Context: KeyContextNormal,
			Chord:   keyReturn,
			Action:  KeyActionMoveUp,
		},
		{
			Context: KeyContextNormal,
			Chord:   "b",
			Action:  KeyActionMoveDown,
		},
		{
			Context: KeyContextNormal,
			Focus:   KeyFocusFileTree,
			Chord:   "enter",
			Action:  KeyActionLast,
		},
		{
			Context: KeyContextNormal,
			Focus:   KeyFocusFileTree,
			Chord:   "c",
			Action:  KeyActionActivate,
		},
	}
	keymap, err := compileKeybindings(raw)
	if err != nil {
		t.Fatalf("compile keybindings: %v", err)
	}
	raw[0] = Keybinding{Context: KeyContextNormal, Chord: "x", Action: KeyActionQuit}

	tree := keymap.effectiveBindings(KeyContextNormal, KeyFocusFileTree)
	if len(tree) != 3 ||
		tree[0].Chord != "enter" || tree[0].Action != KeyActionLast ||
		tree[1].Chord != "b" || tree[1].Action != KeyActionMoveDown ||
		tree[2].Chord != "c" || tree[2].Action != KeyActionActivate {
		t.Fatalf("file-tree bindings = %#v", tree)
	}
	diff := keymap.effectiveBindings(KeyContextNormal, KeyFocusDiff)
	if len(diff) != 2 ||
		diff[0].Chord != KeyChord(keyReturn) || diff[0].Action != KeyActionMoveUp ||
		diff[1].Chord != "b" || diff[1].Action != KeyActionMoveDown {
		t.Fatalf("diff bindings = %#v", diff)
	}
	resolution := keymap.resolve(
		KeyContextNormal,
		KeyFocusFileTree,
		[]string{"enter"},
	)
	if resolution.action != KeyActionLast || !resolution.exact() || resolution.prefix() {
		t.Fatalf("focused enter = %q kind=%v", resolution.action, resolution.kind)
	}
	if summary := keymap.bindingSummary(KeyContextNormal, KeyFocusFileTree); !strings.HasPrefix(
		summary,
		"enter last  b move-down  c activate",
	) {
		t.Fatalf("file-tree summary = %q", summary)
	}
}

func TestCompiledKeybindingsBoundMaximumResolutionAndSummaries(t *testing.T) {
	bindings := make([]Keybinding, maxKeybindings)
	for index := range bindings {
		bindings[index] = Keybinding{
			Context: KeyContextNormal,
			Chord:   KeyChord(fmt.Sprintf("x-%04d", index)),
			Action:  KeyActionMoveDown,
		}
	}
	bindings[0].Chord, bindings[0].Action = "focus", KeyActionFocusNextPane
	bindings[1].Chord, bindings[1].Action = "activate", KeyActionActivate
	bindings[2].Chord, bindings[2].Action = "help", KeyActionHelp
	bindings[3].Chord, bindings[3].Action = "quit", KeyActionQuit
	bindings[4].Chord, bindings[4].Action = "g target", KeyActionMoveUp

	model := modelForKeyContext(KeyContextNormal)
	setModelKeybindings(t, model, bindings...)
	target := []string{"x-4095"}
	prefix := []string{"g"}
	multi := []string{"g", "target"}

	resolution := model.resolveAction(target)
	if resolution.action != KeyActionMoveDown || !resolution.exact() || resolution.prefix() {
		t.Fatalf("maximum exact = %q kind=%v", resolution.action, resolution.kind)
	}
	resolution = model.resolveAction(prefix)
	if resolution.exact() || !resolution.prefix() {
		t.Fatalf("maximum prefix kind=%v", resolution.kind)
	}
	resolution = model.resolveAction(multi)
	if resolution.action != KeyActionMoveUp || !resolution.exact() || resolution.prefix() {
		t.Fatalf("maximum multi = %q kind=%v", resolution.action, resolution.kind)
	}
	wantSummary := strings.Join([]string{
		"focus " + string(KeyActionFocusNextPane),
		"activate " + string(KeyActionActivate),
		"help " + string(KeyActionHelp),
		"quit " + string(KeyActionQuit),
	}, "  ")
	if summary := model.bindingSummary(KeyContextNormal); summary != wantSummary {
		t.Fatalf("binding summary = %q, want %q", summary, wantSummary)
	}
	if summary := model.normalStatusBindingSummary(); summary != wantSummary {
		t.Fatalf("action summary = %q, want %q", summary, wantSummary)
	}

	allocations := testing.AllocsPerRun(1_000, func() {
		compiledResolutionSink = model.resolveAction(target)
		compiledActionSink = compiledResolutionSink.action
		compiledResolutionSink = model.resolveAction(prefix)
		compiledStringSink = model.bindingSummary(KeyContextNormal)
		compiledStringSink = model.normalStatusBindingSummary()
		compiledBindingsSink = model.effectiveBindings(KeyContextNormal)
	})
	if allocations != 0 {
		t.Fatalf("compiled resolution and summaries allocate %.2f times", allocations)
	}
}

func TestCompiledKeybindingsEnforceFixedRuntimeBounds(t *testing.T) {
	tooMany := make([]Keybinding, maxKeybindings+1)
	if _, err := compileKeybindings(tooMany); err == nil {
		t.Fatal("keybinding count above the runtime maximum was accepted")
	}
	if _, err := compileKeybindings([]Keybinding{{
		Context: KeyContextNormal,
		Chord:   "a b c d e",
		Action:  KeyActionQuit,
	}}); err == nil {
		t.Fatal("five-stroke chord was accepted")
	}
}

func TestSettingsCloneSharesCompiledKeybindings(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	clone, err := model.settings.clone()
	if err != nil {
		t.Fatalf("clone settings: %v", err)
	}
	if clone.keymap == nil || clone.keymap != model.settings.keymap {
		t.Fatal("settings clone did not share the immutable compiled keybindings")
	}
}
