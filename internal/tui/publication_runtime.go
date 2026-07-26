package tui

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/source"
)

// PublicationCheckWork represents publication check work.
type PublicationCheckWork struct {
	Target  source.PublicationTarget
	Source  change.PublicationSource
	Request change.PublicationCheckRequest
	ID      change.OperationID
}

// PublicationWork represents publication work.
type PublicationWork struct {
	Request change.PublicationRequest
	Rebase  change.PendingRebaseSource
	ID      change.OperationID
}

// ReconciliationWork represents reconciliation work.
type ReconciliationWork struct {
	Request change.ReconciliationRequest
	Rebase  change.PendingRebaseSource
	ID      change.OperationID
}

type publicationJobKind uint8

const (
	publicationCheckJob publicationJobKind = iota + 1
	publicationCommitJob
	publicationReconciliationJob
)

type publicationJob struct {
	ctx            context.Context
	permit         *publicationCommitPermit
	publication    PublicationWork
	reconciliation ReconciliationWork
	check          PublicationCheckWork
	kind           publicationJobKind
}

type publicationCommitPermit struct {
	state atomic.Uint32
}

const (
	publicationPermitOpen uint32 = iota
	publicationPermitCancelled
	publicationPermitClaimed
)

func (permit *publicationCommitPermit) cancel() {
	permit.state.CompareAndSwap(publicationPermitOpen, publicationPermitCancelled)
}

func (permit *publicationCommitPermit) claim() bool {
	return permit.state.CompareAndSwap(publicationPermitOpen, publicationPermitClaimed)
}

// SubmitPublicationCheck performs the submit publication check operation.
func (coordinator *Coordinator) SubmitPublicationCheck(work PublicationCheckWork) error {
	if work.ID == 0 || work.Target.SourceID() == 0 ||
		work.Request.Operation != work.ID ||
		work.Request.Source == 0 || !work.Source.Ready() ||
		work.Source.Workspace() != work.Request.Workspace ||
		work.Source.Source() != work.Request.Source ||
		work.Target.SourceID() != work.Request.Source {
		return errors.New("invalid publication check work")
	}
	return coordinator.submitReservedPublication(
		work.ID,
		work.Source.RetainedBytes()+work.Target.RetainedBytes()+
			work.Request.Options.RetainedBytes()+256,
		max(publicationResultReservation, work.Source.ConstructionBytes()),
		nil,
		nil,
		func(ctx context.Context) bool {
			select {
			case coordinator.publicationJobs <- publicationJob{
				kind:  publicationCheckJob,
				check: work,
				ctx:   ctx,
			}:
				return true
			default:
				return false
			}
		},
	)
}

// SubmitPublication performs the submit publication operation.
func (coordinator *Coordinator) SubmitPublication(work PublicationWork) error {
	checked := work.Request.Checked
	directDraft := checked != nil && !checked.Pending() &&
		checked.Prepared().Options().Event() == change.PublicationEventDraft
	if work.ID == 0 || work.Request.Operation != work.ID ||
		checked == nil ||
		(checked.Pending() || directDraft) != work.Rebase.Ready() ||
		work.Rebase.Ready() && (work.Rebase.Workspace() != work.Request.Workspace ||
			work.Rebase.Source() != work.Request.Source) {
		return errors.New("invalid publication work")
	}
	permit := &publicationCommitPermit{}
	return coordinator.submitReservedPublication(
		work.ID,
		work.Request.Checked.RetainedBytes()+work.Rebase.RetainedBytes(),
		publicationResultReservation,
		&publicationReservation{
			target: work.Request.Checked.Target(),
			marker: work.Request.Checked.Marker(),
		},
		permit.cancel,
		func(ctx context.Context) bool {
			select {
			case coordinator.publicationJobs <- publicationJob{
				kind:        publicationCommitJob,
				publication: work,
				permit:      permit,
				ctx:         ctx,
			}:
				return true
			default:
				return false
			}
		},
	)
}

