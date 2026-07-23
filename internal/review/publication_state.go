package review

import "strings"

// PublicationStatus describes publication lifecycle state.
type PublicationStatus uint8

// PublicationIdle identifies a supported publication idle value.
const (
	PublicationIdle PublicationStatus = iota + 1
	PublicationChecking
	PublicationChecked
	PublicationPublishing
	PublicationCommitted
	PublicationFailedBeforeCommit
	PublicationUnknown
	PublicationReconciling
	PublicationConfirmedAbsent
)

// String returns the receiver's string value.
func (status PublicationStatus) String() string {
	switch status {
	case PublicationIdle:
		return "idle"
	case PublicationChecking:
		return "checking"
	case PublicationChecked:
		return "checked"
	case PublicationPublishing:
		return "publishing"
	case PublicationCommitted:
		return "committed"
	case PublicationFailedBeforeCommit:
		return "failed-before-commit"
	case PublicationUnknown:
		return "unknown"
	case PublicationReconciling:
		return "reconciling"
	case PublicationConfirmedAbsent:
		return "confirmed-absent"
	default:
		return invalidValue
	}
}

type publicationState struct {
	continuation   *pendingPublicationContinuation
	checked        *CheckedPublication
	marker         PublicationMarker
	detail         OperationDetail
	target         PublicationTarget
	operation      OperationID
	attempt        OperationID
	remoteReview   uint64
	included       uint32
	excluded       uint32
	status         PublicationStatus
	event          PublicationEvent
	acknowledged   bool
	pendingRefresh bool
}

// PendingPublicationPhase identifies the remote phase that may have committed.
type PendingPublicationPhase uint8

// PendingPublicationSynchronizing identifies synchronization uncertainty.
const (
	PendingPublicationSynchronizing PendingPublicationPhase = iota + 1
	PendingPublicationSubmitting
	PendingPublicationIndeterminate
)

func (phase PendingPublicationPhase) valid() bool {
	return phase >= PendingPublicationSynchronizing && phase <= PendingPublicationIndeterminate
}

type pendingPublicationContinuation struct {
	checked *CheckedPublication
	phase   PendingPublicationPhase
}

func newPendingPublicationContinuation(
	checked *CheckedPublication,
	phase PendingPublicationPhase,
) (*pendingPublicationContinuation, error) {
	if checked == nil || !checked.Pending() || !phase.valid() {
		return nil, ErrInvalidPublication
	}
	return &pendingPublicationContinuation{checked: checked, phase: phase}, nil
}

// PublicationSummary describes publication summary.
type PublicationSummary struct {
	Marker                 PublicationMarker
	Detail                 OperationDetail
	Target                 PublicationTarget
	Operation              OperationID
	RemoteReview           uint64
	Included               uint32
	Excluded               uint32
	Status                 PublicationStatus
	Event                  PublicationEvent
	StaleHead              bool
	Acknowledged           bool
	PendingRefreshRequired bool
}

// PublicationCheckRequest describes publication check request.
type PublicationCheckRequest struct {
	Options   PublicationOptions
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
}

// PublicationCheckResult describes publication check result.
type PublicationCheckResult struct {
	Checked   *CheckedPublication
	Detail    OperationDetail
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
}

// StartPublicationCheck performs the start publication check operation.
func (review Review) StartPublicationCheck(
	id OperationID,
	options PublicationOptions,
) (Review, PublicationCheckRequest, error) {
	if !options.valid() || review.draft != nil || review.activeMutation != 0 ||
		review.publication.pendingRefresh ||
		review.publication.status == PublicationChecking ||
		review.publication.status == PublicationPublishing ||
		review.publication.status == PublicationUnknown ||
		review.publication.status == PublicationReconciling {
		return review, PublicationCheckRequest{}, ErrInvalidPublication
	}
	next, err := review.BeginOperation(id, OperationPublication)
	if err != nil {
		return review, PublicationCheckRequest{}, err
	}
	next.publication = publicationState{
		status:    PublicationChecking,
		operation: id,
		event:     options.event,
	}
	return next, PublicationCheckRequest{
		Operation: id,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
		Options:   options,
	}, nil
}

