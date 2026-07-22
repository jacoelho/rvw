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
	baseWorkspace WorkspaceRevision
	baseSource    SourceID
	baseState     uint64
	report        ReloadReport
	valid         bool
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
		if err := step(); err != nil {
			return PreparedReplacement{}, err
		}
	}
	preparation.next.active = remapActive(
		review.active,
		preparation.remapped,
		snapshot,
		&preparation.report,
	)
	preparation.next.selected = remapSelection(
		review.selected,
		preparation.remapped,
		preparation.next.active,
	)
	return PreparedReplacement{
		baseWorkspace: review.workspace,
		baseSource:    review.snapshot.SourceID(),
		baseState:     review.state,
		next:          preparation.next,
		report:        preparation.report,
		valid:         true,
	}, nil
}

type replacementPreparation struct {
	cancelled func() error
	aliases   map[string]File
	remapped  map[FileID]FileID
	snapshot  Snapshot
	original  Review
	next      Review
	report    ReloadReport
}

func newReplacementPreparation(
	cancelled func() error,
	review Review,
	snapshot Snapshot,
) (*replacementPreparation, error) {
	if err := validateReplacementInput(cancelled, review, snapshot); err != nil {
		return nil, err
	}
	if _, err := uniqueFilesByAlias(cancelled, review.snapshot); err != nil {
		return nil, err
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
	next.patches = make(map[FileID]PatchState)
	next.operations = maps.Clone(review.operations)
	next.search = searchState{status: SearchIdle}
	return &replacementPreparation{
		cancelled: cancelled,
		aliases:   aliases,
		remapped:  remapped,
		original:  review,
		next:      next,
		snapshot:  snapshot,
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
	newAliases map[string]File,
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
		var matched *File
		for _, path := range fileAliases(oldFile) {
			newFile, exists := newAliases[path]
			if !exists {
				continue
			}
			if matched != nil && matched.ID() != newFile.ID() {
				return nil, fmt.Errorf(
					"%w: old file aliases map to multiple replacements",
					ErrInvalidReload,
				)
			}
			matchedCopy := newFile
			matched = &matchedCopy
		}
		if matched != nil {
			remapped[oldFile.ID()] = matched.ID()
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
	for index := range preparation.next.comments {
		if index%2_048 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return err
			}
		}
		comment := &preparation.next.comments[index]
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
		newFile, exists := preparation.aliases[path.raw]
		if !exists {
			comment.target = comment.target.withoutFileMapping()
			comment.unmappable = true
			preparation.report.CommentsUnmappable++
			continue
		}
		oldFileID, oldFileMapped := comment.target.FileID()
		oldFile, oldFileExists := preparation.original.snapshot.File(oldFileID)
		comment.target = comment.target.remap(newFile.ID(), newFile.IdentityPath())
		comment.unmappable = false
		if comment.target.Kind() == TargetLine &&
			(!oldFileMapped || !oldFileExists ||
				!sameLineSideContentIdentity(oldFile, newFile, comment.target.side)) {
			comment.stale = true
			preparation.report.CommentsStale++
		}
	}
	preparation.next.commentAnchors = indexCommentAnchors(preparation.next.comments)
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
		newFile, exists := preparation.aliases[path.raw]
		if !exists {
			return fmt.Errorf("%w: active draft target no longer exists", ErrInvalidReload)
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
	leftObject, leftExists := left.Content(side)
	rightObject, rightExists := right.Content(side)
	return leftExists && rightExists && leftObject == rightObject
}

func sameFileContentIdentity(left, right File) bool {
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
) (map[string]File, error) {
	files := make(map[string]File, snapshot.FileCount()*2)
	count := 0
	for file := range snapshot.AllFiles() {
		if count%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return nil, err
			}
		}
		count++
		for _, path := range fileAliases(file) {
			if existing, exists := files[path]; exists && existing.ID() != file.ID() {
				return nil, fmt.Errorf("%w: path identity is ambiguous", ErrInvalidReload)
			}
			files[path] = file
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
	active *FileID,
	remapped map[FileID]FileID,
	snapshot Snapshot,
	report *ReloadReport,
) *FileID {
	if active != nil {
		if id, exists := remapped[*active]; exists {
			report.ActivePreserved = true
			return new(id)
		}
	}
	for file := range snapshot.AllFiles() {
		if !file.Flags().Hidden() {
			id := file.ID()
			return new(id)
		}
	}
	if snapshot.FileCount() != 0 {
		id := snapshot.files[0].ID()
		return new(id)
	}
	return nil
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
