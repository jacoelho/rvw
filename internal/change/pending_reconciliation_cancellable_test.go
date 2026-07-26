package change

import (
	"errors"
	"testing"
)

func TestPendingReconciliationConstructionAndCancellation(t *testing.T) {
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
	pending, ok := prepared.PendingPublication()
	if !ok {
		t.Fatal("pending publication is unavailable")
	}
	remote := pending.Baseline()
	construction := prepared.PendingReconciliationConstructionBytes(remote)
	if construction < prepared.RetainedBytes()+remote.RetainedBytes() {
		t.Fatalf(
			"construction bytes = %d, inputs = %d",
			construction,
			prepared.RetainedBytes()+remote.RetainedBytes(),
		)
	}

	cause := errors.New("cancel pending reconciliation")
	if _, err := prepared.ReconcilePendingPublicationCancellable(
		func() error { return cause },
		remote,
	); !errors.Is(err, cause) {
		t.Fatalf("reconciliation error = %v, want %v", err, cause)
	}

	reconciled, err := prepared.ReconcilePendingPublication(remote)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.RetainedBytes() == 0 || reconciled.RetainedBytes() > construction {
		t.Fatalf(
			"reconciled retained bytes = %d, construction = %d",
			reconciled.RetainedBytes(),
			construction,
		)
	}
}
