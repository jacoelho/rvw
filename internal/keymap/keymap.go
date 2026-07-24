// Package keymap owns rvw's configurable keyboard vocabulary and policy.
package keymap

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// MaxBindings is the maximum supported effective or user binding count.
	MaxBindings = 4_096
	// MaxChordBytes is the maximum canonical chord size.
	MaxChordBytes = 128
	// MaxChordStrokes is the maximum stroke count in one chord.
	MaxChordStrokes = 4
)

const (
	strokeEnter = "enter"
	strokeSpace = "space"
)

// Context identifies one mutually exclusive input-routing context.
type Context string

// ContextNormal identifies the default workspace input context.
const (
	ContextNormal        Context = "normal"
	ContextVisual        Context = "visual"
	ContextCommentEdit   Context = "comment-edit"
	ContextSearch        Context = "search"
	ContextCommand       Context = "command"
	ContextHelp          Context = "help"
	ContextStatusDetails Context = "status-details"
	ContextConfirm       Context = "confirm"
	ContextExportPicker  Context = "export-picker"
	ContextSubmit        Context = "submit-preflight"
	ContextMinimumSize   Context = "minimum-size"
)

var contexts = [...]Context{
	ContextNormal,
	ContextVisual,
	ContextCommentEdit,
	ContextSearch,
	ContextCommand,
	ContextHelp,
	ContextStatusDetails,
	ContextConfirm,
	ContextExportPicker,
	ContextSubmit,
	ContextMinimumSize,
}

// NewContext validates a context.
func NewContext(value string) (Context, error) {
	context := Context(value)
	if slices.Contains(contexts[:], context) {
		return context, nil
	}
	return "", errors.New("unknown key context")
}

func (context Context) String() string { return string(context) }

// Contexts returns every supported context in stable order.
func Contexts() []Context { return slices.Clone(contexts[:]) }

// Focus identifies the optional pane-specific scope within the normal context.
type Focus string

// FocusNone identifies a binding shared by every pane in its context.
const (
	FocusNone     Focus = ""
	FocusFileTree Focus = "file-tree"
	FocusDiff     Focus = "diff"
	FocusComments Focus = "comments"
)

var focuses = [...]Focus{FocusNone, FocusFileTree, FocusDiff, FocusComments}

// NewFocus validates a focus.
func NewFocus(value string) (Focus, error) {
	focus := Focus(value)
	if slices.Contains(focuses[:], focus) {
		return focus, nil
	}
	return "", errors.New("unknown key focus")
}

func (focus Focus) String() string { return string(focus) }

// Focuses returns every supported focus in stable order.
func Focuses() []Focus { return slices.Clone(focuses[:]) }

// Action identifies a semantic application action.
type Action string

