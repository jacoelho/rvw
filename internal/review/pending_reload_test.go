package review

import "testing"

func TestPendingReloadThreeWayMergesBodies(t *testing.T) {
	tests := []struct {
		name      string
		local     string
		remote    string
		wantBody  string
		wantState PendingBindingState
	}{
		{name: "unchanged", local: "inline", remote: "inline", wantBody: "inline", wantState: PendingBindingClean},
		{name: "local only", local: "local", remote: "inline", wantBody: "local", wantState: PendingBindingModified},
		{name: "remote only", local: "inline", remote: "remote", wantBody: "remote", wantState: PendingBindingClean},
		{name: "same edit", local: "same", remote: "same", wantBody: "same", wantState: PendingBindingClean},
		{name: "conflict", local: "local", remote: "remote", wantBody: "local", wantState: PendingBindingConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current, err := NewReview(pendingTestSnapshot(t))
			if err != nil {
				t.Fatal(err)
			}
			inline, _ := current.CommentAt(1)
			if test.local != "inline" {
				current = editTestComment(t, current, inline.ID(), test.local)
			}
			replacement := pendingReloadSnapshot(t, test.remote, true)
			next, _, err := current.ReplaceSnapshot(replacement)
			if err != nil {
				t.Fatal(err)
			}
			comment, ok := next.CommentAt(1)
			if !ok || comment.Body().String() != test.wantBody {
				t.Fatalf("body=%q ok=%t", comment.Body().String(), ok)
			}
			if state := next.PendingBindingState(inline.ID()); state != test.wantState {
				t.Fatalf("state=%s want=%s", state, test.wantState)
			}
		})
	}
}

func TestPendingReloadHandlesRemoteDeletionWithoutDiscardingLocalEdits(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	inline, _ := current.CommentAt(1)
	current = editTestComment(t, current, inline.ID(), "local")
	replacement := pendingReloadSnapshot(t, "", false)
	next, _, err := current.ReplaceSnapshot(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if state := next.PendingBindingState(inline.ID()); state != PendingBindingConflict {
		t.Fatalf("state=%s", state)
	}
}

func TestPendingConflictResolutionChoosesLocalRemoteOrBase(t *testing.T) {
	tests := []struct {
		name      string
		choice    PendingConflictResolution
		wantBody  string
		wantState PendingBindingState
	}{
		{name: "local", choice: PendingConflictLocal, wantBody: "local", wantState: PendingBindingModified},
		{name: "remote", choice: PendingConflictRemote, wantBody: "remote", wantState: PendingBindingClean},
		{name: "base", choice: PendingConflictBase, wantBody: "inline", wantState: PendingBindingModified},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current, err := NewReview(pendingTestSnapshot(t))
			if err != nil {
				t.Fatal(err)
			}
			inline, _ := current.CommentAt(1)
			current = editTestComment(t, current, inline.ID(), "local")
			current, _, err = current.ReplaceSnapshot(pendingReloadSnapshot(t, "remote", true))
			if err != nil {
				t.Fatal(err)
			}
			current, err = current.ResolvePendingConflict(inline.ID(), test.choice)
			if err != nil {
				t.Fatal(err)
			}
			resolved, ok := current.commentByID(inline.ID())
			if !ok || resolved.Body().String() != test.wantBody {
				t.Fatalf("body=%q ok=%t", resolved.Body().String(), ok)
			}
			if state := current.PendingBindingState(inline.ID()); state != test.wantState {
				t.Fatalf("state=%s want=%s", state, test.wantState)
			}
		})
	}
}

func TestPendingConflictResolutionPreservesLocalDeletion(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	inline, _ := current.CommentAt(1)
	current, err = current.DeleteComment(inline.ID())
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = current.ReplaceSnapshot(pendingReloadSnapshot(t, "remote edit", true))
	if err != nil {
		t.Fatal(err)
	}
	if state := current.PendingBindingState(inline.ID()); state != PendingBindingConflict {
		t.Fatalf("state=%s", state)
	}
	current, err = current.ResolvePendingConflict(inline.ID(), PendingConflictLocal)
	if err != nil {
		t.Fatal(err)
	}
	if state := current.PendingBindingState(inline.ID()); state != PendingBindingDeleted {
		t.Fatalf("resolved state=%s", state)
	}
}

