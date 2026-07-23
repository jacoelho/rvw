package source

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestPendingPublicationExecutionCopiesSharePrivateState(t *testing.T) {
	state := &pendingPublicationExecutionState{
		retainedBytes:  4_096,
		reconciliation: pendingPublicationNoReconciliation,
		progress:       pendingPublicationUncommitted,
		status:         pendingPublicationExecutionOpen,
	}
	execution := PendingPublicationExecution{state: state}
	copyOfExecution := execution

	if got := copyOfExecution.RetainedBytes(); got != 4_096 {
		t.Fatalf("copied execution retained bytes = %d", got)
	}
	copyOfExecution.Close()
	if execution.RetainedBytes() != 0 || state.status != 0 {
		t.Fatal("closing a copied execution did not invalidate shared state")
	}
}

func TestPendingPublicationStepAllowsOneOutstandingCell(t *testing.T) {
	state := &pendingPublicationExecutionState{
		reconciliation: pendingPublicationNoReconciliation,
		progress:       pendingPublicationUncommitted,
		status:         pendingPublicationExecutionOpen,
	}
	execution := PendingPublicationExecution{state: state}
	continuation := state.continuationStep()
	copyOfContinuation := continuation
	if continuation.Kind() != PendingPublicationStepContinue {
		t.Fatalf("continuation kind = %d", continuation.Kind())
	}

	rejected := new(Service).AcquirePendingPublication(
		t.Context(),
		execution,
		directCPUAdmission,
	)
	if rejected.Kind() != PendingPublicationStepTerminal {
		t.Fatalf("second outstanding step kind = %d", rejected.Kind())
	}
	rejection, err := rejected.TakeCommit()
	if err != nil {
		t.Fatal(err)
	}
	if rejection.Disposition != review.PublicationDispositionUnknown {
		t.Fatalf("second outstanding step disposition = %d", rejection.Disposition)
	}
	if err := continuation.Advance(); err != nil {
		t.Fatal(err)
	}
	if copyOfContinuation.Kind() != 0 || state.outstanding != nil {
		t.Fatal("consuming one step copy left another copy usable")
	}
}

func TestPendingPublicationStepCloseForcesReadBeforeWriteRetry(t *testing.T) {
	tests := []struct {
		name    string
		kind    pendingPublicationAcquisitionKind
		initial pendingPublicationReconciliation
		want    pendingPublicationReconciliation
	}{
		{
			name:    "mutation response",
			kind:    pendingMutationResponse,
			initial: pendingPublicationNoReconciliation,
			want:    pendingPublicationReconcileMutation,
		},
		{
			name:    "mutation reconciliation",
			kind:    pendingMutationReconciliation,
			initial: pendingPublicationReconcileMutation,
			want:    pendingPublicationReconcileMutation,
		},
		{
			name:    "submission response",
			kind:    pendingSubmissionResponse,
			initial: pendingPublicationNoReconciliation,
			want:    pendingPublicationReconcileSubmission,
		},
		{
			name:    "submission reconciliation",
			kind:    pendingSubmissionReconciliation,
			initial: pendingPublicationReconcileSubmission,
			want:    pendingPublicationReconcileSubmission,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			priorFailure := errors.New("prior remote uncertainty")
			var pendingFailure error
			if test.initial != pendingPublicationNoReconciliation {
				pendingFailure = priorFailure
			}
			state := &pendingPublicationExecutionState{
				pendingFailure: pendingFailure,
				reconciliation: test.initial,
				progress:       pendingPublicationUncommitted,
				status:         pendingPublicationExecutionOpen,
			}
			step := state.acquiredStep(
				test.kind,
				[]byte("unclassified remote response"),
				review.PendingReview{},
				pendingFailure,
			)
			copyOfStep := step

			step.Close()

			if state.outstanding != nil || copyOfStep.Kind() != 0 {
				t.Fatal("closing an acquisition did not consume its shared cell")
			}
			if state.reconciliation != test.want {
				t.Fatalf("close reconciliation fence = %d", state.reconciliation)
			}
			if pendingFailure != nil && !errors.Is(state.pendingFailure, priorFailure) {
				t.Fatalf("close did not preserve failure = %v", state.pendingFailure)
			}
			if pendingFailure == nil && !strings.Contains(
				state.pendingFailure.Error(),
				"closed before classification",
			) {
				t.Fatalf("response close failure = %v", state.pendingFailure)
			}
		})
	}
}

