package source

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // The Git index contract requires SHA-1.
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing"
	gitbinary "github.com/go-git/go-git/v5/utils/binary"
	"github.com/jacoelho/rvw/internal/review"
)

const (
	maxIndexEncodedBytes   int64  = 96 << 20
	maxIndexPathBytes      int64  = 64 << 20
	maxIndexExtensionBytes int64  = 16 << 20
	indexHeaderBytes              = 12
	indexEntryHeaderBytes         = 62
	indexChecksumBytes            = sha1.Size
	indexNameMask          uint16 = 0x0fff
	indexStageMask         uint16 = 0x3000
	indexExtendedMask      uint16 = 0x4000
	indexAssumeValidMask   uint16 = 0x8000
	indexIntentToAddMask   uint16 = 0x2000
	indexSkipWorktreeMask  uint16 = 0x4000
)

type streamedIndexEntry struct {
	path         string
	oid          plumbing.Hash
	mode         uint32
	stage        uint8
	assumeValid  bool
	skipWorktree bool
	intentToAdd  bool
}

func (session *repositorySession) captureIndexCopy(ctx context.Context) (copyPath string, returnErr error) {
	if _, err := session.gitdirRoot.Lstat("index"); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("inspect Git index: %w", err)
	}
	workspace, err := session.ensureWorkspace()
	if err != nil {
		return "", err
	}
	before, err := session.gitdirRoot.Lstat("index")
	if err != nil {
		return "", fmt.Errorf("inspect Git index: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("git index must be a regular file")
	}
	original, err := session.gitdirRoot.Open("index")
	if err != nil {
		return "", fmt.Errorf("open Git index: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, original.Close()) }()
	descriptorInfo, err := original.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect opened Git index: %w", err)
	}
	if !os.SameFile(before, descriptorInfo) {
		return "", fmt.Errorf("%w: Git index changed before capture", ErrSourceStale)
	}

	copyPath = filepath.Join(workspace, "index")
	copyFile, err := os.OpenFile( //nolint:gosec // copyPath is inside the freshly created private workspace.
		copyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600,
	)
	if err != nil {
		return "", fmt.Errorf("create Git index copy: %w", err)
	}
	firstDigest := sha256.New()
	written, copyErr := copyIndexWithCancellation(
		ctx,
		io.MultiWriter(copyFile, firstDigest),
		io.LimitReader(original, maxIndexEncodedBytes+1),
	)
	closeErr := copyFile.Close()
	if copyErr != nil || closeErr != nil {
		return "", errors.Join(copyErr, closeErr)
	}
	if written > maxIndexEncodedBytes {
		return "", &LimitError{Resource: LimitIndexEncoding, Limit: uint64(maxIndexEncodedBytes)}
	}
	after, err := session.gitdirRoot.Lstat("index")
	if err != nil {
		return "", fmt.Errorf("reinspect Git index: %w", err)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return "", fmt.Errorf("%w: Git index changed during capture", ErrSourceStale)
	}
	if _, seekErr := original.Seek(0, io.SeekStart); seekErr != nil {
		return "", fmt.Errorf("rewind Git index: %w", seekErr)
	}
	secondDigest := sha256.New()
	secondBytes, err := copyIndexWithCancellation(
		ctx,
		secondDigest,
		io.LimitReader(original, maxIndexEncodedBytes+1),
	)
	if err != nil {
		return "", err
	}
	if secondBytes != written || !bytes.Equal(firstDigest.Sum(nil), secondDigest.Sum(nil)) {
		return "", fmt.Errorf("%w: Git index contents changed during capture", ErrSourceStale)
	}
	return copyPath, nil
}

func (session *repositorySession) ensureWorkspace() (string, error) {
	if session.cleanup != "" {
		return session.cleanup, nil
	}
	workspace, err := os.MkdirTemp("", "rvw-source-")
	if err != nil {
		return "", fmt.Errorf("create source workspace: %w", err)
	}
	directory, err := os.Open(workspace) //nolint:gosec // workspace was just created by os.MkdirTemp.
	if err != nil {
		return "", cleanupWorkspaceError(workspace, err)
	}
	session.cleanup = workspace
	session.workspace = directory
	return workspace, nil
}

func cleanupWorkspaceError(workspace string, cause error) error {
	return errors.Join(cause, os.RemoveAll(workspace))
}

func copyIndexWithCancellation(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 16<<10)
	var total int64
	for {
		if err := context.Cause(ctx); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
		if read == 0 {
			return total, io.ErrNoProgress
		}
	}
}

