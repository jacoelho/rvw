package keymap

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type chordKey struct {
	strokes [MaxChordStrokes]string
	count   int
}

type scopeKey struct {
	context Context
	focus   Focus
}

type chordBinding struct {
	action Action
	prefix bool
}

type scopeMap struct {
	chords                map[chordKey]chordBinding
	summary               string
	statusBindings        []Binding
	commentEditorBindings []Binding
	bindings              []Binding
}

// Map is an immutable compiled set of effective bindings. Matching, help, and
// status presentation all read the same per-scope binding sequence.
type Map struct {
	scopes map[scopeKey]*scopeMap
}

// Match describes one chord lookup.
type Match struct {
	Action Action
	Exact  bool
	Prefix bool
}

// Build validates and compiles effective bindings for every runtime scope.
func Build(bindings []Binding) (*Map, error) {
	if len(bindings) == 0 {
		return nil, errors.New("at least one keybinding is required")
	}
	if len(bindings) > MaxBindings {
		return nil, fmt.Errorf("keybindings exceed supported limit %d", MaxBindings)
	}
	type identity struct {
		context Context
		focus   Focus
		chord   Chord
	}
	type source struct {
		binding Binding
		chord   chordKey
	}
	seen := make(map[identity]struct{}, len(bindings))
	sources := make([]source, 0, len(bindings))
	for _, binding := range bindings {
		if err := ValidateBinding(binding); err != nil {
			return nil, fmt.Errorf("invalid keybinding: %w", err)
		}
		key := identity{context: binding.Context, focus: binding.Focus, chord: binding.Chord}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf(
				"duplicate keybinding for %q/%q/%q",
				binding.Context,
				binding.Focus,
				binding.Chord,
			)
		}
		seen[key] = struct{}{}
		chord, err := compileChord(binding.Chord)
		if err != nil {
			return nil, fmt.Errorf("keybinding chord %q: %w", binding.Chord, err)
		}
		sources = append(sources, source{binding: binding, chord: chord})
	}
	compiled := &Map{scopes: make(map[scopeKey]*scopeMap, len(RuntimeScopes()))}
	for _, runtimeScope := range RuntimeScopes() {
		scope := scopeKey{context: runtimeScope.Context, focus: runtimeScope.Focus}
		eligible := 0
		for _, source := range sources {
			if applies(source.binding, scope) {
				eligible++
			}
		}
		bindings := make([]Binding, 0, eligible)
		keys := make([]chordKey, 0, eligible)
		positions := make(map[chordKey]int, eligible)
		for _, source := range sources {
			if !applies(source.binding, scope) {
				continue
			}
			if position, exists := positions[source.chord]; exists {
				if source.binding.Focus != FocusNone {
					bindings[position] = source.binding
					keys[position] = source.chord
				}
				continue
			}
			positions[source.chord] = len(bindings)
			bindings = append(bindings, source.binding)
			keys = append(keys, source.chord)
		}
		bindings = slices.Clip(bindings)
		entries := 0
		for _, key := range keys {
			entries += key.count
		}
		chords := make(map[chordKey]chordBinding, entries)
		for index, key := range keys {
			entry := chords[key]
			entry.action = bindings[index].Action
			chords[key] = entry
			for count := 1; count < key.count; count++ {
				prefix := key.prefix(count)
				entry := chords[prefix]
				entry.prefix = true
				chords[prefix] = entry
			}
		}
		compiledScope := &scopeMap{
			chords: chords, bindings: bindings, summary: formatSummary(bindings, 4),
		}
		if scope.context == ContextNormal {
			compiledScope.statusBindings = SelectBindings(bindings, statusActions(scope.focus))
		}
		if scope.context == ContextCommentEdit {
			compiledScope.commentEditorBindings = SelectBindings(bindings, []Action{
				ActionSaveComment, ActionInsertNewline, ActionCancel,
			})
		}
		compiled.scopes[scope] = compiledScope
	}
	return compiled, nil
}

func compileChord(chord Chord) (chordKey, error) {
	fields := chord.Strokes()
	if len(chord) > MaxChordBytes || len(fields) == 0 || len(fields) > MaxChordStrokes {
		return chordKey{}, errors.New("keybinding chord must have 1..4 strokes and at most 128 bytes")
	}
	var key chordKey
	key.count = len(fields)
	for index, field := range fields {
		stroke := NormalizeStroke(field)
		if stroke == "" {
			return chordKey{}, errors.New("keybinding chord contains an empty stroke")
		}
		key.strokes[index] = stroke
	}
	return key, nil
}

