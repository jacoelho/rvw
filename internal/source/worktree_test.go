package source

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestPrepareWorktreeCandidatesNormalizesUnmergedRecordGroups(t *testing.T) {
	regular := testFileMode(t, "100644")
	executable := testFileMode(t, "100755")
	zero := testFileMode(t, "000000")
	tests := []struct {
		name        string
		records     []diffRecord
		wantRaw     bool
		wantRemoval bool
		wantMode    review.FileMode
	}{
		{
			name:     "ordinary concrete record",
			records:  []diffRecord{testWorktreeRecord('M', "path", regular)},
			wantRaw:  true,
			wantMode: regular,
		},
		{
			name: "unmerged then concrete",
			records: []diffRecord{
				testWorktreeRecord('U', "path", regular),
				testWorktreeRecord('M', "path", executable),
			},
			wantRaw:  true,
			wantMode: executable,
		},
		{
			name: "concrete then unmerged",
			records: []diffRecord{
				testWorktreeRecord('T', "path", executable),
				testWorktreeRecord('U', "path", regular),
			},
			wantRaw:  true,
			wantMode: executable,
		},
		{
			name:     "unmerged readable",
			records:  []diffRecord{testWorktreeRecord('U', "path", regular)},
			wantRaw:  true,
			wantMode: regular,
		},
		{
			name:        "unmerged removal",
			records:     []diffRecord{testWorktreeRecord('U', "path", zero)},
			wantRemoval: true,
		},
		{
			name:        "concrete deletion",
			records:     []diffRecord{testWorktreeRecord('D', "path", zero)},
			wantRemoval: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger, ledgerErr := newSourceLedger(0)
			if ledgerErr != nil {
				t.Fatal(ledgerErr)
			}
			raw, removals, paths, err := prepareWorktreeCandidates(
				t.Context(), ledger, test.records, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			wantRaw := 0
			if test.wantRaw {
				wantRaw = 1
			}
			wantRemovals := 0
			if test.wantRemoval {
				wantRemovals = 1
			}
			if len(raw) != wantRaw || len(removals) != wantRemovals {
				t.Fatalf("raw=%+v removals=%+v", raw, removals)
			}
			if len(paths) != 1 || paths[0] != "path" {
				t.Fatalf("paths = %q", paths)
			}
			if test.wantRaw && raw[0].mode != test.wantMode {
				t.Fatalf("mode = %q, want %q", raw[0].mode, test.wantMode)
			}
		})
	}
}

func TestPrepareWorktreeCandidatesRejectsAmbiguousRecordGroups(t *testing.T) {
	mode := testFileMode(t, "100644")
	for _, test := range []struct {
		name    string
		records []diffRecord
	}{
		{
			name: "duplicate unmerged",
			records: []diffRecord{
				testWorktreeRecord('U', "path", mode),
				testWorktreeRecord('U', "path", mode),
			},
		},
		{
			name: "duplicate concrete",
			records: []diffRecord{
				testWorktreeRecord('M', "path", mode),
				testWorktreeRecord('T', "path", mode),
			},
		},
		{
			name: "three records",
			records: []diffRecord{
				testWorktreeRecord('U', "path", mode),
				testWorktreeRecord('M', "path", mode),
				testWorktreeRecord('T', "path", mode),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger, ledgerErr := newSourceLedger(0)
			if ledgerErr != nil {
				t.Fatal(ledgerErr)
			}
			if _, _, _, err := prepareWorktreeCandidates(
				t.Context(), ledger, test.records, nil,
			); err == nil {
				t.Fatal("ambiguous group was accepted")
			}
		})
	}
}