// ActionNone identifies the absence of a resolved action.
const (
	ActionNone                Action = ""
	ActionQuit                Action = "quit"
	ActionInterrupt           Action = "interrupt"
	ActionFocusNextPane       Action = "focus-next-pane"
	ActionFocusPreviousPane   Action = "focus-previous-pane"
	ActionMoveUp              Action = "move-up"
	ActionMoveDown            Action = "move-down"
	ActionScrollLeft          Action = "scroll-left"
	ActionScrollRight         Action = "scroll-right"
	ActionFirst               Action = "first"
	ActionLast                Action = "last"
	ActionPageUp              Action = "page-up"
	ActionPageDown            Action = "page-down"
	ActionHalfPageUp          Action = "half-page-up"
	ActionHalfPageDown        Action = "half-page-down"
	ActionActivate            Action = "activate"
	ActionToggleFileReviewed  Action = "toggle-file-reviewed"
	ActionToggleHunkReviewed  Action = "toggle-hunk-reviewed"
	ActionCommentAtCursor     Action = "comment-at-cursor"
	ActionFileComment         Action = "file-comment"
	ActionSummaryComment      Action = "summary-comment"
	ActionEditComment         Action = "edit-comment"
	ActionDeleteComment       Action = "delete-comment"
	ActionExpandContext       Action = "expand-context"
	ActionVisualLine          Action = "visual-line"
	ActionVisualLines         Action = "visual-lines"
	ActionVisualHunk          Action = "visual-hunk"
	ActionExtendUp            Action = "extend-up"
	ActionExtendDown          Action = "extend-down"
	ActionCommentSelection    Action = "comment-selection"
	ActionSaveComment         Action = "save-comment"
	ActionToggleWrap          Action = "toggle-wrap"
	ActionToggleView          Action = "toggle-view"
	ActionAlignTop            Action = "align-top"
	ActionAlignCenter         Action = "align-center"
	ActionAlignBottom         Action = "align-bottom"
	ActionToggleDirectory     Action = "toggle-directory"
	ActionExpandDirectory     Action = "expand-directory"
	ActionCollapseDirectory   Action = "collapse-directory"
	ActionToggleHiddenFiles   Action = "toggle-hidden-files"
	ActionToggleComments      Action = "toggle-comments"
	ActionNextFile            Action = "next-file"
	ActionPreviousFile        Action = "previous-file"
	ActionNextHunk            Action = "next-hunk"
	ActionPreviousHunk        Action = "previous-hunk"
	ActionNextComment         Action = "next-comment"
	ActionPreviousComment     Action = "previous-comment"
	ActionNextSearchHit       Action = "next-search-hit"
	ActionPreviousSearchHit   Action = "previous-search-hit"
	ActionSearch              Action = "search"
	ActionSearchVisible       Action = "search-visible"
	ActionFindFile            Action = "find-file"
	ActionNextSearchScope     Action = "next-search-scope"
	ActionPreviousSearchScope Action = "previous-search-scope"
	ActionCommand             Action = "command"
	ActionFilter              Action = "filter"
	ActionCopyMarkdown        Action = "copy-markdown"
	ActionExportMarkdown      Action = "export-markdown"
	ActionExportJSON          Action = "export-json"
	ActionOpenSubmit          Action = "open-submit"
	ActionHelp                Action = "help"
	ActionStatusDetails       Action = "status-details"
	ActionReload              Action = "reload"
	ActionCancel              Action = "cancel"
	ActionAccept              Action = "accept"
	ActionDeleteBackward      Action = "delete-backward"
	ActionInsertNewline       Action = "insert-newline"
	ActionConfirmYes          Action = "confirm-yes"
	ActionConfirmNo           Action = "confirm-no"
	ActionNextHelpPage        Action = "next-help-page"
	ActionPreviousHelpPage    Action = "previous-help-page"
	ActionHelpLineUp          Action = "help-line-up"
	ActionHelpLineDown        Action = "help-line-down"
	ActionHelpPageUp          Action = "help-page-up"
	ActionHelpPageDown        Action = "help-page-down"
	ActionHelpHome            Action = "help-home"
	ActionHelpEnd             Action = "help-end"
	ActionToggleExportUnmap   Action = "toggle-export-unmappable"
	ActionExportSelected      Action = "export-selected"
	ActionSubmitRecheck       Action = "submit-recheck"
	ActionToggleStaleOverride Action = "toggle-stale-head-override"
	ActionToggleSubmitExclude Action = "toggle-submit-exclusion"
	ActionToggleSubmitSummary Action = "toggle-submit-summary"
	ActionPreviousSubmitEvent Action = "previous-submit-event"
	ActionNextSubmitEvent     Action = "next-submit-event"
	ActionSubmitReview        Action = "submit-review"
	ActionToggleFilePanel     Action = "toggle-file-panel"
)

