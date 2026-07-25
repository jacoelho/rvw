package app

import "charm.land/lipgloss/v2"

func newSemanticStyles(settings Settings) semanticStyles {
	styles := semanticStyles{
		heading:         lipgloss.NewStyle().Bold(true),
		muted:           lipgloss.NewStyle(),
		focused:         lipgloss.NewStyle().Bold(true).Underline(true),
		selected:        lipgloss.NewStyle().Reverse(true),
		cursorLine:      lipgloss.NewStyle().Reverse(true),
		visualSelection: lipgloss.NewStyle().Bold(true),
		border:          lipgloss.NewStyle(),
		borderFocused:   lipgloss.NewStyle().Bold(true),
		warning:         lipgloss.NewStyle().Bold(true),
		failure:         lipgloss.NewStyle().Bold(true),
		notice:          lipgloss.NewStyle().Bold(true),
		treeDirectory:   lipgloss.NewStyle().Bold(true),
		treeFile:        lipgloss.NewStyle(),
	}
	if !settings.colorEnabled {
		styles.added = lipgloss.NewStyle().Bold(true)
		styles.deleted = lipgloss.NewStyle().Underline(true)
		styles.hunk = lipgloss.NewStyle()
		styles.context = lipgloss.NewStyle()
		styles.pending = lipgloss.NewStyle().Bold(true)
		styles.reviewed = lipgloss.NewStyle().Bold(true)
		styles.commentAnchor = lipgloss.NewStyle().Bold(true)
		styles.keyword = lipgloss.NewStyle().Bold(true)
		styles.literal = lipgloss.NewStyle().Underline(true)
		styles.comment = lipgloss.NewStyle()
		styles.number = lipgloss.NewStyle().Bold(true)
		styles.status = lipgloss.NewStyle()
		styles.panel = lipgloss.NewStyle()
		return styles
	}
	palette := settings.palette
	styles.heading = styles.heading.Foreground(
		lipgloss.Color(palette.color(PalettePrimary, "15")),
	)
	styles.muted = styles.muted.Foreground(
		lipgloss.Color(palette.color(PaletteDim, "245")),
	)
	styles.focused = styles.focused.Foreground(
		lipgloss.Color(palette.color(PaletteFocused, "14")),
	)
	styles.selected = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PalettePrimary, "15"))).
		Background(lipgloss.Color(palette.color(PaletteCursorBackground, "240")))
	styles.cursorLine = lipgloss.NewStyle().Background(
		lipgloss.Color(palette.color(PaletteCursorBackground, "240")),
	)
	styles.visualSelection = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(palette.color(PaletteFocused, "14")))
	styles.border = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteDim, "240")),
	)
	styles.borderFocused = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteFocused, "14")),
	)
	styles.added = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteDiffAdd, "10"))).
		Background(lipgloss.Color(palette.color(PaletteDiffAddBackground, "")))
	styles.deleted = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteDiffDelete, "9"))).
		Background(lipgloss.Color(palette.color(PaletteDiffDeleteBackground, "")))
	styles.hunk = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(
			PaletteDiffHunk,
			palette.color(PaletteDiffContext, "14"),
		)),
	)
	styles.context = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteDiffContext, "252")),
	)
	styles.pending = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PalettePending, "11")),
	)
	styles.reviewed = lipgloss.NewStyle().Bold(true).Foreground(
		lipgloss.Color(palette.color(PalettePrimary, "10")),
	)
	styles.commentAnchor = lipgloss.NewStyle().Bold(true).Foreground(
		lipgloss.Color(palette.color(PalettePrimary, "13")),
	)
	styles.warning = styles.warning.
		Foreground(lipgloss.Color(palette.color(PaletteWarningForeground, "11"))).
		Background(lipgloss.Color(palette.color(PaletteWarningBackground, "")))
	styles.failure = styles.failure.Foreground(lipgloss.Color("9"))
	styles.notice = styles.notice.Foreground(
		lipgloss.Color(palette.color(PaletteNotice, "14")),
	)
	styles.keyword = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxKeyword, "5")),
	)
	styles.literal = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxString, "2")),
	)
	styles.comment = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxComment, "243")),
	)
	styles.number = lipgloss.NewStyle().Foreground(
		lipgloss.Color(palette.color(PaletteSyntaxNumber, "6")),
	)
	styles.status = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteSecondary, "252"))).
		Background(lipgloss.Color(palette.color(PaletteStatusBackground, "")))
	styles.panel = lipgloss.NewStyle().Background(
		lipgloss.Color(palette.color(PalettePanelBackground, "")),
	)
	styles.treeDirectory = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(palette.color(PaletteTreeDirectory, "12")))
	styles.treeFile = lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette.color(PaletteTreeFile, "252")))
	return styles
}

func (styles semanticStyles) onCursorLine() semanticStyles {
	selected := styles
	selected.base = inheritCursorLine(styles.base, styles.cursorLine)
	selected.heading = inheritCursorLine(styles.heading, styles.cursorLine)
	selected.muted = inheritCursorLine(styles.muted, styles.cursorLine)
	selected.focused = inheritCursorLine(styles.focused, styles.cursorLine)
	selected.selected = inheritCursorLine(styles.selected, styles.cursorLine)
	selected.added = inheritCursorLine(styles.added, styles.cursorLine)
	selected.deleted = inheritCursorLine(styles.deleted, styles.cursorLine)
	selected.context = inheritCursorLine(styles.context, styles.cursorLine)
	selected.hunk = inheritCursorLine(styles.hunk, styles.cursorLine)
	selected.pending = inheritCursorLine(styles.pending, styles.cursorLine)
	selected.reviewed = inheritCursorLine(styles.reviewed, styles.cursorLine)
	selected.commentAnchor = inheritCursorLine(styles.commentAnchor, styles.cursorLine)
	selected.keyword = inheritCursorLine(styles.keyword, styles.cursorLine)
	selected.literal = inheritCursorLine(styles.literal, styles.cursorLine)
	selected.comment = inheritCursorLine(styles.comment, styles.cursorLine)
	selected.number = inheritCursorLine(styles.number, styles.cursorLine)
	selected.warning = inheritCursorLine(styles.warning, styles.cursorLine)
	selected.failure = inheritCursorLine(styles.failure, styles.cursorLine)
	selected.treeDirectory = inheritCursorLine(styles.treeDirectory, styles.cursorLine)
	selected.treeFile = inheritCursorLine(styles.treeFile, styles.cursorLine)
	return selected
}

func inheritCursorLine(style, cursor lipgloss.Style) lipgloss.Style {
	if background := cursor.GetBackground(); background != nil {
		return style.Background(background)
	}
	if cursor.GetReverse() {
		return style.Reverse(true)
	}
	return style
}
