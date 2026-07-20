package review

import (
	"errors"
	"fmt"
	"iter"
	"slices"
)

var ErrInvalidSnapshot = errors.New("invalid review snapshot")

type HiddenReason string

const (
	HiddenBuiltinDefault HiddenReason = "builtin-default"
	HiddenGitignore      HiddenReason = "gitignore"
	HiddenRVWIgnore      HiddenReason = "rvwignore"
	HiddenInclude        HiddenReason = "include"
	HiddenExclude        HiddenReason = "exclude"
)

type HiddenReasons struct {
	bits uint8
}

func NewHiddenReasons(values ...HiddenReason) (HiddenReasons, error) {
	var reasons HiddenReasons
	for _, value := range values {
		bit, ok := hiddenReasonBit(value)
		if !ok {
			return HiddenReasons{}, fmt.Errorf(
				"%w: unknown hidden reason %q",
				ErrInvalidSnapshot,
				value,
			)
		}
		reasons.bits |= bit
	}
	return reasons, nil
}

func (reasons HiddenReasons) Has(value HiddenReason) bool {
	bit, ok := hiddenReasonBit(value)
	return ok && reasons.bits&bit != 0
}

func (reasons HiddenReasons) Values() []HiddenReason {
	values := make([]HiddenReason, 0, 5)
	for _, value := range []HiddenReason{
		HiddenBuiltinDefault,
		HiddenGitignore,
		HiddenRVWIgnore,
		HiddenInclude,
		HiddenExclude,
	} {
		if reasons.Has(value) {
			values = append(values, value)
		}
	}
	return values
}

func (reasons HiddenReasons) valid() bool {
	const validBits = 1<<5 - 1
	return reasons.bits&^validBits == 0
}

func hiddenReasonBit(value HiddenReason) (uint8, bool) {
	switch value {
	case HiddenBuiltinDefault:
		return 1 << 0, true
	case HiddenGitignore:
		return 1 << 1, true
	case HiddenRVWIgnore:
		return 1 << 2, true
	case HiddenInclude:
		return 1 << 3, true
	case HiddenExclude:
		return 1 << 4, true
	default:
		return 0, false
	}
}

type FileTrait uint8

const (
	TraitGenerated FileTrait = iota + 1
	TraitVendored
	TraitLockFile
	TraitMinified
	TraitTooLarge
)

type FileTraits struct {
	bits uint8
}

func NewFileTraits(values ...FileTrait) (FileTraits, error) {
	var traits FileTraits
	for _, value := range values {
		if value < TraitGenerated || value > TraitTooLarge {
			return FileTraits{}, fmt.Errorf("%w: unknown file trait %d", ErrInvalidSnapshot, value)
		}
		traits.bits |= 1 << (value - 1)
	}
	return traits, nil
}

func (traits FileTraits) Has(value FileTrait) bool {
	return value >= TraitGenerated && value <= TraitTooLarge &&
		traits.bits&(1<<(value-1)) != 0
}

func (traits FileTraits) valid() bool {
	const validBits = 1<<TraitTooLarge - 1
	return traits.bits&^validBits == 0
}

type Flags struct {
	traits FileTraits
	hidden HiddenReasons
}

func NewFlags(
	traits FileTraits,
	hidden HiddenReasons,
) (Flags, error) {
	if !traits.valid() {
		return Flags{}, fmt.Errorf("%w: file traits contain unknown bits", ErrInvalidSnapshot)
	}
	if !hidden.valid() {
		return Flags{}, fmt.Errorf(
			"%w: hidden reasons contain unknown bits",
			ErrInvalidSnapshot,
		)
	}
	return Flags{traits: traits, hidden: hidden}, nil
}

func (flags Flags) Hidden() bool {
	return flags.hidden.bits != 0
}

func (flags Flags) Traits() FileTraits {
	return flags.traits
}

func (flags Flags) HiddenReasons() []HiddenReason {
	return flags.hidden.Values()
}

func (flags Flags) Clone() Flags {
	return flags
}

func (flags Flags) valid() bool {
	_, err := NewFlags(flags.traits, flags.hidden)
	return err == nil
}

type File struct {
	id          FileID
	oldPath     *Path
	newPath     *Path
	change      Change
	flags       Flags
	patchHandle PatchHandle
	contents    FileContents
}

