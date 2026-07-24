package config

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/x/ansi"
)

const (
	maxThemeBytes          int64 = 1 << 20
	ansiBlack                    = "black"
	ansiGray                     = "gray"
	ansiWhite                    = "white"
	gruvboxLightForeground       = "#ebdbb2"
	gruvboxDarkBackground        = "#3c3836"

	themePanelBackground        ThemeColor = "panel_bg"
	themePrimaryForeground      ThemeColor = "fg_primary"
	themeSecondaryForeground    ThemeColor = "fg_secondary"
	themeDimForeground          ThemeColor = "fg_dim"
	themeDiffAddition           ThemeColor = "diff_add"
	themeDiffDeletion           ThemeColor = "diff_del"
	themeDiffContext            ThemeColor = "diff_context"
	themePending                ThemeColor = "pending"
	themeFocusedBorder          ThemeColor = "border_focused"
	themeStatusBackground       ThemeColor = "status_bar_bg"
	themeCursorBackground       ThemeColor = "cursor_line_bg"
	themeDiffAdditionBackground ThemeColor = "diff_add_bg"
	themeDiffDeletionBackground ThemeColor = "diff_del_bg"
	themeDiffHunkHeader         ThemeColor = "diff_hunk_header"
	themeWarningForeground      ThemeColor = "message_warning_fg"
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
	themePanelBackground,
	themePrimaryForeground,
	themeSecondaryForeground,
	themeDimForeground,
	themeDiffAddition,
	themeDiffDeletion,
	themeDiffContext,
	themePending,
	themeFocusedBorder,
	themeStatusBackground,
	themeCursorBackground,
}

var optionalThemeColors = stringSet[ThemeColor](
	themeDiffAdditionBackground, themeDiffDeletionBackground, themeDiffHunkHeader,
	"file_modified", "file_renamed", "branch_name", themeWarningForeground, "message_warning_bg",
)

var namedColors = stringSet[string](
	"reset", ansiBlack, "red", "green", "yellow", "blue", "magenta", "cyan", ansiGray, "grey",
	"darkgray", "darkgrey", "lightred", "lightgreen", "lightyellow", "lightblue",
	"lightmagenta", "lightcyan", ansiWhite,
)

