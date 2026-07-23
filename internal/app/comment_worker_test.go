package app

import (
	"context"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestCommentSaveRunsThroughWorkerAndSettlesOwnership(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)

	next, err := model.review.BeginDraft(review.ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	next, err = next.UpdateDraft("worker-owned comment")
	if err != nil {
		t.Fatal(err)
	}
	model.review = next
	model.mode = ModeCommentEdit
	model.saveComment()
	if model.review.CommentMutationOperation() == 0 || model.mode != ModeNormal {
		t.Fatalf(
			"admitted save = operation %d mode %v",
			model.review.CommentMutationOperation(), model.mode,
		)
	}

	settleModelWork(t, model, coordinator)
	if model.review.CommentMutationOperation() != 0 || model.review.CommentCount() != 1 {
		t.Fatalf(
			"settled save = operation %d comments %d",
			model.review.CommentMutationOperation(), model.review.CommentCount(),
		)
	}
	if _, exists := model.review.Draft(); exists {
		t.Fatal("settled save retained its draft")
	}
	if stats := coordinator.Stats(); stats.Accepted != 0 || stats.RequestBytes != 0 ||
		stats.ReservedResult != 0 || stats.Retained != 0 {
		t.Fatalf("settled save retained coordinator ownership: %+v", stats)
	}
}

func TestCommentSaveFailureRestoresEditorMode(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	next, err := model.review.BeginDraft(review.ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	next, err = next.UpdateDraft("retry this comment")
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := review.NewOperationID(10_000)
	started, _, err := next.StartSaveComment(operation)
	if err != nil {
		t.Fatal(err)
	}
	model.review = started
	model.mode = ModeNormal

	model.applyCommentMutationOutcome(commentMutationOutcome{
		ID: operation, Err: context.Canceled,
	})
	if model.mode != ModeCommentEdit || model.review.CommentMutationOperation() != 0 {
		t.Fatalf(
			"failed save = mode %v operation %d",
			model.mode, model.review.CommentMutationOperation(),
		)
	}
	if draft, exists := model.review.Draft(); !exists || draft.Body() != "retry this comment" {
		t.Fatalf("failed save draft = (%+v, %v)", draft, exists)
	}
}

func TestCommentSaveInvalidPreparedResultRestoresEditorMode(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	next, err := model.review.BeginDraft(review.ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	next, err = next.UpdateDraft("retry malformed result")
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := review.NewOperationID(10_002)
	started, _, err := next.StartSaveComment(operation)
	if err != nil {
		t.Fatal(err)
	}
	model.review = started
	model.mode = ModeNormal

	model.applyCommentMutationOutcome(commentMutationOutcome{ID: operation})
	if model.mode != ModeCommentEdit || model.review.CommentMutationOperation() != 0 {
		t.Fatalf(
			"malformed save = mode %v operation %d",
			model.mode, model.review.CommentMutationOperation(),
		)
	}
	if draft, exists := model.review.Draft(); !exists || draft.Body() != "retry malformed result" {
		t.Fatalf("malformed save draft = (%+v, %v)", draft, exists)
	}
}

func TestCommentMutationAdmissionChargesSourceAndConstruction(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	next, err := model.review.BeginDraft(review.ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	next, err = next.UpdateDraft("accounted comment")
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := review.NewOperationID(10_001)
	started, mutation, err := next.StartSaveComment(operation)
	if err != nil {
		t.Fatal(err)
	}
	model.review = started
	if err := coordinator.submitCommentMutation(commentMutationWork{
		Mutation: mutation,
	}); err != nil {
		t.Fatal(err)
	}
	stats := coordinator.Stats()
	if stats.RequestBytes != mutation.RetainedBytes() ||
		stats.ReservedResult != mutation.ConstructionBytes() {
		t.Fatalf(
			"comment reservation = request %d result %d, want %d/%d",
			stats.RequestBytes, stats.ReservedResult,
			mutation.RetainedBytes(), mutation.ConstructionBytes(),
		)
	}
	settleModelWork(t, model, coordinator)
}