func chordFromStrokes(strokes []string) (chordKey, bool) {
	if len(strokes) == 0 || len(strokes) > MaxChordStrokes {
		return chordKey{}, false
	}
	var key chordKey
	key.count = len(strokes)
	copy(key.strokes[:], strokes)
	return key, true
}

func (key chordKey) prefix(count int) chordKey {
	var prefix chordKey
	prefix.count = count
	copy(prefix.strokes[:count], key.strokes[:count])
	return prefix
}

func applies(binding Binding, scope scopeKey) bool {
	return binding.Context == scope.context &&
		(binding.Focus == FocusNone || binding.Focus == scope.focus)
}

func (m *Map) scope(scope Scope) *scopeMap {
	if m == nil {
		return nil
	}
	if scope.Context != ContextNormal {
		scope.Focus = FocusNone
	}
	return m.scopes[scopeKey{context: scope.Context, focus: scope.Focus}]
}

// Resolve matches normalized strokes in one concrete scope.
func (m *Map) Resolve(scope Scope, strokes []string) Match {
	key, ok := chordFromStrokes(strokes)
	if !ok {
		return Match{}
	}
	compiled := m.scope(scope)
	if compiled == nil {
		return Match{}
	}
	binding := compiled.chords[key]
	return Match{
		Action: binding.action,
		Exact:  binding.action != ActionNone,
		Prefix: binding.prefix,
	}
}

// Bindings returns the immutable effective binding sequence for a scope.
func (m *Map) Bindings(scope Scope) []Binding {
	compiled := m.scope(scope)
	if compiled == nil {
		return nil
	}
	return compiled.bindings
}

// Summary returns the first four effective binding hints for a scope.
func (m *Map) Summary(scope Scope) string {
	compiled := m.scope(scope)
	if compiled == nil {
		return ""
	}
	return compiled.summary
}

// NormalStatusBindings returns the precompiled workspace status hints.
func (m *Map) NormalStatusBindings(focus Focus) []Binding {
	compiled := m.scope(Scope{Context: ContextNormal, Focus: focus})
	if compiled == nil {
		return nil
	}
	return compiled.statusBindings
}

// CommentEditorBindings returns the precompiled comment-editor hints.
func (m *Map) CommentEditorBindings() []Binding {
	compiled := m.scope(Scope{Context: ContextCommentEdit})
	if compiled == nil {
		return nil
	}
	return compiled.commentEditorBindings
}

// BindingsFor returns the first effective binding for each requested action.
func (m *Map) BindingsFor(scope Scope, actions []Action) []Binding {
	return SelectBindings(m.Bindings(scope), actions)
}

// SelectBindings returns the first binding for each requested action.
func SelectBindings(bindings []Binding, actions []Action) []Binding {
	selected := make([]Binding, 0, len(actions))
	for _, action := range actions {
		for _, binding := range bindings {
			if binding.Action == action {
				selected = append(selected, binding)
				break
			}
		}
	}
	return slices.Clip(selected)
}

// AppendSummary appends one display-ready binding summary item.
func AppendSummary(summary *strings.Builder, index int, chord Chord, action Action) {
	if index != 0 {
		summary.WriteString("  ")
	}
	summary.WriteString(chord.Display())
	summary.WriteByte(' ')
	summary.WriteString(action.Description())
}

func formatSummary(bindings []Binding, limit int) string {
	var summary strings.Builder
	for index, binding := range bindings[:min(len(bindings), limit)] {
		AppendSummary(&summary, index, binding.Chord, binding.Action)
	}
	return summary.String()
}

func statusActions(focus Focus) []Action {
	switch focus {
	case FocusFileTree:
		return []Action{ActionMoveDown, ActionActivate, ActionFocusNextPane, ActionHelp}
	case FocusComments:
		return []Action{ActionMoveDown, ActionEditComment, ActionFocusNextPane, ActionHelp}
	default:
		return []Action{ActionMoveDown, ActionCommentAtCursor, ActionFocusNextPane, ActionHelp}
	}
}
