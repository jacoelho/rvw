package review

import (
	"strings"
	"testing"
)

func TestPendingCapabilitiesRejectUnknownMembersAndBits(t *testing.T) {
	if _, err := NewPendingCapabilities(PendingCapability(99)); err == nil {
		t.Fatal("unknown pending capability was accepted")
	}
	pending, _ := pendingTestSnapshot(t).PendingReview()
	comment := pending.Comments(0, 1)[0]
	if _, err := NewPendingComment(PendingCommentDescriptor{
		NodeID:       comment.nodeID,
		DatabaseID:   comment.databaseID,
		Body:         comment.body,
		Target:       comment.target,
		Capabilities: PendingCapabilities{bits: 1 << 7},
	}); err == nil {
		t.Fatal("pending capability set with unknown bits was accepted")
	}
}

func TestPendingReviewImportsCleanEditableBindings(t *testing.T) {
	snapshot := pendingTestSnapshot(t)
	current, err := NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if current.CommentCount() != 2 || current.HasLocalChanges() {
		t.Fatalf("imported review = comments %d dirty %t", current.CommentCount(), current.HasLocalChanges())
	}
	for index := range current.CommentCount() {
		comment, _ := current.CommentAt(index)
		if state := current.PendingBindingState(comment.ID()); state != PendingBindingClean {
			t.Fatalf("comment %d state = %s", index, state)
		}
		if strings.Contains(comment.Body().String(), "rvw-") {
			t.Fatalf("comment %d exposed reserved marker", index)
		}
	}

	before := current
	comment, _ := current.CommentAt(1)
	current, err = current.BeginEditComment(comment.ID())
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("updated inline")
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = current.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	if state := current.PendingBindingState(comment.ID()); state != PendingBindingModified {
		t.Fatalf("edited state = %s", state)
	}
	if !current.HasLocalChanges() || before.HasLocalChanges() {
		t.Fatalf("copy-on-write dirtiness = before %t after %t", before.HasLocalChanges(), current.HasLocalChanges())
	}
	original, _ := before.CommentAt(1)
	if original.Body().String() != "inline" {
		t.Fatalf("historical body changed to %q", original.Body().String())
	}
}