// SubmitReconciliation performs the submit reconciliation operation.
func (coordinator *Coordinator) SubmitReconciliation(work ReconciliationWork) error {
	if work.ID == 0 || work.Request.Operation != work.ID ||
		work.Request.Marker == "" {
		return errors.New("invalid publication reconciliation work")
	}
	_, phase, pending := work.Request.PendingContinuation()
	if pending && phase == 0 {
		return errors.New("invalid publication reconciliation continuation")
	}
	if pending != work.Rebase.Ready() || work.Rebase.Ready() &&
		(work.Rebase.Workspace() != work.Request.Workspace ||
			work.Rebase.Source() != work.Request.Source) {
		return errors.New("invalid publication reconciliation rebase source")
	}
	return coordinator.submitReservedPublication(
		work.ID,
		work.Request.RetainedBytes()+work.Rebase.RetainedBytes(),
		publicationResultReservation,
		&publicationReservation{
			target: work.Request.Target,
			marker: work.Request.Marker,
		},
		nil,
		func(ctx context.Context) bool {
			select {
			case coordinator.publicationJobs <- publicationJob{
				kind:           publicationReconciliationJob,
				reconciliation: work,
				ctx:            ctx,
			}:
				return true
			default:
				return false
			}
		},
	)
}

func (coordinator *Coordinator) submitReservedPublication(
	id change.OperationID,
	requestBytes, resultBytes int64,
	publication *publicationReservation,
	cancelOperation func(),
	enqueue func(context.Context) bool,
) error {
	if requestBytes < 0 || resultBytes < 0 {
		return errors.New("invalid publication reservation")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return ErrCoordinatorClosed
	}
	if coordinator.publicationActive != 0 {
		return ErrCoordinatorSaturated
	}
	if _, exists := coordinator.accepted[id]; exists {
		return fmt.Errorf("operation %d is already accepted", id)
	}
	active := len(coordinator.accepted)
	if coordinator.collapseCapture != 0 {
		active++
	}
	if active >= MaxAcceptedOperations ||
		coordinator.requestBytes+requestBytes > MaxQueuedRequestBytes ||
		coordinator.reservedResult+resultBytes > MaxRetainedResultBytes {
		return ErrCoordinatorSaturated
	}
	operationContext, cancel := context.WithCancel(coordinator.workContext)
	coordinator.accepted[id] = reservation{
		requestBytes:    requestBytes,
		resultBytes:     resultBytes,
		cancel:          cancel,
		cancelOperation: cancelOperation,
		publication:     publication,
	}
	coordinator.publicationActive = id
	coordinator.requestBytes += requestBytes
	coordinator.reservedResult += resultBytes
	coordinator.required.Register(id)
	if enqueue(operationContext) {
		coordinator.updateStatsLocked()
		return nil
	}
	delete(coordinator.accepted, id)
	coordinator.publicationActive = 0
	coordinator.requestBytes -= requestBytes
	coordinator.reservedResult -= resultBytes
	coordinator.required.Unregister(id)
	cancel()
	coordinator.updateStatsLocked()
	return ErrCoordinatorSaturated
}

func (coordinator *Coordinator) runPublicationWorker() {
	for job := range coordinator.publicationJobs {
		var id change.OperationID
		switch job.kind {
		case publicationCheckJob:
			id = job.check.ID
		case publicationCommitJob:
			id = job.publication.ID
		case publicationReconciliationJob:
			id = job.reconciliation.ID
		}
		coordinator.required.Wait(id)
		switch job.kind {
		case publicationCheckJob:
			coordinator.runPublicationCheck(job)
		case publicationCommitJob:
			coordinator.runPublicationCommit(job)
		case publicationReconciliationJob:
			coordinator.runPublicationReconciliation(job)
		}
	}
}

func (coordinator *Coordinator) runPublicationCheck(job publicationJob) {
	coordinator.reportProgress(Progress{
		ID:     job.check.ID,
		Total:  2,
		Detail: boundedOperationDetail("preparing publication"),
	})
	checked, err := coordinator.preparePublicationCheck(&job)
	if err != nil {
		coordinator.complete(PublicationCheckOutcome{ID: job.check.ID, Err: err})
		return
	}
	coordinator.complete(PublicationCheckOutcome{
		ID: job.check.ID,
		Check: change.PublicationCheckResult{
			Operation: job.check.Request.Operation,
			Workspace: job.check.Request.Workspace,
			Source:    job.check.Request.Source,
			Checked:   checked,
		},
	})
}

