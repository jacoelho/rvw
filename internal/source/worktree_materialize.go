package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/jacoelho/rvw/internal/review"
)

func (service *Service) hashRawWorktreeEntries(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	workspace string,
	entries []rawWorktreeEntry,
	runCPU CPUAdmission,
) (result []rawWorktreeEntry, returnErr error) {
	return service.hashRawWorktreeEntriesWithCPU(
		ctx,
		worktree,
		environment,
		workspace,
		entries,
		openRawWorktreePath,
		runCPU,
	)
}

func (service *Service) hashRawWorktreeEntriesWith(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	workspace string,
	entries []rawWorktreeEntry,
	openPath func(*os.Root, string, review.FileMode) (rawWorktreeCapture, error),
) (result []rawWorktreeEntry, returnErr error) {
	return service.hashRawWorktreeEntriesWithCPU(
		ctx, worktree, environment, workspace, entries, openPath, directCPUAdmission,
	)
}

func (service *Service) hashRawWorktreeEntriesWithCPU(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	workspace string,
	entries []rawWorktreeEntry,
	openPath func(*os.Root, string, review.FileMode) (rawWorktreeCapture, error),
	runCPU CPUAdmission,
) (result []rawWorktreeEntry, returnErr error) {
	staging := filepath.Join(workspace, "raw-blobs")
	if strings.ContainsAny(staging, "\r\n") {
		return nil, errors.New("raw blob staging path contains a line break")
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return nil, fmt.Errorf("create raw blob staging directory: %w", err)
	}
	stagingRoot, err := os.OpenRoot(staging)
	if err != nil {
		return nil, fmt.Errorf("open raw blob staging directory: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, stagingRoot.Close())
	}()
	batch := rawWorktreeBatch{}
	copyBuffer := make([]byte, rawWorktreeCopyBufferBytes)
	defer func() {
		returnErr = errors.Join(returnErr, cleanupStagedWorktreeBlobs(batch.blobs))
	}()
	for index := range entries {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		capture, err := openPath(
			worktree.filesystem,
			entries[index].path,
			entries[index].mode,
		)
		if err != nil {
			return nil, fmt.Errorf("read worktree path %q: %w", entries[index].path, err)
		}
		entries[index].mode = capture.mode
		if capture.kind == rawWorktreeGitlink {
			if closeErr := capture.close(); closeErr != nil {
				return nil, fmt.Errorf("close worktree path %q: %w", entries[index].path, closeErr)
			}
			continue
		}
		name := "blob-" + strconv.Itoa(index)
		filePath := filepath.Join(staging, name)
		inputBytes := len(filePath) + 1
		if inputBytes > rawWorktreeHashInputBytes {
			return nil, errors.Join(
				errors.New("raw blob staging path exceeds hash input batch capacity"),
				capture.close(),
			)
		}
		if !batch.accepts(capture.size, inputBytes) {
			if flushErr := service.flushRawWorktreeBatch(
				ctx,
				worktree,
				environment,
				entries,
				&batch,
				runCPU,
			); flushErr != nil {
				return nil, errors.Join(flushErr, capture.close())
			}
		}
		file, err := stagingRoot.OpenFile(
			name,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("create raw blob staging file: %w", err),
				capture.close(),
			)
		}
		batch.add(stagedWorktreeBlob{
			file:       filePath,
			mode:       capture.mode,
			entryIndex: index,
			size:       capture.size,
		})
		written, writeErr := capture.write(ctx, file, copyBuffer)
		closeErr := file.Close()
		verifyErr := capture.verify(written)
		captureCloseErr := capture.close()
		if err := errors.Join(writeErr, closeErr, verifyErr, captureCloseErr); err != nil {
			return nil, fmt.Errorf("read worktree path %q: %w", entries[index].path, err)
		}
	}
	if err := service.flushRawWorktreeBatch(
		ctx,
		worktree,
		environment,
		entries,
		&batch,
		runCPU,
	); err != nil {
		return nil, err
	}
	return entries, nil
}

func (batch *rawWorktreeBatch) accepts(rawBytes int64, inputBytes int) bool {
	if len(batch.blobs) == 0 {
		return inputBytes <= rawWorktreeHashInputBytes
	}
	return len(batch.blobs) < rawWorktreeBatchEntries &&
		batch.rawBytes <= rawWorktreeBatchBytes-rawBytes &&
		batch.inputBytes <= rawWorktreeHashInputBytes-inputBytes
}

func (batch *rawWorktreeBatch) add(blob stagedWorktreeBlob) {
	batch.blobs = append(batch.blobs, blob)
	batch.rawBytes += blob.size
	batch.inputBytes += len(blob.file) + 1
}

