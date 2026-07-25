package source

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	gitindex "github.com/go-git/go-git/v5/plumbing/format/index"
)

func TestStreamIndexMatchesGoGitEncodedVersions(t *testing.T) {
	for _, version := range []uint32{2, 3, 4} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			entries := []*gitindex.Entry{
				{
					Name: "a.txt",
					Hash: plumbing.NewHash("1111111111111111111111111111111111111111"),
					Mode: filemode.Regular,
				},
				{
					Name: "directory/executable",
					Hash: plumbing.NewHash("2222222222222222222222222222222222222222"),
					Mode: filemode.Executable,
				},
				{
					Name: "directory/link",
					Hash: plumbing.NewHash("3333333333333333333333333333333333333333"),
					Mode: filemode.Symlink,
				},
			}
			if version >= 3 {
				entries = append(entries, &gitindex.Entry{
					Name:        "intent.txt",
					Mode:        filemode.Regular,
					IntentToAdd: true,
				})
			}
			encoded := new(bytes.Buffer)
			if err := gitindex.NewEncoder(encoded).Encode(&gitindex.Index{
				Version: version,
				Entries: entries,
			}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "index")
			if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			var got []streamedIndexEntry
			count, err := streamIndex(t.Context(), path, func(entry streamedIndexEntry) error {
				got = append(got, entry)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if int(count) != len(entries) || len(got) != len(entries) {
				t.Fatalf("count=%d retained=%d want=%d", count, len(got), len(entries))
			}
			for index, want := range entries {
				if got[index].path != want.Name || got[index].oid != want.Hash ||
					got[index].mode != uint32(want.Mode) ||
					got[index].intentToAdd != want.IntentToAdd ||
					got[index].skipWorktree != want.SkipWorktree {
					t.Fatalf("entry %d = %+v, want %+v", index, got[index], want)
				}
			}
		})
	}
}

func TestStreamIndexRejectsChecksumAndHonorsCancellation(t *testing.T) {
	encoded := new(bytes.Buffer)
	if err := gitindex.NewEncoder(encoded).Encode(&gitindex.Index{
		Version: 2,
		Entries: []*gitindex.Entry{{
			Name: "a",
			Hash: plumbing.NewHash("1111111111111111111111111111111111111111"),
			Mode: filemode.Regular,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index")
	corrupt := bytes.Clone(encoded.Bytes())
	corrupt[len(corrupt)-1] ^= 1
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := streamIndex(t.Context(), path, func(streamedIndexEntry) error { return nil }); err == nil {
		t.Fatal("corrupt checksum succeeded")
	}

	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := streamIndex(ctx, path, func(streamedIndexEntry) error { return nil }); err == nil {
		t.Fatal("cancelled decode succeeded")
	}
}

func TestStreamIndexAcceptsExtensionPrefetchedWithEntries(t *testing.T) {
	const entryCount = 180
	entries := make([]*gitindex.Entry, 0, entryCount)
	for index := range entryCount {
		entries = append(entries, &gitindex.Entry{
			Name: fmt.Sprintf("path-%04d.txt", index),
			Hash: plumbing.NewHash(fmt.Sprintf("%040x", index+1)),
			Mode: filemode.Regular,
		})
	}
	encoded := new(bytes.Buffer)
	if err := gitindex.NewEncoder(encoded).Encode(&gitindex.Index{
		Version: 2,
		Entries: entries,
	}); err != nil {
		t.Fatal(err)
	}
	contents := encoded.Bytes()
	contents = appendIndexExtension(t, contents, "TEST", bytes.Repeat([]byte{0x5a}, 4<<10))
	path := filepath.Join(t.TempDir(), "index")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	count, err := streamIndex(t.Context(), path, func(streamedIndexEntry) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if count != entryCount {
		t.Fatalf("count = %d, want %d", count, entryCount)
	}
}

func TestStreamIndexRejectsPrefetchedMandatoryExtension(t *testing.T) {
	encoded := new(bytes.Buffer)
	if err := gitindex.NewEncoder(encoded).Encode(&gitindex.Index{
		Version: 2,
		Entries: []*gitindex.Entry{{
			Name: "a.txt",
			Hash: plumbing.NewHash("1111111111111111111111111111111111111111"),
			Mode: filemode.Regular,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	contents := appendIndexExtension(t, encoded.Bytes(), "link", []byte{0})
	path := filepath.Join(t.TempDir(), "index")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := streamIndex(t.Context(), path, func(streamedIndexEntry) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "unsupported mandatory Git index extension") {
		t.Fatalf("mandatory extension error = %v", err)
	}
}

func appendIndexExtension(t *testing.T, encoded []byte, signature string, body []byte) []byte {
	t.Helper()
	if len(encoded) < indexChecksumBytes || len(signature) != 4 {
		t.Fatal("invalid encoded index fixture")
	}
	contents := bytes.Clone(encoded[:len(encoded)-indexChecksumBytes])
	var header [8]byte
	copy(header[:4], signature)
	binary.BigEndian.PutUint32(header[4:], uint32(len(body)))
	contents = append(contents, header[:]...)
	contents = append(contents, body...)
	checksum := sha1.Sum(contents)
	return append(contents, checksum[:]...)
}
