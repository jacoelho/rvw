package source

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/storer"
	gitstorage "github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
	"golang.org/x/sys/unix"
)

type repositorySession struct { //nolint:govet // Fields are grouped by lifecycle ownership and then repository state.
	closeErr     error
	filesystem   *os.Root
	gitdirRoot   *os.Root
	commonRoot   *os.Root
	storage      *gitstorage.Storage
	objects      storer.EncodedObjectStorer
	objectLayers *layeredObjectStorage
	alternates   []*nativeAlternateLayer
	packLayers   []*repositoryPackLayer
	contents     map[plumbing.Hash]spooledContent
	contentBytes uint64
	contentLimit uint64
	config       *gitConfiguration
	options      ServiceOptions
	attributes   nativeAttributeCache
	directory    *os.File
	workspace    *os.File
	path         RepositoryPath
	cleanup      string
	closed       bool
}

func openRepositorySession(path RepositoryPath, serviceOptions ...ServiceOptions) (*repositorySession, error) {
	var options ServiceOptions
	if len(serviceOptions) > 1 {
		return nil, errors.New("repository session accepts at most one service options value")
	}
	if len(serviceOptions) == 1 {
		var err error
		options, err = serviceOptions[0].clone()
		if err != nil {
			return nil, err
		}
	}
	filesystem, err := os.OpenRoot(path.String())
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	directory, err := filesystem.Open(".")
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open repository root descriptor: %w", err),
			filesystem.Close(),
		)
	}
	gitdirPath, commonPath, err := resolveRepositoryLayout(path.String())
	if err != nil {
		return nil, errors.Join(err, directory.Close(), filesystem.Close())
	}
	gitdirRoot, err := os.OpenRoot(gitdirPath)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open Git directory root: %w", err),
			directory.Close(),
			filesystem.Close(),
		)
	}
	commonRoot, err := os.OpenRoot(commonPath)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open common Git directory root: %w", err),
			gitdirRoot.Close(),
			directory.Close(),
			filesystem.Close(),
		)
	}
	config, err := loadGitConfiguration(gitdirPath, commonPath, options)
	if err != nil {
		return nil, errors.Join(
			err,
			commonRoot.Close(),
			gitdirRoot.Close(),
			directory.Close(),
			filesystem.Close(),
		)
	}
	storage := newRepositoryStorage(gitdirRoot, commonRoot, repositoryStorageReadOnly)
	alternates, err := loadNativeAlternateLayers(filepath.Join(commonPath, "objects"))
	if err != nil {
		return nil, errors.Join(
			err,
			storage.Close(),
			commonRoot.Close(),
			gitdirRoot.Close(),
			directory.Close(),
			filesystem.Close(),
		)
	}
	layers := make([]storer.EncodedObjectStorer, 0, len(alternates))
	for _, alternate := range alternates {
		layers = append(layers, alternate.storage)
	}
	objectLayers := &layeredObjectStorage{primary: storage, layers: layers}
	return &repositorySession{
		filesystem:   filesystem,
		gitdirRoot:   gitdirRoot,
		commonRoot:   commonRoot,
		storage:      storage,
		objects:      objectLayers,
		objectLayers: objectLayers,
		alternates:   alternates,
		contents:     make(map[plumbing.Hash]spooledContent),
		contentLimit: 32 << 30,
		config:       config,
		options:      options,
		directory:    directory,
		path:         path,
	}, nil
}

type repositoryStorageAccess uint8

const (
	repositoryStorageReadOnly repositoryStorageAccess = iota + 1
	repositoryStorageWritable
)

func newRepositoryStorage(
	gitdirRoot, commonRoot *os.Root,
	access repositoryStorageAccess,
) *gitstorage.Storage {
	gitdirFilesystem := newRootedReadOnlyFilesystem(gitdirRoot)
	commonFilesystem := newRootedReadOnlyFilesystem(commonRoot)
	if access == repositoryStorageWritable {
		gitdirFilesystem = newRootedWritableFilesystem(gitdirRoot)
		commonFilesystem = newRootedWritableFilesystem(commonRoot)
	}
	repositoryFilesystem := dotgit.NewRepositoryFilesystem(
		gitdirFilesystem,
		commonFilesystem,
	)
	return gitstorage.NewStorageWithOptions(
		repositoryFilesystem,
		cache.NewObjectLRU(4*cache.MiByte),
		gitstorage.Options{MaxOpenDescriptors: 64, LargeObjectThreshold: 1 << 20},
	)
}

