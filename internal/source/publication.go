package source

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jacoelho/rvw/internal/change"
)

// PublicationCheckAcquisition is the validated network phase of preflight.
type PublicationCheckAcquisition struct {
	target    change.PublicationTarget
	fresh     change.PendingReview
	staleHead bool
	hasFresh  bool
	valid     bool
}

// PublicationCheckMode states whether preflight must reacquire an existing
// pending review before local preparation can be finalized.
type PublicationCheckMode uint8

const (
	// PublicationCheckNew checks a publication with no captured pending change.
	PublicationCheckNew PublicationCheckMode = iota + 1
	// PublicationCheckPendingContinuation reacquires the captured pending change.
	PublicationCheckPendingContinuation
)

// FreshPending returns the immutable pending capture requiring CPU reconciliation.
func (acquisition PublicationCheckAcquisition) FreshPending() (change.PendingReview, bool) {
	return acquisition.fresh, acquisition.valid && acquisition.hasFresh
}

// AcquirePublicationCheck performs only network acquisition and validation.
func (service *Service) AcquirePublicationCheck(
	ctx context.Context,
	target PublicationTarget,
	options change.PublicationOptions,
	mode PublicationCheckMode,
	runCPU CPUAdmission,
) (PublicationCheckAcquisition, error) {
	if mode != PublicationCheckNew && mode != PublicationCheckPendingContinuation {
		return PublicationCheckAcquisition{}, sourceFailure(
			OperationCheckPublication,
			errors.New("publication check mode is invalid"),
		)
	}
	if runCPU == nil {
		return PublicationCheckAcquisition{}, sourceFailure(
			OperationCheckPublication,
			errors.New("publication check CPU admission is unavailable"),
		)
	}
	if target.source == 0 {
		return PublicationCheckAcquisition{}, sourceFailure(
			OperationCheckPublication,
			errors.New("publication source revision is stale"),
		)
	}
	current, err := service.pullRequestMetadata(ctx, target.repository, target.number, runCPU)
	if err != nil {
		return PublicationCheckAcquisition{}, sourceFailure(OperationCheckPublication, err)
	}
	currentDetails, err := validatePullRequestPayload(
		target.repository,
		target.number,
		current,
	)
	if err != nil {
		return PublicationCheckAcquisition{}, sourceFailure(OperationCheckPublication, err)
	}
	if currentDetails.Repository != target.repository ||
		currentDetails.Number != target.number {
		return PublicationCheckAcquisition{}, sourceFailure(
			OperationCheckPublication,
			errors.New("GitHub publication target identity changed"),
		)
	}
	if stateErr := pullRequestPublicationStateError(currentDetails.State); stateErr != nil {
		return PublicationCheckAcquisition{}, sourceFailure(OperationCheckPublication, stateErr)
	}
	staleHead := currentDetails.BaseOID != target.base ||
		currentDetails.HeadOID != target.head
	if staleHead &&
		!options.Policies().Has(change.PublicationAllowStaleHead) {
		return PublicationCheckAcquisition{}, sourceFailure(
			OperationCheckPublication,
			errors.New("GitHub pull request revision changed before publication"),
		)
	}
	acquisition := PublicationCheckAcquisition{
		staleHead: staleHead,
		valid:     true,
	}
	checkedTarget, err := change.NewPublicationTarget(change.PublicationTargetDescriptor{
		Repository:  target.repository.String(),
		PullRequest: target.number.Uint64(),
		Base:        target.base,
		Head:        target.head,
	})
	if err != nil {
		return PublicationCheckAcquisition{}, sourceFailure(OperationCheckPublication, err)
	}
	acquisition.target = checkedTarget
	if mode == PublicationCheckPendingContinuation {
		fresh, status, detail := service.loadPendingReview(ctx, currentDetails, runCPU)
		if cancellationErr := context.Cause(ctx); cancellationErr != nil {
			return PublicationCheckAcquisition{}, sourceFailure(OperationCheckPublication, cancellationErr)
		}
		if status != change.PendingReviewReady {
			message := "GitHub pending review is " + status.String()
			if !detail.IsEmpty() {
				message += ": " + detail.String()
			}
			return PublicationCheckAcquisition{}, sourceFailure(OperationCheckPublication, errors.New(message))
		}
		acquisition.fresh = fresh
		acquisition.hasFresh = true
	}
	return acquisition, nil
}

func pullRequestPublicationStateError(state GitHubState) error {
	switch state {
	case GitHubOpen:
		return nil
	case GitHubClosed:
		return errors.New("pull request is closed")
	case GitHubMerged:
		return errors.New("pull request is already merged")
	default:
		return errors.New("pull request has an invalid GitHub state")
	}
}

