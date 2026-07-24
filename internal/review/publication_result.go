package review

import (
	"errors"
	"fmt"
)

// ErrPendingRefreshRequired reports a known remote terminal outcome whose
// fresh pending-review projection could not be installed locally.
var ErrPendingRefreshRequired = errors.New("pending review refresh failed; reload required")

const (
	preparedPublicationResultMetadataBytes    int64 = 512
	preparedReconciliationResultMetadataBytes int64 = 512
	pendingReconciliationCommentScratchBytes  int64 = 1_024
	pendingReconciliationMutationScratchBytes int64 = 320
	pendingReconciliationFixedScratchBytes    int64 = 4_096
)

// PreparedPublicationResult is a validated terminal publication result. Raw
// pending-review ownership is replaced by an optional prepared rebase delta.
type PreparedPublicationResult struct {
	checked      *CheckedPublication
	continuation *pendingPublicationContinuation
	result       PublicationResult
	refresh      preparedPendingRefresh
	retained     int64
	valid        bool
}

// PreparePublicationResultFallbackCancellable preserves a validated terminal
// remote disposition when dynamic reservation cannot admit pending refresh
// work. It is O(1), drops raw Pending ownership, and performs no rebase.
func PreparePublicationResultFallbackCancellable(
	cancelled func() error,
	request PublicationRequest,
	source PendingRebaseSource,
	result PublicationResult,
) (PreparedPublicationResult, error) {
	if err := cancellationError(cancelled); err != nil {
		return PreparedPublicationResult{}, err
	}
	if request.Operation == 0 || request.Workspace == 0 ||
		request.Source == 0 || result.Operation != request.Operation ||
		result.Workspace != request.Workspace || result.Source != request.Source {
		return PreparedPublicationResult{}, ErrInvalidPublication
	}
	if validationErr := validatePublicationResultCancellable(
		cancelled,
		request.Checked,
		result,
	); validationErr != nil {
		return PreparedPublicationResult{}, validationErr
	}
	prepared := PreparedPublicationResult{
		checked: request.Checked,
		refresh: noPendingRefresh(),
		result:  result,
		valid:   true,
	}
	checkedRetained := int64(0)
	if request.Checked != nil {
		checkedRetained = request.Checked.RetainedBytes()
	}
	prepared.retained = checkedRetained + result.Detail.Bytes() +
		preparedPublicationResultMetadataBytes
	refresh, err := preparePendingRefreshFallback(
		source,
		request.Workspace,
		request.Source,
		result.PendingRefresh,
		result.Pending,
	)
	if err != nil {
		return PreparedPublicationResult{}, err
	}
	prepared.refresh = refresh
	if result.Pending != nil {
		prepared.result.Pending = nil
	}
	if result.Disposition == PublicationDispositionUnknown && request.Checked != nil &&
		request.Checked.Pending() {
		continuation, err := newPendingPublicationContinuation(request.Checked, result.PendingPhase)
		if err != nil {
			return PreparedPublicationResult{}, err
		}
		prepared.continuation = continuation
	}
	if err := cancellationError(cancelled); err != nil {
		return PreparedPublicationResult{}, err
	}
	return prepared, nil
}

// PrepareUnknownPublicationResult constructs the safe terminal state used when
// local result preparation fails after the remote commit boundary is crossed.
func PrepareUnknownPublicationResult(
	request PublicationRequest,
	detail OperationDetail,
) (PreparedPublicationResult, error) {
	phase := PendingPublicationPhase(0)
	if request.Checked != nil && request.Checked.Pending() {
		phase = PendingPublicationIndeterminate
	}
	return PreparePublicationResultFallbackCancellable(
		nil,
		request,
		PendingRebaseSource{},
		PublicationResult{
			Operation:    request.Operation,
			Workspace:    request.Workspace,
			Source:       request.Source,
			Disposition:  PublicationDispositionUnknown,
			PendingPhase: phase,
			Detail:       detail,
		},
	)
}

// RetainedBytes returns complete queued prepared-result ownership.
func (prepared PreparedPublicationResult) RetainedBytes() int64 {
	if !prepared.valid || !prepared.refresh.valid() {
		return 0
	}
	return prepared.retained
}

