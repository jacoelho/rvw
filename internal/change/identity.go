package change

import (
	"errors"
	"fmt"
	"math"
)

// MaxFiles defines the supported files limit.
const (
	MaxFiles             = 100_000
	MaxPathBytes   int64 = 4_096
	MaxSourcePaths int64 = 64 << 20
	MaxAdditions         = 1_000_000
	MaxDeletions         = 1_000_000
	MaxHunks             = 100_000
)

// ErrInvalidIdentity identifies an invalid identity failure.
var ErrInvalidIdentity = errors.New("invalid review identity")

func uint32FromInt(value int) uint32 {
	if value < 0 {
		panic("negative value cannot be represented as uint32")
	}
	converted := uint64(value)
	if converted > math.MaxUint32 {
		panic("value exceeds uint32")
	}
	return uint32(converted)
}

func uint32FromInt64(value int64) uint32 {
	if value < 0 || value > math.MaxUint32 {
		panic("value cannot be represented as uint32")
	}
	return uint32(value)
}

// SourceID represents source id.
type SourceID uint64

// NewSourceID constructs and validates source id.
func NewSourceID(value uint64) (SourceID, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: source ID must be positive", ErrInvalidIdentity)
	}
	return SourceID(value), nil
}

// Uint64 returns the receiver's uint64 value.
func (id SourceID) Uint64() uint64 {
	return uint64(id)
}

// FileID represents file id.
type FileID uint32

// NewFileID constructs and validates file id.
func NewFileID(value uint32) (FileID, error) {
	if value >= MaxFiles {
		return 0, fmt.Errorf("%w: file ID %d exceeds %d", ErrInvalidIdentity, value, MaxFiles-1)
	}
	return FileID(value), nil
}

// Uint32 returns the receiver's uint32 value.
func (id FileID) Uint32() uint32 {
	return uint32(id)
}

// WorkspaceRevision represents workspace revision.
type WorkspaceRevision uint64

// NewWorkspaceRevision constructs and validates workspace revision.
func NewWorkspaceRevision(value uint64) (WorkspaceRevision, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: workspace revision must be positive", ErrInvalidIdentity)
	}
	return WorkspaceRevision(value), nil
}

// Uint64 returns the receiver's uint64 value.
func (revision WorkspaceRevision) Uint64() uint64 {
	return uint64(revision)
}

// HunkIndex represents hunk index.
type HunkIndex uint32

// NewHunkIndex constructs and validates hunk index.
func NewHunkIndex(value uint32) (HunkIndex, error) {
	if value >= MaxHunks {
		return 0, fmt.Errorf("%w: hunk index %d exceeds %d", ErrInvalidIdentity, value, MaxHunks-1)
	}
	return HunkIndex(value), nil
}

// Uint32 returns the receiver's uint32 value.
func (index HunkIndex) Uint32() uint32 {
	return uint32(index)
}

// HunkID represents hunk id.
type HunkID struct {
	file  FileID
	index HunkIndex
}

// NewHunkID constructs and validates hunk id.
func NewHunkID(file FileID, index HunkIndex) (HunkID, error) {
	if uint32(file) >= MaxFiles || uint32(index) >= MaxHunks {
		return HunkID{}, ErrInvalidIdentity
	}
	return HunkID{file: file, index: index}, nil
}

// FileID returns the receiver's file id value.
func (id HunkID) FileID() FileID {
	return id.file
}

// Index returns the receiver's index value.
func (id HunkID) Index() HunkIndex {
	return id.index
}

// CommentID represents comment id.
type CommentID uint32

// NewCommentID constructs and validates comment id.
func NewCommentID(value uint32) (CommentID, error) {
	if value >= 4_096 {
		return 0, fmt.Errorf("%w: comment ID %d exceeds 4095", ErrInvalidIdentity, value)
	}
	return CommentID(value), nil
}

// Uint32 returns the receiver's uint32 value.
func (id CommentID) Uint32() uint32 {
	return uint32(id)
}

// OperationID represents operation id.
type OperationID uint64

// NewOperationID constructs and validates operation id.
func NewOperationID(value uint64) (OperationID, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: operation ID must be positive", ErrInvalidIdentity)
	}
	return OperationID(value), nil
}

// Uint64 returns the receiver's uint64 value.
func (id OperationID) Uint64() uint64 {
	return uint64(id)
}

// PatchID represents patch id.
type PatchID uint64

// NewPatchID constructs and validates patch id.
func NewPatchID(value uint64) (PatchID, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: patch ID must be positive", ErrInvalidIdentity)
	}
	return PatchID(value), nil
}

// Uint64 returns the receiver's uint64 value.
func (id PatchID) Uint64() uint64 {
	return uint64(id)
}

// PatchHandle represents patch handle.
type PatchHandle struct {
	id       PatchID
	sourceID SourceID
	fileID   FileID
}

// NewPatchHandle constructs and validates patch handle.
func NewPatchHandle(id PatchID, sourceID SourceID, fileID FileID) (PatchHandle, error) {
	if id == 0 || sourceID == 0 {
		return PatchHandle{}, fmt.Errorf("%w: patch and source IDs must be positive", ErrInvalidIdentity)
	}
	if uint32(fileID) >= MaxFiles {
		return PatchHandle{}, fmt.Errorf("%w: file ID %d exceeds %d", ErrInvalidIdentity, fileID, MaxFiles-1)
	}
	return PatchHandle{id: id, sourceID: sourceID, fileID: fileID}, nil
}

// ID returns the receiver's id value.
func (handle PatchHandle) ID() PatchID {
	return handle.id
}

// SourceID returns the receiver's source id value.
func (handle PatchHandle) SourceID() SourceID {
	return handle.sourceID
}

// FileID returns the receiver's file id value.
func (handle PatchHandle) FileID() FileID {
	return handle.fileID
}
