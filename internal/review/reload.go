package review

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
)

// ErrInvalidReload identifies an invalid reload failure.
var ErrInvalidReload = errors.New("invalid reload replacement")

// ErrStaleReloadPreparation identifies an stale reload preparation failure.
var ErrStaleReloadPreparation = errors.New("reload preparation is stale")

// ReloadReport describes reload report.
type ReloadReport struct {
	FilesPreserved       uint32
	FilesRemoved         uint32
	FileMarksStale       uint32
	HunkMarksStale       uint32
	OperationsSuperseded uint32
	CommentsStale        uint32
	CommentsUnmappable   uint32
	ActivePreserved      bool
	DraftPreserved       bool
}

// PreparedReplacement represents prepared replacement.
type PreparedReplacement struct {
	next          Review
	snapshotDelta int64
	baseWorkspace WorkspaceRevision
	baseSource    SourceID
	baseState     uint64
	report        ReloadReport
	valid         bool
}

// RetainedBytes returns the replacement-owned review state excluding the
// candidate snapshot, which is accounted once by its containing result.
func (prepared PreparedReplacement) RetainedBytes() int64 {
	if !prepared.valid {
		return 0
	}
	return prepared.next.retainedBytesWithoutSnapshot() + prepared.snapshotDelta
}

func (review Review) retainedBytesWithoutSnapshot() int64 {
	retained := int64(512 + cap(review.comments)*128 + len(review.commentAnchors)*32 +
		len(review.operations)*48 + cap(review.operationIDs)*8 +
		cap(review.fileMarks.words)*8 + cap(review.hunkMarks.chunks)*8)
	for _, comment := range review.comments {
		retained += int64(len(comment.body.String()))
		if path, ok := comment.target.AnchoredPath(); ok {
			retained += int64(len(path.raw))
		}
	}
	if review.draft != nil {
		retained += int64(len(review.draft.body) + 128)
		if path, ok := review.draft.target.AnchoredPath(); ok {
			retained += int64(len(path.raw))
		}
	}
	retained += review.patch.retainedBytes
	for _, operation := range review.operations {
		retained += operation.Detail.Bytes()
	}
	for _, chunk := range review.hunkMarks.chunks {
		if chunk == nil {
			continue
		}
		retained += int64(len(chunk)) * 32
		for _, file := range chunk {
			retained += int64(cap(file.words)) * 8
		}
	}
	retained += int64(len(review.search.query)) + review.search.detail.Bytes() + review.search.hitBytes
	retained += review.publication.detail.Bytes() + int64(
		len(review.publication.marker)+len(review.publication.target.repository)+
			len(review.publication.target.base)+len(review.publication.target.head),
	)
	retained += review.publication.checked.RetainedBytes()
	if review.publication.continuation != nil {
		retained += review.publication.continuation.checked.RetainedBytes()
	}
	return retained + review.pendingRetainedBytes()
}

func (review Review) pendingRetainedBytes() int64 {
	return pendingStateRetainedBytes(review.pending)
}

// ReplacementConstructionBytes returns a conservative bound for the owned
// replacement and the transient alias, remap, comment, and pending-merge
// indexes used while constructing it. The candidate snapshot is excluded.
func ReplacementConstructionBytes(current Review, candidate Snapshot) int64 {
	oldFiles := int64(current.snapshot.FileCount())
	newFiles := int64(candidate.FileCount())
	comments := int64(len(current.comments))
	pendingBytes := int64(0)
	pendingComments := int64(0)
	if candidate.pendingReview != nil {
		pendingBytes = candidate.pendingReview.retainedBytes()
		pendingComments = int64(len(candidate.pendingReview.comments))
	}
	result := current.retainedBytesWithoutSnapshot() + pendingBytes*2 + 1_024
	aliasScratch := (oldFiles + newFiles) * 320
	remapScratch := min(oldFiles, newFiles) * 48
	commentScratch := (comments + pendingComments) * 320
	remoteRetained, remoteScratch := staleRemoteReplacementBytes(current.snapshot, candidate)
	return result + aliasScratch + remapScratch + commentScratch +
		remoteRetained + remoteScratch
}

