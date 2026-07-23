package app

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jacoelho/rvw/internal/review"
)

// DiffView represents diff view.
type DiffView uint8

// DiffViewUnified identifies a supported diff view unified value.
const (
	DiffViewUnified DiffView = iota + 1
	DiffViewSideBySide
)

// KeyContext represents key context.
type KeyContext string

// KeyContextNormal identifies a supported key context normal value.
const (
	KeyContextNormal        KeyContext = "normal"
	KeyContextVisual        KeyContext = "visual"
	KeyContextCommentEdit   KeyContext = "comment-edit"
	KeyContextSearch        KeyContext = "search"
	KeyContextCommand       KeyContext = "command"
	KeyContextHelp          KeyContext = "help"
	KeyContextStatusDetails KeyContext = "status-details"
	KeyContextConfirm       KeyContext = "confirm"
	KeyContextExportPicker  KeyContext = "export-picker"
	KeyContextSubmit        KeyContext = "submit-preflight"
	KeyContextMinimumSize   KeyContext = "minimum-size"
)

// NewKeyContext constructs and validates key context.
func NewKeyContext(value string) (KeyContext, error) {
	context := KeyContext(value)
	switch context {
	case KeyContextNormal, KeyContextVisual, KeyContextCommentEdit,
		KeyContextSearch, KeyContextCommand, KeyContextHelp,
		KeyContextStatusDetails, KeyContextConfirm, KeyContextExportPicker,
		KeyContextSubmit, KeyContextMinimumSize:
		return context, nil
	default:
		return "", errors.New("unknown key context")
	}
}

// String returns the receiver's string value.
func (context KeyContext) String() string {
	return string(context)
}

// KeyFocus represents key focus.
type KeyFocus string

// KeyFocusNone identifies a supported key focus none value.
const (
	KeyFocusNone     KeyFocus = ""
	KeyFocusFileTree KeyFocus = "file-tree"
	KeyFocusDiff     KeyFocus = "diff"
	KeyFocusComments KeyFocus = "comments"
)

// NewKeyFocus constructs and validates key focus.
func NewKeyFocus(value string) (KeyFocus, error) {
	focus := KeyFocus(value)
	switch focus {
	case KeyFocusNone, KeyFocusFileTree, KeyFocusDiff, KeyFocusComments:
		return focus, nil
	default:
		return "", errors.New("unknown key focus")
	}
}

// String returns the receiver's string value.
func (focus KeyFocus) String() string {
	return string(focus)
}

// KeyAction represents key action.
type KeyAction string

