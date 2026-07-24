package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLoadMergesPresenceAwareLayersAndOverrides(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	xdg := filepath.Join(root, "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "rvw"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(xdg, "rvw", "config.toml"), `
mouse = false
appearance = "dark"
[ui]
mouse = true
[diff]
context_lines = 0
[filter]
include = "user/**"
`)
	writeTestFile(t, filepath.Join(root, ".rvw.toml"), `
[diff]
wrap = false
[filter]
include = ["project/**"]
exclude = "**/*.min.js"
`)

	var overrides Overrides
	overrides.DisableMouse()
	pattern, _ := NewPattern("cli/**")
	overrides.AddInclude(pattern)
	config, err := Load(LoadRequest{
		CWD:         root,
		Environment: []string{"XDG_CONFIG_HOME=" + xdg, "HOME=" + filepath.Join(root, "home")},
		Overrides:   overrides,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.UI().Mouse {
		t.Fatal("CLI mouse override was not applied")
	}
	if config.UI().Appearance != AppearanceDark {
		t.Fatalf("appearance = %q", config.UI().Appearance)
	}
	if config.Diff().ContextLines != 0 || config.Diff().Wrap {
		t.Fatalf("unexpected diff configuration: %+v", config.Diff())
	}
	if got := config.Filter().Include; len(got) != 2 ||
		got[0].String() != "project/**" || got[1].String() != "cli/**" {
		t.Fatalf("include = %v", got)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	root := projectDirectory(t)
	writeTestFile(t, filepath.Join(root, ".rvw.toml"), "unknown = true\n")
	_, err := Load(LoadRequest{CWD: root})
	if !errors.Is(err, ErrInvalidConfig) && !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestDefaultsUseExportTemplates(t *testing.T) {
	export := Defaults().Export()
	if got, want := export.MarkdownTemplate.String(),
		"I reviewed your code and have the following comments. Please address them.\n\n{{comments}}\n"; got != want {
		t.Fatalf("markdown template = %q, want %q", got, want)
	}
	if got, want := export.CommentTemplate.String(),
		"{{index}}. `{{target}}` - {{comment}}"; got != want {
		t.Fatalf("comment template = %q, want %q", got, want)
	}
}

func TestDefaultsUseCommentGitHubSubmission(t *testing.T) {
	if got := Defaults().GitHub().SubmitDefault; got != SubmitComment {
		t.Fatalf("submit default = %q, want %q", got, SubmitComment)
	}
}

func TestDraftIsNotAFinalGitHubSubmissionEvent(t *testing.T) {
	configured := Defaults()
	configured.github.SubmitDefault = SubmitEvent("draft")
	if err := validate(&configured, map[string]string{}); err == nil ||
		!strings.Contains(err.Error(), "must be comment, approve, or request-changes") {
		t.Fatalf("draft submit default error = %v", err)
	}
}

func TestLoadRejectsProjectKeybindings(t *testing.T) {
	root := projectDirectory(t)
	writeTestFile(t, filepath.Join(root, ".rvw.toml"), `
[[ui.keybindings]]
context = "normal"
chord = "q"
action = "quit"
`)
	_, err := Load(LoadRequest{CWD: root})
	if err == nil || !strings.Contains(err.Error(), "project keybindings are forbidden") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadAcceptsStringOrArrayLists(t *testing.T) {
	root := projectDirectory(t)
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".config", "rvw"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, ".config", "rvw", "config.toml"), `
[filter]
include = "src/**"
exclude = ["vendor/**", "**/*.lock"]
`)
	config, err := Load(LoadRequest{CWD: root, Environment: []string{"HOME=" + home}})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Filter().Include) != 1 || len(config.Filter().Exclude) != 2 {
		t.Fatalf("unexpected lists: %+v", config.Filter())
	}
}

func TestLoadRejectsRemovedCommentCategories(t *testing.T) {
	root := projectDirectory(t)
	writeTestFile(t, filepath.Join(root, ".rvw.toml"), "[comments]\nkinds = [\"issue\"]\n")
	if _, err := Load(LoadRequest{CWD: root}); err == nil ||
		!strings.Contains(err.Error(), "unknown field comments") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsConfigOverByteLimit(t *testing.T) {
	root := projectDirectory(t)
	writeTestFile(t, filepath.Join(root, ".rvw.toml"), strings.Repeat("#", int(MaxConfigBytes)+1))
	_, err := Load(LoadRequest{CWD: root})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestValidationRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "context", content: "[diff]\ncontext_lines = 10001\n"},
		{name: "ignore path", content: "[filter]\nignore_file = \"../ignore\"\n"},
		{name: "unknown placeholder", content: "[export]\nmarkdown_template = \"{{unknown}}\"\n"},
		{name: "reserved key", content: "[[ui.keybindings]]\ncontext=\"normal\"\nchord=\"Ctrl-c\"\naction=\"quit\"\n"},
		{
			name: "inline context and focus",
			content: `[[ui.keybindings]]
context = "normal.tree"
focus = "diff"
chord = "x"
action = "activate"
`,
		},
		{
			name: "global focused prefix conflict",
			content: `[[ui.keybindings]]
context = "normal.diff"
chord = "q q"
action = "activate"
`,
		},
		{
			name: "focused required action is incomplete",
			content: `[[ui.keybindings]]
context = "normal"
chord = "q"
unbind = true

[[ui.keybindings]]
context = "normal.diff"
chord = "q"
action = "quit"
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := projectDirectory(t)
			home := filepath.Join(root, "home")
			if err := os.MkdirAll(filepath.Join(home, ".config", "rvw"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(home, ".config", "rvw", "config.toml"), test.content)
			if _, err := Load(LoadRequest{CWD: root, Environment: []string{"HOME=" + home}}); err == nil {
				t.Fatal("Load() succeeded, want validation error")
			}
		})
	}
}

