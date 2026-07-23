package app

import (
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jacoelho/rvw/internal/review"
)

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

func sideBySideCells(t *testing.T, row string) (string, string) {
	t.Helper()
	left, right, ok := strings.Cut(ansi.Strip(row), "│")
	if !ok {
		t.Fatalf("side-by-side row omitted separator: %q", row)
	}
	return left, right
}
