package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
)

func TestDefaultFindShortcutsOpenTheirScopes(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	setModelKeybindings(t, model,
		keymap.Binding{Context: keymap.ContextNormal, Chord: "/", Action: keymap.ActionSearch},
		keymap.Binding{Context: keymap.ContextNormal, Chord: "g /", Action: keymap.ActionSearchVisible},
		keymap.Binding{Context: keymap.ContextNormal, Chord: "ctrl-p", Action: keymap.ActionFindFile},
	)

	_, _ = model.Update(keyMessage("/"))
	assertFindScope(t, model, findCurrent)
	model.closeFind(findCloseCancel)

	_, _ = model.Update(keyMessage("g"))
	_, _ = model.Update(keyMessage("/"))
	assertFindScope(t, model, findReview)
	model.closeFind(findCloseCancel)

	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'p', Mod: tea.ModCtrl}))
	assertFindScope(t, model, findFiles)
}

func TestFindFooterUsesEffectiveBindings(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	setModelKeybindings(t, model,
		keymap.Binding{Context: keymap.ContextSearch, Chord: "ctrl-a", Action: keymap.ActionAccept},
		keymap.Binding{Context: keymap.ContextSearch, Chord: "ctrl-x", Action: keymap.ActionCancel},
		keymap.Binding{Context: keymap.ContextSearch, Chord: "ctrl-t", Action: keymap.ActionNextSearchScope},
		keymap.Binding{Context: keymap.ContextSearch, Chord: "ctrl-n", Action: keymap.ActionMoveDown},
	)
	_ = model.openFind(findFiles)
	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 18})

	plain := ansi.Strip(model.View().Content)
	for _, want := range []string{"Ctrl-X cancel", "Ctrl-A open file", "Ctrl-T next search scope"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("find footer omitted remapped binding %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "Tab scope") || strings.Contains(plain, "Esc close") {
		t.Fatalf("find footer retained hard-coded bindings:\n%s", plain)
	}

	_, _ = model.Update(tea.WindowSizeMsg{Width: minimumWidth, Height: minimumHeight})
	plain = ansi.Strip(model.View().Content)
	if !strings.Contains(plain, "Ctrl-X cancel") {
		t.Fatalf("minimum find modal hid cancel:\n%s", plain)
	}

	model.closeFind(findCloseCancel)
	_ = model.openFind(findCurrent)
	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 18})
	plain = ansi.Strip(model.View().Content)
	if !strings.Contains(plain, "Ctrl-A search") {
		t.Fatalf("prompt-stage find footer hid search action:\n%s", plain)
	}
}

func TestOpenFindBorrowsImmutableSortedFileOrder(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "one\n",
		"beta.txt":  "two\n",
	})
	settleModelWork(t, model, coordinator)
	_ = model.openFind(findFiles)
	if len(model.fileOrder) == 0 || len(model.find.candidates) == 0 ||
		&model.fileOrder[0] != &model.find.candidates[0] {
		t.Fatal("file find copied its immutable candidate order")
	}
	if !slices.IsSorted(model.find.candidates) {
		t.Fatal("file find candidates are not sorted by file identity")
	}
}

func TestFindScopeSwitchPreservesQuery(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	_ = model.openFind(findCurrent)
	model.searchInput.SetValue("needle")

	model.switchFindScope(1)
	if model.find.scope != findReview || model.searchInput.Value() != "needle" {
		t.Fatalf("review scope=%v query=%q", model.find.scope, model.searchInput.Value())
	}
	model.switchFindScope(1)
	if model.find.scope != findFiles || model.searchInput.Value() != "needle" {
		t.Fatalf("file scope=%v query=%q", model.find.scope, model.searchInput.Value())
	}
	model.switchFindScope(1)
	if model.find.scope != findCurrent || model.searchInput.Value() != "needle" {
		t.Fatalf("wrapped scope=%v query=%q", model.find.scope, model.searchInput.Value())
	}
}

func TestFindRejectsInvalidUTF8Transactionally(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	_ = model.openFind(findCurrent)
	model.searchInput.SetValue("valid")
	position := model.searchInput.Position()

	_ = model.updateFindInput(tea.PasteMsg{Content: string([]byte{0xff})})
	if value := model.searchInput.Value(); value != "valid" {
		t.Fatalf("invalid paste changed query to %q", value)
	}
	if model.searchInput.Position() != position {
		t.Fatalf("invalid paste moved cursor to %d, want %d", model.searchInput.Position(), position)
	}
	if model.find.phase != findFailed || model.find.failure != "find query is not valid UTF-8" {
		t.Fatalf("invalid paste state phase=%v failure=%q", model.find.phase, model.find.failure)
	}
	if status := model.findStatusText(); status != "Error: find query is not valid UTF-8" {
		t.Fatalf("invalid paste status = %q", status)
	}
}