func staleRemoteReplacementBytes(current, candidate Snapshot) (int64, int64) {
	if candidate.remoteThreadStatus != RemoteThreadsUnavailable &&
		candidate.remoteThreadStatus != RemoteThreadsLimited ||
		len(current.remoteThreads) == 0 {
		return 0, 0
	}
	comments := 0
	for _, thread := range current.remoteThreads {
		comments += len(thread.comments)
	}
	pendingComments := 0
	if candidate.pendingReview != nil {
		pendingComments = len(candidate.pendingReview.comments)
	}
	scratch := int64(len(current.remoteThreads)+comments)*64 +
		int64(len(current.remoteThreads)+comments)*96 +
		int64(pendingComments)*64 + 1_024
	return current.remoteRetainedBytes, scratch
}

// PrepareReplacement prepares replacement.
func PrepareReplacement(review Review, snapshot Snapshot) (PreparedReplacement, error) {
	return PrepareReplacementCancellable(nil, review, snapshot)
}

// PrepareReplacementCancellable prepares replacement cancellable.
func PrepareReplacementCancellable(
	cancelled func() error,
	review Review,
	snapshot Snapshot,
) (PreparedReplacement, error) {
	preparation, err := newReplacementPreparation(cancelled, review, snapshot)
	if err != nil {
		return PreparedReplacement{}, err
	}
	for _, step := range []func() error{
		preparation.preserveHunkMarks,
		preparation.supersedeOperations,
		preparation.preserveFileMarks,
		preparation.remapComments,
		preparation.mergePendingReview,
		preparation.preserveDraft,
	} {
		if stepErr := step(); stepErr != nil {
			return PreparedReplacement{}, stepErr
		}
	}
	err = preparation.next.rebuildCommentAnchorsCancellable(cancelled)
	if err != nil {
		return PreparedReplacement{}, err
	}
	active, hasActive, err := remapActive(
		cancelled,
		review.active,
		preparation.remapped,
		snapshot,
		&preparation.report,
	)
	if err != nil {
		return PreparedReplacement{}, err
	}
	preparation.next.active = nil
	if hasActive {
		preparation.next.active = new(active)
	}
	preparation.next.selected = remapSelection(
		review.selected,
		preparation.remapped,
		preparation.next.active,
	)
	preparation.next.revision = review.revision + 1
	preparation.next.activeMutation = 0
	preparation.next.mutationKind = 0
	preparation.next.publication.pendingRefresh = false
	preparation.next.refresh()
	snapshotDelta := preparation.next.snapshot.RetainedBytes() - preparation.candidateRetained
	snapshotDelta = max(snapshotDelta, 0)
	return PreparedReplacement{
		baseWorkspace: review.workspace,
		baseSource:    review.snapshot.SourceID(),
		baseState:     review.state,
		next:          preparation.next,
		report:        preparation.report,
		snapshotDelta: snapshotDelta,
		valid:         true,
	}, nil
}

type replacementPreparation struct {
	cancelled         func() error
	aliases           map[string]FileID
	remapped          map[FileID]FileID
	commentIndexes    map[CommentID]int
	pendingFiles      remotePathProjection
	snapshot          Snapshot
	original          Review
	next              Review
	report            ReloadReport
	candidateRetained int64
}

func newReplacementPreparation(
	cancelled func() error,
	review Review,
	snapshot Snapshot,
) (*replacementPreparation, error) {
	candidateRetained := snapshot.RetainedBytes()
	var err error
	snapshot, err = retainRemoteThreadsAfterFailure(cancelled, review.snapshot, snapshot)
	if err != nil {
		return nil, err
	}
	if validationErr := validateReplacementInput(cancelled, review, snapshot); validationErr != nil {
		return nil, validationErr
	}
	if _, aliasErr := uniqueFilesByAlias(cancelled, review.snapshot); aliasErr != nil {
		return nil, aliasErr
	}
	aliases, err := uniqueFilesByAlias(cancelled, snapshot)
	if err != nil {
		return nil, err
	}
	remapped, err := remapReplacementFiles(cancelled, review.snapshot, snapshot, aliases)
	if err != nil {
		return nil, err
	}
	next := review.clone()
	next.workspace = WorkspaceRevision(review.workspace.Uint64() + 1)
	next.snapshot = snapshot
	next.fileMarks = fileMarkSet{}
	next.hunkMarks = hunkMarkSet{}
	next.patch = presentationPatch{}
	next.operations = maps.Clone(review.operations)
	next.search = searchState{status: SearchIdle}
	return &replacementPreparation{
		cancelled:         cancelled,
		candidateRetained: candidateRetained,
		aliases:           aliases,
		remapped:          remapped,
		pendingFiles:      snapshot.remotePathMappings(),
		original:          review,
		next:              next,
		snapshot:          snapshot,
		report: ReloadReport{
			FilesPreserved: uint32FromInt(len(remapped)),
			FilesRemoved:   uint32FromInt(review.snapshot.FileCount() - len(remapped)),
		},
	}, nil
}

