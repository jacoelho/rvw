package change

import (
	"strings"
	"testing"
)

func testReviewRequest(snapshot Snapshot) ReviewRequest {
	return ReviewRequest{Snapshot: snapshot}
}

func TestCommentWirePreservesPlainBody(t *testing.T) {
	for _, body := range []string{"body", "[issue] body", "[custom] body"} {
		wire, err := encodeCommentWire(commentContent{body: body})
		if err != nil {
			t.Fatal(err)
		}
		if wire != body {
			t.Fatalf("wire = %q, want %q", wire, body)
		}
		if decoded := decodeOwnedCommentWire(wire); decoded.body != body {
			t.Fatalf("decoded = %#v", decoded)
		}
	}
}

func TestPendingImportTreatsFormerCategoryPrefixAsBody(t *testing.T) {
	marker := draftCommentMarkerPrefix + strings.Repeat("a", 32) + markerSuffix
	for _, wire := range []string{
		"[suggestion] body\n\n" + marker,
		"[suggestion] body",
	} {
		current, err := NewReview(ReviewRequest{Snapshot: pendingWireSnapshot(t, wire)})
		if err != nil {
			t.Fatal(err)
		}
		comment, ok := current.CommentAt(0)
		if !ok || comment.Body().String() != "[suggestion] body" {
			t.Fatalf("comment = %#v, %t", comment, ok)
		}
	}
}

func TestPublicationAndPendingUpdatesUsePlainBodies(t *testing.T) {
	current, err := NewReview(ReviewRequest{Snapshot: testSnapshot(t, 1, "one.go")})
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.BeginDraft(ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("body")
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = current.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	publicationPolicies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventComment, publicationPolicies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SummaryBody() != "body" {
		t.Fatalf("summary = %q", prepared.SummaryBody())
	}

	pendingCurrent, err := NewReview(ReviewRequest{Snapshot: pendingWireSnapshot(
		t,
		"[suggestion] body\n\n"+draftCommentMarkerPrefix+
			strings.Repeat("b", 32)+markerSuffix,
	)})
	if err != nil {
		t.Fatal(err)
	}
	pendingOptions, _ := NewPublicationOptions(PublicationEventDraft, publicationPolicies)
	clean, err := pendingCurrent.PreparePublication(pendingOptions)
	if err != nil {
		t.Fatal(err)
	}
	cleanPlan, _ := clean.PendingPublication()
	if cleanPlan.MutationCount() != 1 {
		t.Fatalf("clean mutation count = %d, want review-body mutation only", cleanPlan.MutationCount())
	}
	comment, _ := pendingCurrent.CommentAt(0)
	pendingCurrent, err = pendingCurrent.BeginEditComment(comment.ID())
	if err != nil {
		t.Fatal(err)
	}
	pendingCurrent, err = pendingCurrent.UpdateDraft("body")
	if err != nil {
		t.Fatal(err)
	}
	pendingCurrent, _, err = pendingCurrent.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	dirty, err := pendingCurrent.PreparePublication(pendingOptions)
	if err != nil {
		t.Fatal(err)
	}
	dirtyPlan, _ := dirty.PendingPublication()
	mutations := dirtyPlan.Mutations(1, 1)
	mutation, ok := PendingMutation{}, len(mutations) == 1
	if ok {
		mutation = mutations[0]
	}
	if dirtyPlan.MutationCount() != 2 || !ok || mutation.Body() != "body" ||
		mutation.Baseline() != "[suggestion] body" {
		t.Fatalf("dirty mutation = %#v, %t; count = %d", mutation, ok, dirtyPlan.MutationCount())
	}
}

func TestPublicationSummaryAddsTargetWithoutCategory(t *testing.T) {
	current, err := NewReview(ReviewRequest{Snapshot: testSnapshot(t, 1, "one.go")})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := FileCommentTarget(0)
	current, err = current.BeginDraft(target)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("body")
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = current.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	publicationPolicies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventComment, publicationPolicies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SummaryBody() != "`one.go`: body" {
		t.Fatalf("summary = %q", prepared.SummaryBody())
	}
}

func pendingWireSnapshot(t *testing.T, wire string) Snapshot {
	t.Helper()
	return pendingWireSnapshotForSource(t, 1, wire)
}

func pendingWireSnapshotForSource(t *testing.T, source uint64, wire string) Snapshot {
	t.Helper()
	pending, _ := pendingTestSnapshot(t).PendingReview()
	remote := pending.comments[0]
	comment, err := NewPendingComment(PendingCommentDescriptor{
		NodeID: remote.nodeID, DatabaseID: remote.databaseID, Body: wire,
		Target: remote.target, Capabilities: remote.capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	owned, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: pending.nodeID, DatabaseID: pending.databaseID, Commit: pending.commit,
		Comments: []PendingComment{comment}, ViewerUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t, source, "one.go")
	snapshot, err = snapshot.WithPendingReview(owned)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
