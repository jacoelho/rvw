package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/change"
)

const (
	commentChangePreparing = "preparing comment change"
	commentSaved           = "comment saved"
)

type commentMutationWork struct {
	Mutation change.CommentMutation
}

type commentMutationOutcome struct {
	Err      error
	Prepared change.PreparedCommentMutation
	ID       change.OperationID
}

func (commentMutationOutcome) coordinatorOutcome()                     {}
func (outcome commentMutationOutcome) operationID() change.OperationID { return outcome.ID }

type commentMutationJob struct {
	ctx  context.Context
	work commentMutationWork
}

func (coordinator *Coordinator) submitCommentMutation(work commentMutationWork) error {
	id := work.Mutation.Operation()
	if id == 0 || work.Mutation.Kind() < change.CommentMutationBeginEdit ||
		work.Mutation.Kind() > change.CommentMutationResolveConflict {
		return errors.New("invalid comment mutation work")
	}
	return coordinator.submit(
		id,
		work.Mutation.RetainedBytes(),
		work.Mutation.ConstructionBytes(),
		func(ctx context.Context) bool {
			select {
			case coordinator.commentMutationJobs <- commentMutationJob{ctx: ctx, work: work}:
				return true
			default:
				return false
			}
		},
	)
}

func (coordinator *Coordinator) executeCommentMutationCPU(job commentMutationJob) {
	id := job.work.Mutation.Operation()
	coordinator.required.Wait(id)
	var prepared change.PreparedCommentMutation
	var err error
	if !coordinator.acquireCPU(job.ctx) {
		err = context.Cause(job.ctx)
	} else {
		prepared, err = change.PrepareCommentMutationCancellable(
			func() error { return context.Cause(job.ctx) },
			job.work.Mutation,
		)
		coordinator.releaseCPU()
	}
	coordinator.complete(commentMutationOutcome{
		ID: id, Prepared: prepared, Err: err,
	})
}

func (model *Model) startCommentMutation(
	start func(change.OperationID) (change.Review, change.CommentMutation, error),
) bool {
	operation, err := model.nextOperationID()
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return false
	}
	next, mutation, err := start(operation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return false
	}
	if err := model.coordinator.submitCommentMutation(commentMutationWork{
		Mutation: mutation,
	}); err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return false
	}
	model.review = next
	model.notice = &Notice{Kind: NoticeInformation, Text: commentChangePreparing}
	return true
}

func (model *Model) applyCommentMutationOutcome(outcome commentMutationOutcome) tea.Cmd {
	if model.review.CommentMutationOperation() != outcome.ID {
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale comment mutation outcome"}
		return nil
	}
	kind := model.review.CommentMutationKind()
	if outcome.Err != nil {
		return model.failCommentMutation(outcome.ID, kind, outcome.Err)
	}
	next, err := model.review.ApplyCommentMutation(outcome.Prepared)
	if err != nil {
		return model.failCommentMutation(outcome.ID, kind, err)
	}
	selections := model.captureReviewSelections()
	model.review = next
	switch kind {
	case change.CommentMutationBeginEdit:
		model.reconcileReviewSelections(selections)
		draft, ok := model.review.Draft()
		if !ok {
			model.notice = &Notice{Kind: NoticeError, Text: "prepared comment edit has no draft"}
			return nil
		}
		model.commentEditor.SetValue(draft.Body())
		model.resizeCommentEditor()
		model.mode = ModeCommentEdit
		model.clampDiffTop()
		return nil
	case change.CommentMutationSave:
		model.mode = ModeNormal
		model.selectCommentIndex(model.review.CommentCount() - 1)
		model.notice = &Notice{Kind: NoticeSuccess, Text: commentSaved}
	case change.CommentMutationDelete:
		model.reconcileReviewSelections(selections)
		model.notice = &Notice{Kind: NoticeSuccess, Text: "comment deleted"}
	case change.CommentMutationResolveConflict:
		model.reconcileReviewSelections(selections)
		model.notice = &Notice{Kind: NoticeSuccess, Text: "draft conflict resolved"}
	}
	return nil
}

func (model *Model) failCommentMutation(
	id change.OperationID,
	kind change.CommentMutationKind,
	failure error,
) tea.Cmd {
	if next, err := model.review.CancelCommentMutation(id); err == nil {
		model.review = next
	}
	model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(failure.Error())}
	if kind == change.CommentMutationSave {
		model.mode = ModeCommentEdit
	}
	return nil
}
