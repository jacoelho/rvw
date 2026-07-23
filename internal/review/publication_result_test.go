package review

import (
	"errors"
	"testing"
	"unsafe"
)

func TestPreparedPublicationResultOwnsRebaseAndAppliesInConstantTime(t *testing.T) {
	publishing, checked, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
	remote := committedPendingCapture(t, checked)
	raw := PublicationResult{
		Pending:        new(remote),
		Operation:      request.Operation,
		Workspace:      request.Workspace,
		Source:         request.Source,
		RemoteReview:   remote.DatabaseID().Uint64(),
		Disposition:    PublicationDispositionCommitted,
		PendingRefresh: PendingRefreshAcquired,
	}
	source := publishing.PendingRebaseSource()
	construction := request.PublicationResultConstructionBytes(source, raw)
	prepared, err := PreparePublicationResult(request, source, raw)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.result.Pending != nil || prepared.refresh.mode != pendingRefreshInstall {
		t.Fatal("prepared publication retained the raw pending capture")
	}
	if prepared.RetainedBytes() == 0 || prepared.RetainedBytes() > construction {
		t.Fatalf(
			"prepared retained = %d, construction = %d",
			prepared.RetainedBytes(),
			construction,
		)
	}
	if prepared.Disposition() != PublicationDispositionCommitted ||
		prepared.RemoteReview() != raw.RemoteReview ||
		prepared.PendingPhase() != 0 || !prepared.Detail().IsEmpty() {
		t.Fatalf("prepared publication getters returned the wrong terminal result")
	}
	if preparedPublicationResultMetadataBytes <
		int64(unsafe.Sizeof(PreparedPublicationResult{})) {
		t.Fatalf(
			"prepared publication metadata = %d, layout = %d",
			preparedPublicationResultMetadataBytes,
			unsafe.Sizeof(PreparedPublicationResult{}),
		)
	}

	var next Review
	var applyErr error
	if allocations := testing.AllocsPerRun(100, func() {
		next, applyErr = publishing.ApplyPreparedPublication(prepared)
	}); allocations != 0 {
		t.Fatalf("ApplyPreparedPublication allocations = %f, want 0", allocations)
	}
	if applyErr != nil {
		t.Fatal(applyErr)
	}
	if summary := next.PublicationSummary(); summary.Status != PublicationCommitted || summary.RemoteReview != raw.RemoteReview {
		t.Fatalf("publication summary = %+v", summary)
	}
	if next.revision != publishing.revision+1 {
		t.Fatalf(
			"comment revision = %d, want %d",
			next.revision,
			publishing.revision+1,
		)
	}
}