func (coordinator *Coordinator) preparePublicationCheck(
	job *publicationJob,
) (*change.CheckedPublication, error) {
	publicationSource := job.check.Source
	job.check.Source = change.PublicationSource{}
	if !coordinator.acquireCPU(job.ctx) {
		return nil, context.Cause(job.ctx)
	}
	prepared, err := publicationSource.PreparePublicationCancellable(
		func() error { return context.Cause(job.ctx) },
		job.check.Request.Options,
	)
	coordinator.releaseCPU()
	if err != nil {
		return nil, err
	}

	coordinator.reportProgress(Progress{
		ID:        job.check.ID,
		Completed: 1,
		Total:     2,
		Detail:    boundedOperationDetail("checking immutable GitHub target"),
	})
	mode := source.PublicationCheckNew
	if prepared.ContinuesPendingPublication() {
		mode = source.PublicationCheckPendingContinuation
	}
	acquisition, err := coordinator.service.AcquirePublicationCheck(
		job.ctx,
		job.check.Target,
		prepared.Options(),
		mode,
		coordinator.runCPU,
	)
	if err != nil {
		return nil, err
	}
	if fresh, ok := acquisition.FreshPending(); ok {
		prepared, err = coordinator.preparePendingPublicationCheck(job, prepared, fresh)
		if err != nil {
			// The remote state was only read; rejecting local construction cannot
			// make the publication outcome ambiguous.
			return nil, err
		}
	}
	if !coordinator.acquireCPU(job.ctx) {
		return nil, context.Cause(job.ctx)
	}
	checked, err := source.FinishPublicationCheckCancellable(
		func() error { return context.Cause(job.ctx) },
		acquisition,
		prepared,
	)
	coordinator.releaseCPU()
	if err != nil {
		return nil, err
	}
	return checked, nil
}

func (coordinator *Coordinator) preparePendingPublicationCheck(
	job *publicationJob,
	prepared change.PreparedPublication,
	fresh change.PendingReview,
) (change.PreparedPublication, error) {
	construction := prepared.PendingReconciliationConstructionBytes(fresh)
	if construction == 0 {
		return change.PreparedPublication{}, change.ErrInvalidPublication
	}
	if err := coordinator.growResultReservation(job.check.ID, construction); err != nil {
		return change.PreparedPublication{}, err
	}
	if !coordinator.acquireCPU(job.ctx) {
		return change.PreparedPublication{}, context.Cause(job.ctx)
	}
	reconciled, err := prepared.ReconcilePendingPublicationCancellable(
		func() error { return context.Cause(job.ctx) },
		fresh,
	)
	coordinator.releaseCPU()
	return reconciled, err
}

func (coordinator *Coordinator) runPublicationCommit(job publicationJob) {
	result := change.PublicationResult{
		Operation: job.publication.Request.Operation,
		Workspace: job.publication.Request.Workspace,
		Source:    job.publication.Request.Source,
	}
	checked := job.publication.Request.Checked
	switch {
	case checked.Pending():
		result = coordinator.runPendingPublicationCommit(job, result, checked)
	case !job.permit.claim():
		result.Disposition = change.PublicationDispositionCancelledBeforeCommit
		result.Detail = boundedOperationDetail("publication cancelled before remote commit")
	default:
		coordinator.reportProgress(Progress{
			ID:     job.publication.ID,
			Total:  1,
			Detail: boundedOperationDetail("committing GitHub review"),
		})
		commit := coordinator.service.Publish(job.ctx, checked)
		result.Disposition = commit.Disposition
		result.PendingPhase = commit.PendingPhase
		result.RemoteReview = commit.RemoteReview
		result.Pending = commit.Pending
		result.PendingRefresh = commit.PendingRefresh
		result.Detail = commit.Detail
	}
	prepared, err := coordinator.preparePublicationResult(job, result)
	result.Pending = nil
	job.publication.Rebase = change.PendingRebaseSource{}
	coordinator.complete(PublicationCommitOutcome{
		ID:          job.publication.ID,
		Publication: prepared,
		Disposition: prepared.Disposition(),
		Detail:      prepared.Detail(),
		Err:         err,
	})
}