// FinishPublicationCheck binds CPU-reconciled preparation to the acquired target.
func FinishPublicationCheck(
	acquisition PublicationCheckAcquisition,
	prepared change.PreparedPublication,
) (*change.CheckedPublication, error) {
	return FinishPublicationCheckCancellable(nil, acquisition, prepared)
}

// FinishPublicationCheckCancellable binds CPU-reconciled preparation to the
// acquired target while keeping payload encoding cancellable.
func FinishPublicationCheckCancellable(
	cancelled func() error,
	acquisition PublicationCheckAcquisition,
	prepared change.PreparedPublication,
) (*change.CheckedPublication, error) {
	if !acquisition.valid || prepared.Source() == 0 {
		return nil, sourceFailure(OperationCheckPublication, errors.New("publication preflight is invalid"))
	}
	marker, err := newPublicationMarker()
	if err != nil {
		return nil, sourceFailure(OperationCheckPublication, err)
	}
	checked, err := change.TakeCheckedPublicationCancellable(cancelled, change.CheckedPublicationDescriptor{
		Target:    acquisition.target,
		Marker:    marker,
		StaleHead: acquisition.staleHead,
	}, prepared)
	if err != nil {
		return nil, sourceFailure(OperationCheckPublication, err)
	}
	return checked, nil
}

// CheckPublication performs the compatibility preflight path synchronously.
func (service *Service) CheckPublication(
	ctx context.Context,
	target PublicationTarget,
	prepared change.PreparedPublication,
) (*change.CheckedPublication, error) {
	mode := PublicationCheckNew
	if prepared.ContinuesPendingPublication() {
		mode = PublicationCheckPendingContinuation
	}
	acquisition, err := service.AcquirePublicationCheck(
		ctx,
		target,
		prepared.Options(),
		mode,
		directCPUAdmission,
	)
	if err != nil {
		return nil, err
	}
	if fresh, ok := acquisition.FreshPending(); ok {
		prepared, err = prepared.ReconcilePendingPublication(fresh)
		if err != nil {
			return nil, sourceFailure(OperationCheckPublication, err)
		}
	}
	return FinishPublicationCheck(acquisition, prepared)
}

func newPublicationMarker() (change.PublicationMarker, error) {
	var attempt [16]byte
	if _, err := rand.Read(attempt[:]); err != nil {
		return "", fmt.Errorf("generate publication identity: %w", err)
	}
	return change.NewPublicationMarkerFromAttempt(attempt), nil
}

// PublicationCommit represents publication commit.
type PublicationCommit struct {
	Pending        *change.PendingReview
	Detail         change.OperationDetail
	RemoteReview   uint64
	Disposition    change.PublicationDisposition
	PendingPhase   change.PendingPublicationPhase
	PendingRefresh change.PendingRefreshAcquisition
}

// Publish performs the publish operation.
func (service *Service) Publish(
	ctx context.Context,
	checked *change.CheckedPublication,
) PublicationCommit {
	if checked == nil || checked.PayloadSize() == 0 {
		return PublicationCommit{
			Disposition: change.PublicationDispositionFailedBeforeCommit,
			Detail:      boundedOperationDetail("checked publication is unavailable"),
		}
	}
	if checked.Pending() {
		execution, err := PreparePendingPublicationExecution(ctx, checked)
		if err != nil {
			return PublicationCommit{
				Disposition: change.PublicationDispositionFailedBeforeCommit,
				Detail:      boundedOperationDetail(err.Error()),
			}
		}
		defer execution.Close()
		step := service.AcquirePendingPublication(ctx, execution, directCPUAdmission)
		for {
			switch step.Kind() {
			case PendingPublicationStepAcquired:
				step = step.Finish(ctx)
			case PendingPublicationStepContinue:
				if err := step.Advance(); err != nil {
					return unknownPendingMutation(err)
				}
				step = service.AcquirePendingPublication(ctx, execution, directCPUAdmission)
			case PendingPublicationStepTerminal:
				commit, err := step.TakeCommit()
				if err != nil {
					return unknownPendingMutation(err)
				}
				return commit
			default:
				return unknownPendingMutation(errors.New("pending publication step is invalid"))
			}
		}
	}
	target := checked.Target()
	endpoint := "repos/" + target.Repository() + "/pulls/" +
		strconv.FormatUint(target.PullRequest(), 10) + "/reviews"
	result, err := service.github.mutation(
		ctx, http.MethodPost, "/"+endpoint, []byte(checked.Payload()), 1<<20,
	)
	if err != nil {
		disposition := change.PublicationDispositionUnknown
		if githubMutationFailedBeforeCommit(err) {
			disposition = change.PublicationDispositionFailedBeforeCommit
		}
		return PublicationCommit{
			Disposition: disposition,
			Detail:      boundedOperationDetail(err.Error()),
		}
	}
	if result.status != http.StatusOK {
		return PublicationCommit{
			Disposition: change.PublicationDispositionUnknown,
			Detail: boundedOperationDetail(
				"GitHub publication returned an undocumented success status",
			),
		}
	}
	reviewID, err := decodePublicationReviewID(ctx, result.body)
	if err != nil {
		return PublicationCommit{
			Disposition: change.PublicationDispositionUnknown,
			Detail:      boundedOperationDetail("decode GitHub publication response: " + err.Error()),
		}
	}
	commit := PublicationCommit{
		Disposition:  change.PublicationDispositionCommitted,
		RemoteReview: reviewID,
	}
	if checked.Prepared().Options().Event() != change.PublicationEventDraft {
		return commit
	}
	pending, detail, ok := service.acquireCreatedDraft(
		ctx,
		checked,
		reviewID,
	)
	if !ok {
		commit.PendingRefresh = change.PendingRefreshUnavailable
		commit.Detail = detail
		return commit
	}
	commit.Pending = new(pending)
	commit.PendingRefresh = change.PendingRefreshAcquired
	return commit
}

