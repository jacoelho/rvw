package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// StopAdmissionAndCancel performs the stop admission and cancel operation.
func (coordinator *Coordinator) StopAdmissionAndCancel() {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return
	}
	coordinator.closed = true
	close(coordinator.progressStop)
	coordinator.progress = make(map[review.OperationID]Progress)
	coordinator.progressOrder = nil
	coordinator.progressBytes = 0
	coordinator.progressDirty = false
	coordinator.requestBytes -= coordinator.collapseCaptureBytes
	coordinator.collapseCapture = 0
	coordinator.collapseCaptureBytes = 0
	for _, reservation := range coordinator.accepted {
		if reservation.cancelOperation != nil {
			reservation.cancelOperation()
		}
		reservation.cancel()
	}
	close(coordinator.patchJobs)
	close(coordinator.searchJobs)
	close(coordinator.syntaxJobs)
	close(coordinator.publicationJobs)
	close(coordinator.reloadJobs)
	close(coordinator.reloadPreparationJobs)
	close(coordinator.fileTreeJobs)
	close(coordinator.workspaceIndexJobs)
	close(coordinator.commentMutationJobs)
	close(coordinator.exportJobs)
	close(coordinator.deliveryJobs)
	close(coordinator.retireCleanups)
	coordinator.updateStatsLocked()
	coordinator.closeReadyIfFinishedLocked()
}

// Escalate performs the escalate operation.
func (coordinator *Coordinator) Escalate() {
	coordinator.delivery.Escalate()
}

// PublicationShutdownFact represents publication shutdown fact.
type PublicationShutdownFact struct {
	Marker         review.PublicationMarker
	Detail         review.OperationDetail
	Target         review.PublicationTarget
	Disposition    review.PublicationDisposition
	Reconciliation review.ReconciliationDisposition
}

// ShutdownSettlement represents shutdown settlement.
type ShutdownSettlement struct {
	Publications []PublicationShutdownFact
}

type shutdownSettlementScope uint8

const (
	settleCompletedPublication shutdownSettlementScope = iota + 1
	settleActivePublicationAsUnknown
)

// Shutdown performs the shutdown operation.
func (coordinator *Coordinator) Shutdown(ctx context.Context) error {
	_, err := coordinator.ShutdownWithSettlement(ctx)
	return err
}

// ShutdownWithSettlement performs the shutdown with settlement operation.
func (coordinator *Coordinator) ShutdownWithSettlement(
	ctx context.Context,
) (ShutdownSettlement, error) {
	coordinator.StopAdmissionAndCancel()
	select {
	case <-coordinator.done:
	case <-ctx.Done():
		coordinator.Escalate()
		coordinator.mu.Lock()
		settlement := coordinator.shutdownSettlementLocked(
			settleActivePublicationAsUnknown,
		)
		coordinator.mu.Unlock()
		return settlement, fmt.Errorf(
			"shutdown work coordinator: %w",
			context.Cause(ctx),
		)
	}

	coordinator.mu.Lock()
	settlement := coordinator.shutdownSettlementLocked(settleCompletedPublication)
	var shutdownErrors []error
	shutdownErrors = append(shutdownErrors, coordinator.cleanupErrors...)
	if coordinator.cleanupOmitted != 0 {
		shutdownErrors = append(shutdownErrors, fmt.Errorf(
			"%d additional source cleanup errors omitted",
			coordinator.cleanupOmitted,
		))
	}
	for _, outcome := range coordinator.outcomes {
		var document *source.Document
		switch outcome := outcome.(type) {
		case ReloadAcquiredOutcome:
			document = outcome.Document
		case ReloadPreparedOutcome:
			document = outcome.Document
		}
		if document != nil {
			if err := document.Close(); err != nil {
				shutdownErrors = append(
					shutdownErrors,
					fmt.Errorf("release reload document: %w", err),
				)
			}
		}
	}
	for _, reservation := range coordinator.accepted {
		if reservation.cancelOperation != nil {
			reservation.cancelOperation()
		}
		reservation.cancel()
	}
	coordinator.accepted = make(map[review.OperationID]reservation)
	coordinator.publicationActive = 0
	coordinator.collapseCapture = 0
	coordinator.collapseCaptureBytes = 0
	coordinator.outcomes = nil
	coordinator.requestBytes = 0
	coordinator.reservedResult = 0
	coordinator.updateStatsLocked()
	coordinator.closeReadyLocked()
	coordinator.mu.Unlock()
	return settlement, errors.Join(shutdownErrors...)
}

func (coordinator *Coordinator) shutdownSettlementLocked(
	scope shutdownSettlementScope,
) ShutdownSettlement {
	settlement := ShutdownSettlement{}
	recorded := make(map[review.OperationID]struct{})
	for _, outcome := range coordinator.outcomes {
		var (
			id             review.OperationID
			disposition    review.PublicationDisposition
			reconciliation review.ReconciliationDisposition
			detail         review.OperationDetail
		)
		switch outcome := outcome.(type) {
		case PublicationCommitOutcome:
			id = outcome.ID
			disposition = outcome.Disposition
			detail = outcome.Detail
		case ReconciliationOutcome:
			id = outcome.ID
			reconciliation = outcome.Disposition
			detail = outcome.Detail
		default:
			continue
		}
		acceptedReservation, accepted := coordinator.accepted[id]
		if !accepted || acceptedReservation.publication == nil {
			continue
		}
		fact := PublicationShutdownFact{
			Target:         acceptedReservation.publication.target,
			Marker:         acceptedReservation.publication.marker,
			Disposition:    disposition,
			Reconciliation: reconciliation,
			Detail:         detail,
		}
		settlement.Publications = append(settlement.Publications, fact)
		recorded[id] = struct{}{}
	}
	if scope != settleActivePublicationAsUnknown {
		return settlement
	}
	for id, reservation := range coordinator.accepted {
		if reservation.publication == nil {
			continue
		}
		if _, exists := recorded[id]; exists {
			continue
		}
		settlement.Publications = append(
			settlement.Publications,
			PublicationShutdownFact{
				Target:      reservation.publication.target,
				Marker:      reservation.publication.marker,
				Disposition: review.PublicationDispositionUnknown,
				Detail:      boundedOperationDetail("publication worker did not quiesce before shutdown timeout"),
			},
		)
	}
	return settlement
}

// PublicationShutdownSettlement returns the receiver's publication shutdown settlement value.
func (model *Model) PublicationShutdownSettlement() ShutdownSettlement {
	summary := model.review.PublicationSummary()
	if summary.Target.Repository() == "" || summary.Marker == "" {
		return ShutdownSettlement{}
	}
	fact := PublicationShutdownFact{
		Target: summary.Target,
		Marker: summary.Marker,
		Detail: summary.Detail,
	}
	switch summary.Status {
	case review.PublicationPublishing, review.PublicationUnknown:
		fact.Disposition = review.PublicationDispositionUnknown
	case review.PublicationReconciling:
		fact.Reconciliation = review.ReconciliationStillUnknown
	default:
		return ShutdownSettlement{}
	}
	return ShutdownSettlement{Publications: []PublicationShutdownFact{fact}}
}
