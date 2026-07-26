package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/jacoelho/rvw/internal/keymap"
)

const bindingBarMaximumItems = 4

type bindingBar struct {
	labels   map[keymap.Action]string
	bindings []keymap.Binding
}

func (bar bindingBar) Render(width int, styles semanticStyles) string {
	return bar.render(width, styles, lipgloss.Style{})
}

func (bar bindingBar) RenderWithBase(
	width int,
	styles semanticStyles,
	base lipgloss.Style,
) string {
	return bar.render(width, styles, base)
}

func (bar bindingBar) render(width int, styles semanticStyles, base lipgloss.Style) string {
	if width <= 0 {
		return ""
	}
	var rendered strings.Builder
	used := 0
	for _, binding := range bar.bindings[:min(len(bar.bindings), bindingBarMaximumItems)] {
		separator := ""
		if used != 0 {
			separator = base.Render("  ")
		}
		hint := bar.renderBinding(binding, styles, base)
		hintWidth := lipgloss.Width(separator + hint)
		if used+hintWidth > width {
			continue
		}
		rendered.WriteString(separator)
		rendered.WriteString(hint)
		used += hintWidth
	}
	return rendered.String()
}

func (bar bindingBar) RenderRows(
	width, maximumRows int,
	styles semanticStyles,
	base lipgloss.Style,
) []string {
	if width <= 0 || maximumRows <= 0 {
		return nil
	}
	rows := make([]string, 0, maximumRows)
	current := ""
	currentWidth := 0
	for _, binding := range bar.bindings[:min(len(bar.bindings), bindingBarMaximumItems)] {
		hint := bar.renderBinding(binding, styles, base)
		hintWidth := lipgloss.Width(hint)
		if hintWidth > width {
			continue
		}
		separator := ""
		separatorWidth := 0
		if currentWidth != 0 {
			separator = base.Render("  ")
			separatorWidth = 2
		}
		if currentWidth+separatorWidth+hintWidth > width {
			rows = append(rows, current)
			if len(rows) == maximumRows {
				return rows
			}
			current = hint
			currentWidth = hintWidth
			continue
		}
		current += separator + hint
		currentWidth += separatorWidth + hintWidth
	}
	if current != "" && len(rows) < maximumRows {
		rows = append(rows, current)
	}
	return rows
}

func (bar bindingBar) renderBinding(
	binding keymap.Binding,
	styles semanticStyles,
	base lipgloss.Style,
) string {
	label := binding.Action.Description()
	if override := bar.labels[binding.Action]; override != "" {
		label = override
	}
	return styles.heading.Inherit(base).Render(binding.Chord.Display()) +
		base.Render(" ") +
		styles.muted.Inherit(base).Render(label)
}

type effectiveHelpKeyMap struct {
	bindings []keymap.Binding
	maxItems int
	offset   int
}

// ShortHelp returns the receiver's short help value.
func (keyMap effectiveHelpKeyMap) ShortHelp() []key.Binding {
	return keyMap.bindingsFor(min(keyMap.maxItems, 6))
}

// FullHelp returns the receiver's full help value.
func (keyMap effectiveHelpKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{keyMap.bindingsFor(keyMap.maxItems)}
}

func (keyMap effectiveHelpKeyMap) bindingsFor(limit int) []key.Binding {
	start := min(max(keyMap.offset, 0), len(keyMap.bindings))
	bindings := make([]key.Binding, 0, min(len(keyMap.bindings)-start, limit))
	for _, binding := range keyMap.bindings[start:] {
		bindings = append(bindings, key.NewBinding(
			key.WithKeys(binding.Chord.String()),
			key.WithHelp(binding.Chord.Display(), binding.Action.Description()),
		))
		if len(bindings) == limit {
			break
		}
	}
	return bindings
}