func TestPendingReloadRemovesCleanBindingsWhenReviewWasSubmittedElsewhere(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	replacement := testSnapshot(t, 2, "one.go")
	replacement, err = replacement.WithPendingReviewStatus(PendingReviewAbsent, EmptyOperationDetail())
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := current.ReplaceSnapshot(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if next.CommentCount() != 0 || next.HasLocalChanges() {
		t.Fatalf("comments=%d dirty=%t", next.CommentCount(), next.HasLocalChanges())
	}
}

func TestPendingReloadAcceptsRemoteAbsenceAfterLocalDeletion(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	inline, _ := current.CommentAt(1)
	current, err = current.DeleteComment(inline.ID())
	if err != nil {
		t.Fatal(err)
	}
	replacement := testSnapshot(t, 2, "one.go")
	replacement, err = replacement.WithPendingReviewStatus(PendingReviewAbsent, EmptyOperationDetail())
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := current.ReplaceSnapshot(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if next.PendingReviewSummary().Conflicts != 0 {
		t.Fatalf("conflicts=%d", next.PendingReviewSummary().Conflicts)
	}
	if _, ok := next.commentByID(inline.ID()); ok {
		t.Fatal("remotely absent deleted comment was retained")
	}
}

func TestPendingMissingReviewCanBecomeNewLocalDraftOnlyAfterResolution(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	inline, _ := current.CommentAt(1)
	current = editTestComment(t, current, inline.ID(), "retain locally")
	replacement := testSnapshot(t, 2, "one.go")
	replacement, err = replacement.WithPendingReviewStatus(PendingReviewAbsent, EmptyOperationDetail())
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = current.ReplaceSnapshot(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if current.PendingReviewSummary().Conflicts != 1 {
		t.Fatalf("conflicts=%d", current.PendingReviewSummary().Conflicts)
	}
	current, err = current.ResolvePendingConflict(inline.ID(), PendingConflictLocal)
	if err != nil {
		t.Fatal(err)
	}
	if current.pending != nil || current.PendingBindingState(inline.ID()) != PendingBindingNew {
		t.Fatalf("pending=%v state=%s", current.pending, current.PendingBindingState(inline.ID()))
	}
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	if _, err := current.PreparePublication(options); err != nil {
		t.Fatalf("explicitly retained local draft cannot start a new review: %v", err)
	}
}

func TestPendingReloadRejectsChangedReviewIdentity(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	baseline, _ := pendingTestSnapshot(t).PendingReview()
	nodeID, _ := NewReviewNodeID("replacement_review")
	databaseID, _ := NewReviewDatabaseID(baseline.DatabaseID().Uint64() + 1)
	replacementReview, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: nodeID, DatabaseID: databaseID, Commit: baseline.Commit(),
		Body: baseline.Body(), Comments: baseline.Comments(0, baseline.CommentCount()),
		ViewerUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement := testSnapshot(t, 2, "one.go")
	replacement, err = replacement.WithPendingReview(replacementReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := current.ReplaceSnapshot(replacement); err == nil {
		t.Fatal("changed pending review identity unexpectedly merged")
	}
}

func TestPendingReloadBindsLocallyAddedDraftByStableMarker(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	current = saveTestComment(t, current, mustFileTarget(t), "new file note")
	added, _ := current.CommentAt(2)
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := prepared.PendingPublication()
	mutations := plan.Mutations(0, plan.MutationCount())
	baseline := plan.Baseline()
	remoteComments := baseline.Comments(0, baseline.CommentCount())
	remoteNode, _ := NewCommentNodeID("added_node")
	remoteDatabase, _ := NewCommentDatabaseID(1<<40 + 2)
	remoteAdded, err := NewPendingComment(PendingCommentDescriptor{
		NodeID: remoteNode, DatabaseID: remoteDatabase,
		Body: mutations[1].RemoteBody(baseline.NodeID(), ""), Target: mutations[1].Target(),
		ViewerUpdate: true, ViewerDelete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteComments = append(remoteComments, remoteAdded)
	remote, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: baseline.NodeID(), DatabaseID: baseline.DatabaseID(), Commit: baseline.Commit(),
		Body: baseline.Body(), Comments: remoteComments, ViewerUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement := testSnapshot(t, 2, "one.go")
	replacement, err = replacement.WithPendingReview(remote)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := current.ReplaceSnapshot(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if next.CommentCount() != 3 || next.PendingBindingState(added.ID()) != PendingBindingClean ||
		next.HasLocalChanges() {
		t.Fatalf("comments=%d state=%s", next.CommentCount(), next.PendingBindingState(added.ID()))
	}
}

func TestPendingConflictBaseDeletesMarkerBoundRemoteAddition(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	current = saveTestComment(t, current, mustFileTarget(t), "new file note")
	added, _ := current.CommentAt(2)
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventDraft, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := prepared.PendingPublication()
	mutations := plan.Mutations(0, plan.MutationCount())
	baseline := plan.Baseline()
	_, marker := splitPendingMarker(
		mutations[1].RemoteBody(baseline.NodeID(), ""),
		draftCommentMarkerPrefix,
	)
	remoteNode, _ := NewCommentNodeID("added_node")
	remoteDatabase, _ := NewCommentDatabaseID(1<<40 + 2)
	remoteAdded, err := NewPendingComment(PendingCommentDescriptor{
		NodeID: remoteNode, DatabaseID: remoteDatabase,
		Body: joinPendingMarker("remote edit", marker), Target: mutations[1].Target(),
		ViewerUpdate: true, ViewerDelete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: baseline.NodeID(), DatabaseID: baseline.DatabaseID(), Commit: baseline.Commit(),
		Body:         baseline.Body(),
		Comments:     append(baseline.Comments(0, baseline.CommentCount()), remoteAdded),
		ViewerUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement := testSnapshot(t, 2, "one.go")
	replacement, err = replacement.WithPendingReview(remote)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = current.ReplaceSnapshot(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if state := current.PendingBindingState(added.ID()); state != PendingBindingConflict {
		t.Fatalf("state=%s", state)
	}
	current, err = current.ResolvePendingConflict(added.ID(), PendingConflictBase)
	if err != nil {
		t.Fatal(err)
	}
	if state := current.PendingBindingState(added.ID()); state != PendingBindingDeleted {
		t.Fatalf("resolved state=%s", state)
	}
	prepared, err = current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ = prepared.PendingPublication()
	mutations = plan.Mutations(0, plan.MutationCount())
	if len(mutations) != 2 || mutations[1].Kind() != PendingMutationDeleteComment {
		t.Fatalf("mutations=%v", mutations)
	}
}

func TestPendingReloadFoldsAppliedSummaryIntoReviewBody(t *testing.T) {
	current, err := NewReview(pendingTestSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	current = saveTestComment(t, current, ReviewCommentTarget(), "extra")
	base, _ := pendingTestSnapshot(t).PendingReview()
	remote, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: base.NodeID(), DatabaseID: base.DatabaseID(), Commit: base.Commit(),
		Body:     "summary\n\nextra\n\n<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
		Comments: base.Comments(0, base.CommentCount()), ViewerUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement := testSnapshot(t, 2, "one.go")
	replacement, _ = replacement.WithPendingReview(remote)
	next, _, err := current.ReplaceSnapshot(replacement)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := next.CommentAt(0)
	if next.CommentCount() != 2 || body.Body().String() != "summary\n\nextra" ||
		next.PendingBindingState(body.ID()) != PendingBindingClean {
		t.Fatalf("comments=%d body=%q state=%s", next.CommentCount(), body.Body().String(), next.PendingBindingState(body.ID()))
	}
}

func pendingReloadSnapshot(t *testing.T, inlineBody string, includeInline bool) Snapshot {
	t.Helper()
	base := pendingTestSnapshot(t)
	pending, _ := base.PendingReview()
	comments := pending.Comments(0, pending.CommentCount())
	if includeInline {
		comments[0].body = inlineBody
	} else {
		comments = nil
	}
	remote, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: pending.NodeID(), DatabaseID: pending.DatabaseID(), Commit: pending.Commit(),
		Body: pending.Body(), Comments: comments, ViewerUpdate: pending.ViewerCanUpdate(),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t, 2, "one.go")
	snapshot, err = snapshot.WithPendingReview(remote)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