func TestPendingDeleteCreatesRestorableTombstone(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	comment, _ := current.CommentAt(1)
	current, err = current.DeleteComment(comment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if current.CommentCount() != 2 || current.PendingBindingState(comment.ID()) != PendingBindingDeleted {
		t.Fatalf("delete did not retain a tombstone")
	}
	current, err = current.BeginEditComment(comment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if current.PendingBindingState(comment.ID()) != PendingBindingClean {
		t.Fatalf("editing did not restore tombstone")
	}
}

func TestPendingReviewAcquisitionStatusesRemainDistinct(t *testing.T) {
	snapshot := testSnapshot(t, 1, "one.go")
	for _, status := range []PendingReviewStatus{
		PendingReviewAbsent,
		PendingReviewUnavailable,
		PendingReviewLimited,
		PendingReviewAmbiguous,
	} {
		detail := EmptyOperationDetail()
		if status != PendingReviewAbsent {
			detail = mustOperationDetail(status.String())
		}
		captured, err := snapshot.WithPendingReviewStatus(status, detail)
		if err != nil {
			t.Fatal(err)
		}
		if got := captured.PendingReviewSummary().Status; got != status {
			t.Fatalf("status = %s, want %s", got, status)
		}
	}
}

func TestPendingReviewRemovesItsCommentsFromReadOnlyRemoteThreads(t *testing.T) {
	base := testSnapshot(t, 1, "one.go")
	path, _ := NewPath([]byte("one.go"))
	line, _ := NewLineNumber(2)
	pendingComment, _ := NewRemoteComment(RemoteCommentDescriptor{ID: "PRRC_node", Body: "draft"})
	publishedComment, _ := NewRemoteComment(RemoteCommentDescriptor{ID: "published", Body: "published"})
	thread, err := NewRemoteThread(RemoteThreadDescriptor{
		ID: "thread", Path: path, Side: LineNew, Line: new(line),
		Comments: []RemoteComment{publishedComment, pendingComment},
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err = base.WithRemoteThreads([]RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	pendingSnapshot := pendingTestSnapshot(t)
	pending, _ := pendingSnapshot.PendingReview()
	base, err = base.WithPendingReview(pending)
	if err != nil {
		t.Fatal(err)
	}
	threads := base.RemoteThreads(0, 1)
	if len(threads) != 1 || threads[0].CommentCount() != 1 ||
		threads[0].Comments(0, 1)[0].ID() != "published" {
		t.Fatalf("filtered threads=%v", threads)
	}
}

func TestPendingConstructorsRevalidateOpaqueInputs(t *testing.T) {
	if _, err := NewReviewNodeID("   "); err == nil {
		t.Fatal("blank review node identity unexpectedly accepted")
	}
	pending, _ := pendingTestSnapshot(t).PendingReview()
	comment := pending.Comments(0, 1)[0]
	invalidTarget := comment.target
	invalidTarget.commit = ObjectID("not-an-object-id")
	if _, err := NewPendingComment(PendingCommentDescriptor{
		NodeID: comment.nodeID, DatabaseID: comment.databaseID,
		Body: comment.body, Target: invalidTarget,
	}); err == nil {
		t.Fatal("comment with an invalid opaque target unexpectedly accepted")
	}
	if _, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: pending.nodeID, DatabaseID: pending.databaseID,
		Commit: ObjectID("not-an-object-id"), Body: pending.body,
		Comments: pending.comments,
	}); err == nil {
		t.Fatal("review with an invalid opaque commit unexpectedly accepted")
	}
}

func TestPendingCommentTargetMatchesPendingAdditionProjection(t *testing.T) {
	path, _ := NewPath([]byte("one.go"))
	commit, _ := NewObjectID(strings.Repeat("1", 40))
	originalCommit, _ := NewObjectID(strings.Repeat("2", 40))
	line, _ := NewLineNumber(12)
	start, _ := NewLineNumber(10)
	originalLine, _ := NewLineNumber(22)
	originalStart, _ := NewLineNumber(20)
	desired, err := NewPendingCommentTarget(PendingCommentTargetDescriptor{
		Path: path, Commit: commit, OriginalCommit: originalCommit,
		Line: new(line), StartLine: new(start), OriginalLine: new(originalLine),
		OriginalStart: new(originalStart), Subject: PendingSubjectLine,
		Side: LineNew, StartSide: LineNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	remote := desired
	otherCommit, _ := NewObjectID(strings.Repeat("3", 40))
	otherOriginalLine, _ := NewLineNumber(30)
	remote.commit = otherCommit
	remote.originalCommit = otherCommit
	remote.originalLine = new(otherOriginalLine)
	remote.originalStart = nil
	if !remote.MatchesPendingAddition(desired) {
		t.Fatal("server-populated provenance changed the requested placement")
	}

	otherPath, _ := NewPath([]byte("other.go"))
	otherLine, _ := NewLineNumber(13)
	otherStart, _ := NewLineNumber(9)
	tests := []struct {
		name   string
		mutate func(*PendingCommentTarget)
	}{
		{name: "subject", mutate: func(target *PendingCommentTarget) { target.subject = PendingSubjectFile }},
		{name: "path", mutate: func(target *PendingCommentTarget) { target.path = otherPath }},
		{name: "line value", mutate: func(target *PendingCommentTarget) { target.line = new(otherLine) }},
		{name: "line presence", mutate: func(target *PendingCommentTarget) { target.line = nil }},
		{name: "start value", mutate: func(target *PendingCommentTarget) { target.startLine = new(otherStart) }},
		{name: "start presence", mutate: func(target *PendingCommentTarget) { target.startLine = nil }},
		{name: "side", mutate: func(target *PendingCommentTarget) { target.side = LineOld }},
		{name: "start side", mutate: func(target *PendingCommentTarget) { target.startSide = LineOld }},
		{name: "outdated", mutate: func(target *PendingCommentTarget) { target.outdated = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := remote
			test.mutate(&changed)
			if changed.MatchesPendingAddition(desired) {
				t.Fatal("divergent request-controlled placement matched")
			}
		})
	}

	replyNode, _ := NewCommentNodeID("reply-node")
	replyDatabase, _ := NewCommentDatabaseID(42)
	desiredReply, err := NewPendingCommentTarget(PendingCommentTargetDescriptor{
		Path: path, Commit: commit, OriginalCommit: originalCommit,
		ReplyTo: replyNode, Subject: PendingSubjectReply,
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteReply := desiredReply
	remoteReply.replyDatabase = replyDatabase
	if !remoteReply.MatchesPendingAddition(desiredReply) {
		t.Fatal("server-populated alternate reply identity did not match")
	}
	remoteReply.replyTo = "other-reply"
	if remoteReply.MatchesPendingAddition(desiredReply) {
		t.Fatal("different requested reply target matched")
	}
}

func TestPendingTargetProjectionUsesMappingDisposition(t *testing.T) {
	path, _ := NewPath([]byte("one.go"))
	commit, _ := NewObjectID(strings.Repeat("1", 40))
	target, err := NewPendingCommentTarget(PendingCommentTargetDescriptor{
		Path: path, Commit: commit, OriginalCommit: commit, Subject: PendingSubjectFile,
	})
	if err != nil {
		t.Fatal(err)
	}

	projected, flags := projectPendingTargetFile(remotePathMapping{
		status: RemoteMappingMapped,
		file:   7,
	}, target)
	if file, ok := projected.FileID(); !ok || file != 7 || flags.has(commentFlagUnmappable) {
		t.Fatalf("mapped projection = file %d present %v flags %d", file, ok, flags)
	}

	for _, status := range []RemoteMappingStatus{RemoteMappingMissing, RemoteMappingAmbiguous} {
		projected, flags = projectPendingTargetFile(remotePathMapping{status: status}, target)
		if projected.Kind() != TargetReview || !flags.has(commentFlagUnmappable) {
			t.Fatalf("mapping status %d projection = kind %d flags %d", status, projected.Kind(), flags)
		}
	}
}

func TestCanonicalPendingMarkerSemanticOperations(t *testing.T) {
	publication := publicationMarkerPrefix + strings.Repeat("a", 32) + markerSuffix
	marker, err := NewPublicationMarker(publication)
	if err != nil || marker.String() != publication {
		t.Fatalf("publication marker = %q, %v", marker, err)
	}
	if _, err := NewPublicationMarker(
		publicationMarkerPrefix + strings.Repeat("A", 32) + markerSuffix,
	); err == nil {
		t.Fatal("uppercase publication marker succeeded")
	}
	var attempt [16]byte
	for index := range attempt {
		attempt[index] = 0xab
	}
	if got := NewPublicationMarkerFromAttempt(attempt).String(); got != publicationMarkerPrefix+strings.Repeat("ab", 16)+markerSuffix {
		t.Fatalf("attempt marker = %q", got)
	}

	draft := draftCommentMarkerPrefix + strings.Repeat("b", 32) + markerSuffix
	comment := PendingComment{body: "visible\n\n" + draft}
	if got := comment.DraftMarker(); got != draft {
		t.Fatalf("draft marker = %q, want %q", got, draft)
	}
	if got := (PendingComment{body: "visible " + draft}).DraftMarker(); got != draft {
		t.Fatalf("terminal marker without separator = %q, want %q", got, draft)
	}
	if got := (PendingComment{body: draft + " trailing"}).DraftMarker(); got != "" {
		t.Fatalf("non-terminal marker = %q", got)
	}
	if got := (PendingComment{body: draft}).DraftMarker(); got != draft {
		t.Fatalf("empty-body draft marker = %q, want %q", got, draft)
	}

	mutation := PendingMutation{
		kind: PendingMutationUpdateComment, baseline: "base", marker: draft,
	}
	if got := mutation.BaselineRemoteBody(); got != "base\n\n"+draft {
		t.Fatalf("baseline wire body = %q", got)
	}
	mutation.baseline = ""
	if got := mutation.BaselineRemoteBody(); got != draft {
		t.Fatalf("empty baseline wire body = %q, want %q", got, draft)
	}
	mutation = PendingMutation{kind: PendingMutationAddComment, localID: 7, body: "body"}
	if got := mutation.DraftMarker(ReviewNodeID("review")); !validPendingMarker(
		got,
		draftCommentMarkerPrefix,
	) {
		t.Fatalf("stable draft marker = %q", got)
	}
}

func TestPendingTakeConstructorsRevalidateOpaqueInputs(t *testing.T) {
	pending, _ := pendingTestSnapshot(t).PendingReview()
	comment := pending.comments[0]

	invalidComment := comment
	invalidComment.nodeID = CommentNodeID("   ")
	if _, err := TakePendingComment(PendingCommentDescriptor{
		NodeID: invalidComment.nodeID, DatabaseID: invalidComment.databaseID,
		Body: invalidComment.body, Target: invalidComment.target,
		Capabilities: invalidComment.capabilities,
	}); err == nil {
		t.Fatal("taken comment with a forged node identity unexpectedly accepted")
	}

	invalidComment = comment
	invalidComment.target.originalCommit = ObjectID("not-an-object-id")
	if _, err := TakePendingComment(PendingCommentDescriptor{
		NodeID: invalidComment.nodeID, DatabaseID: invalidComment.databaseID,
		Body: invalidComment.body, Target: invalidComment.target,
		Capabilities: invalidComment.capabilities,
	}); err == nil {
		t.Fatal("taken comment with a forged target unexpectedly accepted")
	}

	if _, err := TakePendingReview(PendingReviewDescriptor{
		NodeID: ReviewNodeID("   "), DatabaseID: pending.databaseID,
		Commit: pending.commit, Body: pending.body, Comments: pending.comments,
	}); err == nil {
		t.Fatal("taken review with a forged node identity unexpectedly accepted")
	}
	if _, err := TakePendingReview(PendingReviewDescriptor{
		NodeID: pending.nodeID, DatabaseID: pending.databaseID,
		Commit: ObjectID("not-an-object-id"), Body: pending.body, Comments: pending.comments,
	}); err == nil {
		t.Fatal("taken review with a forged commit unexpectedly accepted")
	}
}

func pendingTestSnapshot(t *testing.T) Snapshot {
	t.Helper()
	snapshot := testSnapshot(t, 1, "one.go")
	path, _ := NewPath([]byte("one.go"))
	commit, _ := NewObjectID(strings.Repeat("1", 40))
	reviewNode, _ := NewReviewNodeID("PRR_node")
	reviewDatabase, _ := NewReviewDatabaseID(1 << 40)
	commentNode, _ := NewCommentNodeID("PRRC_node")
	commentDatabase, _ := NewCommentDatabaseID(1<<40 + 1)
	line, _ := NewLineNumber(2)
	target, err := NewPendingCommentTarget(PendingCommentTargetDescriptor{
		Path:           path,
		Commit:         commit,
		OriginalCommit: commit,
		Line:           new(line),
		Subject:        PendingSubjectLine,
		Side:           LineNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := NewPendingComment(PendingCommentDescriptor{
		NodeID:       commentNode,
		DatabaseID:   commentDatabase,
		Body:         "inline\n\n<!-- rvw-draft-comment:v1:0123456789abcdef0123456789abcdef -->",
		Target:       target,
		Capabilities: editablePendingCapabilities(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := NewPendingReview(PendingReviewDescriptor{
		NodeID:       reviewNode,
		DatabaseID:   reviewDatabase,
		Commit:       commit,
		Body:         "summary\n\n<!-- rvw-publication:v1:fedcba9876543210fedcba9876543210 -->",
		Comments:     []PendingComment{comment},
		ViewerUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = snapshot.WithPendingReview(pending)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func editablePendingCapabilities(tb testing.TB) PendingCapabilities {
	tb.Helper()
	capabilities, err := NewPendingCapabilities(
		PendingCapabilityUpdate,
		PendingCapabilityDelete,
	)
	if err != nil {
		tb.Fatal(err)
	}
	return capabilities
}
