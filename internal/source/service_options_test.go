package source

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceOptionsValidateAndOwnInputs(t *testing.T) {
	token, err := NewGitHubToken("secret")
	if err != nil {
		t.Fatal(err)
	}
	home, err := NewGitConfigPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	global, err := NewGitConfigPath(filepath.Join(t.TempDir(), "config"))
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := NewWorkingDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	globals := []GitConfigPath{global}
	service, err := NewService(workingDirectory, ServiceOptions{
		GitHubToken:      &token,
		GitConfigHome:    &home,
		GlobalGitConfigs: globals,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestService(t, service)

	token.value[0] = 'X'
	globals[0] = GitConfigPath{}
	if !bytes.Equal(service.options.GitHubToken.value, []byte("secret")) ||
		service.options.GlobalGitConfigs[0].value != global.value {
		t.Fatal("service options alias caller-owned inputs")
	}
}

func TestServiceOptionsRejectInvalidValuesAndCardinality(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("x", maxGitHubTokenBytes+1), "a\x00b"} {
		if _, err := NewGitHubToken(value); err == nil {
			t.Fatalf("NewGitHubToken(%q) succeeded", value)
		}
	}
	if _, err := NewGitConfigPath("relative"); err == nil {
		t.Fatal("relative Git config path succeeded")
	}
	path, err := NewGitConfigPath(filepath.Join(t.TempDir(), "config"))
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := NewWorkingDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewService(workingDirectory, ServiceOptions{
		GlobalGitConfigs: []GitConfigPath{path, path, path},
	})
	if err == nil {
		t.Fatal("three global Git configs succeeded")
	}
}