var actionDescriptions = map[Action]string{
	ActionQuit:                "quit",
	ActionInterrupt:           "interrupt",
	ActionFocusNextPane:       "next pane",
	ActionFocusPreviousPane:   "previous pane",
	ActionMoveUp:              "previous row",
	ActionMoveDown:            "next row",
	ActionScrollLeft:          "scroll left",
	ActionScrollRight:         "scroll right",
	ActionFirst:               "first row",
	ActionLast:                "last row",
	ActionPageUp:              "page up",
	ActionPageDown:            "page down",
	ActionHalfPageUp:          "half page up",
	ActionHalfPageDown:        "half page down",
	ActionActivate:            "open",
	ActionToggleFileReviewed:  "review file",
	ActionToggleHunkReviewed:  "review hunk",
	ActionCommentAtCursor:     "comment",
	ActionFileComment:         "comment on file",
	ActionSummaryComment:      "summary comment",
	ActionEditComment:         "edit comment",
	ActionDeleteComment:       "delete comment",
	ActionExpandContext:       "expand context",
	ActionVisualLine:          "select line",
	ActionVisualLines:         "select lines",
	ActionVisualHunk:          "select hunk",
	ActionExtendUp:            "extend up",
	ActionExtendDown:          "extend down",
	ActionCommentSelection:    "comment selection",
	ActionSaveComment:         "save",
	ActionToggleWrap:          "toggle wrap",
	ActionToggleView:          "toggle diff layout",
	ActionAlignTop:            "align top",
	ActionAlignCenter:         "align center",
	ActionAlignBottom:         "align bottom",
	ActionToggleDirectory:     "open/close directory",
	ActionExpandDirectory:     "open directory",
	ActionCollapseDirectory:   "close directory",
	ActionToggleHiddenFiles:   "show/hide filtered files",
	ActionToggleComments:      "show/hide comments",
	ActionNextFile:            "next file",
	ActionPreviousFile:        "previous file",
	ActionNextHunk:            "next hunk",
	ActionPreviousHunk:        "previous hunk",
	ActionNextComment:         "next comment",
	ActionPreviousComment:     "previous comment",
	ActionNextSearchHit:       "next match",
	ActionPreviousSearchHit:   "previous match",
	ActionSearch:              "search",
	ActionSearchVisible:       "search visible diff",
	ActionFindFile:            "find file",
	ActionNextSearchScope:     "next search scope",
	ActionPreviousSearchScope: "previous search scope",
	ActionCommand:             "command",
	ActionFilter:              "filter files",
	ActionCopyMarkdown:        "copy Markdown",
	ActionExportMarkdown:      "write Markdown",
	ActionExportJSON:          "write JSON",
	ActionOpenSubmit:          "submit review",
	ActionHelp:                "show keybindings",
	ActionStatusDetails:       "show status details",
	ActionReload:              "reload",
	ActionCancel:              "cancel",
	ActionAccept:              "accept",
	ActionDeleteBackward:      "delete backward",
	ActionInsertNewline:       "new line",
	ActionConfirmYes:          "yes",
	ActionConfirmNo:           "no",
	ActionNextHelpPage:        "next help page",
	ActionPreviousHelpPage:    "previous help page",
	ActionHelpLineUp:          "up",
	ActionHelpLineDown:        "down",
	ActionHelpPageUp:          "page up",
	ActionHelpPageDown:        "page down",
	ActionHelpHome:            "first binding",
	ActionHelpEnd:             "last binding",
	ActionToggleExportUnmap:   "include unmappable comments",
	ActionExportSelected:      "export",
	ActionSubmitRecheck:       "recheck",
	ActionToggleStaleOverride: "allow stale head",
	ActionToggleSubmitExclude: "include/exclude comment",
	ActionToggleSubmitSummary: "include/exclude summary",
	ActionPreviousSubmitEvent: "previous review action",
	ActionNextSubmitEvent:     "next review action",
	ActionSubmitReview:        "publish review",
	ActionToggleFilePanel:     "show/hide files",
}

// NewAction validates an action.
func NewAction(value string) (Action, error) {
	action := Action(value)
	if _, ok := actionDescriptions[action]; ok {
		return action, nil
	}
	return "", errors.New("unknown key action")
}

func (action Action) String() string { return string(action) }

// Description returns concise user-facing help text.
func (action Action) Description() string { return actionDescriptions[action] }

// Actions returns every supported action in stable lexical order.
func Actions() []Action {
	actions := make([]Action, 0, len(actionDescriptions))
	for action := range actionDescriptions {
		actions = append(actions, action)
	}
	slices.Sort(actions)
	return actions
}

// Chord is a canonical sequence of one through four key strokes.
type Chord string

// NewChord normalizes and validates a chord.
func NewChord(value string) (Chord, error) {
	if value == " " {
		value = strokeSpace
	}
	if len(value) > MaxChordBytes {
		return "", errors.New("keybinding chord must have 1..4 strokes and at most 128 bytes")
	}
	fields := strings.Fields(value)
	if len(fields) == 0 || len(fields) > MaxChordStrokes {
		return "", errors.New("keybinding chord must have 1..4 strokes and at most 128 bytes")
	}
	for index, field := range fields {
		fields[index] = NormalizeStroke(field)
		if fields[index] == "" {
			return "", errors.New("keybinding chord contains an empty stroke")
		}
	}
	return Chord(strings.Join(fields, " ")), nil
}