func (service *Service) flushRawWorktreeBatch(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	entries []rawWorktreeEntry,
	batch *rawWorktreeBatch,
	runCPU CPUAdmission,
) (returnErr error) {
	if len(batch.blobs) == 0 {
		return nil
	}
	defer func() {
		returnErr = errors.Join(returnErr, cleanupStagedWorktreeBlobs(batch.blobs))
		*batch = rawWorktreeBatch{}
	}()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	var input []byte
	if err := runCPU(ctx, func() error {
		input = make([]byte, 0, batch.inputBytes)
		for _, blob := range batch.blobs {
			input = append(input, blob.file...)
			input = append(input, '\n')
		}
		return nil
	}); err != nil {
		return err
	}
	result, err := service.runRootedGitWithInput(
		ctx,
		worktree,
		environment,
		[]string{"hash-object", "--no-filters", "-w", "--stdin-paths"},
		input,
		int64(len(batch.blobs))*80,
	)
	if err != nil {
		return fmt.Errorf("hash raw worktree blobs: %w", err)
	}
	var identities []review.ObjectID
	err = runCPU(ctx, func() error {
		var parseErr error
		identities, parseErr = parseRawWorktreeIdentities(result.Stdout, len(batch.blobs))
		return parseErr
	})
	if err != nil {
		return err
	}
	for index, identity := range identities {
		blob := batch.blobs[index]
		entry := &entries[blob.entryIndex]
		entry.oid = new(identity)
		if entry.mode != blob.mode {
			return fmt.Errorf("raw worktree mode changed while hashing path %q", entry.path)
		}
	}
	return nil
}

func cleanupStagedWorktreeBlobs(blobs []stagedWorktreeBlob) error {
	var cleanupErr error
	for _, blob := range blobs {
		if err := os.Remove(blob.file); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove raw blob staging file: %w", err))
		}
	}
	return cleanupErr
}

func parseRawWorktreeIdentities(output []byte, count int) ([]review.ObjectID, error) {
	identities := make([]review.ObjectID, 0, count)
	for index := range count {
		lineEnd := bytes.IndexByte(output, '\n')
		if lineEnd < 0 {
			return nil, fmt.Errorf(
				"git returned %d raw blob identities, want %d",
				index,
				count,
			)
		}
		identity, err := review.NewObjectID(string(output[:lineEnd]))
		if err != nil {
			return nil, fmt.Errorf("validate raw blob identity %d: %w", index+1, err)
		}
		identities = append(identities, identity)
		output = output[lineEnd+1:]
	}
	if len(output) != 0 {
		return nil, fmt.Errorf("git returned extra raw blob identities after %d", count)
	}
	return identities, nil
}

func openRawWorktreePath(
	root *os.Root,
	path string,
	indexMode review.FileMode,
) (rawWorktreeCapture, error) {
	return openRawWorktreePathWith(root, path, indexMode, openRawWorktreeRegular)
}

func openRawWorktreePathWith(
	root *os.Root,
	path string,
	indexMode review.FileMode,
	openRegular func(*os.Root, string) (*os.File, error),
) (rawWorktreeCapture, error) {
	before, err := root.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rawWorktreeCapture{}, errors.New("worktree path disappeared")
		}
		return rawWorktreeCapture{}, err
	}
	switch {
	case before.Mode().IsRegular():
		if before.Size() > maxWorktreeFileBytes {
			return rawWorktreeCapture{}, errors.New("worktree file exceeds raw byte limit")
		}
		file, err := openRegular(root, path)
		if err != nil {
			return rawWorktreeCapture{}, err
		}
		opened, err := file.Stat()
		if err != nil {
			return rawWorktreeCapture{}, errors.Join(err, file.Close())
		}
		if !opened.Mode().IsRegular() || !sameWorktreeFile(before, opened) {
			return rawWorktreeCapture{}, errors.Join(
				errors.New("worktree path changed while opening"),
				file.Close(),
			)
		}
		if opened.Size() > maxWorktreeFileBytes {
			return rawWorktreeCapture{}, errors.Join(
				errors.New("worktree file exceeds raw byte limit"),
				file.Close(),
			)
		}
		mode := "100644"
		if opened.Mode()&0o111 != 0 {
			mode = "100755"
		}
		if indexMode.String() == "100644" || indexMode.String() == "100755" {
			mode = indexMode.String()
		}
		validated, err := review.NewFileMode(mode)
		if err != nil {
			return rawWorktreeCapture{}, errors.Join(err, file.Close())
		}
		return rawWorktreeCapture{
			file: file,
			root: root,
			info: opened,
			path: path,
			mode: validated,
			size: opened.Size(),
			kind: rawWorktreeRegular,
		}, nil
	case before.Mode()&os.ModeSymlink != 0:
		link, err := root.Readlink(path)
		if err != nil {
			return rawWorktreeCapture{}, err
		}
		mode, err := review.NewFileMode("120000")
		if err != nil {
			return rawWorktreeCapture{}, err
		}
		return rawWorktreeCapture{
			root: root,
			info: before,
			path: path,
			link: link,
			mode: mode,
			size: int64(len(link)),
			kind: rawWorktreeSymlink,
		}, nil
	case before.IsDir() && indexMode.String() == "160000":
		return rawWorktreeCapture{
			root: root,
			info: before,
			path: path,
			mode: indexMode,
			kind: rawWorktreeGitlink,
		}, nil
	default:
		return rawWorktreeCapture{}, errors.New("worktree path has an unsupported file type")
	}
}

