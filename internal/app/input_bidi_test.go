package app

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/review"
)

func TestCommandInputRejectsBidirectionalControlsTransactionally(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	model.size = TerminalSize{Width: normalWidth, Height: 24, Known: true}
	for _, character := range []rune{'\u202e', '\u2066'} {
		for _, input := range []struct {
			name    string
			message tea.Msg
		}{
			{name: "paste", message: tea.PasteMsg{Content: string(character)}},
			{
				name: "key",
				message: tea.KeyPressMsg(tea.Key{
					Code: character,
					Text: string(character),
				}),
			},
		} {
			t.Run(input.name+"/"+string(character), func(t *testing.T) {
				_ = model.beginCommandPrompt()
				model.commandInput.SetValue("safe")
				model.commandInput.SetCursor(2)

				_ = model.updatePromptInput(input.message)
				if value := model.commandInput.Value(); value != "safe" {
					t.Fatalf("command value = %q, want safe", value)
				}
				if position := model.commandInput.Position(); position != 2 {
					t.Fatalf("command cursor = %d, want 2", position)
				}
				if model.notice == nil || model.notice.Kind != NoticeWarning ||
					model.notice.Text != "command input contains bidirectional control" {
					t.Fatalf("command notice = %+v", model.notice)
				}
				if view := model.View().Content; strings.ContainsRune(view, character) {
					t.Fatalf("command view contains raw bidirectional control %U", character)
				}
			})
		}
	}
}

func TestFindInputRejectsBidirectionalControlsWithoutChangingSearchState(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	model.size = TerminalSize{Width: normalWidth, Height: 24, Known: true}
	_ = model.openFind(findCurrent)
	for _, character := range []rune{'\u202e', '\u2066'} {
		for _, input := range []struct {
			name    string
			message tea.Msg
		}{
			{name: "paste", message: tea.PasteMsg{Content: string(character)}},
			{
				name: "key",
				message: tea.KeyPressMsg(tea.Key{
					Code: character,
					Text: string(character),
				}),
			},
		} {
			t.Run(input.name+"/"+string(character), func(t *testing.T) {
				model.searchInput.SetValue("safe")
				model.searchInput.SetCursor(2)
				operation, _ := review.NewOperationID(900)
				model.find.phase = findSearching
				model.find.generation = 17
				model.find.failure = "prior failure"
				model.find.submittedQuery = "prior query"
				model.find.searchOperation = operation
				model.find.fileResults = []fileFindResult{{file: model.fileOrder[0], score: 3, order: 2}}
				model.find.fileBuild = &fileFindBuild{
					query: "prior", best: fileFindHeap{{file: model.fileOrder[0], score: 4}},
					generation: 16, position: 3, matches: 2,
				}
				model.foreground.search.active = operation
				beforeFind := cloneFindPaletteForBidiTest(model.find)
				beforeForeground := model.foreground
				beforeReview := model.review

				_ = model.updateFindInput(input.message)
				if value := model.searchInput.Value(); value != "safe" {
					t.Fatalf("find value = %q, want safe", value)
				}
				if position := model.searchInput.Position(); position != 2 {
					t.Fatalf("find cursor = %d, want 2", position)
				}
				if !reflect.DeepEqual(model.find, beforeFind) {
					t.Fatalf("find palette changed: before %#v after %#v", beforeFind, model.find)
				}
				if !reflect.DeepEqual(model.foreground, beforeForeground) {
					t.Fatalf("foreground changed: before %#v after %#v", beforeForeground, model.foreground)
				}
				if !reflect.DeepEqual(model.review, beforeReview) {
					t.Fatal("find rejection changed review state")
				}
				if model.notice == nil || model.notice.Kind != NoticeWarning ||
					model.notice.Text != "find query contains bidirectional control" {
					t.Fatalf("find notice = %+v", model.notice)
				}
				if view := model.View().Content; strings.ContainsRune(view, character) {
					t.Fatalf("find view contains raw bidirectional control %U", character)
				}
			})
		}
	}
}

func TestPromptByteLimitPrecedesBidirectionalControlFailure(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	t.Run("command", func(t *testing.T) {
		_ = model.beginCommandPrompt()
		previous := strings.Repeat("a", maxPromptBytes-1)
		model.commandInput.SetValue(previous)

		_ = model.updatePromptInput(tea.PasteMsg{Content: "\u202e"})
		if model.commandInput.Value() != previous || model.notice == nil ||
			model.notice.Text != "prompt input limit reached: 65536 bytes" {
			t.Fatalf("command result bytes=%d notice=%+v", len(model.commandInput.Value()), model.notice)
		}
	})

	t.Run("find", func(t *testing.T) {
		_ = model.openFind(findCurrent)
		previous := strings.Repeat("a", maxPromptBytes-1)
		model.searchInput.SetValue(previous)

		_ = model.updateFindInput(tea.PasteMsg{Content: "\u202e"})
		if model.searchInput.Value() != previous || model.find.phase != findFailed ||
			model.find.failure != "find query exceeds 65536 bytes" {
			t.Fatalf(
				"find result bytes=%d phase=%v failure=%q",
				len(model.searchInput.Value()), model.find.phase, model.find.failure,
			)
		}
	})
}

func TestPromptInputsPreserveOrdinaryUnicode(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	_ = model.beginCommandPrompt()
	_ = model.updatePromptInput(tea.PasteMsg{Content: "界"})
	if model.commandInput.Value() != "界" {
		t.Fatalf("command value = %q", model.commandInput.Value())
	}

	_ = model.openFind(findCurrent)
	_ = model.updateFindInput(tea.PasteMsg{Content: "界"})
	if model.searchInput.Value() != "界" {
		t.Fatalf("find value = %q", model.searchInput.Value())
	}
}

func cloneFindPaletteForBidiTest(value findPalette) findPalette {
	value.candidates = slices.Clone(value.candidates)
	value.fileResults = slices.Clone(value.fileResults)
	if value.fileBuild != nil {
		build := *value.fileBuild
		build.best = slices.Clone(build.best)
		value.fileBuild = new(build)
	}
	return value
}
