package app

import (
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
)

func TestSideBySideWidthsFollowChangeKind(t *testing.T) {
	for _, test := range []struct {
		kind        review.ChangeKind
		left, right int
	}{
		{kind: review.ChangeAdded, left: 39, right: 80},
		{kind: review.ChangeDeleted, left: 79, right: 40},
		{kind: review.ChangeModified, left: 59, right: 60},
	} {
		left, right := sideBySideWidths(test.kind, 120)
		if left != test.left || right != test.right {
			t.Fatalf("%s widths = %d/%d, want %d/%d", test.kind, left, right, test.left, test.right)
		}
	}
}

func TestPatchRecoveryLineKeepsStateAndActionAheadOfDetail(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	for _, test := range []struct {
		state string
		width int
	}{
		{state: "Failed", width: 20},
		{state: "Limited", width: 20},
		{state: "Failed", width: 48},
	} {
		got := model.patchRecoveryLine(test.state, strings.Repeat("detail ", 20), test.width)
		if lipgloss.Width(got) > test.width || !strings.Contains(got, test.state+"; Enter retry") {
			t.Fatalf("recovery line width %d = %q", test.width, got)
		}
	}
	setModelKeybindings(t, model,
		keymap.Binding{
			Context: keymap.ContextNormal,
			Focus:   keymap.FocusDiff,
			Chord:   "g r",
			Action:  keymap.ActionActivate,
		},
		keymap.Binding{
			Context: keymap.ContextNormal,
			Focus:   keymap.FocusFileTree,
			Chord:   "g r",
			Action:  keymap.ActionToggleDirectory,
		},
		keymap.Binding{
			Context: keymap.ContextNormal,
			Focus:   keymap.FocusComments,
			Chord:   "g r",
			Action:  keymap.ActionEditComment,
		},
	)
	if got := model.patchRecoveryLine("Failed", "detail", 48); !strings.Contains(got, "g r retry") {
		t.Fatalf("remapped recovery = %q", got)
	}
	for _, focus := range []FocusOwner{FocusFileTree, FocusComments} {
		model.focus = focus
		got := model.patchRecoveryLine("Failed", "detail", 64)
		if !strings.Contains(got, "focus diff, then g r retry") {
			t.Fatalf("focus %d recovery = %q", focus, got)
		}
	}
	model.focus = FocusDiff
	model.mode = ModeVisual
	if got := model.patchRecoveryLine("Failed", "detail", 72); !strings.Contains(
		got,
		"leave visual mode, then g r retry",
	) {
		t.Fatalf("visual-mode recovery = %q", got)
	}
	model.mode = ModeNormal
	setModelKeybindings(t, model, keymap.Binding{
		Context: keymap.ContextNormal,
		Chord:   "?",
		Action:  keymap.ActionHelp,
	})
	model.focus = FocusFileTree
	if got := model.patchRecoveryLine("Failed", "detail", 48); !strings.Contains(got, "no retry") ||
		strings.Contains(got, "focus diff") {
		t.Fatalf("unbound recovery = %q", got)
	}
}

func TestCancelledPatchUsesCompletedOutcomeLabel(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitForPatch(t, model, coordinator, active)
	current := model.review.DropPatch(active)
	pending, request, err := current.RequestPatch(active, review.WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	current, err = pending.ApplyPatch(review.PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    review.PatchCancelled,
	})
	if err != nil {
		t.Fatal(err)
	}
	model.review = current
	lines, _ := model.diffLines(48, 4, newSemanticStyles(model.settings))
	got := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(got, "Cancelled;") || strings.Contains(got, "Cancel;") {
		t.Fatalf("cancelled patch = %q", got)
	}
}