func (chord Chord) String() string { return string(chord) }

// Strokes returns the canonical strokes in the chord.
func (chord Chord) Strokes() []string { return strings.Fields(chord.String()) }

// Display returns compact user-facing key notation.
func (chord Chord) Display() string {
	strokes := chord.Strokes()
	for index, stroke := range strokes {
		strokes[index] = displayStroke(stroke)
	}
	return strings.Join(strokes, " ")
}

// NormalizeStroke converts Bubble Tea key strings and configured notation to
// the canonical matching form while preserving case-sensitive printable keys.
func NormalizeStroke(value string) string {
	if value == " " {
		return strokeSpace
	}
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "+", "-")
	if len([]rune(value)) == 1 {
		return value
	}
	switch strings.ToLower(value) {
	case "return":
		return strokeEnter
	case "escape":
		return "esc"
	default:
		return strings.ToLower(value)
	}
}

func displayStroke(stroke string) string {
	switch stroke {
	case strokeEnter:
		return "Enter"
	case "esc":
		return "Esc"
	case strokeSpace:
		return "Space"
	case "tab":
		return "Tab"
	case "up":
		return "Up"
	case "down":
		return "Down"
	case "left":
		return "Left"
	case "right":
		return "Right"
	case "pgup":
		return "PgUp"
	case "pgdown":
		return "PgDn"
	case "home":
		return "Home"
	case "end":
		return "End"
	}
	if len(stroke) > 1 && stroke[0] == 'f' && strings.IndexFunc(stroke[1:], func(value rune) bool {
		return value < '0' || value > '9'
	}) < 0 {
		return strings.ToUpper(stroke)
	}
	parts := strings.Split(stroke, "-")
	modified := false
	for index, part := range parts {
		switch part {
		case "ctrl":
			parts[index] = "Ctrl"
			modified = modified || index == 0
		case "shift":
			parts[index] = "Shift"
			modified = modified || index == 0
		case "alt":
			parts[index] = "Alt"
			modified = modified || index == 0
		case "super":
			parts[index] = "Super"
			modified = modified || index == 0
		default:
			if modified && index > 0 && utf8.RuneCountInString(part) == 1 {
				parts[index] = strings.ToUpper(part)
			}
		}
	}
	return strings.Join(parts, "-")
}

// Binding maps one chord in one input scope to an action. Unbind is accepted
// only while resolving user configuration; effective runtime bindings never
// contain it.
type Binding struct {
	Context Context
	Focus   Focus
	Chord   Chord
	Action  Action
	Unbind  bool
}

type bindingIdentity struct {
	context Context
	focus   Focus
	chord   Chord
}

// Scope identifies a concrete runtime context and focus pair.
type Scope struct {
	Context Context
	Focus   Focus
}

var runtimeScopes = [...]Scope{
	{Context: ContextNormal, Focus: FocusFileTree},
	{Context: ContextNormal, Focus: FocusDiff},
	{Context: ContextNormal, Focus: FocusComments},
	{Context: ContextVisual},
	{Context: ContextCommentEdit},
	{Context: ContextSearch},
	{Context: ContextCommand},
	{Context: ContextHelp},
	{Context: ContextStatusDetails},
	{Context: ContextConfirm},
	{Context: ContextExportPicker},
	{Context: ContextSubmit},
	{Context: ContextMinimumSize},
}

// RuntimeScopes returns every concrete input-routing scope.
func RuntimeScopes() []Scope { return slices.Clone(runtimeScopes[:]) }