func (coordinator *Coordinator) runPendingPublicationCommit(
	job publicationJob,
	result change.PublicationResult,
	checked *change.CheckedPublication,
) change.PublicationResult {
	var execution source.PendingPublicationExecution
	var err error
	if !coordinator.acquireCPU(job.ctx) {
		err = context.Cause(job.ctx)
	} else {
		construction, constructionErr := source.PendingPublicationExecutionConstructionBytes(
			job.ctx,
			checked,
		)
		coordinator.releaseCPU()
		if constructionErr != nil {
			err = constructionErr
		} else {
			err = coordinator.growResultReservation(job.publication.ID, construction)
		}
	}
	if err == nil {
		if !coordinator.acquireCPU(job.ctx) {
			err = context.Cause(job.ctx)
		} else {
			execution, err = source.PreparePendingPublicationExecution(job.ctx, checked)
			coordinator.releaseCPU()
		}
	}
	if err != nil {
		result.Disposition = change.PublicationDispositionFailedBeforeCommit
		if context.Cause(job.ctx) != nil {
			result.Disposition = change.PublicationDispositionCancelledBeforeCommit
		}
		result.Detail = boundedOperationDetail(err.Error())
		return result
	}
	defer execution.Close()
	if !job.permit.claim() {
		result.Disposition = change.PublicationDispositionCancelledBeforeCommit
		result.Detail = boundedOperationDetail("publication cancelled before remote commit")
		return result
	}
	coordinator.reportProgress(Progress{
		ID:     job.publication.ID,
		Total:  1,
		Detail: boundedOperationDetail("committing GitHub review"),
	})
	step := coordinator.service.AcquirePendingPublication(job.ctx, execution, coordinator.runCPU)
	for {
		switch step.Kind() {
		case source.PendingPublicationStepAcquired:
			construction := execution.RetainedBytes() + step.ConstructionBytes()
			if construction == 0 {
				err = change.ErrInvalidPublication
			} else {
				err = coordinator.growResultReservation(job.publication.ID, construction)
			}
			if err == nil && coordinator.acquireCPU(job.ctx) {
				step = step.Finish(job.ctx)
				coordinator.releaseCPU()
			} else if err == nil {
				err = context.Cause(job.ctx)
			}
			if err != nil {
				step.Close()
				return applyPendingPublicationCommit(result, reviewUnknownPendingCommit(err))
			}
		case source.PendingPublicationStepContinue:
			if err = step.Advance(); err != nil {
				return applyPendingPublicationCommit(result, reviewUnknownPendingCommit(err))
			}
			step = coordinator.service.AcquirePendingPublication(job.ctx, execution, coordinator.runCPU)
		case source.PendingPublicationStepTerminal:
			commit, takeErr := step.TakeCommit()
			if takeErr != nil {
				commit = reviewUnknownPendingCommit(takeErr)
			}
			return applyPendingPublicationCommit(result, commit)
		default:
			return applyPendingPublicationCommit(
				result,
				reviewUnknownPendingCommit(errors.New("pending publication step is invalid")),
			)
		}
	}
}

func applyPendingPublicationCommit(
	result change.PublicationResult,
	commit source.PublicationCommit,
) change.PublicationResult {
	result.Disposition = commit.Disposition
	result.PendingPhase = commit.PendingPhase
	result.RemoteReview = commit.RemoteReview
	result.Pending = commit.Pending
	result.PendingRefresh = commit.PendingRefresh
	result.Detail = commit.Detail
	return result
}

func reviewUnknownPendingCommit(err error) source.PublicationCommit {
	return source.PublicationCommit{
		Disposition:  change.PublicationDispositionUnknown,
		PendingPhase: change.PendingPublicationIndeterminate,
		Detail:       boundedOperationDetail(err.Error()),
	}
}

//nolint:dupl // The typed result adapters are intentionally explicit; prepareReservedTerminal owns the shared algorithm.
func (coordinator *Coordinator) preparePublicationResult(
	job publicationJob,
	result change.PublicationResult,
) (change.PreparedPublicationResult, error) {
	request := job.publication.Request
	source := job.publication.Rebase
	return prepareReservedTerminal(
		job.ctx,
		coordinator,
		job.publication.ID,
		func() int64 {
			return request.PublicationResultConstructionBytes(source, result)
		},
		func() (change.PreparedPublicationResult, error) {
			return change.PreparePublicationResultCancellable(
				func() error { return context.Cause(job.ctx) },
				request,
				source,
				result,
			)
		},
		func() (change.PreparedPublicationResult, error) {
			// The remote call crossed its commit boundary. Cancellation may stop
			// proportional refresh work, but cannot discard the terminal fact.
			return change.PreparePublicationResultFallbackCancellable(
				nil,
				request,
				source,
				result,
			)
		},
		func(failure error) (change.PreparedPublicationResult, error) {
			return change.PrepareUnknownPublicationResult(
				request,
				boundedOperationDetail(failure.Error()),
			)
		},
	)
}

