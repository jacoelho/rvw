package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/jacoelho/rvw/internal/keymap"
)

var (
	compiledActionSink     keymap.Action
	compiledBindingsSink   []keymap.Binding
	compiledResolutionSink keyResolution
	compiledStringSink     string
)

func setModelKeybindings(tb testing.TB, model *Model, bindings ...keymap.Binding) {
	tb.Helper()
	compiled, err := keymap.Build(bindings)
	if err != nil {
		tb.Fatalf("compile keybindings: %v", err)
	}
	model.settings.keymap = compiled
	model.keys.SetMap(compiled)
}

func TestCompiledKeybindingsPreserveOrderAndFocusOverrides(t *testing.T) {
	raw := []keymap.Binding{
		{
			Context: keymap.ContextNormal,
			Chord:   "enter",
			Action:  keymap.ActionMoveUp,
		},
		{
			Context: keymap.ContextNormal,
			Chord:   "b",
			Action:  keymap.ActionMoveDown,
		},
		{
			Context: keymap.ContextNormal,
			Focus:   keymap.FocusFileTree,
			Chord:   "enter",
			Action:  keymap.ActionLast,
		},
		{
			Context: keymap.ContextNormal,
			Focus:   keymap.FocusFileTree,
			Chord:   "c",
			Action:  keymap.ActionActivate,
		},
	}
	compiled, err := keymap.Build(raw)
	if err != nil {
		t.Fatalf("compile keybindings: %v", err)
	}
	raw[0] = keymap.Binding{Context: keymap.ContextNormal, Chord: "x", Action: keymap.ActionQuit}

	tree := compiled.Bindings(keymap.Scope{
		Context: keymap.ContextNormal,
		Focus:   keymap.FocusFileTree,
	})
	if len(tree) != 3 ||
		tree[0].Chord != "enter" || tree[0].Action != keymap.ActionLast ||
		tree[1].Chord != "b" || tree[1].Action != keymap.ActionMoveDown ||
		tree[2].Chord != "c" || tree[2].Action != keymap.ActionActivate {
		t.Fatalf("file-tree bindings = %#v", tree)
	}
	diff := compiled.Bindings(keymap.Scope{Context: keymap.ContextNormal, Focus: keymap.FocusDiff})
	if len(diff) != 2 ||
		diff[0].Chord != "enter" || diff[0].Action != keymap.ActionMoveUp ||
		diff[1].Chord != "b" || diff[1].Action != keymap.ActionMoveDown {
		t.Fatalf("diff bindings = %#v", diff)
	}
	resolution := compiled.Resolve(
		keymap.Scope{Context: keymap.ContextNormal, Focus: keymap.FocusFileTree},
		[]string{"enter"},
	)
	if resolution.Action != keymap.ActionLast || !resolution.Exact || resolution.Prefix {
		t.Fatalf("focused enter = %q exact=%v prefix=%v", resolution.Action, resolution.Exact, resolution.Prefix)
	}
	if summary := compiled.Summary(keymap.Scope{
		Context: keymap.ContextNormal,
		Focus:   keymap.FocusFileTree,
	}); !strings.HasPrefix(
		summary,
		"Enter last row  b next row  c open",
	) {
		t.Fatalf("file-tree summary = %q", summary)
	}
}

