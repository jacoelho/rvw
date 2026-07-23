package source

import (
	"context"
	"errors"

	"github.com/jacoelho/rvw/internal/review"
)

// PendingPublicationReconciliationAcquisition owns bounded remote state until
// CPU-only continuation classification completes.
type PendingPublicationReconciliationAcquisition struct {
	checked  *review.CheckedPublication
	response []byte
	fresh    review.PendingReview
	request  review.ReconciliationRequest
	phase    review.PendingPublicationPhase
	valid    bool
}

// AcquirePendingPublicationReconciliation performs only remote acquisition.
func (service *Service) AcquirePendingPublicationReconciliation(
	ctx context.Context,
	request review.ReconciliationRequest,
	runCPU CPUAdmission,
) (*PendingPublicationReconciliationAcquisition, error) {
	if runCPU == nil {
		return nil, errors.New("pending publication CPU admission is unavailable")
	}
	checked, phase, ok := request.PendingContinuation()
	if !ok {
		return nil, errors.New("pending publication continuation is unavailable")
	}
	view, ok := checked.Prepared().BorrowPendingPublication()
	if !ok {
		return nil, errors.New("pending publication continuation is unavailable")
	}
	acquisition := &PendingPublicationReconciliationAcquisition{
		request: request,
		checked: checked,
		phase:   phase,
		valid:   true,
	}
	switch phase {
	case review.PendingPublicationSubmitting:
		response, err := service.acquirePendingSubmission(
			ctx,
			checked.Target(),
			view.BaselineDatabaseID().Uint64(),
		)
		if err != nil {
			return nil, err
		}
		acquisition.response = response
	case review.PendingPublicationSynchronizing, review.PendingPublicationIndeterminate:
		fresh, err := service.acquirePendingReview(
			ctx,
			checked.Target(),
			view.BaselineNodeID(),
			view.BaselineDatabaseID().Uint64(),
			runCPU,
		)
		if err != nil {
			return nil, err
		}
		acquisition.fresh = fresh
	default:
		return nil, errors.New("pending publication phase is invalid")
	}
	return acquisition, nil
}

// ConstructionBytes returns the conservative CPU classification peak.
func (acquisition *PendingPublicationReconciliationAcquisition) ConstructionBytes() int64 {
	if acquisition == nil || !acquisition.valid || acquisition.checked == nil {
		return 0
	}
	view, ok := acquisition.checked.Prepared().BorrowPendingPublication()
	if !ok {
		return 0
	}
	return acquisition.request.RetainedBytes() + acquisition.fresh.RetainedBytes() +
		int64(len(acquisition.response)) +
		int64(view.MutationCount())*pendingEffectMetadataBytes +
		int64(acquisition.fresh.CommentCount())*(pendingEffectMetadataBytes+pendingAliasMetadataBytes) +
		4_096
}

// TakePendingCapture transfers a synchronizing acquisition to the review
// domain for one-pass classification, validation, and rebase preparation.
func (acquisition *PendingPublicationReconciliationAcquisition) TakePendingCapture() (
	review.PendingReview,
	bool,
) {
	if acquisition == nil || !acquisition.valid || acquisition.checked == nil ||
		acquisition.phase == review.PendingPublicationSubmitting ||
		acquisition.fresh.NodeID() == "" {
		return review.PendingReview{}, false
	}
	fresh := acquisition.fresh
	acquisition.Close()
	return fresh, true
}

// Close releases acquired state that was not finished.
func (acquisition *PendingPublicationReconciliationAcquisition) Close() {
	if acquisition == nil {
		return
	}
	*acquisition = PendingPublicationReconciliationAcquisition{}
}

// Finish performs continuation effect construction and classification without
// network I/O.
func (acquisition *PendingPublicationReconciliationAcquisition) Finish(
	ctx context.Context,
) review.ReconciliationResult {
	if acquisition == nil || !acquisition.valid || acquisition.checked == nil {
		return review.ReconciliationResult{
			Disposition: review.ReconciliationStillUnknown,
			Detail:      boundedOperationDetail("pending reconciliation acquisition is unavailable"),
		}
	}
	request := acquisition.request
	checked := acquisition.checked
	fresh := acquisition.fresh
	response := acquisition.response
	phase := acquisition.phase
	acquisition.Close()
	result := review.ReconciliationResult{
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: review.ReconciliationStillUnknown,
	}
	view, ok := checked.Prepared().BorrowPendingPublication()
	if !ok {
		result.Detail = boundedOperationDetail("pending publication continuation is unavailable")
		return result
	}
	if phase == review.PendingPublicationSubmitting {
		mutation, exists := view.MutationAt(0)
		if !exists || mutation.Kind() != review.PendingMutationReviewBody {
			result.Detail = boundedOperationDetail("pending review submission has no body effect")
			return result
		}
		effect, err := pendingEffectFor(
			view.BaselineNodeID(),
			view.BaselineBody(),
			mutation,
			checked.Marker(),
		)
		if err != nil {
			result.Detail = boundedOperationDetail(err.Error())
			return result
		}
		state, err := classifyPendingSubmissionResponse(
			ctx,
			response,
			view.BaselineNodeID(),
			view.BaselineDatabaseID().Uint64(),
			effect.after,
			pendingSubmissionState(checked.Prepared().Options().Event()),
		)
		if err != nil {
			result.Detail = boundedOperationDetail(err.Error())
			return result
		}
		switch state {
		case pendingSubmissionCommitted:
			result.Disposition = review.ReconciliationCommitted
			result.RemoteReview = view.BaselineDatabaseID().Uint64()
			result.Detail = boundedOperationDetail("pending review submission confirmed")
		case pendingSubmissionAbsent:
			result.Detail = boundedOperationDetail(
				"pending review synchronization committed but submission is absent",
			)
		default:
			result.Detail = boundedOperationDetail("pending review submission remains unknown")
		}
		return result
	}
	classified, err := review.ClassifyPendingReconciliationCaptureCancellable(
		func() error { return context.Cause(ctx) },
		request,
		fresh,
	)
	if err != nil {
		result.Detail = boundedOperationDetail(err.Error())
		return result
	}
	return classified
}

