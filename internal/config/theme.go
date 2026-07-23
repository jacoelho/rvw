package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

const (
	maxThemeBytes          int64 = 1 << 20
	ansiBlack                    = "black"
	gruvboxLightForeground       = "#ebdbb2"
	gruvboxDarkBackground        = "#3c3836"
)

var curatedThemes = stringSet[ThemeName](
	ThemeDark, ThemeLight, ThemeANSI16Dark, ThemeTokyoNightStorm, ThemeTokyoNightDay,
	ThemeGruvboxDark, ThemeGruvboxLight, ThemeCatppuccinLatte, ThemeCatppuccinFrappe,
	ThemeCatppuccinMacchiato, ThemeCatppuccinMocha,
)

var embeddedSyntaxThemes = stringSet[ThemeName](
	ThemeBase16OceanDark, ThemeBase16EightiesDark, ThemeBase16MochaDark,
	ThemeBase16OceanLight, ThemeInspiredGitHub, ThemeSolarizedDark, ThemeSolarizedLight,
)

var requiredThemeColors = []ThemeColor{
	"panel_bg",
	"fg_primary",
	"fg_secondary",
	"fg_dim",
	"diff_add",
	"diff_del",
	"diff_context",
	"pending",
	"border_focused",
	"status_bar_bg",
	"cursor_line_bg",
}

var optionalThemeColors = stringSet[ThemeColor](
	"diff_add_bg", "diff_del_bg", "diff_hunk_header", "file_modified", "file_renamed",
	"comment_note", "comment_suggestion", "branch_name", "message_warning_fg", "message_warning_bg",
)

var namedColors = stringSet[string](
	"reset", ansiBlack, "red", "green", "yellow", "blue", "magenta", "cyan", "gray", "grey",
	"darkgray", "darkgrey", "lightred", "lightgreen", "lightyellow", "lightblue",
	"lightmagenta", "lightcyan", "white",
)

func resolveThemes(config *Config, environment map[string]string) error {
	config.themes = make(map[ThemeName]Theme)
	active := config.ui.Theme
	switch config.ui.Appearance {
	case AppearanceLight:
		if config.ui.ThemeLight != "" {
			active = config.ui.ThemeLight
		}
	case AppearanceDark, AppearanceSystem:
		if config.ui.ThemeDark != "" {
			active = config.ui.ThemeDark
		}
	}
	if active == "" || active == ThemeDefault {
		if config.ui.Appearance == AppearanceLight {
			active = ThemeGruvboxLight
		} else {
			active = ThemeGruvboxDark
		}
	}
	config.activeTheme = active

	names := []ThemeName{config.ui.Theme, config.ui.ThemeDark, config.ui.ThemeLight, active}
	for _, name := range names {
		if name == "" || name == ThemeDefault {
			continue
		}
		if _, exists := config.themes[name]; exists {
			continue
		}
		if curatedThemes[name] || embeddedSyntaxThemes[name] {
			config.themes[name] = bundledTheme(name, config.ui.Appearance)
			continue
		}
		theme, err := loadUserTheme(name, environment)
		if err != nil {
			return err
		}
		config.themes[name] = theme
	}
	return nil
}

type bundledThemePalette struct {
	background, primary, secondary, muted string
	header, selected, addition, deletion  string
	accent, warning, context              string
}

func bundledTheme(name ThemeName, appearance Appearance) Theme {
	selector := name
	paletteName := name
	switch name {
	case ThemeDark, ThemeANSI16Dark, ThemeTokyoNightStorm, ThemeGruvboxDark,
		ThemeCatppuccinFrappe, ThemeCatppuccinMacchiato, ThemeCatppuccinMocha:
		selector = ThemeBase16OceanDark
		appearance = AppearanceDark
	case ThemeLight, ThemeTokyoNightDay, ThemeGruvboxLight, ThemeCatppuccinLatte:
		selector = ThemeInspiredGitHub
		appearance = AppearanceLight
	default:
		if appearance == AppearanceSystem {
			appearance = AppearanceDark
		}
		if appearance == AppearanceLight {
			paletteName = ThemeGruvboxLight
		} else {
			paletteName = ThemeGruvboxDark
		}
	}
	palette := bundledPalette(paletteName, appearance)
	return Theme{
		Name:        name,
		SyntaxTheme: SyntaxThemePath(selector.String()),
		Colors: map[ThemeColor]Color{
			"panel_bg":           Color(palette.background),
			"fg_primary":         Color(palette.primary),
			"fg_secondary":       Color(palette.secondary),
			"fg_dim":             Color(palette.muted),
			"diff_add":           Color(palette.addition),
			"diff_del":           Color(palette.deletion),
			"diff_context":       Color(palette.context),
			"pending":            Color(palette.secondary),
			"border_focused":     Color(palette.accent),
			"status_bar_bg":      Color(palette.header),
			"cursor_line_bg":     Color(palette.selected),
			"diff_hunk_header":   Color(palette.accent),
			"message_warning_fg": Color(palette.warning),
		},
	}
}

