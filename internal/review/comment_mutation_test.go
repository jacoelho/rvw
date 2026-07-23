package review

import (
	"context"
	"errors"
	"testing"
)

func TestPreparedCommentMutationUsesCommentRevisionFence(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go", "two.go"))
	target, err := FileCommentTarget(0)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.BeginDraft(target)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("body")
	if err != nil {
		t.Fatal(err)
	}
	current, id, err := current.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}

	started, mutation, err := current.StartBeginEditComment(2, id)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareCommentMutationCancellable(nil, mutation)
	if err != nil {
		t.Fatal(err)
	}
	navigated, err := started.SelectFile(1)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := navigated.ApplyCommentMutation(prepared)
	if err != nil {
		t.Fatalf("unrelated navigation staled mutation: %v", err)
	}
	if selected, ok := applied.SelectedFile(); !ok || selected != 1 {
		t.Fatalf("selected file = %d %v", selected, ok)
	}
	if draft, ok := applied.Draft(); !ok || draft.Body() != "body" {
		t.Fatalf("prepared edit draft = %+v %v", draft, ok)
	}
	if _, err := started.CancelCommentMutation(2); err != nil {
		t.Fatal(err)
	}
	cancelled, _ := started.CancelCommentMutation(2)
	if _, err := cancelled.ApplyCommentMutation(prepared); !errors.Is(err, ErrInvalidCommentMutation) {
		t.Fatalf("stale apply error = %v", err)
	}
}

func TestCommentMutationAndPublicationAreMutuallyExclusive(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	target, _ := FileCommentTarget(0)
	current, _ = current.BeginDraft(target)
	current, _ = current.UpdateDraft("body")
	current, id, _ := current.SaveDraft()

	started, _, err := current.StartDeleteComment(2, id)
	if err != nil {
		t.Fatal(err)
	}
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventComment, policies)
	if _, _, err := started.StartPublicationCheck(3, options); !errors.Is(err, ErrInvalidPublication) {
		t.Fatalf("publication during mutation error = %v", err)
	}

	checking, _, err := current.StartPublicationCheck(3, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := checking.StartDeleteComment(4, id); !errors.Is(err, ErrInvalidCommentMutation) {
		t.Fatalf("mutation during publication error = %v", err)
	}
}

func TestCommentMutationCancellationStopsBeforeMaximumClone(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	current.comments = make([]Comment, MaxComments)
	for index := range current.comments {
		current.comments[index] = Comment{id: CommentID(index), body: "body"}
		current.commentBody = int64(len(current.comments)) * 4
	}
	current.nextComment = MaxComments
	current.refresh()
	started, mutation, err := current.StartDeleteComment(2, MaxComments-1)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := func() error { return context.Canceled }
	var prepareErr error
	allocations := testing.AllocsPerRun(10, func() {
		_, prepareErr = PrepareCommentMutationCancellable(cancelled, mutation)
	})
	err = prepareErr
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if allocations > 1 {
		t.Fatalf("already-cancelled maximum clone allocations = %f, want at most one fixed allocation", allocations)
	}
	if started.CommentCount() != MaxComments || started.CommentMutationOperation() != 2 {
		t.Fatal("cancelled worker mutated admitted review")
	}
}

func TestCommentMutationPreservesCancellationDuringBodyValidation(t *testing.T) {
	cause := errors.New("distinct cancellation cause")
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	current, err := current.BeginDraft(ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("body")
	if err != nil {
		t.Fatal(err)
	}
	_, mutation, err := current.StartSaveComment(2)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	_, err = PrepareCommentMutationCancellable(func() error {
		checks++
		if checks >= 4 {
			return cause
		}
		return nil
	}, mutation)
	if !errors.Is(err, cause) {
		t.Fatalf("body cancellation error = %v, want %v", err, cause)
	}
}
