package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/config"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/tui"
)

func TestInteractiveKeybindingBoundaryConvertsAcceptedVocabulary(t *testing.T) {
	type expectedBinding struct {
		context string
		focus   string
		chord   string
		action  string
	}
	var (
		contents strings.Builder
		expected []expectedBinding
	)
	for _, context := range keymap.Contexts() {
		focuses := []string{""}
		if context == keymap.ContextNormal {
			focuses = append(focuses, "file-tree", "diff", "comments")
		}
		for _, focus := range focuses {
			for _, action := range keymap.AllowedActions(context) {
				chord := fmt.Sprintf("rvw-contract-%03d", len(expected))
				contents.WriteString("[[ui.keybindings]]\n")
				fmt.Fprintf(&contents, "context = %q\n", context)
				if focus != "" {
					fmt.Fprintf(&contents, "focus = %q\n", focus)
				}
				fmt.Fprintf(&contents, "chord = %q\naction = %q\n\n", chord, action)
				expected = append(expected, expectedBinding{
					context: context.String(),
					focus:   focus,
					chord:   chord,
					action:  action.String(),
				})
			}
		}
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	configDirectory := filepath.Join(home, ".config", "rvw")
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "config.toml"), []byte(contents.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.LoadGlobal(config.LoadRequest{
		CWD:         root,
		Environment: []string{"HOME=" + home, "NO_COLOR=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	converted, err := interactiveKeybindings(resolved.UI().Keybindings)
	if err != nil {
		t.Fatal(err)
	}
	byChord := make(map[string]int, len(converted))
	for index, binding := range converted {
		byChord[binding.Chord.String()] = index
	}
	for _, want := range expected {
		index, exists := byChord[want.chord]
		if !exists {
			t.Errorf("converted bindings omit %s/%s/%s", want.context, want.focus, want.action)
			continue
		}
		got := converted[index]
		if got.Context.String() != want.context || got.Focus.String() != want.focus ||
			got.Chord.String() != want.chord || got.Action.String() != want.action {
			t.Errorf("converted binding = %+v, want %+v", got, want)
		}
	}
	if _, err := interactiveSettings(resolved); err != nil {
		t.Fatalf("interactiveSettings rejected converted accepted vocabulary: %v", err)
	}
}

func TestInteractivePaletteBridgesEverySemanticThemeRole(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.LoadGlobal(config.LoadRequest{
		CWD:         root,
		Environment: []string{"HOME=" + home},
	})
	if err != nil {
		t.Fatal(err)
	}
	theme, ok := resolved.Theme(resolved.ActiveTheme())
	if !ok {
		t.Fatal("active bundled theme is absent")
	}
	values := interactivePaletteValues(theme)
	for themeRole, paletteRole := range map[config.ThemeColor]tui.PaletteRole{
		"panel_bg":           tui.PalettePanelBackground,
		"fg_primary":         tui.PalettePrimary,
		"fg_secondary":       tui.PaletteSecondary,
		"fg_dim":             tui.PaletteDim,
		"diff_add":           tui.PaletteDiffAdd,
		"diff_del":           tui.PaletteDiffDelete,
		"diff_context":       tui.PaletteDiffContext,
		"pending":            tui.PalettePending,
		"border_focused":     tui.PaletteFocused,
		"status_bar_bg":      tui.PaletteStatusBackground,
		"cursor_line_bg":     tui.PaletteCursorBackground,
		"cursor_line_fg":     tui.PaletteCursorForeground,
		"diff_add_bg":        tui.PaletteDiffAddBackground,
		"diff_del_bg":        tui.PaletteDiffDeleteBackground,
		"diff_hunk_header":   tui.PaletteDiffHunk,
		"message_warning_fg": tui.PaletteWarningForeground,
		"message_warning_bg": tui.PaletteWarningBackground,
		"message_error_fg":   tui.PaletteErrorForeground,
		"message_error_bg":   tui.PaletteErrorBackground,
		"message_success_fg": tui.PaletteSuccessForeground,
		"message_success_bg": tui.PaletteSuccessBackground,
	} {
		want := theme.Colors[themeRole].String()
		if got := values[paletteRole]; got != want {
			t.Fatalf("theme %q palette value = %q, want %q", themeRole, got, want)
		}
	}
	for syntaxRole, paletteRole := range map[config.SyntaxColor]tui.PaletteRole{
		config.SyntaxKeyword: tui.PaletteSyntaxKeyword,
		config.SyntaxString:  tui.PaletteSyntaxString,
		config.SyntaxComment: tui.PaletteSyntaxComment,
		config.SyntaxNumber:  tui.PaletteSyntaxNumber,
	} {
		if got, want := values[paletteRole], theme.SyntaxColors[syntaxRole].String(); got == "" || got != want {
			t.Fatalf("syntax %q palette value = %q, want %q", syntaxRole, got, want)
		}
	}
	if _, err := interactivePalette(resolved, resolved.ActiveTheme()); err != nil {
		t.Fatalf("final interactive palette: %v", err)
	}
}

func TestInteractiveKeybindingsReportsInvalidBoundaryField(t *testing.T) {
	_, err := interactiveKeybindings([]config.Keybinding{{
		Context: "unknown",
		Chord:   "x",
		Action:  keymap.ActionQuit,
	}})
	if err == nil || !strings.Contains(err.Error(), "keybinding 1: keybinding context") {
		t.Fatalf("interactiveKeybindings error = %v", err)
	}
}

func TestInteractiveKeybindingsRejectsUnresolvedUnbind(t *testing.T) {
	_, err := interactiveKeybindings([]config.Keybinding{{
		Context: keymap.ContextNormal,
		Chord:   "q",
		Unbind:  true,
	}})
	if err == nil || !strings.Contains(err.Error(), "require context, chord, and action") {
		t.Fatalf("interactiveKeybindings unresolved unbind error = %v", err)
	}
}

func TestInteractiveProgramEnvironmentOwnsInputAndRemovesOnlyNoColor(t *testing.T) {
	original := []string{
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"NO_COLOR=1",
		"NO_COLOR=again",
		"GITHUB_TOKEN=secret",
	}
	implicit := interactiveProgramEnvironment(original, false)
	implicit[0] = "TERM=changed"
	if original[0] != "TERM=xterm-256color" || len(implicit) != len(original) {
		t.Fatalf("implicit environment aliases input: original=%q copied=%q", original, implicit)
	}
	explicit := interactiveProgramEnvironment(original, true)
	want := []string{
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"GITHUB_TOKEN=secret",
	}
	if !slices.Equal(explicit, want) {
		t.Fatalf("explicit environment = %q, want %q", explicit, want)
	}
}
