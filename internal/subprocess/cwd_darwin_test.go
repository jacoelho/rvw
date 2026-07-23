//go:build darwin

package subprocess

import (
	"os"
	"testing"
)

func TestDirectoryLauncherTargetDescriptorLayout(t *testing.T) {
	path := BorrowedFilePath(0)
	if path != "/dev/fd/5" {
		t.Fatalf("first borrowed file path = %q", path)
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	borrowed := createTestSpool(t, []byte("object capability"))
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	request := helperRequest("descriptor-layout")
	request.DirFile = directory
	request.BorrowedFiles = []*os.File{borrowed}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Stdout); got != "fd3-witness fd4-closed fd5-borrowed" {
		t.Fatalf("target descriptor layout = %q", got)
	}
}