// Disposition returns the validated remote publication disposition.
func (prepared PreparedPublicationResult) Disposition() PublicationDisposition {
	if !prepared.valid {
		return 0
	}
	return prepared.result.Disposition
}

// Detail returns the validated terminal detail.
func (prepared PreparedPublicationResult) Detail() OperationDetail {
	if !prepared.valid {
		return EmptyOperationDetail()
	}
	return prepared.result.Detail
}

// RemoteReview returns the committed remote review database identity, when known.
func (prepared PreparedPublicationResult) RemoteReview() uint64 {
	if !prepared.valid {
		return 0
	}
	return prepared.result.RemoteReview
}

// PendingPhase returns the uncertain pending-publication phase, when present.
func (prepared PreparedPublicationResult) PendingPhase() PendingPublicationPhase {
	if !prepared.valid {
		return 0
	}
	return prepared.result.PendingPhase
}

// LocalRefreshError reports whether the terminal remote result was preserved
// without installing its fresh pending-review projection.
func (prepared PreparedPublicationResult) LocalRefreshError() error {
	if !prepared.valid || !prepared.refresh.reloadRequired() {
		return nil
	}
	return ErrPendingRefreshRequired
}

// PublicationResultConstructionBytes returns a conservative bound for worker
// validation and optional pending-rebase preparation.
func (request PublicationRequest) PublicationResultConstructionBytes(
	source PendingRebaseSource,
	result PublicationResult,
) int64 {
	construction, err := request.publicationResultConstructionBytesCancellable(nil, source, result)
	if err != nil {
		panic("nil publication-result cancellation returned an error")
	}
	return construction
}

func (request PublicationRequest) publicationResultConstructionBytesCancellable(
	cancelled func() error,
	source PendingRebaseSource,
	result PublicationResult,
) (int64, error) {
	if request.Operation == 0 || request.Workspace == 0 ||
		request.Source == 0 || result.Operation != request.Operation ||
		result.Workspace != request.Workspace || result.Source != request.Source {
		return 0, nil
	}
	checkedRetained := int64(0)
	if request.Checked != nil {
		checkedRetained = request.Checked.RetainedBytes()
	}
	retained := checkedRetained + result.Detail.Bytes() +
		preparedPublicationResultMetadataBytes
	if result.Pending == nil {
		return retained, nil
	}
	rebase, err := source.constructionBytesCancellable(cancelled, *result.Pending)
	if err != nil {
		return 0, err
	}
	if rebase == 0 || source.Workspace() != request.Workspace ||
		source.Source() != request.Source {
		return 0, nil
	}
	return retained + rebase, nil
}

// PreparePublicationResult validates raw acquisition output and performs all
// optional pending-review rebase work.
func PreparePublicationResult(
	request PublicationRequest,
	source PendingRebaseSource,
	result PublicationResult,
) (PreparedPublicationResult, error) {
	return PreparePublicationResultCancellable(nil, request, source, result)
}

// PreparePublicationResultCancellable validates raw acquisition output and
// performs all optional pending-review rebase work cancellably.
func PreparePublicationResultCancellable(
	cancelled func() error,
	request PublicationRequest,
	source PendingRebaseSource,
	result PublicationResult,
) (PreparedPublicationResult, error) {
	if err := cancellationError(cancelled); err != nil {
		return PreparedPublicationResult{}, err
	}
	construction, err := request.publicationResultConstructionBytesCancellable(
		cancelled,
		source,
		result,
	)
	if err != nil {
		return PreparedPublicationResult{}, err
	}
	if construction == 0 {
		return PreparedPublicationResult{}, ErrInvalidPublication
	}
	if validationErr := validatePublicationResultCancellable(
		cancelled,
		request.Checked,
		result,
	); validationErr != nil {
		return PreparedPublicationResult{}, validationErr
	}
	prepared := PreparedPublicationResult{
		checked: request.Checked,
		refresh: noPendingRefresh(),
		result:  result,
		valid:   true,
	}
	refresh, err := preparePendingRefreshCancellable(
		cancelled,
		source,
		request.Workspace,
		request.Source,
		result.PendingRefresh,
		result.Pending,
	)
	if err != nil {
		return PreparedPublicationResult{}, err
	}
	prepared.refresh = refresh
	if result.Pending != nil {
		prepared.result.Pending = nil
	}
	if result.Disposition == PublicationDispositionUnknown && request.Checked != nil &&
		request.Checked.Pending() {
		continuation, err := newPendingPublicationContinuation(request.Checked, result.PendingPhase)
		if err != nil {
			return PreparedPublicationResult{}, err
		}
		prepared.continuation = continuation
	}
	checkedRetained := int64(0)
	if request.Checked != nil {
		checkedRetained = request.Checked.RetainedBytes()
	}
	prepared.retained = checkedRetained + result.Detail.Bytes() +
		prepared.refresh.retainedBytes() + preparedPublicationResultMetadataBytes
	if err := cancellationError(cancelled); err != nil {
		return PreparedPublicationResult{}, err
	}
	if prepared.RetainedBytes() > construction {
		return PreparedPublicationResult{}, ErrInvalidPublication
	}
	return prepared, nil
}

