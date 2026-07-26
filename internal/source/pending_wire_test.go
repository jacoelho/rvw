package source

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func TestPendingJSONStringSizingIsCancellable(t *testing.T) {
	checks := 0
	_, err := pendingJSONStringBytesCancellable(func() error {
		checks++
		if checks == 2 {
			return context.Canceled
		}
		return nil
	}, strings.Repeat("x", 2*sourceBuilderCancellationBytes))
	if !errors.Is(err, context.Canceled) || checks != 2 {
		t.Fatalf("sizing cancellation = %v after %d checks", err, checks)
	}
}

func TestPendingSubmissionResponseHasClosedClassification(t *testing.T) {
	tests := []struct {
		name      string
		state     string
		want      pendingSubmissionClassification
		wantError bool
	}{
		{name: "committed", state: "COMMENTED", want: pendingSubmissionCommitted},
		{name: "absent", state: githubPending, want: pendingSubmissionAbsent},
		{name: "ambiguous", state: "DISMISSED", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded := []byte(
				`{"body":"expected","node_id":"review","state":"` + test.state + `","id":7}`,
			)
			got, err := classifyPendingSubmissionResponse(
				t.Context(),
				encoded,
				change.ReviewNodeID("review"),
				7,
				"expected",
				"COMMENTED",
			)
			if (err != nil) != test.wantError || got != test.want {
				t.Fatalf("classification = (%d, %v), want (%d, error %t)", got, err, test.want, test.wantError)
			}
		})
	}
}

func TestPendingSubmissionResponseFramerRejectsAmbiguousFields(t *testing.T) {
	tests := []string{
		`{"body":"expected","body":"expected","node_id":"review","state":"COMMENTED","id":7}`,
		`{"body":{},"node_id":"review","state":"COMMENTED","id":7}`,
		`{"body":"expected","node_id":"review","state":"COMMENTED","id":0}`,
		`[{"body":"expected","node_id":"review","state":"COMMENTED","id":7}]`,
	}
	for _, encoded := range tests {
		if _, err := classifyPendingSubmissionResponse(
			t.Context(),
			[]byte(encoded),
			change.ReviewNodeID("review"),
			7,
			"expected",
			"COMMENTED",
		); err == nil {
			t.Fatalf("ambiguous response accepted: %s", encoded)
		}
	}
}

func TestPendingSubmissionResponseFramerPropagatesCancellation(t *testing.T) {
	cause := errors.New("stop pending submission response")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	_, err := classifyPendingSubmissionResponse(
		ctx,
		[]byte(`{"body":"expected","node_id":"review","state":"COMMENTED","id":7}`),
		change.ReviewNodeID("review"),
		7,
		"expected",
		"COMMENTED",
	)
	if !errors.Is(err, cause) {
		t.Fatalf("pending submission response cancellation = %v", err)
	}
}

func TestPendingObjectWriterOwnsSeparatorState(t *testing.T) {
	builder, err := newSourceJSONBuilder(256)
	if err != nil {
		t.Fatal(err)
	}
	fields := pendingObjectWriter{builder: builder}
	if err := builder.Syntax("{"); err != nil {
		t.Fatal(err)
	}
	if err := fields.String("first", "one"); err != nil {
		t.Fatal(err)
	}
	if err := fields.Uint32("second", 2); err != nil {
		t.Fatal(err)
	}
	if err := builder.Syntax("}"); err != nil {
		t.Fatal(err)
	}
	encoded, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if encoded != `{"first":"one","second":2}` {
		t.Fatalf("encoded object = %s", encoded)
	}
}
