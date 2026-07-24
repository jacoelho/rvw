package review

import (
	"errors"
	"fmt"
	"iter"
)

// ErrInvalidSnapshot identifies an invalid snapshot failure.
var ErrInvalidSnapshot = errors.New("invalid review snapshot")

// HiddenReason represents hidden reason.
type HiddenReason string

// HiddenBuiltinDefault identifies a supported hidden builtin default value.
const (
	HiddenBuiltinDefault HiddenReason = "builtin-default"
	HiddenGitignore      HiddenReason = "gitignore"
	HiddenRVWIgnore      HiddenReason = "rvwignore"
	HiddenInclude        HiddenReason = "include"
	HiddenExclude        HiddenReason = "exclude"
)

// HiddenReasons represents hidden reasons.
type HiddenReasons struct {
	bits uint8
}

// NewHiddenReasons constructs and validates hidden reasons.
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

// Has performs the has operation.
func (reasons HiddenReasons) Has(value HiddenReason) bool {
	bit, ok := hiddenReasonBit(value)
	return ok && reasons.bits&bit != 0
}

// Values returns the receiver's values value.
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

// FileTrait represents file trait.
type FileTrait uint8

// TraitGenerated identifies a supported trait generated value.
const (
	TraitGenerated FileTrait = iota + 1
	TraitVendored
	TraitLockFile
	TraitMinified
	TraitTooLarge
)

// FileTraits represents file traits.
type FileTraits struct {
	bits uint8
}

// NewFileTraits constructs and validates file traits.
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

// Has performs the has operation.
func (traits FileTraits) Has(value FileTrait) bool {
	return value >= TraitGenerated && value <= TraitTooLarge &&
		traits.bits&(1<<(value-1)) != 0
}

func (traits FileTraits) valid() bool {
	const validBits = 1<<TraitTooLarge - 1
	return traits.bits&^validBits == 0
}

// Flags represents flags.
type Flags struct {
	traits FileTraits
	hidden HiddenReasons
}

// NewFlags constructs and validates flags.
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

// Hidden reports the receiver's hidden state.
func (flags Flags) Hidden() bool {
	return flags.hidden.bits != 0
}

// Traits returns the receiver's traits value.
func (flags Flags) Traits() FileTraits {
	return flags.traits
}

// HiddenReasons returns the receiver's hidden reasons value.
func (flags Flags) HiddenReasons() []HiddenReason {
	return flags.hidden.Values()
}

// Clone returns the receiver's clone value.
func (flags Flags) Clone() Flags {
	return flags
}

func (flags Flags) valid() bool {
	_, err := NewFlags(flags.traits, flags.hidden)
	return err == nil
}

// File represents file.
type File struct {
	contents    FileContents
	oldPath     *Path
	newPath     *Path
	change      Change
	patchHandle PatchHandle
	id          FileID
	flags       Flags
}

// FileContent represents file content.
type FileContent struct {
	oid  ObjectID
	side LineSide
}

// OldFileContent performs the old file content operation.
func OldFileContent(oid ObjectID) FileContent {
	return FileContent{side: LineOld, oid: oid}
}

// NewFileContent constructs and validates file content.
func NewFileContent(oid ObjectID) FileContent {
	return FileContent{side: LineNew, oid: oid}
}

// FileContents represents file contents.
type FileContents struct {
	semantic *FileSemanticIdentity
	old      *ObjectID
	new      *ObjectID
}

// FileSemanticStage is one validated non-zero Git index stage used only for
// source identity. Its fields are intentionally opaque to review consumers.
type FileSemanticStage struct {
	object GitObject
	stage  uint8
}

// NewFileSemanticStage constructs one validated unmerged-index stage.
func NewFileSemanticStage(stage uint8, object GitObject) (FileSemanticStage, error) {
	if stage < 1 || stage > 3 {
		return FileSemanticStage{}, fmt.Errorf("%w: semantic stage must be 1, 2, or 3", ErrInvalidSnapshot)
	}
	if _, err := NewGitObject(object.kind, object.oid, object.mode); err != nil {
		return FileSemanticStage{}, fmt.Errorf("%w: semantic stage object is invalid", ErrInvalidSnapshot)
	}
	return FileSemanticStage{stage: stage, object: object}, nil
}

// FileSemanticIdentity retains an opaque validated tuple that participates in
// reload identity without becoming patchable file content.
type FileSemanticIdentity struct {
	stages [3]GitObject
	bits   uint8
}