func TestLoadResolvesBoundedCustomTheme(t *testing.T) {
	root := projectDirectory(t)
	home := filepath.Join(root, "home")
	configDirectory := filepath.Join(home, ".config", "rvw")
	themesDirectory := filepath.Join(configDirectory, "themes")
	if err := os.MkdirAll(themesDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(configDirectory, "config.toml"), "theme = \"mine.v2\"\n")
	writeTestFile(t, filepath.Join(themesDirectory, "syntax.tmTheme"), `<?xml version="1.0"?>
<plist version="1.0"><dict><key>settings</key><array>
<dict><key>scope</key><string>keyword.control</string><key>settings</key>
<dict><key>foreground</key><string>#123456</string></dict></dict>
</array></dict></plist>`)
	writeTestFile(t, filepath.Join(themesDirectory, "mine.v2.toml"), `
panel_bg = "#010203"
fg_primary = "White"
fg_secondary = "light-blue"
fg_dim = "dark_gray"
diff_add = "green"
diff_del = "red"
diff_context = "gray"
pending = "yellow"
border_focused = "cyan"
status_bar_bg = "black"
cursor_line_bg = "#AABBCC"
syntax_theme = "syntax.tmTheme"
`)

	resolved, err := Load(LoadRequest{
		CWD: root, Environment: []string{"HOME=" + home, "NO_COLOR=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	theme, ok := resolved.Theme(ThemeName("mine.v2"))
	if !ok || !theme.UserDefined || theme.Colors[themeSecondaryForeground] != Color("lightblue") ||
		theme.SyntaxTheme.String() != filepath.Join(themesDirectory, "syntax.tmTheme") ||
		theme.SyntaxColors[SyntaxKeyword] != Color("#123456") {
		t.Fatalf("unexpected resolved theme: %+v", theme)
	}
	if !resolved.UI().ColorEnabled {
		t.Fatal("explicit theme did not override NO_COLOR")
	}
}

func TestLoadRejectsConfigurationAndThemeFIFOsWithoutBlocking(t *testing.T) {
	root := projectDirectory(t)
	home := filepath.Join(root, "home")
	configDirectory := filepath.Join(home, ".config", "rvw")
	if err := os.MkdirAll(filepath.Join(configDirectory, "themes"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDirectory, "config.toml")
	if err := syscall.Mkfifo(configPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(LoadRequest{
		CWD: root, Environment: []string{"HOME=" + home},
	}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("configuration FIFO error = %v", err)
	}
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, configPath, "theme = \"fifo\"\n")
	themePath := filepath.Join(configDirectory, "themes", "fifo.toml")
	if err := syscall.Mkfifo(themePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(LoadRequest{
		CWD: root, Environment: []string{"HOME=" + home},
	}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("theme FIFO error = %v", err)
	}
}

func TestSyntaxThemeRejectsMalformedContentAndParentComponents(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "invalid.tmTheme"), "not XML")
	if _, _, err := validateSyntaxTheme(directory, "invalid.tmTheme"); err == nil {
		t.Fatal("malformed TextMate theme unexpectedly accepted")
	}
	if _, _, err := validateSyntaxTheme(
		directory,
		"sub/../invalid.tmTheme",
	); err == nil || !strings.Contains(err.Error(), "parent path component") {
		t.Fatalf("parent component error = %v", err)
	}
}

func TestDefaultThemeResolvesAgainstAppearance(t *testing.T) {
	root := projectDirectory(t)
	dark, err := Load(LoadRequest{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	if dark.ActiveTheme() != ThemeGruvboxDark {
		t.Fatalf("system active theme = %q", dark.ActiveTheme())
	}

	writeTestFile(t, filepath.Join(root, ".rvw.toml"), "appearance = \"light\"\ntheme = \"default\"\n")
	light, err := Load(LoadRequest{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	if light.ActiveTheme() != ThemeGruvboxLight {
		t.Fatalf("light active theme = %q", light.ActiveTheme())
	}
}

func TestCuratedThemeAliasesResolveTheirOwnPaletteAndSyntaxTheme(t *testing.T) {
	tests := []struct {
		name       ThemeName
		background Color
		syntax     ThemeName
	}{
		{name: ThemeDark, background: "#18181c", syntax: ThemeBase16OceanDark},
		{name: ThemeLight, background: "#f5f3e8", syntax: ThemeInspiredGitHub},
		{name: ThemeANSI16Dark, background: "black", syntax: ThemeBase16OceanDark},
		{name: ThemeTokyoNightStorm, background: "#24283b", syntax: ThemeBase16OceanDark},
		{name: ThemeTokyoNightDay, background: "#e1e2e7", syntax: ThemeInspiredGitHub},
		{name: ThemeGruvboxDark, background: "#282828", syntax: ThemeBase16OceanDark},
		{name: ThemeGruvboxLight, background: "#fbf1c7", syntax: ThemeInspiredGitHub},
		{name: ThemeCatppuccinLatte, background: "#eff1f5", syntax: ThemeInspiredGitHub},
		{name: ThemeCatppuccinFrappe, background: "#303446", syntax: ThemeBase16OceanDark},
		{name: ThemeCatppuccinMacchiato, background: "#24273a", syntax: ThemeBase16OceanDark},
		{name: ThemeCatppuccinMocha, background: "#1e1e2e", syntax: ThemeBase16OceanDark},
	}
	for _, test := range tests {
		t.Run(test.name.String(), func(t *testing.T) {
			root := projectDirectory(t)
			writeTestFile(
				t,
				filepath.Join(root, ".rvw.toml"),
				"theme = \""+test.name.String()+"\"\n",
			)

			resolved, err := Load(LoadRequest{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			theme, ok := resolved.Theme(test.name)
			if !ok {
				t.Fatalf("theme %q was not resolved", test.name)
			}
			if got := theme.Colors[themePanelBackground]; got != test.background {
				t.Fatalf("panel_bg = %q, want %q", got, test.background)
			}
			if got := theme.SyntaxTheme.String(); got != test.syntax.String() {
				t.Fatalf("syntax theme = %q, want %q", got, test.syntax)
			}
		})
	}
}

func TestBundledTrueColorThemesAddSubtleDiffBackgrounds(t *testing.T) {
	dark := bundledTheme(ThemeGruvboxDark, AppearanceDark)
	light := bundledTheme(ThemeGruvboxLight, AppearanceLight)
	for _, theme := range []Theme{dark, light} {
		if theme.Colors[themeDiffAdditionBackground] == theme.Colors[themePanelBackground] ||
			theme.Colors[themeDiffDeletionBackground] == theme.Colors[themePanelBackground] {
			t.Fatalf("theme %q generated an invisible semantic background", theme.Name)
		}
	}

	ansiTheme := bundledTheme(ThemeANSI16Dark, AppearanceDark)
	if _, exists := ansiTheme.Colors[themeDiffAdditionBackground]; exists {
		t.Fatal("ansi16-dark unexpectedly generated an RGB addition background")
	}
	if _, exists := ansiTheme.Colors[themeDiffDeletionBackground]; exists {
		t.Fatal("ansi16-dark unexpectedly generated an RGB deletion background")
	}
}

func TestEveryBundledThemeMeetsCanonicalProfileTextContrast(t *testing.T) {
	themes := []struct {
		name       ThemeName
		appearance Appearance
	}{
		{name: ThemeDark, appearance: AppearanceDark},
		{name: ThemeLight, appearance: AppearanceLight},
		{name: ThemeANSI16Dark, appearance: AppearanceDark},
		{name: ThemeTokyoNightStorm, appearance: AppearanceDark},
		{name: ThemeTokyoNightDay, appearance: AppearanceLight},
		{name: ThemeGruvboxDark, appearance: AppearanceDark},
		{name: ThemeGruvboxLight, appearance: AppearanceLight},
		{name: ThemeCatppuccinLatte, appearance: AppearanceLight},
		{name: ThemeCatppuccinFrappe, appearance: AppearanceDark},
		{name: ThemeCatppuccinMacchiato, appearance: AppearanceDark},
		{name: ThemeCatppuccinMocha, appearance: AppearanceDark},
	}
	for name := range embeddedSyntaxThemes {
		themes = append(themes,
			struct {
				name       ThemeName
				appearance Appearance
			}{name: name, appearance: AppearanceDark},
			struct {
				name       ThemeName
				appearance Appearance
			}{name: name, appearance: AppearanceLight},
		)
	}
	pairs := map[ThemeColor][]ThemeColor{
		themePrimaryForeground:   {themePanelBackground, themeStatusBackground, themeCursorBackground, themeDiffAdditionBackground, themeDiffDeletionBackground},
		themeSecondaryForeground: {themePanelBackground, themeStatusBackground},
		themeDimForeground:       {themePanelBackground, themeStatusBackground, themeCursorBackground, themeDiffAdditionBackground, themeDiffDeletionBackground},
		themeDiffAddition:        {themePanelBackground, themeCursorBackground, themeDiffAdditionBackground},
		themeDiffDeletion:        {themePanelBackground, themeStatusBackground, themeCursorBackground, themeDiffDeletionBackground},
		themeDiffContext:         {themePanelBackground, themeCursorBackground},
		themePending:             {themePanelBackground},
		themeFocusedBorder:       {themePanelBackground, themeStatusBackground, themeCursorBackground},
		themeDiffHunkHeader:      {themePanelBackground, themeStatusBackground, themeCursorBackground},
		themeWarningForeground:   {themePanelBackground, themeStatusBackground},
	}
	for _, test := range themes {
		theme := bundledTheme(test.name, test.appearance)
		for foregroundRole, backgroundRoles := range pairs {
			foreground, ok := bundledTestColor(theme.Colors[foregroundRole])
			if !ok {
				t.Fatalf("theme %q role %q has unknown color %q", test.name, foregroundRole, theme.Colors[foregroundRole])
			}
			for _, backgroundRole := range backgroundRoles {
				backgroundValue, exists := theme.Colors[backgroundRole]
				if !exists {
					continue
				}
				background, ok := bundledTestColor(backgroundValue)
				if !ok {
					t.Fatalf("theme %q role %q has unknown color %q", test.name, backgroundRole, backgroundValue)
				}
				if !contrastsWithProfiles(foreground, [][3]uint8{background}) {
					t.Fatalf(
						"theme %q foreground %q %q does not meet %.1f:1 against %q %q across profiles",
						test.name,
						foregroundRole,
						theme.Colors[foregroundRole],
						minimumTextContrast,
						backgroundRole,
						theme.Colors[backgroundRole],
					)
				}
			}
		}
		for _, syntaxRole := range []SyntaxColor{SyntaxKeyword, SyntaxString, SyntaxComment, SyntaxNumber} {
			foreground, ok := bundledTestColor(theme.SyntaxColors[syntaxRole])
			if !ok {
				t.Fatalf("theme %q syntax role %q has unknown color %q", test.name, syntaxRole, theme.SyntaxColors[syntaxRole])
			}
			for _, backgroundRole := range []ThemeColor{
				themePanelBackground,
				themeCursorBackground,
				themeDiffAdditionBackground,
				themeDiffDeletionBackground,
			} {
				backgroundValue, exists := theme.Colors[backgroundRole]
				if !exists {
					continue
				}
				background, ok := bundledTestColor(backgroundValue)
				if !ok || !contrastsWithProfiles(foreground, [][3]uint8{background}) {
					t.Fatalf(
						"theme %q syntax %q %q does not meet %.1f:1 against %q %q across canonical profiles",
						test.name,
						syntaxRole,
						theme.SyntaxColors[syntaxRole],
						minimumTextContrast,
						backgroundRole,
						backgroundValue,
					)
				}
			}
		}
	}
}

func bundledTestColor(value Color) ([3]uint8, bool) {
	if parsed, ok := parseHexColor(string(value)); ok {
		return parsed, true
	}
	named := map[Color]ansi.BasicColor{
		"black": ansi.Black, "red": ansi.Red, "green": ansi.Green,
		"yellow": ansi.Yellow, "blue": ansi.Blue, "magenta": ansi.Magenta,
		"cyan": ansi.Cyan, "gray": ansi.White, "grey": ansi.White,
		"darkgray": ansi.BrightBlack, "darkgrey": ansi.BrightBlack,
		"lightred": ansi.BrightRed, "lightgreen": ansi.BrightGreen,
		"lightyellow": ansi.BrightYellow, "lightblue": ansi.BrightBlue,
		"lightmagenta": ansi.BrightMagenta, "lightcyan": ansi.BrightCyan,
		"white": ansi.BrightWhite,
	}
	resolved, ok := named[value]
	if !ok {
		return [3]uint8{}, false
	}
	red, green, blue, _ := resolved.RGBA()
	return [3]uint8{uint8(red >> 8), uint8(green >> 8), uint8(blue >> 8)}, true
}

func TestEmbeddedSyntaxThemesUseLightAppearanceFallbackPalette(t *testing.T) {
	names := []ThemeName{
		ThemeBase16OceanDark,
		ThemeBase16EightiesDark,
		ThemeBase16MochaDark,
		ThemeBase16OceanLight,
		ThemeInspiredGitHub,
		ThemeSolarizedDark,
		ThemeSolarizedLight,
	}
	for _, name := range names {
		t.Run(name.String(), func(t *testing.T) {
			root := projectDirectory(t)
			writeTestFile(
				t,
				filepath.Join(root, ".rvw.toml"),
				"appearance = \"light\"\ntheme = \""+name.String()+"\"\n",
			)

			resolved, err := Load(LoadRequest{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			theme, ok := resolved.Theme(name)
			if !ok {
				t.Fatalf("theme %q was not resolved", name)
			}
			if got := theme.Colors[themePanelBackground]; got != Color("#fbf1c7") {
				t.Fatalf("panel_bg = %q, want gruvbox-light fallback", got)
			}
			if got := theme.SyntaxTheme.String(); got != name.String() {
				t.Fatalf("syntax theme = %q, want %q", got, name)
			}
		})
	}
}

func TestLoadRejectsThemeSymlinkEscape(t *testing.T) {
	root := projectDirectory(t)
	home := filepath.Join(root, "home")
	configDirectory := filepath.Join(home, ".config", "rvw")
	themesDirectory := filepath.Join(configDirectory, "themes")
	if err := os.MkdirAll(themesDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(configDirectory, "config.toml"), "theme = \"outside\"\n")
	outside := filepath.Join(t.TempDir(), "outside.toml")
	writeTestFile(t, outside, strings.Repeat("x", 32))
	if err := os.Symlink(outside, filepath.Join(themesDirectory, "outside.toml")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Load(LoadRequest{CWD: root, Environment: []string{"HOME=" + home}}); err == nil {
		t.Fatal("outside theme symlink was followed")
	}
}

func TestLoadRejectsProjectConfigSymlinkEscape(t *testing.T) {
	root := projectDirectory(t)
	outside := filepath.Join(t.TempDir(), "outside.toml")
	writeTestFile(t, outside, "appearance = \"light\"\n")
	if err := os.Symlink(outside, filepath.Join(root, ".rvw.toml")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Load(LoadRequest{CWD: root, ProjectRoot: root}); err == nil {
		t.Fatal("outside project-config symlink was followed")
	}
}

func projectDirectory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