type FileContent struct {
	side LineSide
	oid  ObjectID
}

func OldFileContent(oid ObjectID) FileContent {
	return FileContent{side: LineOld, oid: oid}
}

func NewFileContent(oid ObjectID) FileContent {
	return FileContent{side: LineNew, oid: oid}
}

type FileContents struct {
	old *ObjectID
	new *ObjectID
}

func NewFileContents(values ...FileContent) (FileContents, error) {
	var contents FileContents
	width := 0
	for _, value := range values {
		if !validObjectID(value.oid.String()) {
			return FileContents{}, fmt.Errorf("%w: file content object is invalid", ErrInvalidSnapshot)
		}
		if width != 0 && len(value.oid.String()) != width {
			return FileContents{}, fmt.Errorf(
				"%w: file contents use inconsistent object ID formats",
				ErrInvalidSnapshot,
			)
		}
		width = len(value.oid.String())
		switch value.side {
		case LineOld:
			if contents.old != nil {
				return FileContents{}, fmt.Errorf("%w: duplicate old content", ErrInvalidSnapshot)
			}
			contents.old = new(value.oid)
		case LineNew:
			if contents.new != nil {
				return FileContents{}, fmt.Errorf("%w: duplicate new content", ErrInvalidSnapshot)
			}
			contents.new = new(value.oid)
		default:
			return FileContents{}, fmt.Errorf("%w: file content side is invalid", ErrInvalidSnapshot)
		}
	}
	return contents, nil
}

func (contents FileContents) Object(side LineSide) (ObjectID, bool) {
	if side == LineOld && contents.old != nil {
		return *contents.old, true
	}
	if side == LineNew && contents.new != nil {
		return *contents.new, true
	}
	return "", false
}

func (contents FileContents) clone() FileContents {
	contents.old = cloneValue(contents.old)
	contents.new = cloneValue(contents.new)
	return contents
}

func NewFile(
	id FileID,
	oldPath, newPath *Path,
	change Change,
	flags Flags,
	patchHandle PatchHandle,
) (File, error) {
	return newFileWithContents(
		id,
		oldPath,
		newPath,
		change,
		flags,
		patchHandle,
		FileContents{},
	)
}

func newFileWithContents(
	id FileID,
	oldPath, newPath *Path,
	change Change,
	flags Flags,
	patchHandle PatchHandle,
	contents FileContents,
) (File, error) {
	if uint32(id) >= MaxFiles {
		return File{}, fmt.Errorf("%w: file ID %d exceeds %d", ErrInvalidSnapshot, id, MaxFiles-1)
	}
	if oldPath == nil && newPath == nil {
		return File{}, fmt.Errorf("%w: file requires an old or new path", ErrInvalidSnapshot)
	}
	if oldPath != nil && len(oldPath.raw) == 0 || newPath != nil && len(newPath.raw) == 0 {
		return File{}, fmt.Errorf("%w: file path is invalid", ErrInvalidSnapshot)
	}
	if !change.valid() {
		return File{}, fmt.Errorf("%w: file change is invalid", ErrInvalidSnapshot)
	}
	if !flags.valid() {
		return File{}, fmt.Errorf("%w: file flags are invalid", ErrInvalidSnapshot)
	}
	if patchHandle.FileID() != id {
		return File{}, fmt.Errorf("%w: patch handle file ID does not match file", ErrInvalidSnapshot)
	}
	if _, exists := contents.Object(LineOld); exists && oldPath == nil {
		return File{}, fmt.Errorf(
			"%w: old content requires an old path",
			ErrInvalidSnapshot,
		)
	}
	if _, exists := contents.Object(LineNew); exists && newPath == nil {
		return File{}, fmt.Errorf(
			"%w: new content requires a new path",
			ErrInvalidSnapshot,
		)
	}
	switch change.Kind() {
	case ChangeAdded:
		if oldPath != nil || newPath == nil {
			return File{}, fmt.Errorf("%w: added file requires only a new path", ErrInvalidSnapshot)
		}
	case ChangeDeleted:
		if oldPath == nil || newPath != nil {
			return File{}, fmt.Errorf("%w: deleted file requires only an old path", ErrInvalidSnapshot)
		}
	case ChangeBinary:
		// Binary changes retain whichever path sides Git reports.
	case ChangeSubmodule:
		oldCommit, newCommit := change.SubmoduleCommits()
		if (oldPath != nil) != (oldCommit != nil) ||
			(newPath != nil) != (newCommit != nil) {
			return File{}, fmt.Errorf(
				"%w: submodule paths must match commit sides",
				ErrInvalidSnapshot,
			)
		}
	case ChangeModified, ChangeRenamed, ChangeCopied,
		ChangeRenameOnly, ChangeModeOnly, ChangeObjectTypeChanged:
		if oldPath == nil || newPath == nil {
			return File{}, fmt.Errorf("%w: %s file requires old and new paths", ErrInvalidSnapshot, change.Kind())
		}
	default:
		return File{}, fmt.Errorf("%w: unsupported change kind %q", ErrInvalidSnapshot, change.Kind())
	}

	return File{
		id:          id,
		oldPath:     clonePath(oldPath),
		newPath:     clonePath(newPath),
		change:      change,
		flags:       flags.Clone(),
		patchHandle: patchHandle,
		contents:    contents.clone(),
	}, nil
}