// ValidateBinding validates one canonical effective runtime binding.
func ValidateBinding(binding Binding) error {
	if binding.Unbind || binding.Context == "" || binding.Chord == "" || binding.Action == "" {
		return errors.New("keybindings require context, chord, and action")
	}
	context, err := NewContext(binding.Context.String())
	if err != nil {
		return fmt.Errorf("keybinding context %q: %w", binding.Context, err)
	}
	if context != binding.Context {
		return fmt.Errorf("keybinding context %q is not canonical", binding.Context)
	}
	focus, err := NewFocus(binding.Focus.String())
	if err != nil {
		return fmt.Errorf("keybinding focus %q: %w", binding.Focus, err)
	}
	if focus != binding.Focus {
		return fmt.Errorf("keybinding focus %q is not canonical", binding.Focus)
	}
	chord, err := NewChord(binding.Chord.String())
	if err != nil {
		return fmt.Errorf("keybinding chord %q is not canonical: %w", binding.Chord, err)
	}
	if chord != binding.Chord {
		return fmt.Errorf("keybinding chord %q is not canonical", binding.Chord)
	}
	action, err := NewAction(binding.Action.String())
	if err != nil {
		return fmt.Errorf("keybinding action %q: %w", binding.Action, err)
	}
	if action != binding.Action {
		return fmt.Errorf("keybinding action %q is not canonical", binding.Action)
	}
	if !ActionAllowed(context, action) {
		return fmt.Errorf("keybinding action %q is not available in context %q", action, context)
	}
	if focus != FocusNone && context != ContextNormal {
		return fmt.Errorf("keybinding context %q does not accept focus %q", context, focus)
	}
	return nil
}

// NormalizeUserBinding canonicalizes and validates one configuration override.
func NormalizeUserBinding(binding Binding) (Binding, error) {
	switch binding.Context {
	case Context("normal.tree"):
		if binding.Focus != FocusNone {
			return Binding{}, errors.New("inline normal context cannot also set focus")
		}
		binding.Context, binding.Focus = ContextNormal, FocusFileTree
	case Context("normal.diff"):
		if binding.Focus != FocusNone {
			return Binding{}, errors.New("inline normal context cannot also set focus")
		}
		binding.Context, binding.Focus = ContextNormal, FocusDiff
	case Context("normal.comments"):
		if binding.Focus != FocusNone {
			return Binding{}, errors.New("inline normal context cannot also set focus")
		}
		binding.Context, binding.Focus = ContextNormal, FocusComments
	default:
	}
	context, err := NewContext(binding.Context.String())
	if err != nil {
		return Binding{}, err
	}
	focus, err := NewFocus(binding.Focus.String())
	if err != nil {
		return Binding{}, err
	}
	if context != ContextNormal && focus != FocusNone {
		return Binding{}, errors.New("focus is allowed only for normal")
	}
	chord, err := NewChord(binding.Chord.String())
	if err != nil {
		return Binding{}, err
	}
	if slices.Contains(chord.Strokes(), "ctrl-c") {
		return Binding{}, errors.New("Ctrl-C is reserved")
	}
	binding.Context, binding.Focus, binding.Chord = context, focus, chord
	hasAction := binding.Action != ActionNone
	if hasAction == binding.Unbind {
		return Binding{}, errors.New("binding requires exactly one of action or unbind")
	}
	if binding.Unbind {
		return binding, nil
	}
	action, err := NewAction(binding.Action.String())
	if err != nil {
		return Binding{}, err
	}
	binding.Action = action
	if !ActionAllowed(context, action) {
		return Binding{}, fmt.Errorf("action %q is unreachable in context %q", action, context)
	}
	return binding, nil
}

// Resolve overlays user bindings on the defaults and validates the complete
// effective runtime keymap.
func Resolve(bindings []Binding) ([]Binding, error) {
	if len(bindings) > MaxBindings {
		return nil, fmt.Errorf("keybindings exceed %d entries", MaxBindings)
	}
	effective := make(map[bindingIdentity]Binding, len(defaultBindings)+len(bindings))
	for _, binding := range defaultBindings {
		key := bindingIdentity{binding.Context, binding.Focus, binding.Chord}
		effective[key] = binding
	}
	userKeys := make(map[bindingIdentity]struct{}, len(bindings))
	for index, binding := range bindings {
		normalized, err := NormalizeUserBinding(binding)
		if err != nil {
			return nil, fmt.Errorf("keybinding %d: %w", index+1, err)
		}
		key := bindingIdentity{normalized.Context, normalized.Focus, normalized.Chord}
		if _, exists := userKeys[key]; exists {
			return nil, fmt.Errorf("duplicate binding for %s", normalized.Chord.Display())
		}
		userKeys[key] = struct{}{}
		if normalized.Unbind {
			delete(effective, key)
		} else {
			effective[key] = normalized
		}
	}
	if len(effective) > MaxBindings {
		return nil, fmt.Errorf("effective keybindings exceed %d entries", MaxBindings)
	}
	if err := validateEffective(effective); err != nil {
		return nil, err
	}
	result := make([]Binding, 0, len(effective))
	for _, binding := range effective {
		result = append(result, binding)
	}
	slices.SortFunc(result, compareBinding)
	return result, nil
}