// ApplyPreparedPublication installs a worker-prepared terminal result in O(1).
func (review Review) ApplyPreparedPublication(
	prepared PreparedPublicationResult,
) (Review, error) {
	result := prepared.result
	if !prepared.valid || !prepared.refresh.valid() || result.Pending != nil ||
		review.publication.status != PublicationPublishing ||
		review.publication.operation != result.Operation ||
		!prepared.matchesChecked(review.publication.checked) ||
		review.workspace != result.Workspace ||
		review.snapshot.SourceID() != result.Source {
		return review, ErrInvalidPublication
	}
	next, err := prepared.refresh.install(review)
	if err != nil {
		return review, fmt.Errorf("%w: apply committed pending review", ErrInvalidPublication)
	}
	next.publication.operation = result.Operation
	next.publication.attempt = result.Operation
	next.publication.detail = result.Detail
	next.publication.remoteReview = result.RemoteReview
	next.publication.pendingRefresh = prepared.refresh.reloadRequired()
	switch result.Disposition {
	case PublicationDispositionCommitted:
		next.publication.checked = nil
		next.publication.continuation = nil
		next.publication.status = PublicationCommitted
	case PublicationDispositionFailedBeforeCommit, PublicationDispositionCancelledBeforeCommit:
		next.publication.checked = nil
		next.publication.continuation = nil
		next.publication.status = PublicationFailedBeforeCommit
	case PublicationDispositionUnknown:
		next.publication.checked = nil
		next.publication.continuation = prepared.continuation
		next.publication.status = PublicationUnknown
	default:
		return review, ErrInvalidPublication
	}
	return next, nil
}

func (prepared PreparedPublicationResult) matchesChecked(checked *CheckedPublication) bool {
	return checked == prepared.checked
}

// PreparedReconciliationResult is a validated terminal reconciliation result.
// Raw pending-review ownership is replaced by an optional prepared rebase delta.
type PreparedReconciliationResult struct {
	checked  *CheckedPublication
	result   ReconciliationResult
	refresh  preparedPendingRefresh
	retained int64
	phase    PendingPublicationPhase
	valid    bool
}