func TestPrepareWorktreeCandidatesPreservesUniqueSortedPaths(t *testing.T) {
	mode := testFileMode(t, "100644")
	untracked, err := parseUntrackedWorktreeRecords(
		[]byte("a\x00b\x00"),
		make(map[string]struct{}),
		review.MaxFiles,
	)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, paths, err := prepareWorktreeCandidates(
		t.Context(), ledger,
		[]diffRecord{
			testWorktreeRecord('U', "z", mode),
			testWorktreeRecord('M', "z", mode),
			testWorktreeRecord('M', "b", mode),
		},
		untracked,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths; !slices.Equal(got, []string{"a", "b", "z"}) {
		t.Fatalf("paths = %q", got)
	}
	if got := rawWorktreePaths(raw); !slices.Equal(got, []string{"z", "b", "a"}) {
		t.Fatalf("raw paths = %q", got)
	}
}

func TestRawWorktreeBatchCeilings(t *testing.T) {
	batch := rawWorktreeBatch{}
	if !batch.accepts(rawWorktreeBatchBytes+1, 1) {
		t.Fatal("an oversized accepted file must form an isolated batch")
	}
	batch.add(stagedWorktreeBlob{file: "a", size: rawWorktreeBatchBytes})
	if !batch.accepts(0, 1) || batch.accepts(1, 1) {
		t.Fatal("raw byte boundary was not enforced exactly")
	}

	batch = rawWorktreeBatch{
		blobs:      make([]stagedWorktreeBlob, rawWorktreeBatchEntries-1),
		inputBytes: rawWorktreeHashInputBytes - 1,
	}
	if !batch.accepts(0, 1) {
		t.Fatal("exact entry and hash-input boundaries were rejected")
	}
	batch.blobs = append(batch.blobs, stagedWorktreeBlob{})
	if batch.accepts(0, 0) {
		t.Fatal("entry boundary+1 was accepted")
	}
	batch.blobs = batch.blobs[:rawWorktreeBatchEntries-1]
	if batch.accepts(0, 2) {
		t.Fatal("hash-input boundary+1 was accepted")
	}
}

func TestParseRawWorktreeIdentitiesRequiresExactValidSequence(t *testing.T) {
	oidA := strings.Repeat("1", 40)
	oidB := strings.Repeat("2", 40)
	identities, err := parseRawWorktreeIdentities([]byte(oidA+"\n"+oidB+"\n"), 2)
	if err != nil || len(identities) != 2 {
		t.Fatalf("valid identities = %v, %v", identities, err)
	}
	for name, output := range map[string][]byte{
		"missing":   []byte(oidA + "\n"),
		"malformed": []byte("invalid\n" + oidB + "\n"),
		"extra":     []byte(oidA + "\n" + oidB + "\n" + oidA + "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRawWorktreeIdentities(output, 2); err == nil {
				t.Fatal("invalid identity sequence was accepted")
			}
		})
	}
}

func TestRawWorktreeBatchCancellationCleansStagingAndPreservesCause(t *testing.T) {
	staged := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(staged, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("cancel raw batch")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	batch := rawWorktreeBatch{}
	batch.add(stagedWorktreeBlob{file: staged, entryIndex: 0, size: 4})
	err := (&Service{}).flushRawWorktreeBatch(
		ctx,
		nil,
		nil,
		[]rawWorktreeEntry{{path: "path"}},
		&batch,
		directCPUAdmission,
	)
	if !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file survived cancellation: %v", err)
	}
	if len(batch.blobs) != 0 || batch.rawBytes != 0 || batch.inputBytes != 0 {
		t.Fatalf("batch was not reset: %+v", batch)
	}
}

func TestRawWorktreeBatchCleanupFailureDoesNotMaskCancellation(t *testing.T) {
	staged := filepath.Join(t.TempDir(), "not-a-file")
	if err := os.Mkdir(staged, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "child"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("cancel raw batch")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	batch := rawWorktreeBatch{}
	batch.add(stagedWorktreeBlob{file: staged, entryIndex: 0})
	err := (&Service{}).flushRawWorktreeBatch(
		ctx,
		nil,
		nil,
		[]rawWorktreeEntry{{path: "path"}},
		&batch,
		directCPUAdmission,
	)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "remove raw blob staging file") {
		t.Fatalf("joined cleanup error = %v", err)
	}
}

