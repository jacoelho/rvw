package source

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestPendingMutationBatchEnforcesCountAndExactByteBoundary(t *testing.T) {
	effect := pendingWireEffect{
		field:    "updatePullRequestReview",
		typeName: "UpdatePullRequestReviewInput",
		input: map[string]any{
			"pullRequestReviewId": "review",
			"body":                "",
			"clientMutationId":    "client",
		},
	}
	_, encoded, err := encodePendingMutationBatch([]pendingWireEffect{effect})
	if err != nil {
		t.Fatal(err)
	}
	padding := review.MaxPendingMutationBatchBytes - len(encoded)
	effect.input["body"] = strings.Repeat("x", padding)
	_, encoded, err = encodePendingMutationBatch([]pendingWireEffect{effect})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != review.MaxPendingMutationBatchBytes {
		t.Fatalf("encoded bytes=%d", len(encoded))
	}
	if end := pendingBatchEnd([]pendingWireEffect{effect}, 0); end != 1 {
		t.Fatalf("exact boundary end=%d", end)
	}
	effect.input["body"] = strings.Repeat("x", padding+1)
	if end := pendingBatchEnd([]pendingWireEffect{effect}, 0); end != 0 {
		t.Fatalf("boundary+1 end=%d", end)
	}

	effect.input["body"] = ""
	effects := make([]pendingWireEffect, review.MaxPendingMutationBatch+1)
	for index := range effects {
		input := map[string]any{
			"pullRequestReviewId": "review",
			"body":                "",
			"clientMutationId":    "client-" + string(rune('a'+index)),
		}
		effects[index] = pendingWireEffect{
			field: "updatePullRequestReview", typeName: "UpdatePullRequestReviewInput", input: input,
		}
	}
	if end := pendingBatchEnd(effects, 0); end != review.MaxPendingMutationBatch {
		t.Fatalf("count boundary end=%d", end)
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
			err = validatePendingMutationResponse(encoded, aliases)
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && err == nil {
				t.Fatal("response unexpectedly accepted")
			}
		})
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
	if !pendingAdditionTargetsEqual(target, target) {
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
	if pendingAdditionTargetsEqual(target, other) {
		t.Fatal("different line targets unexpectedly matched")
	}
}