func TestDiffCellSelectionMapsEveryPresentationState(t *testing.T) {
	model, active := modelWithPreparedPatch(t,
		"@@ -1,3 +1,3 @@\n"+
			" first\n"+
			" second\n"+
			" third\n",
	)
	lines := model.review.PatchLines(active, 0, 10, 4_096)
	first := lines[1]
	second := lines[2]
	third := lines[3]

	model.mode = ModeNormal
	model.focus = FocusDiff
	model.diffCursor = second.Number
	if got := model.diffCellSelection(second); got != diffCellSelectionActive {
		t.Fatalf("focused cursor selection = %v, want active", got)
	}
	if got := model.diffCellSelection(first); got != diffCellSelectionNone {
		t.Fatalf("ordinary row selection = %v, want none", got)
	}
	model.focus = FocusFileTree
	if got := model.diffCellSelection(second); got != diffCellSelectionRetained {
		t.Fatalf("unfocused cursor selection = %v, want retained", got)
	}

	model.mode = ModeVisual
	model.focus = FocusDiff
	model.visualAnchor = first.Number
	model.diffCursor = third.Number
	if got := model.diffCellSelection(first); got != diffCellSelectionRetained {
		t.Fatalf("visual anchor selection = %v, want retained", got)
	}
	if got := model.diffCellSelection(second); got != diffCellSelectionRange {
		t.Fatalf("visual interior selection = %v, want range", got)
	}
	if got := model.diffCellSelection(third); got != diffCellSelectionActive {
		t.Fatalf("visual cursor selection = %v, want active", got)
	}
	model.overlay = OverlayHelp
	if got := model.diffCellSelection(third); got != diffCellSelectionRetained {
		t.Fatalf("cursor under overlay selection = %v, want retained", got)
	}
}

func TestVisualRangeMarkersCoverWrappedSegmentsWithoutConsumingSourceGlyphs(t *testing.T) {
	model, active := modelWithPreparedPatch(t,
		"@@ -1,3 +1,3 @@\n"+
			" literal > and │ remain source text\n"+
			" interior range content is deliberately long enough to wrap across multiple terminal rows\n"+
			" active endpoint content is deliberately long enough to wrap across multiple terminal rows\n",
	)
	lines := model.review.PatchLines(active, 0, 10, 16_384)
	anchor := lines[1].Number
	interior := lines[2].Number
	cursor := lines[3].Number
	model.mode = ModeVisual
	model.focus = FocusDiff
	model.visualAnchor = anchor
	model.diffCursor = cursor
	model.settings.wrap = true

	for _, test := range []struct {
		name  string
		view  DiffView
		width int
	}{
		{name: "unified", view: DiffViewUnified, width: 24},
		{name: "side by side", view: DiffViewSideBySide, width: sideBySideContentWidth + diffCommentAnchorColumns},
	} {
		t.Run(test.name, func(t *testing.T) {
			model.settings.diffView = test.view
			rendered, rows := model.diffLines(test.width, 20, newSemanticStyles(model.settings))
			anchorSegments := 0
			interiorSegments := 0
			cursorSegments := 0
			for index, row := range rows {
				plain := ansi.Strip(rendered[index])
				switch row {
				case anchor:
					anchorSegments++
					if anchorSegments == 1 && !strings.HasPrefix(plain, ">") {
						t.Fatalf("anchor first segment marker = %q", plain)
					}
					if anchorSegments > 1 && strings.HasPrefix(plain, ">") {
						t.Fatalf("anchor continuation repeated cursor marker = %q", plain)
					}
				case interior:
					interiorSegments++
					if !strings.HasPrefix(plain, "│") {
						t.Fatalf("range segment omitted rail = %q", plain)
					}
				case cursor:
					cursorSegments++
					if cursorSegments == 1 && !strings.HasPrefix(plain, ">") {
						t.Fatalf("cursor first segment marker = %q", plain)
					}
					if cursorSegments > 1 && strings.HasPrefix(plain, ">") {
						t.Fatalf("cursor continuation repeated marker = %q", plain)
					}
				}
			}
			if anchorSegments == 0 || interiorSegments < 2 || cursorSegments < 2 {
				t.Fatalf(
					"wrapped segment counts = anchor %d interior %d cursor %d; rows=%v",
					anchorSegments,
					interiorSegments,
					cursorSegments,
					rows,
				)
			}
		})
	}
	model.mode = ModeNormal
	model.settings.wrap = false
	model.settings.diffView = DiffViewUnified
	rendered, _ := model.diffLines(80, 10, newSemanticStyles(model.settings))
	content := ansi.Strip(strings.Join(rendered, "\n"))
	if !strings.Contains(content, "literal > and │ remain source text") {
		t.Fatalf("source glyphs were normalized as presentation markers:\n%s", content)
	}
}

