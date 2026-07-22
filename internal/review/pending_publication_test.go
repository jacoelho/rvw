package review

import "testing"

func TestPreparePendingPublicationPlansUpdatesAndAddsWithoutRetargeting(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
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

func TestPreparePendingPublicationRejectsExcludingExistingDraft(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
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
	current, err := NewReview(snapshot)
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
	current, err := NewReview(pendingTestSnapshot(t))
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
		ViewerUpdate: true,
		ViewerDelete: true,
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

func TestReconcilePendingPublicationRejectsMarkerWithDivergedTarget(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
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
		ViewerUpdate: true, ViewerDelete: true,
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
