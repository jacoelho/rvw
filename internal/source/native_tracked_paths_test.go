package source

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTrackedPathSetUsesExactDiskBackedMembership(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	indexPath := filepath.Join(workspace, "index")
	var header [indexHeaderBytes]byte
	copy(header[:4], "DIRC")
	binary.BigEndian.PutUint32(header[4:8], 2)
	binary.BigEndian.PutUint32(header[8:12], 8)
	if err := os.WriteFile(indexPath, header[:], 0o600); err != nil {
		t.Fatalf("write index header: %v", err)
	}

	set, err := newTrackedPathSet(workspace, indexPath)
	if err != nil {
		t.Fatalf("new tracked path set: %v", err)
	}
	first, second := trackedPathsWithSameBucket(set.slots)
	for _, path := range []string{first, second, "nested/tracked.txt"} {
		if err := set.add(path); err != nil {
			t.Fatalf("add %q: %v", path, err)
		}
	}
	beforeDuplicate := set.pathOffset
	if err := set.add(first); err != nil {
		t.Fatalf("add duplicate path: %v", err)
	}
	if set.pathOffset != beforeDuplicate {
		t.Fatalf("duplicate path changed data size: got %d, want %d", set.pathOffset, beforeDuplicate)
	}
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: first, want: true},
		{path: second, want: true},
		{path: "nested/tracked.txt", want: true},
		{path: "nested/untracked.txt", want: false},
	} {
		got, containsErr := set.contains(test.path)
		if containsErr != nil {
			t.Fatalf("contains %q: %v", test.path, containsErr)
		}
		if got != test.want {
			t.Fatalf("contains %q = %t, want %t", test.path, got, test.want)
		}
	}

	tablePath, pathsPath := set.tablePath, set.pathsPath
	if err := set.Close(); err != nil {
		t.Fatalf("close tracked path set: %v", err)
	}
	for _, path := range []string{tablePath, pathsPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary path %q remains after close: %v", path, err)
		}
	}
}

func trackedPathsWithSameBucket(slots uint64) (string, string) {
	seen := make(map[uint64]string)
	for value := uint64(0); ; value++ {
		path := "tracked-" + string(rune('a'+value))
		digest := sha256.Sum256([]byte(path))
		bucket := binary.BigEndian.Uint64(digest[:8]) & (slots - 1)
		if first, ok := seen[bucket]; ok {
			return first, path
		}
		seen[bucket] = path
	}
}
