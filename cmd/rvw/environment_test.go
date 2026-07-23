package main

import (
	"path/filepath"
	"testing"
)

func TestSourceServiceOptionsEnvironmentSelection(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	options, err := sourceServiceOptions(root, []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "xdg"),
		"GH_TOKEN=ignored",
		"GITHUB_TOKEN=first",
		"GITHUB_TOKEN=selected",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.GitHubToken == nil || options.GitConfigHome == nil ||
		len(options.GlobalGitConfigs) != 2 {
		t.Fatalf("source options = %+v", options)
	}

	disabled, err := sourceServiceOptions(root, []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled.GlobalGitConfigs) != 0 {
		t.Fatalf("/dev/null global configs = %d", len(disabled.GlobalGitConfigs))
	}

	selected, err := sourceServiceOptions(root, []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=~/selected.gitconfig",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.GlobalGitConfigs) != 1 {
		t.Fatalf("selected global configs = %d", len(selected.GlobalGitConfigs))
	}
}

func TestSourceServiceOptionsRejectsInvalidExplicitEnvironment(t *testing.T) {
	root := t.TempDir()
	for _, environment := range [][]string{
		{"GITHUB_TOKEN="},
		{"HOME=relative"},
		{"GIT_CONFIG_GLOBAL="},
		{"GIT_CONFIG_GLOBAL=~/config"},
		{"HOME=" + root, "GIT_CONFIG_GLOBAL=~other/config"},
	} {
		if _, err := sourceServiceOptions(root, environment); err == nil {
			t.Fatalf("sourceServiceOptions(%q) succeeded", environment)
		}
	}
}