func (coordinator *Coordinator) runPublicationReconciliation(job publicationJob) {
	coordinator.reportProgress(Progress{
		ID:     job.reconciliation.ID,
		Total:  1,
		Detail: boundedOperationDetail("enumerating GitHub review history"),
	})
	prepared, err := coordinator.preparePublicationReconciliation(job)
	job.reconciliation.Rebase = change.PendingRebaseSource{}
	coordinator.complete(ReconciliationOutcome{
		ID:             job.reconciliation.ID,
		Reconciliation: prepared,
		Disposition:    prepared.Disposition(),
		Detail:         prepared.Detail(),
		Err:            err,
	})
}

func (coordinator *Coordinator) preparePublicationReconciliation(
	job publicationJob,
) (change.PreparedReconciliationResult, error) {
	request := job.reconciliation.Request
	_, phase, pending := request.PendingContinuation()
	switch {
	case pending && phase != change.PendingPublicationSubmitting:
		return coordinator.preparePendingCaptureReconciliation(job)
	case pending:
		result := coordinator.runPendingSubmissionReconciliation(job)
		return coordinator.prepareReconciliationResult(job, result)
	default:
		result := coordinator.runReviewHistoryReconciliation(job)
		return coordinator.prepareReconciliationResult(job, result)
	}
}

func (coordinator *Coordinator) preparePendingCaptureReconciliation(
	job publicationJob,
) (change.PreparedReconciliationResult, error) {
	request := job.reconciliation.Request
	acquisition, err := coordinator.service.AcquirePendingPublicationReconciliation(
		job.ctx,
		request,
		coordinator.runCPU,
	)
	if err != nil {
		return coordinator.prepareReconciliationResult(
			job,
			failedReconciliationResult(request, err),
		)
	}
	defer acquisition.Close()
	fresh, ok := acquisition.TakePendingCapture()
	if !ok {
		return coordinator.prepareReconciliationResult(
			job,
			failedReconciliationResult(request, change.ErrInvalidPublication),
		)
	}

	construction, err := coordinator.pendingCaptureReconciliationConstruction(job, fresh)
	if err == nil {
		err = coordinator.growResultReservation(job.reconciliation.ID, construction)
	}
	if err != nil {
		return coordinator.prepareReconciliationResult(
			job,
			failedReconciliationResult(request, err),
		)
	}
	if !coordinator.acquireCPU(job.ctx) {
		return coordinator.prepareReconciliationResult(
			job,
			failedReconciliationResult(request, context.Cause(job.ctx)),
		)
	}
	prepared, prepareErr := change.PreparePendingReconciliationCaptureCancellable(
		func() error { return context.Cause(job.ctx) },
		request,
		job.reconciliation.Rebase,
		fresh,
	)
	coordinator.releaseCPU()
	if prepareErr != nil {
		return coordinator.prepareReconciliationResult(
			job,
			failedReconciliationResult(request, prepareErr),
		)
	}
	return prepared, nil
}

func (coordinator *Coordinator) pendingCaptureReconciliationConstruction(
	job publicationJob,
	fresh change.PendingReview,
) (int64, error) {
	if !coordinator.acquireCPU(job.ctx) {
		return 0, context.Cause(job.ctx)
	}
	construction := job.reconciliation.Request.PendingReconciliationCaptureConstructionBytes(
		job.reconciliation.Rebase,
		fresh,
	)
	coordinator.releaseCPU()
	if construction == 0 {
		return 0, change.ErrInvalidPublication
	}
	return construction, nil
}