func TestCompiledKeybindingsBoundMaximumResolutionAndSummaries(t *testing.T) {
	bindings := make([]keymap.Binding, keymap.MaxBindings)
	for index := range bindings {
		bindings[index] = keymap.Binding{
			Context: keymap.ContextNormal,
			Chord:   keymap.Chord(fmt.Sprintf("x-%04d", index)),
			Action:  keymap.ActionMoveDown,
		}
	}
	bindings[0].Chord, bindings[0].Action = "focus", keymap.ActionFocusNextPane
	bindings[1].Chord, bindings[1].Action = "activate", keymap.ActionActivate
	bindings[2].Chord, bindings[2].Action = helpLabel, keymap.ActionHelp
	bindings[3].Chord, bindings[3].Action = "quit", keymap.ActionQuit
	bindings[4].Chord, bindings[4].Action = "g target", keymap.ActionMoveUp

	model := modelForKeyContext(keymap.ContextNormal)
	setModelKeybindings(t, model, bindings...)
	target := []string{"x-4095"}
	prefix := []string{"g"}
	multi := []string{"g", "target"}

	resolution := model.resolveAction(target)
	if resolution.action != keymap.ActionMoveDown || !resolution.exact() || resolution.prefix() {
		t.Fatalf("maximum exact = %q kind=%v", resolution.action, resolution.kind)
	}
	resolution = model.resolveAction(prefix)
	if resolution.exact() || !resolution.prefix() {
		t.Fatalf("maximum prefix kind=%v", resolution.kind)
	}
	resolution = model.resolveAction(multi)
	if resolution.action != keymap.ActionMoveUp || !resolution.exact() || resolution.prefix() {
		t.Fatalf("maximum multi = %q kind=%v", resolution.action, resolution.kind)
	}
	wantSummary := strings.Join([]string{
		"focus " + keymap.ActionFocusNextPane.Description(),
		"activate " + keymap.ActionActivate.Description(),
		"help " + keymap.ActionHelp.Description(),
		"quit " + keymap.ActionQuit.Description(),
	}, "  ")
	if summary := model.bindingSummary(keymap.ContextNormal); summary != wantSummary {
		t.Fatalf("binding summary = %q, want %q", summary, wantSummary)
	}
	wantStatusBindings := []keymap.Binding{
		bindings[5],
		bindings[0],
		bindings[2],
	}
	if statusBindings := model.normalStatusBindings(); !slices.Equal(
		statusBindings,
		wantStatusBindings,
	) {
		t.Fatalf("status bindings = %#v, want %#v", statusBindings, wantStatusBindings)
	}

	allocations := testing.AllocsPerRun(1_000, func() {
		compiledResolutionSink = model.resolveAction(target)
		compiledActionSink = compiledResolutionSink.action
		compiledResolutionSink = model.resolveAction(prefix)
		compiledStringSink = model.bindingSummary(keymap.ContextNormal)
		compiledBindingsSink = model.normalStatusBindings()
		compiledBindingsSink = model.effectiveBindings(keymap.ContextNormal)
	})
	if allocations != 0 {
		t.Fatalf("compiled resolution and summaries allocate %.2f times", allocations)
	}
}

func TestBindingBarKeepsWholePriorityOrderedHints(t *testing.T) {
	bindings := []keymap.Binding{
		{Chord: "j", Action: keymap.ActionMoveDown},
		{Chord: "c", Action: keymap.ActionCommentAtCursor},
		{Chord: "tab", Action: keymap.ActionFocusNextPane},
		{Chord: "?", Action: keymap.ActionHelp},
		{Chord: "q", Action: keymap.ActionQuit},
	}
	styles := semanticStyles{
		heading: lipgloss.NewStyle(),
		muted:   lipgloss.NewStyle(),
	}
	bar := bindingBar{bindings: bindings}
	hints := []string{
		"j " + keymap.ActionMoveDown.Description(),
		"c " + keymap.ActionCommentAtCursor.Description(),
		"Tab " + keymap.ActionFocusNextPane.Description(),
		"? " + keymap.ActionHelp.Description(),
	}
	for count := 1; count <= bindingBarMaximumItems; count++ {
		expected := strings.Join(hints[:count], "  ")
		boundary := lipgloss.Width(expected)
		if got := bar.Render(boundary, styles); got != expected {
			t.Fatalf("hint %d at boundary = %q, want %q", count, got, expected)
		}
	}
	oversized := keymap.Binding{
		Chord:  keymap.Chord(strings.Repeat("x", 64)),
		Action: keymap.ActionMoveDown,
	}
	short := keymap.Binding{Chord: "?", Action: keymap.ActionHelp}
	wantShort := "? " + keymap.ActionHelp.Description()
	if got := (bindingBar{bindings: []keymap.Binding{oversized, short}}).Render(32, styles); got != wantShort {
		t.Fatalf("oversized leading hint hid later usable hint: %q", got)
	}
	if got := bar.Render(1_000, styles); strings.Contains(got, "q ") {
		t.Fatalf("binding bar rendered more than %d hints: %q", bindingBarMaximumItems, got)
	}
	if got := (bindingBar{}).Render(80, styles); got != "" {
		t.Fatalf("empty binding bar = %q", got)
	}
	if got := (bindingBar{bindings: bindings[:1]}).Render(80, styles); got != hints[0] {
		t.Fatalf("one-item binding bar = %q, want %q", got, hints[0])
	}
}

func TestCompiledKeybindingsEnforceFixedRuntimeBounds(t *testing.T) {
	tooMany := make([]keymap.Binding, keymap.MaxBindings+1)
	if _, err := keymap.Build(tooMany); err == nil {
		t.Fatal("keybinding count above the runtime maximum was accepted")
	}
	if _, err := keymap.Build([]keymap.Binding{{
		Context: keymap.ContextNormal,
		Chord:   "a b c d e",
		Action:  keymap.ActionQuit,
	}}); err == nil {
		t.Fatal("five-stroke chord was accepted")
	}
}

func TestSettingsCloneSharesCompiledKeybindings(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	clone := model.settings.clone()
	if clone.keymap == nil || clone.keymap != model.settings.keymap {
		t.Fatal("settings clone did not share the immutable compiled keybindings")
	}
}
