package source

import (
	"context"
	"errors"

	"github.com/jacoelho/rvw/internal/review"
)

type pendingPublicationProgress uint8

const (
	pendingPublicationUncommitted pendingPublicationProgress = iota + 1
	pendingPublicationPartiallyCommitted
)

type pendingPublicationReconciliation uint8

const (
	pendingPublicationNoReconciliation pendingPublicationReconciliation = iota + 1
	pendingPublicationReconcileMutation
	pendingPublicationReconcileSubmission
)

type pendingPublicationExecutionStatus uint8

const pendingPublicationExecutionOpen pendingPublicationExecutionStatus = 1

type pendingPublicationAcquisitionKind uint8

const (
	pendingMutationResponse pendingPublicationAcquisitionKind = iota + 1
	pendingMutationReconciliation
	pendingSubmissionResponse
	pendingSubmissionReconciliation
)

type pendingPublicationUncertainty uint8

const (
	pendingPublicationNoUncertainty pendingPublicationUncertainty = iota + 1
	pendingPublicationUnclassifiedMutation
	pendingPublicationMutationReconciliation
	pendingPublicationUnclassifiedSubmission
	pendingPublicationSubmissionReconciliation
)

// PendingPublicationStepKind identifies the only valid outcomes of acquiring
// or finishing one pending-publication protocol step.
type PendingPublicationStepKind uint8

const (
	// PendingPublicationStepAcquired requires bounded CPU-only classification.
	PendingPublicationStepAcquired PendingPublicationStepKind = iota + 1
	// PendingPublicationStepContinue permits acquisition of the next protocol step.
	PendingPublicationStepContinue
	// PendingPublicationStepTerminal owns the final publication commit.
	PendingPublicationStepTerminal
)

// PendingPublicationExecution is an opaque handle to one prepared, bounded
// pending-review commit protocol. Copies share the same execution state and
// must drive it serially.
type PendingPublicationExecution struct {
	state *pendingPublicationExecutionState
}

type pendingPublicationExecutionState struct {
	pendingFailure error
	retry          pendingMutationBatch
	submission     pendingMutationBatch
	expectedState  string
	expectedBody   string
	marker         review.PublicationMarker
	reviewID       review.ReviewNodeID
	batches        []pendingMutationBatch
	outstanding    *pendingPublicationStepCell
	target         review.PublicationTarget
	databaseID     uint64
	retainedBytes  int64
	nextBatch      int

	reconciliation    pendingPublicationReconciliation
	status            pendingPublicationExecutionStatus
	progress          pendingPublicationProgress
	batchAttempt      uint8
	submissionAttempt uint8
}

// PendingPublicationStep is one consuming result from the pending-publication
// protocol. Copied steps share the same cell and must be used serially; only
// the first valid consumer can finish, advance, take, or close it.
type PendingPublicationStep struct {
	cell *pendingPublicationStepCell
}

type pendingPublicationStepCell struct {
	owner       *pendingPublicationExecutionState
	failure     error
	response    []byte
	commit      PublicationCommit
	fresh       review.PendingReview
	kind        PendingPublicationStepKind
	acquisition pendingPublicationAcquisitionKind
}

// RetainedBytes returns the complete prepared execution ownership.
func (execution PendingPublicationExecution) RetainedBytes() int64 {
	if execution.state == nil || execution.state.status != pendingPublicationExecutionOpen {
		return 0
	}
	return execution.state.retainedBytes
}

// Close releases all operation-local ownership. Because copied handles share
// state, closing any copy invalidates every copy and any outstanding step.
func (execution PendingPublicationExecution) Close() {
	if execution.state == nil {
		return
	}
	execution.state.invalidate()
}

func (state *pendingPublicationExecutionState) invalidate() {
	if state == nil {
		return
	}
	if state.outstanding != nil {
		*state.outstanding = pendingPublicationStepCell{}
	}
	*state = pendingPublicationExecutionState{}
}

func (state *pendingPublicationExecutionState) currentBatch() pendingMutationBatch {
	if state.retry.payload != "" {
		return state.retry
	}
	if state.nextBatch < 0 || state.nextBatch >= len(state.batches) {
		return pendingMutationBatch{}
	}
	return state.batches[state.nextBatch]
}

func (state *pendingPublicationExecutionState) advanceBatch() {
	state.retry = pendingMutationBatch{}
	state.batchAttempt = 0
	state.nextBatch++
	state.progress = pendingPublicationPartiallyCommitted
}