// ApplyPublicationCheck performs the apply publication check operation.
func (review Review) ApplyPublicationCheck(result PublicationCheckResult) (Review, error) {
	if review.publication.status != PublicationChecking ||
		review.publication.operation != result.Operation ||
		review.workspace != result.Workspace ||
		review.snapshot.SourceID() != result.Source {
		return review, ErrInvalidPublication
	}
	next := review.clone()
	if result.Checked == nil {
		if result.Detail.IsEmpty() {
			return review, ErrInvalidPublication
		}
		next.publication = publicationState{
			status:    PublicationFailedBeforeCommit,
			operation: result.Operation,
			detail:    result.Detail,
			event:     review.publication.event,
		}
		return next, nil
	}
	if result.Checked.prepared.workspace != result.Workspace ||
		result.Checked.prepared.source != result.Source {
		return review, ErrInvalidPublication
	}
	next.publication = publicationState{
		status:    PublicationChecked,
		operation: result.Operation,
		checked:   result.Checked,
		target:    result.Checked.target,
		marker:    result.Checked.marker,
		event:     result.Checked.prepared.options.event,
		included:  result.Checked.prepared.included,
		excluded:  result.Checked.prepared.excluded,
	}
	return next, nil
}

// PublicationRequest describes publication request.
type PublicationRequest struct {
	Checked   *CheckedPublication
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
}

// StartPublication performs the start publication operation.
func (review Review) StartPublication(
	id OperationID,
) (Review, PublicationRequest, error) {
	if review.activeMutation != 0 || review.publication.status != PublicationChecked ||
		review.publication.checked == nil {
		return review, PublicationRequest{}, ErrInvalidPublication
	}
	next, err := review.BeginOperation(id, OperationPublication)
	if err != nil {
		return review, PublicationRequest{}, err
	}
	next.publication.status = PublicationPublishing
	next.publication.operation = id
	next.publication.attempt = id
	return next, PublicationRequest{
		Operation: id,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
		Checked:   review.publication.checked,
	}, nil
}

// PublicationDisposition represents publication disposition.
type PublicationDisposition uint8

// PublicationDispositionCommitted identifies a supported publication disposition committed value.
const (
	PublicationDispositionCommitted PublicationDisposition = iota + 1
	PublicationDispositionFailedBeforeCommit
	PublicationDispositionUnknown
	PublicationDispositionCancelledBeforeCommit
)

// PendingRefreshAcquisition describes whether a committed draft publication
// acquired the fresh pending-review projection needed for local refresh.
type PendingRefreshAcquisition uint8

// PendingRefreshNotRequested identifies publication outcomes that do not need
// a pending-review capture.
const (
	PendingRefreshNotRequested PendingRefreshAcquisition = iota
	PendingRefreshAcquired
	PendingRefreshUnavailable
)

// PublicationResult describes publication result.
type PublicationResult struct {
	Pending        *PendingReview
	Detail         OperationDetail
	Operation      OperationID
	Workspace      WorkspaceRevision
	Source         SourceID
	RemoteReview   uint64
	Disposition    PublicationDisposition
	PendingPhase   PendingPublicationPhase
	PendingRefresh PendingRefreshAcquisition
}

// ApplyPublication performs the apply publication operation.
func (review Review) ApplyPublication(result PublicationResult) (Review, error) {
	request := PublicationRequest{
		Checked:   review.publication.checked,
		Operation: review.publication.operation,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
	}
	prepared, err := PreparePublicationResult(
		request,
		review.PendingRebaseSource(),
		result,
	)
	if err != nil {
		return review, err
	}
	return review.ApplyPreparedPublication(prepared)
}

func validatePublicationResult(
	checked *CheckedPublication,
	result PublicationResult,
) error {
	return validatePublicationResultCancellable(nil, checked, result)
}

