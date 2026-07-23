package source

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestPreparePendingPublicationExecutionEncodesEachBatchOnce(t *testing.T) {
	checked := pendingExecutionTestChecked(t, review.PublicationEventComment)
	construction, err := PendingPublicationExecutionConstructionBytes(
		context.Background(),
		checked,
	)
	if err != nil {
		t.Fatal(err)
	}
	encodings := 0
	execution, err := preparePendingPublicationExecution(
		context.Background(),
		checked,
		func(_ func() error, effects []pendingWireEffect) ([]pendingMutationAlias, string, error) {
			encodings++
			return encodePendingMutationBatch(effects)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Close()
	if encodings != len(execution.state.batches) {
		t.Fatalf(
			"mutation encodings = %d, want one for each of %d batches",
			encodings,
			len(execution.state.batches),
		)
	}
	if execution.state.submission.payload == "" {
		t.Fatal("submission payload was not prepared before execution")
	}
	if execution.RetainedBytes() == 0 || execution.RetainedBytes() > construction {
		t.Fatalf("retained bytes = %d, construction bound = %d", execution.RetainedBytes(), construction)
	}
	for _, batch := range execution.state.batches {
		sizer := newPendingBatchSizer()
		for _, effect := range batch.effects {
			sizer, err = sizer.add(effect)
			if err != nil {
				t.Fatal(err)
			}
		}
		if got, want := int64(len(batch.payload)), sizer.payloadBytes(); got != want {
			t.Fatalf("encoded batch bytes = %d, sized = %d", got, want)
		}
	}
}

func TestPendingPublicationExecutionPreparationIsCancellable(t *testing.T) {
	checked := pendingExecutionTestChecked(t, review.PublicationEventDraft)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PendingPublicationExecutionConstructionBytes(ctx, checked); !errors.Is(err, context.Canceled) {
		t.Fatalf("construction cancellation = %v", err)
	}
	if _, err := PreparePendingPublicationExecution(ctx, checked); !errors.Is(err, context.Canceled) {
		t.Fatalf("preparation cancellation = %v", err)
	}
	calls := 0
	_, _, err := encodePendingSubmissionCancellable(func() error {
		calls++
		return context.Canceled
	}, pendingSubmissionInput{
		reviewID: "review", event: "COMMENT", clientMutationID: "attempt",
	})
	if !errors.Is(err, context.Canceled) || calls == 0 {
		t.Fatalf("wire cancellation = %v after %d checkpoints", err, calls)
	}
}

func pendingExecutionTestChecked(
	t *testing.T,
	event review.PublicationEvent,
) *review.CheckedPublication {
	t.Helper()
	sourceID, _ := review.NewSourceID(1)
	snapshot, err := review.NewSnapshot(sourceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit, _ := review.NewObjectID(strings.Repeat("a", 40))
	nodeID, _ := review.NewReviewNodeID("pending-review")
	databaseID, _ := review.NewReviewDatabaseID(7)
	pending, err := review.NewPendingReview(review.PendingReviewDescriptor{
		NodeID:       nodeID,
		DatabaseID:   databaseID,
		Commit:       commit,
		Body:         "baseline body",
		ViewerUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = snapshot.WithPendingReview(pending)
	if err != nil {
		t.Fatal(err)
	}
	current, err := review.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	policies, _ := review.NewPublicationPolicies()
	options, err := review.NewPublicationOptions(event, policies)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := review.NewObjectID(strings.Repeat("1", 40))
	head, _ := review.NewObjectID(strings.Repeat("2", 40))
	target, err := review.NewPublicationTarget(review.PublicationTargetDescriptor{
		Repository: "owner/repository", PullRequest: 7, Base: base, Head: head,
	})
	if err != nil {
		t.Fatal(err)
	}
	marker, _ := review.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	checked, err := review.TakeCheckedPublication(
		review.CheckedPublicationDescriptor{Target: target, Marker: marker},
		prepared,
	)
	if err != nil {
		t.Fatal(err)
	}
	return checked
}

func TestPendingSubmissionEnforcesExactByteBoundary(t *testing.T) {
	input := pendingSubmissionInput{
		reviewID: "review", event: "COMMENT", clientMutationID: "client",
	}
	_, encoded, err := encodePendingSubmission(input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"variables":{"v0":{"pullRequestReviewId":"review","event":"COMMENT",` +
		`"clientMutationId":"client"}},"query":` +
		`"mutation($v0:SubmitPullRequestReviewInput!){` +
		`m0:submitPullRequestReview(input:$v0){clientMutationId}}"}`
	if encoded != want {
		t.Fatalf("submission wire payload = %s", encoded)
	}
	padding := review.MaxPendingMutationBatchBytes - len(encoded)
	input.reviewID += strings.Repeat("x", padding)
	_, encoded, err = encodePendingSubmission(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != review.MaxPendingMutationBatchBytes {
		t.Fatalf("encoded bytes=%d", len(encoded))
	}
	input.reviewID += "x"
	if _, _, err := encodePendingSubmission(input); err == nil {
		t.Fatal("boundary+1 submission unexpectedly encoded")
	}

	effects := make([]pendingWireEffect, review.MaxPendingMutationBatch+1)
	if _, _, err := encodePendingMutationBatch(effects); err == nil {
		t.Fatal("oversized mutation count unexpectedly encoded")
	}
}

func TestPendingMutationKindsHaveClosedWireOperations(t *testing.T) {
	tests := []struct {
		kind      review.PendingMutationKind
		typeName  string
		fieldName string
	}{
		{review.PendingMutationReviewBody, updatePullRequestReviewInput, updatePullRequestReviewField},
		{review.PendingMutationUpdateComment, "UpdatePullRequestReviewCommentInput", "updatePullRequestReviewComment"},
		{review.PendingMutationDeleteComment, "DeletePullRequestReviewCommentInput", "deletePullRequestReviewComment"},
		{review.PendingMutationAddComment, "AddPullRequestReviewThreadInput", "addPullRequestReviewThread"},
	}
	for _, test := range tests {
		operation, err := pendingMutationOperationFor(test.kind)
		if err != nil || operation.typeName != test.typeName || operation.field != test.fieldName {
			t.Fatalf("operation %d = (%q, %q, %v)", test.kind, operation.typeName, operation.field, err)
		}
	}
	if _, err := pendingMutationOperationFor(0); err == nil {
		t.Fatal("invalid mutation kind has a wire operation")
	}
}

func TestPendingJSONStringBytePreflightMatchesEncodingJSON(t *testing.T) {
	for _, value := range []string{
		"plain",
		"quote\"slash\\",
		"control\x00\b\f\n\r\t",
		"html<>&",
		"multibyte-日本語-\u2028-\u2029",
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := pendingJSONStringBytes(value)
		if err != nil {
			t.Fatal(err)
		}
		if bytes != int64(len(encoded)) {
			t.Fatalf("encoded bytes for %q = %d, want %d", value, bytes, len(encoded))
		}
	}
	if _, err := pendingJSONStringBytes(string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 string passed wire preflight")
	}
}

func TestPendingMutationResponseRequiresExactAliasClientIdentities(t *testing.T) {
	aliases := []pendingMutationAlias{{name: "m0", client: "client-0"}}
	tests := []struct {
		name    string
		payload any
		valid   bool
	}{
		{
			name: "exact",
			payload: map[string]any{
				"data":   map[string]any{"m0": map[string]any{"clientMutationId": "client-0"}},
				"errors": []any{},
			},
			valid: true,
		},
		{
			name:    "missing",
			payload: map[string]any{"data": map[string]any{}, "errors": []any{}},
		},
		{
			name: "wrong client",
			payload: map[string]any{
				"data":   map[string]any{"m0": map[string]any{"clientMutationId": "other"}},
				"errors": []any{},
			},
		},
		{
			name: "graphql error",
			payload: map[string]any{
				"data":   map[string]any{"m0": map[string]any{"clientMutationId": "client-0"}},
				"errors": []any{map[string]any{"message": "partial"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.payload)
			if err != nil {
				t.Fatal(err)
			}
			err = validatePendingMutationResponse(t.Context(), encoded, aliases)
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && err == nil {
				t.Fatal("response unexpectedly accepted")
			}
		})
	}
}

func TestPendingMutationResponseFramerRejectsAmbiguousStructures(t *testing.T) {
	aliases := []pendingMutationAlias{{name: "m0", client: "client-0"}}
	valid := `{"data":{"m0":{"clientMutationId":"client-0"}},"errors":[]}`
	tests := []struct {
		name    string
		encoded string
	}{
		{name: "duplicate alias", encoded: `{"data":{"m0":{"clientMutationId":"client-0"},"m0":{"clientMutationId":"client-0"}},"errors":[]}`},
		{name: "duplicate client", encoded: `{"data":{"m0":{"clientMutationId":"client-0","clientMutationId":"client-0"}},"errors":[]}`},
		{name: "errors object", encoded: `{"data":{"m0":{"clientMutationId":"client-0"}},"errors":{}}`},
		{name: "data array", encoded: `{"data":[],"errors":[]}`},
		{name: "alias null", encoded: `{"data":{"m0":null},"errors":[]}`},
		{name: "root array", encoded: `[]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePendingMutationResponse(
				t.Context(),
				[]byte(test.encoded),
				aliases,
			); err == nil {
				t.Fatal("ambiguous response unexpectedly accepted")
			}
		})
	}
	if err := validatePendingMutationResponse(t.Context(), []byte(valid), aliases); err != nil {
		t.Fatalf("canonical response rejected: %v", err)
	}
}

func TestPendingMutationResponseFramerPropagatesCancellation(t *testing.T) {
	cause := errors.New("stop pending mutation response")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	err := validatePendingMutationResponse(
		ctx,
		[]byte(`{"data":{"m0":{"clientMutationId":"client-0"}},"errors":[]}`),
		[]pendingMutationAlias{{name: "m0", client: "client-0"}},
	)
	if !errors.Is(err, cause) {
		t.Fatalf("pending mutation response cancellation = %v", err)
	}
}

func TestClassifyPendingBodyIgnoresOnlyExactTerminalMarkers(t *testing.T) {
	desired := "changed\n\n<!-- rvw-draft-comment:v1:0123456789abcdef0123456789abcdef -->"
	if state := classifyPendingBody(desired, desired, "before"); state != pendingEffectCommitted {
		t.Fatalf("exact state=%d", state)
	}
	if state := classifyPendingBody("before", desired, "before"); state != pendingEffectAbsent {
		t.Fatalf("baseline state=%d", state)
	}
	if state := classifyPendingBody(
		"changed\n\n<!-- rvw-draft-comment:v1:0123456789abcdef0123456789abcdeg -->",
		desired,
		"before",
	); state != pendingEffectUnknown {
		t.Fatalf("malformed marker state=%d", state)
	}
	if state := classifyPendingBody("changed", desired, "changed"); state != pendingEffectAbsent {
		t.Fatalf("missing marker state=%d", state)
	}
	if state := classifyPendingBody(
		"changed\n\n<!-- rvw-draft-comment:v1:fedcba9876543210fedcba9876543210 -->",
		desired,
		"changed",
	); state != pendingEffectUnknown {
		t.Fatalf("foreign marker state=%d", state)
	}
}

func TestPendingAdditionTargetMatchIncludesPlacement(t *testing.T) {
	path, _ := review.NewPath([]byte("reviewed.go"))
	commit, _ := review.NewObjectID(strings.Repeat("a", 40))
	line, _ := review.NewLineNumber(12)
	target, err := review.NewPendingCommentTarget(review.PendingCommentTargetDescriptor{
		Path: path, Commit: commit, OriginalCommit: commit,
		Line: new(line), Subject: review.PendingSubjectLine, Side: review.LineNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !target.MatchesPendingAddition(target) {
		t.Fatal("identical addition targets did not match")
	}
	otherLine, _ := review.NewLineNumber(13)
	other, err := review.NewPendingCommentTarget(review.PendingCommentTargetDescriptor{
		Path: path, Commit: commit, OriginalCommit: commit,
		Line: new(otherLine), Subject: review.PendingSubjectLine, Side: review.LineNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.MatchesPendingAddition(other) {
		t.Fatal("different line targets unexpectedly matched")
	}

	node, _ := review.NewCommentNodeID("comment")
	databaseID, _ := review.NewCommentDatabaseID(1)
	desired := "new comment\n\n<!-- rvw-draft-comment:v1:0123456789abcdef0123456789abcdef -->"
	comment, err := review.NewPendingComment(review.PendingCommentDescriptor{
		NodeID: node, DatabaseID: databaseID, Body: desired, Target: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state := classifyPendingAddition(
		pendingMarkerMatch{comment: comment, count: 1},
		desired,
		target,
	); state != pendingEffectCommitted {
		t.Fatalf("exact addition state=%d", state)
	}
	if state := classifyPendingAddition(
		pendingMarkerMatch{comment: comment, count: 2},
		desired,
		target,
	); state != pendingEffectUnknown {
		t.Fatalf("duplicate addition state=%d", state)
	}
	if state := classifyPendingAddition(
		pendingMarkerMatch{comment: comment, count: 1},
		"different\n\n<!-- rvw-draft-comment:v1:0123456789abcdef0123456789abcdef -->",
		target,
	); state != pendingEffectUnknown {
		t.Fatalf("divergent addition state=%d", state)
	}
}
