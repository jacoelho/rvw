package review

import (
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestDraftBodyOwnershipTransitionsPreserveExplicitCopyContracts(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	current, err := current.BeginDraft(ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}

	clonedSource := string([]byte("cloned body"))
	cloned, err := current.UpdateDraft(clonedSource)
	if err != nil {
		t.Fatal(err)
	}
	clonedDraft, _ := cloned.Draft()
	if clonedDraft.Body() != clonedSource ||
		unsafe.StringData(clonedDraft.Body()) == unsafe.StringData(clonedSource) {
		t.Fatal("UpdateDraft did not preserve its defensive-copy contract")
	}

	ownedSource := string([]byte("owned body"))
	materialized, err := MaterializeDraftBody([]rune(ownedSource))
	if err != nil {
		t.Fatal(err)
	}
	owned, err := current.OwnDraftBody(materialized)
	if err != nil {
		t.Fatal(err)
	}
	ownedDraft, _ := owned.Draft()
	if ownedDraft.Body() != ownedSource ||
		unsafe.StringData(ownedDraft.Body()) != unsafe.StringData(materialized.String()) {
		t.Fatal("OwnDraftBody copied or changed the exact-sized transfer")
	}
	wantRetained := current.retainedBytes + int64(len(ownedSource))
	if owned.retainedBytes != wantRetained {
		t.Fatalf("owned retained bytes = %d, want %d", owned.retainedBytes, wantRetained)
	}

	cleared, taken, err := owned.TakeDraftBody()
	if err != nil {
		t.Fatal(err)
	}
	clearedDraft, ok := cleared.Draft()
	if !ok || clearedDraft.Body() != "" || clearedDraft.Kind() != ownedDraft.Kind() ||
		clearedDraft.Target() != ownedDraft.Target() {
		t.Fatalf("cleared draft metadata = (%+v, %t), want preserved empty draft", clearedDraft, ok)
	}
	if taken.String() != ownedSource ||
		unsafe.StringData(taken.String()) != unsafe.StringData(materialized.String()) {
		t.Fatal("TakeDraftBody copied or changed the transferred string")
	}
	if cleared.retainedBytes != current.retainedBytes {
		t.Fatalf(
			"cleared retained bytes = %d, want %d",
			cleared.retainedBytes,
			current.retainedBytes,
		)
	}
}

func TestDraftBodyOwnershipTransitionsRejectInvalidStateAtomically(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	materialized, err := MaterializeDraftBody([]rune("body"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.OwnDraftBody(materialized); !errors.Is(err, ErrInvalidReviewTransition) {
		t.Fatalf("missing-draft OwnDraftBody error = %v", err)
	}
	if _, _, err := current.TakeDraftBody(); !errors.Is(err, ErrInvalidReviewTransition) {
		t.Fatalf("missing-draft TakeDraftBody error = %v", err)
	}

	current, err = current.BeginDraft(ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	before := current.retainedBytes
	if _, err := MaterializeDraftBody([]rune("unsafe\u202e")); !errors.Is(err, ErrInvalidReviewTransition) {
		t.Fatalf("unsafe materialization error = %v", err)
	}
	if next, err := current.OwnDraftBody(OwnedDraftBody{}); !errors.Is(err, ErrInvalidReviewTransition) || next.retainedBytes != before {
		t.Fatalf("invalid ownership result = retained %d error %v", next.retainedBytes, err)
	}

	operation, _ := NewOperationID(7)
	started, _, err := current.StartSaveComment(operation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := started.OwnDraftBody(materialized); !errors.Is(err, ErrInvalidReviewTransition) {
		t.Fatalf("active-mutation OwnDraftBody error = %v", err)
	}
	if _, _, err := started.TakeDraftBody(); !errors.Is(err, ErrInvalidReviewTransition) {
		t.Fatalf("active-mutation TakeDraftBody error = %v", err)
	}
}

func TestMaterializeDraftBodyDoesNotRetainSubstringBacking(t *testing.T) {
	large := strings.Repeat("x", int(MaxCommentBodyBytes))
	substring := large[:1]
	materialized, err := MaterializeDraftBody([]rune(substring))
	if err != nil {
		t.Fatal(err)
	}
	if materialized.String() != substring {
		t.Fatalf("materialized body = %q, want %q", materialized.String(), substring)
	}
	if unsafe.StringData(materialized.String()) == unsafe.StringData(substring) {
		t.Fatal("materialized body retained the substring backing")
	}
}