func validatePublicationResultCancellable(
	cancelled func() error,
	checked *CheckedPublication,
	result PublicationResult,
) error {
	if err := cancellationError(cancelled); err != nil {
		return err
	}
	if !validPublicationRefreshAcquisition(checked, result) {
		return ErrInvalidPublication
	}
	if result.Disposition == PublicationDispositionFailedBeforeCommit ||
		result.Disposition == PublicationDispositionCancelledBeforeCommit {
		if result.PendingPhase != 0 || result.RemoteReview != 0 || result.Pending != nil {
			return ErrInvalidPublication
		}
		return nil
	}
	if checked == nil {
		return ErrInvalidPublication
	}
	prepared := checked.prepared.pending
	if result.Disposition == PublicationDispositionUnknown {
		if result.RemoteReview != 0 || result.Pending != nil {
			return ErrInvalidPublication
		}
		if prepared == nil {
			if result.PendingPhase != 0 {
				return ErrInvalidPublication
			}
			return nil
		}
		if !result.PendingPhase.valid() ||
			result.PendingPhase == PendingPublicationSubmitting && !prepared.submit {
			return ErrInvalidPublication
		}
		return nil
	}
	if result.Disposition != PublicationDispositionCommitted ||
		result.PendingPhase != 0 || result.RemoteReview == 0 {
		return ErrInvalidPublication
	}
	if prepared == nil {
		if result.Pending != nil {
			matches, err := checked.CreatedDraftMatchesCancellable(cancelled, *result.Pending)
			if err != nil {
				return err
			}
			if !matches || result.Pending.databaseID.Uint64() != result.RemoteReview {
				return ErrInvalidPublication
			}
		}
		return nil
	}
	baseline := prepared.baseline
	if result.RemoteReview != baseline.DatabaseID().Uint64() {
		return ErrInvalidPublication
	}
	if prepared.submit {
		if result.Pending != nil {
			return ErrInvalidPublication
		}
		return nil
	}
	if result.Pending != nil && !pendingCaptureMatchesBaseline(*result.Pending, baseline) {
		return ErrInvalidPublication
	}
	return cancellationError(cancelled)
}

func validPublicationRefreshAcquisition(
	checked *CheckedPublication,
	result PublicationResult,
) bool {
	draftSynchronization := checked != nil &&
		checked.prepared.options.event == PublicationEventDraft &&
		(checked.prepared.pending == nil || !checked.prepared.pending.submit)
	if result.PendingRefresh == PendingRefreshAcquired {
		return result.Pending != nil && result.Disposition == PublicationDispositionCommitted &&
			draftSynchronization
	}
	if result.PendingRefresh == PendingRefreshUnavailable {
		return result.Pending == nil && result.Disposition == PublicationDispositionCommitted &&
			draftSynchronization
	}
	if result.PendingRefresh != PendingRefreshNotRequested || result.Pending != nil {
		return false
	}
	return result.Disposition != PublicationDispositionCommitted || !draftSynchronization
}

// ReconciliationRequest describes reconciliation request.
type ReconciliationRequest struct {
	checked   *CheckedPublication
	Marker    PublicationMarker
	Target    PublicationTarget
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	phase     PendingPublicationPhase
}

const reconciliationRequestMetadataBytes int64 = 512

// PendingContinuation returns the validated pending-publication continuation.
func (request ReconciliationRequest) PendingContinuation() (*CheckedPublication, PendingPublicationPhase, bool) {
	if request.checked == nil {
		return nil, 0, false
	}
	if !request.checked.Pending() || !request.phase.valid() ||
		request.Target != request.checked.Target() || request.Marker != request.checked.Marker() ||
		request.Workspace != request.checked.prepared.workspace ||
		request.Source != request.checked.prepared.source {
		return nil, 0, false
	}
	return request.checked, request.phase, true
}

// RetainedBytes returns the conservative queued request ownership.
func (request ReconciliationRequest) RetainedBytes() int64 {
	if checked, _, ok := request.PendingContinuation(); ok {
		return checked.RetainedBytes()
	}
	if request.checked != nil || request.phase != 0 || request.Operation == 0 ||
		request.Workspace == 0 || request.Source == 0 || request.Marker == "" ||
		request.Target.repository == "" {
		return 0
	}
	return reconciliationRequestMetadataBytes + int64(
		len(request.Marker)+len(request.Target.repository)+
			len(request.Target.base)+len(request.Target.head),
	)
}

