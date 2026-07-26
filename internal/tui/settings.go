package tui

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/keymap"
)

// DiffView represents diff view.
type DiffView uint8

// DiffViewUnified identifies a supported diff view unified value.
const (
	DiffViewUnified DiffView = iota + 1
	DiffViewSideBySide
)

// Settings describes settings.
type Settings struct {
	palette          Palette
	keymap           *keymap.Map
	syntaxSelector   change.SyntaxSelector
	exportTemplates  change.ExportTemplates
	publicationEvent change.PublicationEvent
	patchContext     change.PatchContext
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
	SyntaxSelector   change.SyntaxSelector
	Keybindings      []keymap.Binding
	ExportTemplates  change.ExportTemplates
	PublicationEvent change.PublicationEvent
	ContextLines     change.ContextLines
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
	if _, err := change.NewSyntaxSelector(syntaxSelector.String()); err != nil {
		return Settings{}, fmt.Errorf("syntax selector: %w", err)
	}
	if !exportTemplates.Ready() {
		return Settings{}, errors.New("export templates are required")
	}
	publicationPolicies, err := change.NewPublicationPolicies()
	if err != nil {
		return Settings{}, fmt.Errorf("publication settings: %w", err)
	}
	validatedPublication, err := change.NewPublicationOptions(
		publicationEvent,
		publicationPolicies,
	)
	if err != nil {
		return Settings{}, fmt.Errorf("publication settings: %w", err)
	}
	if validatedPublication.Event() == change.PublicationEventDraft {
		return Settings{}, errors.New("publication settings require a final GitHub review event")
	}
	publicationEvent = validatedPublication.Event()
	patchContext, err := change.ContextAround(contextLines)
	if err != nil {
		return Settings{}, err
	}
	effectiveMap, err := keymap.Build(keybindings)
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
		keymap:           effectiveMap,
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