// KeyActionNone identifies a supported key action none value.
const (
	KeyActionNone                KeyAction = ""
	KeyActionQuit                KeyAction = "quit"
	KeyActionInterrupt           KeyAction = "interrupt"
	KeyActionFocusNextPane       KeyAction = "focus-next-pane"
	KeyActionFocusPreviousPane   KeyAction = "focus-previous-pane"
	KeyActionMoveUp              KeyAction = "move-up"
	KeyActionMoveDown            KeyAction = "move-down"
	KeyActionScrollLeft          KeyAction = "scroll-left"
	KeyActionScrollRight         KeyAction = "scroll-right"
	KeyActionFirst               KeyAction = "first"
	KeyActionLast                KeyAction = "last"
	KeyActionPageUp              KeyAction = "page-up"
	KeyActionPageDown            KeyAction = "page-down"
	KeyActionHalfPageUp          KeyAction = "half-page-up"
	KeyActionHalfPageDown        KeyAction = "half-page-down"
	KeyActionActivate            KeyAction = "activate"
	KeyActionToggleFileReviewed  KeyAction = "toggle-file-reviewed"
	KeyActionToggleHunkReviewed  KeyAction = "toggle-hunk-reviewed"
	KeyActionCommentAtCursor     KeyAction = "comment-at-cursor"
	KeyActionFileComment         KeyAction = "file-comment"
	KeyActionSummaryComment      KeyAction = "summary-comment"
	KeyActionEditComment         KeyAction = "edit-comment"
	KeyActionDeleteComment       KeyAction = "delete-comment"
	KeyActionExpandContext       KeyAction = "expand-context"
	KeyActionVisualLine          KeyAction = "visual-line"
	KeyActionVisualLines         KeyAction = "visual-lines"
	KeyActionVisualHunk          KeyAction = "visual-hunk"
	KeyActionExtendUp            KeyAction = "extend-up"
	KeyActionExtendDown          KeyAction = "extend-down"
	KeyActionCommentSelection    KeyAction = "comment-selection"
	KeyActionSaveComment         KeyAction = "save-comment"
	KeyActionToggleWrap          KeyAction = "toggle-wrap"
	KeyActionToggleView          KeyAction = "toggle-view"
	KeyActionAlignTop            KeyAction = "align-top"
	KeyActionAlignCenter         KeyAction = "align-center"
	KeyActionAlignBottom         KeyAction = "align-bottom"
	KeyActionToggleDirectory     KeyAction = "toggle-directory"
	KeyActionExpandDirectory     KeyAction = "expand-directory"
	KeyActionCollapseDirectory   KeyAction = "collapse-directory"
	KeyActionToggleHiddenFiles   KeyAction = "toggle-hidden-files"
	KeyActionToggleComments      KeyAction = "toggle-comments"
	KeyActionNextFile            KeyAction = "next-file"
	KeyActionPreviousFile        KeyAction = "previous-file"
	KeyActionNextHunk            KeyAction = "next-hunk"
	KeyActionPreviousHunk        KeyAction = "previous-hunk"
	KeyActionNextComment         KeyAction = "next-comment"
	KeyActionPreviousComment     KeyAction = "previous-comment"
	KeyActionNextSearchHit       KeyAction = "next-search-hit"
	KeyActionPreviousSearchHit   KeyAction = "previous-search-hit"
	KeyActionSearch              KeyAction = "search"
	KeyActionSearchVisible       KeyAction = "search-visible"
	KeyActionFindFile            KeyAction = "find-file"
	KeyActionNextSearchScope     KeyAction = "next-search-scope"
	KeyActionPreviousSearchScope KeyAction = "previous-search-scope"
	KeyActionCommand             KeyAction = "command"
	KeyActionFilter              KeyAction = "filter"
	KeyActionCopyMarkdown        KeyAction = "copy-markdown"
	KeyActionExportMarkdown      KeyAction = "export-markdown"
	KeyActionExportJSON          KeyAction = "export-json"
	KeyActionOpenSubmit          KeyAction = "open-submit"
	KeyActionHelp                KeyAction = "help"
	KeyActionStatusDetails       KeyAction = "status-details"
	KeyActionReload              KeyAction = "reload"
	KeyActionCancel              KeyAction = "cancel"
	KeyActionAccept              KeyAction = "accept"
	KeyActionDeleteBackward      KeyAction = "delete-backward"
	KeyActionInsertNewline       KeyAction = "insert-newline"
	KeyActionConfirmYes          KeyAction = "confirm-yes"
	KeyActionConfirmNo           KeyAction = "confirm-no"
	KeyActionNextHelpPage        KeyAction = "next-help-page"
	KeyActionPreviousHelpPage    KeyAction = "previous-help-page"
	KeyActionHelpLineUp          KeyAction = "help-line-up"
	KeyActionHelpLineDown        KeyAction = "help-line-down"
	KeyActionHelpPageUp          KeyAction = "help-page-up"
	KeyActionHelpPageDown        KeyAction = "help-page-down"
	KeyActionHelpHome            KeyAction = "help-home"
	KeyActionHelpEnd             KeyAction = "help-end"
	KeyActionToggleExportUnmap   KeyAction = "toggle-export-unmappable"
	KeyActionExportSelected      KeyAction = "export-selected"
	KeyActionSubmitRecheck       KeyAction = "submit-recheck"
	KeyActionToggleStaleOverride KeyAction = "toggle-stale-head-override"
	KeyActionToggleSubmitExclude KeyAction = "toggle-submit-exclusion"
	KeyActionToggleSubmitSummary KeyAction = "toggle-submit-summary"
	KeyActionPreviousSubmitEvent KeyAction = "previous-submit-event"
	KeyActionNextSubmitEvent     KeyAction = "next-submit-event"
	KeyActionSubmitReview        KeyAction = "submit-review"
	KeyActionToggleFilePanel     KeyAction = "toggle-file-panel"
)

