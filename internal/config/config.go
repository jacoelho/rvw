// Package config loads and validates rvw configuration.
package config

import (
	"maps"
	"slices"

	"github.com/jacoelho/rvw/internal/keymap"
)

// MaxConfigBytes defines the supported config bytes limit.
const (
	MaxConfigBytes    int64 = 1 << 20
	MaxFilterPatterns       = 4_096
	MaxPatternBytes   int64 = 4_096
	MaxTemplateBytes  int64 = 256 << 10
	MaxTemplateTokens       = 4_096
)

// Appearance represents appearance.
type Appearance string

// AppearanceDark identifies a supported appearance dark value.
const (
	AppearanceDark   Appearance = "dark"
	AppearanceLight  Appearance = "light"
	AppearanceSystem Appearance = "system"
)

// DiffView represents diff view.
type DiffView string

// DiffUnified identifies a supported diff unified value.
const (
	DiffUnified    DiffView = "unified"
	DiffSideBySide DiffView = "side-by-side"
)

// SubmitEvent describes submit event.
type SubmitEvent string

// SubmitComment identifies a supported submit comment value.
const (
	SubmitComment        SubmitEvent = "comment"
	SubmitApprove        SubmitEvent = "approve"
	SubmitRequestChanges SubmitEvent = "request-changes"
)

// ThemeName represents theme name.
type ThemeName string

// ThemeDefault identifies a supported theme default value.
const (
	ThemeDefault             ThemeName = "default"
	ThemeDark                ThemeName = "dark"
	ThemeLight               ThemeName = "light"
	ThemeANSI16Dark          ThemeName = "ansi16-dark"
	ThemeTokyoNightStorm     ThemeName = "tokyo-night-storm"
	ThemeTokyoNightDay       ThemeName = "tokyo-night-day"
	ThemeGruvboxDark         ThemeName = "gruvbox-dark"
	ThemeGruvboxLight        ThemeName = "gruvbox-light"
	ThemeCatppuccinLatte     ThemeName = "catppuccin-latte"
	ThemeCatppuccinFrappe    ThemeName = "catppuccin-frappe"
	ThemeCatppuccinMacchiato ThemeName = "catppuccin-macchiato"
	ThemeCatppuccinMocha     ThemeName = "catppuccin-mocha"
	ThemeBase16OceanDark     ThemeName = "base16-ocean.dark"
	ThemeBase16EightiesDark  ThemeName = "base16-eighties.dark"
	ThemeBase16MochaDark     ThemeName = "base16-mocha.dark"
	ThemeBase16OceanLight    ThemeName = "base16-ocean.light"
	ThemeInspiredGitHub      ThemeName = "InspiredGitHub"
	ThemeSolarizedDark       ThemeName = "Solarized (dark)"
	ThemeSolarizedLight      ThemeName = "Solarized (light)"
)

// String returns the receiver's string value.
func (name ThemeName) String() string {
	return string(name)
}

// Pattern represents pattern.
type Pattern string

// String returns the receiver's string value.
func (pattern Pattern) String() string {
	return string(pattern)
}

// IgnoreFileName represents ignore file name.
type IgnoreFileName string

// String returns the receiver's string value.
func (name IgnoreFileName) String() string {
	return string(name)
}

// Template represents template.
type Template string

// String returns the receiver's string value.
func (template Template) String() string {
	return string(template)
}

// ThemeColor represents theme color.
type ThemeColor string

// Color represents color.
type Color string

// String returns the receiver's string value.
func (color Color) String() string {
	return string(color)
}

// Theme represents theme.
type Theme struct {
	Name         ThemeName
	Colors       map[ThemeColor]Color
	SyntaxColors map[SyntaxColor]Color
	SyntaxTheme  SyntaxThemePath
	UserDefined  bool
}

// SyntaxColor represents syntax color.
type SyntaxColor string

// SyntaxKeyword identifies a supported syntax keyword value.
const (
	SyntaxKeyword SyntaxColor = "keyword"
	SyntaxString  SyntaxColor = "string"
	SyntaxComment SyntaxColor = "comment"
	SyntaxNumber  SyntaxColor = "number"
)

// SyntaxThemePath represents syntax theme path.
type SyntaxThemePath string

// String returns the receiver's string value.
func (path SyntaxThemePath) String() string {
	return string(path)
}

// Keybinding aliases the canonical keyboard domain value for config callers.
type Keybinding = keymap.Binding

