package main

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jacoelho/rvw/internal/source"
)

func sourceServiceOptions(cwd string, environment []string) (source.ServiceOptions, error) {
	var options source.ServiceOptions
	if value, exists := environmentValue(environment, "GITHUB_TOKEN"); exists {
		token, err := source.NewGitHubToken(value)
		if err != nil {
			return source.ServiceOptions{}, err
		}
		options.GitHubToken = new(token)
	}

	var home string
	if value, exists := environmentValue(environment, "HOME"); exists && value != "" {
		if !filepath.IsAbs(value) {
			return source.ServiceOptions{}, errors.New("HOME must be absolute for Git configuration")
		}
		home = filepath.Clean(value)
		path, err := source.NewGitConfigPath(home)
		if err != nil {
			return source.ServiceOptions{}, err
		}
		options.GitConfigHome = new(path)
	}

	if value, exists := environmentValue(environment, "GIT_CONFIG_GLOBAL"); exists {
		if value == "/dev/null" {
			return options, nil
		}
		path, err := resolveGitConfigPath(cwd, home, value)
		if err != nil {
			return source.ServiceOptions{}, err
		}
		options.GlobalGitConfigs = []source.GitConfigPath{path}
		return options, nil
	}

	if value, exists := environmentValue(environment, "XDG_CONFIG_HOME"); exists && value != "" && filepath.IsAbs(value) {
		path, err := source.NewGitConfigPath(filepath.Join(value, "git", "config"))
		if err != nil {
			return source.ServiceOptions{}, err
		}
		options.GlobalGitConfigs = append(options.GlobalGitConfigs, path)
	} else if home != "" {
		path, err := source.NewGitConfigPath(filepath.Join(home, ".config", "git", "config"))
		if err != nil {
			return source.ServiceOptions{}, err
		}
		options.GlobalGitConfigs = append(options.GlobalGitConfigs, path)
	}
	if home != "" {
		path, err := source.NewGitConfigPath(filepath.Join(home, ".gitconfig"))
		if err != nil {
			return source.ServiceOptions{}, err
		}
		options.GlobalGitConfigs = append(options.GlobalGitConfigs, path)
	}
	return options, nil
}

func resolveGitConfigPath(cwd, home, value string) (source.GitConfigPath, error) {
	if value == "" {
		return source.GitConfigPath{}, errors.New("GIT_CONFIG_GLOBAL must not be empty")
	}
	switch {
	case strings.HasPrefix(value, "~/"):
		if home == "" {
			return source.GitConfigPath{}, errors.New("GIT_CONFIG_GLOBAL uses ~/ but HOME is unavailable")
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	case strings.HasPrefix(value, "~"):
		return source.GitConfigPath{}, errors.New("GIT_CONFIG_GLOBAL does not support ~user expansion")
	case !filepath.IsAbs(value):
		if !filepath.IsAbs(cwd) {
			return source.GitConfigPath{}, errors.New("working directory must be absolute for relative GIT_CONFIG_GLOBAL")
		}
		value = filepath.Join(cwd, value)
	}
	return source.NewGitConfigPath(value)
}

func environmentValue(environment []string, name string) (string, bool) {
	prefix := name + "="
	for _, entry := range slices.Backward(environment) {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return value, true
		}
	}
	return "", false
}