func compareBinding(left, right Binding) int {
	if result := strings.Compare(left.Context.String(), right.Context.String()); result != 0 {
		return result
	}
	if result := strings.Compare(left.Focus.String(), right.Focus.String()); result != 0 {
		return result
	}
	return strings.Compare(left.Chord.String(), right.Chord.String())
}

func validateEffective(effective map[bindingIdentity]Binding) error {
	for _, scope := range runtimeScopes {
		bindings := make(map[Chord]Binding)
		for key, binding := range effective {
			if key.context == scope.Context && key.focus == FocusNone {
				bindings[key.chord] = binding
			}
		}
		for key, binding := range effective {
			if key.context == scope.Context && key.focus == scope.Focus && key.focus != FocusNone {
				bindings[key.chord] = binding
			}
		}
		if err := ValidateScope(scope.Context, scope.Focus, bindings); err != nil {
			return err
		}
	}
	return nil
}

// ValidateScope validates prefix uniqueness and required actions for one
// concrete runtime scope.
func ValidateScope(context Context, focus Focus, bindings map[Chord]Binding) error {
	if _, err := NewContext(context.String()); err != nil {
		return fmt.Errorf("scope context %q: %w", context, err)
	}
	if _, err := NewFocus(focus.String()); err != nil {
		return fmt.Errorf("scope focus %q: %w", focus, err)
	}
	if context != ContextNormal && focus != FocusNone {
		return fmt.Errorf("scope context %q does not accept focus %q", context, focus)
	}
	keys := make([]Chord, 0, len(bindings))
	actions := make(map[Action]struct{}, len(bindings))
	for chord, binding := range bindings {
		if err := ValidateBinding(binding); err != nil {
			return err
		}
		if chord != binding.Chord {
			return fmt.Errorf("scope chord %q does not match binding chord %q", chord, binding.Chord)
		}
		if binding.Context != context {
			return fmt.Errorf("scope context %q contains binding from context %q", context, binding.Context)
		}
		if binding.Focus != FocusNone && binding.Focus != focus {
			return fmt.Errorf("scope focus %q contains binding from focus %q", focus, binding.Focus)
		}
		keys = append(keys, chord)
		actions[binding.Action] = struct{}{}
	}
	slices.Sort(keys)
	for index := 1; index < len(keys); index++ {
		if chordPrefix(keys[index-1], keys[index]) {
			return fmt.Errorf(
				"context %q focus %q has prefix-conflicting chord %q",
				context,
				focus,
				keys[index],
			)
		}
	}
	for _, action := range requiredActions[context] {
		if _, ok := actions[action]; !ok {
			return fmt.Errorf(
				"context %q focus %q is missing required action %q",
				context,
				focus,
				action,
			)
		}
	}
	return nil
}

func chordPrefix(left, right Chord) bool {
	leftStrokes := left.Strokes()
	rightStrokes := right.Strokes()
	return len(leftStrokes) < len(rightStrokes) &&
		slices.Equal(leftStrokes, rightStrokes[:len(leftStrokes)])
}

// ActionAllowed reports whether an action is reachable in a context.
func ActionAllowed(context Context, action Action) bool {
	_, ok := contextActions[context][action]
	return ok
}

// AllowedActions returns a context's actions in stable lexical order.
func AllowedActions(context Context) []Action {
	actions := make([]Action, 0, len(contextActions[context]))
	for action := range contextActions[context] {
		actions = append(actions, action)
	}
	slices.Sort(actions)
	return actions
}

// RequiredActions returns the actions that an effective scope cannot unbind.
func RequiredActions(context Context) []Action {
	return slices.Clone(requiredActions[context])
}

