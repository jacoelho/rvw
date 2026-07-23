//go:build linux

package subprocess

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDirectoryLauncherKeepsWitnessAndClosesDirectoryDescriptor(t *testing.T) {
	path := BorrowedFilePath(0)
	if path != "/proc/self/fd/5" {
		t.Fatalf("first borrowed file path = %q", path)
	}
	readlink, err := exec.LookPath("readlink")
	if err != nil {
		t.Skip("readlink is unavailable")
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	borrowed := createTestSpool(t, []byte("object capability"))
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	request := Request{
		Name:          readlink,
		Args:          []string{"/proc/self/fd/3", "/proc/self/fd/4", "/proc/self/fd/5"},
		DirFile:       directory,
		BorrowedFiles: []*os.File{borrowed},
		AcceptedExits: []int{1},
	}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 {
		t.Fatalf("readlink exit = %d, want 1 for closed FD 4", result.ExitCode)
	}
	lines := strings.Split(strings.TrimSpace(string(result.Stdout)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "pipe:[") {
		t.Fatalf("target descriptor enumeration = %q", result.Stdout)
	}
	if lines[1] != borrowed.Name() {
		t.Fatalf("FD 5 path = %q, want %q", lines[1], borrowed.Name())
	}
	if _, err := borrowed.Stat(); err != nil {
		t.Fatalf("Run closed caller-owned borrowed file: %v", err)
	}
}

func TestDirectoryLauncherUnsetsPWDForTarget(t *testing.T) {
	env, err := exec.LookPath("env")
	if err != nil {
		t.Skip("env is unavailable")
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	result, err := runner.Run(t.Context(), Request{Name: env, DirFile: directory})
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(result.Stdout)) {
		if strings.HasPrefix(line, "PWD=") {
			t.Fatalf("target inherited PWD: %q", line)
		}
	}
}