func bundledPalette(name ThemeName, appearance Appearance) bundledThemePalette {
	switch name {
	case ThemeDark:
		return bundledThemePalette{
			"#18181c", "white", "#d2d2d2", "#a0a0a0",
			"#1e1e1e", "#28282d", "#50dc78", "#f05a5a",
			"#5ac8ff", "#ffd25a", "#c8c8c8",
		}
	case ThemeLight:
		return bundledThemePalette{
			"#f5f3e8", ansiBlack, "#1e1e1e", "#505050",
			"#d2d2dc", "#e1e1eb", "#005000", "#780000",
			"#003c8c", "#8c5000", ansiBlack,
		}
	case ThemeANSI16Dark:
		return bundledThemePalette{
			ansiBlack, "white", "gray", "darkgray",
			"darkgray", "cyan", "green", "red",
			"cyan", "yellow", "gray",
		}
	case ThemeTokyoNightStorm:
		return bundledThemePalette{
			"#24283b", "#c0caf5", "#a9b1d6", "#545c7e",
			"#1f2335", "#292e42", "#9ece6a", "#f7768e",
			"#7aa2f7", "#e0af68", "#a9b1d6",
		}
	case ThemeTokyoNightDay:
		return bundledThemePalette{
			"#e1e2e7", "#3760bf", "#6172b0", "#848cb5",
			"#d0d5e3", "#c4c8da", "#587539", "#f52a65",
			"#2e7de9", "#8c6c3e", "#3760bf",
		}
	case ThemeCatppuccinLatte:
		return catppuccinPalette(AppearanceLight,
			"#4c4f69", "#5c5f77", "#9ca0b0", "#bcc0cc",
			"#eff1f5", "#e6e9ef", "#dcE0e8", "#d20f39",
			"#df8e1d", "#40a02b", "#1e66f5")
	case ThemeCatppuccinFrappe:
		return catppuccinPalette(AppearanceDark,
			"#c6d0f5", "#b5bfe2", "#737994", "#51576d",
			"#303446", "#292c3c", "#232634", "#e78284",
			"#e5c890", "#a6d189", "#8caaee")
	case ThemeCatppuccinMacchiato:
		return catppuccinPalette(AppearanceDark,
			"#cad3f5", "#b8c0e0", "#6e738d", "#494d64",
			"#24273a", "#1e2030", "#181926", "#ed8796",
			"#eed49f", "#a6da95", "#8aadf4")
	case ThemeCatppuccinMocha:
		return catppuccinPalette(AppearanceDark,
			"#cdd6f4", "#bac2de", "#6c7086", "#45475a",
			"#1e1e2e", "#181825", "#11111b", "#f38ba8",
			"#f9e2af", "#a6e3a1", "#89b4fa")
	case ThemeGruvboxDark:
		return bundledThemePalette{
			"#282828", gruvboxLightForeground, "#d5c4a1", "#928374",
			gruvboxDarkBackground, gruvboxDarkBackground, "#b8bb26", "#fb4934",
			"#8ec07c", "#fabd2f", gruvboxLightForeground,
		}
	case ThemeGruvboxLight:
		return bundledThemePalette{
			"#fbf1c7", gruvboxDarkBackground, "#504945", "#928374",
			gruvboxLightForeground, gruvboxLightForeground, "#79740e", "#9d0006",
			"#427b58", "#b57614", gruvboxDarkBackground,
		}
	default:
		if appearance == AppearanceLight {
			return bundledPalette(ThemeGruvboxLight, appearance)
		}
		return bundledPalette(ThemeGruvboxDark, appearance)
	}
}

func catppuccinPalette(
	_ Appearance,
	text, subtext, muted, surface, base, mantle, _,
	red, yellow, green, blue string,
) bundledThemePalette {
	return bundledThemePalette{
		base, text, subtext, muted,
		mantle, surface, green, red,
		blue, yellow, text,
	}
}