func TestPreparedReconciliationResultValidatesEffectsAndAppliesInConstantTime(t *testing.T) {
	publishing, checked, publicationRequest := pendingPublicationAtPublishing(
		t,
		PublicationEventDraft,
	)
	unknownRaw := PublicationResult{
		Operation:    publicationRequest.Operation,
		Workspace:    publicationRequest.Workspace,
		Source:       publicationRequest.Source,
		Disposition:  PublicationDispositionUnknown,
		PendingPhase: PendingPublicationSynchronizing,
	}
	unknownPrepared, err := PreparePublicationResult(
		publicationRequest,
		publishing.PendingRebaseSource(),
		unknownRaw,
	)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := publishing.ApplyPreparedPublication(unknownPrepared)
	if err != nil {
		t.Fatal(err)
	}
	reconciliationID, _ := NewOperationID(3)
	reconciling, request, err := unknown.StartReconciliation(reconciliationID)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := checked.Prepared().PendingPublication()
	remote := plan.Baseline()
	raw := ReconciliationResult{
		Pending:     new(remote),
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: ReconciliationNotCommitted,
	}
	source := reconciling.PendingRebaseSource()
	construction := request.ReconciliationResultConstructionBytes(source, raw)
	prepared, err := PrepareReconciliationResult(request, source, raw)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.result.Pending != nil || prepared.refresh.mode != pendingRefreshInstall {
		t.Fatal("prepared reconciliation retained the raw pending capture")
	}
	if prepared.RetainedBytes() == 0 || prepared.RetainedBytes() > construction {
		t.Fatalf(
			"prepared retained = %d, construction = %d",
			prepared.RetainedBytes(),
			construction,
		)
	}
	if prepared.Disposition() != ReconciliationNotCommitted ||
		prepared.RemoteReview() != 0 || !prepared.Detail().IsEmpty() {
		t.Fatal("prepared reconciliation getters returned the wrong terminal result")
	}
	if preparedReconciliationResultMetadataBytes <
		int64(unsafe.Sizeof(PreparedReconciliationResult{})) {
		t.Fatalf(
			"prepared reconciliation metadata = %d, layout = %d",
			preparedReconciliationResultMetadataBytes,
			unsafe.Sizeof(PreparedReconciliationResult{}),
		)
	}

	var next Review
	var applyErr error
	if allocations := testing.AllocsPerRun(100, func() {
		next, applyErr = reconciling.ApplyPreparedReconciliation(prepared)
	}); allocations != 0 {
		t.Fatalf("ApplyPreparedReconciliation allocations = %f, want 0", allocations)
	}
	if applyErr != nil {
		t.Fatal(applyErr)
	}
	if summary := next.PublicationSummary(); summary.Status != PublicationConfirmedAbsent {
		t.Fatalf("reconciliation summary = %+v", summary)
	}
}

func TestPreparePendingReconciliationCaptureClassifiesAndPreparesOnce(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	reconciling, request, checked := pendingReconciliationCaptureFixture(t, current)
	plan, _ := checked.Prepared().PendingPublication()
	view, _ := checked.Prepared().BorrowPendingPublication()
	baseline := plan.Baseline()
	mutation, ok := view.MutationAt(0)
	if !ok || mutation.Kind() != PendingMutationReviewBody {
		t.Fatal("fixture has no review-body mutation")
	}
	committed := baseline
	committed.body = mutation.RemoteBody(baseline.NodeID(), checked.Marker())
	divergent := baseline
	divergent.body = "external divergence"

	tests := []struct {
		name       string
		fresh      PendingReview
		want       ReconciliationDisposition
		wantStatus PublicationStatus
		wantDetail string
	}{
		{
			name: "committed", fresh: committed, want: ReconciliationCommitted,
			wantStatus: PublicationCommitted,
			wantDetail: "pending review synchronization confirmed",
		},
		{
			name: "absent", fresh: baseline, want: ReconciliationNotCommitted,
			wantStatus: PublicationConfirmedAbsent,
			wantDetail: "pending publication attempt confirmed absent",
		},
		{
			name: "divergent", fresh: divergent, want: ReconciliationStillUnknown,
			wantStatus: PublicationUnknown,
			wantDetail: "pending publication is partially committed or divergent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := reconciling.PendingRebaseSource()
			construction := request.PendingReconciliationCaptureConstructionBytes(
				source,
				test.fresh,
			)
			prepared, err := PreparePendingReconciliationCaptureCancellable(
				nil,
				request,
				source,
				test.fresh,
			)
			if err != nil {
				t.Fatal(err)
			}
			if prepared.Disposition() != test.want ||
				prepared.Detail().String() != test.wantDetail ||
				prepared.result.Pending != nil || prepared.RetainedBytes() == 0 ||
				prepared.RetainedBytes() > construction {
				t.Fatalf(
					"prepared = disposition %d detail %q pending %v retained %d construction %d",
					prepared.Disposition(), prepared.Detail().String(),
					prepared.result.Pending, prepared.RetainedBytes(), construction,
				)
			}
			if test.want == ReconciliationCommitted &&
				prepared.RemoteReview() != baseline.DatabaseID().Uint64() {
				t.Fatalf("committed remote review = %d", prepared.RemoteReview())
			}
			var next Review
			var applyErr error
			if allocations := testing.AllocsPerRun(100, func() {
				next, applyErr = reconciling.ApplyPreparedReconciliation(prepared)
			}); allocations != 0 {
				t.Fatalf("prepared capture apply allocations = %f, want 0", allocations)
			}
			if applyErr != nil {
				t.Fatal(applyErr)
			}
			if next.PublicationSummary().Status != test.wantStatus {
				t.Fatalf("applied status = %s, want %s", next.PublicationSummary().Status, test.wantStatus)
			}
		})
	}
}

