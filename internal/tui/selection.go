package tui

import "github.com/jacoelho/rvw/internal/change"

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
	local    change.CommentID
	kind     commentRowKind
	empty    bool
}

type commentRowReference struct {
	remote string
	local  change.CommentID
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

type reviewSelections struct {
	comment commentRowIdentity
}

func (model *Model) captureReviewSelections() reviewSelections {
	return reviewSelections{
		comment: model.currentCommentSelection(),
	}
}

func (model *Model) reconcileReviewSelections(selections reviewSelections) {
	model.reconcileCommentSelection(selections.comment)
}
