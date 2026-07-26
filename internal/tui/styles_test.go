package tui

import (
	"fmt"
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/jacoelho/rvw/internal/change"
)

func TestDiffCellStylesApplySemanticsOnlyToApplicableCells(t *testing.T) {
	styles := semanticStyles{
		panel:              lipgloss.NewStyle().Background(lipgloss.Color("#111111")),
		activeCursorRow:    lipgloss.NewStyle().Background(lipgloss.Color("#333333")),
		gutter:             lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")),
		added:              lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")),
		deleted:            lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")),
		additionBackground: lipgloss.NewStyle().Background(lipgloss.Color("#002200")),
		deletionBackground: lipgloss.NewStyle().Background(lipgloss.Color("#220000")),
	}
	tests := []struct {
		name       string
		state      diffCellState
		foreground string
		background string
	}{
		{
			name: "unified addition",
			state: diffCellState{
				kind:      change.PatchLineAddition,
				placement: diffCellUnified,
				selection: diffCellSelectionNone,
				colorMode: diffCellColor,
			},
			foreground: "#00ff00",
			background: "#002200",
		},
		{
			name: "old neutral addition",
			state: diffCellState{
				kind:      change.PatchLineAddition,
				placement: diffCellOldNeutral,
				selection: diffCellSelectionNone,
				colorMode: diffCellColor,
			},
			background: "#111111",
		},
		{
			name: "new applicable addition",
			state: diffCellState{
				kind:      change.PatchLineAddition,
				placement: diffCellNewApplicable,
				selection: diffCellSelectionNone,
				colorMode: diffCellColor,
			},
			foreground: "#00ff00",
			background: "#002200",
		},
		{
			name: "old applicable deletion",
			state: diffCellState{
				kind:      change.PatchLineDeletion,
				placement: diffCellOldApplicable,
				selection: diffCellSelectionNone,
				colorMode: diffCellColor,
			},
			foreground: "#ff0000",
			background: "#220000",
		},
		{
			name: "new neutral deletion",
			state: diffCellState{
				kind:      change.PatchLineDeletion,
				placement: diffCellNewNeutral,
				selection: diffCellSelectionNone,
				colorMode: diffCellColor,
			},
			background: "#111111",
		},
		{
			name: "selection overrides deletion background",
			state: diffCellState{
				kind:      change.PatchLineDeletion,
				placement: diffCellOldApplicable,
				selection: diffCellSelectionActive,
				colorMode: diffCellColor,
			},
			background: "#333333",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cell := diffCellStyles(test.state, styles)
			if got := styleColor(cell.content.GetForeground()); got != test.foreground {
				t.Fatalf("content foreground = %q, want %q", got, test.foreground)
			}
			if got := styleColor(cell.content.GetBackground()); got != test.background {
				t.Fatalf("content background = %q, want %q", got, test.background)
			}
			if got := styleColor(cell.gutter.GetBackground()); got != test.background {
				t.Fatalf("gutter background = %q, want %q", got, test.background)
			}
		})
	}
}

func TestDiffCellStylesRejectUnknownStateAsNeutral(t *testing.T) {
	styles := semanticStyles{
		activeCursorRow:    lipgloss.NewStyle().Reverse(true),
		added:              lipgloss.NewStyle().Bold(true),
		additionBackground: lipgloss.NewStyle().Background(lipgloss.Color("#002200")),
	}
	valid := diffCellState{
		kind:      change.PatchLineAddition,
		placement: diffCellUnified,
		selection: diffCellSelectionNone,
		colorMode: diffCellColor,
	}
	tests := []diffCellState{
		{kind: 0, placement: valid.placement, selection: valid.selection, colorMode: valid.colorMode},
		{kind: valid.kind, placement: 0, selection: valid.selection, colorMode: valid.colorMode},
		{kind: valid.kind, placement: valid.placement, selection: 0, colorMode: valid.colorMode},
		{kind: valid.kind, placement: valid.placement, selection: valid.selection, colorMode: 0},
	}
	for _, state := range tests {
		cell := diffCellStyles(state, styles)
		if cell.content.GetBold() || cell.content.GetReverse() ||
			styleColor(cell.content.GetBackground()) != "" ||
			styleColor(cell.content.GetForeground()) != "" {
			t.Fatalf("invalid state %+v produced non-neutral style %+v", state, cell)
		}
	}
}

