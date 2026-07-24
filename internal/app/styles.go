package app

import (
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/review"
)

type semanticStyles struct {
	heading            lipgloss.Style
	muted              lipgloss.Style
	focused            lipgloss.Style
	selected           lipgloss.Style
	paneHeader         lipgloss.Style
	focusedPaneHeader  lipgloss.Style
	gutter             lipgloss.Style
	metadata           lipgloss.Style
	added              lipgloss.Style
	deleted            lipgloss.Style
	additionBackground lipgloss.Style
	deletionBackground lipgloss.Style
	context            lipgloss.Style
	hunk               lipgloss.Style
	hunkBand           lipgloss.Style
	pending            lipgloss.Style
	reviewed           lipgloss.Style
	commentAnchor      lipgloss.Style
	keyword            lipgloss.Style
	literal            lipgloss.Style
	comment            lipgloss.Style
	number             lipgloss.Style
	warning            lipgloss.Style
	failure            lipgloss.Style
	status             lipgloss.Style
	panel              lipgloss.Style
}

type diffCellPlacement uint8

const (
	diffCellUnified diffCellPlacement = iota + 1
	diffCellOldApplicable
	diffCellOldNeutral
	diffCellNewApplicable
	diffCellNewNeutral
)

type diffCellSelection uint8

const (
	diffCellNotSelected diffCellSelection = iota + 1
	diffCellCursorSelected
	diffCellRangeSelected
)

type diffCellColorMode uint8

const (
	diffCellMonochrome diffCellColorMode = iota + 1
	diffCellColor
)

type diffCellState struct {
	kind      review.PatchLineKind
	placement diffCellPlacement
	selection diffCellSelection
	colorMode diffCellColorMode
}

type diffCellPresentation struct {
	base           lipgloss.Style
	gutter         lipgloss.Style
	content        lipgloss.Style
	suppressSyntax bool
}

func diffCellStyles(state diffCellState, styles semanticStyles) diffCellPresentation {
	if !validDiffCellState(state) {
		return diffCellPresentation{}
	}
	applicable := state.placement == diffCellUnified ||
		state.placement == diffCellOldApplicable ||
		state.placement == diffCellNewApplicable
	accent := lipgloss.NewStyle()
	content := lipgloss.NewStyle()
	switch state.kind {
	case review.PatchLineHunkHeader:
		content = styles.hunk
		accent = styles.hunkBand
	case review.PatchLineAddition:
		if applicable {
			content = styles.added
			if state.colorMode == diffCellColor && hasStyleBackground(styles.additionBackground) {
				accent = styles.additionBackground
			}
		}
	case review.PatchLineDeletion:
		if applicable {
			content = styles.deleted
			if state.colorMode == diffCellColor && hasStyleBackground(styles.deletionBackground) {
				accent = styles.deletionBackground
			}
		}
	case review.PatchLineContext:
		content = styles.context
	case review.PatchLineMetadata, review.PatchLineNoNewline:
		content = styles.metadata
	}
	base := diffCellBase(state.selection, state.colorMode, accent, styles)
	return diffCellPresentation{
		base:           base,
		gutter:         styles.gutter.Inherit(base),
		content:        content.Inherit(base),
		suppressSyntax: state.selection != diffCellNotSelected,
	}
}

func diffCellBase(
	selection diffCellSelection,
	colorMode diffCellColorMode,
	accent lipgloss.Style,
	styles semanticStyles,
) lipgloss.Style {
	base := lipgloss.NewStyle()
	if colorMode == diffCellColor {
		base = styles.panel
	}
	base = accent.Inherit(base)
	if selection == diffCellCursorSelected || selection == diffCellRangeSelected {
		base = styles.selected
	}
	return base
}

func hasStyleBackground(style lipgloss.Style) bool {
	_, noColor := style.GetBackground().(lipgloss.NoColor)
	return !noColor
}

func validDiffCellState(state diffCellState) bool {
	validKind := state.kind >= review.PatchLineMetadata && state.kind <= review.PatchLineNoNewline
	validPlacement := state.placement >= diffCellUnified && state.placement <= diffCellNewNeutral
	validSelection := state.selection >= diffCellNotSelected &&
		state.selection <= diffCellRangeSelected
	validColorMode := state.colorMode == diffCellMonochrome || state.colorMode == diffCellColor
	return validKind && validPlacement && validSelection && validColorMode
}

