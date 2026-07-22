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
	KeyActionToggleSubmitSummary, KeyActionSubmitReview, KeyActionToggleFilePanel,
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
		KeyActionToggleSubmitSummary, KeyActionSubmitReview,
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

// Settings describes settings.
type Settings struct {
	palette         Palette
	syntaxSelector  review.SyntaxSelector
	publication     review.PublicationOptions
	keybindings     []Keybinding
	exportTemplates review.ExportTemplates
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
	Publication     review.PublicationOptions
	Keybindings     []Keybinding
	ExportTemplates review.ExportTemplates
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
	validatedPublication, err = validatedPublication.WithExcludedComments(
		publication.ExcludedComments(),
	)
	if err != nil {
		return Settings{}, fmt.Errorf("publication settings: %w", err)
	}
	publication = validatedPublication
	if len(keybindings) == 0 {
		return Settings{}, errors.New("at least one keybinding is required")
	}
	patchContext, err := review.ContextAround(contextLines)
	if err != nil {
		return Settings{}, err
	}
	type bindingIdentity struct {
		context KeyContext
		focus   KeyFocus
		chord   KeyChord
	}
	seenBindings := make(map[bindingIdentity]struct{}, len(keybindings))
	for _, binding := range keybindings {
		if binding.Context == "" || binding.Chord == "" || binding.Action == "" {
			return Settings{}, errors.New("keybindings require context, chord, and action")
		}
		if strings.TrimSpace(binding.Context.String()) != binding.Context.String() ||
			strings.TrimSpace(binding.Chord.String()) == "" ||
			strings.TrimSpace(binding.Action.String()) != binding.Action.String() ||
			strings.TrimSpace(binding.Focus.String()) != binding.Focus.String() {
			return Settings{}, errors.New("keybinding values must be normalized")
		}
		context, err := NewKeyContext(binding.Context.String())
		if err != nil || context != binding.Context {
			return Settings{}, fmt.Errorf("keybinding context %q: %w", binding.Context, err)
		}
		focus, err := NewKeyFocus(binding.Focus.String())
		if err != nil || focus != binding.Focus {
			return Settings{}, fmt.Errorf("keybinding focus %q: %w", binding.Focus, err)
		}
		action, err := NewKeyAction(binding.Action.String())
		if err != nil || action != binding.Action {
			return Settings{}, fmt.Errorf("keybinding action %q: %w", binding.Action, err)
		}
		if _, ok := contextKeyActions[context][action]; !ok {
			return Settings{}, fmt.Errorf(
				"keybinding action %q is not available in context %q",
				action,
				context,
			)
		}
		if focus != KeyFocusNone && context != KeyContextNormal {
			return Settings{}, fmt.Errorf(
				"keybinding context %q does not accept focus %q",
				context,
				focus,
			)
		}
		identity := bindingIdentity{context: context, focus: focus, chord: binding.Chord}
		if _, exists := seenBindings[identity]; exists {
			return Settings{}, fmt.Errorf(
				"duplicate keybinding for %q/%q/%q",
				context,
				focus,
				binding.Chord,
			)
		}
		seenBindings[identity] = struct{}{}
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
		keybindings:     slices.Clone(keybindings),
		palette:         request.Palette,
	}, nil
}

func (settings Settings) clone() (Settings, error) {
	settings.keybindings = slices.Clone(settings.keybindings)
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
