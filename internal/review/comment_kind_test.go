package review

import (
	"strings"
	"testing"
	"unsafe"
)

func testCommentKinds() CommentKinds {
	kinds, err := NewCommentKinds("issue", "note", "praise", "suggestion")
	if err != nil {
		panic(err)
	}
	return kinds
}

func testCommentPolicies() CommentPolicies {
	policies, err := NewCommentPolicies()
	if err != nil {
		panic(err)
	}
	return policies
}

func testReviewRequest(snapshot Snapshot) ReviewRequest {
	return ReviewRequest{
		Snapshot: snapshot, CommentKinds: testCommentKinds(),
		CommentPolicies: testCommentPolicies(),
	}
}

func TestCommentKindsNormalizeSortAndRejectInvalidLabels(t *testing.T) {
	kinds, err := NewCommentKinds(" suggestion ", "issue", "praise", "note")
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []string{"issue", "note", "praise", "suggestion"} {
		kind, ok := kinds.At(index)
		if !ok || kind.String() != want {
			t.Fatalf("kind %d = %q, %t; want %q, true", index, kind.String(), ok, want)
		}
	}
	for _, labels := range [][]string{
		nil,
		{"note", " note "},
		{""},
		{"bad\nlabel"},
		{strings.Repeat("x", MaxCommentKindBytes+1)},
	} {
		if _, err := NewCommentKinds(labels...); err == nil {
			t.Fatalf("NewCommentKinds(%q) succeeded", labels)
		}
	}
}

func TestCommentKindAccountingCoversCompiledLayout(t *testing.T) {
	if compiled := int64(unsafe.Sizeof(CommentKind{})); compiled > commentKindMetadataBytes {
		t.Fatalf("comment kind metadata charge = %d, compiled size = %d", commentKindMetadataBytes, compiled)
	}
	kinds := testCommentKinds()
	if kinds.retained() < int64(cap(kinds.values))*commentKindMetadataBytes {
		t.Fatalf("kind retained bytes = %d", kinds.retained())
	}
}

func TestCommentKindEncodedPrefixAccountingCoversWorstCase(t *testing.T) {
	wantMaximum := 1 + 4*MaxCommentKindBytes + 2
	if MaxEncodedCommentKindPrefixBytes != wantMaximum {
		t.Fatalf(
			"maximum encoded prefix = %d, want %d",
			MaxEncodedCommentKindPrefixBytes,
			wantMaximum,
		)
	}
	label := strings.Repeat("\u061c", MaxCommentKindBytes/2)
	kinds, err := NewCommentKinds(label)
	if err != nil {
		t.Fatal(err)
	}
	kind, _ := kinds.At(0)
	policies, _ := NewCommentPolicies(CommentTypePrefixForGitHub)
	wire, err := encodeCommentWire(commentContent{kind: kind}, policies)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != MaxEncodedCommentKindPrefixBytes {
		t.Fatalf("worst-case prefix bytes = %d, want %d", len(wire), MaxEncodedCommentKindPrefixBytes)
	}
	maximumBody := strings.Repeat("b", int(MaxCommentBodyBytes)-len(wire))
	if _, err := encodeCommentWire(commentContent{kind: kind, body: maximumBody}, policies); err != nil {
		t.Fatalf("exact maximum wire rejected: %v", err)
	}
	if _, err := encodeCommentWire(commentContent{kind: kind, body: maximumBody + "b"}, policies); err == nil {
		t.Fatal("oversized encoded wire was accepted")
	}
}

func TestDraftKindCyclesLexicallyWithWrap(t *testing.T) {
	current, err := NewReview(testReviewRequest(testSnapshot(t, 1, "one.go")))
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.BeginDraft(ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"note", "praise", "suggestion", "issue"} {
		current, err = current.CycleDraftKind(NavigateNext)
		if err != nil {
			t.Fatal(err)
		}
		draft, _ := current.Draft()
		if draft.Kind().String() != want {
			t.Fatalf("kind = %q, want %q", draft.Kind(), want)
		}
	}
	current, err = current.CycleDraftKind(NavigatePrevious)
	if err != nil {
		t.Fatal(err)
	}
	draft, _ := current.Draft()
	if draft.Kind().String() != "suggestion" {
		t.Fatalf("previous wrapped to %q", draft.Kind())
	}
}

func TestCommentPoliciesRequireExplicitConstruction(t *testing.T) {
	if (CommentPolicies{}).Ready() {
		t.Fatal("zero comment policies are ready")
	}
	disabled, err := NewCommentPolicies()
	if err != nil || !disabled.Ready() || disabled.Has(CommentTypePrefixForGitHub) {
		t.Fatalf("disabled policies = %#v, %v", disabled, err)
	}
	enabled, err := NewCommentPolicies(CommentTypePrefixForGitHub)
	if err != nil || !enabled.Has(CommentTypePrefixForGitHub) {
		t.Fatalf("enabled policies = %#v, %v", enabled, err)
	}
}
