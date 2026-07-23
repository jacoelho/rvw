package source

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxGitHubTokenBytes = 4 << 10
	maxGlobalGitConfigs = 2
)

// GitHubToken is an owned GitHub credential. It intentionally has no string
// conversion so it cannot be included accidentally in diagnostics.
type GitHubToken struct {
	value []byte
}

// NewGitHubToken validates and copies a GitHub credential.
func NewGitHubToken(value string) (GitHubToken, error) {
	if value == "" || len(value) > maxGitHubTokenBytes {
		return GitHubToken{}, errors.New("GitHub token must contain from 1 through 4096 bytes")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return GitHubToken{}, errors.New("GitHub token must contain no NUL")
	}
	return GitHubToken{value: bytes.Clone([]byte(value))}, nil
}

func (token GitHubToken) clone() (GitHubToken, error) {
	if len(token.value) == 0 || len(token.value) > maxGitHubTokenBytes ||
		bytes.IndexByte(token.value, 0) >= 0 {
		return GitHubToken{}, errors.New("source service received an invalid GitHub token")
	}
	return GitHubToken{value: bytes.Clone(token.value)}, nil
}

// GitConfigPath is an owned absolute path used by Git configuration loading.
type GitConfigPath struct {
	value string
}

// NewGitConfigPath validates and copies an absolute Git configuration path.
func NewGitConfigPath(value string) (GitConfigPath, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 || !filepath.IsAbs(value) {
		return GitConfigPath{}, errors.New("git configuration path must be absolute and contain no NUL")
	}
	return GitConfigPath{value: strings.Clone(filepath.Clean(value))}, nil
}

func (path GitConfigPath) clone() (GitConfigPath, error) {
	return NewGitConfigPath(path.value)
}

// ServiceOptions contains the complete process-derived configuration admitted
// into source. The service never reads ambient credentials or environment.
type ServiceOptions struct {
	GitHubToken      *GitHubToken
	GitConfigHome    *GitConfigPath
	GlobalGitConfigs []GitConfigPath
}

func (options ServiceOptions) clone() (ServiceOptions, error) {
	if len(options.GlobalGitConfigs) > maxGlobalGitConfigs {
		return ServiceOptions{}, errors.New("source service accepts at most two global Git configuration files")
	}
	cloned := ServiceOptions{
		GlobalGitConfigs: make([]GitConfigPath, len(options.GlobalGitConfigs)),
	}
	if options.GitHubToken != nil {
		token, err := options.GitHubToken.clone()
		if err != nil {
			return ServiceOptions{}, err
		}
		cloned.GitHubToken = new(token)
	}
	if options.GitConfigHome != nil {
		home, err := options.GitConfigHome.clone()
		if err != nil {
			return ServiceOptions{}, err
		}
		cloned.GitConfigHome = new(home)
	}
	for index, path := range options.GlobalGitConfigs {
		validated, err := path.clone()
		if err != nil {
			return ServiceOptions{}, err
		}
		cloned.GlobalGitConfigs[index] = validated
	}
	cloned.GlobalGitConfigs = slices.Clip(cloned.GlobalGitConfigs)
	return cloned, nil
}