// PrepareReconciliationResultFallbackCancellable preserves scalar terminal
// results when dynamic reservation cannot admit pending validation. A pending
// capture cannot be trusted without proportional effect matching, so the
// fallback conservatively preserves the continuation as still unknown. It is
// O(1), drops raw Pending ownership, and performs no rebase.
func PrepareReconciliationResultFallbackCancellable(
	cancelled func() error,
	request ReconciliationRequest,
	source PendingRebaseSource,
	result ReconciliationResult,
) (PreparedReconciliationResult, error) {
	if err := cancellationError(cancelled); err != nil {
		return PreparedReconciliationResult{}, err
	}
	if request.Operation == 0 || request.Workspace == 0 ||
		request.Source == 0 || result.Operation != request.Operation ||
		result.Workspace != request.Workspace || result.Source != request.Source {
		return PreparedReconciliationResult{}, ErrInvalidPublication
	}
	var continuation *pendingPublicationContinuation
	checked, phase, hasContinuation := request.PendingContinuation()
	if hasContinuation {
		continuation = &pendingPublicationContinuation{checked: checked, phase: phase}
	} else if request.checked != nil || request.phase != 0 {
		return PreparedReconciliationResult{}, ErrInvalidPublication
	}
	fallback := result
	switch {
	case result.Pending == nil:
		if err := validateReconciliationResultCancellable(
			cancelled,
			continuation,
			result,
		); err != nil {
			return PreparedReconciliationResult{}, err
		}
	case !hasContinuation ||
		!pendingRebaseSourceMatchesResult(
			source,
			request.Workspace,
			request.Source,
			*result.Pending,
		) || !validReconciliationFallback(checked, phase, result):
		return PreparedReconciliationResult{}, ErrInvalidPublication
	default:
		fallback.Pending = nil
		fallback.RemoteReview = 0
		fallback.Disposition = ReconciliationStillUnknown
	}
	prepared := PreparedReconciliationResult{
		checked: request.checked,
		refresh: noPendingRefresh(),
		result:  fallback,
		phase:   request.phase,
		valid:   true,
	}
	prepared.retained = request.RetainedBytes() + result.Detail.Bytes() +
		preparedReconciliationResultMetadataBytes
	refresh, err := preparePendingRefreshFallback(
		source,
		request.Workspace,
		request.Source,
		pendingRefreshAcquisition(fallback.Pending),
		fallback.Pending,
	)
	if err != nil {
		return PreparedReconciliationResult{}, err
	}
	prepared.refresh = refresh
	if err := cancellationError(cancelled); err != nil {
		return PreparedReconciliationResult{}, err
	}
	return prepared, nil
}

// PrepareStillUnknownReconciliationResult constructs the safe terminal state
// used when local preparation cannot refine a reconciliation result.
func PrepareStillUnknownReconciliationResult(
	request ReconciliationRequest,
	detail OperationDetail,
) (PreparedReconciliationResult, error) {
	return PrepareReconciliationResultFallbackCancellable(
		nil,
		request,
		PendingRebaseSource{},
		ReconciliationResult{
			Operation:   request.Operation,
			Workspace:   request.Workspace,
			Source:      request.Source,
			Disposition: ReconciliationStillUnknown,
			Detail:      detail,
		},
	)
}

// RetainedBytes returns complete queued prepared-result ownership.
func (prepared PreparedReconciliationResult) RetainedBytes() int64 {
	if !prepared.valid || !prepared.refresh.valid() {
		return 0
	}
	return prepared.retained
}

// Disposition returns the validated reconciliation disposition.
func (prepared PreparedReconciliationResult) Disposition() ReconciliationDisposition {
	if !prepared.valid {
		return 0
	}
	return prepared.result.Disposition
}

// Detail returns the validated terminal detail.
func (prepared PreparedReconciliationResult) Detail() OperationDetail {
	if !prepared.valid {
		return EmptyOperationDetail()
	}
	return prepared.result.Detail
}

// RemoteReview returns the committed remote review database identity, when known.
func (prepared PreparedReconciliationResult) RemoteReview() uint64 {
	if !prepared.valid {
		return 0
	}
	return prepared.result.RemoteReview
}

// LocalRefreshError reports whether the terminal remote result was preserved
// without installing its fresh pending-review projection.
func (prepared PreparedReconciliationResult) LocalRefreshError() error {
	if !prepared.valid || !prepared.refresh.reloadRequired() {
		return nil
	}
	return ErrPendingRefreshRequired
}

// ReconciliationResultConstructionBytes returns a conservative bound for
// worker validation and optional pending-rebase preparation.
func (request ReconciliationRequest) ReconciliationResultConstructionBytes(
	source PendingRebaseSource,
	result ReconciliationResult,
) int64 {
	construction, err := request.reconciliationResultConstructionBytesCancellable(
		nil,
		source,
		result,
	)
	if err != nil {
		panic("nil reconciliation-result cancellation returned an error")
	}
	return construction
}