var validKeyActions = actionSet(
	KeyActionQuit, KeyActionInterrupt,
	KeyActionFocusNextPane, KeyActionFocusPreviousPane,
	KeyActionMoveUp, KeyActionMoveDown, KeyActionScrollLeft, KeyActionScrollRight,
	KeyActionFirst, KeyActionLast, KeyActionPageUp, KeyActionPageDown,
	KeyActionHalfPageUp, KeyActionHalfPageDown, KeyActionActivate,
	KeyActionToggleFileReviewed, KeyActionToggleHunkReviewed,
	KeyActionCommentAtCursor, KeyActionFileComment, KeyActionSummaryComment,
	KeyActionEditComment, KeyActionDeleteComment, KeyActionExpandContext,
	KeyActionVisualLine, KeyActionVisualLines, KeyActionVisualHunk,
	KeyActionExtendUp, KeyActionExtendDown, KeyActionCommentSelection,
	KeyActionSaveComment,
	KeyActionToggleWrap, KeyActionToggleView, KeyActionAlignTop,
	KeyActionAlignCenter, KeyActionAlignBottom, KeyActionToggleDirectory,
	KeyActionExpandDirectory, KeyActionCollapseDirectory, KeyActionToggleHiddenFiles,
	KeyActionToggleComments, KeyActionNextFile, KeyActionPreviousFile,
	KeyActionNextHunk, KeyActionPreviousHunk, KeyActionNextComment,
	KeyActionPreviousComment, KeyActionNextSearchHit, KeyActionPreviousSearchHit,
	KeyActionSearch, KeyActionSearchVisible, KeyActionFindFile,
	KeyActionNextSearchScope, KeyActionPreviousSearchScope,
	KeyActionCommand, KeyActionFilter, KeyActionCopyMarkdown,
	KeyActionExportMarkdown, KeyActionExportJSON, KeyActionOpenSubmit,
	KeyActionHelp, KeyActionStatusDetails, KeyActionReload, KeyActionCancel,
	KeyActionAccept, KeyActionDeleteBackward, KeyActionInsertNewline,
	KeyActionConfirmYes, KeyActionConfirmNo, KeyActionNextHelpPage,
	KeyActionPreviousHelpPage, KeyActionHelpLineUp, KeyActionHelpLineDown,
	KeyActionHelpPageUp, KeyActionHelpPageDown, KeyActionHelpHome,
	KeyActionHelpEnd, KeyActionToggleExportUnmap, KeyActionExportSelected,
	KeyActionSubmitRecheck,
	KeyActionToggleStaleOverride, KeyActionToggleSubmitExclude,
	KeyActionToggleSubmitSummary, KeyActionPreviousSubmitEvent,
	KeyActionNextSubmitEvent, KeyActionSubmitReview, KeyActionToggleFilePanel,
)