func (state *pendingPublicationExecutionState) refreshRetainedBytes() {
	retained := pendingExecutionMetadataBytes + int64(
		len(state.target.Repository())+len(state.target.Base().String())+
			len(state.target.Head().String())+len(state.reviewID)+len(state.marker)+
			len(state.expectedBody)+len(state.expectedState),
	)
	retained += int64(cap(state.batches)) * pendingBatchMetadataBytes
	for _, batch := range state.batches {
		retained += batch.retainedBytes()
	}
	retained += state.retry.retainedBytes()
	retained += state.submission.retainedBytes()
	state.retainedBytes = retained
}

// Kind returns the step's current closed outcome. A zero result is invalid or
// was already consumed through another copy.
func (step *PendingPublicationStep) Kind() PendingPublicationStepKind {
	if !step.available() {
		return 0
	}
	return step.cell.kind
}

func (step *PendingPublicationStep) available() bool {
	return step != nil && step.cell != nil && step.cell.owner != nil &&
		step.cell.owner.status == pendingPublicationExecutionOpen &&
		step.cell.owner.outstanding == step.cell
}

// ConstructionBytes returns the conservative simultaneous acquisition,
// classification, and retry-encoding peak.
func (step *PendingPublicationStep) ConstructionBytes() int64 {
	if !step.available() || step.cell.kind != PendingPublicationStepAcquired {
		return 0
	}
	state := step.cell.owner
	batch := state.currentBatch()
	return pendingExecutionMetadataBytes + int64(len(step.cell.response)) +
		step.cell.fresh.RetainedBytes() +
		int64(step.cell.fresh.CommentCount())*(pendingEffectMetadataBytes+pendingAliasMetadataBytes) +
		int64(len(batch.effects))*pendingEffectMetadataBytes +
		review.MaxPendingMutationBatchBytes + pendingMutationResponseBytes + 4_096
}

// Advance consumes a continuation result so its execution can acquire the next
// network step.
func (step *PendingPublicationStep) Advance() error {
	_, err := step.take(PendingPublicationStepContinue)
	return err
}

// TakeCommit consumes a terminal result and invalidates its execution before
// transferring the commit to the caller.
func (step *PendingPublicationStep) TakeCommit() (PublicationCommit, error) {
	cell, err := step.take(PendingPublicationStepTerminal)
	if err != nil {
		return PublicationCommit{}, err
	}
	cell.owner.invalidate()
	return cell.commit, nil
}

// Close consumes an unfinished step. Closing acquired remote state preserves
// the uncertainty fence and forces the next acquisition through a safe read;
// it never makes a direct GitHub write eligible for retry.
func (step *PendingPublicationStep) Close() {
	cell, err := step.takeAny()
	if err != nil {
		return
	}
	state := cell.owner
	switch cell.kind {
	case PendingPublicationStepAcquired:
		failure := state.pendingFailure
		if failure == nil {
			failure = cell.failure
		}
		if failure == nil {
			failure = errors.New("pending publication acquisition closed before classification")
		}
		state.pendingFailure = failure
		switch cell.acquisition {
		case pendingMutationResponse, pendingMutationReconciliation:
			state.reconciliation = pendingPublicationReconcileMutation
		case pendingSubmissionResponse, pendingSubmissionReconciliation:
			state.reconciliation = pendingPublicationReconcileSubmission
		default:
			state.invalidate()
		}
	case PendingPublicationStepContinue:
		// A continuation contains no unclassified remote state. Consuming it
		// leaves the already-classified next transition eligible.
	default:
		state.invalidate()
	}
}

func (step *PendingPublicationStep) take(
	want PendingPublicationStepKind,
) (pendingPublicationStepCell, error) {
	if step == nil || !step.available() {
		return pendingPublicationStepCell{}, errors.New("pending publication step is unavailable")
	}
	if step.cell.kind != want {
		return pendingPublicationStepCell{}, errors.New("pending publication step kind is invalid")
	}
	return step.takeAvailable(), nil
}

func (step *PendingPublicationStep) takeAny() (pendingPublicationStepCell, error) {
	if step == nil || !step.available() {
		return pendingPublicationStepCell{}, errors.New("pending publication step is unavailable")
	}
	return step.takeAvailable(), nil
}

func (step *PendingPublicationStep) takeAvailable() pendingPublicationStepCell {
	cell := step.cell
	value := *cell
	value.owner.outstanding = nil
	*cell = pendingPublicationStepCell{}
	step.cell = nil
	return value
}