// UI represents ui.
type UI struct {
	Theme         ThemeName
	ThemeDark     ThemeName
	ThemeLight    ThemeName
	Appearance    Appearance
	Keybindings   []Keybinding
	Mouse         bool
	ColorEnabled  bool
	ExplicitTheme bool
}

// Diff represents diff.
type Diff struct {
	View             DiffView
	ContextLines     int
	Wrap             bool
	IncludeUntracked bool
}

// Filter represents filter.
type Filter struct {
	IgnoreFile      IgnoreFileName
	Include         []Pattern
	Exclude         []Pattern
	BuiltinDefaults bool
	Gitignore       bool
	RVWIgnore       bool
}

// GitHub represents git hub.
type GitHub struct {
	SubmitDefault SubmitEvent
	FetchThreads  bool
}

// Export represents export.
type Export struct {
	MarkdownTemplate Template
	CommentTemplate  Template
}

// Config describes config.
type Config struct {
	themes      map[ThemeName]Theme
	export      Export
	activeTheme ThemeName
	filter      Filter
	github      GitHub
	ui          UI
	diff        Diff
}

// Defaults performs the defaults operation.
func Defaults() Config {
	return Config{
		ui: UI{
			Mouse:        true,
			Appearance:   AppearanceSystem,
			ColorEnabled: true,
		},
		diff: Diff{
			Wrap:             true,
			View:             DiffUnified,
			ContextLines:     3,
			IncludeUntracked: true,
		},
		filter: Filter{
			BuiltinDefaults: true,
			Gitignore:       true,
			RVWIgnore:       true,
			IgnoreFile:      IgnoreFileName(".rvwignore"),
			Include:         []Pattern{},
			Exclude:         []Pattern{},
		},
		github: GitHub{
			SubmitDefault: SubmitComment,
			FetchThreads:  true,
		},
		export: Export{
			MarkdownTemplate: Template("I reviewed your code and have the following comments. Please address them.\n\n{{comments}}\n"),
			CommentTemplate:  Template("{{index}}. `{{target}}` - {{comment}}"),
		},
	}
}

// UI returns the receiver's ui value.
func (config Config) UI() UI {
	ui := config.ui
	ui.Keybindings = slices.Clone(ui.Keybindings)
	return ui
}

// Diff returns the receiver's diff value.
func (config Config) Diff() Diff {
	return config.diff
}

// Filter returns the receiver's filter value.
func (config Config) Filter() Filter {
	filter := config.filter
	filter.Include = slices.Clone(filter.Include)
	filter.Exclude = slices.Clone(filter.Exclude)
	return filter
}

// GitHub returns the receiver's git hub value.
func (config Config) GitHub() GitHub {
	return config.github
}

// Export returns the receiver's export value.
func (config Config) Export() Export {
	return config.export
}

// Theme performs the theme operation.
func (config Config) Theme(name ThemeName) (Theme, bool) {
	theme, ok := config.themes[name]
	if !ok {
		return Theme{}, false
	}
	theme.Colors = cloneThemeColors(theme.Colors)
	theme.SyntaxColors = maps.Clone(theme.SyntaxColors)
	return theme, true
}

// ActiveTheme returns the receiver's active theme value.
func (config Config) ActiveTheme() ThemeName {
	return config.activeTheme
}

// Overrides represents overrides.
type Overrides struct {
	mouse      *bool
	theme      *ThemeName
	appearance *Appearance
	include    []Pattern
	exclude    []Pattern
}

// DisableMouse performs the disable mouse operation.
func (overrides *Overrides) DisableMouse() {
	overrides.mouse = new(false)
}

// SetTheme performs the set theme operation.
func (overrides *Overrides) SetTheme(theme ThemeName) {
	overrides.theme = new(theme)
}

// SetAppearance performs the set appearance operation.
func (overrides *Overrides) SetAppearance(appearance Appearance) {
	overrides.appearance = new(appearance)
}

// AddInclude performs the add include operation.
func (overrides *Overrides) AddInclude(pattern Pattern) {
	overrides.include = append(overrides.include, pattern)
}

// AddExclude performs the add exclude operation.
func (overrides *Overrides) AddExclude(pattern Pattern) {
	overrides.exclude = append(overrides.exclude, pattern)
}

func cloneThemeColors(colors map[ThemeColor]Color) map[ThemeColor]Color {
	clone := make(map[ThemeColor]Color, len(colors))
	maps.Copy(clone, colors)
	return clone
}
