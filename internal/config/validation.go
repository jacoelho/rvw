package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/keymap"
)

// ErrInvalidConfig identifies an invalid config failure.
var ErrInvalidConfig = errors.New("invalid configuration")

// ParseAppearance parses and validates appearance.
func ParseAppearance(value string) (Appearance, error) {
	appearance := Appearance(value)
	if appearance != AppearanceDark && appearance != AppearanceLight && appearance != AppearanceSystem {
		return "", fmt.Errorf("%w: appearance must be dark, light, or system", ErrInvalidConfig)
	}
	return appearance, nil
}

// NewThemeName constructs and validates theme name.
func NewThemeName(value string) (ThemeName, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: theme name is not valid UTF-8", ErrInvalidConfig)
	}
	if value == "" || int64(len(value)) > 255 {
		return "", fmt.Errorf("%w: theme name must use 1 through 255 bytes", ErrInvalidConfig)
	}
	if value == "." || value == ".." || strings.ContainsAny(value, "\x00/\\") {
		return "", fmt.Errorf("%w: unsafe theme name %q", ErrInvalidConfig, value)
	}
	return ThemeName(value), nil
}

// NewPattern constructs and validates pattern.
func NewPattern(value string) (Pattern, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: filter pattern is not valid UTF-8", ErrInvalidConfig)
	}
	if value == "" || int64(len(value)) > MaxPatternBytes {
		return "", fmt.Errorf("%w: filter pattern must use 1 through %d bytes", ErrInvalidConfig, MaxPatternBytes)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("%w: filter pattern contains NUL", ErrInvalidConfig)
	}
	return Pattern(value), nil
}

func validate(config *Config, environment map[string]string) error {
	appearance, err := ParseAppearance(string(config.ui.Appearance))
	if err != nil {
		return err
	}
	config.ui.Appearance = appearance
	for _, name := range []*ThemeName{&config.ui.Theme, &config.ui.ThemeDark, &config.ui.ThemeLight} {
		if *name == "" {
			continue
		}
		validated, validationErr := NewThemeName(name.String())
		if validationErr != nil {
			return validationErr
		}
		*name = validated
	}
	config.ui.ColorEnabled = environment["NO_COLOR"] == "" || config.ui.ExplicitTheme

	if config.diff.View != DiffUnified && config.diff.View != DiffSideBySide {
		return fmt.Errorf("%w: diff.view must be unified or side-by-side", ErrInvalidConfig)
	}
	if config.diff.ContextLines < 0 || config.diff.ContextLines > 10_000 {
		return fmt.Errorf("%w: diff.context_lines must be from 0 through 10000", ErrInvalidConfig)
	}

	if filterErr := validateFilters(&config.filter); filterErr != nil {
		return filterErr
	}
	if config.github.SubmitDefault != SubmitComment &&
		config.github.SubmitDefault != SubmitApprove &&
		config.github.SubmitDefault != SubmitRequestChanges {
		return fmt.Errorf(
			"%w: github.submit_default must be comment, approve, or request-changes",
			ErrInvalidConfig,
		)
	}
	if templateErr := validateTemplate(
		config.export.MarkdownTemplate,
		"export.markdown_template",
		map[string]bool{
			"comments": true, "comment_count": true,
		},
	); templateErr != nil {
		return templateErr
	}
	if templateErr := validateTemplate(
		config.export.CommentTemplate,
		"export.comment_template",
		map[string]bool{
			"index": true, "target": true,
			"path": true, "line_or_range": true, "comment": true,
		},
	); templateErr != nil {
		return templateErr
	}
	effective, err := resolveKeybindings(config.ui.Keybindings)
	if err != nil {
		return err
	}
	config.ui.Keybindings = effective
	return nil
}

func validateFilters(filter *Filter) error {
	value := filter.IgnoreFile.String()
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "\x00/\\") {
		return fmt.Errorf("%w: filter.ignore_file must be one safe path component", ErrInvalidConfig)
	}
	if len(filter.Include)+len(filter.Exclude) > MaxFilterPatterns {
		return fmt.Errorf("%w: include/exclude patterns exceed %d", ErrInvalidConfig, MaxFilterPatterns)
	}
	for _, list := range [][]Pattern{filter.Include, filter.Exclude} {
		for index, pattern := range list {
			validated, err := NewPattern(pattern.String())
			if err != nil {
				return fmt.Errorf("filter pattern %d: %w", index, err)
			}
			list[index] = validated
		}
	}
	return nil
}

func validateTemplate(template Template, field string, allowed map[string]bool) error {
	value := template.String()
	if int64(len(value)) > MaxTemplateBytes {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidConfig, field, MaxTemplateBytes)
	}
	tokens := 0
	for value != "" {
		open := strings.Index(value, "{{")
		closeBeforeOpen := strings.Index(value, "}}")
		if closeBeforeOpen >= 0 && (open < 0 || closeBeforeOpen < open) {
			return fmt.Errorf("%w: %s contains unmatched }}", ErrInvalidConfig, field)
		}
		if open < 0 {
			tokens++
			break
		}
		if open > 0 {
			tokens++
		}
		value = value[open+2:]
		closeIndex := strings.Index(value, "}}")
		if closeIndex < 0 {
			return fmt.Errorf("%w: %s contains an unterminated placeholder", ErrInvalidConfig, field)
		}
		name := value[:closeIndex]
		if name == "" || strings.Contains(name, "{{") || !allowed[name] {
			return fmt.Errorf("%w: %s contains unknown placeholder %q", ErrInvalidConfig, field, name)
		}
		tokens++
		if tokens > MaxTemplateTokens {
			return fmt.Errorf("%w: %s exceeds %d tokens", ErrInvalidConfig, field, MaxTemplateTokens)
		}
		value = value[closeIndex+2:]
	}
	if tokens > MaxTemplateTokens {
		return fmt.Errorf("%w: %s exceeds %d tokens", ErrInvalidConfig, field, MaxTemplateTokens)
	}
	return nil
}

func resolveKeybindings(bindings []Keybinding) ([]Keybinding, error) {
	resolved, err := keymap.Resolve(bindings)
	if err != nil {
		return nil, fmt.Errorf("%w: ui.keybindings: %w", ErrInvalidConfig, err)
	}
	return resolved, nil
}