func TestWorktreeRawBatchesRemoveEveryStagingFile(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "base"), "base\n")
	runGitTest(t, root, "add", "base")
	runGitTest(t, root, "commit", "-m", "base")
	for index := range rawWorktreeBatchEntries + 1 {
		name := filepath.Join(root, "untracked-"+leftPadDecimal(index, 4))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, err := service.Open(
		t.Context(),
		WorktreeTarget(),
		OpenOptions{IncludeUntracked: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	entries, err := os.ReadDir(filepath.Join(document.revision.cleanupPath, "raw-blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("raw staging retained %d files", len(entries))
	}
}

func TestWorktreeFileLargerThanNormalBatchHashesInIsolation(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "base"), "base\n")
	runGitTest(t, root, "add", "base")
	runGitTest(t, root, "commit", "-m", "base")
	large := filepath.Join(root, "large.bin")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(file.Truncate(rawWorktreeBatchBytes+1), file.Close()); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, err := service.Open(
		t.Context(), WorktreeTarget(), OpenOptions{IncludeUntracked: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	fileEntry, ok := document.Snapshot().File(0)
	if !ok || document.Snapshot().FileCount() != 1 || fileEntry.DisplayPath() != "large.bin" {
		t.Fatalf("isolated large-file snapshot = %+v, present=%v", document.Snapshot(), ok)
	}
}

func TestOpenRawWorktreePathRetainsPerFileLimit(t *testing.T) {
	directory, root := newOpenTestRoot(t)
	path := filepath.Join(directory, "too-large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(file.Truncate(maxWorktreeFileBytes+1), file.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err := openRawWorktreePath(root, "too-large.bin", review.FileMode("")); err == nil ||
		!strings.Contains(err.Error(), "exceeds raw byte limit") {
		t.Fatalf("per-file limit error = %v", err)
	}
}

func TestOpenRawWorktreePathDerivesExecutableModeFromOpenedDescriptor(t *testing.T) {
	directory, root := newOpenTestRoot(t)
	path := filepath.Join(directory, "executable")
	if err := os.WriteFile(path, []byte("body"), 0o700); err != nil {
		t.Fatal(err)
	}
	capture, err := openRawWorktreePath(root, "executable", review.FileMode(""))
	if err != nil {
		t.Fatal(err)
	}
	defer capture.close()
	if capture.mode.String() != "100755" {
		t.Fatalf("captured mode = %s, want 100755", capture.mode)
	}
}

func TestOpenRawWorktreePathRejectsMetadataChangeBeforeDescriptorOpen(t *testing.T) {
	directory, root := newOpenTestRoot(t)
	path := filepath.Join(directory, "chmod-race")
	if err := os.WriteFile(path, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := openRawWorktreePathWith(
		root,
		"chmod-race",
		review.FileMode(""),
		func(root *os.Root, relative string) (*os.File, error) {
			if err := root.Chmod(relative, 0o700); err != nil {
				return nil, err
			}
			return openRawWorktreeRegular(root, relative)
		},
	)
	if err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("metadata race error = %v", err)
	}
}

func TestCopyBoundedWorktreeFileEnforcesLimitAndBufferSize(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		wantBytes int64
		wantError bool
	}{
		{name: "exact", body: "12345678", wantBytes: 8},
		{name: "limit plus one", body: "123456789", wantBytes: 8, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &maximumReadReader{reader: bytes.NewReader([]byte(test.body))}
			var destination bytes.Buffer
			written, err := copyBoundedWorktreeFile(
				t.Context(),
				&destination,
				source,
				make([]byte, 4),
				8,
			)
			if (err != nil) != test.wantError || written != test.wantBytes {
				t.Fatalf("copy = %d bytes, %v", written, err)
			}
			if source.maximum > 4 {
				t.Fatalf("maximum read request = %d, want at most 4", source.maximum)
			}
			if test.wantError && !strings.Contains(err.Error(), "exceeds raw byte limit") {
				t.Fatalf("limit error = %v", err)
			}
		})
	}
}

func TestCopyBoundedWorktreeFilePreservesCancellationCause(t *testing.T) {
	cause := errors.New("cancel staged copy")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if _, err := copyBoundedWorktreeFile(
		ctx,
		io.Discard,
		strings.NewReader("body"),
		make([]byte, 4),
		8,
	); !errors.Is(err, cause) {
		t.Fatalf("pre-cancel error = %v", err)
	}

	ctx, cancel = context.WithCancelCause(t.Context())
	source := &cancelAfterRead{cancel: cancel, cause: cause, body: []byte("body")}
	if _, err := copyBoundedWorktreeFile(
		ctx,
		io.Discard,
		source,
		make([]byte, 4),
		8,
	); !errors.Is(err, cause) {
		t.Fatalf("mid-copy cancellation error = %v", err)
	}
}

func TestCopyBoundedWorktreeFilePropagatesShortWrite(t *testing.T) {
	_, err := copyBoundedWorktreeFile(
		t.Context(),
		shortWriter{},
		strings.NewReader("body"),
		make([]byte, 4),
		8,
	)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short-write error = %v", err)
	}
}

func TestRawWorktreeCaptureRejectsPathReplacementAfterCopy(t *testing.T) {
	directory, root := newOpenTestRoot(t)
	path := filepath.Join(directory, "path")
	if err := os.WriteFile(path, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture, err := openRawWorktreePath(root, "path", review.FileMode(""))
	if err != nil {
		t.Fatal(err)
	}
	defer capture.close()
	var staged bytes.Buffer
	written, err := capture.write(t.Context(), &staged, make([]byte, 4))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(directory, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := capture.verify(written); err == nil ||
		!strings.Contains(err.Error(), "changed while reading") {
		t.Fatalf("replacement verification error = %v", err)
	}
}

func TestOpenWorktreeNormalizesContentAndDeleteModifyConflicts(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "content.txt"), "base\n")
	writeFile(t, filepath.Join(root, "deleted-on-main.txt"), "base\n")
	runGitTest(t, root, "add", ".")
	runGitTest(t, root, "commit", "-m", "base")
	mainBranch := strings.TrimSpace(runGitTest(t, root, "branch", "--show-current"))

	runGitTest(t, root, "checkout", "-b", "side")
	writeFile(t, filepath.Join(root, "content.txt"), "side\n")
	writeFile(t, filepath.Join(root, "deleted-on-main.txt"), "side\n")
	runGitTest(t, root, "commit", "-am", "side")
	runGitTest(t, root, "checkout", mainBranch)
	writeFile(t, filepath.Join(root, "content.txt"), "main\n")
	runGitTest(t, root, "rm", "--quiet", "deleted-on-main.txt")
	runGitTest(t, root, "commit", "-am", "main")
	runConflictingMerge(t, root)

	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	got := make(map[string]review.ChangeKind)
	for file := range document.Snapshot().AllFiles() {
		got[file.DisplayPath()] = file.Change().Kind()
	}
	want := map[string]review.ChangeKind{
		"content.txt":         review.ChangeModified,
		"deleted-on-main.txt": review.ChangeAdded,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("worktree conflict snapshot = %v, want %v", got, want)
	}
}

func TestOpenWorktreeNormalizesAddAddConflict(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "base"), "base\n")
	runGitTest(t, root, "add", "base")
	runGitTest(t, root, "commit", "-m", "base")
	mainBranch := strings.TrimSpace(runGitTest(t, root, "branch", "--show-current"))
	runGitTest(t, root, "checkout", "-b", "side")
	writeFile(t, filepath.Join(root, "added.txt"), "side\n")
	runGitTest(t, root, "add", "added.txt")
	runGitTest(t, root, "commit", "-m", "side")
	runGitTest(t, root, "checkout", mainBranch)
	writeFile(t, filepath.Join(root, "added.txt"), "main\n")
	runGitTest(t, root, "add", "added.txt")
	runGitTest(t, root, "commit", "-m", "main")
	runConflictingMerge(t, root)

	got := openWorktreeKinds(t, root)
	if !maps.Equal(got, map[string]review.ChangeKind{"added.txt": review.ChangeModified}) {
		t.Fatalf("add/add worktree snapshot = %v", got)
	}
}

