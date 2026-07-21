package config

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

var ErrInvalidConfig = errors.New("invalid configuration")

func ParseAppearance(value string) (Appearance, error) {
	appearance := Appearance(value)
	if appearance != AppearanceDark && appearance != AppearanceLight && appearance != AppearanceSystem {
		return "", fmt.Errorf("%w: appearance must be dark, light, or system", ErrInvalidConfig)
	}
	return appearance, nil
}

func NewThemeName(value string) (ThemeName, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: theme name is not valid UTF-8", ErrInvalidConfig)
	}
	if len(value) == 0 || int64(len(value)) > 255 {
		return "", fmt.Errorf("%w: theme name must use 1 through 255 bytes", ErrInvalidConfig)
	}
	if value == "." || value == ".." || strings.ContainsAny(value, "\x00/\\") {
		return "", fmt.Errorf("%w: unsafe theme name %q", ErrInvalidConfig, value)
	}
	return ThemeName(value), nil
}

func NewPattern(value string) (Pattern, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: filter pattern is not valid UTF-8", ErrInvalidConfig)
	}
	if len(value) == 0 || int64(len(value)) > MaxPatternBytes {
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
		validated, err := NewThemeName(name.String())
		if err != nil {
			return err
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

	if err := validateFilters(&config.filter); err != nil {
		return err
	}
	if config.github.SubmitDefault != SubmitComment &&
		config.github.SubmitDefault != SubmitDraft &&
		config.github.SubmitDefault != SubmitApprove &&
		config.github.SubmitDefault != SubmitRequestChanges {
		return fmt.Errorf(
			"%w: github.submit_default must be comment, draft, approve, or request-changes",
			ErrInvalidConfig,
		)
	}
	if err := validateTemplate(
		config.export.MarkdownTemplate,
		"export.markdown_template",
		map[string]bool{"comments": true, "comment_count": true},
	); err != nil {
		return err
	}
	if err := validateTemplate(
		config.export.CommentTemplate,
		"export.comment_template",
		map[string]bool{
			"index": true, "target": true,
			"path": true, "line_or_range": true, "comment": true,
		},
	); err != nil {
		return err
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
	for len(value) > 0 {
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

type bindingKey struct {
	context KeyContext
	focus   KeyFocus
	chord   KeyChord
}

func resolveKeybindings(bindings []Keybinding) ([]Keybinding, error) {
	if len(bindings) > MaxKeybindings {
		return nil, fmt.Errorf("%w: ui.keybindings exceeds %d entries", ErrInvalidConfig, MaxKeybindings)
	}
	effective := make(map[bindingKey]Keybinding)
	for _, binding := range defaultKeybindings() {
		key := bindingKey{context: binding.Context, focus: binding.Focus, chord: binding.Chord}
		effective[key] = binding
	}
	userKeys := make(map[bindingKey]bool, len(bindings))
	for index, binding := range bindings {
		binding, err := normalizeBinding(binding)
		if err != nil {
			return nil, fmt.Errorf("ui.keybindings[%d]: %w", index, err)
		}
		hasAction := binding.Action != ""
		if hasAction == binding.Unbind {
			return nil, fmt.Errorf(
				"%w: ui.keybindings[%d] requires exactly one of action or unbind",
				ErrInvalidConfig,
				index,
			)
		}
		if hasAction && !keyActions[binding.Action] {
			return nil, fmt.Errorf(
				"%w: ui.keybindings[%d] has unknown action %q",
				ErrInvalidConfig,
				index,
				binding.Action,
			)
		}
		if hasAction && !actionAllowed(binding.Context, binding.Action) {
			return nil, fmt.Errorf(
				"%w: action %q is unreachable in context %q",
				ErrInvalidConfig,
				binding.Action,
				binding.Context,
			)
		}
		key := bindingKey{context: binding.Context, focus: binding.Focus, chord: binding.Chord}
		if userKeys[key] {
			return nil, fmt.Errorf("%w: duplicate binding for %s", ErrInvalidConfig, binding.Chord)
		}
		userKeys[key] = true
		if binding.Unbind {
			delete(effective, key)
		} else {
			effective[key] = binding
		}
	}
	if err := validateEffectiveKeymap(effective); err != nil {
		return nil, err
	}

	result := make([]Keybinding, 0, len(effective))
	for _, binding := range effective {
		result = append(result, binding)
	}
	slices.SortFunc(result, func(left, right Keybinding) int {
		if result := cmp.Compare(left.Context, right.Context); result != 0 {
			return result
		}
		if result := cmp.Compare(left.Focus, right.Focus); result != 0 {
			return result
		}
		return cmp.Compare(left.Chord, right.Chord)
	})
	return result, nil
}

func normalizeBinding(binding Keybinding) (Keybinding, error) {
	switch binding.Context {
	case KeyContext("normal.tree"):
		if binding.Focus != "" {
			return Keybinding{}, fmt.Errorf("%w: inline normal context cannot also set focus", ErrInvalidConfig)
		}
		binding.Context, binding.Focus = KeyContext("normal"), KeyFocus("file-tree")
	case KeyContext("normal.diff"):
		if binding.Focus != "" {
			return Keybinding{}, fmt.Errorf("%w: inline normal context cannot also set focus", ErrInvalidConfig)
		}
		binding.Context, binding.Focus = KeyContext("normal"), KeyFocus("diff")
	case KeyContext("normal.comments"):
		if binding.Focus != "" {
			return Keybinding{}, fmt.Errorf("%w: inline normal context cannot also set focus", ErrInvalidConfig)
		}
		binding.Context, binding.Focus = KeyContext("normal"), KeyFocus("comments")
	}
	if !keyContexts[binding.Context] {
		return Keybinding{}, fmt.Errorf("%w: unknown context %q", ErrInvalidConfig, binding.Context)
	}
	if binding.Context == KeyContext("normal") {
		if binding.Focus != "" && !keyFocuses[binding.Focus] {
			return Keybinding{}, fmt.Errorf("%w: unknown focus %q", ErrInvalidConfig, binding.Focus)
		}
	} else if binding.Focus != "" {
		return Keybinding{}, fmt.Errorf("%w: focus is allowed only for normal", ErrInvalidConfig)
	}
	chord := strings.Join(strings.Fields(string(binding.Chord)), " ")
	strokes := strings.Fields(chord)
	if len(chord) == 0 || int64(len(chord)) > 128 || len(strokes) == 0 || len(strokes) > 4 {
		return Keybinding{}, fmt.Errorf("%w: chord must have 1..4 strokes and at most 128 bytes", ErrInvalidConfig)
	}
	for _, stroke := range strokes {
		if strings.EqualFold(stroke, "ctrl-c") {
			return Keybinding{}, fmt.Errorf("%w: Ctrl-c is reserved", ErrInvalidConfig)
		}
	}
	binding.Chord = KeyChord(chord)
	return binding, nil
}

func validateEffectiveKeymap(effective map[bindingKey]Keybinding) error {
	for _, focus := range []KeyFocus{KeyFocusFileTree, KeyFocusDiff, KeyFocusComments} {
		scope := make(map[KeyChord]Keybinding)
		for key, binding := range effective {
			if key.context == KeyContextNormal && key.focus == "" {
				scope[key.chord] = binding
			}
		}
		for key, binding := range effective {
			if key.context == KeyContextNormal && key.focus == focus {
				scope[key.chord] = binding
			}
		}
		if err := validateRuntimeKeyScope(KeyContextNormal, focus, scope); err != nil {
			return err
		}
	}
	for context := range requiredActions {
		if context == KeyContextNormal {
			continue
		}
		scope := make(map[KeyChord]Keybinding)
		for key, binding := range effective {
			if key.context == context {
				scope[key.chord] = binding
			}
		}
		if err := validateRuntimeKeyScope(context, "", scope); err != nil {
			return err
		}
	}
	return nil
}

func validateRuntimeKeyScope(
	context KeyContext,
	focus KeyFocus,
	bindings map[KeyChord]Keybinding,
) error {
	prefixes := make([][]string, 0, len(bindings))
	actions := make(map[KeyAction]bool, len(bindings))
	for chord, binding := range bindings {
		strokes := strings.Fields(chord.String())
		for _, prior := range prefixes {
			if isStrokePrefix(prior, strokes) || isStrokePrefix(strokes, prior) {
				return fmt.Errorf(
					"%w: context %q focus %q has prefix-conflicting chord %q",
					ErrInvalidConfig,
					context,
					focus,
					chord,
				)
			}
		}
		prefixes = append(prefixes, strokes)
		actions[binding.Action] = true
	}
	for _, action := range requiredActions[context] {
		if !actions[action] {
			return fmt.Errorf(
				"%w: context %q focus %q is missing required action %q",
				ErrInvalidConfig,
				context,
				focus,
				action,
			)
		}
	}
	return nil
}

func isStrokePrefix(left, right []string) bool {
	return len(left) < len(right) && slices.Equal(left, right[:len(left)])
}

var keyContexts = stringSet[KeyContext](
	"normal",
	"visual", "comment-edit", "search", "command", "help", "status-details", "confirm",
	"export-picker", "submit-preflight", "minimum-size",
)

var requiredActions = map[KeyContext][]KeyAction{
	"normal":           {"quit", "interrupt"},
	"visual":           {"cancel"},
	"comment-edit":     {"cancel", "save-comment"},
	"search":           {"cancel"},
	"command":          {"cancel"},
	"help":             {"cancel"},
	"status-details":   {"cancel"},
	"confirm":          {"cancel", "confirm-yes", "confirm-no"},
	"export-picker":    {"cancel", "export-selected"},
	"submit-preflight": {"cancel", "submit-review"},
	"minimum-size":     {"quit", "interrupt"},
}

func defaultKeybindings() []Keybinding {
	return []Keybinding{
		binding(KeyContextNormal, "", "q", KeyActionQuit),
		binding(KeyContextNormal, "", "Ctrl-c", KeyActionInterrupt),
		binding(KeyContextNormal, "", "Tab", KeyActionFocusNextPane),
		binding(KeyContextNormal, "", "Shift-Tab", KeyActionFocusPreviousPane),
		binding(KeyContextNormal, "", "j", KeyActionMoveDown),
		binding(KeyContextNormal, "", "down", KeyActionMoveDown),
		binding(KeyContextNormal, "", "k", KeyActionMoveUp),
		binding(KeyContextNormal, "", "up", KeyActionMoveUp),
		binding(KeyContextNormal, "", "Ctrl-f", KeyActionPageDown),
		binding(KeyContextNormal, "", "Ctrl-b", KeyActionPageUp),
		binding(KeyContextNormal, "", "pgdown", KeyActionPageDown),
		binding(KeyContextNormal, "", "pgup", KeyActionPageUp),
		binding(KeyContextNormal, "", "Ctrl-d", KeyActionHalfPageDown),
		binding(KeyContextNormal, "", "Ctrl-u", KeyActionHalfPageUp),
		binding(KeyContextNormal, "", "g g", KeyActionFirst),
		binding(KeyContextNormal, "", "G", KeyActionLast),
		binding(KeyContextNormal, "", "Enter", KeyActionActivate),
		binding(KeyContextNormal, "", "c", KeyActionCommentAtCursor),
		binding(KeyContextNormal, "", "C", KeyActionFileComment),
		binding(KeyContextNormal, "", "v", KeyActionVisualLine),
		binding(KeyContextNormal, "", "V", KeyActionVisualHunk),
		binding(KeyContextNormal, "", "e", KeyActionEditComment),
		binding(KeyContextNormal, "", "d d", KeyActionDeleteComment),
		binding(KeyContextNormal, "", "r", KeyActionToggleFileReviewed),
		binding(KeyContextNormal, "", "R", KeyActionToggleHunkReviewed),
		binding(KeyContextNormal, "", "w", KeyActionToggleWrap),
		binding(KeyContextNormal, "", "t", KeyActionToggleView),
		binding(KeyContextNormal, "", "] f", KeyActionNextFile),
		binding(KeyContextNormal, "", "[ f", KeyActionPreviousFile),
		binding(KeyContextNormal, "", "] h", KeyActionNextHunk),
		binding(KeyContextNormal, "", "[ h", KeyActionPreviousHunk),
		binding(KeyContextNormal, "", "] c", KeyActionNextComment),
		binding(KeyContextNormal, "", "[ c", KeyActionPreviousComment),
		binding(KeyContextNormal, "", "y", KeyActionCopyMarkdown),
		binding(KeyContextNormal, "", "M", KeyActionExportMarkdown),
		binding(KeyContextNormal, "", "J", KeyActionExportJSON),
		binding(KeyContextNormal, "", "s", KeyActionOpenSubmit),
		binding(KeyContextNormal, "", "F2", KeyActionStatusDetails),
		binding(KeyContextNormal, "", "?", KeyActionHelp),
		binding(KeyContextNormal, "", "/", KeyActionSearch),
		binding(KeyContextNormal, "", "n", KeyActionNextSearchHit),
		binding(KeyContextNormal, "", "N", KeyActionPreviousSearchHit),
		binding(KeyContextNormal, "", ":", KeyActionCommand),
		binding(KeyContextNormal, "", "Ctrl-r", KeyActionReload),
		binding(KeyContextNormal, KeyFocusFileTree, "space", KeyActionToggleDirectory),
		binding(KeyContextNormal, "", "z f", KeyActionToggleFilePanel),
		binding(KeyContextNormal, "", "z c", KeyActionToggleComments),
		binding(KeyContextVisual, "", "Esc", KeyActionCancel),
		binding(KeyContextVisual, "", "j", KeyActionExtendDown),
		binding(KeyContextVisual, "", "k", KeyActionExtendUp),
		binding(KeyContextVisual, "", "c", KeyActionCommentSelection),
		binding(KeyContextCommentEdit, "", "Esc", KeyActionCancel),
		binding(KeyContextCommentEdit, "", "Ctrl-s", KeyActionSaveComment),
		binding(KeyContextSearch, "", "Esc", KeyActionCancel),
		binding(KeyContextSearch, "", "Enter", "accept"),
		binding(KeyContextCommand, "", "Esc", KeyActionCancel),
		binding(KeyContextCommand, "", "Enter", "accept"),
		binding(KeyContextHelp, "", "Esc", KeyActionCancel),
		binding(KeyContextHelp, "", "q", KeyActionCancel),
		binding(KeyContextHelp, "", "j", KeyActionHelpLineDown),
		binding(KeyContextHelp, "", "k", KeyActionHelpLineUp),
		binding(KeyContextHelp, "", "pgdown", KeyActionHelpPageDown),
		binding(KeyContextHelp, "", "pgup", KeyActionHelpPageUp),
		binding(KeyContextHelp, "", "g g", KeyActionHelpHome),
		binding(KeyContextHelp, "", "G", KeyActionHelpEnd),
		binding(KeyContextHelp, "", "Tab", KeyActionNextHelpPage),
		binding(KeyContextHelp, "", "Shift-Tab", KeyActionPreviousHelpPage),
		binding(KeyContextStatusDetails, "", "Esc", KeyActionCancel),
		binding(KeyContextStatusDetails, "", "j", KeyActionMoveDown),
		binding(KeyContextStatusDetails, "", "k", KeyActionMoveUp),
		binding(KeyContextStatusDetails, "", "pgdown", KeyActionPageDown),
		binding(KeyContextStatusDetails, "", "pgup", KeyActionPageUp),
		binding(KeyContextConfirm, "", "Esc", KeyActionCancel),
		binding(KeyContextConfirm, "", "y", "confirm-yes"),
		binding(KeyContextConfirm, "", "n", "confirm-no"),
		binding(KeyContextExportPicker, "", "Esc", KeyActionCancel),
		binding(KeyContextExportPicker, "", "u", KeyActionToggleExportUnmap),
		binding(KeyContextExportPicker, "", "Enter", "export-selected"),
		binding(KeyContextSubmit, "", "Esc", KeyActionCancel),
		binding(KeyContextSubmit, "", "j", KeyActionMoveDown),
		binding(KeyContextSubmit, "", "k", KeyActionMoveUp),
		binding(KeyContextSubmit, "", "pgdown", KeyActionPageDown),
		binding(KeyContextSubmit, "", "pgup", KeyActionPageUp),
		binding(KeyContextSubmit, "", "x", KeyActionToggleSubmitExclude),
		binding(KeyContextSubmit, "", "m", KeyActionToggleSubmitSummary),
		binding(KeyContextSubmit, "", "S", KeyActionToggleStaleOverride),
		binding(KeyContextSubmit, "", "r", KeyActionSubmitRecheck),
		binding(KeyContextSubmit, "", "Enter", "submit-review"),
		binding(KeyContextMinimumSize, "", "q", KeyActionQuit),
		binding(KeyContextMinimumSize, "", "Ctrl-c", KeyActionInterrupt),
	}
}

func binding(context KeyContext, focus KeyFocus, chord KeyChord, action KeyAction) Keybinding {
	return Keybinding{Context: context, Focus: focus, Chord: chord, Action: action}
}

func actionAllowed(context KeyContext, action KeyAction) bool {
	allowed, exists := contextActions[context]
	return exists && allowed[action]
}

var keyFocuses = stringSet[KeyFocus]("file-tree", "diff", "comments")

var keyActions = stringSet[KeyAction](
	"quit", "focus-next-pane", "focus-previous-pane", "move-up", "move-down",
	"scroll-left", "scroll-right", "first", "last", "page-up", "page-down",
	"half-page-up", "half-page-down", "next-file", "previous-file", "next-hunk",
	"previous-hunk", "next-comment", "previous-comment", "next-search-hit",
	"previous-search-hit", "comment-at-cursor", "file-comment", "summary-comment",
	"edit-comment", "delete-comment", "expand-context", "toggle-file-reviewed",
	"toggle-hunk-reviewed", "activate", "accept", "toggle-wrap", "toggle-view",
	"align-top", "align-center", "align-bottom", "toggle-directory", "expand-directory",
	"collapse-directory", "visual-line", "visual-lines", "visual-hunk",
	"comment-selection", "command", "filter", "help", "status-details", "search",
	"open-submit", "cancel", "interrupt", "delete-backward", "confirm-yes", "confirm-no",
	"save-comment", "insert-newline",
	"extend-up", "extend-down", "next-help-page", "previous-help-page", "help-line-up",
	"help-line-down", "help-page-up", "help-page-down", "help-home", "help-end",
	"toggle-export-unmappable",
	"export-selected", "copy-markdown", "export-markdown", "export-json",
	"submit-recheck", "toggle-stale-head-override", "toggle-submit-exclusion",
	"toggle-submit-summary", "submit-review", "reload", "toggle-file-panel",
	"toggle-comments", "toggle-hidden-files",
)

var contextActions = map[KeyContext]map[KeyAction]bool{
	"normal": stringSet[KeyAction](
		"quit", "interrupt", "focus-next-pane", "focus-previous-pane",
		"move-up", "move-down", "scroll-left", "scroll-right", "first", "last",
		"page-up", "page-down", "half-page-up", "half-page-down",
		"next-file", "previous-file", "next-hunk", "previous-hunk",
		"next-comment", "previous-comment", "next-search-hit", "previous-search-hit",
		"comment-at-cursor", "file-comment", "summary-comment", "edit-comment",
		"delete-comment", "expand-context", "toggle-file-reviewed", "toggle-hunk-reviewed",
		"activate", "accept", "toggle-wrap", "toggle-view", "align-top", "align-center",
		"align-bottom", "toggle-directory", "expand-directory", "collapse-directory",
		"visual-line", "visual-lines", "visual-hunk", "command", "filter", "help",
		"status-details", "search", "open-submit", "reload", "copy-markdown",
		"export-markdown", "export-json", "toggle-file-panel", "toggle-comments",
		"toggle-hidden-files",
	),
	"visual": stringSet[KeyAction](
		"cancel", "interrupt", "move-up", "move-down", "page-up", "page-down",
		"extend-up", "extend-down", "comment-selection",
	),
	"comment-edit": stringSet[KeyAction](
		"cancel", "interrupt", "delete-backward", "save-comment", "insert-newline",
	),
	"search": stringSet[KeyAction](
		"cancel", "interrupt", "accept", "delete-backward",
	),
	"command": stringSet[KeyAction](
		"cancel", "interrupt", "accept", "delete-backward",
	),
	"help": stringSet[KeyAction](
		"cancel", "interrupt", "next-help-page", "previous-help-page", "help-line-up",
		"help-line-down", "help-page-up", "help-page-down", "help-home", "help-end",
	),
	"status-details": stringSet[KeyAction](
		"cancel", "interrupt", "move-up", "move-down", "page-up", "page-down",
	),
	"confirm": stringSet[KeyAction](
		"cancel", "interrupt", "confirm-yes", "confirm-no",
	),
	"export-picker": stringSet[KeyAction](
		"cancel", "interrupt", "toggle-export-unmappable", "export-selected",
	),
	"submit-preflight": stringSet[KeyAction](
		"cancel", "interrupt", "submit-recheck", "toggle-stale-head-override",
		"toggle-submit-exclusion", "toggle-submit-summary", "submit-review",
		"move-up", "move-down", "page-up", "page-down",
	),
	"minimum-size": stringSet[KeyAction]("quit", "interrupt"),
}

func stringSet[T ~string](values ...T) map[T]bool {
	result := make(map[T]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
