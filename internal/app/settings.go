package app

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
)

// DiffView represents diff view.
type DiffView uint8

// DiffViewUnified identifies a supported diff view unified value.
const (
	DiffViewUnified DiffView = iota + 1
	DiffViewSideBySide
)

const maxKeyChordStrokes = keymap.MaxChordStrokes

type keyChordKey struct {
	strokes [maxKeyChordStrokes]string
	count   int
}

func compileKeyChord(chord keymap.Chord) (keyChordKey, error) {
	fields := chord.Strokes()
	if len(chord) > keymap.MaxChordBytes || len(fields) == 0 || len(fields) > maxKeyChordStrokes {
		return keyChordKey{}, errors.New("keybinding chord must have 1..4 strokes and at most 128 bytes")
	}
	var key keyChordKey
	key.count = len(fields)
	for index, field := range fields {
		stroke := normalizeStroke(field)
		if stroke == "" {
			return keyChordKey{}, errors.New("keybinding chord contains an empty stroke")
		}
		key.strokes[index] = stroke
	}
	return key, nil
}

func keyChordFromStrokes(strokes []string) (keyChordKey, bool) {
	if len(strokes) == 0 || len(strokes) > maxKeyChordStrokes {
		return keyChordKey{}, false
	}
	var key keyChordKey
	key.count = len(strokes)
	copy(key.strokes[:], strokes)
	return key, true
}

func (key keyChordKey) prefix(count int) keyChordKey {
	var prefix keyChordKey
	prefix.count = count
	copy(prefix.strokes[:count], key.strokes[:count])
	return prefix
}

type keyBindingScope struct {
	context keymap.Context
	focus   keymap.Focus
}

type compiledChordBinding struct {
	action keymap.Action
	prefix bool
}

type keyResolutionKind uint8

const (
	keyResolutionNone keyResolutionKind = iota
	keyResolutionExact
	keyResolutionPrefix
	keyResolutionExactPrefix
)

type keyResolution struct {
	action keymap.Action
	kind   keyResolutionKind
}

func (resolution keyResolution) exact() bool {
	return resolution.kind == keyResolutionExact || resolution.kind == keyResolutionExactPrefix
}

func (resolution keyResolution) prefix() bool {
	return resolution.kind == keyResolutionPrefix || resolution.kind == keyResolutionExactPrefix
}

var commentEditorSummaryActions = [...]keymap.Action{
	keymap.ActionSaveComment,
	keymap.ActionInsertNewline,
	keymap.ActionCancel,
}

type compiledKeyScope struct {
	chords                map[keyChordKey]compiledChordBinding
	bindingSummary        string
	normalStatusBindings  []keymap.Binding
	commentEditorBindings []keymap.Binding
	bindings              []keymap.Binding
}

type compiledKeybindings struct {
	scopes map[keyBindingScope]*compiledKeyScope
}

type compiledBindingSource struct {
	binding keymap.Binding
	chord   keyChordKey
}

func compileKeybindings(bindings []keymap.Binding) (*compiledKeybindings, error) {
	if len(bindings) == 0 {
		return nil, errors.New("at least one keybinding is required")
	}
	if len(bindings) > keymap.MaxBindings {
		return nil, fmt.Errorf("keybindings exceed supported limit %d", keymap.MaxBindings)
	}
	type bindingIdentity struct {
		context keymap.Context
		focus   keymap.Focus
		chord   keymap.Chord
	}
	seen := make(map[bindingIdentity]struct{}, len(bindings))
	sources := make([]compiledBindingSource, 0, len(bindings))
	for _, binding := range bindings {
		if err := validateKeybinding(binding); err != nil {
			return nil, err
		}
		identity := bindingIdentity{
			context: binding.Context,
			focus:   binding.Focus,
			chord:   binding.Chord,
		}
		if _, exists := seen[identity]; exists {
			return nil, fmt.Errorf(
				"duplicate keybinding for %q/%q/%q",
				binding.Context,
				binding.Focus,
				binding.Chord,
			)
		}
		seen[identity] = struct{}{}
		chord, err := compileKeyChord(binding.Chord)
		if err != nil {
			return nil, fmt.Errorf("keybinding chord %q: %w", binding.Chord, err)
		}
		sources = append(sources, compiledBindingSource{binding: binding, chord: chord})
	}
	runtimeScopes := keymap.RuntimeScopes()
	compiled := &compiledKeybindings{
		scopes: make(map[keyBindingScope]*compiledKeyScope, len(runtimeScopes)),
	}
	for _, runtimeScope := range runtimeScopes {
		scope := keyBindingScope{context: runtimeScope.Context, focus: runtimeScope.Focus}
		compiled.scopes[scope] = compileKeyScope(sources, scope)
	}
	return compiled, nil
}

