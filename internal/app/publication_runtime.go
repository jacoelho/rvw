package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

type PublicationCheckWork struct {
	ID       review.OperationID
	Document *source.Document
	Review   review.Review
	Request  review.PublicationCheckRequest
}

type PublicationWork struct {
	ID      review.OperationID
	Request review.PublicationRequest
}

type ReconciliationWork struct {
	ID      review.OperationID
	Request review.ReconciliationRequest
}

type publicationJobKind uint8

const (
	publicationCheckJob publicationJobKind = iota + 1
	publicationCommitJob
	publicationReconciliationJob
)

type publicationJob struct {
	kind           publicationJobKind
	check          PublicationCheckWork
	publication    PublicationWork
	reconciliation ReconciliationWork
	permit         *publicationCommitPermit
	ctx            context.Context
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

func (coordinator *Coordinator) SubmitPublicationCheck(work PublicationCheckWork) error {
	if work.ID == 0 || work.Document == nil ||
		work.Request.Operation != work.ID ||
		work.Request.Source == 0 {
		return errors.New("invalid publication check work")
	}
	return coordinator.submitReservedPublication(
		work.ID,
		work.Review.RetainedCommentBytes()+256,
		review.MaxPublicationBytes,
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

func (coordinator *Coordinator) SubmitPublication(work PublicationWork) error {
	if work.ID == 0 || work.Request.Operation != work.ID ||
		work.Request.Checked == nil {
		return errors.New("invalid publication work")
	}
	permit := &publicationCommitPermit{}
	return coordinator.submitReservedPublication(
		work.ID,
		work.Request.Checked.PayloadSize(),
		1<<20,
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

func (coordinator *Coordinator) SubmitReconciliation(work ReconciliationWork) error {
	if work.ID == 0 || work.Request.Operation != work.ID ||
		work.Request.Marker == "" {
		return errors.New("invalid publication reconciliation work")
	}
	return coordinator.submitReservedPublication(
		work.ID,
		512,
		review.MaxPublicationBytes,
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
	id review.OperationID,
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
	if len(coordinator.accepted) >= MaxAcceptedOperations ||
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
	coordinator.required.register(id)
	if enqueue(operationContext) {
		coordinator.updateStatsLocked()
		return nil
	}
	delete(coordinator.accepted, id)
	coordinator.publicationActive = 0
	coordinator.requestBytes -= requestBytes
	coordinator.reservedResult -= resultBytes
	coordinator.required.unregister(id)
	cancel()
	coordinator.updateStatsLocked()
	return ErrCoordinatorSaturated
}

func (coordinator *Coordinator) runPublicationWorker() {
	for job := range coordinator.publicationJobs {
		var id review.OperationID
		switch job.kind {
		case publicationCheckJob:
			id = job.check.ID
		case publicationCommitJob:
			id = job.publication.ID
		case publicationReconciliationJob:
			id = job.reconciliation.ID
		}
		coordinator.required.wait(id)
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
	_ = coordinator.ReportProgress(Progress{
		ID:     job.check.ID,
		Total:  2,
		Detail: boundedOperationDetail("preparing publication"),
	})
	prepared, err := job.check.Review.PreparePublication(job.check.Request.Options)
	if err == nil {
		_ = coordinator.ReportProgress(Progress{
			ID:        job.check.ID,
			Completed: 1,
			Total:     2,
			Detail:    boundedOperationDetail("checking immutable GitHub target"),
		})
		var checked *review.CheckedPublication
		checked, err = coordinator.service.CheckPublication(
			job.ctx,
			job.check.Document,
			prepared,
		)
		if err == nil {
			coordinator.complete(PublicationCheckOutcome{
				ID: job.check.ID,
				Check: review.PublicationCheckResult{
					Operation: job.check.Request.Operation,
					Workspace: job.check.Request.Workspace,
					Source:    job.check.Request.Source,
					Checked:   checked,
				},
			})
			return
		}
	}
	coordinator.complete(PublicationCheckOutcome{
		ID:  job.check.ID,
		Err: err,
	})
}

func (coordinator *Coordinator) runPublicationCommit(job publicationJob) {
	result := review.PublicationResult{
		Operation: job.publication.Request.Operation,
		Workspace: job.publication.Request.Workspace,
		Source:    job.publication.Request.Source,
	}
	if !job.permit.claim() {
		result.Disposition = review.PublicationDispositionCancelledBeforeCommit
		result.Detail = boundedOperationDetail("publication cancelled before remote commit")
	} else {
		_ = coordinator.ReportProgress(Progress{
			ID:     job.publication.ID,
			Total:  1,
			Detail: boundedOperationDetail("committing GitHub review"),
		})
		commit := coordinator.service.Publish(
			job.ctx,
			job.publication.Request.Checked,
		)
		result.Disposition = commit.Disposition
		result.RemoteReview = commit.RemoteReview
		result.Detail = commit.Detail
	}
	coordinator.complete(PublicationCommitOutcome{
		ID:          job.publication.ID,
		Publication: result,
	})
}

func (coordinator *Coordinator) runPublicationReconciliation(job publicationJob) {
	_ = coordinator.ReportProgress(Progress{
		ID:     job.reconciliation.ID,
		Total:  1,
		Detail: boundedOperationDetail("enumerating GitHub review history"),
	})
	result := coordinator.service.ReconcilePublication(
		job.ctx,
		job.reconciliation.Request,
	)
	coordinator.complete(ReconciliationOutcome{
		ID:             job.reconciliation.ID,
		Reconciliation: result,
	})
}
