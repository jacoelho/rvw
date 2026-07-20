package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

type LoadRequest struct {
	CWD         string
	ProjectRoot string
	Environment []string
	Overrides   Overrides
}

type rawConfig struct {
	Mouse      *bool   `toml:"mouse"`
	Theme      *string `toml:"theme"`
	ThemeDark  *string `toml:"theme_dark"`
	ThemeLight *string `toml:"theme_light"`
	Appearance *string `toml:"appearance"`

	UI       *rawUI       `toml:"ui"`
	Diff     *rawDiff     `toml:"diff"`
	Filter   *rawFilter   `toml:"filter"`
	Comments *rawComments `toml:"comments"`
	GitHub   *rawGitHub   `toml:"github"`
	Export   *rawExport   `toml:"export"`
}

type rawUI struct {
	Mouse       *bool           `toml:"mouse"`
	Theme       *string         `toml:"theme"`
	ThemeDark   *string         `toml:"theme_dark"`
	ThemeLight  *string         `toml:"theme_light"`
	Appearance  *string         `toml:"appearance"`
	Keybindings []rawKeybinding `toml:"keybindings"`
}

type rawKeybinding struct {
	Context string `toml:"context"`
	Focus   string `toml:"focus"`
	Chord   string `toml:"chord"`
	Action  string `toml:"action"`
	Unbind  bool   `toml:"unbind"`
}

type rawDiff struct {
	Wrap             *bool   `toml:"wrap"`
	View             *string `toml:"view"`
	ContextLines     *int    `toml:"context_lines"`
	IncludeUntracked *bool   `toml:"include_untracked"`
}

type rawFilter struct {
	BuiltinDefaults *bool   `toml:"builtin_defaults"`
	Gitignore       *bool   `toml:"gitignore"`
	UseGitignore    *bool   `toml:"use_gitignore"`
	RVWIgnore       *bool   `toml:"rvwignore"`
	IgnoreFile      *string `toml:"ignore_file"`
	Include         any     `toml:"include"`
	Exclude         any     `toml:"exclude"`
}

type rawComments struct {
	Kinds               any   `toml:"kinds"`
	TypePrefixForGitHub *bool `toml:"type_prefix_for_github"`
}

type rawGitHub struct {
	SubmitDefault *string `toml:"submit_default"`
	FetchThreads  *bool   `toml:"fetch_threads"`
}

type rawExport struct {
	MarkdownTemplate *string `toml:"markdown_template"`
	CommentTemplate  *string `toml:"comment_template"`
}

type decodedLayer struct {
	raw      rawConfig
	metadata toml.MetaData
	path     string
	project  bool
}

func Load(request LoadRequest) (Config, error) {
	return load(request, loadProjectConfiguration)
}

func LoadGlobal(request LoadRequest) (Config, error) {
	return load(request, loadGlobalConfiguration)
}

type loadScope uint8

const (
	loadGlobalConfiguration loadScope = iota
	loadProjectConfiguration
)

func load(request LoadRequest, scope loadScope) (Config, error) {
	if request.CWD == "" {
		return Config{}, errors.New("configuration working directory is empty")
	}
	absoluteCWD, err := filepath.Abs(request.CWD)
	if err != nil {
		return Config{}, fmt.Errorf("resolve working directory: %w", err)
	}

	environment := environmentMap(request.Environment)
	projectRoot := request.ProjectRoot
	if projectRoot == "" {
		projectRoot = absoluteCWD
	}
	paths, err := configurationPaths(projectRoot, environment)
	if err != nil {
		return Config{}, err
	}
	config := Defaults()
	projectLoaded := false
	for _, candidate := range paths {
		if candidate.project && scope != loadProjectConfiguration {
			continue
		}
		if candidate.project && projectLoaded {
			continue
		}
		layer, exists, err := decodeFile(candidate)
		if err != nil {
			return Config{}, err
		}
		if !exists {
			continue
		}
		projectLoaded = candidate.project
		if err := applyLayer(&config, layer); err != nil {
			return Config{}, err
		}
	}
	applyOverrides(&config, request.Overrides)
	if err := validate(&config, environment); err != nil {
		return Config{}, err
	}
	if err := resolveThemes(&config, environment); err != nil {
		return Config{}, err
	}
	return config, nil
}

type configPath struct {
	path     string
	root     string
	relative string
	project  bool
}