func TestSideBySidePatchStripsOnlyTheOwnedKindPrefix(t *testing.T) {
	model, active := modelWithPreparedPatch(t,
		"@@ -1,4 +1,4 @@\n"+
			" +context\n"+
			" -context\n"+
			"-+old\n"+
			"--old\n"+
			"++new\n"+
			"+-new\n",
	)
	model.settings.wrap = false
	const width = 120
	lines := model.review.PatchLines(active, 0, 20, 4_096)
	rendered, rows := model.sideBySideDiffLines(
		active,
		lines,
		width,
		20,
		newDiffLineNumberWidths(model.review.Patch(active)),
		newSemanticStyles(model.settings),
	)
	if want := []uint32{0, 1, 2, 3, 4, 5, 6}; !slices.Equal(rows, want) {
		t.Fatalf("side-by-side row mapping = %v, want %v", rows, want)
	}
	for index, row := range rendered {
		if got := lipgloss.Width(row); got != width {
			t.Fatalf("rendered row %d width = %d, want %d", index, got, width)
		}
	}
	for _, test := range []struct {
		content string
		row     uint32
		left    bool
		right   bool
	}{
		{row: 1, content: "+context", left: true, right: true},
		{row: 2, content: "-context", left: true, right: true},
		{row: 3, content: "+old", left: true},
		{row: 4, content: "-old", left: true},
		{row: 5, content: "+new", right: true},
		{row: 6, content: "-new", right: true},
	} {
		left, right := sideBySideCells(t, rendered[rowIndex(t, rows, test.row)])
		if strings.Contains(left, test.content) != test.left ||
			strings.Contains(right, test.content) != test.right {
			t.Fatalf(
				"patch row %d content %q sides = (%q, %q), want left %t right %t",
				test.row,
				test.content,
				left,
				right,
				test.left,
				test.right,
			)
		}
	}
}

func TestSideBySideNoNewlineMarkersRenderOnlyOnTheirOwnedSides(t *testing.T) {
	model, active := modelWithPreparedPatch(t,
		"@@ -1 +1 @@\n"+
			"-old\n"+
			"\\ No newline at end of file\n"+
			"+new\n"+
			"\\ No newline at end of file\n"+
			"@@ -3 +3 @@\n"+
			" same\n"+
			"\\ No newline at end of file\n",
	)
	model.settings.wrap = false
	lines := model.review.PatchLines(active, 0, 20, 4_096)
	rendered, rows := model.sideBySideDiffLines(
		active,
		lines,
		100,
		20,
		newDiffLineNumberWidths(model.review.Patch(active)),
		newSemanticStyles(model.settings),
	)
	for _, test := range []struct {
		row     uint32
		oldSide bool
		newSide bool
	}{
		{row: 2, oldSide: true},
		{row: 4, newSide: true},
		{row: 7, oldSide: true, newSide: true},
	} {
		left, right := sideBySideCells(t, rendered[rowIndex(t, rows, test.row)])
		if strings.Contains(left, noNewlineMarkerTextForTest) != test.oldSide ||
			strings.Contains(right, noNewlineMarkerTextForTest) != test.newSide {
			t.Fatalf(
				"marker row %d sides = (%q, %q), want old %t new %t",
				test.row,
				left,
				right,
				test.oldSide,
				test.newSide,
			)
		}
	}

	marker := model.review.PatchLines(active, 2, 1, 4_096)
	direct, directRows := model.sideBySideDiffLines(
		active,
		marker,
		100,
		1,
		newDiffLineNumberWidths(model.review.Patch(active)),
		newSemanticStyles(model.settings),
	)
	if len(direct) != 1 || !slices.Equal(directRows, []uint32{2}) {
		t.Fatalf("direct marker viewport = %d rows %v", len(direct), directRows)
	}
	left, right := sideBySideCells(t, direct[0])
	if !strings.Contains(left, noNewlineMarkerTextForTest) ||
		strings.Contains(right, noNewlineMarkerTextForTest) {
		t.Fatalf("direct old marker sides = (%q, %q)", left, right)
	}
}

