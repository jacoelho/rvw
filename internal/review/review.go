package review

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrInvalidReviewTransition identifies an invalid review transition failure.
var ErrInvalidReviewTransition = errors.New("invalid review transition")

// Review represents review.
type Review struct {
	active       *FileID
	selected     *FileID
	operations   map[OperationID]Operation
	operationIDs []OperationID
	fileMarks    fileMarkSet
	hunkMarks    hunkMarkSet
	search       searchState
	patch        presentationPatch
	publication  publicationState
	snapshot     Snapshot
	commentState
	workspace  WorkspaceRevision
	state      uint64
	showHidden bool
}

// commentState is the single owner of coupled local and pending comment data.
// Worker-prepared mutations replace this value atomically after checking its
// revision; unrelated review transitions do not invalidate it.
type commentState struct {
	commentAnchors  commentAnchorCounts
	draft           *Draft
	pending         *pendingReviewState
	comments        []Comment
	commentBody     int64
	commentBytes    int64
	retainedBytes   int64
	revision        uint64
	activeMutation  OperationID
	pendingDirty    uint32
	pendingConflict uint32
	nextComment     uint32
	mutationKind    CommentMutationKind
}

// ReviewRequest describes the required immutable inputs for a review.
type ReviewRequest struct {
	Snapshot Snapshot
}

// NewReview constructs and validates review.
func NewReview(request ReviewRequest) (Review, error) {
	snapshot := request.Snapshot
	if snapshot.SourceID() == 0 {
		return Review{}, fmt.Errorf("%w: snapshot has no source", ErrInvalidReviewTransition)
	}
	workspace := WorkspaceRevision(1)
	review := Review{
		commentState: commentState{
			revision: 1,
		},
		workspace:   workspace,
		snapshot:    snapshot,
		state:       1,
		operations:  make(map[OperationID]Operation),
		search:      searchState{status: SearchIdle},
		publication: publicationState{status: PublicationIdle},
	}
	for file := range snapshot.AllFiles() {
		if !file.Flags().Hidden() {
			id := file.ID()
			review.active = new(id)
			review.selected = new(id)
			break
		}
	}
	if review.active == nil && snapshot.FileCount() != 0 {
		file, _ := snapshot.File(0)
		id := file.ID()
		review.active = new(id)
		review.selected = new(id)
	}
	current, err := importPendingReviewCancellable(review, nil)
	if err != nil {
		return Review{}, err
	}
	current.refresh()
	return current, nil
}

// WorkspaceRevision returns the receiver's workspace revision value.
func (review Review) WorkspaceRevision() WorkspaceRevision {
	return review.workspace
}

// StateRevision returns the receiver's state revision value.
func (review Review) StateRevision() uint64 {
	return review.state
}

// Snapshot returns the receiver's snapshot value.
func (review Review) Snapshot() Snapshot {
	return review.snapshot
}

// ActiveFile returns the receiver's active file value.
func (review Review) ActiveFile() (FileID, bool) {
	if review.active == nil {
		return 0, false
	}
	return *review.active, true
}