const fileSemanticIdentityRetainedBytes int64 = 128

// NewFileSemanticIdentity constructs a non-empty unique stage tuple.
func NewFileSemanticIdentity(stages ...FileSemanticStage) (FileSemanticIdentity, error) {
	var identity FileSemanticIdentity
	for _, stage := range stages {
		if stage.stage < 1 || stage.stage > 3 {
			return FileSemanticIdentity{}, fmt.Errorf("%w: semantic stage is invalid", ErrInvalidSnapshot)
		}
		bit := uint8(1 << (stage.stage - 1))
		if identity.bits&bit != 0 {
			return FileSemanticIdentity{}, fmt.Errorf("%w: duplicate semantic stage %d", ErrInvalidSnapshot, stage.stage)
		}
		if _, err := NewGitObject(stage.object.kind, stage.object.oid, stage.object.mode); err != nil {
			return FileSemanticIdentity{}, fmt.Errorf("%w: semantic stage object is invalid", ErrInvalidSnapshot)
		}
		identity.bits |= bit
		identity.stages[stage.stage-1] = stage.object
	}
	if identity.bits == 0 {
		return FileSemanticIdentity{}, fmt.Errorf("%w: semantic identity is empty", ErrInvalidSnapshot)
	}
	return identity, nil
}

// NewFileContents constructs and validates file contents.
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

// NewFileContentsWithSemanticIdentity constructs file contents whose opaque
// unmerged-stage tuple also participates in reload identity.
func NewFileContentsWithSemanticIdentity(
	identity FileSemanticIdentity,
	values ...FileContent,
) (FileContents, error) {
	if identity.bits == 0 {
		return FileContents{}, fmt.Errorf("%w: semantic identity is invalid", ErrInvalidSnapshot)
	}
	contents, err := NewFileContents(values...)
	if err != nil {
		return FileContents{}, err
	}
	contents.semantic = new(identity)
	return contents, nil
}

// Object performs the object operation.
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
	contents.semantic = cloneValue(contents.semantic)
	contents.old = cloneValue(contents.old)
	contents.new = cloneValue(contents.new)
	return contents
}

