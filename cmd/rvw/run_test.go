package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunLocalSourceOutputPairs(t *testing.T) {
	root := testRepository(t)
	writeRunFile(t, filepath.Join(root, "file.txt"), "first\n")
	runTestGit(t, root, "add", "file.txt")
	runTestGit(t, root, "commit", "-m", "first")
	first := strings.TrimSpace(runTestGit(t, root, "rev-parse", "HEAD"))

	writeRunFile(t, filepath.Join(root, "file.txt"), "first\nsecond\n")
	runTestGit(t, root, "add", "file.txt")
	runTestGit(t, root, "commit", "-m", "second")
	second := strings.TrimSpace(runTestGit(t, root, "rev-parse", "HEAD"))

	writeRunFile(t, filepath.Join(root, "staged.txt"), "staged\n")
	runTestGit(t, root, "add", "staged.txt")
	writeRunFile(t, filepath.Join(root, "worktree.txt"), "worktree\n")

	tests := []struct {
		name string
		args []string
	}{
		{name: "worktree human", args: []string{"diff", "--stdout"}},
		{name: "worktree JSON", args: []string{"diff", "--json"}},
		{name: "staged human", args: []string{"diff", "--staged", "--stdout"}},
		{name: "staged JSON", args: []string{"diff", "--staged", "--json"}},
		{name: "range human", args: []string{"diff", "--range", first + ".." + second, "--stdout"}},
		{name: "range JSON", args: []string{"diff", "--range", first + ".." + second, "--json"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(runRequest{
				ctx:         t.Context(),
				args:        test.args,
				environment: []string{"HOME=" + filepath.Join(root, "home")},
				cwd:         root,
				stdin:       strings.NewReader(""),
				stdout:      &stdout,
				stderr:      &stderr,
			})
			if status != 0 {
				t.Fatalf("run() status = %d, stderr = %s", status, stderr.String())
			}
			if strings.HasSuffix(test.name, "JSON") {
				var value struct {
					Source    string `json:"source"`
					FileCount int    `json:"file_count"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
					t.Fatalf("decode output: %v\n%s", err, stdout.String())
				}
				if value.Source != "diff" || value.FileCount == 0 {
					t.Fatalf("unexpected JSON: %+v", value)
				}
			} else if !strings.Contains(stdout.String(), "changed files\n") {
				t.Fatalf("unexpected human output: %q", stdout.String())
			}
		})
	}
}

func TestRunStartupFailureIsPrefixedAndDoesNotRequireRepository(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := run(runRequest{
		ctx:    t.Context(),
		args:   []string{"diff", "--range", "bad...range", "--stdout"},
		cwd:    t.TempDir(),
		stdin:  strings.NewReader(""),
		stdout: &stdout,
		stderr: &stderr,
	})
	if status != 1 || stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "rvw: ") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestRunValidatesGlobalConfigBeforeRepositoryDiscovery(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".config", "rvw"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeRunFile(t, filepath.Join(home, ".config", "rvw", "config.toml"), "unknown = true\n")
	var stdout, stderr bytes.Buffer
	status := run(runRequest{
		ctx:         t.Context(),
		args:        []string{"diff", "--stdout"},
		environment: []string{"HOME=" + home},
		cwd:         root,
		stdin:       strings.NewReader(""),
		stdout:      &stdout,
		stderr:      &stderr,
	})
	if status != 1 || !strings.Contains(stderr.String(), "unknown field") ||
		strings.Contains(stderr.String(), "Git repository") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestRunHelpSucceedsWithoutRepository(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := run(runRequest{
		ctx:    t.Context(),
		args:   []string{"--help"},
		cwd:    t.TempDir(),
		stdin:  strings.NewReader(""),
		stdout: &stdout,
		stderr: &stderr,
	})
	if status != 0 || stderr.Len() != 0 || !strings.HasPrefix(stdout.String(), "usage:") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestTerminationOwnerCancelsThenEscalatesRepeatedSignal(t *testing.T) {
	executionContext, cancelExecution := context.WithCancel(t.Context())
	owner := newTerminationOwner(cancelExecution)
	ownerClosed := false
	defer func() {
		if !ownerClosed {
			owner.Close()
		}
	}()

	escalated := make(chan struct{}, 1)
	unregister := owner.RegisterEscalator(func() {
		escalated <- struct{}{}
	})

	owner.signals <- os.Interrupt
	select {
	case <-executionContext.Done():
	case <-time.After(time.Second):
		t.Fatal("first signal did not cancel execution")
	}
	if !owner.Received() {
		t.Fatal("first signal was not recorded")
	}
	select {
	case <-escalated:
		t.Fatal("first signal escalated instead of only cancelling")
	default:
	}

	owner.signals <- os.Interrupt
	select {
	case <-escalated:
	case <-time.After(time.Second):
		t.Fatal("repeated signal did not invoke the registered escalator")
	}

	unregistered := make(chan struct{})
	go func() {
		unregister()
		close(unregistered)
	}()
	select {
	case <-unregistered:
	case <-time.After(time.Second):
		t.Fatal("unregister blocked")
	}

	closed := make(chan struct{})
	go func() {
		owner.Close()
		close(closed)
	}()
	select {
	case <-closed:
		ownerClosed = true
	case <-time.After(time.Second):
		t.Fatal("Close blocked")
	}
}

func TestRunInteractiveStartsAndQuitsThroughConfiguredBinding(t *testing.T) {
	root := testRepository(t)
	writeRunFile(t, filepath.Join(root, "file.txt"), "original\n")
	runTestGit(t, root, "add", "file.txt")
	runTestGit(t, root, "commit", "-m", "initial")
	writeRunFile(t, filepath.Join(root, "file.txt"), "changed\n")

	runContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	status := run(runRequest{
		ctx:         runContext,
		args:        []string{"diff"},
		environment: []string{"HOME=" + filepath.Join(root, "home"), "NO_COLOR=1"},
		cwd:         root,
		stdin:       strings.NewReader("q"),
		stdout:      &stdout,
		stderr:      &stderr,
		terminals:   testTerminalCapabilities(t, terminalInput, terminalOutput),
	})
	if status != 0 {
		t.Fatalf("run() status = %d, stderr = %s", status, stderr.String())
	}
	if !strings.Contains(stdout.String(), "\x1b[?1049h") ||
		!strings.Contains(stdout.String(), "\x1b[?1049l") {
		t.Fatalf("interactive output did not enter and restore alternate screen: %q", stdout.String())
	}
}

func testRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runTestGit(t, root, "init")
	runTestGit(t, root, "config", "user.email", "rvw@example.test")
	runTestGit(t, root, "config", "user.name", "rvw test")
	return root
}

func runTestGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func writeRunFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