func (service *Service) acquireCreatedDraft(
	ctx context.Context,
	checked *change.CheckedPublication,
	reviewID uint64,
) (change.PendingReview, change.OperationDetail, bool) {
	if checked == nil || reviewID == 0 {
		return change.PendingReview{}, boundedOperationDetail(
			"GitHub draft was created, but its checked identity is unavailable",
		), false
	}
	target := checked.Target()
	repository, err := NewOwnerRepository(target.Repository())
	if err != nil {
		return change.PendingReview{}, boundedOperationDetail(
			"GitHub draft was created, but its repository identity is invalid",
		), false
	}
	payload, err := service.pullRequestMetadata(
		ctx,
		repository,
		PullRequestNumber(target.PullRequest()),
		directCPUAdmission,
	)
	if err != nil {
		return change.PendingReview{}, boundedOperationDetail(
			"GitHub draft was created, but its pending state could not be loaded: " + err.Error(),
		), false
	}
	details, err := validatePullRequestPayload(
		repository,
		PullRequestNumber(target.PullRequest()),
		payload,
	)
	if err != nil || details.BaseOID != target.Base() || details.HeadOID != target.Head() {
		return change.PendingReview{}, boundedOperationDetail(
			"GitHub draft was created, but the pull request changed before it could be loaded",
		), false
	}
	pending, status, detail := service.loadPendingReview(ctx, details, directCPUAdmission)
	if status != change.PendingReviewReady || pending.DatabaseID().Uint64() != reviewID {
		message := "GitHub draft was created, but its pending state is " + status.String()
		if status == change.PendingReviewReady {
			message = "GitHub draft was created, but GitHub returned a different pending review"
		} else if !detail.IsEmpty() {
			message += ": " + detail.String()
		}
		return change.PendingReview{}, boundedOperationDetail(message), false
	}
	matches, matchErr := checked.CreatedDraftMatchesCancellable(
		func() error { return context.Cause(ctx) },
		pending,
	)
	if matchErr != nil || !matches {
		message := "GitHub draft was created, but its pending state does not match the published comments"
		if matchErr != nil {
			message += ": " + matchErr.Error()
		}
		return change.PendingReview{}, boundedOperationDetail(message), false
	}
	return pending, change.EmptyOperationDetail(), true
}

type publicationReviewResponseCollector struct {
	scope      *jsonDecodeScope
	path       jsonPathTracker
	duplicates jsonDuplicateGuard
	id         uint64
	root       bool
	hasID      bool
}

func decodePublicationReviewID(ctx context.Context, encoded []byte) (uint64, error) {
	ledger, err := newSourceLedger(int64(cap(encoded)))
	if err != nil {
		return 0, err
	}
	metrics, err := inspectJSONWithCPU(
		ctx,
		bytes.NewReader(encoded),
		maxGitHubJSONTokenBytes,
		directCPUAdmission,
		ledger,
	)
	if err != nil {
		return 0, err
	}
	defer metrics.release(ledger)
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return 0, err
	}
	if err := scope.reserve(jsonDecodedCollectorBytes); err != nil {
		scope.Close()
		return 0, err
	}
	collector := publicationReviewResponseCollector{scope: scope}
	defer collector.close()
	if err := walkJSONFrames(
		ctx,
		bytes.NewReader(encoded),
		directCPUAdmission,
		ledger,
		metrics,
		collector.consume,
	); err != nil {
		return 0, err
	}
	if !collector.root || !collector.hasID || collector.id == 0 || collector.path.depth != 0 {
		return 0, errors.New("GitHub publication response has no review identity")
	}
	return collector.id, nil
}