func TestFindStatusReportsPositionVisibleCountAndLimitedTotal(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	model.searchInput.SetValue("a")
	model.find = findPalette{
		scope:        findFiles,
		phase:        findReady,
		cursor:       1,
		fileResults:  make([]fileFindResult, 3),
		limited:      true,
		totalMatches: 12,
	}
	if got := model.findStatusText(); got != "2 of 3 · 12 total" {
		t.Fatalf("limited file find status = %q", got)
	}
	model.find.cursor = 99
	model.find.limited = false
	if got := model.findStatusText(); got != "3 of 3" {
		t.Fatalf("clamped file find status = %q", got)
	}
	model.find.fileResults = nil
	if got := model.findStatusText(); got != "0 of 0" {
		t.Fatalf("empty file find status = %q", got)
	}
}

func TestReviewFindSearchesOnlyCapturedVisibleProjection(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "visible only\n",
		"beta.txt":  "hidden needle\n",
	})
	settleModelWork(t, model, coordinator)
	alpha := findTestFile(t, model, "alpha.txt")
	model.fileOrder = []review.FileID{alpha}
	_ = model.openFind(findReview)
	model.searchInput.SetValue("needle")
	model.submitFindText()
	settleModelWork(t, model, coordinator)

	summary := model.review.SearchSummary()
	if summary.Status != review.SearchComplete || summary.HitCount != 0 {
		t.Fatalf("visible search summary = %+v", summary)
	}
	if model.find.phase != findReady {
		t.Fatalf("find phase = %v, want ready", model.find.phase)
	}
}

func TestFileFindRanksInBoundedGenerationFencedChunks(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt":   "changed\n",
		"beta.txt":    "changed\n",
		"beta-doc.md": "changed\n",
	})
	settleModelWork(t, model, coordinator)
	_ = model.openFind(findFiles)
	model.searchInput.SetValue("bt")
	_ = model.scheduleFileFind()
	generation := model.find.generation

	if command := model.beginFileFind(findDebounceMsg{generation: generation - 1}); command != nil {
		t.Fatal("stale debounce started matching")
	}
	command := model.beginFileFind(findDebounceMsg{generation: generation})
	for command != nil {
		raw := command()
		message, ok := raw.(findChunkMsg)
		if !ok {
			t.Fatalf("file matcher command returned %T", raw)
		}
		command = model.advanceFileFind(message)
	}
	if model.find.phase != findReady || model.findResultCount() != 2 {
		t.Fatalf(
			"file match phase=%v count=%d total=%d",
			model.find.phase,
			model.findResultCount(),
			model.find.totalMatches,
		)
	}
	paths := make([]string, 0, 2)
	for index := range 2 {
		fileID, _ := model.findFileAt(index)
		file, _ := model.review.Snapshot().File(fileID)
		paths = append(paths, file.DisplayPath())
	}
	if !slices.Contains(paths, "beta.txt") || !slices.Contains(paths, "beta-doc.md") {
		t.Fatalf("fuzzy matches = %v", paths)
	}
}

func TestFileFindScoringIsCaseInsensitiveAndPrefersPathBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		query string
		path  string
		match bool
	}{
		{query: "rdm", path: "README.md", match: true},
		{query: "ång", path: "Ångström.go", match: true},
		{query: "xyz", path: "README.md"},
	} {
		_, matched := scoreFileFind(testCase.query, testCase.path)
		if matched != testCase.match {
			t.Errorf("scoreFileFind(%q, %q) matched=%v, want %v", testCase.query, testCase.path, matched, testCase.match)
		}
	}
	boundary, ok := scoreFileFind("fb", "foo/bar.go")
	if !ok {
		t.Fatal("boundary candidate did not match")
	}
	plain, ok := scoreFileFind("fb", "fizzbuzz.go")
	if !ok {
		t.Fatal("plain candidate did not match")
	}
	if boundary <= plain {
		t.Fatalf("boundary score=%d, want greater than plain score=%d", boundary, plain)
	}
}

func TestFindPaletteCompositesWorkspaceAtNormalAndMinimumSizes(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	for _, size := range []TerminalSize{
		{Width: normalWidth, Height: 24, Known: true},
		{Width: minimumWidth, Height: minimumHeight, Known: true},
	} {
		model.size = size
		_ = model.openFind(findCurrent)
		plain := ansi.Strip(model.View().Content)
		if !strings.Contains(plain, "Find") || !strings.Contains(plain, "Current") {
			t.Fatalf("%dx%d find view lacks palette:\n%s", size.Width, size.Height, plain)
		}
		if lines := strings.Count(plain, "\n") + 1; lines != size.Height {
			t.Fatalf("%dx%d rendered %d lines", size.Width, size.Height, lines)
		}
		model.closeFind(findCloseCancel)
	}
}

func TestFindCaretUsesDialogInnerCoordinatesAcrossThreshold(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	model.searchInput.SetVirtualCursor(false)
	for _, size := range []TerminalSize{
		{Width: 47, Height: 12, Known: true},
		{Width: 48, Height: 12, Known: true},
		{Width: 49, Height: 12, Known: true},
		{Width: 48, Height: 13, Known: true},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			model.size = size
			_ = model.openFind(findCurrent)
			base := model.searchInput.Cursor()
			view := model.View()
			layout := newFindModalLayout(size.Width, size.Height)
			if base == nil || view.Cursor == nil {
				t.Fatalf("find cursor = base %+v view %+v", base, view.Cursor)
			}
			if view.Cursor.X != base.X+layout.inputX ||
				view.Cursor.Y != base.Y+layout.inputY {
				t.Fatalf(
					"find cursor = (%d,%d), want base (%d,%d) + input (%d,%d)",
					view.Cursor.X,
					view.Cursor.Y,
					base.X,
					base.Y,
					layout.inputX,
					layout.inputY,
				)
			}
			model.closeFind(findCloseCancel)
		})
	}
}

