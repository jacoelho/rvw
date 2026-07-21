package config

import (
	"maps"
	"slices"
)

const (
	MaxConfigBytes    int64 = 1 << 20
	MaxFilterPatterns       = 4_096
	MaxPatternBytes   int64 = 4_096
	MaxTemplateBytes  int64 = 256 << 10
	MaxTemplateTokens       = 4_096
	MaxKeybindings          = 4_096
)

type Appearance string

const (
	AppearanceDark   Appearance = "dark"
	AppearanceLight  Appearance = "light"
	AppearanceSystem Appearance = "system"
)

type DiffView string

const (
	DiffUnified    DiffView = "unified"
	DiffSideBySide DiffView = "side-by-side"
)

type SubmitEvent string

const (
	SubmitComment        SubmitEvent = "comment"
	SubmitDraft          SubmitEvent = "draft"
	SubmitApprove        SubmitEvent = "approve"
	SubmitRequestChanges SubmitEvent = "request-changes"
)

type ThemeName string

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

func (name ThemeName) String() string {
	return string(name)
}

type Pattern string

func (pattern Pattern) String() string {
	return string(pattern)
}

type IgnoreFileName string

func (name IgnoreFileName) String() string {
	return string(name)
}

type Template string

func (template Template) String() string {
	return string(template)
}

type ThemeColor string

type Color string

func (color Color) String() string {
	return string(color)
}

type Theme struct {
	Name         ThemeName
	Colors       map[ThemeColor]Color
	SyntaxColors map[SyntaxColor]Color
	SyntaxTheme  SyntaxThemePath
	UserDefined  bool
}

type SyntaxColor string

const (
	SyntaxKeyword SyntaxColor = "keyword"
	SyntaxString  SyntaxColor = "string"
	SyntaxComment SyntaxColor = "comment"
	SyntaxNumber  SyntaxColor = "number"
)

type SyntaxThemePath string

func (path SyntaxThemePath) String() string {
	return string(path)
}

type KeyContext string

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

func (context KeyContext) String() string {
	return string(context)
}

type KeyFocus string

const (
	KeyFocusFileTree KeyFocus = "file-tree"
	KeyFocusDiff     KeyFocus = "diff"
	KeyFocusComments KeyFocus = "comments"
)

func (focus KeyFocus) String() string {
	return string(focus)
}

type KeyAction string

const (
	KeyActionQuit                KeyAction = "quit"
	KeyActionInterrupt           KeyAction = "interrupt"
	KeyActionFocusNextPane       KeyAction = "focus-next-pane"
	KeyActionFocusPreviousPane   KeyAction = "focus-previous-pane"
	KeyActionMoveUp              KeyAction = "move-up"
	KeyActionMoveDown            KeyAction = "move-down"
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
	KeyActionVisualLine          KeyAction = "visual-line"
	KeyActionVisualLines         KeyAction = "visual-lines"
	KeyActionVisualHunk          KeyAction = "visual-hunk"
	KeyActionExtendUp            KeyAction = "extend-up"
	KeyActionExtendDown          KeyAction = "extend-down"
	KeyActionCommentSelection    KeyAction = "comment-selection"
	KeyActionSaveComment         KeyAction = "save-comment"
	KeyActionNextFile            KeyAction = "next-file"
	KeyActionPreviousFile        KeyAction = "previous-file"
	KeyActionNextHunk            KeyAction = "next-hunk"
	KeyActionPreviousHunk        KeyAction = "previous-hunk"
	KeyActionNextComment         KeyAction = "next-comment"
	KeyActionPreviousComment     KeyAction = "previous-comment"
	KeyActionNextSearchHit       KeyAction = "next-search-hit"
	KeyActionPreviousSearchHit   KeyAction = "previous-search-hit"
	KeyActionSearch              KeyAction = "search"
	KeyActionCommand             KeyAction = "command"
	KeyActionCopyMarkdown        KeyAction = "copy-markdown"
	KeyActionExportMarkdown      KeyAction = "export-markdown"
	KeyActionExportJSON          KeyAction = "export-json"
	KeyActionOpenSubmit          KeyAction = "open-submit"
	KeyActionHelp                KeyAction = "help"
	KeyActionStatusDetails       KeyAction = "status-details"
	KeyActionReload              KeyAction = "reload"
	KeyActionToggleHiddenFiles   KeyAction = "toggle-hidden-files"
	KeyActionToggleView          KeyAction = "toggle-view"
	KeyActionToggleWrap          KeyAction = "toggle-wrap"
	KeyActionToggleDirectory     KeyAction = "toggle-directory"
	KeyActionToggleFilePanel     KeyAction = "toggle-file-panel"
	KeyActionToggleComments      KeyAction = "toggle-comments"
	KeyActionHelpLineDown        KeyAction = "help-line-down"
	KeyActionHelpLineUp          KeyAction = "help-line-up"
	KeyActionHelpPageDown        KeyAction = "help-page-down"
	KeyActionHelpPageUp          KeyAction = "help-page-up"
	KeyActionHelpHome            KeyAction = "help-home"
	KeyActionHelpEnd             KeyAction = "help-end"
	KeyActionNextHelpPage        KeyAction = "next-help-page"
	KeyActionPreviousHelpPage    KeyAction = "previous-help-page"
	KeyActionToggleExportUnmap   KeyAction = "toggle-export-unmappable"
	KeyActionToggleSubmitExclude KeyAction = "toggle-submit-exclusion"
	KeyActionToggleSubmitSummary KeyAction = "toggle-submit-summary"
	KeyActionToggleStaleOverride KeyAction = "toggle-stale-head-override"
	KeyActionSubmitRecheck       KeyAction = "submit-recheck"
	KeyActionCancel              KeyAction = "cancel"
)