func validateKeybinding(binding keymap.Binding) error {
	if err := keymap.ValidateBinding(binding); err != nil {
		return fmt.Errorf("invalid keybinding: %w", err)
	}
	return nil
}

func compileKeyScope(sources []compiledBindingSource, scope keyBindingScope) *compiledKeyScope {
	eligible := 0
	for _, source := range sources {
		if keyBindingAppliesToScope(source.binding, scope) {
			eligible++
		}
	}
	if eligible == 0 {
		return &compiledKeyScope{}
	}
	bindings := make([]keymap.Binding, 0, eligible)
	keys := make([]keyChordKey, 0, eligible)
	positions := make(map[keyChordKey]int, eligible)
	for _, source := range sources {
		binding := source.binding
		if !keyBindingAppliesToScope(binding, scope) {
			continue
		}
		if position, exists := positions[source.chord]; exists {
			if binding.Focus != keymap.FocusNone {
				bindings[position] = binding
				keys[position] = source.chord
			}
			continue
		}
		positions[source.chord] = len(bindings)
		bindings = append(bindings, binding)
		keys = append(keys, source.chord)
	}
	bindings = slices.Clip(bindings)
	chordEntries := 0
	for _, key := range keys {
		chordEntries += key.count
	}
	chords := make(map[keyChordKey]compiledChordBinding, chordEntries)
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
	compiled := &compiledKeyScope{
		bindings:       bindings,
		chords:         chords,
		bindingSummary: formatBindingSummary(bindings, 4),
	}
	if scope.context == keymap.ContextNormal {
		compiled.normalStatusBindings = bindingsForActions(bindings, normalStatusActions(scope.focus))
	}
	if scope.context == keymap.ContextCommentEdit {
		compiled.commentEditorBindings = bindingsForActions(bindings, commentEditorSummaryActions[:])
	}
	return compiled
}

func normalStatusActions(focus keymap.Focus) []keymap.Action {
	switch focus {
	case keymap.FocusFileTree:
		return []keymap.Action{
			keymap.ActionMoveDown,
			keymap.ActionActivate,
			keymap.ActionFocusNextPane,
			keymap.ActionHelp,
		}
	case keymap.FocusComments:
		return []keymap.Action{
			keymap.ActionMoveDown,
			keymap.ActionEditComment,
			keymap.ActionFocusNextPane,
			keymap.ActionHelp,
		}
	default:
		return []keymap.Action{
			keymap.ActionMoveDown,
			keymap.ActionCommentAtCursor,
			keymap.ActionFocusNextPane,
			keymap.ActionHelp,
		}
	}
}

func keyBindingAppliesToScope(binding keymap.Binding, scope keyBindingScope) bool {
	return binding.Context == scope.context &&
		(binding.Focus == keymap.FocusNone || binding.Focus == scope.focus)
}

func formatBindingSummary(bindings []keymap.Binding, limit int) string {
	var summary strings.Builder
	for index, binding := range bindings[:min(len(bindings), limit)] {
		writeBindingSummary(&summary, index, binding.Chord, binding.Action)
	}
	return summary.String()
}

func bindingsForActions(bindings []keymap.Binding, actions []keymap.Action) []keymap.Binding {
	selected := make([]keymap.Binding, 0, len(actions))
	for _, action := range actions {
		for _, binding := range bindings {
			if binding.Action != action {
				continue
			}
			selected = append(selected, binding)
			break
		}
	}
	return slices.Clip(selected)
}

func writeBindingSummary(
	summary *strings.Builder,
	index int,
	chord keymap.Chord,
	action keymap.Action,
) {
	if index != 0 {
		summary.WriteString("  ")
	}
	summary.WriteString(chord.Display())
	summary.WriteByte(' ')
	summary.WriteString(action.Description())
}

func (compiled *compiledKeybindings) scope(
	context keymap.Context,
	focus keymap.Focus,
) *compiledKeyScope {
	if compiled == nil {
		return nil
	}
	if context != keymap.ContextNormal {
		focus = keymap.FocusNone
	}
	return compiled.scopes[keyBindingScope{context: context, focus: focus}]
}

func (compiled *compiledKeybindings) resolve(
	context keymap.Context,
	focus keymap.Focus,
	strokes []string,
) keyResolution {
	key, ok := keyChordFromStrokes(strokes)
	if !ok {
		return keyResolution{}
	}
	scope := compiled.scope(context, focus)
	if scope == nil {
		return keyResolution{}
	}
	binding := scope.chords[key]
	switch {
	case binding.action != keymap.ActionNone && binding.prefix:
		return keyResolution{action: binding.action, kind: keyResolutionExactPrefix}
	case binding.action != keymap.ActionNone:
		return keyResolution{action: binding.action, kind: keyResolutionExact}
	case binding.prefix:
		return keyResolution{kind: keyResolutionPrefix}
	default:
		return keyResolution{}
	}
}