func TestOpenWorktreeDeleteByThemConflictCanProduceNoDelta(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "path.txt"), "base\n")
	runGitTest(t, root, "add", "path.txt")
	runGitTest(t, root, "commit", "-m", "base")
	mainBranch := strings.TrimSpace(runGitTest(t, root, "branch", "--show-current"))
	runGitTest(t, root, "checkout", "-b", "side")
	runGitTest(t, root, "rm", "--quiet", "path.txt")
	runGitTest(t, root, "commit", "-m", "side deletes")
	runGitTest(t, root, "checkout", mainBranch)
	writeFile(t, filepath.Join(root, "path.txt"), "main\n")
	runGitTest(t, root, "commit", "-am", "main modifies")
	runConflictingMerge(t, root)

	if got := openWorktreeKinds(t, root); len(got) != 0 {
		t.Fatalf("unchanged conflict worktree produced changes: %v", got)
	}
}

func TestOpenWorktreeDeletedConflictFileBecomesRemoval(t *testing.T) {
	root := contentConflictRepository(t, "path.txt")
	if err := os.Remove(filepath.Join(root, "path.txt")); err != nil {
		t.Fatal(err)
	}
	got := openWorktreeKinds(t, root)
	if !maps.Equal(got, map[string]review.ChangeKind{"path.txt": review.ChangeDeleted}) {
		t.Fatalf("deleted conflict worktree snapshot = %v", got)
	}
}