func (collector *publicationReviewResponseCollector) consume(frame jsonFrame) error {
	switch frame.Kind {
	case jsonObjectKey:
		if err := collector.path.keyFrame(collector.scope, frame); err != nil {
			return err
		}
		return collector.duplicates.key(&collector.path)
	case jsonObjectStart:
		if err := collector.path.start(frame.Kind, 0); err != nil {
			return err
		}
		collector.duplicates.startObject(collector.path.depth)
		if collector.path.depth == 1 {
			collector.root = true
		}
		if collector.path.depth == 2 && collector.path.stack[1].name == "id" {
			return errors.New("GitHub publication review identity is not a scalar")
		}
		return nil
	case jsonArrayStart:
		if err := collector.path.start(frame.Kind, frame.Count); err != nil {
			return err
		}
		if collector.path.depth == 1 ||
			collector.path.depth == 2 && collector.path.stack[1].name == "id" {
			return errors.New("GitHub publication response has an invalid structure")
		}
		return nil
	case jsonObjectEnd:
		collector.duplicates.endObject(collector.path.depth)
		return collector.path.end(frame.Kind)
	case jsonArrayEnd:
		return collector.path.end(frame.Kind)
	default:
		return collector.path.scalar(func(value jsonPathValue) error {
			if collector.path.depth != 1 || value.name != "id" {
				return nil
			}
			id, err := jsonUint64Value(frame)
			if err != nil {
				return err
			}
			collector.id = id
			collector.hasID = true
			return nil
		})
	}
}

func (collector *publicationReviewResponseCollector) close() {
	if collector == nil {
		return
	}
	collector.path.close()
	collector.scope.Close()
	collector.scope = nil
}

// ReconcilePublication performs the reconcile publication operation.
func (service *Service) ReconcilePublication(
	ctx context.Context,
	request change.ReconciliationRequest,
) change.ReconciliationResult {
	if _, _, ok := request.PendingContinuation(); ok {
		acquisition, err := service.AcquirePendingPublicationReconciliation(
			ctx,
			request,
			directCPUAdmission,
		)
		if err != nil {
			return failedReconciliation(request, err)
		}
		defer acquisition.Close()
		return acquisition.Finish(ctx)
	}
	acquisition, err := service.AcquirePublicationReconciliation(ctx, request)
	if err != nil {
		return failedReconciliation(request, err)
	}
	return acquisition.Finish(ctx, directCPUAdmission)
}

// PublicationReconciliationAcquisition owns one bounded GitHub history spool
// awaiting cancellable CPU validation.
type PublicationReconciliationAcquisition struct {
	spool   *githubSpool
	request change.ReconciliationRequest
}

// AcquirePublicationReconciliation performs only the non-pending network
// acquisition phase.
func (service *Service) AcquirePublicationReconciliation(
	ctx context.Context,
	request change.ReconciliationRequest,
) (*PublicationReconciliationAcquisition, error) {
	if _, _, pending := request.PendingContinuation(); pending || request.Operation == 0 ||
		request.Workspace == 0 || request.Source == 0 || request.Marker == "" {
		return nil, errors.New("publication reconciliation request is invalid")
	}
	target := request.Target
	endpoint := "/repos/" + target.Repository() + "/pulls/" +
		strconv.FormatUint(target.PullRequest(), 10) + "/reviews"
	spool, err := service.restSpool(ctx, endpoint, change.MaxPublicationBytes)
	if err != nil {
		return nil, err
	}
	return &PublicationReconciliationAcquisition{spool: spool, request: request}, nil
}

// Close releases an unfinished reconciliation acquisition.
func (acquisition *PublicationReconciliationAcquisition) Close() error {
	if acquisition == nil || acquisition.spool == nil {
		return nil
	}
	err := acquisition.spool.Close()
	acquisition.spool = nil
	return err
}

func failedReconciliation(
	request change.ReconciliationRequest,
	err error,
) change.ReconciliationResult {
	return change.ReconciliationResult{
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: change.ReconciliationStillUnknown,
		Detail:      boundedOperationDetail(err.Error()),
	}
}

func boundedOperationDetail(value string) change.OperationDetail {
	return change.NewBoundedOperationDetail(value)
}
