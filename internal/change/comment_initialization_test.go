package change

import (
	"errors"
	"testing"
)

func TestInitializeCommentsAppendsAtomicallyAsOrdinaryDirtyComments(t *testing.T) {
	snapshot := testSnapshot(t, 1, "a.go")
	review := testReview(t, snapshot)
	fileTarget, err := FileCommentTarget(0)
	if err != nil {
		t.Fatal(err)
	}
	reviewSeed, err := NewInitialComment(snapshot, ReviewCommentTarget(), "review body")
	if err != nil {
		t.Fatal(err)
	}
	fileSeed, err := NewInitialComment(snapshot, fileTarget, "file body")
	if err != nil {
		t.Fatal(err)
	}
	initialization, err := NewCommentInitialization(snapshot, []InitialComment{reviewSeed, fileSeed})
	if err != nil {
		t.Fatal(err)
	}
	initialized, err := review.InitializeComments(nil, initialization)
	if err != nil {
		t.Fatal(err)
	}
	if review.CommentCount() != 0 {
		t.Fatalf("original review changed: %d comments", review.CommentCount())
	}
	if initialized.CommentCount() != 2 || !initialized.HasLocalChanges() {
		t.Fatalf("initialized review = %d comments, local changes %t", initialized.CommentCount(), initialized.HasLocalChanges())
	}
	first, _ := initialized.CommentAt(0)
	second, _ := initialized.CommentAt(1)
	if first.ID() != 0 || second.ID() != 1 || first.Body().String() != "review body" || second.Body().String() != "file body" {
		t.Fatalf("initialized comments = %+v", initialized.Comments())
	}
	path, ok := second.Target().AnchoredPath()
	if !ok || path.Display() != "a.go" {
		t.Fatalf("canonical file path = %q, present %t", path.Display(), ok)
	}

	edited, err := initialized.BeginEditComment(second.ID())
	if err != nil {
		t.Fatal(err)
	}
	edited, err = edited.UpdateDraft("edited")
	if err != nil {
		t.Fatal(err)
	}
	edited, saved, err := edited.SaveDraft()
	if err != nil || saved != second.ID() {
		t.Fatalf("save edited seed = %d, %v", saved, err)
	}
	edited, err = edited.DeleteComment(first.ID())
	if err != nil || edited.CommentCount() != 1 {
		t.Fatalf("delete seeded comment = %d comments, %v", edited.CommentCount(), err)
	}
	exported, err := edited.PrepareJSON(DefaultExportOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := exported.Body(), "{\"comments\":[{\"target\":{\"type\":\"file\",\"path\":\"a.go\"},\"binding\":\"active\",\"comment\":\"edited\"}]}\n"; got != want {
		t.Fatalf("seed export = %q, want %q", got, want)
	}
}

func TestInitializeCommentsCancellationAndSourceMismatchLeaveReviewUnchanged(t *testing.T) {
	snapshot := testSnapshot(t, 1, "a.go")
	review := testReview(t, snapshot)
	seed, err := NewInitialComment(snapshot, ReviewCommentTarget(), "body")
	if err != nil {
		t.Fatal(err)
	}
	initialization, err := NewCommentInitialization(snapshot, []InitialComment{seed})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := errors.New("cancelled")
	next, err := review.InitializeComments(func() error { return cancelled }, initialization)
	if !errors.Is(err, cancelled) || next.CommentCount() != 0 || review.CommentCount() != 0 {
		t.Fatalf("cancelled initialization = %d comments, error %v", next.CommentCount(), err)
	}

	other := testReview(t, testSnapshot(t, 2, "a.go"))
	next, err = other.InitializeComments(nil, initialization)
	if !errors.Is(err, ErrInvalidCommentInitialization) || next.CommentCount() != 0 {
		t.Fatalf("source mismatch = %d comments, error %v", next.CommentCount(), err)
	}
}

func TestInitializeCommentsKeepsPendingFirstAndEnforcesCombinedLimit(t *testing.T) {
	snapshot := testSnapshot(t, 1, "a.go")
	nodeID, err := NewReviewNodeID("PRR_pending")
	if err != nil {
		t.Fatal(err)
	}
	databaseID, err := NewReviewDatabaseID(1)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := NewObjectID("0123456789012345678901234567890123456789")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: nodeID, DatabaseID: databaseID, Commit: commit, Body: "pending body",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = snapshot.WithPendingReview(pending)
	if err != nil {
		t.Fatal(err)
	}
	review := testReview(t, snapshot)
	seed, err := NewInitialComment(snapshot, ReviewCommentTarget(), "seed body")
	if err != nil {
		t.Fatal(err)
	}
	initialization, err := NewCommentInitialization(snapshot, []InitialComment{seed})
	if err != nil {
		t.Fatal(err)
	}
	initialized, err := review.InitializeComments(nil, initialization)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := initialized.CommentAt(0)
	second, _ := initialized.CommentAt(1)
	if first.Body().String() != "pending body" || first.ID() != 0 ||
		second.Body().String() != "seed body" || second.ID() != 1 {
		t.Fatalf("pending/seed order = %+v", initialized.Comments())
	}
	if summary := initialized.PendingReviewSummary(); summary.Dirty != 1 {
		t.Fatalf("pending summary dirty = %d, want 1", summary.Dirty)
	}

	tooMany := make([]InitialComment, MaxComments)
	for index := range tooMany {
		tooMany[index] = seed
	}
	overLimit, err := NewCommentInitialization(snapshot, tooMany)
	if err != nil {
		t.Fatal(err)
	}
	next, err := review.InitializeComments(nil, overLimit)
	if !errors.Is(err, ErrInvalidCommentInitialization) || next.CommentCount() != 1 || review.CommentCount() != 1 {
		t.Fatalf("combined limit = next %d original %d error %v", next.CommentCount(), review.CommentCount(), err)
	}
}
