package config

import (
	"fmt"
	"math"
	"strconv"
	"testing"
)

func TestBundledTrueColorPalettesMeetTextContrastFloor(t *testing.T) {
	themes := []struct {
		name       ThemeName
		appearance Appearance
	}{
		{ThemeDark, AppearanceDark},
		{ThemeLight, AppearanceLight},
		{ThemeTokyoNightStorm, AppearanceDark},
		{ThemeTokyoNightDay, AppearanceLight},
		{ThemeGruvboxDark, AppearanceDark},
		{ThemeGruvboxLight, AppearanceLight},
		{ThemeCatppuccinLatte, AppearanceLight},
		{ThemeCatppuccinFrappe, AppearanceDark},
		{ThemeCatppuccinMacchiato, AppearanceDark},
		{ThemeCatppuccinMocha, AppearanceDark},
	}
	for _, theme := range themes {
		t.Run(theme.name.String(), func(t *testing.T) {
			palette := bundledPalette(theme.name, theme.appearance)
			pairs := []struct {
				role       string
				foreground string
				background string
			}{
				{"primary", palette.primary, palette.background},
				{"secondary", palette.secondary, palette.background},
				{"muted", palette.muted, palette.background},
				{"addition", palette.addition, palette.background},
				{"deletion", palette.deletion, palette.background},
				{"context", palette.context, palette.background},
				{"focused", palette.accent, palette.background},
				{"warning", palette.warning, palette.background},
				{"status", palette.secondary, palette.header},
				{"selected", palette.primary, palette.selected},
			}
			for _, pair := range pairs {
				ratio := contrastRatio(t, pair.foreground, pair.background)
				if ratio < 4.5 {
					t.Errorf(
						"%s contrast %s on %s = %.3f, want at least 4.5",
						pair.role,
						pair.foreground,
						pair.background,
						ratio,
					)
				}
			}
		})
	}
}

func contrastRatio(t *testing.T, foreground, background string) float64 {
	t.Helper()
	foregroundLuminance := relativeLuminance(t, foreground)
	backgroundLuminance := relativeLuminance(t, background)
	lighter := max(foregroundLuminance, backgroundLuminance)
	darker := min(foregroundLuminance, backgroundLuminance)
	return (lighter + 0.05) / (darker + 0.05)
}

func relativeLuminance(t *testing.T, value string) float64 {
	t.Helper()
	switch value {
	case "black":
		return 0
	case "white":
		return 1
	}
	if len(value) != 7 || value[0] != '#' {
		t.Fatalf("contrast test requires an RGB color, got %q", value)
	}
	channels := [3]float64{}
	for index := range channels {
		channel, err := strconv.ParseUint(value[1+index*2:3+index*2], 16, 8)
		if err != nil {
			t.Fatal(fmt.Errorf("parse color %q: %w", value, err))
		}
		normalized := float64(channel) / 255
		if normalized <= 0.04045 {
			channels[index] = normalized / 12.92
		} else {
			channels[index] = math.Pow((normalized+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}
