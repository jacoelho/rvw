package source

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootedFileKeepsLogicalName(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "objects", "pack")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "value.pack"), []byte("pack"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	filesystem := newRootedReadOnlyFilesystem(root)
	file, err := filesystem.Open(filepath.Join("objects", "pack", "value.pack"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if file.Name() != filepath.Join("objects", "pack", "value.pack") {
		t.Fatalf("logical name = %q", file.Name())
	}
	if _, err := filesystem.Stat(file.Name()); err != nil {
		t.Fatalf("reopen logical name: %v", err)
	}
}
