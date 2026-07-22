package app

import "charm.land/bubbles/v2/key"

type effectiveHelpKeyMap struct {
	bindings []Keybinding
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
			key.WithHelp(binding.Chord.String(), binding.Action.String()),
		))
		if len(bindings) == limit {
			break
		}
	}
	return bindings
}