// StartReconciliation performs the start reconciliation operation.
func (review Review) StartReconciliation(
	id OperationID,
) (Review, ReconciliationRequest, error) {
	if review.activeMutation != 0 || review.publication.status != PublicationUnknown ||
		review.publication.marker == "" ||
		review.publication.target.repository == "" {
		return review, ReconciliationRequest{}, ErrInvalidPublication
	}
	next, err := review.BeginOperation(id, OperationReconciliation)
	if err != nil {
		return review, ReconciliationRequest{}, err
	}
	next.publication.status = PublicationReconciling
	next.publication.operation = id
	request := ReconciliationRequest{
		Operation: id,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
		Target:    review.publication.target,
		Marker:    review.publication.marker,
	}
	if continuation := review.publication.continuation; continuation != nil {
		request.checked = continuation.checked
		request.phase = continuation.phase
		if _, _, ok := request.PendingContinuation(); !ok {
			return review, ReconciliationRequest{}, ErrInvalidPublication
		}
	}
	return next, request, nil
}

// ReconciliationDisposition represents reconciliation disposition.
type ReconciliationDisposition uint8

// ReconciliationCommitted identifies a supported reconciliation committed value.
const (
	ReconciliationCommitted ReconciliationDisposition = iota + 1
	ReconciliationNotCommitted
	ReconciliationStillUnknown
)

// ReconciliationResult describes reconciliation result.
type ReconciliationResult struct {
	Pending      *PendingReview
	Detail       OperationDetail
	Operation    OperationID
	Workspace    WorkspaceRevision
	Source       SourceID
	RemoteReview uint64
	Disposition  ReconciliationDisposition
}

// ApplyReconciliation performs the apply reconciliation operation.
func (review Review) ApplyReconciliation(result ReconciliationResult) (Review, error) {
	request := ReconciliationRequest{
		Marker:    review.publication.marker,
		Target:    review.publication.target,
		Operation: review.publication.operation,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
	}
	if continuation := review.publication.continuation; continuation != nil {
		request.checked = continuation.checked
		request.phase = continuation.phase
	}
	prepared, err := PrepareReconciliationResult(
		request,
		review.PendingRebaseSource(),
		result,
	)
	if err != nil {
		return review, err
	}
	return review.ApplyPreparedReconciliation(prepared)
}

func validateReconciliationResult(
	continuation *pendingPublicationContinuation,
	result ReconciliationResult,
) error {
	return validateReconciliationResultCancellable(nil, continuation, result)
}

func validateReconciliationResultCancellable(
	cancelled func() error,
	continuation *pendingPublicationContinuation,
	result ReconciliationResult,
) error {
	if err := cancellationError(cancelled); err != nil {
		return err
	}
	if continuation == nil {
		if result.Pending != nil || result.RemoteReview != 0 {
			return ErrInvalidPublication
		}
		return nil
	}
	if continuation.checked == nil {
		return ErrInvalidPublication
	}
	prepared := continuation.checked.prepared.pending
	if prepared == nil || !continuation.phase.valid() {
		return ErrInvalidPublication
	}
	if continuation.phase == PendingPublicationSubmitting && !prepared.submit {
		return ErrInvalidPublication
	}
	if result.Disposition == ReconciliationStillUnknown {
		if result.Pending != nil || result.RemoteReview != 0 {
			return ErrInvalidPublication
		}
		return nil
	}
	baseline := prepared.baseline
	if continuation.phase == PendingPublicationSubmitting {
		if result.Disposition != ReconciliationCommitted || result.Pending != nil ||
			result.RemoteReview != baseline.DatabaseID().Uint64() {
			return ErrInvalidPublication
		}
		return nil
	}
	if result.Pending == nil || !pendingCaptureMatchesBaseline(*result.Pending, baseline) {
		return ErrInvalidPublication
	}
	switch result.Disposition {
	case ReconciliationCommitted:
		matches, err := pendingPublicationEffectsMatchCancellable(
			cancelled,
			continuation.checked,
			*result.Pending,
			pendingEffectCommitted,
		)
		if err != nil {
			return err
		}
		if prepared.submit || result.RemoteReview != baseline.DatabaseID().Uint64() || !matches {
			return ErrInvalidPublication
		}
	case ReconciliationNotCommitted:
		matches, err := pendingPublicationEffectsMatchCancellable(
			cancelled,
			continuation.checked,
			*result.Pending,
			pendingEffectAbsent,
		)
		if err != nil {
			return err
		}
		if result.RemoteReview != 0 || !matches {
			return ErrInvalidPublication
		}
	default:
		return ErrInvalidPublication
	}
	return nil
}