func loadUserTheme(name ThemeName, environment map[string]string) (Theme, error) {
	directory := userThemesDirectory(environment)
	if directory == "" {
		return Theme{}, fmt.Errorf("%w: custom theme %q requires XDG_CONFIG_HOME or HOME", ErrInvalidConfig, name)
	}
	relative := name.String() + ".toml"
	contents, path, err := readBoundedRootFile(directory, relative, maxThemeBytes, "theme")
	if err != nil {
		return Theme{}, err
	}
	if !utf8.Valid(contents) {
		return Theme{}, fmt.Errorf("%w: theme %s is not valid UTF-8", ErrInvalidConfig, path)
	}

	values := make(map[string]string)
	metadata, err := toml.NewDecoder(bytes.NewReader(contents)).Decode(&values)
	if err != nil {
		return Theme{}, fmt.Errorf("%w: decode theme %s: %w", ErrInvalidConfig, path, err)
	}
	if unknown := metadata.Undecoded(); len(unknown) != 0 {
		return Theme{}, fmt.Errorf("%w: theme %s contains undecoded fields", ErrInvalidConfig, path)
	}
	allowed := make(map[ThemeColor]bool, len(requiredThemeColors)+len(optionalThemeColors))
	for _, key := range requiredThemeColors {
		allowed[key] = true
	}
	for key := range optionalThemeColors {
		allowed[key] = true
	}
	colors := make(map[ThemeColor]Color, len(values))
	var syntaxPath SyntaxThemePath
	var syntaxColors map[SyntaxColor]Color
	for rawKey, rawValue := range values {
		key := ThemeColor(rawKey)
		if rawKey == "syntax_theme" {
			syntaxPath, syntaxColors, err = validateSyntaxTheme(directory, rawValue)
			if err != nil {
				return Theme{}, err
			}
			continue
		}
		if !allowed[key] {
			return Theme{}, fmt.Errorf("%w: theme %s contains unknown key %q", ErrInvalidConfig, path, rawKey)
		}
		color, err := validateColor(rawValue)
		if err != nil {
			return Theme{}, fmt.Errorf("%w: theme %s key %s: %w", ErrInvalidConfig, path, rawKey, err)
		}
		colors[key] = color
	}
	for _, key := range requiredThemeColors {
		if _, exists := colors[key]; !exists {
			return Theme{}, fmt.Errorf("%w: theme %s is missing %s", ErrInvalidConfig, path, key)
		}
	}
	return Theme{
		Name:         name,
		Colors:       colors,
		SyntaxColors: syntaxColors,
		SyntaxTheme:  syntaxPath,
		UserDefined:  true,
	}, nil
}

func validateColor(value string) (Color, error) {
	if len(value) == 7 && value[0] == '#' {
		for _, digit := range value[1:] {
			if digit < '0' || digit > '9' && digit < 'A' || digit > 'F' && digit < 'a' || digit > 'f' {
				return "", fmt.Errorf("invalid RGB color %q", value)
			}
		}
		return Color(value), nil
	}
	normalized := strings.ToLower(value)
	normalized = strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	if !namedColors[normalized] {
		return "", fmt.Errorf("unknown color %q", value)
	}
	return Color(normalized), nil
}

func validateSyntaxTheme(
	directory, value string,
) (SyntaxThemePath, map[SyntaxColor]Color, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 || strings.ContainsRune(value, '\\') ||
		filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return "", nil, fmt.Errorf("%w: invalid syntax_theme path", ErrInvalidConfig)
	}
	for component := range strings.SplitSeq(value, string(filepath.Separator)) {
		if component == ".." {
			return "", nil, fmt.Errorf(
				"%w: syntax_theme contains a parent path component",
				ErrInvalidConfig,
			)
		}
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", nil, fmt.Errorf("%w: syntax_theme escapes the themes directory", ErrInvalidConfig)
	}
	contents, path, err := readBoundedRootFile(
		directory,
		clean,
		maxThemeBytes,
		"syntax theme",
	)
	if err != nil {
		return "", nil, err
	}
	colors, err := parseTextMateTheme(contents)
	if err != nil {
		return "", nil, fmt.Errorf(
			"%w: decode syntax theme %s: %w",
			ErrInvalidConfig,
			path,
			err,
		)
	}
	return SyntaxThemePath(path), colors, nil
}

func readBoundedRootFile(
	directory string,
	relative string,
	limit int64,
	kind string,
) (contents []byte, path string, returnErr error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, "", fmt.Errorf("%w: open themes directory %s: %w", ErrInvalidConfig, directory, err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	file, err := root.OpenFile(relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	path = filepath.Join(directory, relative)
	if err != nil {
		return nil, "", fmt.Errorf("%w: read %s %s: %w", ErrInvalidConfig, kind, path, err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, "", fmt.Errorf("%w: inspect %s %s: %w", ErrInvalidConfig, kind, path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%w: %s %s is not a regular file", ErrInvalidConfig, kind, path)
	}
	contents, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: read %s %s: %w", ErrInvalidConfig, kind, path, err)
	}
	if int64(len(contents)) > limit {
		return nil, "", fmt.Errorf("%w: %s %s exceeds %d bytes", ErrInvalidConfig, kind, path, limit)
	}
	return contents, path, nil
}

func userThemesDirectory(environment map[string]string) string {
	if xdg := environment["XDG_CONFIG_HOME"]; xdg != "" {
		return filepath.Join(xdg, "rvw", "themes")
	}
	if home := environment["HOME"]; home != "" {
		return filepath.Join(home, ".config", "rvw", "themes")
	}
	return ""
}