func TestPendingPublicationStepKindsAreConsuming(t *testing.T) {
	t.Run("acquired finish", func(t *testing.T) {
		state := &pendingPublicationExecutionState{
			batches: []pendingMutationBatch{{
				payload: "mutation",
				effects: []pendingWireEffect{{}},
				aliases: []pendingMutationAlias{{name: "m0", client: "client"}},
			}},
			reconciliation: pendingPublicationNoReconciliation,
			progress:       pendingPublicationUncommitted,
			status:         pendingPublicationExecutionOpen,
		}
		step := state.acquiredStep(
			pendingMutationResponse,
			[]byte(`{"data":{"m0":{"clientMutationId":"client"}},"errors":[]}`),
			review.PendingReview{},
			nil,
		)
		copyOfStep := step
		continuation := step.Finish(t.Context())
		if continuation.Kind() != PendingPublicationStepContinue {
			t.Fatalf("finish kind = %d", continuation.Kind())
		}
		if copyOfStep.Kind() != 0 {
			t.Fatal("finished acquisition copy remained usable")
		}
		if err := continuation.Advance(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("continuation close", func(t *testing.T) {
		state := &pendingPublicationExecutionState{
			reconciliation: pendingPublicationNoReconciliation,
			progress:       pendingPublicationUncommitted,
			status:         pendingPublicationExecutionOpen,
		}
		step := state.continuationStep()
		step.Close()
		if state.status != pendingPublicationExecutionOpen || state.outstanding != nil {
			t.Fatal("closing continuation did not preserve the next classified transition")
		}
	})

	t.Run("terminal take", func(t *testing.T) {
		state := &pendingPublicationExecutionState{
			reconciliation: pendingPublicationNoReconciliation,
			progress:       pendingPublicationUncommitted,
			status:         pendingPublicationExecutionOpen,
		}
		step := state.terminalStep(PublicationCommit{
			Disposition: review.PublicationDispositionCommitted,
		})
		copyOfStep := step
		commit, err := step.TakeCommit()
		if err != nil {
			t.Fatal(err)
		}
		if commit.Disposition != review.PublicationDispositionCommitted || state.status != 0 {
			t.Fatalf("terminal take = disposition:%d status:%d", commit.Disposition, state.status)
		}
		if copyOfStep.Kind() != 0 {
			t.Fatal("taken terminal copy remained usable")
		}
	})

	t.Run("terminal close", func(t *testing.T) {
		state := &pendingPublicationExecutionState{
			reconciliation: pendingPublicationNoReconciliation,
			progress:       pendingPublicationUncommitted,
			status:         pendingPublicationExecutionOpen,
		}
		step := state.terminalStep(PublicationCommit{})
		step.Close()
		if state.status != 0 {
			t.Fatal("closing terminal step did not invalidate execution")
		}
	})
}

func TestPendingPublicationFinishCancellationUsesAcquiredResponseUncertainty(t *testing.T) {
	tests := []struct {
		name      string
		kind      pendingPublicationAcquisitionKind
		wantPhase review.PendingPublicationPhase
	}{
		{
			name:      "mutation response",
			kind:      pendingMutationResponse,
			wantPhase: review.PendingPublicationSynchronizing,
		},
		{
			name:      "submission response",
			kind:      pendingSubmissionResponse,
			wantPhase: review.PendingPublicationSubmitting,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &pendingPublicationExecutionState{
				reconciliation: pendingPublicationNoReconciliation,
				progress:       pendingPublicationUncommitted,
				status:         pendingPublicationExecutionOpen,
			}
			step := state.acquiredStep(
				test.kind,
				[]byte(`{"data":{}}`),
				review.PendingReview{},
				nil,
			)
			cause := errors.New("cancel before response classification")
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(cause)

			terminal := step.Finish(ctx)
			commit, err := terminal.TakeCommit()
			if err != nil {
				t.Fatal(err)
			}
			if commit.Disposition != review.PublicationDispositionUnknown ||
				commit.PendingPhase != test.wantPhase ||
				!strings.Contains(commit.Detail.String(), cause.Error()) {
				t.Fatalf("cancelled response classification = %+v", commit)
			}
		})
	}
}

func TestPendingPublicationAcquireCancellationPreservesStartedFailure(t *testing.T) {
	tests := []struct {
		name           string
		reconciliation pendingPublicationReconciliation
		wantPhase      review.PendingPublicationPhase
	}{
		{
			name:           "mutation reconciliation",
			reconciliation: pendingPublicationReconcileMutation,
			wantPhase:      review.PendingPublicationSynchronizing,
		},
		{
			name:           "submission reconciliation",
			reconciliation: pendingPublicationReconcileSubmission,
			wantPhase:      review.PendingPublicationSubmitting,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			startedFailure := errors.New("started write failed")
			state := &pendingPublicationExecutionState{
				pendingFailure: startedFailure,
				reconciliation: test.reconciliation,
				progress:       pendingPublicationUncommitted,
				status:         pendingPublicationExecutionOpen,
			}
			cause := errors.New("cancel before reconciliation read")
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(cause)

			terminal := new(Service).AcquirePendingPublication(
				ctx,
				PendingPublicationExecution{state: state},
				directCPUAdmission,
			)
			commit, err := terminal.TakeCommit()
			if err != nil {
				t.Fatal(err)
			}
			detail := commit.Detail.String()
			if commit.Disposition != review.PublicationDispositionUnknown ||
				commit.PendingPhase != test.wantPhase ||
				!strings.Contains(detail, startedFailure.Error()) ||
				!strings.Contains(detail, cause.Error()) {
				t.Fatalf("cancelled reconciliation = %+v", commit)
			}
		})
	}
}

func TestPendingPublicationNilAdmissionPreservesUncertainty(t *testing.T) {
	tests := []struct {
		name            string
		reconciliation  pendingPublicationReconciliation
		pendingFailure  error
		wantDisposition review.PublicationDisposition
		wantPhase       review.PendingPublicationPhase
	}{
		{
			name:            "no uncertain write",
			reconciliation:  pendingPublicationNoReconciliation,
			wantDisposition: review.PublicationDispositionFailedBeforeCommit,
		},
		{
			name:            "mutation reconciliation",
			reconciliation:  pendingPublicationReconcileMutation,
			pendingFailure:  errors.New("mutation write failed after start"),
			wantDisposition: review.PublicationDispositionUnknown,
			wantPhase:       review.PendingPublicationSynchronizing,
		},
		{
			name:            "submission reconciliation",
			reconciliation:  pendingPublicationReconcileSubmission,
			pendingFailure:  errors.New("submission write failed after start"),
			wantDisposition: review.PublicationDispositionUnknown,
			wantPhase:       review.PendingPublicationSubmitting,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &pendingPublicationExecutionState{
				pendingFailure: test.pendingFailure,
				reconciliation: test.reconciliation,
				progress:       pendingPublicationUncommitted,
				status:         pendingPublicationExecutionOpen,
			}
			terminal := new(Service).AcquirePendingPublication(
				t.Context(),
				PendingPublicationExecution{state: state},
				nil,
			)
			commit, err := terminal.TakeCommit()
			if err != nil {
				t.Fatal(err)
			}
			if commit.Disposition != test.wantDisposition || commit.PendingPhase != test.wantPhase {
				t.Fatalf("nil-admission terminal = %+v", commit)
			}
			if test.pendingFailure != nil &&
				!strings.Contains(commit.Detail.String(), test.pendingFailure.Error()) {
				t.Fatalf("nil-admission detail = %q", commit.Detail.String())
			}
		})
	}
}

func TestFailedPendingCommitDistinguishesRemoteProgress(t *testing.T) {
	failure := errors.New("mutation failed")
	uncommitted := failedPendingCommit(failure, pendingPublicationUncommitted)
	if uncommitted.Disposition != review.PublicationDispositionFailedBeforeCommit ||
		uncommitted.PendingPhase != 0 {
		t.Fatalf("uncommitted failure = %+v", uncommitted)
	}
	partial := failedPendingCommit(failure, pendingPublicationPartiallyCommitted)
	if partial.Disposition != review.PublicationDispositionUnknown ||
		partial.PendingPhase != review.PendingPublicationSynchronizing {
		t.Fatalf("partial failure = %+v", partial)
	}
}