func (coordinator *Coordinator) runPendingSubmissionReconciliation(
	job publicationJob,
) change.ReconciliationResult {
	request := job.reconciliation.Request
	acquisition, err := coordinator.service.AcquirePendingPublicationReconciliation(
		job.ctx,
		request,
		coordinator.runCPU,
	)
	if err != nil {
		return failedReconciliationResult(request, err)
	}
	defer acquisition.Close()
	construction := acquisition.ConstructionBytes()
	switch {
	case construction == 0:
		err = change.ErrInvalidPublication
	case construction > MaxRetainedResultBytes:
		err = ErrCoordinatorSaturated
	default:
		err = coordinator.growResultReservation(job.reconciliation.ID, construction)
	}
	if err != nil {
		return failedReconciliationResult(request, err)
	}
	if !coordinator.acquireCPU(job.ctx) {
		return failedReconciliationResult(request, context.Cause(job.ctx))
	}
	result := acquisition.Finish(job.ctx)
	coordinator.releaseCPU()
	return result
}

func (coordinator *Coordinator) runReviewHistoryReconciliation(
	job publicationJob,
) change.ReconciliationResult {
	request := job.reconciliation.Request
	acquisition, err := coordinator.service.AcquirePublicationReconciliation(job.ctx, request)
	if err != nil {
		return failedReconciliationResult(request, err)
	}
	return acquisition.Finish(job.ctx, coordinator.runCPU)
}

//nolint:dupl // The typed result adapters are intentionally explicit; prepareReservedTerminal owns the shared algorithm.
func (coordinator *Coordinator) prepareReconciliationResult(
	job publicationJob,
	result change.ReconciliationResult,
) (change.PreparedReconciliationResult, error) {
	request := job.reconciliation.Request
	source := job.reconciliation.Rebase
	return prepareReservedTerminal(
		job.ctx,
		coordinator,
		job.reconciliation.ID,
		func() int64 {
			return request.ReconciliationResultConstructionBytes(source, result)
		},
		func() (change.PreparedReconciliationResult, error) {
			return change.PrepareReconciliationResultCancellable(
				func() error { return context.Cause(job.ctx) },
				request,
				source,
				result,
			)
		},
		func() (change.PreparedReconciliationResult, error) {
			// Reconciliation produced a terminal fact. Finish the fixed fallback
			// during shutdown so settlement never invents a disposition.
			return change.PrepareReconciliationResultFallbackCancellable(
				nil,
				request,
				source,
				result,
			)
		},
		func(failure error) (change.PreparedReconciliationResult, error) {
			return change.PrepareStillUnknownReconciliationResult(
				request,
				boundedOperationDetail(failure.Error()),
			)
		},
	)
}

func prepareReservedTerminal[T any](
	ctx context.Context,
	coordinator *Coordinator,
	id change.OperationID,
	constructionBytes func() int64,
	prepare func() (T, error),
	fallback func() (T, error),
	safeFallback func(error) (T, error),
) (T, error) {
	var zero T
	var prepareErr error
	if !coordinator.acquireCPU(ctx) {
		prepareErr = context.Cause(ctx)
	} else {
		construction := constructionBytes()
		coordinator.releaseCPU()
		switch {
		case construction == 0:
			prepareErr = change.ErrInvalidPublication
		case construction > MaxRetainedResultBytes:
			prepareErr = ErrCoordinatorSaturated
		default:
			prepareErr = coordinator.growResultReservation(id, construction)
		}
		if prepareErr == nil {
			if !coordinator.acquireCPU(ctx) {
				prepareErr = context.Cause(ctx)
			} else {
				prepared, err := prepare()
				coordinator.releaseCPU()
				if err == nil {
					return prepared, nil
				}
				prepareErr = err
			}
		}
	}
	prepared, fallbackErr := fallback()
	if fallbackErr == nil {
		return prepared, nil
	}
	failure := errors.Join(prepareErr, fallbackErr)
	prepared, safeErr := safeFallback(failure)
	if safeErr == nil {
		return prepared, nil
	}
	return zero, errors.Join(failure, safeErr)
}

func failedReconciliationResult(
	request change.ReconciliationRequest,
	failure error,
) change.ReconciliationResult {
	detail := "publication reconciliation failed"
	if failure != nil {
		detail = failure.Error()
	}
	return change.ReconciliationResult{
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: change.ReconciliationStillUnknown,
		Detail:      boundedOperationDetail(detail),
	}
}
