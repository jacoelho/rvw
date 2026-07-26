package source

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/jacoelho/rvw/internal/change"
	"golang.org/x/sys/unix"
)

const maxWorktreeFileBytes int64 = 128 << 20

type rawWorktreeCaptureKind uint8

const (
	rawWorktreeRegular rawWorktreeCaptureKind = iota + 1
	rawWorktreeSymlink
	rawWorktreeGitlink
)

type rawWorktreeCapture struct {
	file *os.File
	root *os.Root
	info os.FileInfo
	path string
	link string
	mode change.FileMode
	size int64
	kind rawWorktreeCaptureKind
}

type rawWorktreeOptions struct {
	trustExecutableBit bool
}

func openRawWorktreePath(
	root *os.Root,
	name string,
	indexMode change.FileMode,
	options rawWorktreeOptions,
) (rawWorktreeCapture, error) {
	before, err := root.Lstat(name)
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
		file, err := openRawWorktreeRegular(root, name)
		if err != nil {
			return rawWorktreeCapture{}, err
		}
		opened, err := file.Stat()
		if err != nil || !opened.Mode().IsRegular() || !sameWorktreeFile(before, opened) {
			return rawWorktreeCapture{}, errors.Join(
				errors.New("worktree path changed while opening"), err, file.Close(),
			)
		}
		mode := "100644"
		if opened.Mode()&0o111 != 0 {
			mode = "100755"
		}
		if !options.trustExecutableBit &&
			(indexMode.String() == "100644" || indexMode.String() == "100755") {
			mode = indexMode.String()
		}
		validated, err := change.NewFileMode(mode)
		if err != nil {
			return rawWorktreeCapture{}, errors.Join(err, file.Close())
		}
		return rawWorktreeCapture{
			file: file, root: root, info: opened, path: name, mode: validated,
			size: opened.Size(), kind: rawWorktreeRegular,
		}, nil
	case before.Mode()&os.ModeSymlink != 0:
		link, err := root.Readlink(name)
		if err != nil {
			return rawWorktreeCapture{}, err
		}
		if len(link) > 16<<10 {
			return rawWorktreeCapture{}, &LimitError{
				Resource: LimitWorktreeSymlink, Limit: 16 << 10,
			}
		}
		mode, err := change.NewFileMode("120000")
		if err != nil {
			return rawWorktreeCapture{}, err
		}
		return rawWorktreeCapture{
			root: root, info: before, path: name, link: link, mode: mode,
			size: int64(len(link)), kind: rawWorktreeSymlink,
		}, nil
	case before.IsDir() && indexMode.String() == "160000":
		return rawWorktreeCapture{
			root: root, info: before, path: name, mode: indexMode, kind: rawWorktreeGitlink,
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
		int(parent.Fd()), base,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, errors.Join(err, parent.Close())
	}
	file := os.NewFile(uintptr(descriptor), relative)
	if err := parent.Close(); err != nil {
		return nil, errors.Join(err, file.Close())
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
			ctx, destination, capture.file, buffer, maxWorktreeFileBytes,
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
		link, readErr := capture.root.Readlink(capture.path)
		if readErr != nil || link != capture.link || !sameWorktreeFile(capture.info, current) {
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
		if count > 0 {
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
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
		if count == 0 {
			return written, io.ErrNoProgress
		}
	}
	return written, errors.New("worktree file exceeds raw byte limit")
}

func sameWorktreeFile(left, right os.FileInfo) bool {
	return right != nil && os.SameFile(left, right) && left.Mode() == right.Mode() &&
		left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}