func configurationPaths(projectRoot string, environment map[string]string) ([]configPath, error) {
	var paths []configPath
	if xdg := environment["XDG_CONFIG_HOME"]; xdg != "" {
		paths = append(paths, configPath{path: filepath.Join(xdg, "rvw", "config.toml")})
	} else if home := environment["HOME"]; home != "" {
		paths = append(paths, configPath{path: filepath.Join(home, ".config", "rvw", "config.toml")})
	}

	for _, name := range []string{".rvw.toml", "rvw.toml"} {
		paths = append(paths, configPath{
			path:     filepath.Join(projectRoot, name),
			root:     projectRoot,
			relative: name,
			project:  true,
		})
	}
	return paths, nil
}

func decodeFile(candidate configPath) (decodedLayer, bool, error) {
	var (
		file *os.File
		err  error
	)
	if candidate.project {
		root, openErr := os.OpenRoot(candidate.root)
		if openErr != nil {
			return decodedLayer{}, false, fmt.Errorf(
				"open project root %s: %w",
				candidate.root,
				openErr,
			)
		}
		defer root.Close()
		file, err = root.OpenFile(candidate.relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	} else {
		file, err = os.OpenFile(candidate.path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	}
	if errors.Is(err, os.ErrNotExist) {
		return decodedLayer{}, false, nil
	}
	if err != nil {
		return decodedLayer{}, false, fmt.Errorf("read configuration %s: %w", candidate.path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return decodedLayer{}, false, fmt.Errorf("inspect configuration %s: %w", candidate.path, err)
	}
	if !info.Mode().IsRegular() {
		return decodedLayer{}, false, fmt.Errorf("configuration %s is not a regular file", candidate.path)
	}

	contents, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return decodedLayer{}, false, fmt.Errorf("read configuration %s: %w", candidate.path, err)
	}
	if int64(len(contents)) > MaxConfigBytes {
		return decodedLayer{}, false, fmt.Errorf(
			"configuration %s exceeds %d bytes",
			candidate.path,
			MaxConfigBytes,
		)
	}
	if !utf8.Valid(contents) {
		return decodedLayer{}, false, fmt.Errorf("configuration %s is not valid UTF-8", candidate.path)
	}

	var raw rawConfig
	metadata, err := toml.NewDecoder(bytes.NewReader(contents)).Decode(&raw)
	if err != nil {
		return decodedLayer{}, false, fmt.Errorf("decode configuration %s: %w", candidate.path, err)
	}
	if unknown := metadata.Undecoded(); len(unknown) != 0 {
		names := make([]string, len(unknown))
		for index, key := range unknown {
			names[index] = key.String()
		}
		slices.Sort(names)
		return decodedLayer{}, false, fmt.Errorf(
			"configuration %s contains unknown field %s",
			candidate.path,
			strings.Join(names, ", "),
		)
	}
	return decodedLayer{
		raw: raw, metadata: metadata, path: candidate.path, project: candidate.project,
	}, true, nil
}

func applyLayer(config *Config, layer decodedLayer) error {
	raw := layer.raw
	mouse := raw.Mouse
	theme := raw.Theme
	themeDark := raw.ThemeDark
	themeLight := raw.ThemeLight
	appearance := raw.Appearance
	if raw.UI != nil {
		mouse = prefer(raw.UI.Mouse, mouse)
		theme = prefer(raw.UI.Theme, theme)
		themeDark = prefer(raw.UI.ThemeDark, themeDark)
		themeLight = prefer(raw.UI.ThemeLight, themeLight)
		appearance = prefer(raw.UI.Appearance, appearance)
		if layer.project && layer.metadata.IsDefined("ui", "keybindings") {
			return fmt.Errorf("configuration %s: project keybindings are forbidden", layer.path)
		}
		if layer.metadata.IsDefined("ui", "keybindings") {
			config.ui.Keybindings = make([]Keybinding, len(raw.UI.Keybindings))
			for index, binding := range raw.UI.Keybindings {
				config.ui.Keybindings[index] = Keybinding{
					Context: KeyContext(binding.Context),
					Focus:   KeyFocus(binding.Focus),
					Chord:   KeyChord(binding.Chord),
					Action:  KeyAction(binding.Action),
					Unbind:  binding.Unbind,
				}
			}
		}
	}
	assign(&config.ui.Mouse, mouse)
	if theme != nil {
		config.ui.Theme = ThemeName(*theme)
		config.ui.ExplicitTheme = true
	}
	if themeDark != nil {
		config.ui.ThemeDark = ThemeName(*themeDark)
		config.ui.ExplicitTheme = true
	}
	if themeLight != nil {
		config.ui.ThemeLight = ThemeName(*themeLight)
		config.ui.ExplicitTheme = true
	}
	if appearance != nil {
		config.ui.Appearance = Appearance(*appearance)
	}

	if raw.Diff != nil {
		assign(&config.diff.Wrap, raw.Diff.Wrap)
		if raw.Diff.View != nil {
			config.diff.View = DiffView(*raw.Diff.View)
		}
		assign(&config.diff.ContextLines, raw.Diff.ContextLines)
		assign(&config.diff.IncludeUntracked, raw.Diff.IncludeUntracked)
	}

	if raw.Filter != nil {
		assign(&config.filter.BuiltinDefaults, raw.Filter.BuiltinDefaults)
		assign(&config.filter.Gitignore, prefer(raw.Filter.Gitignore, raw.Filter.UseGitignore))
		assign(&config.filter.RVWIgnore, raw.Filter.RVWIgnore)
		if raw.Filter.IgnoreFile != nil {
			config.filter.IgnoreFile = IgnoreFileName(*raw.Filter.IgnoreFile)
		}
		if layer.metadata.IsDefined("filter", "include") {
			values, err := stringList(raw.Filter.Include, "filter.include")
			if err != nil {
				return fmt.Errorf("configuration %s: %w", layer.path, err)
			}
			config.filter.Include = patterns(values)
		}
		if layer.metadata.IsDefined("filter", "exclude") {
			values, err := stringList(raw.Filter.Exclude, "filter.exclude")
			if err != nil {
				return fmt.Errorf("configuration %s: %w", layer.path, err)
			}
			config.filter.Exclude = patterns(values)
		}
	}

	if raw.Comments != nil {
		if layer.metadata.IsDefined("comments", "kinds") {
			values, err := stringList(raw.Comments.Kinds, "comments.kinds")
			if err != nil {
				return fmt.Errorf("configuration %s: %w", layer.path, err)
			}
			config.comments.Kinds = commentKinds(values)
		}
		assign(&config.comments.TypePrefixForGitHub, raw.Comments.TypePrefixForGitHub)
	}
	if raw.GitHub != nil {
		if raw.GitHub.SubmitDefault != nil {
			config.github.SubmitDefault = SubmitEvent(*raw.GitHub.SubmitDefault)
		}
		assign(&config.github.FetchThreads, raw.GitHub.FetchThreads)
	}
	if raw.Export != nil {
		if raw.Export.MarkdownTemplate != nil {
			config.export.MarkdownTemplate = Template(*raw.Export.MarkdownTemplate)
		}
		if raw.Export.CommentTemplate != nil {
			config.export.CommentTemplate = Template(*raw.Export.CommentTemplate)
		}
	}
	return nil
}

func applyOverrides(config *Config, overrides Overrides) {
	assign(&config.ui.Mouse, overrides.mouse)
	if overrides.theme != nil {
		config.ui.Theme = *overrides.theme
		config.ui.ExplicitTheme = true
	}
	if overrides.appearance != nil {
		config.ui.Appearance = *overrides.appearance
	}
	config.filter.Include = append(config.filter.Include, overrides.include...)
	config.filter.Exclude = append(config.filter.Exclude, overrides.exclude...)
}

func environmentMap(environment []string) map[string]string {
	values := make(map[string]string)
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func stringList(value any, field string) ([]string, error) {
	switch value := value.(type) {
	case string:
		return []string{value}, nil
	case []map[string]any:
		return nil, fmt.Errorf("%s must be a string or array of strings", field)
	case []any:
		values := make([]string, len(value))
		for index, element := range value {
			text, ok := element.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be a string", field, index)
			}
			values[index] = text
		}
		return values, nil
	case nil:
		return nil, fmt.Errorf("%s must be a string or array of strings", field)
	default:
		return nil, fmt.Errorf("%s must be a string or array of strings", field)
	}
}

func prefer[T any](preferred, fallback *T) *T {
	if preferred != nil {
		return preferred
	}
	return fallback
}

func assign[T any](destination *T, value *T) {
	if value != nil {
		*destination = *value
	}
}

func patterns(values []string) []Pattern {
	result := make([]Pattern, len(values))
	for index, value := range values {
		result[index] = Pattern(value)
	}
	return result
}

func commentKinds(values []string) []CommentKind {
	result := make([]CommentKind, len(values))
	for index, value := range values {
		result[index] = CommentKind(value)
	}
	return result
}