// PendingReconciliationCaptureConstructionBytes returns a conservative bound
// for one-pass pending-effect classification and prepared-rebase construction.
func (request ReconciliationRequest) PendingReconciliationCaptureConstructionBytes(
	source PendingRebaseSource,
	fresh PendingReview,
) int64 {
	construction, err := request.pendingReconciliationCaptureConstructionBytesCancellable(
		nil,
		source,
		fresh,
	)
	if err != nil {
		panic("nil pending-reconciliation cancellation returned an error")
	}
	return construction
}

func (request ReconciliationRequest) pendingReconciliationCaptureConstructionBytesCancellable(
	cancelled func() error,
	source PendingRebaseSource,
	fresh PendingReview,
) (int64, error) {
	checked, phase, ok := request.PendingContinuation()
	if !ok || phase == PendingPublicationSubmitting ||
		!pendingCaptureMatchesBaseline(fresh, checked.prepared.pending.baseline) ||
		source.Workspace() != request.Workspace || source.Source() != request.Source {
		return 0, nil
	}
	rebase, err := source.constructionBytesCancellable(cancelled, fresh)
	if err != nil {
		return 0, err
	}
	if rebase == 0 {
		return 0, nil
	}
	return request.RetainedBytes() + rebase +
		int64(fresh.CommentCount())*pendingReconciliationCommentScratchBytes +
		int64(len(checked.prepared.pending.mutations))*pendingReconciliationMutationScratchBytes +
		MaxOperationDetail + preparedReconciliationResultMetadataBytes +
		pendingReconciliationFixedScratchBytes, nil
}

// PreparePendingReconciliationCaptureCancellable validates and classifies one
// fresh pending-review capture, then prepares its local rebase in the same
// cancellable CPU phase. The returned terminal result applies in O(1).
func PreparePendingReconciliationCaptureCancellable(
	cancelled func() error,
	request ReconciliationRequest,
	source PendingRebaseSource,
	fresh PendingReview,
) (PreparedReconciliationResult, error) {
	if err := cancellationError(cancelled); err != nil {
		return PreparedReconciliationResult{}, err
	}
	construction, err := request.pendingReconciliationCaptureConstructionBytesCancellable(
		cancelled,
		source,
		fresh,
	)
	if err != nil {
		return PreparedReconciliationResult{}, err
	}
	if construction == 0 {
		return PreparedReconciliationResult{}, ErrInvalidPublication
	}
	result, err := ClassifyPendingReconciliationCaptureCancellable(
		cancelled,
		request,
		fresh,
	)
	if err != nil {
		return PreparedReconciliationResult{}, err
	}
	refresh := noPendingRefresh()
	if result.Pending != nil {
		refresh, err = preparePendingRefreshCancellable(
			cancelled,
			source,
			request.Workspace,
			request.Source,
			PendingRefreshAcquired,
			result.Pending,
		)
		if err != nil {
			return PreparedReconciliationResult{}, err
		}
		result.Pending = nil
	}
	prepared := PreparedReconciliationResult{
		checked: request.checked,
		refresh: refresh,
		phase:   request.phase,
		result:  result,
		valid:   true,
	}
	prepared.retained = request.RetainedBytes() + result.Detail.Bytes() +
		refresh.retainedBytes() + preparedReconciliationResultMetadataBytes
	if err := cancellationError(cancelled); err != nil {
		return PreparedReconciliationResult{}, err
	}
	if prepared.RetainedBytes() > construction {
		return PreparedReconciliationResult{}, ErrInvalidPublication
	}
	return prepared, nil
}

