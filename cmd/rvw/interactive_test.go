package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/config"
)

type acceptedKeybindingContext struct {
	context string
	actions []string
}

var acceptedKeybindingVocabulary = []acceptedKeybindingContext{
	{context: "normal", actions: strings.Fields(`
		quit interrupt focus-next-pane focus-previous-pane move-up move-down scroll-left scroll-right
		first last page-up page-down half-page-up half-page-down next-file previous-file next-hunk
		previous-hunk next-comment previous-comment next-search-hit previous-search-hit
		comment-at-cursor file-comment summary-comment edit-comment delete-comment expand-context
		toggle-file-reviewed toggle-hunk-reviewed activate accept toggle-wrap toggle-view align-top
		align-center align-bottom toggle-directory expand-directory collapse-directory visual-line
		visual-lines visual-hunk command filter help status-details search open-submit reload
		copy-markdown export-markdown export-json toggle-file-panel toggle-comments toggle-hidden-files
	`)},
	{context: "visual", actions: strings.Fields(`
		cancel interrupt move-up move-down page-up page-down extend-up extend-down comment-selection
	`)},
	{context: "comment-edit", actions: strings.Fields(`
		cancel interrupt delete-backward save-comment insert-newline
	`)},
	{context: "search", actions: strings.Fields(`cancel interrupt accept delete-backward`)},
	{context: "command", actions: strings.Fields(`cancel interrupt accept delete-backward`)},
	{context: "help", actions: strings.Fields(`
		cancel interrupt next-help-page previous-help-page help-line-up help-line-down help-page-up
		help-page-down help-home help-end
	`)},
	{context: "status-details", actions: strings.Fields(`
		cancel interrupt move-up move-down page-up page-down
	`)},
	{context: "confirm", actions: strings.Fields(`cancel interrupt confirm-yes confirm-no`)},
	{context: "export-picker", actions: strings.Fields(`
		cancel interrupt toggle-export-unmappable export-selected
	`)},
	{context: "submit-preflight", actions: strings.Fields(`
		cancel interrupt submit-recheck toggle-stale-head-override toggle-submit-exclusion
		toggle-submit-summary submit-review move-up move-down page-up page-down
	`)},
	{context: "minimum-size", actions: strings.Fields(`quit interrupt`)},
}

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
	for _, vocabulary := range acceptedKeybindingVocabulary {
		focuses := []string{""}
		if vocabulary.context == "normal" {
			focuses = append(focuses, "file-tree", "diff", "comments")
		}
		for _, focus := range focuses {
			for _, action := range vocabulary.actions {
				chord := fmt.Sprintf("rvw-contract-%03d", len(expected))
				contents.WriteString("[[ui.keybindings]]\n")
				fmt.Fprintf(&contents, "context = %q\n", vocabulary.context)
				if focus != "" {
					fmt.Fprintf(&contents, "focus = %q\n", focus)
				}
				fmt.Fprintf(&contents, "chord = %q\naction = %q\n\n", chord, action)
				expected = append(expected, expectedBinding{
					context: vocabulary.context,
					focus:   focus,
					chord:   chord,
					action:  action,
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

func TestInteractiveKeybindingsReportsInvalidBoundaryField(t *testing.T) {
	_, err := interactiveKeybindings([]config.Keybinding{{
		Context: "unknown",
		Chord:   "x",
		Action:  config.KeyActionQuit,
	}})
	if err == nil || !strings.Contains(err.Error(), "keybinding 1 context") {
		t.Fatalf("interactiveKeybindings error = %v", err)
	}
}
