package review

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
)

var ErrInvalidReload = errors.New("invalid reload replacement")
var ErrStaleReloadPreparation = errors.New("reload preparation is stale")

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

type PreparedReplacement struct {
	baseWorkspace WorkspaceRevision
	baseSource    SourceID
	baseState     uint64
	next          Review
	report        ReloadReport
	valid         bool
}

func PrepareReplacement(review Review, snapshot Snapshot) (PreparedReplacement, error) {
	return PrepareReplacementCancellable(nil, review, snapshot)
}

func PrepareReplacementCancellable(
	cancelled func() error,
	review Review,
	snapshot Snapshot,
) (PreparedReplacement, error) {
	if err := cancellationError(cancelled); err != nil {
		return PreparedReplacement{}, err
	}
	if snapshot.SourceID() == 0 || snapshot.SourceID() == review.snapshot.SourceID() {
		return PreparedReplacement{}, fmt.Errorf(
			"%w: replacement requires a new source identity",
			ErrInvalidReload,
		)
	}
	if review.workspace.Uint64() == math.MaxUint64 {
		return PreparedReplacement{}, fmt.Errorf("%w: workspace revision exhausted", ErrInvalidReload)
	}
	if review.blocksReplacement() {
		return PreparedReplacement{}, fmt.Errorf(
			"%w: export or publication is unresolved",
			ErrInvalidReload,
		)
	}

	_, err := uniqueFilesByAlias(cancelled, review.snapshot)
	if err != nil {
		return PreparedReplacement{}, err
	}
	newAliases, err := uniqueFilesByAlias(cancelled, snapshot)
	if err != nil {
		return PreparedReplacement{}, err
	}
	remapped := make(map[FileID]FileID, min(review.snapshot.FileCount(), snapshot.FileCount()))
	fileCount := 0
	for oldFile := range review.snapshot.AllFiles() {
		if fileCount%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return PreparedReplacement{}, err
			}
		}
		fileCount++
		var matched *File
		for _, path := range fileAliases(oldFile) {
			newFile, exists := newAliases[path]
			if !exists {
				continue
			}
			if matched != nil && matched.ID() != newFile.ID() {
				return PreparedReplacement{}, fmt.Errorf(
					"%w: old file aliases map to multiple replacements",
					ErrInvalidReload,
				)
			}
			copy := newFile
			matched = &copy
		}
		if matched != nil {
			remapped[oldFile.ID()] = matched.ID()
		}
	}

	next := review.clone()
	next.workspace = WorkspaceRevision(review.workspace.Uint64() + 1)
	next.snapshot = snapshot
	next.fileMarks = fileMarkSet{}
	next.hunkMarks = hunkMarkSet{}
	next.patches = make(map[FileID]PatchState)
	next.operations = maps.Clone(review.operations)
	next.search = searchState{status: SearchIdle}

	report := ReloadReport{
		FilesPreserved: uint32(len(remapped)),
		FilesRemoved:   uint32(review.snapshot.FileCount() - len(remapped)),
	}
	var preparationErr error
	markCount := 0
	review.hunkMarks.each(func(oldHunk HunkID) bool {
		if markCount%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				preparationErr = err
				return false
			}
		}
		markCount++
		newFileID, remappedFile := remapped[oldHunk.FileID()]
		oldFile, oldExists := review.snapshot.File(oldHunk.FileID())
		newFile, newExists := snapshot.File(newFileID)
		patch := review.Patch(oldHunk.FileID())
		if !remappedFile || !oldExists || !newExists ||
			!sameFileContentIdentity(oldFile, newFile) ||
			patch.Status != PatchReady ||
			oldHunk.Index().Uint32() >= patch.Hunks {
			report.HunkMarksStale++
			return true
		}
		newHunk, err := NewHunkID(newFileID, oldHunk.Index())
		if err != nil {
			preparationErr = err
			return false
		}
		next.hunkMarks.add(newHunk)
		return true
	})
	if preparationErr != nil {
		return PreparedReplacement{}, preparationErr
	}
	operationCount := 0
	for id, operation := range next.operations {
		if operationCount%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return PreparedReplacement{}, err
			}
		}
		operationCount++
		if operation.Status != OperationPending ||
			operation.Kind != OperationPatch &&
				operation.Kind != OperationSearch &&
				operation.Kind != OperationSyntax {
			continue
		}
		operation.Status = OperationSuperseded
		operation.Detail = mustOperationDetail("source revision was replaced")
		next.operations[id] = operation
		report.OperationsSuperseded++
	}
	markCount = 0
	review.fileMarks.each(func(oldID FileID) bool {
		if markCount%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				preparationErr = err
				return false
			}
		}
		markCount++
		newID, remappedFile := remapped[oldID]
		oldFile, oldExists := review.snapshot.File(oldID)
		newFile, newExists := snapshot.File(newID)
		if remappedFile && oldExists && newExists &&
			sameFileContentIdentity(oldFile, newFile) {
			next.fileMarks.add(newID)
		} else {
			report.FileMarksStale++
		}
		return true
	})
	if preparationErr != nil {
		return PreparedReplacement{}, preparationErr
	}
	next.active = remapActive(review.active, remapped, snapshot, &report)
	next.selected = remapSelection(review.selected, remapped, next.active)

	next.comments = slices.Clone(review.comments)
	for index := range next.comments {
		if index%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return PreparedReplacement{}, err
			}
		}
		comment := &next.comments[index]
		if comment.target.Kind() == TargetReview {
			continue
		}
		path, exists := comment.target.AnchoredPath()
		if !exists {
			oldID, _ := comment.target.FileID()
			oldFile, valid := review.file(oldID)
			if !valid {
				return PreparedReplacement{}, fmt.Errorf(
					"%w: comment target has no path identity",
					ErrInvalidReload,
				)
			}
			path = oldFile.IdentityPath()
		}
		newFile, exists := newAliases[path.raw]
		if !exists {
			comment.target = comment.target.withoutFileMapping()
			comment.unmappable = true
			report.CommentsUnmappable++
			continue
		}
		oldFileID, oldFileMapped := comment.target.FileID()
		oldFile, oldFileExists := review.snapshot.File(oldFileID)
		comment.target = comment.target.remap(newFile.ID(), newFile.IdentityPath())
		comment.unmappable = false
		if comment.target.Kind() == TargetLine &&
			(!oldFileMapped || !oldFileExists ||
				!sameLineSideContentIdentity(oldFile, newFile, comment.target.side)) {
			comment.stale = true
			report.CommentsStale++
		}
	}
	next.commentAnchors = indexCommentAnchors(next.comments)

	if review.draft != nil {
		draft := *review.draft
		if draft.target.Kind() != TargetReview {
			path, exists := draft.target.AnchoredPath()
			if !exists {
				return PreparedReplacement{}, fmt.Errorf(
					"%w: active draft target has no path identity",
					ErrInvalidReload,
				)
			}
			newFile, exists := newAliases[path.raw]
			if !exists {
				return PreparedReplacement{}, fmt.Errorf(
					"%w: active draft target no longer exists",
					ErrInvalidReload,
				)
			}
			if draft.target.Kind() == TargetLine {
				oldFileID, oldFileMapped := draft.target.FileID()
				oldFile, oldFileExists := review.snapshot.File(oldFileID)
				if !oldFileMapped || !oldFileExists ||
					!sameLineSideContentIdentity(oldFile, newFile, draft.target.side) {
					return PreparedReplacement{}, fmt.Errorf(
						"%w: active line draft content changed",
						ErrInvalidReload,
					)
				}
			}
			draft.target = draft.target.remap(newFile.ID(), newFile.IdentityPath())
		}
		next.draft = new(draft)
		report.DraftPreserved = true
	}
	return PreparedReplacement{
		baseWorkspace: review.workspace,
		baseSource:    review.snapshot.SourceID(),
		baseState:     review.state,
		next:          next,
		report:        report,
		valid:         true,
	}, nil
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