func streamIndex(
	ctx context.Context,
	path string,
	consume func(streamedIndexEntry) error,
) (count uint32, returnErr error) {
	if path == "" {
		return 0, nil
	}
	file, err := os.Open(path) //nolint:gosec // path is the owner-created private index copy.
	if err != nil {
		return 0, fmt.Errorf("open copied Git index: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() < indexHeaderBytes+indexChecksumBytes || info.Size() > maxIndexEncodedBytes {
		return 0, errors.New("git index has an invalid encoded size")
	}
	if err := verifyIndexChecksum(ctx, file, info.Size()); err != nil {
		return 0, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	limited := &io.LimitedReader{R: file, N: info.Size() - indexChecksumBytes}
	reader := bufio.NewReaderSize(limited, 16<<10)
	var header [indexHeaderBytes]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, err
	}
	if string(header[:4]) != "DIRC" {
		return 0, errors.New("git index signature is invalid")
	}
	version := binary.BigEndian.Uint32(header[4:8])
	if version < 2 || version > 4 {
		return 0, fmt.Errorf("unsupported Git index version %d", version)
	}
	count = binary.BigEndian.Uint32(header[8:12])
	var previousPath string
	var previousStage uint8
	var pathBytes uint64
	for index := range count {
		if index%4_096 == 0 {
			if err := context.Cause(ctx); err != nil {
				return 0, err
			}
		}
		entry, err := readIndexEntry(reader, version, previousPath)
		if err != nil {
			return 0, fmt.Errorf("decode Git index entry %d: %w", index+1, err)
		}
		pathBytes += uint64(len(entry.path))
		if pathBytes > uint64(maxIndexPathBytes) {
			return 0, &LimitError{Resource: LimitIndexPaths, Limit: uint64(maxIndexPathBytes)}
		}
		if index > 0 && (entry.path < previousPath ||
			(entry.path == previousPath && entry.stage <= previousStage)) {
			return 0, errors.New("git index entries are not strictly ordered by path and stage")
		}
		if err := consume(entry); err != nil {
			return 0, err
		}
		previousPath = entry.path
		previousStage = entry.stage
	}
	extensions := &io.LimitedReader{
		R: reader,
		N: limited.N + int64(reader.Buffered()),
	}
	if err := readIndexExtensions(ctx, extensions); err != nil {
		return 0, err
	}
	return count, nil
}

func readIndexEntry(reader *bufio.Reader, version uint32, previous string) (streamedIndexEntry, error) {
	var fixed [indexEntryHeaderBytes]byte
	if _, err := io.ReadFull(reader, fixed[:]); err != nil {
		return streamedIndexEntry{}, err
	}
	mode := binary.BigEndian.Uint32(fixed[24:28])
	if mode != 0o100644 && mode != 0o100755 && mode != 0o120000 && mode != 0o160000 {
		return streamedIndexEntry{}, fmt.Errorf("unsupported index mode %06o", mode)
	}
	var oid plumbing.Hash
	copy(oid[:], fixed[40:60])
	flags := binary.BigEndian.Uint16(fixed[60:62])
	extended := uint16(0)
	readBytes := indexEntryHeaderBytes
	if flags&indexExtendedMask != 0 {
		if version == 2 {
			return streamedIndexEntry{}, errors.New("version 2 index entry has extended flags")
		}
		if err := binary.Read(reader, binary.BigEndian, &extended); err != nil {
			return streamedIndexEntry{}, err
		}
		readBytes += 2
		if extended & ^(indexIntentToAddMask|indexSkipWorktreeMask) != 0 {
			return streamedIndexEntry{}, errors.New("index entry has unknown extended flags")
		}
	}
	stage := uint8((flags & indexStageMask) >> 12)
	path, consumed, err := readIndexPath(reader, version, flags&indexNameMask, previous)
	if err != nil {
		return streamedIndexEntry{}, err
	}
	if err := validateRawPath([]byte(path)); err != nil {
		return streamedIndexEntry{}, err
	}
	if version < 4 {
		entrySize := readBytes + len(path)
		padding := 8 - entrySize%8 - (consumed - len(path))
		if padding > 0 {
			bytes := make([]byte, padding)
			if _, err := io.ReadFull(reader, bytes); err != nil {
				return streamedIndexEntry{}, err
			}
			for _, value := range bytes {
				if value != 0 {
					return streamedIndexEntry{}, errors.New("index entry padding is not NUL")
				}
			}
		}
	}
	intent := extended&indexIntentToAddMask != 0
	if oid.IsZero() && !intent {
		return streamedIndexEntry{}, errors.New("index entry has a zero object ID")
	}
	return streamedIndexEntry{
		path:         path,
		oid:          oid,
		mode:         mode,
		stage:        stage,
		assumeValid:  flags&indexAssumeValidMask != 0,
		skipWorktree: extended&indexSkipWorktreeMask != 0,
		intentToAdd:  intent,
	}, nil
}

func readIndexPath(
	reader *bufio.Reader,
	version uint32,
	encodedLength uint16,
	previous string,
) (string, int, error) {
	if version == 4 {
		strip, err := gitbinary.ReadVariableWidthInt(reader)
		if err != nil {
			return "", 0, err
		}
		if strip < 0 || strip > int64(len(previous)) {
			return "", 0, errors.New("version 4 index path has an invalid prefix strip")
		}
		suffix, err := readNULTerminatedIndexPath(reader, review.MaxPathBytes)
		if err != nil {
			return "", 0, err
		}
		path := previous[:len(previous)-int(strip)] + suffix
		if len(path) > int(review.MaxPathBytes) {
			return "", 0, errors.New("version 4 index path exceeds the path limit")
		}
		return path, 0, nil
	}
	if encodedLength == indexNameMask {
		path, err := readNULTerminatedIndexPath(reader, review.MaxPathBytes)
		return path, len(path) + 1, err
	}
	if encodedLength == 0 || encodedLength > uint16(review.MaxPathBytes) {
		return "", 0, errors.New("index path length is invalid")
	}
	path := make([]byte, encodedLength)
	if _, err := io.ReadFull(reader, path); err != nil {
		return "", 0, err
	}
	return string(path), len(path), nil
}

func readNULTerminatedIndexPath(reader *bufio.Reader, limit int64) (string, error) {
	path := make([]byte, 0, min(int(limit), 256))
	for len(path) <= int(limit) {
		value, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		if value == 0 {
			if len(path) == 0 {
				return "", errors.New("index path is empty")
			}
			return string(path), nil
		}
		path = append(path, value)
	}
	return "", errors.New("index path exceeds the path limit")
}

func readIndexExtensions(ctx context.Context, reader *io.LimitedReader) error {
	var aggregate uint64
	for reader.N > 0 {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if reader.N < 8 {
			return errors.New("git index extension header is truncated")
		}
		var header [8]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return err
		}
		size := uint64(binary.BigEndian.Uint32(header[4:8]))
		aggregate += size
		if aggregate > uint64(maxIndexExtensionBytes) {
			return &LimitError{Resource: LimitIndexExtensions, Limit: uint64(maxIndexExtensionBytes)}
		}
		if size > uint64(reader.N) {
			return errors.New("git index extension is truncated")
		}
		if header[0] < 'A' || header[0] > 'Z' {
			return fmt.Errorf("unsupported mandatory Git index extension %q", header[:4])
		}
		signature := string(header[:4])
		if signature == "EOIE" && size != 24 {
			return errors.New("git index EOIE extension has an invalid size")
		}
		if signature == "IEOT" && (size < 4 || (size-4)%8 != 0) {
			return errors.New("git index IEOT extension has an invalid size")
		}
		if _, err := io.CopyN(io.Discard, reader, int64(size)); err != nil { //nolint:gosec // size is a uint32 field.
			return err
		}
	}
	return nil
}

func verifyIndexChecksum(ctx context.Context, file *os.File, size int64) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	digest := sha1.New() //nolint:gosec // The Git index trailer is SHA-1 by definition.
	remaining := size - indexChecksumBytes
	buffer := make([]byte, 16<<10)
	for remaining > 0 {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		chunk := int64(len(buffer))
		chunk = min(remaining, chunk)
		read, err := io.ReadFull(file, buffer[:chunk])
		if err != nil {
			return err
		}
		_, _ = digest.Write(buffer[:read])
		remaining -= int64(read)
	}
	var trailer [indexChecksumBytes]byte
	if _, err := io.ReadFull(file, trailer[:]); err != nil {
		return err
	}
	if !bytes.Equal(digest.Sum(nil), trailer[:]) {
		return errors.New("git index checksum is invalid")
	}
	return nil
}
