package review

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"
)

func TestGenericReconciliationRequestAccountsDynamicTargetOwnership(t *testing.T) {
	publishing, _, publication := genericPublicationAtPublishing(t, PublicationEventComment)
	unknown, err := publishing.ApplyPublication(PublicationResult{
		Operation: publication.Operation, Workspace: publication.Workspace,
		Source: publication.Source, Disposition: PublicationDispositionUnknown,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := "a/" + strings.Repeat("b", 510)
	base, _ := NewObjectID(strings.Repeat("1", 40))
	head, _ := NewObjectID(strings.Repeat("2", 40))
	target, err := NewPublicationTarget(PublicationTargetDescriptor{
		Repository: repository, PullRequest: 1, Base: base, Head: head,
	})
	if err != nil {
		t.Fatal(err)
	}
	unknown.publication.target = target
	reconciling, request, err := unknown.StartReconciliation(3)
	if err != nil {
		t.Fatal(err)
	}
	_ = reconciling
	want := reconciliationRequestMetadataBytes + int64(
		len(request.Marker)+len(repository)+len(base)+len(head),
	)
	if request.RetainedBytes() != want || request.RetainedBytes() <= 512 {
		t.Fatalf("reconciliation request retained = %d, want %d", request.RetainedBytes(), want)
	}
	if reconciliationRequestMetadataBytes < int64(unsafe.Sizeof(ReconciliationRequest{})) {
		t.Fatalf(
			"reconciliation metadata = %d, layout = %d",
			reconciliationRequestMetadataBytes,
			unsafe.Sizeof(ReconciliationRequest{}),
		)
	}
}

func TestPreparePendingPublicationPlansUpdatesAndAddsWithoutRetargeting(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := current.CommentAt(0)
	current = editTestComment(t, current, body.ID(), "updated summary")
	inline, _ := current.CommentAt(1)
	current = editTestComment(t, current, inline.ID(), "updated inline")
	current = saveTestComment(t, current, mustFileTarget(t), "new file note")

	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := prepared.PendingPublication()
	if !ok || pending.Submit() || pending.MutationCount() != 3 {
		t.Fatalf("pending=%t submit=%t mutations=%d", ok, pending.Submit(), pending.MutationCount())
	}
	mutations := pending.Mutations(0, pending.MutationCount())
	if mutations[0].Kind() != PendingMutationReviewBody ||
		mutations[1].Kind() != PendingMutationUpdateComment ||
		mutations[2].Kind() != PendingMutationAddComment ||
		mutations[2].Target().Subject() != PendingSubjectFile {
		t.Fatalf("mutation kinds=%d,%d,%d", mutations[0].Kind(), mutations[1].Kind(), mutations[2].Kind())
	}
	if len(mutations[1].Target().Path().Raw()) != 0 {
		t.Fatal("an existing update unexpectedly exposed an add target")
	}
	first := mutations[2].RemoteBody(pending.Baseline().NodeID(), "<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->")
	second := mutations[2].RemoteBody(pending.Baseline().NodeID(), "<!-- rvw-publication:v1:fedcba9876543210fedcba9876543210 -->")
	if first != second {
		t.Fatal("new-comment marker changed across attempts")
	}
}

func TestReusePendingPublicationConvertsNewDraftToExistingReviewMutations(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	current = saveTestComment(t, current, ReviewCommentTarget(), "local summary")
	line, err := NewLineNumber(1)
	if err != nil {
		t.Fatal(err)
	}
	lineTarget, err := LineCommentTarget(0, LineNew, line, line)
	if err != nil {
		t.Fatal(err)
	}
	current = saveTestComment(t, current, lineTarget, "local inline")
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}

	nodeID, _ := NewReviewNodeID("existing-review")
	databaseID, _ := NewReviewDatabaseID(42)
	commit, _ := NewObjectID(strings.Repeat("2", 40))
	remote, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: nodeID, DatabaseID: databaseID, Commit: commit,
		Body: "existing summary", ViewerUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	construction := prepared.PendingReuseConstructionBytes(remote)
	reused, err := prepared.ReusePendingPublication(remote)
	if err != nil {
		t.Fatal(err)
	}
	if construction == 0 || reused.RetainedBytes() > construction {
		t.Fatalf("reuse bytes = %d, construction = %d", reused.RetainedBytes(), construction)
	}
	plan, ok := reused.PendingPublication()
	if !ok || plan.Submit() || plan.Baseline().NodeID() != nodeID ||
		plan.Baseline().DatabaseID() != databaseID {
		t.Fatalf("reused plan = (%t, %+v)", ok, plan)
	}
	if plan.DesiredBody() != "existing summary\n\nlocal summary" {
		t.Fatalf("desired body = %q", plan.DesiredBody())
	}
	mutations := plan.Mutations(0, plan.MutationCount())
	if len(mutations) != 2 || mutations[0].Kind() != PendingMutationReviewBody ||
		mutations[1].Kind() != PendingMutationAddComment ||
		mutations[1].Target().Subject() != PendingSubjectLine ||
		string(mutations[1].Target().Path().Raw()) != "one.go" {
		t.Fatalf("reused mutations = %+v", mutations)
	}
	if reused.SummaryBody() != "" || reused.CommentCount() != 0 {
		t.Fatal("reused draft retained the direct-review payload")
	}
}

func TestReusePendingPublicationAddsInlineWithoutUpdatingReadOnlyBody(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	line, _ := NewLineNumber(1)
	target, _ := LineCommentTarget(0, LineNew, line, line)
	current = saveTestComment(t, current, target, "local inline")
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	nodeID, _ := NewReviewNodeID("existing-review")
	databaseID, _ := NewReviewDatabaseID(42)
	commit, _ := NewObjectID(strings.Repeat("2", 40))
	remote, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: nodeID, DatabaseID: databaseID, Commit: commit,
		Body: "read-only summary", ViewerUpdate: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := prepared.ReusePendingPublication(remote)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := reused.PendingPublication()
	mutations := plan.Mutations(0, plan.MutationCount())
	if len(mutations) != 1 || mutations[0].Kind() != PendingMutationAddComment {
		t.Fatalf("read-only mutations = %+v", mutations)
	}
}

func TestPendingPublicationViewBorrowsImmutableBackingWithoutAllocation(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	retained := prepared.RetainedBytes()
	view, ok := prepared.BorrowPendingPublication()
	if !ok || view.BaselineNodeID() == "" || view.BaselineDatabaseID() == 0 ||
		view.BaselineBody() == "" || view.MutationCount() == 0 {
		t.Fatal("borrowed pending publication is incomplete")
	}
	mutation, ok := view.MutationAt(0)
	if !ok {
		t.Fatal("borrowed mutation is unavailable")
	}
	originalBody := mutation.Body()
	mutation.body = "mutated copy"
	mutation, ok = view.MutationAt(0)
	if !ok || mutation.Body() != originalBody {
		t.Fatal("mutating a returned effect changed borrowed backing")
	}
	cloned, _ := prepared.PendingPublication()
	cloned.baseline.body = "mutated baseline copy"
	cloned.mutations[0].body = "mutated plan copy"
	mutation, _ = view.MutationAt(0)
	if view.BaselineBody() == cloned.baseline.body || mutation.Body() != originalBody ||
		prepared.RetainedBytes() != retained {
		t.Fatal("borrowed view exposed mutable or separately accounted ownership")
	}
	if allocations := testing.AllocsPerRun(100, func() {
		borrowed, borrowedOK := prepared.BorrowPendingPublication()
		_, mutationOK := borrowed.MutationAt(0)
		if !borrowedOK || !mutationOK || borrowed.BaselineNodeID() == "" {
			panic("borrowed pending publication is unavailable")
		}
	}); allocations != 0 {
		t.Fatalf("borrowed pending access allocations = %f, want 0", allocations)
	}
}

func TestPreparePendingPublicationRejectsExcludingExistingDraft(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	inline, _ := current.CommentAt(1)
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	options, _ = options.WithExcludedComments([]CommentID{inline.ID()})
	if _, err := current.PreparePublication(options); err == nil {
		t.Fatal("excluding an existing GitHub draft unexpectedly succeeded")
	}
}

func TestPreparePendingDraftAllowsInlineEditWhenUnchangedReviewBodyIsReadOnly(t *testing.T) {
	baseline, _ := pendingTestSnapshot(t).PendingReview()
	readOnlyBody, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: baseline.NodeID(), DatabaseID: baseline.DatabaseID(), Commit: baseline.Commit(),
		Body: baseline.Body(), Comments: baseline.Comments(0, baseline.CommentCount()),
		ViewerUpdate: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t, 1, "one.go")
	snapshot, err = snapshot.WithPendingReview(readOnlyBody)
	if err != nil {
		t.Fatal(err)
	}
	current, err := NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	inline, _ := current.CommentAt(1)
	current = editTestComment(t, current, inline.ID(), "updated inline")
	policies, _ := NewPublicationPolicies()
	draft, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(draft)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err = prepared.ReconcilePendingPublication(readOnlyBody)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := prepared.PendingPublication()
	mutations := plan.Mutations(0, plan.MutationCount())
	if len(mutations) != 1 || mutations[0].Kind() != PendingMutationUpdateComment {
		t.Fatalf("mutations=%v", mutations)
	}
	comment, _ := NewPublicationOptions(PublicationEventComment, policies)
	if _, err := current.PreparePublication(comment); err == nil {
		t.Fatal("submitting a read-only pending review unexpectedly succeeded")
	}
}