type pendingEffectDisposition uint8

const (
	pendingEffectCommitted pendingEffectDisposition = iota + 1
	pendingEffectAbsent
	pendingEffectDivergent
)

type pendingTargetMarkerMatch struct {
	comment PendingComment
	count   int
}

func pendingCaptureMatchesBaseline(remote, baseline PendingReview) bool {
	return remote.nodeID == baseline.nodeID && remote.databaseID == baseline.databaseID &&
		remote.commit == baseline.commit
}

func pendingPublicationEffectsMatchCancellable(
	cancelled func() error,
	checked *CheckedPublication,
	remote PendingReview,
	want pendingEffectDisposition,
) (bool, error) {
	disposition, err := pendingPublicationEffectsDispositionCancellable(
		cancelled,
		checked,
		remote,
	)
	return disposition == want, err
}

func pendingPublicationEffectsDispositionCancellable(
	cancelled func() error,
	checked *CheckedPublication,
	remote PendingReview,
) (pendingEffectDisposition, error) {
	if err := cancellationError(cancelled); err != nil {
		return 0, err
	}
	if checked == nil {
		return pendingEffectDivergent, nil
	}
	prepared := checked.prepared.pending
	if prepared == nil || len(prepared.mutations) == 0 {
		return pendingEffectDivergent, nil
	}
	baseline := prepared.baseline
	comments := make(map[CommentNodeID]PendingComment, len(remote.comments))
	markers := make(map[string]pendingTargetMarkerMatch, len(remote.comments))
	for index, comment := range remote.comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return 0, err
			}
		}
		comments[comment.nodeID] = comment
		_, marker := splitPendingMarker(comment.body, draftCommentMarkerPrefix)
		if marker != "" {
			match := markers[marker]
			match.count++
			if match.count == 1 {
				match.comment = comment
			}
			markers[marker] = match
		}
	}
	consensus := pendingEffectDisposition(0)
	for index, mutation := range prepared.mutations {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return 0, err
			}
		}
		desired, err := pendingMutationRemoteBodyCancellable(
			cancelled,
			baseline.nodeID,
			checked.marker,
			mutation,
		)
		if err != nil {
			return 0, err
		}
		disposition := pendingEffectDivergent
		switch mutation.kind {
		case PendingMutationReviewBody:
			disposition = classifyPendingTextEffect(remote.body, desired, baseline.body)
		case PendingMutationUpdateComment:
			comment, exists := comments[mutation.nodeID]
			if exists {
				disposition = classifyPendingTextEffect(
					comment.body,
					desired,
					joinPendingMarker(mutation.baseline, mutation.marker),
				)
			}
		case PendingMutationDeleteComment:
			comment, exists := comments[mutation.nodeID]
			if !exists {
				disposition = pendingEffectCommitted
			} else if comment.body == joinPendingMarker(mutation.baseline, mutation.marker) {
				disposition = pendingEffectAbsent
			}
		case PendingMutationAddComment:
			marker, err := stablePendingCommentMarkerCancellable(
				cancelled,
				baseline.nodeID,
				mutation,
			)
			if err != nil {
				return 0, err
			}
			match := markers[marker]
			if match.count == 0 {
				disposition = pendingEffectAbsent
			} else if match.count == 1 && match.comment.body == desired &&
				match.comment.target.MatchesPendingAddition(mutation.target) {
				disposition = pendingEffectCommitted
			}
		default:
			return pendingEffectDivergent, nil
		}
		if disposition == pendingEffectDivergent {
			return pendingEffectDivergent, nil
		}
		if consensus == 0 {
			consensus = disposition
		} else if consensus != disposition {
			return pendingEffectDivergent, nil
		}
	}
	if err := cancellationError(cancelled); err != nil {
		return 0, err
	}
	if consensus == pendingEffectAbsent && strings.Contains(remote.body, checked.marker.String()) {
		return pendingEffectDivergent, nil
	}
	return consensus, nil
}

