package source

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	billy "github.com/go-git/go-billy/v5"
)

type rootedFilesystem struct {
	root           *os.Root
	prefix         string
	writable       bool
	objectsAtRoot  bool
	hideAlternates bool
}

var (
	_ billy.Filesystem = (*rootedFilesystem)(nil)
	_ billy.Capable    = (*rootedFilesystem)(nil)
	_ billy.File       = (*rootedFile)(nil)
)

func newRootedReadOnlyFilesystem(root *os.Root) billy.Filesystem { //nolint:ireturn // Billy requires its filesystem interface.
	return &rootedFilesystem{root: root, hideAlternates: true}
}

func newRootedWritableFilesystem(root *os.Root) billy.Filesystem { //nolint:ireturn // Billy requires its filesystem interface.
	return &rootedFilesystem{root: root, writable: true, hideAlternates: true}
}

func (filesystem *rootedFilesystem) Create(name string) (billy.File, error) { //nolint:ireturn // Billy's method contract returns billy.File.
	return filesystem.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
}

func (filesystem *rootedFilesystem) Open(name string) (billy.File, error) { //nolint:ireturn // Billy's method contract returns billy.File.
	return filesystem.OpenFile(name, os.O_RDONLY, 0)
}

//nolint:ireturn // Billy's method contract returns billy.File.
func (filesystem *rootedFilesystem) OpenFile(
	name string,
	flag int,
	perm os.FileMode,
) (billy.File, error) {
	write := flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_EXCL|os.O_TRUNC) != 0
	if write && !filesystem.writable {
		return nil, fs.ErrPermission
	}
	path, err := filesystem.resolve(name)
	if err != nil {
		return nil, err
	}
	file, err := filesystem.root.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return &rootedFile{File: file, name: path, writable: write}, nil
}

func (filesystem *rootedFilesystem) Stat(name string) (os.FileInfo, error) {
	path, err := filesystem.resolve(name)
	if err != nil {
		return nil, err
	}
	return filesystem.root.Stat(path)
}

func (filesystem *rootedFilesystem) Rename(oldName, newName string) error {
	if !filesystem.writable {
		return fs.ErrPermission
	}
	oldPath, err := filesystem.resolve(oldName)
	if err != nil {
		return err
	}
	newPath, err := filesystem.resolve(newName)
	if err != nil {
		return err
	}
	return filesystem.root.Rename(oldPath, newPath)
}

func (filesystem *rootedFilesystem) Remove(name string) error {
	if !filesystem.writable {
		return fs.ErrPermission
	}
	path, err := filesystem.resolve(name)
	if err != nil {
		return err
	}
	return filesystem.root.Remove(path)
}

func (filesystem *rootedFilesystem) Join(elements ...string) string {
	return filepath.Join(elements...)
}

func (filesystem *rootedFilesystem) TempFile(directory, prefix string) (billy.File, error) { //nolint:ireturn // Billy's method contract returns billy.File.
	if !filesystem.writable {
		return nil, fs.ErrPermission
	}
	for range 32 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(directory, prefix+hex.EncodeToString(random[:]))
		file, err := filesystem.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return nil, errors.New("create rooted temporary file exhausted collision attempts")
}

func (filesystem *rootedFilesystem) ReadDir(name string) ([]os.FileInfo, error) {
	file, err := filesystem.Open(name)
	if err != nil {
		return nil, err
	}
	directory, ok := file.(*rootedFile)
	if !ok {
		return nil, errors.Join(
			errors.New("rooted filesystem returned an invalid directory handle"), file.Close(),
		)
	}
	entries, readErr := directory.Readdir(-1)
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(first, second int) bool {
		return entries[first].Name() < entries[second].Name()
	})
	return entries, nil
}

func (filesystem *rootedFilesystem) MkdirAll(name string, mode os.FileMode) error {
	if !filesystem.writable {
		return fs.ErrPermission
	}
	path, err := filesystem.resolve(name)
	if err != nil {
		return err
	}
	return filesystem.root.MkdirAll(path, mode)
}

