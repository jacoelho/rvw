package source

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/go-git/gcfg"
)

const (
	maxConfigFiles        = 64
	maxConfigAggregate    = 8 << 20
	maxConfigEntries      = 100_000
	maxConfigIncludeDepth = 16
	gitBooleanTrue        = "true"
	gitBooleanFalse       = "false"
)

type gitConfigEntry struct {
	section    string
	subsection string
	key        string
	value      string
	blank      bool
}

type gitConfiguration struct {
	entries []gitConfigEntry
}

func (configuration *gitConfiguration) value(section, subsection, key string) (string, bool) {
	for _, entry := range slices.Backward(configuration.entries) {
		if strings.EqualFold(entry.section, section) && entry.subsection == subsection &&
			strings.EqualFold(entry.key, key) {
			if entry.blank {
				return gitBooleanTrue, true
			}
			return entry.value, true
		}
	}
	return "", false
}

func (configuration *gitConfiguration) values(section, subsection, key string) []string {
	values := make([]string, 0, 2)
	for _, entry := range configuration.entries {
		if strings.EqualFold(entry.section, section) && entry.subsection == subsection &&
			strings.EqualFold(entry.key, key) {
			if entry.blank {
				values = append(values, gitBooleanTrue)
			} else {
				values = append(values, entry.value)
			}
		}
	}
	return values
}

func (configuration *gitConfiguration) subsections(section string) []string {
	seen := make(map[string]struct{})
	values := make([]string, 0)
	for _, entry := range configuration.entries {
		if !strings.EqualFold(entry.section, section) || entry.subsection == "" {
			continue
		}
		if _, exists := seen[entry.subsection]; exists {
			continue
		}
		seen[entry.subsection] = struct{}{}
		values = append(values, entry.subsection)
	}
	return values
}

type configLoader struct {
	configuration gitConfiguration
	options       ServiceOptions
	gitdir        string
	branch        string
	active        []os.FileInfo
	files         uint64
	bytes         uint64
	entries       uint64
}

func loadGitConfiguration(
	gitdir, common string,
	options ServiceOptions,
) (*gitConfiguration, error) {
	loader := &configLoader{
		options: options,
		gitdir:  filepath.Clean(gitdir),
		branch:  readCurrentBranch(gitdir),
	}
	for _, path := range options.GlobalGitConfigs {
		if path.value == "/dev/null" {
			continue
		}
		if err := loader.load(path.value, 0); err != nil {
			return nil, fmt.Errorf("read global Git config %q: %w", path.value, err)
		}
	}
	if err := loader.load(filepath.Join(common, "config"), 0); err != nil {
		return nil, fmt.Errorf("read repository Git config: %w", err)
	}
	if enabled, ok := loader.configuration.value("extensions", "", "worktreeconfig"); ok {
		value, err := parseGitBoolean(enabled)
		if err != nil {
			return nil, fmt.Errorf("extensions.worktreeConfig: %w", err)
		}
		if value {
			if err := loader.load(filepath.Join(gitdir, "config.worktree"), 0); err != nil {
				return nil, fmt.Errorf("read worktree Git config: %w", err)
			}
		}
	}
	format, _ := loader.configuration.value("extensions", "", "objectformat")
	format = strings.ToLower(strings.TrimSpace(format))
	if format != "" && format != "sha1" {
		return nil, fmt.Errorf("unsupported Git object format %q; rvw supports SHA-1 only", format)
	}
	return &loader.configuration, nil
}