func TestOpenWorktreeSymlinkConflictUsesFilesystemObject(t *testing.T) {
	root := initializedRepository(t)
	link := filepath.Join(root, "link")
	if err := os.Symlink("base-target", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	runGitTest(t, root, "add", "link")
	runGitTest(t, root, "commit", "-m", "base")
	mainBranch := strings.TrimSpace(runGitTest(t, root, "branch", "--show-current"))
	runGitTest(t, root, "checkout", "-b", "side")
	replaceSymlink(t, link, "side-target")
	runGitTest(t, root, "add", "link")
	runGitTest(t, root, "commit", "-m", "side")
	runGitTest(t, root, "checkout", mainBranch)
	replaceSymlink(t, link, "main-target")
	runGitTest(t, root, "add", "link")
	runGitTest(t, root, "commit", "-m", "main")
	runConflictingMerge(t, root)

	if got := openWorktreeKinds(t, root); len(got) != 0 {
		t.Fatalf("unchanged symlink conflict produced changes: %v", got)
	}
}

func TestOpenWorktreeFileSymlinkConflictUsesFilesystemObject(t *testing.T) {
	root := initializedRepository(t)
	path := filepath.Join(root, "path")
	writeFile(t, path, "base\n")
	runGitTest(t, root, "add", "path")
	runGitTest(t, root, "commit", "-m", "base")
	mainBranch := strings.TrimSpace(runGitTest(t, root, "branch", "--show-current"))
	runGitTest(t, root, "checkout", "-b", "side")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("side-target", path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	runGitTest(t, root, "add", "path")
	runGitTest(t, root, "commit", "-m", "side")
	runGitTest(t, root, "checkout", mainBranch)
	writeFile(t, path, "main\n")
	runGitTest(t, root, "commit", "-am", "main")
	runConflictingMerge(t, root)

	got := openWorktreeKinds(t, root)
	want := map[string]review.ChangeKind{
		"path":      review.ChangeModified,
		"path~HEAD": review.ChangeCopied,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("file/symlink worktree snapshot = %v, want %v", got, want)
	}
}

func TestOpenWorktreeStageOneOnlyZeroModeBecomesRemoval(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "path.txt"), "base\n")
	runGitTest(t, root, "add", "path.txt")
	runGitTest(t, root, "commit", "-m", "base")
	oid := strings.TrimSpace(runGitTest(t, root, "rev-parse", "HEAD:path.txt"))
	command := exec.CommandContext(t.Context(), "git", "update-index", "-z", "--index-info")
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	command.Stdin = strings.NewReader("100644 " + oid + " 1\tpath.txt\x00")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("construct stage-1-only index: %v\n%s", err, output)
	}
	if err := os.Remove(filepath.Join(root, "path.txt")); err != nil {
		t.Fatal(err)
	}
	got := openWorktreeKinds(t, root)
	if !maps.Equal(got, map[string]review.ChangeKind{"path.txt": review.ChangeDeleted}) {
		t.Fatalf("stage-1-only worktree snapshot = %v", got)
	}
}

func BenchmarkRawWorktreeBatchAdmission(b *testing.B) {
	blobs := make([]stagedWorktreeBlob, rawWorktreeBatchEntries)
	for index := range blobs {
		blobs[index] = stagedWorktreeBlob{file: leftPadDecimal(index, 4), size: 64 << 10}
	}
	b.ReportAllocs()
	for b.Loop() {
		batch := rawWorktreeBatch{}
		for _, blob := range blobs {
			if !batch.accepts(blob.size, len(blob.file)+1) {
				b.Fatal("batch rejected an exact-boundary entry")
			}
			batch.add(blob)
		}
	}
}