func (action KeyAction) String() string {
	return string(action)
}

type KeyChord string

func (chord KeyChord) String() string {
	return string(chord)
}

type UI struct {
	Mouse         bool
	Theme         ThemeName
	ThemeDark     ThemeName
	ThemeLight    ThemeName
	Appearance    Appearance
	Keybindings   []Keybinding
	ColorEnabled  bool
	ExplicitTheme bool
}

type Diff struct {
	Wrap             bool
	View             DiffView
	ContextLines     int
	IncludeUntracked bool
}

type Filter struct {
	BuiltinDefaults bool
	Gitignore       bool
	RVWIgnore       bool
	IgnoreFile      IgnoreFileName
	Include         []Pattern
	Exclude         []Pattern
}

type GitHub struct {
	SubmitDefault SubmitEvent
	FetchThreads  bool
}

type Export struct {
	MarkdownTemplate Template
	CommentTemplate  Template
}

type Config struct {
	ui          UI
	diff        Diff
	filter      Filter
	github      GitHub
	export      Export
	themes      map[ThemeName]Theme
	activeTheme ThemeName
}

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

func (config Config) UI() UI {
	ui := config.ui
	ui.Keybindings = slices.Clone(ui.Keybindings)
	return ui
}

func (config Config) Diff() Diff {
	return config.diff
}

func (config Config) Filter() Filter {
	filter := config.filter
	filter.Include = slices.Clone(filter.Include)
	filter.Exclude = slices.Clone(filter.Exclude)
	return filter
}

func (config Config) GitHub() GitHub {
	return config.github
}

func (config Config) Export() Export {
	return config.export
}

func (config Config) Theme(name ThemeName) (Theme, bool) {
	theme, ok := config.themes[name]
	if !ok {
		return Theme{}, false
	}
	theme.Colors = cloneThemeColors(theme.Colors)
	theme.SyntaxColors = maps.Clone(theme.SyntaxColors)
	return theme, true
}

func (config Config) ActiveTheme() ThemeName {
	return config.activeTheme
}

type Overrides struct {
	mouse      *bool
	theme      *ThemeName
	appearance *Appearance
	include    []Pattern
	exclude    []Pattern
}

func (overrides *Overrides) DisableMouse() {
	overrides.mouse = new(false)
}

func (overrides *Overrides) SetTheme(theme ThemeName) {
	overrides.theme = new(theme)
}

func (overrides *Overrides) SetAppearance(appearance Appearance) {
	overrides.appearance = new(appearance)
}

func (overrides *Overrides) AddInclude(pattern Pattern) {
	overrides.include = append(overrides.include, pattern)
}

func (overrides *Overrides) AddExclude(pattern Pattern) {
	overrides.exclude = append(overrides.exclude, pattern)
}

type Keybinding struct {
	Context KeyContext
	Focus   KeyFocus
	Chord   KeyChord
	Action  KeyAction
	Unbind  bool
}

func cloneThemeColors(colors map[ThemeColor]Color) map[ThemeColor]Color {
	clone := make(map[ThemeColor]Color, len(colors))
	for name, color := range colors {
		clone[name] = color
	}
	return clone
}