func (compiled *compiledKeybindings) bindingSummary(
	context keymap.Context,
	focus keymap.Focus,
) string {
	scope := compiled.scope(context, focus)
	if scope == nil {
		return ""
	}
	return scope.bindingSummary
}

func (compiled *compiledKeybindings) normalStatusBindings(focus keymap.Focus) []keymap.Binding {
	scope := compiled.scope(keymap.ContextNormal, focus)
	if scope == nil {
		return nil
	}
	return scope.normalStatusBindings
}

func (compiled *compiledKeybindings) commentEditorBindings() []keymap.Binding {
	scope := compiled.scope(keymap.ContextCommentEdit, keymap.FocusNone)
	if scope == nil {
		return nil
	}
	return scope.commentEditorBindings
}

func (compiled *compiledKeybindings) effectiveBindings(
	context keymap.Context,
	focus keymap.Focus,
) []keymap.Binding {
	scope := compiled.scope(context, focus)
	if scope == nil {
		return nil
	}
	return scope.bindings
}

// Settings describes settings.
type Settings struct {
	palette          Palette
	keymap           *compiledKeybindings
	syntaxSelector   review.SyntaxSelector
	exportTemplates  review.ExportTemplates
	publicationEvent review.PublicationEvent
	patchContext     review.PatchContext
	mouse            bool
	colorEnabled     bool
	wrap             bool
	diffView         DiffView
}

// PaletteRole describes palette role.
type PaletteRole uint8

// PalettePanelBackground identifies a supported palette panel background value.
const (
	PalettePanelBackground PaletteRole = iota + 1
	PalettePrimary
	PaletteSecondary
	PaletteDim
	PaletteDiffAdd
	PaletteDiffDelete
	PaletteDiffContext
	PalettePending
	PaletteFocused
	PaletteStatusBackground
	PaletteCursorBackground
	PaletteCursorForeground
	PaletteDiffAddBackground
	PaletteDiffDeleteBackground
	PaletteDiffHunk
	PaletteSyntaxKeyword
	PaletteSyntaxString
	PaletteSyntaxComment
	PaletteSyntaxNumber
	PaletteWarningForeground
	PaletteWarningBackground
	PaletteErrorForeground
	PaletteErrorBackground
	PaletteSuccessForeground
	PaletteSuccessBackground
)

// Palette represents palette.
type Palette struct {
	colors map[PaletteRole]string
}

var requiredColorPaletteRoles = [...]PaletteRole{
	PalettePanelBackground,
	PalettePrimary,
	PaletteSecondary,
	PaletteDim,
	PaletteDiffAdd,
	PaletteDiffDelete,
	PaletteDiffContext,
	PalettePending,
	PaletteFocused,
	PaletteStatusBackground,
	PaletteCursorBackground,
	PaletteCursorForeground,
}

// NewPalette constructs and validates palette.
func NewPalette(values map[PaletteRole]string) (Palette, error) {
	colors := make(map[PaletteRole]string, len(values))
	for role, value := range values {
		if role < PalettePanelBackground || role > PaletteSuccessBackground ||
			value == "" || len(value) > 64 {
			return Palette{}, errors.New("invalid presentation palette")
		}
		normalized, err := normalizePaletteColor(value)
		if err != nil {
			return Palette{}, err
		}
		colors[role] = normalized
	}
	return Palette{colors: colors}, nil
}

func (palette Palette) color(role PaletteRole, fallback string) string {
	if value, exists := palette.colors[role]; exists {
		return value
	}
	return fallback
}

func normalizePaletteColor(value string) (string, error) {
	if len(value) == 7 && value[0] == '#' {
		for _, character := range value[1:] {
			if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
				return "", errors.New("invalid hexadecimal palette color")
			}
		}
		return value, nil
	}
	normalized := strings.ToLower(value)
	normalized = strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	ansi := map[string]string{
		"reset": "", "black": "0", "red": "1", "green": "2", "yellow": "3",
		"blue": "4", "magenta": "5", "cyan": "6", "gray": "7", "grey": "7",
		"darkgray": "8", "darkgrey": "8", "lightred": "9", "lightgreen": "10",
		"lightyellow": "11", "lightblue": "12", "lightmagenta": "13",
		"lightcyan": "14", "white": "15",
	}
	color, ok := ansi[normalized]
	if !ok {
		return "", errors.New("unknown named palette color")
	}
	return color, nil
}

// SettingFeature represents setting feature.
type SettingFeature uint8