func BenchmarkContainingWorktreeRepository100000Roots(b *testing.B) {
	roots := make(map[string]string, 100_000)
	paths := make([]string, 100_000)
	for index := range 100_000 {
		root := "repository-" + strconv.Itoa(index)
		roots[root] = root
		paths[index] = root + "/child"
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, path := range paths {
			if _, ok := containingWorktreeRepository(path, roots); !ok {
				b.Fatal("repository root was not found")
			}
		}
	}
}

func BenchmarkCopyBoundedWorktreeFile(b *testing.B) {
	for _, test := range []struct {
		name string
		size int64
	}{
		{name: "16MiB", size: 16 << 20},
		{name: "128MiB", size: 128 << 20},
	} {
		b.Run(test.name, func(b *testing.B) {
			buffer := make([]byte, rawWorktreeCopyBufferBytes)
			b.ReportAllocs()
			b.SetBytes(test.size)
			for b.Loop() {
				source := io.LimitReader(zeroReader{}, test.size)
				written, err := copyBoundedWorktreeFile(
					b.Context(),
					io.Discard,
					source,
					buffer,
					test.size,
				)
				if err != nil || written != test.size {
					b.Fatalf("copy = %d bytes, %v", written, err)
				}
			}
		})
	}
}

type maximumReadReader struct {
	reader  *bytes.Reader
	maximum int
}

func (reader *maximumReadReader) Read(value []byte) (int, error) {
	reader.maximum = max(reader.maximum, len(value))
	return reader.reader.Read(value)
}

type cancelAfterRead struct {
	cancel context.CancelCauseFunc
	cause  error
	body   []byte
	read   bool
}

func (reader *cancelAfterRead) Read(value []byte) (int, error) {
	if reader.read {
		return 0, io.EOF
	}
	reader.read = true
	written := copy(value, reader.body)
	reader.cancel(reader.cause)
	return written, nil
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) {
	return len(value) / 2, nil
}

type zeroReader struct{}

func (zeroReader) Read(value []byte) (int, error) {
	clear(value)
	return len(value), nil
}

func testFileMode(t *testing.T, value string) review.FileMode {
	t.Helper()
	mode, err := review.NewFileMode(value)
	if err != nil {
		t.Fatal(err)
	}
	return mode
}

func testWorktreeRecord(status byte, path string, mode review.FileMode) diffRecord {
	record := diffRecord{status: status, newMode: mode}
	if status == 'D' {
		record.oldPath = path
	} else {
		record.oldPath = path
		record.newPath = path
	}
	return record
}

func rawWorktreePaths(entries []rawWorktreeEntry) []string {
	values := make([]string, len(entries))
	for index, entry := range entries {
		values[index] = entry.path
	}
	return values
}

func leftPadDecimal(value, width int) string {
	digits := strconv.Itoa(value)
	return strings.Repeat("0", max(0, width-len(digits))) + digits
}

func runConflictingMerge(t *testing.T, root string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", "merge", "--no-ff", "--no-edit", "side")
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded:\n%s", output)
	}
}

func contentConflictRepository(t *testing.T, path string) string {
	t.Helper()
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, path), "base\n")
	runGitTest(t, root, "add", path)
	runGitTest(t, root, "commit", "-m", "base")
	mainBranch := strings.TrimSpace(runGitTest(t, root, "branch", "--show-current"))
	runGitTest(t, root, "checkout", "-b", "side")
	writeFile(t, filepath.Join(root, path), "side\n")
	runGitTest(t, root, "commit", "-am", "side")
	runGitTest(t, root, "checkout", mainBranch)
	writeFile(t, filepath.Join(root, path), "main\n")
	runGitTest(t, root, "commit", "-am", "main")
	runConflictingMerge(t, root)
	return root
}

func openWorktreeKinds(t *testing.T, root string) map[string]review.ChangeKind {
	t.Helper()
	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	got := make(map[string]review.ChangeKind)
	for file := range document.Snapshot().AllFiles() {
		got[file.DisplayPath()] = file.Change().Kind()
	}
	return got
}

func replaceSymlink(t *testing.T, path, target string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}