func (state *pendingPublicationExecutionState) newStep(
	cell pendingPublicationStepCell,
) PendingPublicationStep {
	if state == nil || state.status != pendingPublicationExecutionOpen || state.outstanding != nil {
		return standalonePendingPublicationTerminal(unknownPendingMutation(
			errors.New("pending publication execution transition is invalid"),
		))
	}
	cell.owner = state
	state.outstanding = new(cell)
	return PendingPublicationStep{cell: state.outstanding}
}

func (state *pendingPublicationExecutionState) acquiredStep(
	kind pendingPublicationAcquisitionKind,
	response []byte,
	fresh review.PendingReview,
	failure error,
) PendingPublicationStep {
	return state.newStep(pendingPublicationStepCell{
		failure: failure, response: response, fresh: fresh,
		kind: PendingPublicationStepAcquired, acquisition: kind,
	})
}

func (state *pendingPublicationExecutionState) continuationStep() PendingPublicationStep {
	return state.newStep(pendingPublicationStepCell{kind: PendingPublicationStepContinue})
}

func (state *pendingPublicationExecutionState) terminalStep(
	commit PublicationCommit,
) PendingPublicationStep {
	return state.newStep(pendingPublicationStepCell{
		kind:   PendingPublicationStepTerminal,
		commit: commit,
	})
}

func (state *pendingPublicationExecutionState) terminalFailure(
	err error,
	uncertainty pendingPublicationUncertainty,
) PendingPublicationStep {
	failure := errors.Join(state.pendingFailure, err)
	switch uncertainty {
	case pendingPublicationNoUncertainty:
		return state.terminalStep(failedPendingCommit(failure, state.progress))
	case pendingPublicationUnclassifiedMutation, pendingPublicationMutationReconciliation:
		return state.terminalStep(unknownPendingMutation(failure))
	case pendingPublicationUnclassifiedSubmission, pendingPublicationSubmissionReconciliation:
		return state.terminalStep(unknownPendingSubmission(failure))
	default:
		return state.terminalStep(PublicationCommit{
			Disposition:  review.PublicationDispositionUnknown,
			PendingPhase: review.PendingPublicationIndeterminate,
			Detail: boundedOperationDetail(errors.Join(
				failure,
				errors.New("pending publication uncertainty state is invalid"),
			).Error()),
		})
	}
}

func (state *pendingPublicationExecutionState) reconciliationUncertainty() pendingPublicationUncertainty {
	switch state.reconciliation {
	case pendingPublicationNoReconciliation:
		return pendingPublicationNoUncertainty
	case pendingPublicationReconcileMutation:
		return pendingPublicationMutationReconciliation
	case pendingPublicationReconcileSubmission:
		return pendingPublicationSubmissionReconciliation
	default:
		return 0
	}
}

func acquisitionUncertainty(kind pendingPublicationAcquisitionKind) pendingPublicationUncertainty {
	switch kind {
	case pendingMutationResponse:
		return pendingPublicationUnclassifiedMutation
	case pendingMutationReconciliation:
		return pendingPublicationMutationReconciliation
	case pendingSubmissionResponse:
		return pendingPublicationUnclassifiedSubmission
	case pendingSubmissionReconciliation:
		return pendingPublicationSubmissionReconciliation
	default:
		return 0
	}
}

func standalonePendingPublicationTerminal(commit PublicationCommit) PendingPublicationStep {
	state := &pendingPublicationExecutionState{status: pendingPublicationExecutionOpen}
	cell := &pendingPublicationStepCell{
		owner: state, kind: PendingPublicationStepTerminal, commit: commit,
	}
	state.outstanding = cell
	return PendingPublicationStep{cell: cell}
}

