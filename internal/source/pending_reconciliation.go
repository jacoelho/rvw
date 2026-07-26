package source

import (
	"context"
	"errors"

	"github.com/jacoelho/rvw/internal/change"
)

// PendingPublicationReconciliationAcquisition owns bounded remote state until
// CPU-only continuation classification completes.
type PendingPublicationReconciliationAcquisition struct {
	checked  *change.CheckedPublication
	response []byte
	fresh    change.PendingReview
	request  change.ReconciliationRequest
	phase    change.PendingPublicationPhase
	valid    bool
}

// AcquirePendingPublicationReconciliation performs only remote acquisition.
func (service *Service) AcquirePendingPublicationReconciliation(
	ctx context.Context,
	request change.ReconciliationRequest,
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
	case change.PendingPublicationSubmitting:
		response, err := service.acquirePendingSubmission(
			ctx,
			checked.Target(),
			view.BaselineDatabaseID().Uint64(),
		)
		if err != nil {
			return nil, err
		}
		acquisition.response = response
	case change.PendingPublicationSynchronizing, change.PendingPublicationIndeterminate:
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
	change.PendingReview,
	bool,
) {
	if acquisition == nil || !acquisition.valid || acquisition.checked == nil ||
		acquisition.phase == change.PendingPublicationSubmitting ||
		acquisition.fresh.NodeID() == "" {
		return change.PendingReview{}, false
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
) change.ReconciliationResult {
	if acquisition == nil || !acquisition.valid || acquisition.checked == nil {
		return change.ReconciliationResult{
			Disposition: change.ReconciliationStillUnknown,
			Detail:      boundedOperationDetail("pending reconciliation acquisition is unavailable"),
		}
	}
	request := acquisition.request
	checked := acquisition.checked
	fresh := acquisition.fresh
	response := acquisition.response
	phase := acquisition.phase
	acquisition.Close()
	result := change.ReconciliationResult{
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: change.ReconciliationStillUnknown,
	}
	view, ok := checked.Prepared().BorrowPendingPublication()
	if !ok {
		result.Detail = boundedOperationDetail("pending publication continuation is unavailable")
		return result
	}
	if phase == change.PendingPublicationSubmitting {
		mutation, exists := view.MutationAt(0)
		if !exists || mutation.Kind() != change.PendingMutationReviewBody {
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
			result.Disposition = change.ReconciliationCommitted
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
	classified, err := change.ClassifyPendingReconciliationCaptureCancellable(
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
	target change.PublicationTarget,
	nodeID change.ReviewNodeID,
	databaseID uint64,
	runCPU CPUAdmission,
) (change.PendingReview, error) {
	details, err := publicationTargetDetails(target)
	if err != nil {
		return change.PendingReview{}, err
	}
	metadata, err := service.pullRequestMetadata(ctx, details.Repository, details.Number, runCPU)
	if err != nil {
		return change.PendingReview{}, err
	}
	details, err = validatePullRequestPayload(details.Repository, details.Number, metadata)
	if err != nil {
		return change.PendingReview{}, err
	}
	fresh, status, detail := service.loadPendingReview(ctx, details, runCPU)
	if cancellationErr := context.Cause(ctx); cancellationErr != nil {
		return change.PendingReview{}, cancellationErr
	}
	if status != change.PendingReviewReady {
		message := "pending review reconciliation is " + status.String()
		if !detail.IsEmpty() {
			message += ": " + detail.String()
		}
		return change.PendingReview{}, errors.New(message)
	}
	if fresh.NodeID() != nodeID || fresh.DatabaseID().Uint64() != databaseID {
		return change.PendingReview{}, errors.New("pending review identity changed during reconciliation")
	}
	return fresh, nil
}

func classifyPendingEffects(
	ctx context.Context,
	fresh change.PendingReview,
	effects []pendingEffect,
) ([]pendingEffectState, error) {
	comments := make(map[change.CommentNodeID]change.PendingComment, fresh.CommentCount())
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
		case change.PendingMutationReviewBody:
			states[index] = classifyPendingBody(fresh.Body(), effect.after, effect.before)
		case change.PendingMutationUpdateComment:
			comment, exists := comments[mutation.NodeID()]
			if !exists {
				states[index] = pendingEffectUnknown
			} else {
				states[index] = classifyPendingBody(comment.Body(), effect.after, effect.before)
			}
		case change.PendingMutationDeleteComment:
			comment, exists := comments[mutation.NodeID()]
			switch {
			case !exists:
				states[index] = pendingEffectCommitted
			case comment.Body() == effect.before:
				states[index] = pendingEffectAbsent
			default:
				states[index] = pendingEffectUnknown
			}
		case change.PendingMutationAddComment:
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
	comment change.PendingComment
	count   int
}

func classifyPendingAddition(
	match pendingMarkerMatch,
	desired string,
	target change.PendingCommentTarget,
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

func publicationTargetDetails(target change.PublicationTarget) (PullRequestDetails, error) {
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
