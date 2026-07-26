package tui

import (
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func TestCommentSelectionFollowsLocalIdentityAndUsesAdjacentFallback(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	ids := appendTestReviewComments(t, model, "first", "second", "third")

	model.selectCommentIndex(1)
	selection := model.currentCommentSelection()
	var err error
	model.review, err = model.review.DeleteComment(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	model.reconcileCommentSelection(selection)
	assertSelectedLocalComment(t, model, 0, ids[1])

	selection = model.currentCommentSelection()
	model.review, err = model.review.DeleteComment(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	model.reconcileCommentSelection(selection)
	assertSelectedLocalComment(t, model, 0, ids[2])
}

func TestCommentSelectionUsesPreviousFallbackWhenLastRowDisappears(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	ids := appendTestReviewComments(t, model, "first", "second", "third")
	model.selectCommentIndex(2)
	selection := model.currentCommentSelection()

	next, err := model.review.DeleteComment(ids[2])
	if err != nil {
		t.Fatal(err)
	}
	model.review = next
	model.reconcileCommentSelection(selection)
	assertSelectedLocalComment(t, model, 1, ids[1])
}

func TestCommentSelectionFollowsRemoteThreadIdentityAcrossReorder(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	path := model.review.Snapshot().FileCount()
	file, ok := model.review.Snapshot().File(change.FileID(path - 1))
	if !ok {
		t.Fatal("fixture file is unavailable")
	}
	first := newTestRemoteThread(t, "thread-b", file.IdentityPath())
	second := newTestRemoteThread(t, "thread-a", file.IdentityPath())
	snapshot, err := model.review.Snapshot().WithRemoteThreads([]change.RemoteThread{first, second})
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = change.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	model.selectCommentIndex(0)
	selection := model.currentCommentSelection()

	snapshot, err = snapshot.WithRemoteThreads([]change.RemoteThread{second, first})
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = change.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	model.reconcileCommentSelection(selection)
	if model.commentCursor != 1 {
		t.Fatalf("remote comment cursor = %d, want 1", model.commentCursor)
	}
	selected, ok := model.commentRowIdentityAt(model.commentCursor)
	if !ok || selected.kind != commentRowRemote || selected.remote != "thread-b" {
		t.Fatalf("remote selection = %#v, present=%t", selected, ok)
	}
}

func TestCommentSelectionUsesStableSuccessorAcrossRemovalAndInsertion(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	file, ok := model.review.Snapshot().File(0)
	if !ok {
		t.Fatal("fixture file is unavailable")
	}
	first := newTestRemoteThread(t, "thread-a", file.IdentityPath())
	selected := newTestRemoteThread(t, "thread-b", file.IdentityPath())
	next := newTestRemoteThread(t, "thread-c", file.IdentityPath())
	snapshot, err := model.review.Snapshot().WithRemoteThreads(
		[]change.RemoteThread{first, selected, next},
	)
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = change.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	model.selectCommentIndex(1)
	selection := model.currentCommentSelection()

	inserted := newTestRemoteThread(t, "thread-x", file.IdentityPath())
	snapshot, err = snapshot.WithRemoteThreads([]change.RemoteThread{inserted, first, next})
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = change.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	model.reconcileCommentSelection(selection)
	if model.commentCursor != 2 || model.commentSelection.remote != "thread-c" {
		t.Fatalf("selection = cursor %d, identity %#v; want stable successor thread-c", model.commentCursor, model.commentSelection)
	}
}

func TestCommentSelectionBecomesEmptyWhenOnlyRowDisappears(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	file, ok := model.review.Snapshot().File(0)
	if !ok {
		t.Fatal("fixture file is unavailable")
	}
	selected := newTestRemoteThread(t, "thread-a", file.IdentityPath())
	snapshot, err := model.review.Snapshot().WithRemoteThreads([]change.RemoteThread{selected})
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = change.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	model.selectCommentIndex(0)
	selection := model.currentCommentSelection()

	inserted := newTestRemoteThread(t, "thread-x", file.IdentityPath())
	snapshot, err = snapshot.WithRemoteThreads([]change.RemoteThread{inserted})
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = change.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	model.reconcileCommentSelection(selection)
	if model.commentSelection.kind != 0 {
		t.Fatalf("selection = %#v, want empty", model.commentSelection)
	}
	selection = model.currentCommentSelection()
	model.reconcileCommentSelection(selection)
	if model.commentSelection.kind != 0 || !model.commentSelectionKnown {
		t.Fatalf("selection after second reconciliation = %#v, known=%t; want stable empty", model.commentSelection, model.commentSelectionKnown)
	}
}

func TestCommentNavigationEntersExplicitEmptySelectionAtNearestEnd(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	appendTestReviewComments(t, model, "first", "second")
	model.commentSelectionKnown = true
	model.commentSelection = commentRowIdentity{}
	model.commentCursor = 0

	model.moveComment(1)
	if model.commentCursor != 0 || !model.commentRowSelected(0) {
		t.Fatalf("down from empty selected cursor %d, want first row", model.commentCursor)
	}
	model.commentSelection = commentRowIdentity{}
	model.commentSelectionKnown = true
	model.moveComment(-1)
	if model.commentCursor != 1 || !model.commentRowSelected(1) {
		t.Fatalf("up from empty selected cursor %d, want last row", model.commentCursor)
	}
}

func TestFindReturnRestoresCommentIdentityAfterRowsMove(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	ids := appendTestReviewComments(t, model, "first", "second")
	model.focus = FocusComments
	model.selectCommentIndex(1)
	state := model.captureFindReturnState()

	next, err := model.review.DeleteComment(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	model.review = next
	model.restoreFindReturnState(state)
	assertSelectedLocalComment(t, model, 0, ids[1])
}

func appendTestReviewComments(
	t *testing.T,
	model *Model,
	bodies ...string,
) []change.CommentID {
	t.Helper()
	ids := make([]change.CommentID, 0, len(bodies))
	for _, body := range bodies {
		next, err := model.review.BeginDraft(change.ReviewCommentTarget())
		if err != nil {
			t.Fatal(err)
		}
		next, err = next.UpdateDraft(body)
		if err != nil {
			t.Fatal(err)
		}
		next, id, err := next.SaveDraft()
		if err != nil {
			t.Fatal(err)
		}
		model.review = next
		ids = append(ids, id)
	}
	return ids
}

func newTestRemoteThread(
	t *testing.T,
	id string,
	path change.Path,
) change.RemoteThread {
	t.Helper()
	thread, err := change.NewRemoteThread(change.RemoteThreadDescriptor{
		ID: id, Path: path, Side: change.LineNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	return thread
}

func assertSelectedLocalComment(
	t *testing.T,
	model *Model,
	wantIndex int,
	wantID change.CommentID,
) {
	t.Helper()
	if model.commentCursor != wantIndex {
		t.Fatalf("comment cursor = %d, want %d", model.commentCursor, wantIndex)
	}
	comment, ok := model.review.CommentAt(model.commentCursor)
	if !ok || comment.ID() != wantID {
		t.Fatalf("selected comment = %v, present=%t, want ID %d", comment.ID(), ok, wantID)
	}
}