// AcquirePendingPublication performs the next network-only step of a prepared
// pending-review publication. It returns either acquired CPU work or a terminal
// commit; a caller must consume that result before acquiring again.
func (service *Service) AcquirePendingPublication(
	ctx context.Context,
	execution PendingPublicationExecution,
	runCPU CPUAdmission,
) PendingPublicationStep {
	state := execution.state
	if state == nil || state.status != pendingPublicationExecutionOpen {
		return standalonePendingPublicationTerminal(failedPendingCommit(
			errors.New("prepared pending publication is unavailable"),
			pendingPublicationUncommitted,
		))
	}
	if state.outstanding != nil {
		return standalonePendingPublicationTerminal(unknownPendingMutation(
			errors.New("pending publication step is still outstanding"),
		))
	}
	if runCPU == nil {
		return state.terminalFailure(
			errors.New("pending publication CPU admission is unavailable"),
			state.reconciliationUncertainty(),
		)
	}
	if err := context.Cause(ctx); err != nil {
		return state.terminalFailure(err, state.reconciliationUncertainty())
	}
	switch state.reconciliation {
	case pendingPublicationReconcileMutation:
		fresh, err := service.acquirePendingReview(
			ctx, state.target, state.reviewID, state.databaseID, runCPU,
		)
		if err != nil {
			return state.terminalFailure(err, pendingPublicationMutationReconciliation)
		}
		return state.acquiredStep(
			pendingMutationReconciliation,
			nil,
			fresh,
			state.pendingFailure,
		)
	case pendingPublicationReconcileSubmission:
		response, err := service.acquirePendingSubmission(ctx, state.target, state.databaseID)
		if err != nil {
			return state.terminalFailure(err, pendingPublicationSubmissionReconciliation)
		}
		return state.acquiredStep(
			pendingSubmissionReconciliation,
			response,
			review.PendingReview{},
			state.pendingFailure,
		)
	case pendingPublicationNoReconciliation:
	default:
		return state.terminalFailure(
			errors.New("pending publication reconciliation state is invalid"),
			state.reconciliationUncertainty(),
		)
	}
	if state.nextBatch < len(state.batches) {
		batch := state.currentBatch()
		if batch.payload == "" || len(batch.effects) == 0 {
			return state.terminalFailure(
				errors.New("prepared pending mutation batch is unavailable"),
				pendingPublicationNoUncertainty,
			)
		}
		state.batchAttempt++
		command, runErr := service.runGraphQLMutation(ctx, batch.payload)
		if runErr != nil && githubGraphQLMutationFailedBeforeCommit(runErr) {
			return state.terminalFailure(runErr, pendingPublicationNoUncertainty)
		}
		if runErr != nil {
			state.pendingFailure = runErr
			state.reconciliation = pendingPublicationReconcileMutation
			return service.AcquirePendingPublication(ctx, execution, runCPU)
		}
		return state.acquiredStep(pendingMutationResponse, command.body, review.PendingReview{}, nil)
	}
	if state.submission.payload == "" {
		fresh, err := service.acquirePendingReview(
			ctx, state.target, state.reviewID, state.databaseID, runCPU,
		)
		commit := PublicationCommit{
			Disposition:  review.PublicationDispositionCommitted,
			RemoteReview: state.databaseID,
		}
		if err == nil {
			commit.Pending = new(fresh)
			commit.PendingRefresh = review.PendingRefreshAcquired
		} else {
			commit.PendingRefresh = review.PendingRefreshUnavailable
			commit.Detail = boundedOperationDetail(
				"draft synchronized; refresh pending review: " + err.Error(),
			)
		}
		return state.terminalStep(commit)
	}
	state.submissionAttempt++
	command, runErr := service.runGraphQLMutation(ctx, state.submission.payload)
	if runErr != nil && githubGraphQLMutationFailedBeforeCommit(runErr) {
		return state.terminalFailure(runErr, pendingPublicationNoUncertainty)
	}
	if runErr != nil {
		state.pendingFailure = runErr
		state.reconciliation = pendingPublicationReconcileSubmission
		return service.AcquirePendingPublication(ctx, execution, runCPU)
	}
	return state.acquiredStep(pendingSubmissionResponse, command.body, review.PendingReview{}, nil)
}