func newSemanticStyles(settings Settings) semanticStyles {
	styles := semanticStyles{
		heading:           lipgloss.NewStyle().Bold(true),
		muted:             lipgloss.NewStyle(),
		focused:           lipgloss.NewStyle().Bold(true).Underline(true),
		selected:          lipgloss.NewStyle().Reverse(true),
		paneHeader:        lipgloss.NewStyle(),
		focusedPaneHeader: lipgloss.NewStyle().Bold(true),
		gutter:            lipgloss.NewStyle(),
		metadata:          lipgloss.NewStyle(),
		warning:           lipgloss.NewStyle().Bold(true),
		failure:           lipgloss.NewStyle().Bold(true),
	}
	if !settings.colorEnabled {
		styles.added = lipgloss.NewStyle().Bold(true)
		styles.deleted = lipgloss.NewStyle().Underline(true)
		styles.hunk = lipgloss.NewStyle().Bold(true)
		styles.context = lipgloss.NewStyle()
		styles.pending = lipgloss.NewStyle()
		styles.reviewed = lipgloss.NewStyle().Bold(true)
		styles.commentAnchor = lipgloss.NewStyle().Bold(true)
		styles.keyword = lipgloss.NewStyle().Bold(true)
		styles.literal = lipgloss.NewStyle().Underline(true)
		styles.comment = lipgloss.NewStyle()
		styles.number = lipgloss.NewStyle().Bold(true)
		return styles
	}
	palette := settings.palette
	styles.panel = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PalettePrimary, "15"))).
		Background(lipgloss.Color(palette.color(PalettePanelBackground, "")))
	styles.heading = styles.heading.Foreground(
		lipgloss.Color(palette.color(PalettePrimary, "15")),
	)
	styles.muted = styles.muted.Foreground(
		lipgloss.Color(palette.color(PaletteDim, "8")),
	)
	styles.focused = styles.focused.Foreground(
		lipgloss.Color(palette.color(PaletteFocused, "6")),
	)
	styles.selected = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(palette.color(PalettePrimary, "15"))).
		Background(lipgloss.Color(palette.color(PaletteCursorBackground, "8")))
	styles.paneHeader = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSecondary, "7")),
	).Inherit(styles.panel)
	styles.focusedPaneHeader = lipgloss.NewStyle().Bold(true).Foreground(
		lipgloss.Color(palette.color(PaletteFocused, "6")),
	).Inherit(styles.panel)
	styles.gutter = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteDim, "8")),
	)
	styles.metadata = styles.gutter
	styles.added = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteDiffAdd, "2")))
	styles.deleted = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteDiffDelete, "1")))
	if background := palette.color(PaletteDiffAddBackground, ""); background != "" {
		styles.additionBackground = lipgloss.NewStyle().Background(lipgloss.Color(background))
	}
	if background := palette.color(PaletteDiffDeleteBackground, ""); background != "" {
		styles.deletionBackground = lipgloss.NewStyle().Background(lipgloss.Color(background))
	}
	styles.hunk = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(
			PaletteDiffHunk,
			palette.color(PaletteDiffContext, "6"),
		)),
	)
	if background := palette.color(PaletteStatusBackground, ""); background != "" {
		styles.hunkBand = lipgloss.NewStyle().Background(lipgloss.Color(background))
	}
	styles.context = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteDiffContext, "7")),
	)
	styles.pending = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PalettePending, "3")),
	)
	styles.reviewed = lipgloss.NewStyle().Bold(true).Foreground(
		lipgloss.Color(palette.color(PalettePrimary, "15")),
	)
	styles.commentAnchor = lipgloss.NewStyle().Bold(true).Foreground(
		lipgloss.Color(palette.color(PalettePrimary, "15")),
	)
	styles.warning = styles.warning.Foreground(
		lipgloss.Color(palette.color(PaletteWarningForeground, "3")),
	)
	if background := palette.color(PaletteWarningBackground, ""); background != "" {
		styles.warning = styles.warning.Background(lipgloss.Color(background))
	}
	styles.failure = styles.failure.Foreground(
		lipgloss.Color(palette.color(PaletteDiffDelete, "1")),
	)
	styles.keyword = lipgloss.NewStyle().Bold(true).Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxKeyword, "5")),
	)
	styles.literal = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxString, "2")),
	)
	styles.comment = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxComment, "8")),
	)
	styles.number = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxNumber, "6")),
	)
	styles.status = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteSecondary, "7"))).
		Background(lipgloss.Color(palette.color(PaletteStatusBackground, "")))
	return styles
}