func openRawWorktreeRegular(root *os.Root, relative string) (*os.File, error) {
	parentPath, base := path.Split(relative)
	parentPath = strings.TrimSuffix(parentPath, "/")
	if parentPath == "" {
		parentPath = "."
	}
	parent, err := root.Open(parentPath)
	if err != nil {
		return nil, err
	}
	descriptor, err := unix.Openat(
		int(parent.Fd()),
		base,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, errors.Join(err, parent.Close())
	}
	file := os.NewFile(uintptr(descriptor), relative)
	if closeErr := parent.Close(); closeErr != nil {
		return nil, errors.Join(closeErr, file.Close())
	}
	return file, nil
}

func (capture rawWorktreeCapture) write(
	ctx context.Context,
	destination io.Writer,
	buffer []byte,
) (int64, error) {
	switch capture.kind {
	case rawWorktreeRegular:
		return copyBoundedWorktreeFile(
			ctx,
			destination,
			capture.file,
			buffer,
			maxWorktreeFileBytes,
		)
	case rawWorktreeSymlink:
		if err := context.Cause(ctx); err != nil {
			return 0, err
		}
		written, err := io.WriteString(destination, capture.link)
		if err == nil && written != len(capture.link) {
			err = io.ErrShortWrite
		}
		return int64(written), err
	default:
		return 0, errors.New("worktree capture cannot be staged")
	}
}

func (capture rawWorktreeCapture) verify(written int64) error {
	if written != capture.size {
		return errors.New("worktree path changed while reading")
	}
	current, err := capture.root.Lstat(capture.path)
	if err != nil {
		return errors.New("worktree path changed while reading")
	}
	switch capture.kind {
	case rawWorktreeRegular:
		after, err := capture.file.Stat()
		if err != nil || !sameWorktreeFile(capture.info, after) ||
			!sameWorktreeFile(capture.info, current) {
			return errors.New("worktree file changed while reading")
		}
	case rawWorktreeSymlink:
		if !sameWorktreeFile(capture.info, current) {
			return errors.New("worktree symlink changed while reading")
		}
	default:
		return errors.New("worktree capture cannot be verified")
	}
	return nil
}

func (capture rawWorktreeCapture) close() error {
	if capture.file == nil {
		return nil
	}
	return capture.file.Close()
}

func copyBoundedWorktreeFile(
	ctx context.Context,
	destination io.Writer,
	source io.Reader,
	buffer []byte,
	limit int64,
) (int64, error) {
	if len(buffer) == 0 || limit < 0 {
		return 0, errors.New("worktree copy has an invalid bound")
	}
	var written int64
	emptyReads := 0
	for written <= limit {
		if err := context.Cause(ctx); err != nil {
			return written, err
		}
		remaining := limit - written + 1
		readBuffer := buffer
		if int64(len(readBuffer)) > remaining {
			readBuffer = readBuffer[:remaining]
		}
		count, readErr := source.Read(readBuffer)
		if count < 0 || count > len(readBuffer) {
			return written, errors.New("worktree file reader returned an invalid count")
		}
		if count > 0 {
			emptyReads = 0
			if written+int64(count) > limit {
				return written, errors.New("worktree file exceeds raw byte limit")
			}
			countWritten, writeErr := destination.Write(readBuffer[:count])
			written += int64(countWritten)
			if writeErr != nil {
				return written, writeErr
			}
			if countWritten != count {
				return written, io.ErrShortWrite
			}
		} else {
			emptyReads++
			if emptyReads >= 100 {
				return written, io.ErrNoProgress
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
	return written, errors.New("worktree file exceeds raw byte limit")
}

func sameWorktreeFile(left, right os.FileInfo) bool {
	return right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() &&
		left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime())
}
