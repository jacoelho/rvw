package app

import "github.com/jacoelho/rvw/internal/review"

type commentRowKind uint8

const (
	commentRowLocal commentRowKind = iota + 1
	commentRowRemote
)

type commentRowIdentity struct {
	remote   string
	next     commentRowReference
	previous commentRowReference
	fallback int
	local    review.CommentID
	kind     commentRowKind
	empty    bool
}

type commentRowReference struct {
	remote string
	local  review.CommentID
	kind   commentRowKind
}

func (identity commentRowIdentity) sameRow(other commentRowIdentity) bool {
	return identity.kind == other.kind && identity.local == other.local &&
		identity.remote == other.remote
}

func (identity commentRowIdentity) reference() commentRowReference {
	return commentRowReference{
		kind: identity.kind, local: identity.local, remote: identity.remote,
	}
}

func (model *Model) commentRowIdentityAt(index int) (commentRowIdentity, bool) {
	localCount := model.review.CommentCount()
	if index >= 0 && index < localCount {
		comment, ok := model.review.CommentAt(index)
		if !ok {
			return commentRowIdentity{}, false
		}
		return commentRowIdentity{
			kind: commentRowLocal, local: comment.ID(), fallback: index,
		}, true
	}
	thread, ok := model.review.Snapshot().RemoteThread(index - localCount)
	if !ok {
		return commentRowIdentity{}, false
	}
	return commentRowIdentity{
		kind: commentRowRemote, remote: thread.ID(), fallback: index,
	}, true
}

func (model *Model) currentCommentSelection() commentRowIdentity {
	if model.commentSelectionKnown && model.commentSelection.kind == 0 {
		return commentRowIdentity{fallback: max(model.commentCursor, 0), empty: true}
	}
	identity, ok := model.commentRowIdentityAt(model.commentCursor)
	if !ok {
		return commentRowIdentity{fallback: max(model.commentCursor, 0)}
	}
	identity.fallback = model.commentCursor
	if next, exists := model.commentRowIdentityAt(model.commentCursor + 1); exists {
		identity.next = next.reference()
	}
	if previous, exists := model.commentRowIdentityAt(model.commentCursor - 1); exists {
		identity.previous = previous.reference()
	}
	return identity
}

func (model *Model) selectCommentIndex(index int) {
	count := model.commentRowCount()
	if count == 0 {
		model.commentCursor = 0
		model.commentSelection = commentRowIdentity{}
		model.commentSelectionKnown = false
		return
	}
	model.commentCursor = min(max(index, 0), count-1)
	model.commentSelection, _ = model.commentRowIdentityAt(model.commentCursor)
	model.commentSelectionKnown = true
}

func (model *Model) reconcileCommentSelection(selection commentRowIdentity) {
	if selection.empty {
		model.commentCursor = 0
		model.commentSelection = commentRowIdentity{}
		model.commentSelectionKnown = true
		return
	}
	if index, ok := model.commentIndex(selection); ok {
		model.selectCommentIndex(index)
		return
	}
	if index, ok := model.commentReferenceIndex(selection.next); ok {
		model.selectCommentIndex(index)
		return
	}
	if index, ok := model.commentReferenceIndex(selection.previous); ok {
		model.selectCommentIndex(index)
		return
	}
	if selection.kind != 0 {
		model.commentCursor = 0
		model.commentSelection = commentRowIdentity{}
		model.commentSelectionKnown = true
		return
	}
	model.selectCommentIndex(selection.fallback)
}

func (model *Model) commentRowSelected(index int) bool {
	if !model.commentSelectionKnown {
		return model.commentRowCount() != 0 &&
			index == min(max(model.commentCursor, 0), model.commentRowCount()-1)
	}
	identity, ok := model.commentRowIdentityAt(index)
	return ok && model.commentSelection.sameRow(identity)
}

func (model *Model) commentIndex(identity commentRowIdentity) (int, bool) {
	switch identity.kind {
	case commentRowLocal:
		return model.review.CommentIndex(identity.local)
	case commentRowRemote:
		index, ok := model.review.Snapshot().RemoteThreadIndex(identity.remote)
		if ok {
			return model.review.CommentCount() + index, true
		}
	}
	return 0, false
}

func (model *Model) commentReferenceIndex(reference commentRowReference) (int, bool) {
	return model.commentIndex(commentRowIdentity{
		kind: reference.kind, local: reference.local, remote: reference.remote,
	})
}

type publicationRowKind uint8

const (
	publicationRowPayload publicationRowKind = iota + 1
	publicationRowComment
)

type publicationRowIdentity struct {
	next     review.CommentID
	previous review.CommentID
	fallback int
	comment  review.CommentID
	kind     publicationRowKind
}

func (model *Model) currentPublicationSelection() publicationRowIdentity {
	if model.publicationCursor <= 0 {
		return publicationRowIdentity{kind: publicationRowPayload}
	}
	comment, ok := model.review.CommentAt(model.publicationCursor - 1)
	if !ok {
		return publicationRowIdentity{fallback: max(model.publicationCursor, 0)}
	}
	identity := publicationRowIdentity{
		kind: publicationRowComment, comment: comment.ID(), fallback: model.publicationCursor,
	}
	if next, exists := model.review.CommentAt(model.publicationCursor); exists {
		identity.next = next.ID()
	}
	if previous, exists := model.review.CommentAt(model.publicationCursor - 2); exists {
		identity.previous = previous.ID()
	}
	return identity
}

func (model *Model) selectPublicationIndex(index int) {
	model.publicationCursor = min(max(index, 0), model.review.CommentCount())
	if model.publicationCursor == 0 {
		model.publicationSelection = publicationRowIdentity{kind: publicationRowPayload}
		return
	}
	comment, ok := model.review.CommentAt(model.publicationCursor - 1)
	if !ok {
		model.publicationSelection = publicationRowIdentity{}
		return
	}
	model.publicationSelection = publicationRowIdentity{
		kind: publicationRowComment, comment: comment.ID(), fallback: model.publicationCursor,
	}
}

func (model *Model) reconcilePublicationSelection(selection publicationRowIdentity) {
	if selection.kind == publicationRowPayload {
		model.selectPublicationIndex(0)
		return
	}
	if selection.kind == publicationRowComment {
		if index, ok := model.review.CommentIndex(selection.comment); ok {
			model.selectPublicationIndex(index + 1)
			return
		}
		if index, ok := model.review.CommentIndex(selection.next); ok {
			model.selectPublicationIndex(index + 1)
			return
		}
		if index, ok := model.review.CommentIndex(selection.previous); ok {
			model.selectPublicationIndex(index + 1)
			return
		}
		model.selectPublicationIndex(0)
		return
	}
	model.selectPublicationIndex(selection.fallback)
}

type reviewSelections struct {
	comment     commentRowIdentity
	publication publicationRowIdentity
}

func (model *Model) captureReviewSelections() reviewSelections {
	return reviewSelections{
		comment:     model.currentCommentSelection(),
		publication: model.currentPublicationSelection(),
	}
}

func (model *Model) reconcileReviewSelections(selections reviewSelections) {
	model.reconcileCommentSelection(selections.comment)
	model.reconcilePublicationSelection(selections.publication)
}