var contextKeyActions = map[KeyContext]map[KeyAction]struct{}{
	KeyContextNormal: actionSet(
		KeyActionQuit, KeyActionInterrupt,
		KeyActionFocusNextPane, KeyActionFocusPreviousPane,
		KeyActionMoveUp, KeyActionMoveDown, KeyActionScrollLeft, KeyActionScrollRight,
		KeyActionFirst, KeyActionLast, KeyActionPageUp, KeyActionPageDown,
		KeyActionHalfPageUp, KeyActionHalfPageDown, KeyActionNextFile,
		KeyActionPreviousFile, KeyActionNextHunk, KeyActionPreviousHunk,
		KeyActionNextComment, KeyActionPreviousComment, KeyActionNextSearchHit,
		KeyActionPreviousSearchHit, KeyActionCommentAtCursor, KeyActionFileComment,
		KeyActionSummaryComment, KeyActionEditComment, KeyActionDeleteComment,
		KeyActionExpandContext, KeyActionToggleFileReviewed,
		KeyActionToggleHunkReviewed, KeyActionActivate, KeyActionAccept,
		KeyActionToggleWrap, KeyActionToggleView, KeyActionAlignTop,
		KeyActionAlignCenter, KeyActionAlignBottom, KeyActionToggleDirectory,
		KeyActionExpandDirectory, KeyActionCollapseDirectory, KeyActionVisualLine,
		KeyActionVisualLines, KeyActionVisualHunk, KeyActionCommand,
		KeyActionFilter, KeyActionHelp, KeyActionStatusDetails, KeyActionSearch,
		KeyActionSearchVisible, KeyActionFindFile,
		KeyActionOpenSubmit, KeyActionReload, KeyActionCopyMarkdown,
		KeyActionExportMarkdown, KeyActionExportJSON, KeyActionToggleFilePanel,
		KeyActionToggleComments, KeyActionToggleHiddenFiles,
	),
	KeyContextVisual: actionSet(
		KeyActionCancel, KeyActionInterrupt, KeyActionMoveUp, KeyActionMoveDown,
		KeyActionPageUp, KeyActionPageDown, KeyActionExtendUp,
		KeyActionExtendDown, KeyActionCommentSelection,
	),
	KeyContextCommentEdit: actionSet(
		KeyActionCancel, KeyActionInterrupt, KeyActionDeleteBackward,
		KeyActionSaveComment, KeyActionInsertNewline,
	),
	KeyContextSearch: actionSet(
		KeyActionCancel, KeyActionInterrupt, KeyActionAccept,
		KeyActionDeleteBackward, KeyActionMoveUp, KeyActionMoveDown,
		KeyActionPageUp, KeyActionPageDown,
		KeyActionNextSearchScope, KeyActionPreviousSearchScope,
	),
	KeyContextCommand: actionSet(
		KeyActionCancel, KeyActionInterrupt, KeyActionAccept, KeyActionDeleteBackward,
	),
	KeyContextHelp: actionSet(
		KeyActionCancel, KeyActionInterrupt, KeyActionNextHelpPage,
		KeyActionPreviousHelpPage, KeyActionHelpLineUp, KeyActionHelpLineDown,
		KeyActionHelpPageUp, KeyActionHelpPageDown, KeyActionHelpHome,
		KeyActionHelpEnd,
	),
	KeyContextStatusDetails: actionSet(
		KeyActionCancel, KeyActionInterrupt, KeyActionMoveUp, KeyActionMoveDown,
		KeyActionPageUp, KeyActionPageDown,
	),
	KeyContextConfirm: actionSet(
		KeyActionCancel, KeyActionInterrupt, KeyActionConfirmYes, KeyActionConfirmNo,
	),
	KeyContextExportPicker: actionSet(
		KeyActionCancel, KeyActionInterrupt, KeyActionToggleExportUnmap, KeyActionExportSelected,
	),
	KeyContextSubmit: actionSet(
		KeyActionCancel, KeyActionInterrupt, KeyActionSubmitRecheck,
		KeyActionToggleStaleOverride, KeyActionToggleSubmitExclude,
		KeyActionToggleSubmitSummary, KeyActionPreviousSubmitEvent,
		KeyActionNextSubmitEvent, KeyActionSubmitReview,
		KeyActionMoveUp, KeyActionMoveDown, KeyActionPageUp, KeyActionPageDown,
	),
	KeyContextMinimumSize: actionSet(KeyActionQuit, KeyActionInterrupt),
}