func validateReplacementInput(cancelled func() error, review Review, snapshot Snapshot) error {
	if err := cancellationError(cancelled); err != nil {
		return err
	}
	if snapshot.SourceID() == 0 || snapshot.SourceID() == review.snapshot.SourceID() {
		return fmt.Errorf("%w: replacement requires a new source identity", ErrInvalidReload)
	}
	if review.workspace.Uint64() == math.MaxUint64 {
		return fmt.Errorf("%w: workspace revision exhausted", ErrInvalidReload)
	}
	if review.blocksReplacement() {
		return fmt.Errorf("%w: export or publication is unresolved", ErrInvalidReload)
	}
	return nil
}

func remapReplacementFiles(
	cancelled func() error,
	oldSnapshot Snapshot,
	newSnapshot Snapshot,
	newAliases map[string]FileID,
) (map[FileID]FileID, error) {
	remapped := make(map[FileID]FileID, min(oldSnapshot.FileCount(), newSnapshot.FileCount()))
	count := 0
	for oldFile := range oldSnapshot.AllFiles() {
		if count%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return nil, err
			}
		}
		count++
		var matched *FileID
		for _, path := range fileAliases(oldFile) {
			newFile, exists := newAliases[path]
			if !exists {
				continue
			}
			if matched != nil && *matched != newFile {
				return nil, fmt.Errorf(
					"%w: old file aliases map to multiple replacements",
					ErrInvalidReload,
				)
			}
			matchedCopy := newFile
			matched = &matchedCopy
		}
		if matched != nil {
			remapped[oldFile.ID()] = *matched
		}
	}
	return remapped, nil
}

func (preparation *replacementPreparation) preserveHunkMarks() error {
	var preparationErr error
	count := 0
	preparation.original.hunkMarks.each(func(oldHunk HunkID) bool {
		if count%2_048 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				preparationErr = err
				return false
			}
		}
		count++
		newFileID, remappedFile := preparation.remapped[oldHunk.FileID()]
		oldFile, oldExists := preparation.original.snapshot.File(oldHunk.FileID())
		newFile, newExists := preparation.snapshot.File(newFileID)
		patch := preparation.original.Patch(oldHunk.FileID())
		if !remappedFile || !oldExists || !newExists ||
			!sameFileContentIdentity(oldFile, newFile) || patch.Status != PatchReady ||
			oldHunk.Index().Uint32() >= patch.Hunks {
			preparation.report.HunkMarksStale++
			return true
		}
		newHunk, err := NewHunkID(newFileID, oldHunk.Index())
		if err != nil {
			preparationErr = err
			return false
		}
		addHunkMark(&preparation.next.hunkMarks, newHunk)
		return true
	})
	return preparationErr
}

func (preparation *replacementPreparation) supersedeOperations() error {
	count := 0
	for id, operation := range preparation.next.operations {
		if count%2_048 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return err
			}
		}
		count++
		if operation.Status != OperationPending ||
			operation.Kind != OperationPatch &&
				operation.Kind != OperationSearch && operation.Kind != OperationSyntax {
			continue
		}
		operation.Status = OperationSuperseded
		operation.Detail = mustOperationDetail("source revision was replaced")
		preparation.next.operations[id] = operation
		preparation.report.OperationsSuperseded++
	}
	return nil
}