// ActivateFile performs the activate file operation.
func (review Review) ActivateFile(id FileID) (Review, error) {
	if _, ok := review.file(id); !ok {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.active = new(id)
	next.selected = new(id)
	return next, nil
}

// SelectedFile returns the receiver's selected file value.
func (review Review) SelectedFile() (FileID, bool) {
	if review.selected == nil {
		return 0, false
	}
	return *review.selected, true
}

// SelectFile performs the select file operation.
func (review Review) SelectFile(id FileID) (Review, error) {
	file, ok := review.file(id)
	if !ok || file.Flags().Hidden() && !review.showHidden {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.selected = new(id)
	return next, nil
}

// NavigationDirection describes navigation direction.
type NavigationDirection int8

// NavigatePrevious identifies a supported navigate previous value.
const (
	NavigatePrevious NavigationDirection = -1
	NavigateNext     NavigationDirection = 1
)

// ActivateSelectedFile returns the receiver's activate selected file value.
func (review Review) ActivateSelectedFile() (Review, error) {
	if review.selected == nil {
		return review, ErrInvalidReviewTransition
	}
	return review.ActivateFile(*review.selected)
}

// ToggleFileReviewed performs the toggle file reviewed operation.
func (review Review) ToggleFileReviewed(id FileID) (Review, error) {
	if _, ok := review.file(id); !ok {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.fileMarks = review.fileMarks.toggle(id)
	return next, nil
}

// ToggleHunkReviewed performs the toggle hunk reviewed operation.
func (review Review) ToggleHunkReviewed(id HunkID) (Review, error) {
	if _, ok := review.file(id.FileID()); !ok {
		return review, ErrInvalidReviewTransition
	}
	patch := review.Patch(id.FileID())
	if patch.Status != PatchReady || id.Index().Uint32() >= patch.Hunks {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.hunkMarks = review.hunkMarks.toggle(id)
	return next, nil
}

// ShowHiddenFiles returns the receiver's show hidden files value.
func (review Review) ShowHiddenFiles() Review {
	next := review.clone()
	next.showHidden = true
	return next
}

// HideHiddenFiles returns the receiver's hide hidden files value.
func (review Review) HideHiddenFiles() Review {
	next := review.clone()
	next.showHidden = false
	return next
}

// BeginDraft performs the begin draft operation.
func (review Review) BeginDraft(target CommentTarget) (Review, error) {
	if review.draft != nil || review.activeMutation != 0 ||
		review.publicationBlocksCommentMutation() || !review.validTarget(target) {
		return review, ErrInvalidReviewTransition
	}
	if fileID, ok := target.FileID(); ok {
		file, _ := review.file(fileID)
		target = target.withPath(file.IdentityPath())
	}
	next := review.cloneCommentState()
	next.draft = &Draft{target: target}
	next.retainedBytes += draftRetainedBytes(next.draft)
	return next, nil
}

// UpdateDraft performs the update draft operation.
func (review Review) UpdateDraft(body string) (Review, error) {
	if review.draft == nil || review.activeMutation != 0 {
		return review, ErrInvalidReviewTransition
	}
	if _, err := scanCommentText(nil, body); err != nil {
		return review, ErrInvalidReviewTransition
	}
	next := review.cloneCommentState()
	next.draft.body = strings.Clone(body)
	next.retainedBytes += int64(len(next.draft.body) - len(review.draft.body))
	return next, nil
}

// OwnDraftBody installs an already-owned immutable draft body without copying it.
func (review Review) OwnDraftBody(body OwnedDraftBody) (Review, error) {
	if review.draft == nil || review.activeMutation != 0 || !body.valid {
		return review, ErrInvalidReviewTransition
	}
	next := review.cloneCommentState()
	next.draft.body = body.value
	next.retainedBytes += int64(len(body.value) - len(review.draft.body))
	return next, nil
}

// TakeDraftBody transfers the draft body out while preserving draft metadata.
func (review Review) TakeDraftBody() (Review, OwnedDraftBody, error) {
	if review.draft == nil || review.activeMutation != 0 {
		return review, OwnedDraftBody{}, ErrInvalidReviewTransition
	}
	next := review.cloneCommentState()
	body := OwnedDraftBody{value: next.draft.body, valid: true}
	next.draft.body = ""
	next.retainedBytes -= int64(len(body.value))
	return next, body, nil
}

// SaveDraft returns the receiver's save draft value.
func (review Review) SaveDraft() (Review, CommentID, error) {
	started, mutation, err := review.StartSaveComment(synchronousCommentMutationID)
	if err != nil {
		return review, 0, ErrInvalidReviewTransition
	}
	prepared, err := PrepareCommentMutationCancellable(nil, mutation)
	if err != nil {
		return review, 0, ErrInvalidReviewTransition
	}
	next, err := started.ApplyCommentMutation(prepared)
	if err != nil {
		return review, 0, ErrInvalidReviewTransition
	}
	id, _ := prepared.SavedComment()
	return next, id, nil
}

// BeginEditComment performs the begin edit comment operation.
func (review Review) BeginEditComment(id CommentID) (Review, error) {
	started, mutation, err := review.StartBeginEditComment(synchronousCommentMutationID, id)
	if err != nil {
		return review, ErrInvalidReviewTransition
	}
	prepared, err := PrepareCommentMutationCancellable(nil, mutation)
	if err != nil {
		return review, ErrInvalidReviewTransition
	}
	next, err := started.ApplyCommentMutation(prepared)
	if err != nil {
		return review, ErrInvalidReviewTransition
	}
	return next, nil
}

// CancelDraft returns the receiver's cancel draft value.
func (review Review) CancelDraft() (Review, error) {
	if review.draft == nil || review.activeMutation != 0 {
		return review, ErrInvalidReviewTransition
	}
	next := review.cloneCommentState()
	next.retainedBytes -= draftRetainedBytes(review.draft)
	next.draft = nil
	return next, nil
}

// DeleteComment performs the delete comment operation.
func (review Review) DeleteComment(id CommentID) (Review, error) {
	started, mutation, err := review.StartDeleteComment(synchronousCommentMutationID, id)
	if err != nil {
		return review, ErrInvalidReviewTransition
	}
	prepared, err := PrepareCommentMutationCancellable(nil, mutation)
	if err != nil {
		return review, ErrInvalidReviewTransition
	}
	next, err := started.ApplyCommentMutation(prepared)
	if err != nil {
		return review, ErrInvalidReviewTransition
	}
	return next, nil
}

// Comments returns the receiver's comments value.
func (review Review) Comments() []Comment {
	return slices.Clone(review.comments)
}

// CommentCount returns the receiver's comment count value.
func (review Review) CommentCount() int {
	return len(review.comments)
}

// CommentAt performs the comment at operation.
func (review Review) CommentAt(index int) (Comment, bool) {
	if index < 0 || index >= len(review.comments) {
		return Comment{}, false
	}
	return review.comments[index], true
}

// CommentIndex returns the position of the comment with id.
func (review Review) CommentIndex(id CommentID) (int, bool) {
	index := commentIndex(review.comments, id)
	return index, index >= 0
}

// CommentRange performs the comment range operation.
func (review Review) CommentRange(start, count int) []Comment {
	if start < 0 || start >= len(review.comments) || count <= 0 {
		return nil
	}
	end := len(review.comments)
	if count < end-start {
		end = start + count
	}
	return slices.Clone(review.comments[start:end])
}

// RetainedCommentBytes returns the receiver's retained comment bytes value.
func (review Review) RetainedCommentBytes() int64 {
	return review.retainedBytes
}

// Draft returns the receiver's draft value.
func (review Review) Draft() (Draft, bool) {
	if review.draft == nil {
		return Draft{}, false
	}
	return *review.draft, true
}

// HasCommentAnchor reports whether a saved local comment or remote review
// thread is anchored to the source line. Its indexes are immutable across
// Review values, so the lookup is safe on the rendering path.
func (review Review) HasCommentAnchor(
	file FileID,
	side LineSide,
	line LineNumber,
) bool {
	anchor := commentAnchor{file: file, side: side, line: line}
	if review.commentAnchors[anchor] != 0 {
		return true
	}
	_, exists := review.snapshot.remoteCommentAnchors[anchor]
	return exists
}

func (review Review) clone() Review {
	next := review
	next.state++
	if review.active != nil {
		next.active = new(*review.active)
	}
	if review.selected != nil {
		next.selected = new(*review.selected)
	}
	if review.draft != nil {
		next.draft = new(*review.draft)
	}
	return next
}

func (review Review) cloneCommentState() Review {
	next := review.clone()
	next.revision++
	return next
}

func (review Review) file(id FileID) (File, bool) {
	files := review.snapshot.files
	if id.Uint32() >= uint32FromInt(len(files)) {
		return File{}, false
	}
	file := files[id]
	return file, file.ID() == id
}

func (review Review) validTarget(target CommentTarget) bool {
	switch target.Kind() {
	case TargetReview:
		return true
	case TargetFile, TargetLine:
		fileID, _ := target.FileID()
		_, ok := review.file(fileID)
		return ok
	default:
		return false
	}
}

// FileReviewed performs the file reviewed operation.
func (review Review) FileReviewed(id FileID) bool {
	return review.fileMarks.has(id)
}

// HunkReviewed performs the hunk reviewed operation.
func (review Review) HunkReviewed(id HunkID) bool {
	return review.hunkMarks.has(id)
}

// ShowHidden reports the receiver's show hidden state.
func (review Review) ShowHidden() bool {
	return review.showHidden
}

// HasLocalChanges reports the receiver's has local changes state.
func (review Review) HasLocalChanges() bool {
	return review.hasPendingLocalChanges() || review.draft != nil ||
		review.fileMarks.count != 0 || review.hunkMarks.count != 0
}
