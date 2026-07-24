package source

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"os"
	"path/filepath"
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

func TestStreamIndexReadsOptionalExtensionAcrossBufferBoundary(t *testing.T) {
	encoded := encodeIndexWithExtension(t, "TEST", make([]byte, 16<<10))
	path := filepath.Join(t.TempDir(), "index")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	count, err := streamIndex(t.Context(), path, func(streamedIndexEntry) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d, want 1", count)
	}
}

func TestStreamIndexRejectsMalformedBufferedExtension(t *testing.T) {
	encoded := encodeIndexWithExtension(t, "EOIE", []byte{0})
	path := filepath.Join(t.TempDir(), "index")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := streamIndex(t.Context(), path, func(streamedIndexEntry) error { return nil })
	const want = "git index EOIE extension has an invalid size"
	if err == nil || err.Error() != want {
		t.Fatalf("error=%v, want %q", err, want)
	}
}

func encodeIndexWithExtension(t *testing.T, signature string, payload []byte) []byte {
	t.Helper()
	if len(signature) != 4 {
		t.Fatalf("extension signature length=%d, want 4", len(signature))
	}

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
	body := bytes.Clone(encoded.Bytes()[:encoded.Len()-indexChecksumBytes])
	body = append(body, signature...)
	body = binary.BigEndian.AppendUint32(body, uint32(len(payload)))
	body = append(body, payload...)
	checksum := sha1.Sum(body)
	return append(body, checksum[:]...)
}