func stringSet[T ~string](values ...T) map[T]bool {
	result := make(map[T]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

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
	colors := map[ThemeColor]Color{
		themePanelBackground:     Color(palette.background),
		themePrimaryForeground:   Color(palette.primary),
		themeSecondaryForeground: Color(palette.secondary),
		themeDimForeground:       Color(palette.muted),
		themeDiffAddition:        Color(palette.addition),
		themeDiffDeletion:        Color(palette.deletion),
		themeDiffContext:         Color(palette.context),
		themePending:             Color(palette.secondary),
		themeFocusedBorder:       Color(palette.accent),
		themeStatusBackground:    Color(palette.header),
		themeCursorBackground:    Color(palette.selected),
		themeDiffHunkHeader:      Color(palette.accent),
		themeWarningForeground:   Color(palette.warning),
	}
	syntaxColors := map[SyntaxColor]Color{
		SyntaxKeyword: Color(palette.accent),
		SyntaxString:  Color(palette.addition),
		SyntaxComment: Color(palette.muted),
		SyntaxNumber:  Color(palette.accent),
	}
	if background, ok := subtleDiffBackground(palette.background, palette.addition); ok {
		colors[themeDiffAdditionBackground] = background
	}
	if background, ok := subtleDiffBackground(palette.background, palette.deletion); ok {
		colors[themeDiffDeletionBackground] = background
	}
	enforceBundledContrast(colors, syntaxColors, appearance)
	return Theme{
		Name:         name,
		SyntaxTheme:  SyntaxThemePath(selector.String()),
		Colors:       colors,
		SyntaxColors: syntaxColors,
	}
}

const minimumTextContrast = 4.5

func enforceBundledContrast(
	colors map[ThemeColor]Color,
	syntaxColors map[SyntaxColor]Color,
	appearance Appearance,
) {
	panel := themePanelBackground
	status := themeStatusBackground
	selection := themeCursorBackground
	addition := themeDiffAdditionBackground
	deletion := themeDiffDeletionBackground
	for _, role := range []ThemeColor{panel, status, selection, addition, deletion} {
		if value, exists := colors[role]; exists {
			colors[role] = contrastSafeBackground(value, appearance)
		}
	}
	rules := []struct {
		role        ThemeColor
		backgrounds []ThemeColor
	}{
		{role: themePrimaryForeground, backgrounds: []ThemeColor{panel, status, selection, addition, deletion}},
		{role: themeSecondaryForeground, backgrounds: []ThemeColor{panel, status}},
		{role: themeDimForeground, backgrounds: []ThemeColor{panel, status, selection, addition, deletion}},
		{role: themeDiffAddition, backgrounds: []ThemeColor{panel, selection, addition}},
		{role: themeDiffDeletion, backgrounds: []ThemeColor{panel, status, selection, deletion}},
		{role: themeDiffContext, backgrounds: []ThemeColor{panel, selection}},
		{role: themePending, backgrounds: []ThemeColor{panel}},
		{role: themeFocusedBorder, backgrounds: []ThemeColor{panel, status, selection}},
		{role: themeDiffHunkHeader, backgrounds: []ThemeColor{panel, status, selection}},
		{role: themeWarningForeground, backgrounds: []ThemeColor{panel, status}},
	}
	for _, rule := range rules {
		role := rule.role
		value, exists := colors[role]
		if !exists {
			continue
		}
		backgroundValues := make([]Color, 0, len(rule.backgrounds))
		for _, background := range rule.backgrounds {
			if backgroundValue, exists := colors[background]; exists {
				backgroundValues = append(backgroundValues, backgroundValue)
			}
		}
		colors[role] = contrastSafeColor(value, backgroundValues, appearance)
	}
	syntaxBackgrounds := make([]Color, 0, 4)
	for _, role := range []ThemeColor{panel, selection, addition, deletion} {
		if value, exists := colors[role]; exists {
			syntaxBackgrounds = append(syntaxBackgrounds, value)
		}
	}
	for _, role := range []SyntaxColor{SyntaxKeyword, SyntaxString, SyntaxComment, SyntaxNumber} {
		if value, exists := syntaxColors[role]; exists {
			syntaxColors[role] = contrastSafeColor(value, syntaxBackgrounds, appearance)
		}
	}
}

func contrastSafeBackground(background Color, appearance Appearance) Color {
	original, ok := parseHexColor(string(background))
	if !ok {
		return background
	}
	targetBackground := [3]uint8{}
	targetForeground := [3]uint8{255, 255, 255}
	if appearance == AppearanceLight {
		targetBackground, targetForeground = targetForeground, targetBackground
	}
	for step := range 256 {
		candidate := mixRGB(original, targetBackground, uint8(step))
		if contrastsWithProfiles(targetForeground, [][3]uint8{candidate}) {
			return Color(fmt.Sprintf("#%02x%02x%02x", candidate[0], candidate[1], candidate[2]))
		}
	}
	return Color(fmt.Sprintf(
		"#%02x%02x%02x",
		targetBackground[0],
		targetBackground[1],
		targetBackground[2],
	))
}

func contrastSafeColor(
	foreground Color,
	backgrounds []Color,
	appearance Appearance,
) Color {
	original, ok := parseHexColor(string(foreground))
	if !ok {
		return foreground
	}
	parsedBackgrounds := make([][3]uint8, 0, len(backgrounds))
	for _, background := range backgrounds {
		if parsed, ok := parseHexColor(string(background)); ok {
			parsedBackgrounds = append(parsedBackgrounds, parsed)
		}
	}
	if len(parsedBackgrounds) == 0 {
		return foreground
	}
	target := [3]uint8{255, 255, 255}
	if appearance == AppearanceLight {
		target = [3]uint8{}
	}
	for step := range 256 {
		candidate := mixRGB(original, target, uint8(step))
		if contrastsWithProfiles(candidate, parsedBackgrounds) {
			return Color(fmt.Sprintf("#%02x%02x%02x", candidate[0], candidate[1], candidate[2]))
		}
	}
	return Color(fmt.Sprintf("#%02x%02x%02x", target[0], target[1], target[2]))
}

func mixRGB(first, second [3]uint8, amount uint8) [3]uint8 {
	var mixed [3]uint8
	for index := range mixed {
		value := (uint16(first[index])*uint16(255-amount) +
			uint16(second[index])*uint16(amount) + 127) / 255
		if value > math.MaxUint8 {
			panic("mixed RGB component cannot be represented as uint8")
		}
		mixed[index] = uint8(value)
	}
	return mixed
}

func contrastsWithProfiles(
	foreground [3]uint8,
	backgrounds [][3]uint8,
) bool {
	for _, background := range backgrounds {
		foregroundColor := color.RGBA{R: foreground[0], G: foreground[1], B: foreground[2], A: 255}
		backgroundColor := color.RGBA{R: background[0], G: background[1], B: background[2], A: 255}
		if contrastRatio(foregroundColor, backgroundColor) < minimumTextContrast ||
			contrastRatio(ansi.Convert256(foregroundColor), ansi.Convert256(backgroundColor)) < minimumTextContrast ||
			contrastRatio(ansi.Convert16(foregroundColor), ansi.Convert16(backgroundColor)) < minimumTextContrast {
			return false
		}
	}
	return true
}

func contrastRatio(first, second color.Color) float64 {
	firstLuminance := relativeLuminance(first)
	secondLuminance := relativeLuminance(second)
	if firstLuminance < secondLuminance {
		firstLuminance, secondLuminance = secondLuminance, firstLuminance
	}
	return (firstLuminance + 0.05) / (secondLuminance + 0.05)
}

func relativeLuminance(value color.Color) float64 {
	red, green, blue, _ := value.RGBA()
	channel := func(component uint32) float64 {
		normalized := float64(component) / 65535
		if normalized <= 0.04045 {
			return normalized / 12.92
		}
		return math.Pow((normalized+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(red) + 0.7152*channel(green) + 0.0722*channel(blue)
}

func subtleDiffBackground(background, foreground string) (Color, bool) {
	backgroundRGB, backgroundOK := parseHexColor(background)
	foregroundRGB, foregroundOK := parseHexColor(foreground)
	if !backgroundOK || !foregroundOK {
		return "", false
	}
	var blended [3]uint16
	for index := range blended {
		blended[index] = (uint16(backgroundRGB[index])*7 +
			uint16(foregroundRGB[index]) + 4) / 8
	}
	return Color(fmt.Sprintf("#%02x%02x%02x", blended[0], blended[1], blended[2])), true
}

func parseHexColor(value string) ([3]uint8, bool) {
	if len(value) != 7 || value[0] != '#' {
		return [3]uint8{}, false
	}
	var color [3]uint8
	for index := range color {
		component, err := strconv.ParseUint(value[1+index*2:3+index*2], 16, 8)
		if err != nil {
			return [3]uint8{}, false
		}
		color[index] = uint8(component)
	}
	return color, true
}

func bundledPalette(name ThemeName, appearance Appearance) bundledThemePalette {
	switch name {
	case ThemeDark:
		return bundledThemePalette{
			"#18181c", ansiWhite, "#d2d2d2", "#a0a0a0",
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
			ansiBlack, ansiWhite, ansiGray, "darkgray",
			ansiBlack, ansiBlack, "lightgreen", "lightred",
			"lightcyan", "lightyellow", ansiGray,
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
