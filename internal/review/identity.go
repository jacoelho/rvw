package review

import (
	"errors"
	"fmt"
)

const (
	MaxFiles             = 100_000
	MaxPathBytes   int64 = 4_096
	MaxSourcePaths int64 = 64 << 20
	MaxAdditions         = 1_000_000
	MaxDeletions         = 1_000_000
	MaxHunks             = 100_000
)

var ErrInvalidIdentity = errors.New("invalid review identity")

type SourceID uint64

func NewSourceID(value uint64) (SourceID, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: source ID must be positive", ErrInvalidIdentity)
	}
	return SourceID(value), nil
}

func (id SourceID) Uint64() uint64 {
	return uint64(id)
}

type FileID uint32

func NewFileID(value uint32) (FileID, error) {
	if value >= MaxFiles {
		return 0, fmt.Errorf("%w: file ID %d exceeds %d", ErrInvalidIdentity, value, MaxFiles-1)
	}
	return FileID(value), nil
}

func (id FileID) Uint32() uint32 {
	return uint32(id)
}

type WorkspaceRevision uint64

func NewWorkspaceRevision(value uint64) (WorkspaceRevision, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: workspace revision must be positive", ErrInvalidIdentity)
	}
	return WorkspaceRevision(value), nil
}

func (revision WorkspaceRevision) Uint64() uint64 {
	return uint64(revision)
}

type HunkIndex uint32

func NewHunkIndex(value uint32) (HunkIndex, error) {
	if value >= MaxHunks {
		return 0, fmt.Errorf("%w: hunk index %d exceeds %d", ErrInvalidIdentity, value, MaxHunks-1)
	}
	return HunkIndex(value), nil
}

func (index HunkIndex) Uint32() uint32 {
	return uint32(index)
}

type HunkID struct {
	file  FileID
	index HunkIndex
}

func NewHunkID(file FileID, index HunkIndex) (HunkID, error) {
	if uint32(file) >= MaxFiles || uint32(index) >= MaxHunks {
		return HunkID{}, ErrInvalidIdentity
	}
	return HunkID{file: file, index: index}, nil
}

func (id HunkID) FileID() FileID {
	return id.file
}

func (id HunkID) Index() HunkIndex {
	return id.index
}

type CommentID uint32

func NewCommentID(value uint32) (CommentID, error) {
	if value >= 4_096 {
		return 0, fmt.Errorf("%w: comment ID %d exceeds 4095", ErrInvalidIdentity, value)
	}
	return CommentID(value), nil
}

func (id CommentID) Uint32() uint32 {
	return uint32(id)
}

type OperationID uint64

func NewOperationID(value uint64) (OperationID, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: operation ID must be positive", ErrInvalidIdentity)
	}
	return OperationID(value), nil
}

func (id OperationID) Uint64() uint64 {
	return uint64(id)
}

type PatchID uint64

func NewPatchID(value uint64) (PatchID, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: patch ID must be positive", ErrInvalidIdentity)
	}
	return PatchID(value), nil
}

func (id PatchID) Uint64() uint64 {
	return uint64(id)
}

type PatchHandle struct {
	id       PatchID
	sourceID SourceID
	fileID   FileID
}

func NewPatchHandle(id PatchID, sourceID SourceID, fileID FileID) (PatchHandle, error) {
	if id == 0 || sourceID == 0 {
		return PatchHandle{}, fmt.Errorf("%w: patch and source IDs must be positive", ErrInvalidIdentity)
	}
	if uint32(fileID) >= MaxFiles {
		return PatchHandle{}, fmt.Errorf("%w: file ID %d exceeds %d", ErrInvalidIdentity, fileID, MaxFiles-1)
	}
	return PatchHandle{id: id, sourceID: sourceID, fileID: fileID}, nil
}

func (handle PatchHandle) ID() PatchID {
	return handle.id
}

func (handle PatchHandle) SourceID() SourceID {
	return handle.sourceID
}

func (handle PatchHandle) FileID() FileID {
	return handle.fileID
}