func (file File) ID() FileID {
	return file.id
}

func (file File) OldPath() (Path, bool) {
	if file.oldPath == nil {
		return Path{}, false
	}
	return *file.oldPath, true
}

func (file File) NewPath() (Path, bool) {
	if file.newPath == nil {
		return Path{}, false
	}
	return *file.newPath, true
}

func (file File) DisplayPath() string {
	if file.newPath != nil {
		return file.newPath.Display()
	}
	return file.oldPath.Display()
}

func (file File) IdentityPath() Path {
	if file.newPath != nil {
		return *file.newPath
	}
	return *file.oldPath
}

func (file File) Change() Change {
	return file.change
}

func (file File) Flags() Flags {
	return file.flags.Clone()
}

func (file File) PatchHandle() PatchHandle {
	return file.patchHandle
}

func (file File) Content(side LineSide) (ObjectID, bool) {
	return file.contents.Object(side)
}

type Snapshot struct {
	sourceID           SourceID
	files              []File
	remoteThreads      []RemoteThread
	remoteThreadStatus RemoteThreadStatus
	remoteThreadDetail OperationDetail
}

func NewSnapshot(sourceID SourceID, files []File) (Snapshot, error) {
	if sourceID == 0 {
		return Snapshot{}, fmt.Errorf("%w: source ID must be positive", ErrInvalidSnapshot)
	}
	if len(files) > MaxFiles {
		return Snapshot{}, fmt.Errorf("%w: %d files exceed limit %d", ErrInvalidSnapshot, len(files), MaxFiles)
	}

	var pathBytes int64
	objectIDWidth := 0
	patchIDs := make(map[PatchID]bool, len(files))
	owned := slices.Clone(files)
	for index, file := range owned {
		if uint32(file.ID()) != uint32(index) {
			return Snapshot{}, fmt.Errorf("%w: file ID %d is not sequential index %d", ErrInvalidSnapshot, file.ID(), index)
		}
		if file.PatchHandle().SourceID() != sourceID {
			return Snapshot{}, fmt.Errorf("%w: file %d patch source does not match snapshot", ErrInvalidSnapshot, index)
		}
		if patchIDs[file.PatchHandle().ID()] {
			return Snapshot{}, fmt.Errorf("%w: duplicate patch ID %d", ErrInvalidSnapshot, file.PatchHandle().ID())
		}
		patchIDs[file.PatchHandle().ID()] = true
		for _, objectID := range fileObjectIDs(file) {
			if objectIDWidth != 0 && len(objectID.String()) != objectIDWidth {
				return Snapshot{}, fmt.Errorf(
					"%w: files use inconsistent object ID formats",
					ErrInvalidSnapshot,
				)
			}
			objectIDWidth = len(objectID.String())
		}
		if path, ok := file.OldPath(); ok {
			pathBytes += int64(len(path.raw))
		}
		if path, ok := file.NewPath(); ok {
			pathBytes += int64(len(path.raw))
		}
		if pathBytes > MaxSourcePaths {
			return Snapshot{}, fmt.Errorf("%w: raw paths exceed %d bytes", ErrInvalidSnapshot, MaxSourcePaths)
		}
	}
	return Snapshot{sourceID: sourceID, files: owned}, nil
}