// NewFile constructs and validates file.
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
	if oldPath != nil && oldPath.raw == "" || newPath != nil && newPath.raw == "" {
		return File{}, fmt.Errorf("%w: file path is invalid", ErrInvalidSnapshot)
	}
	if !change.valid() {
		return File{}, fmt.Errorf("%w: file change is invalid", ErrInvalidSnapshot)
	}
	if !flags.valid() {
		return File{}, fmt.Errorf("%w: file flags are invalid", ErrInvalidSnapshot)
	}
	if contents.semantic != nil && contents.semantic.bits == 0 {
		return File{}, fmt.Errorf("%w: file semantic identity is invalid", ErrInvalidSnapshot)
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

// ID returns the receiver's id value.
func (file File) ID() FileID {
	return file.id
}

// OldPath returns the receiver's old path value.
func (file File) OldPath() (Path, bool) {
	if file.oldPath == nil {
		return Path{}, false
	}
	return *file.oldPath, true
}

// NewPath constructs and validates path.
func (file File) NewPath() (Path, bool) {
	if file.newPath == nil {
		return Path{}, false
	}
	return *file.newPath, true
}

// DisplayPath returns the receiver's display path value.
func (file File) DisplayPath() string {
	if file.newPath != nil {
		return file.newPath.Display()
	}
	return file.oldPath.Display()
}

// IdentityPath returns the receiver's identity path value.
func (file File) IdentityPath() Path {
	if file.newPath != nil {
		return *file.newPath
	}
	return *file.oldPath
}

// Change returns the receiver's change value.
func (file File) Change() Change {
	return file.change
}

// Flags returns the receiver's flags value.
func (file File) Flags() Flags {
	return file.flags.Clone()
}

// PatchHandle returns the receiver's patch handle value.
func (file File) PatchHandle() PatchHandle {
	return file.patchHandle
}

// Content performs the content operation.
func (file File) Content(side LineSide) (ObjectID, bool) {
	return file.contents.Object(side)
}

// Snapshot represents snapshot.
type Snapshot struct {
	remoteCommentAnchors  map[commentAnchor]struct{}
	pendingReview         *PendingReview
	remoteThreadDetail    OperationDetail
	pendingReviewDetail   OperationDetail
	files                 []File
	remoteThreads         []RemoteThread
	remoteThreadPositions []remoteThreadPosition
	pathProjection        remotePathProjection
	sourceID              SourceID
	retainedBytes         int64
	remoteRetainedBytes   int64
	pendingRetainedBytes  int64
	remoteThreadStatus    RemoteThreadStatus
	pendingReviewStatus   PendingReviewStatus
}

// NewSnapshot constructs and validates snapshot.
func NewSnapshot(sourceID SourceID, files []File) (Snapshot, error) {
	owned := make([]File, len(files))
	copy(owned, files)
	return newSnapshotCancellable(nil, sourceID, owned)
}

func newSnapshotCancellable(
	cancelled func() error,
	sourceID SourceID,
	owned []File,
) (Snapshot, error) {
	if err := cancellationError(cancelled); err != nil {
		return Snapshot{}, err
	}
	if sourceID == 0 {
		return Snapshot{}, fmt.Errorf("%w: source ID must be positive", ErrInvalidSnapshot)
	}
	if len(owned) > MaxFiles {
		return Snapshot{}, fmt.Errorf("%w: %d files exceed limit %d", ErrInvalidSnapshot, len(owned), MaxFiles)
	}

	var pathBytes int64
	var semanticBytes int64
	aliases := make([]remotePathAlias, 0, len(owned)*2)
	objectIDWidth := 0
	patchIDs := make(map[PatchID]bool, len(owned))
	for index, file := range owned {
		if index%16 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return Snapshot{}, err
			}
		}
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
		if file.contents.semantic != nil {
			semanticBytes += fileSemanticIdentityRetainedBytes
			for index, object := range file.contents.semantic.stages {
				if file.contents.semantic.bits&(1<<index) != 0 {
					semanticBytes += int64(len(object.oid) + len(object.mode))
				}
			}
		}
		if path, ok := file.OldPath(); ok {
			pathBytes += int64(len(path.raw))
			aliases = append(aliases, remotePathAlias{
				path: path.raw, file: file.ID(), status: RemoteMappingMapped,
			})
		}
		if path, ok := file.NewPath(); ok {
			pathBytes += int64(len(path.raw))
			if old, oldOK := file.OldPath(); !oldOK || old.raw != path.raw {
				aliases = append(aliases, remotePathAlias{
					path: path.raw, file: file.ID(), status: RemoteMappingMapped,
				})
			}
		}
		if pathBytes > MaxSourcePaths {
			return Snapshot{}, fmt.Errorf("%w: raw paths exceed %d bytes", ErrInvalidSnapshot, MaxSourcePaths)
		}
	}
	projection, err := newRemotePathProjectionCancellable(cancelled, aliases)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		sourceID:       sourceID,
		files:          owned,
		pathProjection: projection,
		retainedBytes: int64(cap(owned))*256 + pathBytes + semanticBytes +
			projection.metadataBytes,
	}
	if snapshot.RetainedBytes() > MaxBaseSnapshotRetainedBytes {
		return Snapshot{}, fmt.Errorf(
			"%w: snapshot exceeds %d retained bytes",
			ErrInvalidSnapshot,
			MaxBaseSnapshotRetainedBytes,
		)
	}
	return snapshot, nil
}

const (
	// MaxBaseSnapshotRetainedBytes bounds files and paths before remote state.
	MaxBaseSnapshotRetainedBytes int64 = MaxSourcePaths +
		MaxFiles*256 + 2*MaxFiles*remotePathAliasStorageBytes
	// MaxSnapshotRetainedBytes bounds one complete source snapshot, including
	// remote threads, pending review state, anchors, and acquisition details.
	MaxSnapshotRetainedBytes int64 = 96 << 20
)

// SnapshotBuilderConstructionBytes returns the file and cached-path-projection
// ownership simultaneously retained while a builder is finished.
func SnapshotBuilderConstructionBytes(fileCount int) int64 {
	if fileCount < 0 || fileCount > MaxFiles {
		return 0
	}
	return int64(fileCount) * (256 + 2*remotePathAliasStorageBytes)
}

// RetainedBytes returns the conservative retained size of the complete snapshot.
func (snapshot Snapshot) RetainedBytes() int64 {
	return snapshot.retainedBytes
}

