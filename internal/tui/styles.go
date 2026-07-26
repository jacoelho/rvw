package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/change"
)

type semanticStyles struct {
	heading            lipgloss.Style
	muted              lipgloss.Style
	focused            lipgloss.Style
	activeCursorRow    lipgloss.Style
	selectionRail      lipgloss.Style
	selectedControl    lipgloss.Style
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
	success            lipgloss.Style
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
	diffCellSelectionNone diffCellSelection = iota + 1
	diffCellSelectionRetained
	diffCellSelectionActive
	diffCellSelectionRange
)

type diffCellColorMode uint8

const (
	diffCellMonochrome diffCellColorMode = iota + 1
	diffCellColor
)

type diffCellState struct {
	kind      change.PatchLineKind
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
	case change.PatchLineHunkHeader:
		content = styles.hunk
		accent = styles.hunkBand
	case change.PatchLineAddition:
		if applicable {
			content = styles.added
			if state.colorMode == diffCellColor && hasStyleBackground(styles.additionBackground) {
				accent = styles.additionBackground
			}
		}
	case change.PatchLineDeletion:
		if applicable {
			content = styles.deleted
			if state.colorMode == diffCellColor && hasStyleBackground(styles.deletionBackground) {
				accent = styles.deletionBackground
			}
		}
	case change.PatchLineContext:
		content = styles.context
	case change.PatchLineMetadata, change.PatchLineNoNewline:
		content = styles.metadata
	}
	base := diffCellBase(state.selection, state.colorMode, accent, styles)
	gutter := styles.gutter.Inherit(base)
	styledContent := content.Inherit(base)
	if state.selection == diffCellSelectionActive {
		gutter = styles.activeCursorRow
		styledContent = styles.activeCursorRow
	}
	return diffCellPresentation{
		base:           base,
		gutter:         gutter,
		content:        styledContent,
		suppressSyntax: state.selection == diffCellSelectionActive,
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
	if selection == diffCellSelectionActive {
		base = styles.activeCursorRow
	}
	return base
}

func hasStyleBackground(style lipgloss.Style) bool {
	_, noColor := style.GetBackground().(lipgloss.NoColor)
	return !noColor
}

func validDiffCellState(state diffCellState) bool {
	validKind := state.kind >= change.PatchLineMetadata && state.kind <= change.PatchLineNoNewline
	validPlacement := state.placement >= diffCellUnified && state.placement <= diffCellNewNeutral
	validSelection := state.selection >= diffCellSelectionNone &&
		state.selection <= diffCellSelectionRange
	validColorMode := state.colorMode == diffCellMonochrome || state.colorMode == diffCellColor
	return validKind && validPlacement && validSelection && validColorMode
}

func newSemanticStyles(settings Settings) semanticStyles {
	styles := semanticStyles{
		heading:           lipgloss.NewStyle().Bold(true),
		muted:             lipgloss.NewStyle(),
		focused:           lipgloss.NewStyle().Bold(true).Underline(true),
		activeCursorRow:   lipgloss.NewStyle().Reverse(true),
		selectedControl:   lipgloss.NewStyle().Bold(true).Underline(true),
		paneHeader:        lipgloss.NewStyle(),
		focusedPaneHeader: lipgloss.NewStyle().Bold(true),
		gutter:            lipgloss.NewStyle(),
		metadata:          lipgloss.NewStyle(),
		warning:           lipgloss.NewStyle().Bold(true),
		failure:           lipgloss.NewStyle().Bold(true),
		success:           lipgloss.NewStyle().Bold(true),
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
	styles.activeCursorRow = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(palette.color(PaletteCursorForeground, "15"))).
		Background(lipgloss.Color(palette.color(PaletteCursorBackground, "8")))
	styles.selectionRail = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteFocused, "6"))).
		Background(lipgloss.Color(palette.color(PalettePanelBackground, "")))
	styles.selectedControl = styles.selectedControl.Foreground(
		lipgloss.Color(palette.color(PaletteFocused, "6")))
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
	styles.warning = semanticMessageStyle(
		styles.warning, palette, PaletteWarningForeground, PaletteWarningBackground,
		palette.color(PalettePending, "3"),
	)
	styles.failure = semanticMessageStyle(
		styles.failure, palette, PaletteErrorForeground, PaletteErrorBackground,
		palette.color(PaletteDiffDelete, "1"),
	)
	styles.success = semanticMessageStyle(
		styles.success, palette, PaletteSuccessForeground, PaletteSuccessBackground,
		palette.color(PaletteDiffAdd, "2"),
	)
	syntaxFallback := palette.color(PalettePrimary, "15")
	styles.keyword = lipgloss.NewStyle().Bold(true).Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxKeyword, syntaxFallback)),
	)
	styles.literal = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxString, syntaxFallback)),
	)
	styles.comment = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxComment, syntaxFallback)),
	)
	styles.number = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxNumber, syntaxFallback)),
	)
	styles.status = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteSecondary, "7"))).
		Background(lipgloss.Color(palette.color(PaletteStatusBackground, "")))
	return styles
}

func semanticMessageStyle(
	base lipgloss.Style,
	palette Palette,
	foregroundRole, backgroundRole PaletteRole,
	foregroundFallback string,
) lipgloss.Style {
	style := base.Foreground(lipgloss.Color(
		palette.color(foregroundRole, foregroundFallback),
	))
	if background := palette.color(backgroundRole, ""); background != "" {
		style = style.Background(lipgloss.Color(background))
	}
	return style
}
