package app

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/jacoelho/rvw/internal/keymap"
)

const bindingBarMaximumItems = 4

type bindingBar struct {
	bindings []keymap.Binding
}

func (bar bindingBar) Render(width int, styles semanticStyles) string {
	if width <= 0 {
		return ""
	}
	var rendered strings.Builder
	used := 0
	for _, binding := range bar.bindings[:min(len(bar.bindings), bindingBarMaximumItems)] {
		separator := ""
		if used != 0 {
			separator = "  "
		}
		hint := styles.heading.Render(binding.Chord.Display()) + " " +
			styles.muted.Render(binding.Action.Description())
		hintWidth := lipgloss.Width(separator + hint)
		if used+hintWidth > width {
			break
		}
		rendered.WriteString(separator)
		rendered.WriteString(hint)
		used += hintWidth
	}
	return rendered.String()
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