func (preparation *replacementPreparation) preserveFileMarks() error {
	var preparationErr error
	count := 0
	preparation.original.fileMarks.each(func(oldID FileID) bool {
		if count%2_048 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				preparationErr = err
				return false
			}
		}
		count++
		newID, remappedFile := preparation.remapped[oldID]
		oldFile, oldExists := preparation.original.snapshot.File(oldID)
		newFile, newExists := preparation.snapshot.File(newID)
		if remappedFile && oldExists && newExists && sameFileContentIdentity(oldFile, newFile) {
			addFileMark(&preparation.next.fileMarks, newID)
		} else {
			preparation.report.FileMarksStale++
		}
		return true
	})
	return preparationErr
}

func (preparation *replacementPreparation) remapComments() error {
	preparation.next.comments = slices.Clone(preparation.original.comments)
	preparation.commentIndexes = make(map[CommentID]int, len(preparation.next.comments))
	for index := range preparation.next.comments {
		if index%2_048 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return err
			}
		}
		comment := &preparation.next.comments[index]
		preparation.commentIndexes[comment.id] = index
		if comment.target.Kind() == TargetReview {
			continue
		}
		path, exists := comment.target.AnchoredPath()
		if !exists {
			oldID, _ := comment.target.FileID()
			oldFile, valid := preparation.original.file(oldID)
			if !valid {
				return fmt.Errorf("%w: comment target has no path identity", ErrInvalidReload)
			}
			path = oldFile.IdentityPath()
		}
		newFileID, exists := preparation.aliases[path.raw]
		if !exists {
			comment.target = comment.target.withoutFileMapping()
			comment.flags = comment.flags.markUnmappable()
			preparation.report.CommentsUnmappable++
			continue
		}
		newFile, exists := preparation.snapshot.File(newFileID)
		if !exists {
			return fmt.Errorf("%w: replacement alias has no file", ErrInvalidReload)
		}
		oldFileID, oldFileMapped := comment.target.FileID()
		oldFile, oldFileExists := preparation.original.snapshot.File(oldFileID)
		comment.target = comment.target.remap(newFile.ID(), newFile.IdentityPath())
		comment.flags = comment.flags.clearUnmappable()
		if comment.target.Kind() == TargetLine &&
			(!oldFileMapped || !oldFileExists ||
				!sameLineSideContentIdentity(oldFile, newFile, comment.target.side)) {
			comment.flags = comment.flags.markStale()
			preparation.report.CommentsStale++
		}
	}
	return nil
}

func (preparation *replacementPreparation) preserveDraft() error {
	if preparation.original.draft == nil {
		return nil
	}
	draft := *preparation.original.draft
	if draft.target.Kind() != TargetReview {
		path, exists := draft.target.AnchoredPath()
		if !exists {
			return fmt.Errorf("%w: active draft target has no path identity", ErrInvalidReload)
		}
		newFileID, exists := preparation.aliases[path.raw]
		if !exists {
			return fmt.Errorf("%w: active draft target no longer exists", ErrInvalidReload)
		}
		newFile, exists := preparation.snapshot.File(newFileID)
		if !exists {
			return fmt.Errorf("%w: replacement alias has no file", ErrInvalidReload)
		}
		if draft.target.Kind() == TargetLine {
			oldFileID, oldFileMapped := draft.target.FileID()
			oldFile, oldFileExists := preparation.original.snapshot.File(oldFileID)
			if !oldFileMapped || !oldFileExists ||
				!sameLineSideContentIdentity(oldFile, newFile, draft.target.side) {
				return fmt.Errorf("%w: active line draft content changed", ErrInvalidReload)
			}
		}
		draft.target = draft.target.remap(newFile.ID(), newFile.IdentityPath())
	}
	preparation.next.draft = new(draft)
	preparation.report.DraftPreserved = true
	return nil
}

func sameLineSideContentIdentity(left, right File, side LineSide) bool {
	if !sameFileSemanticIdentity(left, right) {
		return false
	}
	leftObject, leftExists := left.Content(side)
	rightObject, rightExists := right.Content(side)
	return leftExists && rightExists && leftObject == rightObject
}

func sameFileContentIdentity(left, right File) bool {
	if !sameFileSemanticIdentity(left, right) {
		return false
	}
	compared := false
	for _, side := range []LineSide{LineOld, LineNew} {
		leftObject, leftExists := left.Content(side)
		rightObject, rightExists := right.Content(side)
		if leftExists != rightExists ||
			leftExists && leftObject != rightObject {
			return false
		}
		compared = compared || leftExists
	}
	return compared
}

