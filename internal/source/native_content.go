package source

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // Git SHA-1 object identity is the repository contract.
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/jacoelho/rvw/internal/review"
)

type spooledContent struct {
	path string
	size int64
}

func (session *repositorySession) pinObjectContent(
	ctx context.Context,
	hash plumbing.Hash,
) error {
	if _, exists := session.contents[hash]; exists {
		return nil
	}
	encoded, err := session.objects.EncodedObject(plumbing.BlobObject, hash)
	if err != nil {
		return err
	}
	if encoded.Size() < 0 || session.contentBytes > session.contentLimit ||
		uint64(encoded.Size()) > session.contentLimit-session.contentBytes { //nolint:gosec // negative sizes are rejected first.
		return &LimitError{Resource: LimitDiffInputBytes, Limit: session.contentLimit}
	}
	reader, err := encoded.Reader()
	if err != nil {
		return err
	}
	defer reader.Close() //nolint:errcheck // The fully consumed immutable-object reader has no commit-on-close behavior.
	workspace, err := session.ensureWorkspace()
	if err != nil {
		return err
	}
	directory := filepath.Join(workspace, "content")
	if mkdirErr := os.Mkdir(directory, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
		return mkdirErr
	}
	path := filepath.Join(directory, hash.String())
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is inside the private content workspace.
	if errors.Is(err, os.ErrExist) {
		return errors.New("content spool exists without a published identity")
	}
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = file.Close() //nolint:errcheck // The normal path closes and checks the spool before this cleanup fallback.
		if !keep {
			_ = os.Remove(path) //nolint:errcheck // Best-effort removal follows every reported construction failure.
		}
	}()
	written, err := copyIndexWithCancellation(ctx, file, io.LimitReader(reader, encoded.Size()+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if written != encoded.Size() {
		return errors.New("git blob size changed while pinning")
	}
	actual, err := hashSpooledBlob(ctx, path, written)
	if err != nil {
		return err
	}
	if actual != hash {
		return errors.New("git blob content does not match its object ID")
	}
	session.contents[hash] = spooledContent{path: path, size: written}
	session.contentBytes += uint64(written) //nolint:gosec // written equals the validated non-negative object size above.
	keep = true
	return nil
}

//nolint:funlen,maintidx // Capture keeps descriptor verification, conversion, hashing, and publication in one ownership scope.
func (session *repositorySession) captureWorktreeContent(
	ctx context.Context,
	path string,
	indexMode review.FileMode,
) (plumbing.Hash, review.FileMode, bool, error) {
	if err := context.Cause(ctx); err != nil {
		return plumbing.ZeroHash, "", false, err
	}
	info, err := session.filesystem.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return plumbing.ZeroHash, "", false, nil
	}
	if err != nil {
		return plumbing.ZeroHash, "", false, err
	}
	if info.IsDir() && indexMode.String() == "160000" {
		childPath, pathErr := NewRepositoryPath(filepath.Join(session.path.String(), filepath.FromSlash(path)))
		if pathErr != nil {
			return plumbing.ZeroHash, "", false, pathErr
		}
		child, childErr := openRepositorySession(childPath, session.options)
		if childErr != nil {
			return plumbing.ZeroHash, "", false, fmt.Errorf("open tracked embedded repository %q: %w", path, childErr)
		}
		defer child.Close() //nolint:errcheck // Read-only child close cannot change the captured commit identity.
		childHash, resolveErr := child.resolveCommitish(ctx, RevisionRef("HEAD"))
		if resolveErr != nil {
			return plumbing.ZeroHash, "", false, fmt.Errorf("resolve tracked embedded repository %q: %w", path, resolveErr)
		}
		return childHash, indexMode, true, nil
	}
	conversion, err := session.nativeConversionForPath(ctx, path)
	if err != nil {
		return plumbing.ZeroHash, "", false, err
	}
	trustExecutableBit := true
	if configured, ok := session.config.value("core", "", "filemode"); ok {
		trustExecutableBit, err = parseGitBoolean(configured)
		if err != nil {
			return plumbing.ZeroHash, "", false, fmt.Errorf("core.fileMode: %w", err)
		}
	}
	capture, err := openRawWorktreePath(
		session.filesystem,
		path,
		indexMode,
		rawWorktreeOptions{trustExecutableBit: trustExecutableBit},
	)
	if err != nil {
		return plumbing.ZeroHash, "", false, err
	}
	defer capture.close() //nolint:errcheck // capture.verify reports descriptor instability; close has no commit behavior.
	if capture.kind == rawWorktreeGitlink {
		return plumbing.ZeroHash, "", false, errors.New("tracked Gitlink was not resolved")
	}
	workspace, err := session.ensureWorkspace()
	if err != nil {
		return plumbing.ZeroHash, "", false, err
	}
	directory := filepath.Join(workspace, "content")
	if mkdirErr := os.Mkdir(directory, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
		return plumbing.ZeroHash, "", false, fmt.Errorf("create content workspace: %w", mkdirErr)
	}
	temporary, err := os.CreateTemp(directory, "capture-")
	if err != nil {
		return plumbing.ZeroHash, "", false, fmt.Errorf("create content spool: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close() //nolint:errcheck // The normal path closes and checks the spool before this cleanup fallback.
		if !keep {
			_ = os.Remove(temporaryPath) //nolint:errcheck // Best-effort removal follows every reported construction failure.
		}
	}()
	rawBytes, copyErr := capture.write(ctx, temporary, make([]byte, 64<<10))
	closeErr := temporary.Close()
	verifyErr := capture.verify(rawBytes)
	if captureErr := errors.Join(copyErr, closeErr, verifyErr); captureErr != nil {
		return plumbing.ZeroHash, "", false, captureErr
	}
	written := rawBytes
	if conversion.normalizeEOL || conversion.ident {
		converted, createErr := os.CreateTemp(directory, "converted-")
		if createErr != nil {
			return plumbing.ZeroHash, "", false, createErr
		}
		convertedPath := converted.Name()
		convertedKeep := false
		defer func() {
			_ = converted.Close() //nolint:errcheck // Normal-path close is checked before publication.
			if !convertedKeep {
				_ = os.Remove(convertedPath) //nolint:errcheck // Best-effort cleanup follows a reported error.
			}
		}()
		written, err = streamNativeConversion(ctx, temporaryPath, converted, conversion)
		if convertedCloseErr := converted.Close(); err == nil {
			err = convertedCloseErr
		}
		if err != nil {
			return plumbing.ZeroHash, "", false, err
		}
		if removeErr := os.Remove(temporaryPath); removeErr != nil {
			return plumbing.ZeroHash, "", false, removeErr
		}
		temporary = converted
		temporaryPath = convertedPath
		convertedKeep = true
	}
	if written < 0 {
		return plumbing.ZeroHash, "", false, errors.New("captured worktree byte count is negative")
	}
	hash, err := hashSpooledBlob(ctx, temporaryPath, written)
	if err != nil {
		return plumbing.ZeroHash, "", false, err
	}
	finalPath := filepath.Join(directory, hash.String())
	if _, exists := session.contents[hash]; !exists {
		if session.contentBytes > session.contentLimit ||
			uint64(written) > session.contentLimit-session.contentBytes {
			return plumbing.ZeroHash, "", false, &LimitError{
				Resource: LimitDiffInputBytes, Limit: session.contentLimit,
			}
		}
		if err := os.Rename(temporaryPath, finalPath); err != nil {
			return plumbing.ZeroHash, "", false, fmt.Errorf("publish content spool: %w", err)
		}
		session.contents[hash] = spooledContent{path: finalPath, size: written}
		session.contentBytes += uint64(written)
		keep = true
	}
	return hash, capture.mode, true, nil
}

func hashSpooledBlob(ctx context.Context, path string, size int64) (plumbing.Hash, error) {
	file, err := os.Open(path) //nolint:gosec // path is a private owner-published content spool.
	if err != nil {
		return plumbing.ZeroHash, err
	}
	defer file.Close()   //nolint:errcheck // The read-only spool has no commit-on-close behavior.
	digest := sha1.New() //nolint:gosec // Git SHA-1 object identity is required.
	if _, err := io.WriteString(digest, "blob "+strconv.FormatInt(size, 10)+"\x00"); err != nil {
		return plumbing.ZeroHash, err
	}
	buffer := make([]byte, 16<<10)
	var read int64
	for {
		if err := context.Cause(ctx); err != nil {
			return plumbing.ZeroHash, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			read += int64(count)
			if _, err := digest.Write(buffer[:count]); err != nil {
				return plumbing.ZeroHash, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return plumbing.ZeroHash, readErr
		}
		if count == 0 {
			return plumbing.ZeroHash, io.ErrNoProgress
		}
	}
	if read != size {
		return plumbing.ZeroHash, fmt.Errorf("content spool size changed: read %d, want %d", read, size)
	}
	var hash plumbing.Hash
	copy(hash[:], digest.Sum(nil))
	return hash, nil
}

func (session *repositorySession) resolveCommitish(
	ctx context.Context,
	value RevisionRef,
) (plumbing.Hash, error) {
	hash, suffix, err := session.resolveRevisionBase(value.String())
	if err != nil {
		return plumbing.ZeroHash, err
	}
	objectType, hash, err := session.peelTags(ctx, hash)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if suffix != "" {
		return plumbing.ZeroHash, errors.New("embedded repository HEAD has unsupported ancestry syntax")
	}
	if objectType != plumbing.CommitObject {
		return plumbing.ZeroHash, errors.New("revision does not resolve to a commit")
	}
	return hash, nil
}

func (session *repositorySession) openContent(
	hash plumbing.Hash,
) (io.ReadCloser, int64, error) {
	if content, ok := session.contents[hash]; ok {
		file, err := os.Open(content.path)
		return file, content.size, err
	}
	encoded, err := session.objects.EncodedObject(plumbing.BlobObject, hash)
	if err != nil {
		return nil, 0, err
	}
	reader, err := encoded.Reader()
	if err != nil {
		return nil, 0, err
	}
	return reader, encoded.Size(), nil
}

func (session *repositorySession) readContent(
	ctx context.Context,
	hash plumbing.Hash,
	limit int64,
) ([]byte, error) {
	if limit < 0 {
		return nil, errors.New("content limit is negative")
	}
	reader, size, err := session.openContent(hash)
	if err != nil {
		return nil, err
	}
	defer reader.Close() //nolint:errcheck // The immutable content reader has no commit-on-close behavior.
	if size < 0 || size > limit {
		return nil, &LimitError{Resource: LimitDiffInputBytes, Limit: uint64(limit)}
	}
	contents := make([]byte, 0, size)
	buffered := bufio.NewReaderSize(reader, 16<<10)
	buffer := make([]byte, 16<<10)
	for int64(len(contents)) <= limit {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		count, readErr := buffered.Read(buffer)
		if count > 0 {
			if int64(len(contents)+count) > limit {
				return nil, &LimitError{Resource: LimitDiffInputBytes, Limit: uint64(limit)}
			}
			contents = append(contents, buffer[:count]...)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return contents, nil
			}
			return nil, readErr
		}
		if count == 0 {
			return nil, io.ErrNoProgress
		}
	}
	return nil, &LimitError{Resource: LimitDiffInputBytes, Limit: uint64(limit)}
}
