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
	active         *FileID
	selected       *FileID
	commentAnchors commentAnchorCounts
	patches        map[FileID]PatchState
	draft          *Draft
	operations     map[OperationID]Operation
	pending        *pendingReviewState
	snapshot       Snapshot
	comments       []Comment
	operationIDs   []OperationID
	hunkMarks      hunkMarkSet
	fileMarks      fileMarkSet
	search         searchState
	publication    publicationState
	commentBody    int64
	workspace      WorkspaceRevision
	state          uint64
	nextComment    uint32
	showHidden     bool
}

// NewReview constructs and validates review.
func NewReview(snapshot Snapshot) (Review, error) {
	if snapshot.SourceID() == 0 {
		return Review{}, fmt.Errorf("%w: snapshot has no source", ErrInvalidReviewTransition)
	}
	workspace := WorkspaceRevision(1)
	review := Review{
		workspace:   workspace,
		snapshot:    snapshot,
		state:       1,
		operations:  make(map[OperationID]Operation),
		patches:     make(map[FileID]PatchState),
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
	return importPendingReview(review)
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
	if review.draft != nil || !review.validTarget(target) {
		return review, ErrInvalidReviewTransition
	}
	if fileID, ok := target.FileID(); ok {
		file, _ := review.file(fileID)
		target = target.withPath(file.IdentityPath())
	}
	next := review.clone()
	next.draft = &Draft{target: target}
	return next, nil
}

// UpdateDraft performs the update draft operation.
func (review Review) UpdateDraft(body string) (Review, error) {
	if review.draft == nil || int64(len(body)) > MaxCommentBodyBytes {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	draft := *review.draft
	draft.body = strings.Clone(body)
	next.draft = new(draft)
	return next, nil
}

// SaveDraft returns the receiver's save draft value.
func (review Review) SaveDraft() (Review, CommentID, error) {
	if review.draft == nil {
		return review, 0, ErrInvalidReviewTransition
	}
	body, err := NewCommentBody(review.draft.body)
	if err != nil {
		return review, 0, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.comments = slices.Clone(review.comments)
	var id CommentID
	if review.draft.editing != nil {
		index := slices.IndexFunc(review.comments, func(comment Comment) bool {
			return comment.id == *review.draft.editing
		})
		if index < 0 ||
			review.commentBody-int64(len(review.comments[index].body))+int64(len(body)) >
				MaxCommentBodyTotal {
			return review, 0, ErrInvalidReviewTransition
		}
		id = review.comments[index].id
		next.commentBody -= int64(len(next.comments[index].body))
		before := next.comments[index]
		next.comments[index].target = review.draft.target
		next.comments[index].body = body
		next.commentBody += int64(len(body))
		next.commentAnchors = replaceCommentAnchor(
			review.commentAnchors,
			&before,
			&next.comments[index],
		)
	} else {
		if len(review.comments) >= MaxComments || review.nextComment >= MaxComments ||
			review.commentBody+int64(len(body)) > MaxCommentBodyTotal {
			return review, 0, ErrInvalidReviewTransition
		}
		id = CommentID(review.nextComment)
		comment := Comment{
			id:     id,
			target: review.draft.target,
			body:   body,
		}
		next.comments = append(next.comments, comment)
		next.commentAnchors = replaceCommentAnchor(review.commentAnchors, nil, &comment)
		next.commentBody += int64(len(body))
		next.nextComment++
	}
	next.draft = nil
	return next, id, nil
}

// BeginEditComment performs the begin edit comment operation.
func (review Review) BeginEditComment(id CommentID) (Review, error) {
	if review.draft != nil {
		return review, ErrInvalidReviewTransition
	}
	index := slices.IndexFunc(review.comments, func(comment Comment) bool {
		return comment.id == id
	})
	if index < 0 {
		return review, ErrInvalidReviewTransition
	}
	restore := false
	if review.pending != nil {
		if binding, bound := review.pending.bindings[id]; bound {
			if !binding.canUpdate {
				return review, ErrInvalidReviewTransition
			}
			restore = binding.deleted
		}
	}
	comment := review.comments[index]
	next := review.clone()
	if restore {
		next.pending = review.pending.clone()
		binding := next.pending.bindings[id]
		binding.deleted = false
		next.pending.bindings[id] = binding
		next.commentAnchors = replaceCommentAnchor(review.commentAnchors, nil, &comment)
	}
	next.draft = &Draft{
		target:  comment.target,
		body:    comment.body.String(),
		editing: new(id),
	}
	return next, nil
}

// CancelDraft returns the receiver's cancel draft value.
func (review Review) CancelDraft() (Review, error) {
	if review.draft == nil {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.draft = nil
	return next, nil
}

// DeleteComment performs the delete comment operation.
func (review Review) DeleteComment(id CommentID) (Review, error) {
	index := slices.IndexFunc(review.comments, func(comment Comment) bool {
		return comment.id == id
	})
	if index < 0 {
		return review, ErrInvalidReviewTransition
	}
	if review.pending != nil {
		if binding, bound := review.pending.bindings[id]; bound {
			if binding.kind == pendingBindingBody && !review.pending.bodyUpdate ||
				binding.kind == pendingBindingComment && !binding.canDelete {
				return review, ErrInvalidReviewTransition
			}
			next := review.clone()
			next.pending = review.pending.clone()
			binding.deleted = true
			next.pending.bindings[id] = binding
			next.commentAnchors = replaceCommentAnchor(
				review.commentAnchors,
				&review.comments[index],
				nil,
			)
			return next, nil
		}
	}
	next := review.clone()
	next.comments = slices.Clone(review.comments)
	removed := next.comments[index]
	next.commentBody -= int64(len(next.comments[index].body))
	next.comments = slices.Delete(next.comments, index, index+1)
	next.commentAnchors = replaceCommentAnchor(review.commentAnchors, &removed, nil)
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
	return review.commentBody
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
