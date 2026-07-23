package review

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func prefixedReviewRequest(snapshot Snapshot) ReviewRequest {
	policies, err := NewCommentPolicies(CommentTypePrefixForGitHub)
	if err != nil {
		panic(err)
	}
	return ReviewRequest{
		Snapshot: snapshot, CommentKinds: testCommentKinds(), CommentPolicies: policies,
	}
}

func TestCommentWireEscapesAndDecodesConfiguredKinds(t *testing.T) {
	kinds, err := NewCommentKinds(`a]b\c`)
	if err != nil {
		t.Fatal(err)
	}
	policies, _ := NewCommentPolicies(CommentTypePrefixForGitHub)
	kind, _ := kinds.At(0)
	wire, err := encodeCommentWire(commentContent{kind: kind, body: "body"}, policies)
	if err != nil {
		t.Fatal(err)
	}
	if wire != `[a\]b\\c] body` {
		t.Fatalf("wire = %q", wire)
	}
	if decoded := decodeOwnedCommentWire(wire, kinds); decoded.kind != kind || decoded.body != "body" {
		t.Fatalf("decoded = %#v", decoded)
	}
	for _, malformed := range []string{
		`[a\qb] body`, `[missing] body`, `[a\]b\\c]body`, `ordinary body`,
	} {
		decoded := decodeOwnedCommentWire(malformed, kinds)
		if decoded.kind != kind || decoded.body != malformed {
			t.Fatalf("malformed %q decoded as %#v", malformed, decoded)
		}
	}
}

func TestCommentWireCanonicalizesEveryUnsafeKindScalar(t *testing.T) {
	unsafeScalars := []rune{
		'\u061c',
		'\u200e', '\u200f',
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069',
		utf8.RuneError,
	}
	policies, _ := NewCommentPolicies(CommentTypePrefixForGitHub)
	for _, character := range unsafeScalars {
		t.Run(fmt.Sprintf("U+%04X", character), func(t *testing.T) {
			label := "x" + string(character) + "y"
			kinds, err := NewCommentKinds("first", label)
			if err != nil {
				t.Fatal(err)
			}
			kind, ok := kinds.Lookup(label)
			if !ok || kind.String() != label {
				t.Fatalf("configured kind = %q, %t", kind.String(), ok)
			}
			wire, err := encodeCommentWire(commentContent{kind: kind, body: "body"}, policies)
			if err != nil {
				t.Fatal(err)
			}
			wantWire := fmt.Sprintf(`[x\u{%04X}y] body`, character)
			if wire != wantWire || strings.ContainsRune(wire, character) {
				t.Fatalf("wire = %q, want %q", wire, wantWire)
			}
			if decoded := decodeOwnedCommentWire(wire, kinds); decoded.kind != kind || decoded.body != "body" {
				t.Fatalf("decoded = %#v", decoded)
			}

			raw := "[" + label + "] body"
			fallback, _ := kinds.Lookup("first")
			if decoded := decodeOwnedCommentWire(raw, kinds); decoded.kind != fallback || decoded.body != raw {
				t.Fatalf("raw unsafe wire %q decoded as %#v", raw, decoded)
			}
		})
	}
}

func TestCommentWireRejectsNoncanonicalUnsafeEscapes(t *testing.T) {
	for _, test := range []struct {
		name   string
		label  string
		escape string
	}{
		{name: "lowercase hex", label: "x\u202ey", escape: `\u{202e}`},
		{name: "short hex", label: "x\u061cy", escape: `\u{61C}`},
		{name: "long hex", label: "x\u202ey", escape: `\u{0202E}`},
		{name: "non-unsafe scalar", label: "xAy", escape: `\u{0041}`},
		{name: "surrogate", label: "unused", escape: `\u{D800}`},
		{name: "truncated", label: "x\u202ey", escape: `\u{202E`},
	} {
		t.Run(test.name, func(t *testing.T) {
			kinds, err := NewCommentKinds("first", test.label)
			if err != nil {
				t.Fatal(err)
			}
			wire := "[x" + test.escape + "y] body"
			fallback, _ := kinds.Lookup("first")
			if decoded := decodeOwnedCommentWire(wire, kinds); decoded.kind != fallback || decoded.body != wire {
				t.Fatalf("noncanonical wire %q decoded as %#v", wire, decoded)
			}
		})
	}
}

func TestCommentWireLeavesSafeBoundaryUnicodeUnchanged(t *testing.T) {
	policies, _ := NewCommentPolicies(CommentTypePrefixForGitHub)
	for _, character := range []rune{
		'\u061b', '\u061d',
		'\u200d', '\u2010',
		'\u2029', '\u202f',
		'\u2065', '\u206a',
	} {
		t.Run(fmt.Sprintf("U+%04X", character), func(t *testing.T) {
			label := "x" + string(character) + "y"
			kinds, err := NewCommentKinds("first", label)
			if err != nil {
				t.Fatal(err)
			}
			kind, _ := kinds.Lookup(label)
			wire, err := encodeCommentWire(commentContent{kind: kind, body: "body"}, policies)
			if err != nil {
				t.Fatal(err)
			}
			want := "[" + label + "] body"
			if wire != want {
				t.Fatalf("wire = %q, want %q", wire, want)
			}
			if decoded := decodeOwnedCommentWire(wire, kinds); decoded.kind != kind || decoded.body != "body" {
				t.Fatalf("decoded = %#v", decoded)
			}
		})
	}
}