func TestSideBySideSemanticBackgroundsStayOnApplicableCells(t *testing.T) {
	model, active := modelWithPreparedPatch(t,
		"@@ -1 +1 @@\n"+
			"-old\n"+
			"+new\n",
	)
	palette, err := NewPalette(map[PaletteRole]string{
		PalettePanelBackground:      "#111111",
		PaletteDiffAdd:              "#00ff00",
		PaletteDiffDelete:           "#ff0000",
		PaletteDiffAddBackground:    "#002200",
		PaletteDiffDeleteBackground: "#220000",
		PaletteStatusBackground:     "#333333",
		PaletteCursorBackground:     "#444444",
	})
	if err != nil {
		t.Fatal(err)
	}
	model.settings.colorEnabled = true
	model.settings.palette = palette
	model.diffCursor = 99
	lines := model.review.PatchLines(active, 0, 10, 4_096)
	rendered, rows := model.sideBySideDiffLines(
		active,
		lines,
		100,
		10,
		newDiffLineNumberWidths(model.review.Patch(active)),
		newSemanticStyles(model.settings),
	)
	terminal := vt.NewEmulator(100, len(rendered))
	t.Cleanup(func() { _ = terminal.Close() })
	if _, err := terminal.WriteString(strings.Join(rendered, "\r\n")); err != nil {
		t.Fatal(err)
	}
	deletionRow := rowIndex(t, rows, 1)
	additionRow := rowIndex(t, rows, 2)
	hunkRow := rowIndex(t, rows, 0)
	for _, column := range []int{0, 10, 49, 50, 60, 99} {
		if got := terminalColor(terminal.CellAt(column, hunkRow).Style.Bg); got != "#333333" {
			t.Fatalf("hunk band cell (%d,%d) background = %q", column, hunkRow, got)
		}
	}
	if got := terminalColor(terminal.CellAt(10, deletionRow).Style.Bg); got != "#220000" {
		t.Fatalf("deletion old-side background = %q", got)
	}
	if got := terminalColor(terminal.CellAt(60, deletionRow).Style.Bg); got != "#111111" {
		t.Fatalf("deletion neutral new-side background = %q", got)
	}
	if got := terminalColor(terminal.CellAt(10, additionRow).Style.Bg); got != "#111111" {
		t.Fatalf("addition neutral old-side background = %q", got)
	}
	if got := terminalColor(terminal.CellAt(60, additionRow).Style.Bg); got != "#002200" {
		t.Fatalf("addition new-side background = %q", got)
	}
	for _, row := range []int{deletionRow, additionRow} {
		for _, column := range []int{0, 1, 49} {
			if got := terminalColor(terminal.CellAt(column, row).Style.Bg); got != "#111111" {
				t.Fatalf("side-by-side chrome cell (%d,%d) background = %q, want panel", column, row, got)
			}
		}
	}
	if got := terminalColor(terminal.CellAt(51, deletionRow).Style.Bg); got != "#111111" {
		t.Fatalf("deletion absent new-side anchor background = %q, want panel", got)
	}
	if got := terminalColor(terminal.CellAt(3, additionRow).Style.Bg); got != "#111111" {
		t.Fatalf("addition absent old-side anchor background = %q, want panel", got)
	}

	model.diffCursor = 1
	selectedRendered, selectedRows := model.sideBySideDiffLines(
		active,
		lines,
		100,
		10,
		newDiffLineNumberWidths(model.review.Patch(active)),
		newSemanticStyles(model.settings),
	)
	selectedTerminal := vt.NewEmulator(100, len(selectedRendered))
	t.Cleanup(func() { _ = selectedTerminal.Close() })
	if _, err := selectedTerminal.WriteString(strings.Join(selectedRendered, "\r\n")); err != nil {
		t.Fatal(err)
	}
	selectedDeletion := rowIndex(t, selectedRows, 1)
	for _, column := range []int{0, 10, 49, 60, 99} {
		if got := terminalColor(selectedTerminal.CellAt(column, selectedDeletion).Style.Bg); got != "#444444" {
			t.Fatalf("selected split cell (%d,%d) background = %q", column, selectedDeletion, got)
		}
	}

	model.diffCursor = 99
	model.settings.diffView = DiffViewUnified
	unified, unifiedRows := model.diffLines(100, 10, newSemanticStyles(model.settings))
	unifiedTerminal := vt.NewEmulator(100, len(unified))
	t.Cleanup(func() { _ = unifiedTerminal.Close() })
	if _, err := unifiedTerminal.WriteString(strings.Join(unified, "\r\n")); err != nil {
		t.Fatal(err)
	}
	unifiedDeletion := rowIndex(t, unifiedRows, 1)
	unifiedAddition := rowIndex(t, unifiedRows, 2)
	if got := terminalColor(unifiedTerminal.CellAt(6, unifiedDeletion).Style.Bg); got != "#220000" {
		t.Fatalf("unified deletion absent new anchor background = %q", got)
	}
	if got := terminalColor(unifiedTerminal.CellAt(3, unifiedAddition).Style.Bg); got != "#002200" {
		t.Fatalf("unified addition absent old anchor background = %q", got)
	}
}