func actionSet(values ...KeyAction) map[KeyAction]struct{} {
	result := make(map[KeyAction]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

// NewKeyAction constructs and validates key action.
func NewKeyAction(value string) (KeyAction, error) {
	action := KeyAction(value)
	if _, ok := validKeyActions[action]; !ok {
		return "", errors.New("unknown key action")
	}
	return action, nil
}

// String returns the receiver's string value.
func (action KeyAction) String() string {
	return string(action)
}

// KeyChord represents key chord.
type KeyChord string

// NewKeyChord constructs and validates key chord.
func NewKeyChord(value string) (KeyChord, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("key chord must not be empty")
	}
	return KeyChord(value), nil
}

// String returns the receiver's string value.
func (chord KeyChord) String() string {
	return string(chord)
}

// Keybinding represents keybinding.
type Keybinding struct {
	Context KeyContext
	Focus   KeyFocus
	Chord   KeyChord
	Action  KeyAction
}

const (
	maxKeybindings     = 4_096
	maxKeyChordBytes   = 128
	maxKeyChordStrokes = 4
)

type keyChordKey struct {
	strokes [maxKeyChordStrokes]string
	count   int
}

func compileKeyChord(chord KeyChord) (keyChordKey, error) {
	fields := strings.Fields(chord.String())
	if len(chord) > maxKeyChordBytes || len(fields) == 0 || len(fields) > maxKeyChordStrokes {
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
	context KeyContext
	focus   KeyFocus
}

type compiledChordBinding struct {
	action KeyAction
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
	action KeyAction
	kind   keyResolutionKind
}

func (resolution keyResolution) exact() bool {
	return resolution.kind == keyResolutionExact || resolution.kind == keyResolutionExactPrefix
}

func (resolution keyResolution) prefix() bool {
	return resolution.kind == keyResolutionPrefix || resolution.kind == keyResolutionExactPrefix
}

var (
	normalStatusSummaryActions = [...]KeyAction{
		KeyActionFocusNextPane,
		KeyActionActivate,
		KeyActionHelp,
		KeyActionQuit,
	}
	commentEditorSummaryActions = [...]KeyAction{
		KeyActionSaveComment,
		KeyActionInsertNewline,
		KeyActionCancel,
	}
)

type compiledKeyScope struct {
	chords               map[keyChordKey]compiledChordBinding
	bindingSummary       string
	normalStatusSummary  string
	commentEditorSummary string
	bindings             []Keybinding
}

type compiledKeybindings struct {
	scopes map[keyBindingScope]*compiledKeyScope
}

type compiledBindingSource struct {
	binding Keybinding
	chord   keyChordKey
}

var runtimeKeyBindingScopes = [...]keyBindingScope{
	{context: KeyContextNormal, focus: KeyFocusFileTree},
	{context: KeyContextNormal, focus: KeyFocusDiff},
	{context: KeyContextNormal, focus: KeyFocusComments},
	{context: KeyContextVisual},
	{context: KeyContextCommentEdit},
	{context: KeyContextSearch},
	{context: KeyContextCommand},
	{context: KeyContextHelp},
	{context: KeyContextStatusDetails},
	{context: KeyContextConfirm},
	{context: KeyContextExportPicker},
	{context: KeyContextSubmit},
	{context: KeyContextMinimumSize},
}

func compileKeybindings(bindings []Keybinding) (*compiledKeybindings, error) {
	if len(bindings) == 0 {
		return nil, errors.New("at least one keybinding is required")
	}
	if len(bindings) > maxKeybindings {
		return nil, fmt.Errorf("keybindings exceed supported limit %d", maxKeybindings)
	}
	type bindingIdentity struct {
		context KeyContext
		focus   KeyFocus
		chord   KeyChord
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
	compiled := &compiledKeybindings{
		scopes: make(map[keyBindingScope]*compiledKeyScope, len(runtimeKeyBindingScopes)),
	}
	for _, scope := range runtimeKeyBindingScopes {
		compiled.scopes[scope] = compileKeyScope(sources, scope)
	}
	return compiled, nil
}

func validateKeybinding(binding Keybinding) error {
	if binding.Context == "" || binding.Chord == "" || binding.Action == "" {
		return errors.New("keybindings require context, chord, and action")
	}
	if strings.TrimSpace(binding.Context.String()) != binding.Context.String() ||
		strings.TrimSpace(binding.Chord.String()) == "" ||
		strings.TrimSpace(binding.Action.String()) != binding.Action.String() ||
		strings.TrimSpace(binding.Focus.String()) != binding.Focus.String() {
		return errors.New("keybinding values must be normalized")
	}
	context, err := NewKeyContext(binding.Context.String())
	if err != nil || context != binding.Context {
		return fmt.Errorf("keybinding context %q: %w", binding.Context, err)
	}
	focus, err := NewKeyFocus(binding.Focus.String())
	if err != nil || focus != binding.Focus {
		return fmt.Errorf("keybinding focus %q: %w", binding.Focus, err)
	}
	action, err := NewKeyAction(binding.Action.String())
	if err != nil || action != binding.Action {
		return fmt.Errorf("keybinding action %q: %w", binding.Action, err)
	}
	if _, ok := contextKeyActions[context][action]; !ok {
		return fmt.Errorf(
			"keybinding action %q is not available in context %q",
			action,
			context,
		)
	}
	if focus != KeyFocusNone && context != KeyContextNormal {
		return fmt.Errorf(
			"keybinding context %q does not accept focus %q",
			context,
			focus,
		)
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
	bindings := make([]Keybinding, 0, eligible)
	keys := make([]keyChordKey, 0, eligible)
	positions := make(map[keyChordKey]int, eligible)
	for _, source := range sources {
		binding := source.binding
		if !keyBindingAppliesToScope(binding, scope) {
			continue
		}
		if position, exists := positions[source.chord]; exists {
			if binding.Focus != KeyFocusNone {
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
	if scope.context == KeyContextNormal {
		compiled.normalStatusSummary = formatActionSummary(bindings, normalStatusSummaryActions[:])
	}
	if scope.context == KeyContextCommentEdit {
		compiled.commentEditorSummary = formatActionSummary(bindings, commentEditorSummaryActions[:])
	}
	return compiled
}

func keyBindingAppliesToScope(binding Keybinding, scope keyBindingScope) bool {
	return binding.Context == scope.context &&
		(binding.Focus == KeyFocusNone || binding.Focus == scope.focus)
}

func formatBindingSummary(bindings []Keybinding, limit int) string {
	var summary strings.Builder
	for index, binding := range bindings[:min(len(bindings), limit)] {
		writeBindingSummary(&summary, index, binding.Chord, binding.Action)
	}
	return summary.String()
}

func formatActionSummary(bindings []Keybinding, actions []KeyAction) string {
	var summary strings.Builder
	count := 0
	for _, action := range actions {
		for _, binding := range bindings {
			if binding.Action != action {
				continue
			}
			writeBindingSummary(&summary, count, binding.Chord, action)
			count++
			break
		}
	}
	return summary.String()
}

func writeBindingSummary(
	summary *strings.Builder,
	index int,
	chord KeyChord,
	action KeyAction,
) {
	if index != 0 {
		summary.WriteString("  ")
	}
	summary.WriteString(chord.String())
	summary.WriteByte(' ')
	summary.WriteString(action.String())
}

func (compiled *compiledKeybindings) scope(
	context KeyContext,
	focus KeyFocus,
) *compiledKeyScope {
	if compiled == nil {
		return nil
	}
	if context != KeyContextNormal {
		focus = KeyFocusNone
	}
	return compiled.scopes[keyBindingScope{context: context, focus: focus}]
}

func (compiled *compiledKeybindings) resolve(
	context KeyContext,
	focus KeyFocus,
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
	case binding.action != KeyActionNone && binding.prefix:
		return keyResolution{action: binding.action, kind: keyResolutionExactPrefix}
	case binding.action != KeyActionNone:
		return keyResolution{action: binding.action, kind: keyResolutionExact}
	case binding.prefix:
		return keyResolution{kind: keyResolutionPrefix}
	default:
		return keyResolution{}
	}
}

func (compiled *compiledKeybindings) bindingSummary(
	context KeyContext,
	focus KeyFocus,
) string {
	scope := compiled.scope(context, focus)
	if scope == nil {
		return ""
	}
	return scope.bindingSummary
}

func (compiled *compiledKeybindings) normalStatusSummary(focus KeyFocus) string {
	scope := compiled.scope(KeyContextNormal, focus)
	if scope == nil {
		return ""
	}
	return scope.normalStatusSummary
}

func (compiled *compiledKeybindings) commentEditorSummary() string {
	scope := compiled.scope(KeyContextCommentEdit, KeyFocusNone)
	if scope == nil {
		return ""
	}
	return scope.commentEditorSummary
}

func (compiled *compiledKeybindings) effectiveBindings(
	context KeyContext,
	focus KeyFocus,
) []Keybinding {
	scope := compiled.scope(context, focus)
	if scope == nil {
		return nil
	}
	return scope.bindings
}

// Settings describes settings.
type Settings struct {
	palette         Palette
	keymap          *compiledKeybindings
	syntaxSelector  review.SyntaxSelector
	exportTemplates review.ExportTemplates
	publication     review.PublicationOptions
	patchContext    review.PatchContext
	mouse           bool
	colorEnabled    bool
	wrap            bool
	diffView        DiffView
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
	PaletteDiffAddBackground
	PaletteDiffDeleteBackground
	PaletteDiffHunk
	PaletteSyntaxKeyword
	PaletteSyntaxString
	PaletteSyntaxComment
	PaletteSyntaxNumber
	PaletteWarningForeground
	PaletteWarningBackground
)

// Palette represents palette.
type Palette struct {
	colors map[PaletteRole]string
}

// NewPalette constructs and validates palette.
func NewPalette(values map[PaletteRole]string) (Palette, error) {
	colors := make(map[PaletteRole]string, len(values))
	for role, value := range values {
		if role < PalettePanelBackground || role > PaletteWarningBackground ||
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
	Palette         Palette
	SyntaxSelector  review.SyntaxSelector
	Keybindings     []Keybinding
	ExportTemplates review.ExportTemplates
	Publication     review.PublicationOptions
	ContextLines    review.ContextLines
	Features        SettingFeatures
	DiffView        DiffView
}

// NewSettings constructs and validates settings.
func NewSettings(request SettingsRequest) (Settings, error) {
	features := request.Features
	diffView := request.DiffView
	contextLines := request.ContextLines
	syntaxSelector := request.SyntaxSelector
	exportTemplates := request.ExportTemplates
	publication := request.Publication
	keybindings := request.Keybindings
	if !features.valid() {
		return Settings{}, errors.New("interactive setting features contain unknown bits")
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
	validatedPublication, err := review.NewPublicationOptions(
		publication.Event(),
		publication.Policies(),
	)
	if err != nil {
		return Settings{}, fmt.Errorf("publication settings: %w", err)
	}
	if validatedPublication.Event() == review.PublicationEventDraft {
		return Settings{}, errors.New("publication settings require a final GitHub review event")
	}
	validatedPublication, err = validatedPublication.WithExcludedComments(
		publication.ExcludedComments(),
	)
	if err != nil {
		return Settings{}, fmt.Errorf("publication settings: %w", err)
	}
	publication = validatedPublication
	patchContext, err := review.ContextAround(contextLines)
	if err != nil {
		return Settings{}, err
	}
	keymap, err := compileKeybindings(keybindings)
	if err != nil {
		return Settings{}, err
	}
	return Settings{
		mouse:           features.Has(FeatureMouse),
		colorEnabled:    features.Has(FeatureColor),
		wrap:            features.Has(FeatureWrap),
		diffView:        diffView,
		patchContext:    patchContext,
		syntaxSelector:  syntaxSelector,
		exportTemplates: exportTemplates,
		publication:     publication,
		keymap:          keymap,
		palette:         request.Palette,
	}, nil
}

func (settings Settings) clone() (Settings, error) {
	publication, err := settings.publication.WithExcludedComments(
		settings.publication.ExcludedComments(),
	)
	if err != nil {
		return Settings{}, err
	}
	settings.publication = publication
	settings.palette.colors = maps.Clone(settings.palette.colors)
	return settings, nil
}
