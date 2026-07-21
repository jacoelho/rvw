package review

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrInvalidReviewTransition = errors.New("invalid review transition")

type Review struct {
	workspace WorkspaceRevision
	snapshot  Snapshot
	kinds     []CommentKind
	state     uint64

	active       *FileID
	selected     *FileID
	showHidden   bool
	fileMarks    fileMarkSet
	hunkMarks    hunkMarkSet
	comments     []Comment
	commentBody  int64
	nextComment  uint32
	draft        *Draft
	operations   map[OperationID]Operation
	operationIDs []OperationID
	patches      map[FileID]PatchState
	search       searchState
	publication  publicationState
}

func NewReview(snapshot Snapshot, kinds []CommentKind) (Review, error) {
	if snapshot.SourceID() == 0 {
		return Review{}, fmt.Errorf("%w: snapshot has no source", ErrInvalidReviewTransition)
	}
	validKinds, err := validateKinds(kinds)
	if err != nil {
		return Review{}, err
	}
	workspace, _ := NewWorkspaceRevision(1)
	review := Review{
		workspace:   workspace,
		snapshot:    snapshot,
		kinds:       validKinds,
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
	return review, nil
}

func (review Review) WorkspaceRevision() WorkspaceRevision {
	return review.workspace
}

func (review Review) StateRevision() uint64 {
	return review.state
}

func (review Review) Snapshot() Snapshot {
	return review.snapshot
}

func (review Review) ActiveFile() (FileID, bool) {
	if review.active == nil {
		return 0, false
	}
	return *review.active, true
}

func (review Review) ActivateFile(id FileID) (Review, error) {
	if _, ok := review.file(id); !ok {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.active = new(id)
	next.selected = new(id)
	return next, nil
}

func (review Review) SelectedFile() (FileID, bool) {
	if review.selected == nil {
		return 0, false
	}
	return *review.selected, true
}

func (review Review) SelectFile(id FileID) (Review, error) {
	file, ok := review.file(id)
	if !ok || file.Flags().Hidden() && !review.showHidden {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.selected = new(id)
	return next, nil
}

type NavigationDirection int8

const (
	NavigatePrevious NavigationDirection = -1
	NavigateNext     NavigationDirection = 1
)

func (review Review) ActivateSelectedFile() (Review, error) {
	if review.selected == nil {
		return review, ErrInvalidReviewTransition
	}
	return review.ActivateFile(*review.selected)
}

func (review Review) ToggleFileReviewed(id FileID) (Review, error) {
	if _, ok := review.file(id); !ok {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.fileMarks = review.fileMarks.toggle(id)
	return next, nil
}

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

func (review Review) ShowHiddenFiles() Review {
	next := review.clone()
	next.showHidden = true
	return next
}

func (review Review) HideHiddenFiles() Review {
	next := review.clone()
	next.showHidden = false
	return next
}

func (review Review) BeginDraft(target CommentTarget, kind CommentKind) (Review, error) {
	if review.draft != nil || !review.validTarget(target) || !slices.Contains(review.kinds, kind) {
		return review, ErrInvalidReviewTransition
	}
	if fileID, ok := target.FileID(); ok {
		file, _ := review.file(fileID)
		target = target.withPath(file.IdentityPath())
	}
	next := review.clone()
	next.draft = &Draft{target: target, kind: kind}
	return next, nil
}

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
		next.comments[index].kind = review.draft.kind
		next.comments[index].target = review.draft.target
		next.comments[index].body = body
		next.commentBody += int64(len(body))
	} else {
		if len(review.comments) >= MaxComments || review.nextComment >= MaxComments ||
			review.commentBody+int64(len(body)) > MaxCommentBodyTotal {
			return review, 0, ErrInvalidReviewTransition
		}
		id, _ = NewCommentID(review.nextComment)
		next.comments = append(next.comments, Comment{
			id:     id,
			kind:   review.draft.kind,
			target: review.draft.target,
			body:   body,
		})
		next.commentBody += int64(len(body))
		next.nextComment++
	}
	next.draft = nil
	return next, id, nil
}

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
	comment := review.comments[index]
	next := review.clone()
	next.draft = &Draft{
		kind:    comment.kind,
		target:  comment.target,
		body:    comment.body.String(),
		editing: new(id),
	}
	return next, nil
}

func (review Review) SetDraftKind(kind CommentKind) (Review, error) {
	if review.draft == nil || !slices.Contains(review.kinds, kind) {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	draft := *review.draft
	draft.kind = kind
	next.draft = new(draft)
	return next, nil
}

func (review Review) CancelDraft() (Review, error) {
	if review.draft == nil {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.draft = nil
	return next, nil
}

func (review Review) DeleteComment(id CommentID) (Review, error) {
	index := slices.IndexFunc(review.comments, func(comment Comment) bool {
		return comment.id == id
	})
	if index < 0 {
		return review, ErrInvalidReviewTransition
	}
	next := review.clone()
	next.comments = slices.Clone(review.comments)
	next.commentBody -= int64(len(next.comments[index].body))
	next.comments = slices.Delete(next.comments, index, index+1)
	return next, nil
}

func (review Review) Comments() []Comment {
	return slices.Clone(review.comments)
}

func (review Review) CommentCount() int {
	return len(review.comments)
}

func (review Review) CommentAt(index int) (Comment, bool) {
	if index < 0 || index >= len(review.comments) {
		return Comment{}, false
	}
	return review.comments[index], true
}

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

func (review Review) RetainedCommentBytes() int64 {
	return review.commentBody
}

func (review Review) Draft() (Draft, bool) {
	if review.draft == nil {
		return Draft{}, false
	}
	return *review.draft, true
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
	if uint32(id) >= uint32(len(files)) {
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

func (review Review) FileReviewed(id FileID) bool {
	return review.fileMarks.has(id)
}

func (review Review) HunkReviewed(id HunkID) bool {
	return review.hunkMarks.has(id)
}

func (review Review) ShowHidden() bool {
	return review.showHidden
}

func (review Review) HasLocalChanges() bool {
	return len(review.comments) != 0 || review.draft != nil ||
		review.fileMarks.count != 0 || review.hunkMarks.count != 0
}