// FeatureMouse identifies a supported feature mouse value.
const (
	FeatureMouse SettingFeature = iota + 1
	FeatureColor
	FeatureWrap
)

// SettingFeatures represents setting features.
type SettingFeatures struct {
	bits uint8
}

// NewSettingFeatures constructs and validates setting features.
func NewSettingFeatures(values ...SettingFeature) (SettingFeatures, error) {
	var features SettingFeatures
	for _, value := range values {
		if value < FeatureMouse || value > FeatureWrap {
			return SettingFeatures{}, errors.New("unknown interactive setting feature")
		}
		features.bits |= 1 << (value - 1)
	}
	return features, nil
}

// Has performs the has operation.
func (features SettingFeatures) Has(value SettingFeature) bool {
	return value >= FeatureMouse && value <= FeatureWrap &&
		features.bits&(1<<(value-1)) != 0
}

func (features SettingFeatures) valid() bool {
	const validBits = 1<<FeatureWrap - 1
	return features.bits&^validBits == 0
}

// SettingsRequest describes settings request.
type SettingsRequest struct {
	Palette          Palette
	SyntaxSelector   review.SyntaxSelector
	Keybindings      []keymap.Binding
	ExportTemplates  review.ExportTemplates
	PublicationEvent review.PublicationEvent
	ContextLines     review.ContextLines
	Features         SettingFeatures
	DiffView         DiffView
}

// NewSettings constructs and validates settings.
func NewSettings(request SettingsRequest) (Settings, error) {
	features := request.Features
	diffView := request.DiffView
	contextLines := request.ContextLines
	syntaxSelector := request.SyntaxSelector
	exportTemplates := request.ExportTemplates
	publicationEvent := request.PublicationEvent
	keybindings := request.Keybindings
	if !features.valid() {
		return Settings{}, errors.New("interactive setting features contain unknown bits")
	}
	if features.Has(FeatureColor) {
		for _, role := range requiredColorPaletteRoles {
			if request.Palette.color(role, "") == "" {
				return Settings{}, fmt.Errorf(
					"color-enabled palette is missing required role %s",
					paletteRoleName(role),
				)
			}
		}
	}
	if diffView != DiffViewUnified && diffView != DiffViewSideBySide {
		return Settings{}, errors.New("invalid diff view")
	}
	if _, err := review.NewSyntaxSelector(syntaxSelector.String()); err != nil {
		return Settings{}, fmt.Errorf("syntax selector: %w", err)
	}
	if !exportTemplates.Ready() {
		return Settings{}, errors.New("export templates are required")
	}
	publicationPolicies, err := review.NewPublicationPolicies()
	if err != nil {
		return Settings{}, fmt.Errorf("publication settings: %w", err)
	}
	validatedPublication, err := review.NewPublicationOptions(
		publicationEvent,
		publicationPolicies,
	)
	if err != nil {
		return Settings{}, fmt.Errorf("publication settings: %w", err)
	}
	if validatedPublication.Event() == review.PublicationEventDraft {
		return Settings{}, errors.New("publication settings require a final GitHub review event")
	}
	publicationEvent = validatedPublication.Event()
	patchContext, err := review.ContextAround(contextLines)
	if err != nil {
		return Settings{}, err
	}
	keymap, err := compileKeybindings(keybindings)
	if err != nil {
		return Settings{}, err
	}
	return Settings{
		mouse:            features.Has(FeatureMouse),
		colorEnabled:     features.Has(FeatureColor),
		wrap:             features.Has(FeatureWrap),
		diffView:         diffView,
		patchContext:     patchContext,
		syntaxSelector:   syntaxSelector,
		exportTemplates:  exportTemplates,
		publicationEvent: publicationEvent,
		keymap:           keymap,
		palette:          request.Palette,
	}, nil
}

func paletteRoleName(role PaletteRole) string {
	switch role {
	case PalettePanelBackground:
		return "panel_bg"
	case PalettePrimary:
		return "fg_primary"
	case PaletteSecondary:
		return "fg_secondary"
	case PaletteDim:
		return "fg_dim"
	case PaletteDiffAdd:
		return "diff_add"
	case PaletteDiffDelete:
		return "diff_del"
	case PaletteDiffContext:
		return "diff_context"
	case PalettePending:
		return "pending"
	case PaletteFocused:
		return "border_focused"
	case PaletteStatusBackground:
		return "status_bar_bg"
	case PaletteCursorBackground:
		return "cursor_line_bg"
	case PaletteCursorForeground:
		return "cursor_line_fg"
	default:
		return unknownValue
	}
}

func (settings Settings) clone() Settings {
	settings.palette.colors = maps.Clone(settings.palette.colors)
	return settings
}