// ClassifyPendingReconciliationCaptureCancellable validates and classifies a
// fresh pending-review capture into the canonical raw reconciliation result.
// Terminal results transfer the capture through Pending; unknown results do
// not retain it.
func ClassifyPendingReconciliationCaptureCancellable(
	cancelled func() error,
	request ReconciliationRequest,
	fresh PendingReview,
) (ReconciliationResult, error) {
	if err := cancellationError(cancelled); err != nil {
		return ReconciliationResult{}, err
	}
	checked, phase, ok := request.PendingContinuation()
	if !ok || phase == PendingPublicationSubmitting ||
		!pendingCaptureMatchesBaseline(fresh, checked.prepared.pending.baseline) {
		return ReconciliationResult{}, ErrInvalidPublication
	}
	effects, err := pendingPublicationEffectsDispositionCancellable(
		cancelled,
		checked,
		fresh,
	)
	if err != nil {
		return ReconciliationResult{}, err
	}
	result := ReconciliationResult{
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: ReconciliationStillUnknown,
		Detail: mustOperationDetail(
			"pending publication is partially committed or divergent",
		),
	}
	switch effects {
	case pendingEffectCommitted:
		if !checked.prepared.pending.submit {
			result.Disposition = ReconciliationCommitted
			result.RemoteReview = checked.prepared.pending.baseline.databaseID.Uint64()
			result.Detail = mustOperationDetail("pending review synchronization confirmed")
			result.Pending = new(fresh)
		}
	case pendingEffectAbsent:
		result.Disposition = ReconciliationNotCommitted
		result.Detail = mustOperationDetail("pending publication attempt confirmed absent")
		result.Pending = new(fresh)
	case pendingEffectDivergent:
	default:
		return ReconciliationResult{}, ErrInvalidPublication
	}
	if err := cancellationError(cancelled); err != nil {
		return ReconciliationResult{}, err
	}
	return result, nil
}

func (request ReconciliationRequest) reconciliationResultConstructionBytesCancellable(
	cancelled func() error,
	source PendingRebaseSource,
	result ReconciliationResult,
) (int64, error) {
	if request.Operation == 0 || request.Workspace == 0 || request.Source == 0 ||
		result.Operation != request.Operation || result.Workspace != request.Workspace ||
		result.Source != request.Source {
		return 0, nil
	}
	if request.checked != nil {
		if _, _, ok := request.PendingContinuation(); !ok {
			return 0, nil
		}
	} else if request.phase != 0 {
		return 0, nil
	}
	retained := request.RetainedBytes() + result.Detail.Bytes() +
		preparedReconciliationResultMetadataBytes
	if result.Pending == nil {
		return retained, nil
	}
	rebase, err := source.constructionBytesCancellable(cancelled, *result.Pending)
	if err != nil {
		return 0, err
	}
	if rebase == 0 || source.Workspace() != request.Workspace ||
		source.Source() != request.Source {
		return 0, nil
	}
	return retained + rebase, nil
}

// PrepareReconciliationResult validates raw acquisition output and performs
// all optional pending-review rebase work.
func PrepareReconciliationResult(
	request ReconciliationRequest,
	source PendingRebaseSource,
	result ReconciliationResult,
) (PreparedReconciliationResult, error) {
	return PrepareReconciliationResultCancellable(nil, request, source, result)
}

// PrepareReconciliationResultCancellable validates raw acquisition output and
// performs all optional pending-review rebase work cancellably.
func PrepareReconciliationResultCancellable(
	cancelled func() error,
	request ReconciliationRequest,
	source PendingRebaseSource,
	result ReconciliationResult,
) (PreparedReconciliationResult, error) {
	if err := cancellationError(cancelled); err != nil {
		return PreparedReconciliationResult{}, err
	}
	construction, err := request.reconciliationResultConstructionBytesCancellable(
		cancelled,
		source,
		result,
	)
	if err != nil {
		return PreparedReconciliationResult{}, err
	}
	if construction == 0 {
		return PreparedReconciliationResult{}, ErrInvalidPublication
	}
	var continuation *pendingPublicationContinuation
	if checked, phase, ok := request.PendingContinuation(); ok {
		continuation = &pendingPublicationContinuation{checked: checked, phase: phase}
	}
	if validationErr := validateReconciliationResultCancellable(
		cancelled,
		continuation,
		result,
	); validationErr != nil {
		return PreparedReconciliationResult{}, validationErr
	}
	prepared := PreparedReconciliationResult{
		checked: request.checked,
		refresh: noPendingRefresh(),
		phase:   request.phase,
		result:  result,
		valid:   true,
	}
	refresh, err := preparePendingRefreshCancellable(
		cancelled,
		source,
		request.Workspace,
		request.Source,
		pendingRefreshAcquisition(result.Pending),
		result.Pending,
	)
	if err != nil {
		return PreparedReconciliationResult{}, err
	}
	prepared.refresh = refresh
	if result.Pending != nil {
		prepared.result.Pending = nil
	}
	prepared.retained = request.RetainedBytes() + result.Detail.Bytes() +
		prepared.refresh.retainedBytes() + preparedReconciliationResultMetadataBytes
	if err := cancellationError(cancelled); err != nil {
		return PreparedReconciliationResult{}, err
	}
	if prepared.RetainedBytes() > construction {
		return PreparedReconciliationResult{}, ErrInvalidPublication
	}
	return prepared, nil
}

