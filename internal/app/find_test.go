package app

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jacoelho/rvw/internal/review"
)

func TestDefaultFindShortcutsOpenTheirScopes(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	setModelKeybindings(t, model,
		Keybinding{Context: KeyContextNormal, Chord: "/", Action: KeyActionSearch},
		Keybinding{Context: KeyContextNormal, Chord: "g /", Action: KeyActionSearchVisible},
		Keybinding{Context: KeyContextNormal, Chord: "Ctrl-p", Action: KeyActionFindFile},
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