func TestCommentWireLiteralEscapeLikeLabelRemainsUnambiguous(t *testing.T) {
	label := `x\u{202E}y`
	kinds, err := NewCommentKinds("first", label)
	if err != nil {
		t.Fatal(err)
	}
	kind, _ := kinds.Lookup(label)
	policies, _ := NewCommentPolicies(CommentTypePrefixForGitHub)
	wire, err := encodeCommentWire(commentContent{kind: kind, body: "body"}, policies)
	if err != nil {
		t.Fatal(err)
	}
	if wire != `[x\\u{202E}y] body` {
		t.Fatalf("wire = %q", wire)
	}
	if decoded := decodeOwnedCommentWire(wire, kinds); decoded.kind != kind || decoded.body != "body" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCommentWireUnsafeKindRemainsReadyAcrossPendingImportAndReload(t *testing.T) {
	for _, label := range []string{"kind\u202e", "kind\u2066", "kind\ufffd", `kind\u{202E}`} {
		t.Run(label, func(t *testing.T) {
			kinds, err := NewCommentKinds("first", label)
			if err != nil {
				t.Fatal(err)
			}
			kind, _ := kinds.Lookup(label)
			policies, _ := NewCommentPolicies(CommentTypePrefixForGitHub)
			wire, err := encodeCommentWire(commentContent{kind: kind, body: "body"}, policies)
			if err != nil {
				t.Fatal(err)
			}
			if strings.ContainsAny(wire, "\u202e\u2066\ufffd") {
				t.Fatalf("encoded pending wire contains an unsafe scalar: %q", wire)
			}
			marker := draftCommentMarkerPrefix + strings.Repeat("c", 32) + markerSuffix
			owned := wire + "\n\n" + marker
			request := ReviewRequest{
				Snapshot:     pendingWireSnapshotForSource(t, 1, owned),
				CommentKinds: kinds, CommentPolicies: policies,
			}
			current, err := NewReview(request)
			if err != nil {
				t.Fatal(err)
			}
			if summary := current.PendingReviewSummary(); summary.Status != PendingReviewReady {
				t.Fatalf("captured pending status = %s", summary.Status)
			}
			comment, ok := current.CommentAt(0)
			if !ok || comment.Kind() != kind || comment.Body().String() != "body" {
				t.Fatalf("imported comment = %#v, %t", comment, ok)
			}

			next, _, err := current.ReplaceSnapshot(pendingWireSnapshotForSource(t, 2, owned))
			if err != nil {
				t.Fatal(err)
			}
			if summary := next.PendingReviewSummary(); summary.Status != PendingReviewReady {
				t.Fatalf("reloaded pending status = %s", summary.Status)
			}
			comment, ok = next.CommentAt(0)
			if !ok || comment.Kind() != kind || comment.Body().String() != "body" {
				t.Fatalf("reloaded comment = %#v, %t", comment, ok)
			}
		})
	}
}

func TestPendingImportDecodesKindsOnlyBehindRecognizedMarkers(t *testing.T) {
	marker := draftCommentMarkerPrefix + strings.Repeat("a", 32) + markerSuffix
	tests := []struct {
		name     string
		wire     string
		wantKind string
		wantBody string
	}{
		{
			name: "owned", wire: "[suggestion] body\n\n" + marker,
			wantKind: "suggestion", wantBody: "body",
		},
		{
			name: "unmarked lookalike", wire: "[suggestion] body",
			wantKind: "issue", wantBody: "[suggestion] body",
		},
		{
			name: "unknown owned prefix", wire: "[unknown] body\n\n" + marker,
			wantKind: "issue", wantBody: "[unknown] body",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current, err := NewReview(prefixedReviewRequest(pendingWireSnapshot(t, test.wire)))
			if err != nil {
				t.Fatal(err)
			}
			comment, ok := current.CommentAt(0)
			if !ok || comment.Kind().String() != test.wantKind ||
				comment.Body().String() != test.wantBody {
				t.Fatalf("comment = %#v, %t", comment, ok)
			}
		})
	}
}

func TestPublicationPrefixesKindsAndPendingUpdatesUseExactWireBaseline(t *testing.T) {
	current, err := NewReview(prefixedReviewRequest(testSnapshot(t, 1, "one.go")))
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.BeginDraft(ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		current, err = current.CycleDraftKind(NavigateNext)
		if err != nil {
			t.Fatal(err)
		}
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
	comments := prepared.Comments(0, 1)
	if len(comments) != 0 || prepared.SummaryBody() != "[suggestion] body" {
		t.Fatalf("publication comments = %v, summary = %q", comments, prepared.SummaryBody())
	}

	pendingCurrent, err := NewReview(prefixedReviewRequest(pendingWireSnapshot(
		t,
		"[suggestion] body\n\n"+draftCommentMarkerPrefix+
			strings.Repeat("b", 32)+markerSuffix,
	)))
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
	pendingCurrent, err = pendingCurrent.CycleDraftKind(NavigateNext)
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
	if dirtyPlan.MutationCount() != 2 || !ok || mutation.Body() != "[issue] body" ||
		mutation.Baseline() != "[suggestion] body" {
		t.Fatalf("dirty mutation = %#v, %t; count = %d", mutation, ok, dirtyPlan.MutationCount())
	}
}

func TestPublicationSummaryPrefixPrecedesTargetContext(t *testing.T) {
	current, err := NewReview(prefixedReviewRequest(testSnapshot(t, 1, "one.go")))
	if err != nil {
		t.Fatal(err)
	}
	target, _ := FileCommentTarget(0)
	current, err = current.BeginDraft(target)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		current, err = current.CycleDraftKind(NavigateNext)
		if err != nil {
			t.Fatal(err)
		}
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
	if prepared.SummaryBody() != "[suggestion] `one.go`: body" {
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
