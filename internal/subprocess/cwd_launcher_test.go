//go:build darwin || linux

package subprocess

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDirectoryLauncherReportsFchdirFailure(t *testing.T) {
	notDirectory, err := os.CreateTemp(t.TempDir(), "not-directory-*")
	if err != nil {
		t.Fatal(err)
	}
	defer notDirectory.Close()
	output, err := runDirectoryLauncherTest(t, notDirectory, "/usr/bin/true")
	assertDirectoryLauncherFailure(t, output, err, "fchdir")
}

func TestDirectoryLauncherReportsExecFailure(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	output, err := runDirectoryLauncherTest(t, directory, "/rvw-command-that-does-not-exist")
	assertDirectoryLauncherFailure(t, output, err, "exec")
}

func TestBorrowedFilePathRejectsIndexesOutsideRequestBound(t *testing.T) {
	for _, index := range []int{-1, MaxBorrowedFiles} {
		if path := BorrowedFilePath(index); path != "" {
			t.Fatalf("BorrowedFilePath(%d) = %q, want empty", index, path)
		}
	}
	path := BorrowedFilePath(0)
	if !strings.HasSuffix(path, "/5") {
		t.Fatalf("first borrowed file path = %q, want FD 5", path)
	}
}

func runDirectoryLauncherTest(t *testing.T, directory *os.File, executable string) (string, error) {
	t.Helper()
	launcher, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	lifetime, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer lifetime.Close()
	command := exec.CommandContext(t.Context(), launcher)
	command.Args = []string{descriptorLauncherArg0, executable, executable}
	command.ExtraFiles = []*os.File{lifetime, directory}
	output, err := command.CombinedOutput()
	return string(output), err
}

func assertDirectoryLauncherFailure(t *testing.T, output string, err error, detail string) {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 126 {
		t.Fatalf("launcher error = %v, output = %q; want exit 126", err, output)
	}
	if !strings.Contains(output, "directory launcher: "+detail) {
		t.Fatalf("launcher output = %q, want %s failure", output, detail)
	}
}