func (loader *configLoader) load(path string, depth int) error {
	if depth > maxConfigIncludeDepth {
		return &LimitError{Resource: LimitConfigIncludeDepth, Limit: maxConfigIncludeDepth}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("git config must be a no-follow regular file")
	}
	for _, active := range loader.active {
		if os.SameFile(active, info) {
			return errors.New("git config include cycle")
		}
	}
	loader.files++
	if loader.files > maxConfigFiles {
		return &LimitError{Resource: LimitConfigFiles, Limit: maxConfigFiles}
	}
	remaining := int64(maxConfigAggregate - loader.bytes)
	contents, err := readStableRegularFile(path, remaining)
	if err != nil {
		return err
	}
	loader.bytes += uint64(len(contents))
	if loader.bytes > maxConfigAggregate {
		return &LimitError{Resource: LimitConfigBytes, Limit: maxConfigAggregate}
	}
	loader.active = append(loader.active, info)
	defer func() { loader.active = loader.active[:len(loader.active)-1] }()
	directory := filepath.Dir(path)
	err = gcfg.ReadWithCallback(bytes.NewReader(contents), func(
		section, subsection, key, value string,
		blank bool,
	) error {
		if key == "" {
			return nil
		}
		loader.entries++
		if loader.entries > maxConfigEntries {
			return &LimitError{Resource: LimitConfigEntries, Limit: maxConfigEntries}
		}
		if strings.EqualFold(section, "include") && subsection == "" &&
			strings.EqualFold(key, "path") {
			included, includeErr := loader.includePath(directory, value)
			if includeErr != nil {
				return includeErr
			}
			return loader.load(included, depth+1)
		}
		if strings.EqualFold(section, "includeif") && strings.EqualFold(key, "path") {
			match, conditionErr := loader.includeCondition(subsection, directory)
			if conditionErr != nil {
				return conditionErr
			}
			if match {
				included, includeErr := loader.includePath(directory, value)
				if includeErr != nil {
					return includeErr
				}
				return loader.load(included, depth+1)
			}
			return nil
		}
		loader.configuration.entries = append(loader.configuration.entries, gitConfigEntry{
			section: section, subsection: subsection, key: key, value: value, blank: blank,
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("parse %q: %w", path, err)
	}
	return nil
}

func (loader *configLoader) includePath(directory, value string) (string, error) {
	if strings.IndexByte(value, 0) >= 0 || value == "" {
		return "", errors.New("git config include path is invalid")
	}
	if strings.HasPrefix(value, "~") {
		if value != "~" && !strings.HasPrefix(value, "~/") {
			return "", errors.New("git config ~user includes are unsupported")
		}
		if loader.options.GitConfigHome == nil {
			return "", errors.New("git config home is required for ~/ expansion")
		}
		return filepath.Join(loader.options.GitConfigHome.value, strings.TrimPrefix(value, "~/")), nil
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	return filepath.Clean(filepath.Join(directory, value)), nil
}

func (loader *configLoader) includeCondition(condition, directory string) (bool, error) {
	fold := false
	var pattern string
	switch {
	case strings.HasPrefix(condition, "gitdir/i:"):
		fold = true
		pattern = strings.TrimPrefix(condition, "gitdir/i:")
	case strings.HasPrefix(condition, "gitdir:"):
		pattern = strings.TrimPrefix(condition, "gitdir:")
	case strings.HasPrefix(condition, "onbranch:"):
		pattern = strings.TrimPrefix(condition, "onbranch:")
		if strings.HasSuffix(pattern, "/") {
			pattern += "**"
		}
		return matchGitConfigGlob(pattern, loader.branch, false)
	case strings.HasPrefix(condition, "hasconfig:"):
		return false, errors.New("git config hasconfig include conditions are unsupported")
	default:
		return false, fmt.Errorf("unsupported Git config include condition %q", condition)
	}
	if pattern == "" {
		return false, errors.New("git config gitdir condition is empty")
	}
	switch {
	case strings.HasPrefix(pattern, "~/"):
		if loader.options.GitConfigHome == nil {
			return false, errors.New("git config home is required for gitdir ~/ matching")
		}
		pattern = filepath.Join(loader.options.GitConfigHome.value, strings.TrimPrefix(pattern, "~/"))
	case strings.HasPrefix(pattern, "./"):
		relative := strings.TrimPrefix(pattern, "./")
		pattern = filepath.Join(directory, relative)
	case !filepath.IsAbs(pattern):
		pattern = "**/" + pattern
	}
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	return matchGitConfigGlob(filepath.ToSlash(pattern), filepath.ToSlash(loader.gitdir)+"/", fold)
}

func matchGitConfigGlob(pattern, value string, fold bool) (bool, error) {
	expression, err := gitConfigGlobExpression(pattern)
	if err != nil {
		return false, err
	}
	if fold {
		expression = "(?i)" + expression
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return false, err
	}
	return compiled.MatchString(value), nil
}

func gitConfigGlobExpression(pattern string) (string, error) {
	var builder strings.Builder
	builder.WriteByte('^')
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				builder.WriteString(".*")
				index++
			} else {
				builder.WriteString("[^/]*")
			}
		case '?':
			builder.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[index+1:], ']')
			if end < 0 {
				return "", errors.New("git config glob has an unterminated character class")
			}
			end += index + 1
			builder.WriteString(pattern[index : end+1])
			index = end
		default:
			builder.WriteString(regexp.QuoteMeta(string(pattern[index])))
		}
	}
	builder.WriteByte('$')
	return builder.String(), nil
}

func readCurrentBranch(gitdir string) string {
	contents, err := readStableRegularFile(filepath.Join(gitdir, "HEAD"), 64<<10)
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(contents))
	const prefix = "ref: refs/heads/"
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	return strings.TrimPrefix(value, prefix)
}

func parseGitBoolean(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case gitBooleanTrue, "yes", "on", "1":
		return true, nil
	case gitBooleanFalse, "no", "off", "0", "":
		return false, nil
	default:
		return false, fmt.Errorf("invalid Git Boolean %q", value)
	}
}
