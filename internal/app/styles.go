package app

import "charm.land/lipgloss/v2"

func newSemanticStyles(settings Settings) semanticStyles {
	styles := semanticStyles{
		heading:  lipgloss.NewStyle().Bold(true),
		muted:    lipgloss.NewStyle().Faint(true),
		focused:  lipgloss.NewStyle().Bold(true).Underline(true),
		selected: lipgloss.NewStyle().Reverse(true),
		warning:  lipgloss.NewStyle().Bold(true),
		failure:  lipgloss.NewStyle().Bold(true),
	}
	if !settings.colorEnabled {
		styles.added = lipgloss.NewStyle().Bold(true)
		styles.deleted = lipgloss.NewStyle().Underline(true)
		styles.hunk = lipgloss.NewStyle().Faint(true)
		styles.context = lipgloss.NewStyle()
		styles.pending = lipgloss.NewStyle().Faint(true)
		styles.reviewed = lipgloss.NewStyle().Bold(true)
		styles.commentAnchor = lipgloss.NewStyle().Bold(true)
		styles.keyword = lipgloss.NewStyle().Bold(true)
		styles.literal = lipgloss.NewStyle().Underline(true)
		styles.comment = lipgloss.NewStyle().Faint(true)
		styles.commentNote = lipgloss.NewStyle()
		styles.commentSuggest = lipgloss.NewStyle()
		styles.number = lipgloss.NewStyle().Bold(true)
		return styles
	}
	palette := settings.palette
	styles.heading = styles.heading.Foreground(
		lipgloss.Color(palette.color(PalettePrimary, "15")),
	)
	styles.muted = styles.muted.Foreground(
		lipgloss.Color(palette.color(PaletteDim, "8")),
	)
	styles.focused = styles.focused.Foreground(
		lipgloss.Color(palette.color(PaletteFocused, "6")),
	)
	styles.selected = styles.selected.
		Foreground(lipgloss.Color(palette.color(PalettePrimary, "15"))).
		Background(lipgloss.Color(palette.color(PaletteCursorBackground, "8")))
	styles.added = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteDiffAdd, "2"))).
		Background(lipgloss.Color(palette.color(PaletteDiffAddBackground, "")))
	styles.deleted = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteDiffDelete, "1"))).
		Background(lipgloss.Color(palette.color(PaletteDiffDeleteBackground, "")))
	styles.hunk = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(
			PaletteDiffHunk,
			palette.color(PaletteDiffContext, "6"),
		)),
	)
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
	styles.warning = styles.warning.
		Foreground(lipgloss.Color(palette.color(PaletteWarningForeground, "3"))).
		Background(lipgloss.Color(palette.color(PaletteWarningBackground, "")))
	styles.failure = styles.failure.Foreground(lipgloss.Red)
	styles.keyword = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxKeyword, "5")),
	)
	styles.literal = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxString, "2")),
	)
	styles.comment = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxComment, "8")),
	)
	styles.commentNote = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteCommentNote, palette.color(PaletteSecondary, "7"))),
	)
	styles.commentSuggest = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteCommentSuggestion, palette.color(PaletteSecondary, "7"))),
	)
	styles.number = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxNumber, "6")),
	)
	styles.status = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteSecondary, "7"))).
		Background(lipgloss.Color(palette.color(PaletteStatusBackground, "")))
	styles.panel = lipgloss.NewStyle().Background(
		lipgloss.Color(palette.color(PalettePanelBackground, "")),
	)
	return styles
}