// Finish consumes acquired remote state and returns either an explicit
// continuation or terminal commit. It performs no network I/O.
func (step *PendingPublicationStep) Finish(ctx context.Context) PendingPublicationStep {
	cell, err := step.take(PendingPublicationStepAcquired)
	if err != nil {
		return standalonePendingPublicationTerminal(unknownPendingMutation(err))
	}
	state := cell.owner
	if err := context.Cause(ctx); err != nil {
		return state.terminalFailure(err, acquisitionUncertainty(cell.acquisition))
	}
	switch cell.acquisition {
	case pendingMutationResponse:
		batch := state.currentBatch()
		if err := validatePendingMutationResponse(ctx, cell.response, batch.aliases); err != nil {
			state.pendingFailure = err
			state.reconciliation = pendingPublicationReconcileMutation
			return state.continuationStep()
		}
		state.advanceBatch()
		state.refreshRetainedBytes()
		return state.continuationStep()
	case pendingMutationReconciliation:
		return finishPendingMutationReconciliation(ctx, state, cell.fresh)
	case pendingSubmissionResponse:
		if err := validatePendingMutationResponse(ctx, cell.response, state.submission.aliases); err != nil {
			state.pendingFailure = err
			state.reconciliation = pendingPublicationReconcileSubmission
			return state.continuationStep()
		}
		return state.terminalStep(committedPendingSubmissionID(state.databaseID))
	case pendingSubmissionReconciliation:
		submissionState, err := classifyPendingSubmissionResponse(
			ctx,
			cell.response,
			state.reviewID,
			state.databaseID,
			state.expectedBody,
			state.expectedState,
		)
		if err != nil {
			return state.terminalFailure(err, pendingPublicationSubmissionReconciliation)
		}
		switch submissionState {
		case pendingSubmissionCommitted:
			return state.terminalStep(committedPendingSubmissionID(state.databaseID))
		case pendingSubmissionAbsent:
			if state.submissionAttempt >= 2 {
				return state.terminalFailure(
					errors.New("pending review submission was confirmed absent after retry"),
					pendingPublicationSubmissionReconciliation,
				)
			}
			state.reconciliation = pendingPublicationNoReconciliation
			state.pendingFailure = nil
			return state.continuationStep()
		default:
			return state.terminalFailure(
				errors.New("pending review submission state is invalid"),
				pendingPublicationSubmissionReconciliation,
			)
		}
	default:
		return state.terminalFailure(
			errors.New("pending publication acquisition kind is invalid"),
			acquisitionUncertainty(cell.acquisition),
		)
	}
}

func finishPendingMutationReconciliation(
	ctx context.Context,
	state *pendingPublicationExecutionState,
	fresh review.PendingReview,
) PendingPublicationStep {
	batch := state.currentBatch()
	planned := make([]pendingEffect, len(batch.effects))
	for index := range batch.effects {
		planned[index] = batch.effects[index].pendingEffect
	}
	states, err := classifyPendingEffects(ctx, fresh, planned)
	if err != nil {
		return state.terminalFailure(err, pendingPublicationMutationReconciliation)
	}
	retry := make([]pendingWireEffect, 0, len(batch.effects))
	for index, effectState := range states {
		switch effectState {
		case pendingEffectCommitted:
			state.progress = pendingPublicationPartiallyCommitted
		case pendingEffectAbsent:
			retry = append(retry, batch.effects[index])
		case pendingEffectUnknown:
			return state.terminalFailure(
				errors.New("pending review mutation outcome is unknown"),
				pendingPublicationMutationReconciliation,
			)
		default:
			return state.terminalFailure(
				errors.New("pending review mutation state is invalid"),
				pendingPublicationMutationReconciliation,
			)
		}
	}
	state.reconciliation = pendingPublicationNoReconciliation
	state.pendingFailure = nil
	if len(retry) == 0 {
		state.advanceBatch()
		state.refreshRetainedBytes()
		return state.continuationStep()
	}
	if state.batchAttempt >= 2 {
		return state.terminalFailure(
			errors.New("pending review mutation was confirmed absent after retry"),
			pendingPublicationNoUncertainty,
		)
	}
	aliases, payload, err := encodePendingMutationBatch(retry)
	if err != nil {
		return state.terminalFailure(err, pendingPublicationNoUncertainty)
	}
	state.retry = pendingMutationBatch{effects: retry, aliases: aliases, payload: payload}
	state.refreshRetainedBytes()
	return state.continuationStep()
}

func failedPendingCommit(err error, progress pendingPublicationProgress) PublicationCommit {
	detail := "pending publication failed"
	if err != nil {
		detail = err.Error()
	}
	disposition := review.PublicationDispositionFailedBeforeCommit
	phase := review.PendingPublicationPhase(0)
	switch progress {
	case pendingPublicationUncommitted:
	case pendingPublicationPartiallyCommitted:
		disposition = review.PublicationDispositionUnknown
		phase = review.PendingPublicationSynchronizing
	default:
		disposition = review.PublicationDispositionUnknown
		phase = review.PendingPublicationIndeterminate
		detail = "pending publication progress is invalid: " + detail
	}
	return PublicationCommit{
		Disposition:  disposition,
		PendingPhase: phase,
		Detail:       boundedOperationDetail(detail),
	}
}

func unknownPendingMutation(err error) PublicationCommit {
	detail := "pending review mutation outcome is unknown"
	if err != nil {
		detail = err.Error()
	}
	return PublicationCommit{
		Disposition:  review.PublicationDispositionUnknown,
		PendingPhase: review.PendingPublicationSynchronizing,
		Detail:       boundedOperationDetail(detail),
	}
}