func TestSemanticStylesPreserveOptionalDiffBackgroundContract(t *testing.T) {
	withoutBackground, err := NewPalette(map[PaletteRole]string{
		PaletteDiffAdd:    "#00ff00",
		PaletteDiffDelete: "#ff0000",
	})
	if err != nil {
		t.Fatal(err)
	}
	styles := newSemanticStyles(Settings{colorEnabled: true, palette: withoutBackground})
	if got := styleColor(styles.additionBackground.GetBackground()); got != "" {
		t.Fatalf("missing custom diff_add_bg generated %q", got)
	}
	if got := styleColor(styles.deletionBackground.GetBackground()); got != "" {
		t.Fatalf("missing custom diff_del_bg generated %q", got)
	}

	withBackground, err := NewPalette(map[PaletteRole]string{
		PaletteDiffAdd:              "#00ff00",
		PaletteDiffDelete:           "#ff0000",
		PaletteDiffAddBackground:    "#001100",
		PaletteDiffDeleteBackground: "#110000",
	})
	if err != nil {
		t.Fatal(err)
	}
	styles = newSemanticStyles(Settings{colorEnabled: true, palette: withBackground})
	if got := styleColor(styles.additionBackground.GetBackground()); got != "#001100" {
		t.Fatalf("custom diff_add_bg = %q", got)
	}
	if got := styleColor(styles.deletionBackground.GetBackground()); got != "#110000" {
		t.Fatalf("custom diff_del_bg = %q", got)
	}
	namedBackground, err := NewPalette(map[PaletteRole]string{
		PaletteDiffAddBackground:    "green",
		PaletteDiffDeleteBackground: "red",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := namedBackground.color(PaletteDiffAddBackground, ""); got != "2" {
		t.Fatalf("named custom diff_add_bg normalized to %q, want ANSI green", got)
	}
	if got := namedBackground.color(PaletteDiffDeleteBackground, ""); got != "1" {
		t.Fatalf("named custom diff_del_bg normalized to %q, want ANSI red", got)
	}

	styles = newSemanticStyles(Settings{colorEnabled: false, palette: withBackground})
	if styleColor(styles.additionBackground.GetBackground()) != "" ||
		styleColor(styles.deletionBackground.GetBackground()) != "" {
		t.Fatal("monochrome styles retained optional diff backgrounds")
	}
	if !styles.activeCursorRow.GetReverse() {
		t.Fatal("monochrome selection lost its non-color reverse emphasis")
	}
}

func TestCursorAndSelectedControlStylesHaveDistinctContracts(t *testing.T) {
	palette, err := NewPalette(map[PaletteRole]string{
		PalettePanelBackground:  "#111111",
		PaletteFocused:          "#00ffff",
		PaletteCursorBackground: "#555555",
		PaletteCursorForeground: "#ffffff",
	})
	if err != nil {
		t.Fatal(err)
	}
	styles := newSemanticStyles(Settings{colorEnabled: true, palette: palette})
	if got := styleColor(styles.activeCursorRow.GetForeground()); got != "#ffffff" {
		t.Fatalf("active cursor foreground = %q, want #ffffff", got)
	}
	if got := styleColor(styles.activeCursorRow.GetBackground()); got != "#555555" {
		t.Fatalf("active cursor background = %q, want #555555", got)
	}
	if !styles.activeCursorRow.GetBold() {
		t.Fatal("active cursor row omitted bold emphasis")
	}
	if got := styleColor(styles.selectionRail.GetForeground()); got != "#00ffff" {
		t.Fatalf("selection rail foreground = %q, want #00ffff", got)
	}
	if got := styleColor(styles.selectionRail.GetBackground()); got != "#111111" {
		t.Fatalf("selection rail background = %q, want #111111", got)
	}
	if got := styleColor(styles.selectedControl.GetForeground()); got != "#00ffff" {
		t.Fatalf("selected control foreground = %q, want #00ffff", got)
	}
	if got := styleColor(styles.selectedControl.GetBackground()); got != "" {
		t.Fatalf("selected control introduced row fill %q", got)
	}
	if !styles.selectedControl.GetBold() || !styles.selectedControl.GetUnderline() {
		t.Fatal("selected control omitted bold underline emphasis")
	}
}

func TestSemanticTextStylesDoNotCompoundPaletteContrastWithFaint(t *testing.T) {
	palette, err := NewPalette(map[PaletteRole]string{
		PalettePanelBackground:  "#111111",
		PalettePrimary:          "#ffffff",
		PaletteSecondary:        "#dddddd",
		PaletteDim:              "#bbbbbb",
		PaletteDiffDelete:       "#ff7777",
		PaletteSyntaxComment:    "#aaaaaa",
		PaletteStatusBackground: "#222222",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, settings := range []Settings{
		{colorEnabled: false},
		{colorEnabled: true, palette: palette},
	} {
		styles := newSemanticStyles(settings)
		for name, style := range map[string]lipgloss.Style{
			"muted":         styles.muted,
			"pane header":   styles.paneHeader,
			"gutter":        styles.gutter,
			"metadata":      styles.metadata,
			"pending style": styles.pending,
			commentLabel:    styles.comment,
		} {
			if style.GetFaint() {
				t.Fatalf("%s style compounds semantic contrast with terminal faint", name)
			}
		}
	}
}

func TestWarningStyleInheritsStatusBackgroundUnlessExplicitlyOverridden(t *testing.T) {
	for _, test := range []struct {
		name              string
		warningBackground string
		wantBackground    string
	}{
		{name: "inherits status", wantBackground: "#112233"},
		{name: "explicit override", warningBackground: "#445566", wantBackground: "#445566"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := map[PaletteRole]string{
				PaletteStatusBackground:  "#112233",
				PaletteWarningForeground: "#ffee00",
			}
			if test.warningBackground != "" {
				values[PaletteWarningBackground] = test.warningBackground
			}
			palette, err := NewPalette(values)
			if err != nil {
				t.Fatal(err)
			}
			styles := newSemanticStyles(Settings{colorEnabled: true, palette: palette})
			terminal := vt.NewEmulator(1, 1)
			t.Cleanup(func() { _ = terminal.Close() })
			if _, err := terminal.WriteString(styles.warning.Inherit(styles.status).Render("!")); err != nil {
				t.Fatal(err)
			}
			assertVTCellStyle(t, terminal, 0, "#ffee00", test.wantBackground, 1)
		})
	}
}

func TestDiffCellStylesReachGutterSyntaxContentAndPaddingCells(t *testing.T) {
	t.Run("color", func(t *testing.T) {
		styles := semanticStyles{
			gutter:             lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")),
			added:              lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")),
			additionBackground: lipgloss.NewStyle().Background(lipgloss.Color("#002200")),
			keyword:            lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0000ff")),
		}
		cell := diffCellStyles(diffCellState{
			kind:      change.PatchLineAddition,
			placement: diffCellUnified,
			selection: diffCellSelectionNone,
			colorMode: diffCellColor,
		}, styles)
		keywordStyle := styles.keyword.Inherit(cell.content)
		if cell.suppressSyntax {
			keywordStyle = cell.content
		}
		row := cell.gutter.Render("1 ") +
			keywordStyle.Render("k") +
			fitStyledCellWidth(cell.content.Render("x"), 2, cell.base)
		terminal := vt.NewEmulator(5, 1)
		t.Cleanup(func() { _ = terminal.Close() })
		if _, err := terminal.WriteString(row); err != nil {
			t.Fatal(err)
		}
		assertVTCellStyle(t, terminal, 0, "#888888", "#002200", 0)
		assertVTCellStyle(t, terminal, 2, "#0000ff", "#002200", 1)
		assertVTCellStyle(t, terminal, 3, "#00ff00", "#002200", 0)
		assertVTCellStyle(t, terminal, 4, "", "#002200", 0)
	})

	t.Run("monochrome selection", func(t *testing.T) {
		styles := semanticStyles{
			activeCursorRow: lipgloss.NewStyle().Reverse(true),
			gutter:          lipgloss.NewStyle().Faint(true),
			added:           lipgloss.NewStyle().Bold(true),
			keyword:         lipgloss.NewStyle().Bold(true),
		}
		cell := diffCellStyles(diffCellState{
			kind:      change.PatchLineAddition,
			placement: diffCellUnified,
			selection: diffCellSelectionActive,
			colorMode: diffCellMonochrome,
		}, styles)
		keywordStyle := styles.keyword.Inherit(cell.content)
		if cell.suppressSyntax {
			keywordStyle = cell.content
		}
		row := cell.gutter.Render("1 ") +
			keywordStyle.Render("k") +
			fitStyledCellWidth(cell.content.Render("x"), 2, cell.base)
		terminal := vt.NewEmulator(5, 1)
		t.Cleanup(func() { _ = terminal.Close() })
		if _, err := terminal.WriteString(row); err != nil {
			t.Fatal(err)
		}
		const attrReverse = uint8(1 << 5)
		assertVTCellStyle(t, terminal, 0, "", "", attrReverse)
		assertVTCellStyle(t, terminal, 2, "", "", attrReverse)
		assertVTCellStyle(t, terminal, 3, "", "", attrReverse)
		assertVTCellStyle(t, terminal, 4, "", "", attrReverse)
	})

	t.Run("monochrome hunk and selection remain distinct", func(t *testing.T) {
		styles := semanticStyles{
			gutter: lipgloss.NewStyle().Faint(true),
			hunk:   lipgloss.NewStyle().Bold(true),
		}
		cell := diffCellStyles(diffCellState{
			kind:      change.PatchLineHunkHeader,
			placement: diffCellUnified,
			selection: diffCellSelectionNone,
			colorMode: diffCellMonochrome,
		}, styles)
		row := cell.gutter.Render("1 ") +
			fitStyledCellWidth(cell.content.Render("@@"), 3, cell.base)
		terminal := vt.NewEmulator(5, 1)
		t.Cleanup(func() { _ = terminal.Close() })
		if _, err := terminal.WriteString(row); err != nil {
			t.Fatal(err)
		}
		const (
			attrBold  = uint8(1)
			attrFaint = uint8(1 << 1)
		)
		assertVTCellStyle(t, terminal, 0, "", "", attrFaint)
		assertVTCellStyle(t, terminal, 2, "", "", attrBold)
		assertVTCellStyle(t, terminal, 4, "", "", 0)

		selected := diffCellStyles(diffCellState{
			kind:      change.PatchLineHunkHeader,
			placement: diffCellUnified,
			selection: diffCellSelectionActive,
			colorMode: diffCellMonochrome,
		}, semanticStyles{
			activeCursorRow: lipgloss.NewStyle().Reverse(true),
			gutter:          styles.gutter,
			hunk:            styles.hunk,
		})
		selectedRow := selected.gutter.Render("1 ") +
			fitStyledCellWidth(selected.content.Render("@@"), 3, selected.base)
		selectedTerminal := vt.NewEmulator(5, 1)
		t.Cleanup(func() { _ = selectedTerminal.Close() })
		if _, err := selectedTerminal.WriteString(selectedRow); err != nil {
			t.Fatal(err)
		}
		const attrReverse = uint8(1 << 5)
		assertVTCellStyle(t, selectedTerminal, 0, "", "", attrReverse)
		assertVTCellStyle(t, selectedTerminal, 2, "", "", attrReverse)
		assertVTCellStyle(t, selectedTerminal, 4, "", "", attrReverse)
	})
}

func assertVTCellStyle(
	t *testing.T,
	terminal *vt.Emulator,
	x int,
	foreground, background string,
	attributes uint8,
) {
	t.Helper()
	cell := terminal.CellAt(x, 0)
	if cell == nil {
		t.Fatalf("cell %d is absent", x)
	}
	if got := terminalColor(cell.Style.Fg); got != foreground {
		t.Fatalf("cell %d foreground = %q, want %q", x, got, foreground)
	}
	if got := terminalColor(cell.Style.Bg); got != background {
		t.Fatalf("cell %d background = %q, want %q", x, got, background)
	}
	if cell.Style.Attrs != attributes {
		t.Fatalf("cell %d attributes = %08b, want %08b", x, cell.Style.Attrs, attributes)
	}
}

func terminalColor(value color.Color) string {
	if value == nil {
		return ""
	}
	if _, noColor := value.(lipgloss.NoColor); noColor {
		return ""
	}
	red, green, blue, alpha := value.RGBA()
	if alpha == 0 {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x", red>>8, green>>8, blue>>8)
}

func styleColor(value color.Color) string {
	return terminalColor(value)
}
