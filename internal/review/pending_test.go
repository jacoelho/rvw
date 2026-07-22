package review

import (
	"strings"
	"testing"
)

func TestPendingReviewImportsCleanEditableBindings(t *testing.T) {
	snapshot := pendingTestSnapshot(t)
	current, err := NewReview(snapshot)
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
	current, err := NewReview(pendingTestSnapshot(t))
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
		ViewerUpdate: true,
		ViewerDelete: true,
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
