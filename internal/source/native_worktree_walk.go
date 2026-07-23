package source

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

const nativeDirectoryReadBatch = 256

type nativeDirectoryQueue struct {
	file        *os.File
	path        string
	readOffset  int64
	writeOffset int64
}

func newNativeDirectoryQueue(workspace string) (*nativeDirectoryQueue, error) {
	file, err := os.CreateTemp(workspace, "worktree-directories-")
	if err != nil {
		return nil, err
	}
	return &nativeDirectoryQueue{file: file, path: file.Name()}, nil
}

func (queue *nativeDirectoryQueue) push(name string, depth uint32) error {
	if len(name) > int(^uint32(0)) {
		return errors.New("worktree directory path exceeds queue encoding")
	}
	var header [8]byte
	binary.BigEndian.PutUint32(header[0:4], depth)
	binary.BigEndian.PutUint32(header[4:8], uint32(len(name))) //nolint:gosec // The length bound above is exact.
	if _, err := queue.file.WriteAt(header[:], queue.writeOffset); err != nil {
		return err
	}
	queue.writeOffset += int64(len(header))
	if _, err := queue.file.WriteAt([]byte(name), queue.writeOffset); err != nil {
		return err
	}
	queue.writeOffset += int64(len(name))
	return nil
}

func (queue *nativeDirectoryQueue) pop() (string, uint32, bool, error) {
	if queue.readOffset == queue.writeOffset {
		return "", 0, false, nil
	}
	var header [8]byte
	if _, err := queue.file.ReadAt(header[:], queue.readOffset); err != nil {
		return "", 0, false, err
	}
	queue.readOffset += int64(len(header))
	length := binary.BigEndian.Uint32(header[4:8])
	value := make([]byte, length)
	if _, err := queue.file.ReadAt(value, queue.readOffset); err != nil {
		return "", 0, false, err
	}
	queue.readOffset += int64(length)
	return string(value), binary.BigEndian.Uint32(header[0:4]), true, nil
}

func (queue *nativeDirectoryQueue) Close() error {
	if queue == nil {
		return nil
	}
	err := queue.file.Close()
	if removeErr := os.Remove(queue.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		err = errors.Join(err, removeErr)
	}
	*queue = nativeDirectoryQueue{}
	return err
}

type nativeWorktreeVisitor func(relative string, entry os.DirEntry, depth uint32) (bool, error)

//nolint:gocognit // Descriptor traversal, queue admission, and stability checks form one ownership pass.
func walkNativeWorktree(
	ctx context.Context,
	session *repositorySession,
	visit nativeWorktreeVisitor,
) (returnErr error) {
	if session == nil || session.directory == nil || session.filesystem == nil || visit == nil {
		return errors.New("native worktree traversal has invalid ownership")
	}
	workspace, err := session.ensureWorkspace()
	if err != nil {
		return err
	}
	queue, err := newNativeDirectoryQueue(workspace)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, queue.Close()) }()
	if err := queue.push("", 0); err != nil {
		return err
	}
	var visited, pathBytes uint64
	for {
		directory, depth, present, popErr := queue.pop()
		if popErr != nil || !present {
			return popErr
		}
		if cancelErr := context.Cause(ctx); cancelErr != nil {
			return cancelErr
		}
		opened, before, err := openStableWorktreeDirectory(session, directory)
		if err != nil {
			return err
		}
		for {
			entries, readErr := opened.ReadDir(nativeDirectoryReadBatch)
			for _, entry := range entries {
				if err := context.Cause(ctx); err != nil {
					return errors.Join(err, opened.Close())
				}
				if err := validateWorktreeEntryName(entry.Name()); err != nil {
					return errors.Join(err, opened.Close())
				}
				relative := entry.Name()
				if directory != "" {
					relative = directory + "/" + relative
				}
				visited++
				pathBytes += uint64(len(relative))
				if visited > 2_000_000 {
					return errors.Join(&LimitError{Resource: LimitWorktreeEntries, Limit: 2_000_000}, opened.Close())
				}
				if pathBytes > 512<<20 {
					return errors.Join(&LimitError{Resource: LimitWorktreePaths, Limit: 512 << 20}, opened.Close())
				}
				entryDepth := depth + 1
				if entryDepth > 4_096 {
					return errors.Join(&LimitError{Resource: LimitWorktreeDepth, Limit: 4_096}, opened.Close())
				}
				descend, visitErr := visit(relative, entry, entryDepth)
				if visitErr != nil {
					return errors.Join(visitErr, opened.Close())
				}
				if entry.IsDir() && descend {
					if err := queue.push(relative, entryDepth); err != nil {
						return errors.Join(err, opened.Close())
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return errors.Join(readErr, opened.Close())
			}
			if len(entries) == 0 {
				return errors.Join(io.ErrNoProgress, opened.Close())
			}
		}
		if err := verifyStableWorktreeDirectory(session, directory, opened, before); err != nil {
			return err
		}
	}
}

func openStableWorktreeDirectory(
	session *repositorySession,
	relative string,
) (*os.File, os.FileInfo, error) {
	name := relative
	if name == "" {
		name = "."
	}
	before, err := session.filesystem.Lstat(name)
	if err != nil || !before.IsDir() {
		return nil, nil, errors.Join(ErrSourceStale, err)
	}
	descriptor, err := unix.Dup(int(session.directory.Fd()))
	if err != nil {
		return nil, nil, err
	}
	current := os.NewFile(uintptr(descriptor), ".")
	for component := range strings.SplitSeq(relative, "/") {
		if component == "" {
			continue
		}
		nextDescriptor, openErr := unix.Openat(
			int(current.Fd()), component,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY,
			0,
		)
		if openErr != nil {
			return nil, nil, errors.Join(ErrSourceStale, openErr, current.Close())
		}
		next := os.NewFile(uintptr(nextDescriptor), component)
		if closeErr := current.Close(); closeErr != nil {
			return nil, nil, errors.Join(closeErr, next.Close())
		}
		current = next
	}
	opened, err := current.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return nil, nil, errors.Join(ErrSourceStale, err, current.Close())
	}
	return current, before, nil
}

func verifyStableWorktreeDirectory(
	session *repositorySession,
	relative string,
	directory *os.File,
	before os.FileInfo,
) error {
	name := relative
	if name == "" {
		name = "."
	}
	after, pathErr := session.filesystem.Lstat(name)
	opened, descriptorErr := directory.Stat()
	closeErr := directory.Close()
	if pathErr != nil || descriptorErr != nil || !after.IsDir() || !opened.IsDir() ||
		!os.SameFile(before, after) || !os.SameFile(before, opened) {
		return errors.Join(ErrSourceStale, pathErr, descriptorErr, closeErr)
	}
	return closeErr
}

func validateWorktreeEntryName(name string) error {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") ||
		strings.IndexByte(name, 0) >= 0 || path.Clean(name) != name {
		return errors.New("worktree directory contains an invalid entry name")
	}
	return nil
}