func TestUnifiedMetadataAndSyntaxRetainTheirCellBases(t *testing.T) {
	model, active := modelWithPreparedPatch(t,
		"diff --git a/alpha.txt b/alpha.txt\n"+
			"index 1111111..2222222 100644\n"+
			"--- a/alpha.txt\n"+
			"+++ b/alpha.txt\n"+
			"@@ -0,0 +1 @@\n"+
			"+func value\n",
	)
	palette, err := NewPalette(map[PaletteRole]string{
		PalettePanelBackground:   "#111111",
		PalettePrimary:           "#ffffff",
		PaletteDim:               "#888888",
		PaletteCursorBackground:  "#333333",
		PaletteDiffAdd:           "#00ff00",
		PaletteDiffAddBackground: "#002200",
		PaletteSyntaxKeyword:     "#0000ff",
	})
	if err != nil {
		t.Fatal(err)
	}
	selector, err := review.NewSyntaxSelector("go")
	if err != nil {
		t.Fatal(err)
	}
	object, err := review.NewObjectID(strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := review.NewSyntaxIdentity(object, selector)
	if err != nil {
		t.Fatal(err)
	}
	lineOne, err := review.NewLineNumber(1)
	if err != nil {
		t.Fatal(err)
	}
	model.syntaxNew, err = review.PrepareSyntax(
		identity,
		[]review.LineNumber{lineOne},
		[]byte("func value\n"),
	)
	if err != nil {
		t.Fatal(err)
	}
	model.settings.colorEnabled = true
	model.settings.palette = palette
	model.settings.wrap = false
	model.settings.diffView = DiffViewUnified
	model.diffCursor = 99
	const width = 50
	lines := model.review.PatchLines(active, 0, 20, 4_096)
	rendered, rows := model.diffLines(width, 20, newSemanticStyles(model.settings))
	terminal := vt.NewEmulator(width, len(rendered))
	t.Cleanup(func() { _ = terminal.Close() })
	if _, err := terminal.WriteString(strings.Join(rendered, "\r\n")); err != nil {
		t.Fatal(err)
	}
	metadataRow := rowIndex(t, rows, patchRowContaining(t, lines, "+++ b/alpha.txt"))
	assertVTCellStyleAtRow(t, terminal, 8, metadataRow, "#ffffff", "#111111", 1)
	assertVTCellStyleAtRow(t, terminal, width-1, metadataRow, "#ffffff", "#111111", 0)

	additionRow := rowIndex(t, rows, patchRowContaining(t, lines, "+func value"))
	assertVTCellStyleAtRow(t, terminal, 9, additionRow, "#0000ff", "#002200", 1)
	assertVTCellStyleAtRow(t, terminal, 14, additionRow, "#00ff00", "#002200", 0)
	assertVTCellStyleAtRow(t, terminal, width-1, additionRow, "#ffffff", "#002200", 0)

	model.diffCursor = patchRowContaining(t, lines, "+func value")
	selected, selectedRows := model.diffLines(width, 20, newSemanticStyles(model.settings))
	selectedTerminal := vt.NewEmulator(width, len(selected))
	t.Cleanup(func() { _ = selectedTerminal.Close() })
	if _, err := selectedTerminal.WriteString(strings.Join(selected, "\r\n")); err != nil {
		t.Fatal(err)
	}
	selectedAddition := rowIndex(t, selectedRows, model.diffCursor)
	assertVTCellStyleAtRow(t, selectedTerminal, 9, selectedAddition, "#ffffff", "#333333", 1)
}

func TestUnifiedWrappedContinuationKeepsSemanticGutterBackground(t *testing.T) {
	model, active := modelWithPreparedPatch(t,
		"@@ -0,0 +1 @@\n"+
			"+this addition is deliberately long enough to wrap across several terminal rows\n",
	)
	palette, err := NewPalette(map[PaletteRole]string{
		PalettePanelBackground:   "#111111",
		PaletteDiffAdd:           "#00ff00",
		PaletteDiffAddBackground: "#002200",
	})
	if err != nil {
		t.Fatal(err)
	}
	model.settings.colorEnabled = true
	model.settings.palette = palette
	model.settings.wrap = true
	model.settings.diffView = DiffViewUnified
	model.diffCursor = 99
	rendered, rows := model.diffLines(24, 10, newSemanticStyles(model.settings))
	addition := model.review.PatchLines(active, 0, 10, 4_096)[1].Number
	indices := make([]int, 0, 2)
	for index, row := range rows {
		if row == addition {
			indices = append(indices, index)
		}
	}
	if len(indices) < 2 {
		t.Fatalf("wrapped addition produced row mapping %v", rows)
	}
	terminal := vt.NewEmulator(24, len(rendered))
	t.Cleanup(func() { _ = terminal.Close() })
	if _, err := terminal.WriteString(strings.Join(rendered, "\r\n")); err != nil {
		t.Fatal(err)
	}
	continuation := indices[1]
	for _, column := range []int{0, 1} {
		if got := terminalColor(terminal.CellAt(column, continuation).Style.Bg); got != "#002200" {
			t.Fatalf("continuation gutter cell (%d,%d) background = %q", column, continuation, got)
		}
	}
}

func TestSideBySideNoNewlineMarkerWrapsWithinWidthAndHeight(t *testing.T) {
	model, active := modelWithPreparedPatch(t,
		"@@ -1 +1 @@\n"+
			"-old\n"+
			"\\ No newline at end of file\n"+
			"+new\n",
	)
	marker := model.review.PatchLines(active, 2, 1, 4_096)
	widths := newDiffLineNumberWidths(model.review.Patch(active))
	styles := newSemanticStyles(model.settings)
	const width = 40

	model.settings.wrap = true
	wrapped, wrappedRows := model.sideBySideDiffLines(
		active,
		marker,
		width,
		2,
		widths,
		styles,
	)
	if len(wrapped) != 2 || !slices.Equal(wrappedRows, []uint32{2, 2}) {
		t.Fatalf("wrapped marker = %d rows %v, want two mapped rows", len(wrapped), wrappedRows)
	}
	for index, row := range wrapped {
		if got := lipgloss.Width(row); got != width {
			t.Fatalf("wrapped marker row %d width = %d, want %d", index, got, width)
		}
	}
	truncated, truncatedRows := model.sideBySideDiffLines(
		active,
		marker,
		width,
		1,
		widths,
		styles,
	)
	if len(truncated) != 1 || !slices.Equal(truncatedRows, []uint32{2}) {
		t.Fatalf("height-truncated marker = %d rows %v", len(truncated), truncatedRows)
	}

	model.settings.wrap = false
	clipped, clippedRows := model.sideBySideDiffLines(
		active,
		marker,
		width,
		2,
		widths,
		styles,
	)
	if len(clipped) != 1 || !slices.Equal(clippedRows, []uint32{2}) ||
		lipgloss.Width(clipped[0]) != width {
		t.Fatalf(
			"clipped marker = %d rows %v width %d",
			len(clipped),
			clippedRows,
			lipgloss.Width(clipped[0]),
		)
	}
}

const noNewlineMarkerTextForTest = `\ No newline at end of file`

func modelWithPreparedPatch(t *testing.T, patchText string) (*Model, review.FileID) {
	t.Helper()
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitForPatch(t, model, coordinator, active)
	current := model.review.DropPatch(active)
	pending, request, err := current.RequestPatch(active, review.WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := review.TakePatch([]byte(patchText))
	if err != nil {
		t.Fatal(err)
	}
	current, err = pending.ApplyPatch(review.PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    review.PatchReady,
		Prepared:  prepared,
	})
	if err != nil {
		t.Fatal(err)
	}
	model.review = current
	model.settings.colorEnabled = false
	return model, active
}

func rowIndex(t *testing.T, rows []uint32, row uint32) int {
	t.Helper()
	index := slices.Index(rows, row)
	if index < 0 {
		t.Fatalf("rendered row mapping omitted patch row %d: %v", row, rows)
	}
	return index
}

func patchRowContaining(t *testing.T, lines []review.PatchLine, fragment string) uint32 {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(line.Text.String(), fragment) {
			return line.Number
		}
	}
	t.Fatalf("patch lines omitted %q", fragment)
	return 0
}

func assertVTCellStyleAtRow(
	t *testing.T,
	terminal *vt.Emulator,
	x, y int,
	foreground, background string,
	attributes uint8,
) {
	t.Helper()
	cell := terminal.CellAt(x, y)
	if cell == nil {
		t.Fatalf("cell (%d,%d) is absent", x, y)
	}
	if got := terminalColor(cell.Style.Fg); got != foreground {
		t.Fatalf("cell (%d,%d) foreground = %q, want %q", x, y, got, foreground)
	}
	if got := terminalColor(cell.Style.Bg); got != background {
		t.Fatalf("cell (%d,%d) background = %q, want %q", x, y, got, background)
	}
	if cell.Style.Attrs != attributes {
		t.Fatalf("cell (%d,%d) attributes = %08b, want %08b", x, y, cell.Style.Attrs, attributes)
	}
}

func sideBySideCells(t *testing.T, row string) (string, string) {
	t.Helper()
	left, right, ok := strings.Cut(ansi.Strip(row), "│")
	if !ok {
		t.Fatalf("side-by-side row omitted separator: %q", row)
	}
	return left, right
}