func (snapshot Snapshot) withRemoteRetainedBytes(
	retained int64,
	cause error,
) (Snapshot, error) {
	next := snapshot
	total, err := replaceSnapshotRetainedComponent(
		next.retainedBytes,
		next.remoteRetainedBytes,
		retained,
		cause,
	)
	if err != nil {
		return Snapshot{}, err
	}
	next.retainedBytes = total
	next.remoteRetainedBytes = retained
	return next.validateRetainedBytes(cause)
}

func (snapshot Snapshot) withPendingRetainedBytes(
	retained int64,
	cause error,
) (Snapshot, error) {
	next, err := snapshot.stagePendingRetainedBytes(retained, cause)
	if err != nil {
		return Snapshot{}, err
	}
	return next.validateRetainedBytes(cause)
}

func (snapshot Snapshot) stagePendingRetainedBytes(
	retained int64,
	cause error,
) (Snapshot, error) {
	next := snapshot
	total, err := replaceSnapshotRetainedComponent(
		next.retainedBytes,
		next.pendingRetainedBytes,
		retained,
		cause,
	)
	if err != nil {
		return Snapshot{}, err
	}
	next.retainedBytes = total
	next.pendingRetainedBytes = retained
	return next, nil
}

func replaceSnapshotRetainedComponent(
	total int64,
	previous int64,
	next int64,
	cause error,
) (int64, error) {
	if total < 0 || previous < 0 || next < 0 || total < previous {
		return 0, fmt.Errorf("%w: snapshot retained cache is invalid", cause)
	}
	return total - previous + next, nil
}

func (snapshot Snapshot) validateRetainedBytes(cause error) (Snapshot, error) {
	if snapshot.retainedBytes <= MaxSnapshotRetainedBytes {
		return snapshot, nil
	}
	return Snapshot{}, fmt.Errorf(
		"%w: complete snapshot exceeds %d retained bytes",
		cause,
		MaxSnapshotRetainedBytes,
	)
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

// SourceID returns the receiver's source id value.
func (snapshot Snapshot) SourceID() SourceID {
	return snapshot.sourceID
}

// FileCount returns the receiver's file count value.
func (snapshot Snapshot) FileCount() int {
	return len(snapshot.files)
}

// File performs the file operation.
func (snapshot Snapshot) File(id FileID) (File, bool) {
	if int(id.Uint32()) >= len(snapshot.files) {
		return File{}, false
	}
	file := snapshot.files[id]
	return file, file.ID() == id
}

// AllFiles returns the receiver's all files value.
func (snapshot Snapshot) AllFiles() iter.Seq[File] {
	return func(yield func(File) bool) {
		for _, file := range snapshot.files {
			if !yield(file) {
				return
			}
		}
	}
}

// SnapshotBuilder represents snapshot builder.
type SnapshotBuilder struct {
	files     []File
	sourceID  SourceID
	pathBytes int64
	finished  bool
}

// NewSnapshotBuilder constructs and validates snapshot builder.
func NewSnapshotBuilder(sourceID SourceID, capacity int) (*SnapshotBuilder, error) {
	if sourceID == 0 || capacity < 0 || capacity > MaxFiles {
		return nil, ErrInvalidSnapshot
	}
	return &SnapshotBuilder{sourceID: sourceID, files: make([]File, 0, capacity)}, nil
}

// AddFile performs the add file operation.
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

// AddFileWithContents performs the add file with contents operation.
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
	fileID := FileID(uint32FromInt(len(builder.files)))
	patchID := PatchID(len(builder.files) + 1)
	handle := PatchHandle{id: patchID, sourceID: builder.sourceID, fileID: fileID}
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

// Finish returns the receiver's finish value.
func (builder *SnapshotBuilder) Finish() (Snapshot, error) {
	return builder.FinishCancellable(nil)
}

// FinishCancellable consumes the builder while bounding validation and
// projection-sort cancellation latency.
func (builder *SnapshotBuilder) FinishCancellable(
	cancelled func() error,
) (Snapshot, error) {
	if builder == nil || builder.finished {
		return Snapshot{}, ErrInvalidSnapshot
	}
	builder.finished = true
	files := builder.files
	builder.files = nil
	return newSnapshotCancellable(cancelled, builder.sourceID, files)
}

func clonePath(path *Path) *Path {
	if path == nil {
		return nil
	}
	return new(*path)
}
