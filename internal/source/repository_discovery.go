package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	maxRepositoryDiscoveryDepth = 4_096
	maxGitDirectoryMarkerBytes  = 64 << 10
)

func discoverRepositoryRoot(ctx context.Context, start WorkingDirectory) (RepositoryPath, *os.File, error) {
	directory, err := os.Open(start.String())
	if err != nil {
		return "", nil, fmt.Errorf("open working directory: %w", err)
	}
	defer func() {
		if directory != nil {
			_ = directory.Close() //nolint:errcheck // Every replaced descriptor is checked; this is final read-only cleanup.
		}
	}()

	for depth := uint64(0); ; depth++ {
		if err := context.Cause(ctx); err != nil {
			return "", nil, err
		}
		if depth > maxRepositoryDiscoveryDepth {
			return "", nil, &LimitError{
				Resource: LimitWorktreeDepth,
				Limit:    maxRepositoryDiscoveryDepth,
			}
		}
		found, markerErr := directoryHasGitMarker(directory)
		if markerErr != nil {
			return "", nil, markerErr
		}
		if found {
			path, pathErr := repositoryDescriptorRealPath(directory)
			if pathErr != nil {
				return "", nil, pathErr
			}
			root, pathErr := NewRepositoryPath(path)
			if pathErr != nil {
				return "", nil, pathErr
			}
			owner := directory
			directory = nil
			return root, owner, nil
		}

		parent, parentErr := openParentDirectory(directory)
		if parentErr != nil {
			return "", nil, parentErr
		}
		same, identityErr := sameDirectory(directory, parent)
		if identityErr != nil {
			return "", nil, errors.Join(identityErr, parent.Close())
		}
		if same {
			return "", nil, errors.Join(
				errors.New("working directory is not inside a Git repository"), parent.Close(),
			)
		}
		if closeErr := directory.Close(); closeErr != nil {
			return "", nil, errors.Join(
				fmt.Errorf("close repository discovery directory: %w", closeErr), parent.Close(),
			)
		}
		directory = parent
	}
}

func directoryHasGitMarker(directory *os.File) (found bool, returnErr error) {
	fd, err := unix.Openat(
		int(directory.Fd()),
		".git",
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		if errors.Is(err, unix.ELOOP) {
			return false, errors.New("git directory marker must not be a symbolic link")
		}
		return false, fmt.Errorf("open Git directory marker: %w", err)
	}
	marker := os.NewFile(uintptr(fd), ".git")
	if marker == nil {
		_ = unix.Close(fd) //nolint:errcheck // No os.File owns the descriptor on this construction failure.
		return false, errors.New("open Git directory marker descriptor")
	}
	defer func() { returnErr = errors.Join(returnErr, marker.Close()) }()

	before, err := marker.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect Git directory marker: %w", err)
	}
	if before.IsDir() {
		return true, nil
	}
	if !before.Mode().IsRegular() {
		return false, errors.New("git directory marker must be a directory or regular file")
	}
	contents, err := io.ReadAll(io.LimitReader(marker, maxGitDirectoryMarkerBytes+1))
	if err != nil {
		return false, fmt.Errorf("read Git directory marker: %w", err)
	}
	if len(contents) > maxGitDirectoryMarkerBytes {
		return false, &LimitError{Resource: LimitConfigBytes, Limit: maxGitDirectoryMarkerBytes}
	}
	after, err := marker.Stat()
	if err != nil {
		return false, fmt.Errorf("reinspect Git directory marker: %w", err)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return false, fmt.Errorf("%w: Git directory marker changed during discovery", ErrSourceStale)
	}
	return validGitDirectoryFile(directory, string(contents))
}

func validGitDirectoryFile(directory *os.File, contents string) (bool, error) {
	if strings.IndexByte(contents, 0) >= 0 {
		return false, errors.New("git directory marker contains NUL")
	}
	contents = strings.TrimSuffix(contents, "\n")
	contents = strings.TrimSuffix(contents, "\r")
	if strings.ContainsAny(contents, "\r\n") || !strings.HasPrefix(contents, "gitdir: ") {
		return false, errors.New("git directory marker is malformed")
	}
	path := strings.TrimPrefix(contents, "gitdir: ")
	if path == "" {
		return false, errors.New("git directory marker has no target")
	}
	if !filepath.IsAbs(path) {
		root, err := repositoryDescriptorRealPath(directory)
		if err != nil {
			return false, err
		}
		path = filepath.Join(root, path)
	}
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		return false, fmt.Errorf("inspect Git directory marker target: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("git directory marker target must be a directory")
	}
	return true, nil
}

func openParentDirectory(directory *os.File) (*os.File, error) {
	fd, err := unix.Openat(
		int(directory.Fd()),
		"..",
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open parent during repository discovery: %w", err)
	}
	parent := os.NewFile(uintptr(fd), "..")
	if parent == nil {
		_ = unix.Close(fd) //nolint:errcheck // No os.File owns the descriptor on this construction failure.
		return nil, errors.New("open parent repository descriptor")
	}
	return parent, nil
}

func sameDirectory(first, second *os.File) (bool, error) {
	firstInfo, err := first.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect repository discovery directory: %w", err)
	}
	secondInfo, err := second.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect repository discovery parent: %w", err)
	}
	return os.SameFile(firstInfo, secondInfo), nil
}