func TestReconcilePendingPublicationPrunesAlreadyAppliedDraftEffects(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	inline, _ := current.CommentAt(1)
	current = editTestComment(t, current, inline.ID(), "updated inline")
	current = saveTestComment(t, current, mustFileTarget(t), "new file note")
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := prepared.PendingPublication()
	baseline := plan.Baseline()
	mutations := plan.Mutations(0, plan.MutationCount())
	comments := baseline.Comments(0, baseline.CommentCount())
	comments[0].body = mutations[1].RemoteBody(baseline.NodeID(), "")
	addedNode, _ := NewCommentNodeID("new_node")
	addedDatabase, _ := NewCommentDatabaseID(1<<40 + 2)
	added, err := NewPendingComment(PendingCommentDescriptor{
		NodeID:       addedNode,
		DatabaseID:   addedDatabase,
		Body:         mutations[2].RemoteBody(baseline.NodeID(), ""),
		Target:       mutations[2].Target(),
		Capabilities: editablePendingCapabilities(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	comments = append(comments, added)
	remote, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: baseline.NodeID(), DatabaseID: baseline.DatabaseID(), Commit: baseline.Commit(),
		Body: baseline.Body(), Comments: comments, ViewerUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := prepared.ReconcilePendingPublication(remote)
	if err != nil {
		t.Fatal(err)
	}
	remaining, _ := reconciled.PendingPublication()
	if remaining.MutationCount() != 0 {
		t.Fatalf("remaining mutations=%d", remaining.MutationCount())
	}
}

func TestClassifyPendingPublicationMutationUsesClosedOutcomes(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	inline, _ := current.CommentAt(1)
	current = editTestComment(t, current, inline.ID(), "updated inline")
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	pending := prepared.pending
	if pending == nil || len(pending.mutations) < 2 ||
		pending.mutations[1].kind != PendingMutationUpdateComment {
		t.Fatalf("pending mutations = %+v", pending)
	}
	mutation := pending.mutations[1]

	for _, test := range []struct {
		name string
		body string
		want pendingMutationReconciliation
	}{
		{name: "retain", body: mutation.baseline, want: pendingMutationRetain},
		{name: "already applied", body: mutation.body, want: pendingMutationAlreadyApplied},
		{name: "diverged", body: "changed externally", want: pendingMutationDiverged},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := pending.baseline
			remote.comments = append([]PendingComment(nil), remote.comments...)
			remote.comments[0].body = test.body
			index, err := buildPendingRemotePublicationIndexCancellable(nil, remote)
			if err != nil {
				t.Fatal(err)
			}
			got, classifyErr := classifyPendingPublicationMutation(
				pending,
				remote,
				index,
				mutation,
				"",
			)
			if got != test.want {
				t.Fatalf("disposition = %d, want %d", got, test.want)
			}
			if (classifyErr != nil) != (test.want == pendingMutationDiverged) {
				t.Fatalf("classification error = %v", classifyErr)
			}
		})
	}
}

func TestReconcilePendingPublicationRejectsMarkerWithDivergedTarget(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	current = saveTestComment(t, current, mustFileTarget(t), "new file note")
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := prepared.PendingPublication()
	baseline := plan.Baseline()
	mutations := plan.Mutations(0, plan.MutationCount())
	otherPath, _ := NewPath([]byte("other.go"))
	wrongTarget, err := NewPendingCommentTarget(PendingCommentTargetDescriptor{
		Path: otherPath, Commit: baseline.Commit(), OriginalCommit: baseline.Commit(),
		Subject: PendingSubjectFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	addedNode, _ := NewCommentNodeID("new_node")
	addedDatabase, _ := NewCommentDatabaseID(1<<40 + 2)
	added, err := NewPendingComment(PendingCommentDescriptor{
		NodeID: addedNode, DatabaseID: addedDatabase,
		Body: mutations[1].RemoteBody(baseline.NodeID(), ""), Target: wrongTarget,
		Capabilities: editablePendingCapabilities(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: baseline.NodeID(), DatabaseID: baseline.DatabaseID(), Commit: baseline.Commit(),
		Body:         baseline.Body(),
		Comments:     append(baseline.Comments(0, baseline.CommentCount()), added),
		ViewerUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.ReconcilePendingPublication(remote); err == nil {
		t.Fatal("marker with a diverged target unexpectedly reconciled")
	}
}

func TestPendingPublicationUnknownRetainsTypedContinuationUntilTerminalReconciliation(t *testing.T) {
	publishing, checked, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
	before := publishing.retainedBytesWithoutSnapshot()
	if _, err := publishing.ApplyPublication(PublicationResult{
		Operation: request.Operation, Workspace: request.Workspace, Source: request.Source,
		Disposition: PublicationDispositionUnknown,
	}); err == nil {
		t.Fatal("pending publication accepted unknown disposition without a phase")
	}
	unknown, err := publishing.ApplyPublication(PublicationResult{
		Operation: request.Operation, Workspace: request.Workspace, Source: request.Source,
		Disposition: PublicationDispositionUnknown, PendingPhase: PendingPublicationSynchronizing,
	})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.PublicationSummary().Status != PublicationUnknown ||
		!unknown.PublicationContinuesPendingReview() ||
		unknown.retainedBytesWithoutSnapshot() != before {
		t.Fatalf("unknown continuation was not retained exactly")
	}

	reconciliationID, _ := NewOperationID(3)
	reconciling, reconciliation, err := unknown.StartReconciliation(reconciliationID)
	if err != nil {
		t.Fatal(err)
	}
	continued, phase, ok := reconciliation.PendingContinuation()
	if !ok || continued != checked || phase != PendingPublicationSynchronizing ||
		reconciliation.RetainedBytes() != checked.RetainedBytes() {
		t.Fatalf("reconciliation continuation = (%p, %d, %t, %d), want (%p, %d, true, %d)",
			continued, phase, ok, reconciliation.RetainedBytes(),
			checked, PendingPublicationSynchronizing, checked.RetainedBytes())
	}
	stillUnknown, err := reconciling.ApplyReconciliation(ReconciliationResult{
		Operation: reconciliationID, Workspace: reconciliation.Workspace,
		Source: reconciliation.Source, Disposition: ReconciliationStillUnknown,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stillUnknown.PublicationContinuesPendingReview() {
		t.Fatal("still-unknown reconciliation discarded its continuation")
	}

	reconciliationID, _ = NewOperationID(4)
	reconciling, reconciliation, err = stillUnknown.StartReconciliation(reconciliationID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, _ := checked.Prepared().PendingPublication()
	fresh := prepared.Baseline()
	settled, err := reconciling.ApplyReconciliation(ReconciliationResult{
		Operation: reconciliationID, Workspace: reconciliation.Workspace,
		Source: reconciliation.Source, Disposition: ReconciliationNotCommitted,
		Pending: new(fresh),
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.PublicationSummary().Status != PublicationConfirmedAbsent ||
		settled.PublicationContinuesPendingReview() {
		t.Fatalf("terminal reconciliation did not release continuation: %+v", settled.PublicationSummary())
	}
}

func TestPendingPublicationReconciliationRequiresTerminalCapture(t *testing.T) {
	publishing, checked, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
	unknown, err := publishing.ApplyPublication(PublicationResult{
		Operation: request.Operation, Workspace: request.Workspace, Source: request.Source,
		Disposition: PublicationDispositionUnknown, PendingPhase: PendingPublicationSynchronizing,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciliationID, _ := NewOperationID(3)
	reconciling, reconciliation, err := unknown.StartReconciliation(reconciliationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciling.ApplyReconciliation(ReconciliationResult{
		Operation: reconciliationID, Workspace: reconciliation.Workspace,
		Source: reconciliation.Source, Disposition: ReconciliationCommitted,
		RemoteReview: checked.Prepared().pending.baseline.databaseID.Uint64(),
	}); err == nil {
		t.Fatal("committed pending draft reconciliation accepted no fresh capture")
	}
	if _, err := reconciling.ApplyReconciliation(ReconciliationResult{
		Operation: reconciliationID, Workspace: reconciliation.Workspace,
		Source: reconciliation.Source, Disposition: ReconciliationNotCommitted,
	}); err == nil {
		t.Fatal("absent pending reconciliation accepted no fresh capture")
	}
}

func TestPublicationResultShapeMatrix(t *testing.T) {
	_, generic, _ := genericPublicationAtPublishing(t, PublicationEventComment)
	_, draft, _ := pendingPublicationAtPublishing(t, PublicationEventDraft)
	_, submit, _ := pendingPublicationAtPublishing(t, PublicationEventComment)
	draftPlan, _ := draft.prepared.PendingPublication()
	submitPlan, _ := submit.prepared.PendingPublication()
	draftCapture := committedPendingCapture(t, draft)

	type shapeCase struct {
		name    string
		checked *CheckedPublication
		result  PublicationResult
	}
	valid := []shapeCase{
		{name: "generic committed", checked: generic, result: PublicationResult{
			Disposition: PublicationDispositionCommitted, RemoteReview: 42,
		}},
		{name: "pending draft committed refresh unavailable", checked: draft, result: PublicationResult{
			Disposition:    PublicationDispositionCommitted,
			RemoteReview:   draftPlan.Baseline().DatabaseID().Uint64(),
			PendingRefresh: PendingRefreshUnavailable,
		}},
		{name: "pending draft committed with refresh", checked: draft, result: PublicationResult{
			Disposition:  PublicationDispositionCommitted,
			RemoteReview: draftPlan.Baseline().DatabaseID().Uint64(), Pending: new(draftCapture),
			PendingRefresh: PendingRefreshAcquired,
		}},
		{name: "pending submit committed", checked: submit, result: PublicationResult{
			Disposition:  PublicationDispositionCommitted,
			RemoteReview: submitPlan.Baseline().DatabaseID().Uint64(),
		}},
		{name: "generic unknown", checked: generic, result: PublicationResult{
			Disposition: PublicationDispositionUnknown,
		}},
		{name: "pending synchronizing", checked: submit, result: PublicationResult{
			Disposition: PublicationDispositionUnknown, PendingPhase: PendingPublicationSynchronizing,
		}},
		{name: "pending submitting", checked: submit, result: PublicationResult{
			Disposition: PublicationDispositionUnknown, PendingPhase: PendingPublicationSubmitting,
		}},
		{name: "pending indeterminate", checked: draft, result: PublicationResult{
			Disposition: PublicationDispositionUnknown, PendingPhase: PendingPublicationIndeterminate,
		}},
		{name: "failed", checked: generic, result: PublicationResult{
			Disposition: PublicationDispositionFailedBeforeCommit,
		}},
		{name: "cancelled", checked: draft, result: PublicationResult{
			Disposition: PublicationDispositionCancelledBeforeCommit,
		}},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePublicationResult(test.checked, test.result); err != nil {
				t.Fatal(err)
			}
		})
	}

	wrongCapture := draftCapture
	wrongCapture.commit, _ = NewObjectID(strings.Repeat("9", 40))
	wrongNode := draftCapture
	wrongNode.nodeID, _ = NewReviewNodeID("other-review")
	wrongDatabase := draftCapture
	wrongDatabase.databaseID, _ = NewReviewDatabaseID(draftPlan.Baseline().DatabaseID().Uint64() + 1)
	invalid := []shapeCase{
		{name: "generic committed missing identity", checked: generic, result: PublicationResult{Disposition: PublicationDispositionCommitted}},
		{name: "generic committed capture", checked: generic, result: PublicationResult{
			Disposition: PublicationDispositionCommitted, RemoteReview: 42, Pending: new(draftCapture),
		}},
		{name: "generic committed phase", checked: generic, result: PublicationResult{
			Disposition: PublicationDispositionCommitted, RemoteReview: 42,
			PendingPhase: PendingPublicationIndeterminate,
		}},
		{name: "draft committed wrong identity", checked: draft, result: PublicationResult{
			Disposition: PublicationDispositionCommitted, RemoteReview: 42,
		}},
		{name: "draft committed wrong capture target", checked: draft, result: PublicationResult{
			Disposition:  PublicationDispositionCommitted,
			RemoteReview: draftPlan.Baseline().DatabaseID().Uint64(), Pending: new(wrongCapture),
			PendingRefresh: PendingRefreshAcquired,
		}},
		{name: "draft committed wrong capture node", checked: draft, result: PublicationResult{
			Disposition:  PublicationDispositionCommitted,
			RemoteReview: draftPlan.Baseline().DatabaseID().Uint64(), Pending: new(wrongNode),
			PendingRefresh: PendingRefreshAcquired,
		}},
		{name: "draft committed wrong capture database", checked: draft, result: PublicationResult{
			Disposition:  PublicationDispositionCommitted,
			RemoteReview: draftPlan.Baseline().DatabaseID().Uint64(), Pending: new(wrongDatabase),
			PendingRefresh: PendingRefreshAcquired,
		}},
		{name: "submit committed capture", checked: submit, result: PublicationResult{
			Disposition:  PublicationDispositionCommitted,
			RemoteReview: submitPlan.Baseline().DatabaseID().Uint64(), Pending: new(draftCapture),
		}},
		{name: "generic unknown phase", checked: generic, result: PublicationResult{
			Disposition: PublicationDispositionUnknown, PendingPhase: PendingPublicationIndeterminate,
		}},
		{name: "generic unknown identity", checked: generic, result: PublicationResult{
			Disposition: PublicationDispositionUnknown, RemoteReview: 42,
		}},
		{name: "generic unknown capture", checked: generic, result: PublicationResult{
			Disposition: PublicationDispositionUnknown, Pending: new(draftCapture),
		}},
		{name: "draft unknown missing phase", checked: draft, result: PublicationResult{Disposition: PublicationDispositionUnknown}},
		{name: "draft unknown submitting", checked: draft, result: PublicationResult{
			Disposition: PublicationDispositionUnknown, PendingPhase: PendingPublicationSubmitting,
		}},
		{name: "pending unknown identity", checked: submit, result: PublicationResult{
			Disposition: PublicationDispositionUnknown, PendingPhase: PendingPublicationSubmitting,
			RemoteReview: submitPlan.Baseline().DatabaseID().Uint64(),
		}},
		{name: "pending unknown capture", checked: submit, result: PublicationResult{
			Disposition: PublicationDispositionUnknown, PendingPhase: PendingPublicationSubmitting,
			Pending: new(draftCapture),
		}},
		{name: "draft committed omitted refresh acquisition", checked: draft, result: PublicationResult{
			Disposition:  PublicationDispositionCommitted,
			RemoteReview: draftPlan.Baseline().DatabaseID().Uint64(),
		}},
		{name: "draft committed acquired without capture", checked: draft, result: PublicationResult{
			Disposition:    PublicationDispositionCommitted,
			RemoteReview:   draftPlan.Baseline().DatabaseID().Uint64(),
			PendingRefresh: PendingRefreshAcquired,
		}},
		{name: "draft committed unavailable with capture", checked: draft, result: PublicationResult{
			Disposition:  PublicationDispositionCommitted,
			RemoteReview: draftPlan.Baseline().DatabaseID().Uint64(), Pending: new(draftCapture),
			PendingRefresh: PendingRefreshUnavailable,
		}},
		{name: "generic committed unavailable", checked: generic, result: PublicationResult{
			Disposition: PublicationDispositionCommitted, RemoteReview: 42,
			PendingRefresh: PendingRefreshUnavailable,
		}},
		{name: "submit committed unavailable", checked: submit, result: PublicationResult{
			Disposition:    PublicationDispositionCommitted,
			RemoteReview:   submitPlan.Baseline().DatabaseID().Uint64(),
			PendingRefresh: PendingRefreshUnavailable,
		}},
		{name: "unknown refresh unavailable", checked: draft, result: PublicationResult{
			Disposition: PublicationDispositionUnknown, PendingPhase: PendingPublicationIndeterminate,
			PendingRefresh: PendingRefreshUnavailable,
		}},
		{name: "unsupported refresh acquisition", checked: generic, result: PublicationResult{
			Disposition:    PublicationDispositionUnknown,
			PendingRefresh: PendingRefreshAcquisition(255),
		}},
	}
	for _, disposition := range []PublicationDisposition{
		PublicationDispositionFailedBeforeCommit,
		PublicationDispositionCancelledBeforeCommit,
	} {
		name := fmt.Sprintf("disposition-%d", disposition)
		invalid = append(invalid,
			shapeCase{name: name + " phase", checked: draft, result: PublicationResult{
				Disposition: disposition, PendingPhase: PendingPublicationSynchronizing,
			}},
			shapeCase{name: name + " identity", checked: generic, result: PublicationResult{
				Disposition: disposition, RemoteReview: 42,
			}},
			shapeCase{name: name + " capture", checked: draft, result: PublicationResult{
				Disposition: disposition, Pending: new(draftCapture),
			}},
		)
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePublicationResult(test.checked, test.result); err == nil {
				t.Fatal("invalid publication result was accepted")
			}
		})
	}
}

func TestCommittedPublicationShapesApply(t *testing.T) {
	t.Run("generic", func(t *testing.T) {
		publishing, _, request := genericPublicationAtPublishing(t, PublicationEventComment)
		next, err := publishing.ApplyPublication(PublicationResult{
			Operation: request.Operation, Workspace: request.Workspace, Source: request.Source,
			Disposition: PublicationDispositionCommitted, RemoteReview: 42,
		})
		if err != nil {
			t.Fatal(err)
		}
		if summary := next.PublicationSummary(); summary.Status != PublicationCommitted ||
			summary.RemoteReview != 42 {
			t.Fatalf("generic committed summary = %+v", summary)
		}
	})

	for _, acquisition := range []PendingRefreshAcquisition{
		PendingRefreshUnavailable,
		PendingRefreshAcquired,
	} {
		name := "pending draft refresh unavailable"
		if acquisition == PendingRefreshAcquired {
			name = "pending draft refresh acquired"
		}
		t.Run(name, func(t *testing.T) {
			publishing, checked, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
			prepared, _ := checked.prepared.PendingPublication()
			result := PublicationResult{
				Operation: request.Operation, Workspace: request.Workspace, Source: request.Source,
				Disposition:    PublicationDispositionCommitted,
				RemoteReview:   prepared.Baseline().DatabaseID().Uint64(),
				PendingRefresh: acquisition,
			}
			if acquisition == PendingRefreshAcquired {
				capture := committedPendingCapture(t, checked)
				result.Pending = new(capture)
			}
			next, err := publishing.ApplyPublication(result)
			if err != nil {
				t.Fatal(err)
			}
			if summary := next.PublicationSummary(); summary.Status != PublicationCommitted ||
				summary.RemoteReview != result.RemoteReview ||
				summary.PendingRefreshRequired != (acquisition == PendingRefreshUnavailable) ||
				next.PublicationContinuesPendingReview() {
				t.Fatalf("draft committed summary = %+v", summary)
			}
		})
	}

	t.Run("pending submission", func(t *testing.T) {
		publishing, checked, request := pendingPublicationAtPublishing(t, PublicationEventComment)
		prepared, _ := checked.prepared.PendingPublication()
		next, err := publishing.ApplyPublication(PublicationResult{
			Operation: request.Operation, Workspace: request.Workspace, Source: request.Source,
			Disposition:  PublicationDispositionCommitted,
			RemoteReview: prepared.Baseline().DatabaseID().Uint64(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if summary := next.PublicationSummary(); summary.Status != PublicationCommitted ||
			next.PublicationContinuesPendingReview() {
			t.Fatalf("submit committed summary = %+v", summary)
		}
	})
}

func TestReconciliationResultPhaseEventDispositionMatrix(t *testing.T) {
	_, draft, _ := pendingPublicationAtPublishing(t, PublicationEventDraft)
	_, submit, _ := pendingPublicationAtPublishing(t, PublicationEventComment)
	for _, test := range []struct {
		name    string
		checked *CheckedPublication
		submit  bool
	}{
		{name: "draft", checked: draft},
		{name: "submit", checked: submit, submit: true},
	} {
		prepared, _ := test.checked.prepared.PendingPublication()
		baseline := prepared.Baseline()
		committed := committedPendingCapture(t, test.checked)
		for _, phase := range []PendingPublicationPhase{
			PendingPublicationSynchronizing,
			PendingPublicationSubmitting,
			PendingPublicationIndeterminate,
		} {
			for _, disposition := range []ReconciliationDisposition{
				ReconciliationCommitted,
				ReconciliationNotCommitted,
				ReconciliationStillUnknown,
			} {
				name := fmt.Sprintf("%s/phase-%d/disposition-%d", test.name, phase, disposition)
				t.Run(name, func(t *testing.T) {
					result := ReconciliationResult{Disposition: disposition}
					switch disposition {
					case ReconciliationCommitted:
						result.RemoteReview = baseline.DatabaseID().Uint64()
						if phase != PendingPublicationSubmitting {
							result.Pending = new(committed)
						}
					case ReconciliationNotCommitted:
						result.Pending = new(baseline)
					case ReconciliationStillUnknown:
					}
					var wantValid bool
					switch {
					case !test.submit:
						wantValid = phase != PendingPublicationSubmitting
					case phase == PendingPublicationSubmitting:
						wantValid = disposition != ReconciliationNotCommitted
					default:
						wantValid = disposition != ReconciliationCommitted
					}
					continuation := &pendingPublicationContinuation{checked: test.checked, phase: phase}
					err := validateReconciliationResult(continuation, result)
					if wantValid && err != nil {
						t.Fatal(err)
					}
					if !wantValid && err == nil {
						t.Fatal("invalid reconciliation result was accepted")
					}
				})
			}
		}
	}

	for _, disposition := range []ReconciliationDisposition{
		ReconciliationCommitted,
		ReconciliationNotCommitted,
		ReconciliationStillUnknown,
	} {
		if err := validateReconciliationResult(nil, ReconciliationResult{
			Disposition: disposition,
		}); err != nil {
			t.Fatalf("generic disposition %d: %v", disposition, err)
		}
		if err := validateReconciliationResult(nil, ReconciliationResult{
			Disposition: disposition, RemoteReview: 42,
		}); err == nil {
			t.Fatalf("generic disposition %d accepted a remote identity", disposition)
		}
	}
}

func TestRejectedPublicationResultsPreserveRetryFence(t *testing.T) {
	publishing, checked, request := pendingPublicationAtPublishing(t, PublicationEventDraft)
	before := publishing.publication
	if next, err := publishing.ApplyPublication(PublicationResult{
		Operation: request.Operation, Workspace: request.Workspace, Source: request.Source,
		Disposition:  PublicationDispositionUnknown,
		RemoteReview: checked.prepared.pending.baseline.databaseID.Uint64(),
		PendingPhase: PendingPublicationSynchronizing,
	}); err == nil || next.publication.checked != before.checked ||
		next.publication.status != PublicationPublishing {
		t.Fatal("rejected publication result changed the publishing fence")
	}

	unknown, err := publishing.ApplyPublication(PublicationResult{
		Operation: request.Operation, Workspace: request.Workspace, Source: request.Source,
		Disposition:  PublicationDispositionUnknown,
		PendingPhase: PendingPublicationSynchronizing,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciliationID, _ := NewOperationID(3)
	reconciling, reconciliation, err := unknown.StartReconciliation(reconciliationID)
	if err != nil {
		t.Fatal(err)
	}
	continuation := reconciling.publication.continuation
	if next, err := reconciling.ApplyReconciliation(ReconciliationResult{
		Operation: reconciliation.Operation, Workspace: reconciliation.Workspace,
		Source: reconciliation.Source, Disposition: ReconciliationCommitted,
		RemoteReview: checked.prepared.pending.baseline.databaseID.Uint64(),
	}); err == nil || next.publication.continuation != continuation ||
		next.publication.status != PublicationReconciling {
		t.Fatal("rejected reconciliation result changed the continuation fence")
	}
}

func committedPendingCapture(t *testing.T, checked *CheckedPublication) PendingReview {
	t.Helper()
	prepared, ok := checked.prepared.PendingPublication()
	if !ok {
		t.Fatal("checked publication has no pending plan")
	}
	mutations := prepared.Mutations(0, prepared.MutationCount())
	if len(mutations) != 1 || mutations[0].Kind() != PendingMutationReviewBody {
		t.Fatalf("test pending plan has mutations %+v", mutations)
	}
	remote := prepared.Baseline()
	remote.body = mutations[0].RemoteBody(remote.nodeID, checked.marker)
	return remote
}

func genericPublicationAtPublishing(
	t *testing.T,
	event PublicationEvent,
) (Review, *CheckedPublication, PublicationRequest) {
	t.Helper()
	current, err := NewReview(testReviewRequest(testSnapshot(t, 1, "one.go")))
	if err != nil {
		t.Fatal(err)
	}
	current = saveTestComment(t, current, ReviewCommentTarget(), summaryValue)
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(event, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	checkID, _ := NewOperationID(1)
	checking, check, err := current.StartPublicationCheck(checkID, options)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := NewObjectID(strings.Repeat("1", 40))
	head, _ := NewObjectID(strings.Repeat("2", 40))
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
		Operation: checkID, Workspace: check.Workspace, Source: check.Source, Checked: checked,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishID, _ := NewOperationID(2)
	publishing, request, err := checkedReview.StartPublication(publishID)
	if err != nil {
		t.Fatal(err)
	}
	return publishing, checked, request
}

func pendingPublicationAtPublishing(
	t *testing.T,
	event PublicationEvent,
) (Review, *CheckedPublication, PublicationRequest) {
	t.Helper()
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	policies, _ := NewPublicationPolicies()
	options, err := NewPublicationOptions(event, policies)
	if err != nil {
		t.Fatal(err)
	}
	checkID, _ := NewOperationID(1)
	checking, check, err := current.StartPublicationCheck(checkID, options)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := NewObjectID(strings.Repeat("1", 40))
	head, _ := NewObjectID(strings.Repeat("2", 40))
	target, err := NewPublicationTarget(PublicationTargetDescriptor{
		Repository: "owner/repository", PullRequest: 7, Base: base, Head: head,
	})
	if err != nil {
		t.Fatal(err)
	}
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
		Operation: checkID, Workspace: check.Workspace, Source: check.Source, Checked: checked,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishID, _ := NewOperationID(2)
	publishing, request, err := checkedReview.StartPublication(publishID)
	if err != nil {
		t.Fatal(err)
	}
	return publishing, checked, request
}

func editTestComment(t *testing.T, current Review, id CommentID, body string) Review {
	t.Helper()
	next, err := current.BeginEditComment(id)
	if err != nil {
		t.Fatal(err)
	}
	next, err = next.UpdateDraft(body)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err = next.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	return next
}