func TestPreparePendingReconciliationCaptureKeepsPartialUnknown(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	inline, _ := current.CommentAt(1)
	current = editTestComment(t, current, inline.ID(), "updated inline")
	reconciling, request, checked := pendingReconciliationCaptureFixture(t, current)
	plan, _ := checked.Prepared().PendingPublication()
	view, _ := checked.Prepared().BorrowPendingPublication()
	if view.MutationCount() < 2 {
		t.Fatal("partial fixture needs at least two effects")
	}
	fresh := plan.Baseline()
	for index := range view.MutationCount() {
		mutation, _ := view.MutationAt(index)
		if mutation.Kind() == PendingMutationReviewBody {
			fresh.body = mutation.RemoteBody(fresh.NodeID(), checked.Marker())
			break
		}
	}
	prepared, err := PreparePendingReconciliationCaptureCancellable(
		nil,
		request,
		reconciling.PendingRebaseSource(),
		fresh,
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Disposition() != ReconciliationStillUnknown ||
		prepared.refresh.mode != pendingRefreshNone || prepared.result.Pending != nil {
		t.Fatal("partial capture was accepted as terminal")
	}
}

func TestPreparePendingReconciliationCapturePropagatesCancellation(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	reconciling, request, checked := pendingReconciliationCaptureFixture(t, current)
	plan, _ := checked.Prepared().PendingPublication()
	cause := errors.New("cancel one-pass pending reconciliation")
	_, err = PreparePendingReconciliationCaptureCancellable(
		func() error { return cause },
		request,
		reconciling.PendingRebaseSource(),
		plan.Baseline(),
	)
	if !errors.Is(err, cause) {
		t.Fatalf("capture preparation cancellation = %v, want %v", err, cause)
	}
}

func pendingReconciliationCaptureFixture(
	t *testing.T,
	current Review,
) (Review, ReconciliationRequest, *CheckedPublication) {
	t.Helper()
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	checking, check, err := current.StartPublicationCheck(1, options)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := NewObjectID("1111111111111111111111111111111111111111")
	head, _ := NewObjectID("2222222222222222222222222222222222222222")
	target, _ := NewPublicationTarget(PublicationTargetDescriptor{
		Repository: "owner/repository", PullRequest: 7, Base: base, Head: head,
	})
	marker, _ := NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	checked, err := TakeCheckedPublication(
		CheckedPublicationDescriptor{Target: target, Marker: marker},
		prepared,
	)
	if err != nil {
		t.Fatal(err)
	}
	checkedReview, err := checking.ApplyPublicationCheck(PublicationCheckResult{
		Operation: check.Operation, Workspace: check.Workspace, Source: check.Source,
		Checked: checked,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishing, publication, err := checkedReview.StartPublication(2)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := publishing.ApplyPublication(PublicationResult{
		Operation: publication.Operation, Workspace: publication.Workspace,
		Source: publication.Source, Disposition: PublicationDispositionUnknown,
		PendingPhase: PendingPublicationSynchronizing,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciling, request, err := unknown.StartReconciliation(3)
	if err != nil {
		t.Fatal(err)
	}
	return reconciling, request, checked
}

func TestPrepareReconciliationResultCancellableInterruptsEffectValidation(t *testing.T) {
	publishing, checked, publicationRequest := pendingPublicationAtPublishing(
		t,
		PublicationEventDraft,
	)
	unknown, err := publishing.ApplyPublication(PublicationResult{
		Operation:    publicationRequest.Operation,
		Workspace:    publicationRequest.Workspace,
		Source:       publicationRequest.Source,
		Disposition:  PublicationDispositionUnknown,
		PendingPhase: PendingPublicationSynchronizing,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciliationID, _ := NewOperationID(3)
	reconciling, request, err := unknown.StartReconciliation(reconciliationID)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := checked.Prepared().PendingPublication()
	remote := plan.Baseline()
	raw := ReconciliationResult{
		Pending:     new(remote),
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: ReconciliationNotCommitted,
	}
	cause := errors.New("cancel effect validation")
	checks := 0
	_, err = PrepareReconciliationResultCancellable(func() error {
		checks++
		if checks == 7 {
			return cause
		}
		return nil
	}, request, reconciling.PendingRebaseSource(), raw)
	if !errors.Is(err, cause) {
		t.Fatalf("preparation error = %v, want %v", err, cause)
	}
	if checks != 7 {
		t.Fatalf("cancellation checks = %d, want 7", checks)
	}
}

func TestPreparePublicationResultAcceptsOnlyExplicitPreCommitLocalFailure(t *testing.T) {
	current, err := NewReview(testReviewRequest(testSnapshot(t, 1, "one.go")))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := NewOperationID(1)
	request := PublicationRequest{
		Operation: id,
		Workspace: current.workspace,
		Source:    current.snapshot.SourceID(),
	}
	detail, _ := NewOperationDetail("local publication failed")
	raw := PublicationResult{
		Detail:      detail,
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: PublicationDispositionFailedBeforeCommit,
	}
	prepared, err := PreparePublicationResult(request, PendingRebaseSource{}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Disposition() != raw.Disposition || prepared.Detail() != detail {
		t.Fatal("prepared local failure erased its explicit raw disposition")
	}

	committed := raw
	committed.Disposition = PublicationDispositionCommitted
	committed.RemoteReview = 42
	if _, err := PreparePublicationResult(
		request,
		PendingRebaseSource{},
		committed,
	); !errors.Is(err, ErrInvalidPublication) {
		t.Fatalf("unbacked committed preparation error = %v", err)
	}
}

func TestPreparedPublicationPreservesCommittedWhenLocalRefreshFails(t *testing.T) {
	publishing, checked, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
	remote := pendingCaptureWithExtraComment(t, committedPendingCapture(t, checked))
	raw := PublicationResult{
		Pending:        new(remote),
		Operation:      request.Operation,
		Workspace:      request.Workspace,
		Source:         request.Source,
		RemoteReview:   remote.DatabaseID().Uint64(),
		Disposition:    PublicationDispositionCommitted,
		PendingRefresh: PendingRefreshAcquired,
	}
	source := publishing.PendingRebaseSource()
	source.state.nextComment = MaxComments
	prepared, err := PreparePublicationResult(request, source, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(prepared.LocalRefreshError(), ErrPendingRefreshRequired) ||
		prepared.Disposition() != PublicationDispositionCommitted ||
		prepared.refresh.mode != pendingRefreshReloadRequired ||
		prepared.result.Pending != nil {
		t.Fatalf(
			"prepared fallback = disposition %d refresh %v rebase %t pending %v",
			prepared.Disposition(),
			prepared.LocalRefreshError(),
			prepared.refresh.mode == pendingRefreshInstall,
			prepared.result.Pending,
		)
	}
	oldPending := publishing.snapshot.pendingReview
	oldRevision := publishing.revision
	var next Review
	var applyErr error
	if allocations := testing.AllocsPerRun(100, func() {
		next, applyErr = publishing.ApplyPreparedPublication(prepared)
	}); allocations != 0 {
		t.Fatalf("refresh-fallback apply allocations = %f, want 0", allocations)
	}
	if applyErr != nil {
		t.Fatal(applyErr)
	}
	if next.PublicationSummary().Status != PublicationCommitted ||
		!next.PublicationSummary().PendingRefreshRequired ||
		next.snapshot.pendingReview != oldPending ||
		next.revision != oldRevision {
		t.Fatalf(
			"fallback apply = status %s refresh %t pending changed %t revision %d",
			next.PublicationSummary().Status,
			next.PublicationSummary().PendingRefreshRequired,
			next.snapshot.pendingReview != oldPending,
			next.revision,
		)
	}
	stale := publishing
	stale.revision++
	if _, err := stale.ApplyPreparedPublication(prepared); !errors.Is(err, ErrInvalidPublication) {
		t.Fatalf("stale fallback apply error = %v", err)
	}
}

func TestPreparedReconciliationPreservesNotCommittedWhenLocalRefreshFails(t *testing.T) {
	publishing, checked, publicationRequest := pendingPublicationAtPublishing(
		t,
		PublicationEventDraft,
	)
	unknown, err := publishing.ApplyPublication(PublicationResult{
		Operation:    publicationRequest.Operation,
		Workspace:    publicationRequest.Workspace,
		Source:       publicationRequest.Source,
		Disposition:  PublicationDispositionUnknown,
		PendingPhase: PendingPublicationSynchronizing,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciliationID, _ := NewOperationID(3)
	reconciling, request, err := unknown.StartReconciliation(reconciliationID)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := checked.Prepared().PendingPublication()
	remote := pendingCaptureWithExtraComment(t, plan.Baseline())
	raw := ReconciliationResult{
		Pending:     new(remote),
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: ReconciliationNotCommitted,
	}
	source := reconciling.PendingRebaseSource()
	source.state.nextComment = MaxComments
	prepared, err := PrepareReconciliationResult(request, source, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(prepared.LocalRefreshError(), ErrPendingRefreshRequired) ||
		prepared.Disposition() != ReconciliationNotCommitted ||
		prepared.refresh.mode != pendingRefreshReloadRequired ||
		prepared.result.Pending != nil {
		t.Fatalf(
			"prepared fallback = disposition %d refresh %v rebase %t pending %v",
			prepared.Disposition(),
			prepared.LocalRefreshError(),
			prepared.refresh.mode == pendingRefreshInstall,
			prepared.result.Pending,
		)
	}
	oldPending := reconciling.snapshot.pendingReview
	oldRevision := reconciling.revision
	var next Review
	var applyErr error
	if allocations := testing.AllocsPerRun(100, func() {
		next, applyErr = reconciling.ApplyPreparedReconciliation(prepared)
	}); allocations != 0 {
		t.Fatalf("refresh-fallback apply allocations = %f, want 0", allocations)
	}
	if applyErr != nil {
		t.Fatal(applyErr)
	}
	if next.PublicationSummary().Status != PublicationConfirmedAbsent ||
		!next.PublicationSummary().PendingRefreshRequired ||
		next.snapshot.pendingReview != oldPending ||
		next.revision != oldRevision {
		t.Fatalf(
			"fallback apply = status %s refresh %t pending changed %t revision %d",
			next.PublicationSummary().Status,
			next.PublicationSummary().PendingRefreshRequired,
			next.snapshot.pendingReview != oldPending,
			next.revision,
		)
	}
}

func pendingCaptureWithExtraComment(t *testing.T, pending PendingReview) PendingReview {
	t.Helper()
	comments := pending.Comments(0, pending.CommentCount())
	extraNode, _ := NewCommentNodeID("extra_pending_node")
	extraDatabase, _ := NewCommentDatabaseID(pending.DatabaseID().Uint64() + 99)
	extra, err := NewPendingComment(PendingCommentDescriptor{
		NodeID:       extraNode,
		DatabaseID:   extraDatabase,
		Body:         "unrelated remote draft",
		Target:       comments[0].Target(),
		Capabilities: editablePendingCapabilities(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	comments = append(comments, extra)
	withExtra, err := NewPendingReview(PendingReviewDescriptor{
		NodeID:       pending.NodeID(),
		DatabaseID:   pending.DatabaseID(),
		Commit:       pending.Commit(),
		Body:         pending.Body(),
		Comments:     comments,
		ViewerUpdate: pending.ViewerCanUpdate(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return withExtra
}

func TestPublicationResultFallbackDropsPendingWithoutScanningOrRebasing(t *testing.T) {
	publishing, checked, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
	remote := committedPendingCapture(t, checked)
	raw := PublicationResult{
		Pending:        new(remote),
		Operation:      request.Operation,
		Workspace:      request.Workspace,
		Source:         request.Source,
		RemoteReview:   remote.DatabaseID().Uint64(),
		Disposition:    PublicationDispositionCommitted,
		PendingRefresh: PendingRefreshAcquired,
	}
	source := publishing.PendingRebaseSource()
	var prepared PreparedPublicationResult
	var prepareErr error
	if allocations := testing.AllocsPerRun(100, func() {
		prepared, prepareErr = PreparePublicationResultFallbackCancellable(
			nil,
			request,
			source,
			raw,
		)
	}); allocations != 0 {
		t.Fatalf("publication fallback allocations = %f, want 0", allocations)
	}
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	if prepared.result.Pending != nil || prepared.refresh.mode != pendingRefreshReloadRequired ||
		!errors.Is(prepared.LocalRefreshError(), ErrPendingRefreshRequired) ||
		prepared.checked != checked ||
		prepared.RetainedBytes() != checked.RetainedBytes()+
			preparedPublicationResultMetadataBytes {
		t.Fatalf("publication fallback retained raw or lost its checked fence")
	}
	oldPending := publishing.snapshot.pendingReview
	next, err := publishing.ApplyPreparedPublication(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if next.PublicationSummary().Status != PublicationCommitted ||
		!next.PublicationSummary().PendingRefreshRequired ||
		next.snapshot.pendingReview != oldPending {
		t.Fatal("publication fallback did not preserve committed with stale local projection")
	}

	cause := errors.New("cancel fallback")
	if _, err := PreparePublicationResultFallbackCancellable(
		func() error { return cause },
		request,
		source,
		raw,
	); !errors.Is(err, cause) {
		t.Fatalf("publication fallback cancellation = %v", err)
	}
}

func TestTerminalFallbackAcceptsScalarUnknownWithoutRefreshWarning(t *testing.T) {
	publishing, _, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
	raw := PublicationResult{
		Operation:    request.Operation,
		Workspace:    request.Workspace,
		Source:       request.Source,
		Disposition:  PublicationDispositionUnknown,
		PendingPhase: PendingPublicationSynchronizing,
	}
	prepared, err := PreparePublicationResultFallbackCancellable(
		nil,
		request,
		PendingRebaseSource{},
		raw,
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.LocalRefreshError() != nil {
		t.Fatalf("scalar fallback refresh error = %v", prepared.LocalRefreshError())
	}
	unknown, err := publishing.ApplyPreparedPublication(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.PublicationSummary().Status != PublicationUnknown ||
		unknown.PublicationSummary().PendingRefreshRequired ||
		!unknown.PublicationContinuesPendingReview() {
		t.Fatal("scalar fallback lost the pending continuation")
	}

	reconciliationID, _ := NewOperationID(3)
	reconciling, reconciliation, err := unknown.StartReconciliation(reconciliationID)
	if err != nil {
		t.Fatal(err)
	}
	reconciliationRaw := ReconciliationResult{
		Operation:   reconciliation.Operation,
		Workspace:   reconciliation.Workspace,
		Source:      reconciliation.Source,
		Disposition: ReconciliationStillUnknown,
	}
	reconciliationPrepared, err := PrepareReconciliationResultFallbackCancellable(
		nil,
		reconciliation,
		PendingRebaseSource{},
		reconciliationRaw,
	)
	if err != nil {
		t.Fatal(err)
	}
	stillUnknown, err := reconciling.ApplyPreparedReconciliation(reconciliationPrepared)
	if err != nil {
		t.Fatal(err)
	}
	if stillUnknown.PublicationSummary().Status != PublicationUnknown ||
		stillUnknown.PublicationSummary().PendingRefreshRequired {
		t.Fatal("scalar reconciliation fallback changed the remote outcome")
	}
}

func TestReconciliationResultFallbackKeepsUnvalidatedPendingCaptureUnknown(t *testing.T) {
	publishing, checked, publicationRequest := pendingPublicationAtPublishing(
		t,
		PublicationEventDraft,
	)
	unknown, err := publishing.ApplyPublication(PublicationResult{
		Operation:    publicationRequest.Operation,
		Workspace:    publicationRequest.Workspace,
		Source:       publicationRequest.Source,
		Disposition:  PublicationDispositionUnknown,
		PendingPhase: PendingPublicationSynchronizing,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciliationID, _ := NewOperationID(3)
	reconciling, request, err := unknown.StartReconciliation(reconciliationID)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := checked.Prepared().PendingPublication()
	remote := plan.Baseline()
	remote.body = "external divergence"
	raw := ReconciliationResult{
		Pending:     new(remote),
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: ReconciliationNotCommitted,
	}
	prepared, err := PrepareReconciliationResultFallbackCancellable(
		nil,
		request,
		reconciling.PendingRebaseSource(),
		raw,
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.result.Pending != nil || prepared.Disposition() != ReconciliationStillUnknown ||
		prepared.refresh.mode != pendingRefreshNone || prepared.checked != checked ||
		prepared.LocalRefreshError() != nil ||
		prepared.RetainedBytes() != request.RetainedBytes()+raw.Detail.Bytes()+
			preparedReconciliationResultMetadataBytes {
		t.Fatal("reconciliation fallback trusted raw pending effects or miscounted ownership")
	}
	oldPending := reconciling.snapshot.pendingReview
	next, err := reconciling.ApplyPreparedReconciliation(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if next.PublicationSummary().Status != PublicationUnknown ||
		next.PublicationSummary().PendingRefreshRequired ||
		next.snapshot.pendingReview != oldPending || !next.PublicationContinuesPendingReview() {
		t.Fatal("reconciliation fallback did not preserve the unknown continuation")
	}
	stale := reconciling
	stale.publication.continuation = &pendingPublicationContinuation{
		checked: checked,
		phase:   PendingPublicationIndeterminate,
	}
	if _, err := stale.ApplyPreparedReconciliation(prepared); !errors.Is(err, ErrInvalidPublication) {
		t.Fatalf("stale continuation fallback apply = %v", err)
	}
}

func TestPendingRefreshBlocksStaleMutationAndPreflightUntilReload(t *testing.T) {
	publishing, checked, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
	plan, _ := checked.Prepared().PendingPublication()
	raw := PublicationResult{
		Operation: request.Operation, Workspace: request.Workspace, Source: request.Source,
		RemoteReview: plan.Baseline().DatabaseID().Uint64(),
		Disposition:  PublicationDispositionCommitted, PendingRefresh: PendingRefreshUnavailable,
	}
	prepared, err := PreparePublicationResult(request, publishing.PendingRebaseSource(), raw)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := publishing.ApplyPreparedPublication(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.publication.pendingRefresh || stale.PublicationSource().Ready() {
		t.Fatal("capture failure did not gate stale publication source")
	}
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	if _, _, err := stale.StartPublicationCheck(4, options); !errors.Is(err, ErrInvalidPublication) {
		t.Fatalf("stale publication check = %v", err)
	}
	if _, err := stale.PreparePublication(options); !errors.Is(err, ErrInvalidPublication) {
		t.Fatalf("stale direct preflight = %v", err)
	}
	comment, ok := stale.CommentAt(0)
	if !ok {
		t.Fatal("stale pending comment is unavailable")
	}
	if _, _, err := stale.StartDeleteComment(5, comment.ID()); !errors.Is(err, ErrInvalidCommentMutation) {
		t.Fatalf("stale pending mutation = %v", err)
	}
	for id, operation := range stale.operations {
		if operation.Status != OperationPending {
			continue
		}
		stale, err = stale.ResolveOperation(id, OperationSucceeded, EmptyOperationDetail())
		if err != nil {
			t.Fatal(err)
		}
	}
	reloaded, _, err := stale.ReplaceSnapshot(
		pendingReloadSnapshot(t, "inline", pendingInlineIncluded),
	)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.publication.pendingRefresh || !reloaded.PublicationSource().Ready() {
		t.Fatal("successful reload did not restore fresh pending transitions")
	}
	if _, _, err := reloaded.StartDeleteComment(6, comment.ID()); err != nil {
		t.Fatalf("fresh pending mutation = %v", err)
	}
}

func TestSafeTerminalConstructorsOwnUnknownFallbackShapes(t *testing.T) {
	publishing, _, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
	detail, _ := NewOperationDetail("local terminal preparation failed")
	prepared, err := PrepareUnknownPublicationResult(request, detail)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Disposition() != PublicationDispositionUnknown ||
		prepared.PendingPhase() != PendingPublicationIndeterminate ||
		prepared.Detail() != detail || prepared.LocalRefreshError() != nil ||
		prepared.refresh.mode != pendingRefreshNone {
		t.Fatalf(
			"safe publication = disposition %d phase %d detail %q refresh %v mode %d",
			prepared.Disposition(),
			prepared.PendingPhase(),
			prepared.Detail().String(),
			prepared.LocalRefreshError(),
			prepared.refresh.mode,
		)
	}
	unknown, err := publishing.ApplyPreparedPublication(prepared)
	if err != nil {
		t.Fatal(err)
	}
	reconciliationID, _ := NewOperationID(3)
	reconciling, reconciliation, err := unknown.StartReconciliation(reconciliationID)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := PrepareStillUnknownReconciliationResult(reconciliation, detail)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Disposition() != ReconciliationStillUnknown ||
		reconciled.Detail() != detail || reconciled.LocalRefreshError() != nil ||
		reconciled.refresh.mode != pendingRefreshNone {
		t.Fatalf(
			"safe reconciliation = disposition %d detail %q refresh %v mode %d",
			reconciled.Disposition(),
			reconciled.Detail().String(),
			reconciled.LocalRefreshError(),
			reconciled.refresh.mode,
		)
	}
	if _, err := reconciling.ApplyPreparedReconciliation(reconciled); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedPendingRefreshRejectsInvalidModePayloadCombinations(t *testing.T) {
	publishing, checked, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
	remote := committedPendingCapture(t, checked)
	prepared, err := PreparePublicationResult(
		request,
		publishing.PendingRebaseSource(),
		PublicationResult{
			Pending:        new(remote),
			Operation:      request.Operation,
			Workspace:      request.Workspace,
			Source:         request.Source,
			RemoteReview:   remote.DatabaseID().Uint64(),
			Disposition:    PublicationDispositionCommitted,
			PendingRefresh: PendingRefreshAcquired,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.refresh.mode != pendingRefreshInstall || !prepared.refresh.valid() {
		t.Fatal("valid installed refresh was not represented by the install mode")
	}
	invalid := prepared
	invalid.refresh.mode = pendingRefreshReloadRequired
	if invalid.refresh.valid() || invalid.RetainedBytes() != 0 {
		t.Fatal("reload-required mode accepted an installable rebase payload")
	}
	if _, err := publishing.ApplyPreparedPublication(invalid); !errors.Is(err, ErrInvalidPublication) {
		t.Fatalf("invalid refresh apply = %v", err)
	}

	invalid = prepared
	invalid.refresh.rebase = PreparedPendingRebase{}
	if invalid.refresh.valid() {
		t.Fatal("install mode accepted an empty rebase payload")
	}
}