func resolveRepositoryLayout(root string) (string, string, error) {
	marker := filepath.Join(root, ".git")
	info, err := os.Lstat(marker)
	gitdir := marker
	switch {
	case err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0:
	case err == nil && info.Mode().IsRegular():
		contents, readErr := readStableRegularFile(marker, maxGitDirectoryMarkerBytes)
		if readErr != nil {
			return "", "", readErr
		}
		gitdir, readErr = parseGitDirectoryPath(root, string(contents))
		if readErr != nil {
			return "", "", readErr
		}
	case errors.Is(err, os.ErrNotExist):
		head, headErr := os.Lstat(filepath.Join(root, "HEAD"))
		objects, objectsErr := os.Lstat(filepath.Join(root, "objects"))
		if headErr != nil || objectsErr != nil || !head.Mode().IsRegular() || !objects.IsDir() {
			return "", "", errors.New("repository has no valid Git directory")
		}
		gitdir = root
	case err != nil:
		return "", "", fmt.Errorf("inspect Git directory: %w", err)
	default:
		return "", "", errors.New("git directory marker must be a directory or regular file")
	}
	gitdir = filepath.Clean(gitdir)
	gitdirInfo, err := os.Lstat(gitdir)
	if err != nil || !gitdirInfo.IsDir() || gitdirInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("git directory target must be a directory")
	}
	common := gitdir
	commondirPath := filepath.Join(gitdir, "commondir")
	if _, commondirErr := os.Lstat(commondirPath); commondirErr == nil {
		contents, readErr := readStableRegularFile(commondirPath, maxGitDirectoryMarkerBytes)
		if readErr != nil {
			return "", "", readErr
		}
		value := strings.TrimSuffix(strings.TrimSuffix(string(contents), "\n"), "\r")
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return "", "", errors.New("commondir is malformed")
		}
		if filepath.IsAbs(value) {
			common = filepath.Clean(value)
		} else {
			common = filepath.Clean(filepath.Join(gitdir, value))
		}
	} else if !errors.Is(commondirErr, os.ErrNotExist) {
		return "", "", fmt.Errorf("inspect commondir: %w", commondirErr)
	}
	commonInfo, err := os.Lstat(common)
	if err != nil || !commonInfo.IsDir() || commonInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("common Git directory must be a directory")
	}
	return gitdir, common, nil
}

func readStableRegularFile(path string, limit int64) (contents []byte, returnErr error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open stable regular file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd) //nolint:errcheck // No os.File owns the descriptor on this construction failure.
		return nil, errors.New("open stable regular file descriptor")
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	return readStableRegularDescriptor(file, limit)
}

func readStableRootFile(root *os.Root, name string, limit int64) (contents []byte, returnErr error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("rooted file must be a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	contents, err = readStableRegularDescriptor(file, limit)
	if err != nil {
		return nil, err
	}
	after, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return nil, fmt.Errorf("%w: rooted file changed during read", ErrSourceStale)
	}
	return contents, nil
}

func readStableRegularDescriptor(file *os.File, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, errors.New("stable file limit is negative")
	}
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("stable file is not regular")
	}
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, &LimitError{Resource: LimitConfigBytes, Limit: uint64(limit)}
	}
	if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
		return nil, seekErr
	}
	digest := sha256.Sum256(contents)
	secondDigest := sha256.New()
	written, err := io.Copy(secondDigest, io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if written != int64(len(contents)) || !bytes.Equal(digest[:], secondDigest.Sum(nil)) {
		return nil, fmt.Errorf("%w: file contents changed during read", ErrSourceStale)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return nil, fmt.Errorf("%w: file identity changed during read", ErrSourceStale)
	}
	return contents, nil
}

func parseGitDirectoryPath(root, contents string) (string, error) {
	if strings.IndexByte(contents, 0) >= 0 {
		return "", errors.New("git directory marker contains NUL")
	}
	contents = strings.TrimSuffix(contents, "\n")
	contents = strings.TrimSuffix(contents, "\r")
	if strings.ContainsAny(contents, "\r\n") || !strings.HasPrefix(contents, "gitdir: ") {
		return "", errors.New("git directory marker is malformed")
	}
	path := strings.TrimPrefix(contents, "gitdir: ")
	if path == "" {
		return "", errors.New("git directory marker has no target")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return filepath.Clean(path), nil
}

func (repository *repositorySession) Close() error {
	if repository == nil {
		return nil
	}
	if repository.closed {
		return repository.closeErr
	}
	repository.closed = true
	var storageErr error
	if repository.storage != nil {
		storageErr = repository.storage.Close()
	}
	repository.closeErr = errors.Join(
		storageErr,
		closeRepositoryPackLayers(repository.packLayers),
		closeNativeAlternateLayers(repository.alternates),
		closeRepositoryFile(repository.workspace),
		closeRepositoryFile(repository.directory),
		closeRepositoryRoot(repository.commonRoot),
		closeRepositoryRoot(repository.gitdirRoot),
		closeRepositoryRoot(repository.filesystem),
	)
	if repository.cleanup != "" {
		repository.closeErr = errors.Join(repository.closeErr, os.RemoveAll(repository.cleanup))
	}
	return repository.closeErr
}

func closeRepositoryFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func closeRepositoryRoot(root *os.Root) error {
	if root == nil {
		return nil
	}
	return root.Close()
}