func (service *Service) acquirePendingReview(
	ctx context.Context,
	target review.PublicationTarget,
	nodeID review.ReviewNodeID,
	databaseID uint64,
	runCPU CPUAdmission,
) (review.PendingReview, error) {
	details, err := publicationTargetDetails(target)
	if err != nil {
		return review.PendingReview{}, err
	}
	metadata, err := service.pullRequestMetadata(ctx, details.Repository, details.Number, runCPU)
	if err != nil {
		return review.PendingReview{}, err
	}
	details, err = validatePullRequestPayload(details.Repository, details.Number, metadata)
	if err != nil {
		return review.PendingReview{}, err
	}
	fresh, status, detail := service.loadPendingReview(ctx, details, runCPU)
	if cancellationErr := context.Cause(ctx); cancellationErr != nil {
		return review.PendingReview{}, cancellationErr
	}
	if status != review.PendingReviewReady {
		message := "pending review reconciliation is " + status.String()
		if !detail.IsEmpty() {
			message += ": " + detail.String()
		}
		return review.PendingReview{}, errors.New(message)
	}
	if fresh.NodeID() != nodeID || fresh.DatabaseID().Uint64() != databaseID {
		return review.PendingReview{}, errors.New("pending review identity changed during reconciliation")
	}
	return fresh, nil
}

func classifyPendingEffects(
	ctx context.Context,
	fresh review.PendingReview,
	effects []pendingEffect,
) ([]pendingEffectState, error) {
	comments := make(map[review.CommentNodeID]review.PendingComment, fresh.CommentCount())
	markers := make(map[string]pendingMarkerMatch, fresh.CommentCount())
	for start := 0; start < fresh.CommentCount(); start += 256 {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		for _, comment := range fresh.Comments(start, min(256, fresh.CommentCount()-start)) {
			comments[comment.NodeID()] = comment
			marker := comment.DraftMarker()
			if marker == "" {
				continue
			}
			match := markers[marker]
			match.count++
			if match.count == 1 {
				match.comment = comment
			}
			markers[marker] = match
		}
	}
	states := make([]pendingEffectState, len(effects))
	for index, effect := range effects {
		if index%256 == 0 {
			if err := context.Cause(ctx); err != nil {
				return nil, err
			}
		}
		mutation := effect.mutation
		switch mutation.Kind() {
		case review.PendingMutationReviewBody:
			states[index] = classifyPendingBody(fresh.Body(), effect.after, effect.before)
		case review.PendingMutationUpdateComment:
			comment, exists := comments[mutation.NodeID()]
			if !exists {
				states[index] = pendingEffectUnknown
			} else {
				states[index] = classifyPendingBody(comment.Body(), effect.after, effect.before)
			}
		case review.PendingMutationDeleteComment:
			comment, exists := comments[mutation.NodeID()]
			switch {
			case !exists:
				states[index] = pendingEffectCommitted
			case comment.Body() == effect.before:
				states[index] = pendingEffectAbsent
			default:
				states[index] = pendingEffectUnknown
			}
		case review.PendingMutationAddComment:
			states[index] = classifyPendingAddition(
				markers[effect.marker],
				effect.after,
				mutation.Target(),
			)
		}
	}
	return states, nil
}

type pendingMarkerMatch struct {
	comment review.PendingComment
	count   int
}

func classifyPendingAddition(
	match pendingMarkerMatch,
	desired string,
	target review.PendingCommentTarget,
) pendingEffectState {
	if match.count == 0 {
		return pendingEffectAbsent
	}
	if match.count == 1 && match.comment.Body() == desired &&
		match.comment.Target().MatchesPendingAddition(target) {
		return pendingEffectCommitted
	}
	return pendingEffectUnknown
}

func classifyPendingBody(raw, desired, baseline string) pendingEffectState {
	if raw == desired {
		return pendingEffectCommitted
	}
	if raw == baseline {
		return pendingEffectAbsent
	}
	return pendingEffectUnknown
}

func publicationTargetDetails(target review.PublicationTarget) (PullRequestDetails, error) {
	repository, err := NewOwnerRepository(target.Repository())
	if err != nil {
		return PullRequestDetails{}, err
	}
	return PullRequestDetails{
		Repository: repository,
		Number:     PullRequestNumber(target.PullRequest()),
		BaseOID:    target.Base(),
		HeadOID:    target.Head(),
	}, nil
}
