package app

import (
	"fmt"
	"testing"

	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
)

func TestNewSettingsRequiresEveryColorPresentationRole(t *testing.T) {
	request := validPresentationSettingsRequest(t, true)
	for _, missing := range requiredColorPaletteRoles {
		t.Run(paletteRoleName(missing), func(t *testing.T) {
			colors := make(map[PaletteRole]string, len(request.Palette.colors)-1)
			for role, value := range request.Palette.colors {
				if role != missing {
					colors[role] = value
				}
			}
			palette, err := NewPalette(colors)
			if err != nil {
				t.Fatal(err)
			}
			request := request
			request.Palette = palette
			_, err = NewSettings(request)
			want := fmt.Sprintf(
				"color-enabled palette is missing required role %s",
				paletteRoleName(missing),
			)
			if err == nil || err.Error() != want {
				t.Fatalf("NewSettings() error = %v, want %q", err, want)
			}
		})
	}
}

func TestNewSettingsAllowsMissingPaletteRolesWithoutColor(t *testing.T) {
	request := validPresentationSettingsRequest(t, false)
	request.Palette = Palette{}
	settings, err := NewSettings(request)
	if err != nil {
		t.Fatal(err)
	}
	if settings.colorEnabled {
		t.Fatal("monochrome settings unexpectedly enabled color")
	}
}

func validPresentationSettingsRequest(t *testing.T, colorEnabled bool) SettingsRequest {
	t.Helper()
	features := SettingFeatures{}
	var err error
	if colorEnabled {
		features, err = NewSettingFeatures(FeatureColor)
		if err != nil {
			t.Fatal(err)
		}
	}
	contextLines, err := review.NewContextLines(3)
	if err != nil {
		t.Fatal(err)
	}
	selector, err := review.NewSyntaxSelector("test")
	if err != nil {
		t.Fatal(err)
	}
	templates, err := review.CompileExportTemplates("{{comments}}", "{{comment}}")
	if err != nil {
		t.Fatal(err)
	}
	colors := make(map[PaletteRole]string, len(requiredColorPaletteRoles))
	for _, role := range requiredColorPaletteRoles {
		colors[role] = "#ffffff"
	}
	palette, err := NewPalette(colors)
	if err != nil {
		t.Fatal(err)
	}
	return SettingsRequest{
		Palette:          palette,
		SyntaxSelector:   selector,
		Keybindings:      []keymap.Binding{{Context: keymap.ContextNormal, Chord: "q", Action: keymap.ActionQuit}},
		ExportTemplates:  templates,
		PublicationEvent: review.PublicationEventComment,
		ContextLines:     contextLines,
		Features:         features,
		DiffView:         DiffViewUnified,
	}
}