func sameFileSemanticIdentity(left, right File) bool {
	if left.contents.semantic == nil || right.contents.semantic == nil {
		return left.contents.semantic == nil && right.contents.semantic == nil
	}
	return *left.contents.semantic == *right.contents.semantic
}

// CommitReplacement performs the commit replacement operation.
func (review Review) CommitReplacement(
	prepared PreparedReplacement,
) (Review, ReloadReport, error) {
	if !prepared.valid ||
		prepared.baseWorkspace != review.workspace ||
		prepared.baseSource != review.snapshot.SourceID() ||
		prepared.baseState != review.state {
		return review, ReloadReport{}, ErrStaleReloadPreparation
	}
	if review.blocksReplacement() {
		return review, ReloadReport{}, fmt.Errorf(
			"%w: export or publication is unresolved",
			ErrInvalidReload,
		)
	}
	return prepared.next, prepared.report, nil
}

// ReplaceSnapshot performs the replace snapshot operation.
func (review Review) ReplaceSnapshot(snapshot Snapshot) (Review, ReloadReport, error) {
	prepared, err := PrepareReplacement(review, snapshot)
	if err != nil {
		return review, ReloadReport{}, err
	}
	return review.CommitReplacement(prepared)
}

func uniqueFilesByAlias(
	cancelled func() error,
	snapshot Snapshot,
) (map[string]FileID, error) {
	files := make(map[string]FileID, snapshot.FileCount()*2)
	count := 0
	for file := range snapshot.AllFiles() {
		if count%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return nil, err
			}
		}
		count++
		for _, path := range fileAliases(file) {
			if existing, exists := files[path]; exists && existing != file.ID() {
				return nil, fmt.Errorf("%w: path identity is ambiguous", ErrInvalidReload)
			}
			files[path] = file.ID()
		}
	}
	return files, nil
}

func cancellationError(cancelled func() error) error {
	if cancelled == nil {
		return nil
	}
	return cancelled()
}

func fileAliases(file File) []string {
	aliases := make([]string, 0, 2)
	if path, ok := file.OldPath(); ok {
		aliases = append(aliases, path.raw)
	}
	if path, ok := file.NewPath(); ok &&
		(len(aliases) == 0 || aliases[0] != path.raw) {
		aliases = append(aliases, path.raw)
	}
	return aliases
}

func remapActive(
	cancelled func() error,
	active *FileID,
	remapped map[FileID]FileID,
	snapshot Snapshot,
	report *ReloadReport,
) (FileID, bool, error) {
	if active != nil {
		if id, exists := remapped[*active]; exists {
			report.ActivePreserved = true
			return id, true, nil
		}
	}
	count := 0
	for file := range snapshot.AllFiles() {
		if count%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return 0, false, err
			}
		}
		count++
		if !file.Flags().Hidden() {
			id := file.ID()
			return id, true, nil
		}
	}
	if snapshot.FileCount() != 0 {
		id := snapshot.files[0].ID()
		return id, true, nil
	}
	return 0, false, nil
}

func remapSelection(
	selected *FileID,
	remapped map[FileID]FileID,
	active *FileID,
) *FileID {
	if selected != nil {
		if id, exists := remapped[*selected]; exists {
			return new(id)
		}
	}
	if active == nil {
		return nil
	}
	return new(*active)
}

func (review Review) blocksReplacement() bool {
	if review.publication.status == PublicationChecking ||
		review.publication.status == PublicationChecked ||
		review.publication.status == PublicationPublishing ||
		review.publication.status == PublicationUnknown ||
		review.publication.status == PublicationReconciling {
		return true
	}
	for _, operation := range review.operations {
		switch operation.Kind {
		case OperationPatch, OperationReload, OperationSearch, OperationSyntax, OperationDelivery:
		case OperationExport:
			if operation.Status == OperationPending {
				return true
			}
		case OperationPublication, OperationReconciliation:
			if operation.Status == OperationPending || operation.Status == OperationUnknown {
				return true
			}
		}
	}
	return false
}