func (filesystem *rootedFilesystem) Lstat(name string) (os.FileInfo, error) {
	path, err := filesystem.resolve(name)
	if err != nil {
		return nil, err
	}
	return filesystem.root.Lstat(path)
}

func (filesystem *rootedFilesystem) Symlink(target, name string) error {
	if !filesystem.writable {
		return fs.ErrPermission
	}
	path, err := filesystem.resolve(name)
	if err != nil {
		return err
	}
	return filesystem.root.Symlink(target, path)
}

func (filesystem *rootedFilesystem) Readlink(name string) (string, error) {
	path, err := filesystem.resolve(name)
	if err != nil {
		return "", err
	}
	return filesystem.root.Readlink(path)
}

func (filesystem *rootedFilesystem) Chroot(name string) (billy.Filesystem, error) { //nolint:ireturn // Billy's method contract returns billy.Filesystem.
	path, err := filesystem.resolve(name)
	if err != nil {
		return nil, err
	}
	info, err := filesystem.root.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("chroot target is not a directory")
	}
	return &rootedFilesystem{
		root: filesystem.root, prefix: path, writable: filesystem.writable,
		objectsAtRoot: filesystem.objectsAtRoot, hideAlternates: filesystem.hideAlternates,
	}, nil
}

func (filesystem *rootedFilesystem) Root() string {
	if filesystem.prefix == "" || filesystem.prefix == "." {
		return string(filepath.Separator)
	}
	return string(filepath.Separator) + filesystem.prefix
}

func (filesystem *rootedFilesystem) Capabilities() billy.Capability {
	capabilities := billy.ReadCapability | billy.SeekCapability
	if filesystem.writable {
		capabilities |= billy.WriteCapability | billy.ReadAndWriteCapability |
			billy.TruncateCapability
	}
	return capabilities
}

func (filesystem *rootedFilesystem) resolve(name string) (string, error) {
	if filesystem == nil || filesystem.root == nil {
		return "", errors.New("rooted filesystem is closed")
	}
	if strings.IndexByte(name, 0) >= 0 {
		return "", errors.New("rooted filesystem path contains NUL")
	}
	cleaned := filepath.Clean(name)
	if filepath.IsAbs(cleaned) {
		return "", errors.New("rooted filesystem requires a relative path")
	}
	logical := filepath.ToSlash(cleaned)
	if filesystem.hideAlternates && logical == "objects/info/alternates" {
		return "", os.ErrNotExist
	}
	if filesystem.objectsAtRoot {
		switch {
		case logical == "objects":
			cleaned = "."
		case strings.HasPrefix(logical, "objects/"):
			cleaned = filepath.FromSlash(strings.TrimPrefix(logical, "objects/"))
		default:
			return "", errors.New("alternate object filesystem path is outside the object database")
		}
	}
	path := filepath.Join(filesystem.prefix, cleaned)
	if path == "" {
		path = "."
	}
	relative, err := filepath.Rel(".", path)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("rooted filesystem path escapes its root")
	}
	return relative, nil
}

type rootedFile struct {
	*os.File
	name     string
	writable bool
}

// Name returns the logical Billy path. go-git reopens packed objects by this
// value, so returning the host path exposed by os.File would escape the rooted
// namespace accidentally.
func (file *rootedFile) Name() string { return file.name }

func (file *rootedFile) Write(value []byte) (int, error) {
	if !file.writable {
		return 0, fs.ErrPermission
	}
	return file.File.Write(value)
}

func (file *rootedFile) Lock() error { return nil }

func (file *rootedFile) Unlock() error { return nil }

func (file *rootedFile) Truncate(size int64) error {
	if !file.writable {
		return fs.ErrPermission
	}
	return file.File.Truncate(size)
}