func classifyPendingTextEffect(
	actual string,
	committed string,
	absent string,
) pendingEffectDisposition {
	if actual == committed {
		return pendingEffectCommitted
	}
	if actual == absent {
		return pendingEffectAbsent
	}
	return pendingEffectDivergent
}

func pendingMutationRemoteBodyCancellable(
	cancelled func() error,
	reviewID ReviewNodeID,
	attempt PublicationMarker,
	mutation PendingMutation,
) (string, error) {
	marker := mutation.marker
	if mutation.kind == PendingMutationReviewBody {
		marker = attempt.String()
	} else if mutation.kind == PendingMutationAddComment ||
		mutation.kind == PendingMutationUpdateComment && marker == "" {
		var err error
		marker, err = stablePendingCommentMarkerCancellable(cancelled, reviewID, mutation)
		if err != nil {
			return "", err
		}
	}
	if err := cancellationError(cancelled); err != nil {
		return "", err
	}
	return joinPendingMarker(mutation.body, marker), nil
}

// CancelPublicationCheck returns the receiver's cancel publication check value.
func (review Review) CancelPublicationCheck() (Review, error) {
	if review.publication.status != PublicationChecked {
		return review, ErrInvalidPublication
	}
	next := review.clone()
	next.publication = publicationState{status: PublicationIdle}
	return next, nil
}

// AcknowledgePublication returns the receiver's acknowledge publication value.
func (review Review) AcknowledgePublication() (Review, error) {
	if review.publication.status == PublicationUnknown ||
		review.publication.status == PublicationReconciling ||
		review.publication.status == PublicationIdle {
		return review, ErrInvalidPublication
	}
	next := review.clone()
	next.publication.acknowledged = true
	return next, nil
}

// PublicationSummary returns the receiver's publication summary value.
func (review Review) PublicationSummary() PublicationSummary {
	summary := PublicationSummary{
		Status:                 review.publication.status,
		Operation:              review.publication.operation,
		Target:                 review.publication.target,
		Marker:                 review.publication.marker,
		RemoteReview:           review.publication.remoteReview,
		Detail:                 review.publication.detail,
		Acknowledged:           review.publication.acknowledged,
		Event:                  review.publication.event,
		Included:               review.publication.included,
		Excluded:               review.publication.excluded,
		PendingRefreshRequired: review.publication.pendingRefresh,
	}
	if review.publication.checked != nil {
		summary.StaleHead = review.publication.checked.staleHead
	}
	return summary
}

// UnknownPublicationOperation returns the receiver's unknown publication operation value.
func (review Review) UnknownPublicationOperation() (OperationID, bool) {
	return review.publication.attempt,
		review.publication.attempt != 0 &&
			(review.publication.status == PublicationUnknown ||
				review.publication.status == PublicationReconciling)
}

// PublicationPreflightBody returns the receiver's publication preflight body value.
func (review Review) PublicationPreflightBody() string {
	if review.publication.checked == nil {
		return ""
	}
	return review.publication.checked.prepared.summary
}

// PublicationPreflightPayload returns the receiver's publication preflight payload value.
func (review Review) PublicationPreflightPayload() string {
	if review.publication.checked == nil {
		return ""
	}
	return review.publication.checked.payload
}

// PublicationContinuesPendingReview reports whether the active publication has a pending plan.
func (review Review) PublicationContinuesPendingReview() bool {
	return review.publication.checked != nil && review.publication.checked.Pending() ||
		review.publication.continuation != nil
}

// PublicationDecisionAt performs the publication decision at operation.
func (review Review) PublicationDecisionAt(
	index int,
) (PublicationCommentDecision, bool) {
	if review.publication.checked == nil {
		return PublicationCommentDecision{}, false
	}
	return review.publication.checked.prepared.DecisionAt(index)
}