func fileObjectIDs(file File) []ObjectID {
	ids := make([]ObjectID, 0, 6)
	if value, ok := file.contents.Object(LineOld); ok {
		ids = append(ids, value)
	}
	if value, ok := file.contents.Object(LineNew); ok {
		ids = append(ids, value)
	}
	if file.change.oldCommit != nil {
		ids = append(ids, *file.change.oldCommit)
	}
	if file.change.newCommit != nil {
		ids = append(ids, *file.change.newCommit)
	}
	if file.change.oldObject != nil {
		ids = append(ids, file.change.oldObject.oid)
	}
	if file.change.newObject != nil {
		ids = append(ids, file.change.newObject.oid)
	}
	return ids
}

func (snapshot Snapshot) SourceID() SourceID {
	return snapshot.sourceID
}

func (snapshot Snapshot) FileCount() int {
	return len(snapshot.files)
}

func (snapshot Snapshot) Files() []File {
	return slices.Clone(snapshot.files)
}

func (snapshot Snapshot) File(id FileID) (File, bool) {
	if int(id.Uint32()) >= len(snapshot.files) {
		return File{}, false
	}
	file := snapshot.files[id]
	return file, file.ID() == id
}

func (snapshot Snapshot) AllFiles() iter.Seq[File] {
	return func(yield func(File) bool) {
		for _, file := range snapshot.files {
			if !yield(file) {
				return
			}
		}
	}
}

type SnapshotBuilder struct {
	sourceID  SourceID
	files     []File
	pathBytes int64
	finished  bool
}

func NewSnapshotBuilder(sourceID SourceID, capacity int) (*SnapshotBuilder, error) {
	if sourceID == 0 || capacity < 0 || capacity > MaxFiles {
		return nil, ErrInvalidSnapshot
	}
	return &SnapshotBuilder{sourceID: sourceID, files: make([]File, 0, capacity)}, nil
}

func (builder *SnapshotBuilder) AddFile(
	oldPath, newPath *Path,
	change Change,
	flags Flags,
) (PatchHandle, error) {
	return builder.AddFileWithContents(
		oldPath,
		newPath,
		change,
		flags,
		FileContents{},
	)
}

func (builder *SnapshotBuilder) AddFileWithContents(
	oldPath, newPath *Path,
	change Change,
	flags Flags,
	contents FileContents,
) (PatchHandle, error) {
	if builder == nil || builder.finished || len(builder.files) >= MaxFiles {
		return PatchHandle{}, ErrInvalidSnapshot
	}
	pathBytes := builder.pathBytes
	if oldPath != nil {
		pathBytes += int64(len(oldPath.raw))
	}
	if newPath != nil {
		pathBytes += int64(len(newPath.raw))
	}
	if pathBytes > MaxSourcePaths {
		return PatchHandle{}, fmt.Errorf("%w: raw paths exceed %d bytes", ErrInvalidSnapshot, MaxSourcePaths)
	}
	fileID, _ := NewFileID(uint32(len(builder.files)))
	patchID, _ := NewPatchID(uint64(len(builder.files) + 1))
	handle, _ := NewPatchHandle(patchID, builder.sourceID, fileID)
	file, err := newFileWithContents(
		fileID,
		oldPath,
		newPath,
		change,
		flags,
		handle,
		contents,
	)
	if err != nil {
		return PatchHandle{}, err
	}
	builder.files = append(builder.files, file)
	builder.pathBytes = pathBytes
	return handle, nil
}

func (builder *SnapshotBuilder) Finish() (Snapshot, error) {
	if builder == nil || builder.finished {
		return Snapshot{}, ErrInvalidSnapshot
	}
	builder.finished = true
	snapshot, err := NewSnapshot(builder.sourceID, builder.files)
	builder.files = nil
	return snapshot, err
}

func validHiddenReason(reason HiddenReason) bool {
	return reason == HiddenBuiltinDefault || reason == HiddenGitignore ||
		reason == HiddenRVWIgnore || reason == HiddenInclude || reason == HiddenExclude
}

func clonePath(path *Path) *Path {
	if path == nil {
		return nil
	}
	return new(*path)
}