// ApplyPreparedReconciliation installs a worker-prepared terminal result in O(1).
func (review Review) ApplyPreparedReconciliation(
	prepared PreparedReconciliationResult,
) (Review, error) {
	result := prepared.result
	if !prepared.valid || !prepared.refresh.valid() || result.Pending != nil ||
		review.publication.status != PublicationReconciling ||
		review.publication.operation != result.Operation ||
		review.workspace != result.Workspace ||
		review.snapshot.SourceID() != result.Source ||
		!prepared.matchesContinuation(review.publication.continuation) {
		return review, ErrInvalidPublication
	}
	next, err := prepared.refresh.install(review)
	if err != nil {
		return review, fmt.Errorf("%w: apply reconciled pending review", ErrInvalidPublication)
	}
	next.publication.operation = result.Operation
	next.publication.detail = result.Detail
	if result.RemoteReview != 0 {
		next.publication.remoteReview = result.RemoteReview
	}
	next.publication.pendingRefresh = prepared.refresh.reloadRequired()
	switch result.Disposition {
	case ReconciliationCommitted:
		next.publication.continuation = nil
		next.publication.status = PublicationCommitted
	case ReconciliationNotCommitted:
		next.publication.continuation = nil
		next.publication.status = PublicationConfirmedAbsent
	case ReconciliationStillUnknown:
		next.publication.status = PublicationUnknown
	default:
		return review, ErrInvalidPublication
	}
	return next, nil
}

func (prepared PreparedReconciliationResult) matchesContinuation(
	continuation *pendingPublicationContinuation,
) bool {
	if prepared.checked == nil {
		return continuation == nil && prepared.phase == 0
	}
	return continuation != nil && continuation.checked == prepared.checked &&
		continuation.phase == prepared.phase
}

func pendingRebaseSourceMatchesResult(
	source PendingRebaseSource,
	workspace WorkspaceRevision,
	sourceID SourceID,
	pending PendingReview,
) bool {
	if !pendingRebaseSourceMatchesRequest(source, workspace, sourceID) {
		return false
	}
	if source.installDraft {
		return source.state.pending == nil
	}
	return source.state.pending != nil && pending.nodeID == source.state.pending.nodeID &&
		pending.databaseID == source.state.pending.databaseID
}

func pendingRebaseSourceMatchesRequest(
	source PendingRebaseSource,
	workspace WorkspaceRevision,
	sourceID SourceID,
) bool {
	return source.Ready() && source.Workspace() == workspace && source.Source() == sourceID
}

func pendingRefreshAcquisition(pending *PendingReview) PendingRefreshAcquisition {
	if pending == nil {
		return PendingRefreshNotRequested
	}
	return PendingRefreshAcquired
}

func validReconciliationFallback(
	checked *CheckedPublication,
	phase PendingPublicationPhase,
	result ReconciliationResult,
) bool {
	if checked == nil || !phase.valid() || result.Pending == nil {
		return false
	}
	prepared := checked.prepared.pending
	if prepared == nil || phase == PendingPublicationSubmitting ||
		!pendingCaptureMatchesBaseline(*result.Pending, prepared.baseline) {
		return false
	}
	switch result.Disposition {
	case ReconciliationCommitted:
		return !prepared.submit &&
			result.RemoteReview == prepared.baseline.DatabaseID().Uint64()
	case ReconciliationNotCommitted:
		return result.RemoteReview == 0
	default:
		return false
	}
}