func actionSet(values ...Action) map[Action]struct{} {
	result := make(map[Action]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

var requiredActions = map[Context][]Action{
	ContextNormal:        {ActionQuit, ActionInterrupt},
	ContextVisual:        {ActionCancel, ActionInterrupt},
	ContextCommentEdit:   {ActionCancel, ActionInterrupt, ActionSaveComment},
	ContextSearch:        {ActionCancel, ActionInterrupt},
	ContextCommand:       {ActionCancel, ActionInterrupt},
	ContextHelp:          {ActionCancel, ActionInterrupt},
	ContextStatusDetails: {ActionCancel, ActionInterrupt},
	ContextConfirm:       {ActionCancel, ActionInterrupt, ActionConfirmYes, ActionConfirmNo},
	ContextExportPicker:  {ActionCancel, ActionInterrupt, ActionExportSelected},
	ContextSubmit:        {ActionCancel, ActionInterrupt, ActionSubmitReview},
	ContextMinimumSize:   {ActionQuit, ActionInterrupt},
}

var contextActions = map[Context]map[Action]struct{}{
	ContextNormal: actionSet(
		ActionQuit, ActionInterrupt,
		ActionFocusNextPane, ActionFocusPreviousPane,
		ActionMoveUp, ActionMoveDown, ActionScrollLeft, ActionScrollRight,
		ActionFirst, ActionLast, ActionPageUp, ActionPageDown,
		ActionHalfPageUp, ActionHalfPageDown, ActionNextFile,
		ActionPreviousFile, ActionNextHunk, ActionPreviousHunk,
		ActionNextComment, ActionPreviousComment, ActionNextSearchHit,
		ActionPreviousSearchHit, ActionCommentAtCursor, ActionFileComment,
		ActionSummaryComment, ActionEditComment, ActionDeleteComment,
		ActionExpandContext, ActionToggleFileReviewed,
		ActionToggleHunkReviewed, ActionActivate, ActionAccept,
		ActionToggleWrap, ActionToggleView, ActionAlignTop,
		ActionAlignCenter, ActionAlignBottom, ActionToggleDirectory,
		ActionExpandDirectory, ActionCollapseDirectory, ActionVisualLine,
		ActionVisualLines, ActionVisualHunk, ActionCommand,
		ActionFilter, ActionHelp, ActionStatusDetails, ActionSearch,
		ActionSearchVisible, ActionFindFile,
		ActionOpenSubmit, ActionReload, ActionCopyMarkdown,
		ActionExportMarkdown, ActionExportJSON, ActionToggleFilePanel,
		ActionToggleComments, ActionToggleHiddenFiles,
	),
	ContextVisual: actionSet(
		ActionCancel, ActionInterrupt, ActionMoveUp, ActionMoveDown,
		ActionPageUp, ActionPageDown, ActionExtendUp,
		ActionExtendDown, ActionCommentSelection,
	),
	ContextCommentEdit: actionSet(
		ActionCancel, ActionInterrupt, ActionDeleteBackward,
		ActionSaveComment, ActionInsertNewline,
	),
	ContextSearch: actionSet(
		ActionCancel, ActionInterrupt, ActionAccept,
		ActionDeleteBackward, ActionMoveUp, ActionMoveDown,
		ActionPageUp, ActionPageDown,
		ActionNextSearchScope, ActionPreviousSearchScope,
	),
	ContextCommand: actionSet(
		ActionCancel, ActionInterrupt, ActionAccept, ActionDeleteBackward,
	),
	ContextHelp: actionSet(
		ActionCancel, ActionInterrupt, ActionNextHelpPage,
		ActionPreviousHelpPage, ActionHelpLineUp, ActionHelpLineDown,
		ActionHelpPageUp, ActionHelpPageDown, ActionHelpHome,
		ActionHelpEnd,
	),
	ContextStatusDetails: actionSet(
		ActionCancel, ActionInterrupt, ActionMoveUp, ActionMoveDown,
		ActionPageUp, ActionPageDown,
	),
	ContextConfirm: actionSet(
		ActionCancel, ActionInterrupt, ActionConfirmYes, ActionConfirmNo,
	),
	ContextExportPicker: actionSet(
		ActionCancel, ActionInterrupt, ActionToggleExportUnmap, ActionExportSelected,
	),
	ContextSubmit: actionSet(
		ActionCancel, ActionInterrupt, ActionStatusDetails,
		ActionSubmitRecheck, ActionToggleStaleOverride,
		ActionToggleSubmitExclude, ActionToggleSubmitSummary,
		ActionPreviousSubmitEvent, ActionNextSubmitEvent, ActionSubmitReview,
		ActionMoveUp, ActionMoveDown, ActionPageUp, ActionPageDown,
	),
	ContextMinimumSize: actionSet(ActionQuit, ActionInterrupt),
}