func TestFindMouseMappingRejectsBorderAndOutsideClicks(t *testing.T) {
	for _, size := range []TerminalSize{
		{Width: 47, Height: 12, Known: true},
		{Width: 48, Height: 12, Known: true},
		{Width: 49, Height: 12, Known: true},
		{Width: 48, Height: 13, Known: true},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			layout := newFindModalLayout(size.Width, size.Height)
			hits := mouseHitMap{
				findRows:       []int{7},
				findX:          layout.dialog.inner.x,
				findY:          layout.dialog.inner.y,
				findWidth:      layout.dialog.inner.width,
				findHeight:     layout.dialog.inner.height,
				findResultY:    layout.resultY,
				findGeneration: 1,
			}
			click := hits.findCommand(tea.MouseClickMsg{
				X:      layout.dialog.inner.x,
				Y:      layout.resultY,
				Button: tea.MouseLeft,
			})
			if click == nil {
				t.Fatal("result click returned no command")
			}
			action, ok := click().(mouseActionMsg)
			if !ok || action.target != mouseTargetFind || action.findIndex != 7 {
				t.Fatalf("result click translated to %#v", action)
			}
			if layout.dialog.mode != dialogGeometryBordered {
				return
			}
			for _, point := range []struct{ x, y int }{
				{x: layout.dialog.outer.x, y: layout.resultY},
				{x: layout.dialog.outer.x + layout.dialog.outer.width - 1, y: layout.resultY},
				{x: layout.dialog.inner.x, y: layout.dialog.outer.y},
				{x: 0, y: 0},
			} {
				if command := hits.findCommand(tea.MouseClickMsg{
					X: point.x, Y: point.y, Button: tea.MouseLeft,
				}); command != nil {
					t.Fatalf("inert click at (%d,%d) returned a command", point.x, point.y)
				}
			}
		})
	}
}

func TestFindResultEscapesHostileExcerptText(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "needle\x1b[31m\u202eafter\n",
	})
	settleModelWork(t, model, coordinator)
	_, _ = model.Update(tea.WindowSizeMsg{Width: normalWidth, Height: 24})
	_ = model.openFind(findCurrent)
	model.searchInput.SetValue("needle")
	model.submitFindText()
	settleModelWork(t, model, coordinator)

	plain := ansi.Strip(model.View().Content)
	if !strings.Contains(plain, `needle\x1B[31m\u{202E}after`) {
		t.Fatalf("find result did not visibly escape hostile text:\n%s", plain)
	}
}

func TestFindEscapeRestoresPreviewedNavigation(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "one\ntwo\nthree\n",
		"beta.txt":  "four\nfive\nsix\n",
	})
	settleModelWork(t, model, coordinator)
	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: minimumHeight})
	alpha := findTestFile(t, model, "alpha.txt")
	beta := findTestFile(t, model, "beta.txt")
	if active, _ := model.review.ActiveFile(); active != alpha {
		t.Fatalf("initial active file = %d, want alpha %d", active, alpha)
	}
	model.diffCursor = 2
	model.diffTop = 1
	model.diffLeft = 7
	originalTree := model.treeCursor
	_ = model.openFind(findFiles)
	model.find.cursor = slices.Index(model.find.candidates, beta)
	model.previewFindSelection(findSelectionPreview)
	settleModelWork(t, model, coordinator)
	if active, _ := model.review.ActiveFile(); active != beta {
		t.Fatalf("preview active file = %d, want beta %d", active, beta)
	}

	model.closeFind(findCloseCancel)
	settleModelWork(t, model, coordinator)
	active, _ := model.review.ActiveFile()
	if active != alpha || model.diffCursor != 2 || model.diffTop != 1 ||
		model.diffLeft != 7 || model.treeCursor != originalTree {
		t.Fatalf(
			"restored active=%d cursor=%d top=%d left=%d tree=%+v",
			active,
			model.diffCursor,
			model.diffTop,
			model.diffLeft,
			model.treeCursor,
		)
	}
}

func assertFindScope(t *testing.T, model *Model, scope findScope) {
	t.Helper()
	if model.overlay != OverlayFind || model.find.scope != scope || !model.searchInput.Focused() {
		t.Fatalf(
			"find overlay=%v scope=%v focused=%v",
			model.overlay,
			model.find.scope,
			model.searchInput.Focused(),
		)
	}
}

func findTestFile(t *testing.T, model *Model, path string) review.FileID {
	t.Helper()
	for file := range model.review.Snapshot().AllFiles() {
		if file.DisplayPath() == path {
			return file.ID()
		}
	}
	t.Fatalf("file %q is missing", path)
	return 0
}
