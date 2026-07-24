package app

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

func TestPublicationCheckReadyAndCancellation(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		id, result := beginTestPublicationCheck(t, model)
		model.applyPublicationCheckOutcome(PublicationCheckOutcome{ID: id, Check: result})
		if summary := model.review.PublicationSummary(); summary.Status != review.PublicationChecked || summary.Marker == "" {
			t.Fatalf("publication check summary = %+v", summary)
		}
		if operation := model.review.Operations()[id]; operation.Status != review.OperationSucceeded {
			t.Fatalf("publication check operation = %+v", operation)
		}
	})

	t.Run("cancel after worker completion", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		id, result := beginTestPublicationCheck(t, model)
		model.publicationIntent = publicationOperationIntentCancel
		model.overlay = OverlaySubmit
		model.applyPublicationCheckOutcome(PublicationCheckOutcome{ID: id, Check: result})
		if summary := model.review.PublicationSummary(); summary.Status != review.PublicationIdle {
			t.Fatalf("cancelled publication check summary = %+v", summary)
		}
		if model.overlay != OverlayNone || model.publicationIntent != publicationOperationIntentNone {
			t.Fatalf("cancelled publication UI = overlay %v intent %v", model.overlay, model.publicationIntent)
		}
	})
}

func TestEnterDuringPublicationCheckSubmitsWhenPreflightIsReady(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	setModelKeybindings(t, model, keymap.Binding{
		Context: keymap.ContextSubmit,
		Chord:   "enter",
		Action:  keymap.ActionSubmitReview,
	})
	id, result := beginTestPublicationCheck(t, model)
	model.overlay = OverlaySubmit

	_, _ = model.Update(keyMessage("enter"))
	if model.publicationIntent != publicationOperationIntentSubmit {
		t.Fatalf("publication intent = %v, want submit", model.publicationIntent)
	}
	if summary := model.review.PublicationSummary(); summary.Status != review.PublicationChecking {
		t.Fatalf("publication summary before check outcome = %+v", summary)
	}

	model.applyPublicationCheckOutcome(PublicationCheckOutcome{ID: id, Check: result})
	if summary := model.review.PublicationSummary(); summary.Status != review.PublicationPublishing {
		t.Fatalf("publication summary after check outcome = %+v", summary)
	}
	if model.publicationOperation == 0 || model.publicationOperation == id {
		t.Fatalf("publication operation = %d, want a new active operation", model.publicationOperation)
	}
	if model.publicationIntent != publicationOperationIntentNone {
		t.Fatalf("publication intent after admission = %v, want none", model.publicationIntent)
	}
}

func TestPublicationEventChoicesExposeAndCycleFinalGitHubActions(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	setModelKeybindings(t, model,
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "h", Action: keymap.ActionPreviousSubmitEvent},
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "l", Action: keymap.ActionNextSubmitEvent},
	)
	draftChoices := ansi.Strip(strings.Join(
		model.publicationEventLines(newSemanticStyles(model.settings)),
		"\n",
	))
	if !strings.Contains(draftChoices, "Synchronize draft comments") ||
		strings.Contains(draftChoices, "Leave feedback without approving") {
		t.Fatalf("draft choices = %q", draftChoices)
	}
	options, err := model.publicationOptions()
	if err != nil || options.Event() != review.PublicationEventDraft {
		t.Fatalf("initial publication options = (%s, %v), want draft", options.Event(), err)
	}

	model.publicationDraftReady = true
	choices := ansi.Strip(strings.Join(
		model.publicationEventLines(newSemanticStyles(model.settings)),
		"\n",
	))
	for _, want := range []string{
		"h or l to change",
		"▸ Comment",
		publicationEventApproveLabel,
		publicationEventRequestChangesLabel,
		"Leave feedback without approving",
		"Approve the pull request",
		"Require updates before merge",
	} {
		if !strings.Contains(choices, want) {
			t.Fatalf("choices omitted %q:\n%s", want, choices)
		}
	}
	model.applyPublicationAction(keymap.ActionNextSubmitEvent)
	choices = ansi.Strip(strings.Join(
		model.publicationEventLines(newSemanticStyles(model.settings)),
		"\n",
	))
	if model.publicationEvent != review.PublicationEventApprove ||
		!strings.Contains(choices, "▸ Approve") {
		t.Fatalf("next choices = %q (%s)", choices, model.publicationEvent)
	}
	model.applyPublicationAction(keymap.ActionPreviousSubmitEvent)
	if model.publicationEvent != review.PublicationEventComment {
		t.Fatalf("previous event = %s", model.publicationEvent)
	}
	model.applyPublicationAction(keymap.ActionPreviousSubmitEvent)
	if model.publicationEvent != review.PublicationEventRequestChanges {
		t.Fatalf("wrapped event = %s", model.publicationEvent)
	}
	options, err = model.publicationOptions()
	if err != nil || options.Event() != review.PublicationEventRequestChanges {
		t.Fatalf("final publication options = (%s, %v)", options.Event(), err)
	}
}

func TestPublicationEventChoicesUseConfiguredNavigationBindings(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	setModelKeybindings(t, model,
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "ctrl-c", Action: keymap.ActionInterrupt},
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "esc", Action: keymap.ActionCancel},
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "enter", Action: keymap.ActionSubmitReview},
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "[", Action: keymap.ActionPreviousSubmitEvent},
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "]", Action: keymap.ActionNextSubmitEvent},
	)
	model.publicationDraftReady = true
	if got := model.publicationEventNavigationHint(); got != "[ or ] to change" {
		t.Fatalf("navigation hint = %q", got)
	}
	controls := model.publicationBindingSummary()
	for _, want := range []string{
		"Enter publish review",
		"[ previous review action",
		"] next review action",
		"Esc cancel",
	} {
		if !strings.Contains(controls, want) {
			t.Fatalf("controls omitted %q: %q", want, controls)
		}
	}
}

func TestPublicationChoiceAndPrimaryActionSurviveMinimumViewport(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	setModelKeybindings(t, model,
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "enter", Action: keymap.ActionSubmitReview},
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "h", Action: keymap.ActionPreviousSubmitEvent},
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "l", Action: keymap.ActionNextSubmitEvent},
		keymap.Binding{Context: keymap.ContextSubmit, Chord: "esc", Action: keymap.ActionCancel},
	)
	model.publicationDraftReady = true
	view := ansi.Strip(model.publicationView(20, 6))
	lines := strings.Split(view, "\n")
	if len(lines) != 6 {
		t.Fatalf("publication view rows = %d, want 6:\n%s", len(lines), view)
	}
	for _, want := range []string{
		"Comment",
		publicationEventApproveLabel,
		publicationEventRequestChangesLabel,
		"Enter publish review",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("minimum publication view omitted %q:\n%s", want, view)
		}
	}
}

func TestPublicationCommitDispositionFencesRetry(t *testing.T) {
	testCases := []struct {
		name        string
		disposition review.PublicationDisposition
		wantSummary review.PublicationStatus
		wantStatus  review.OperationStatus
	}{
		{
			name:        "committed",
			disposition: review.PublicationDispositionCommitted,
			wantSummary: review.PublicationCommitted,
			wantStatus:  review.OperationSucceeded,
		},
		{
			name:        "failed before commit",
			disposition: review.PublicationDispositionFailedBeforeCommit,
			wantSummary: review.PublicationFailedBeforeCommit,
			wantStatus:  review.OperationFailed,
		},
		{
			name:        "cancelled before commit",
			disposition: review.PublicationDispositionCancelledBeforeCommit,
			wantSummary: review.PublicationFailedBeforeCommit,
			wantStatus:  review.OperationFailed,
		},
		{
			name:        "unknown",
			disposition: review.PublicationDispositionUnknown,
			wantSummary: review.PublicationUnknown,
			wantStatus:  review.OperationUnknown,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			model, coordinator := checkedPublicationModel(t)
			id, request := beginTestPublication(t, model)
			remoteReview := uint64(0)
			if testCase.disposition == review.PublicationDispositionCommitted {
				remoteReview = 42
			}
			if testCase.disposition == review.PublicationDispositionUnknown {
				// Keep the automatic reconciliation attempt deterministic: a
				// closed coordinator must leave the unknown publication fenced.
				coordinator.StopAdmissionAndCancel()
			}
			prepared := prepareTestPublicationResult(t, model, request, review.PublicationResult{
				Operation:    id,
				Workspace:    request.Workspace,
				Source:       request.Source,
				Disposition:  testCase.disposition,
				RemoteReview: remoteReview,
				Detail:       boundedOperationDetail("terminal publication result"),
			})
			model.applyPublicationCommitOutcome(PublicationCommitOutcome{
				ID:          id,
				Publication: prepared,
			})
			if summary := model.review.PublicationSummary(); summary.Status != testCase.wantSummary {
				t.Fatalf("publication summary = %+v, want %v", summary, testCase.wantSummary)
			}
			if operation := model.review.Operations()[id]; operation.Status != testCase.wantStatus {
				t.Fatalf("publication operation = %+v, want %v", operation, testCase.wantStatus)
			}
			if model.publicationOperation != 0 {
				t.Fatalf("publication operation remained active: %d", model.publicationOperation)
			}
		})
	}
}

func TestPublicationAdmissionFailureLeavesNoPhantomOperation(t *testing.T) {
	model, coordinator := checkedPublicationModel(t)
	id, _ := review.NewOperationID(model.nextOperation)
	coordinator.StopAdmissionAndCancel()
	model.startPublication()
	if model.publicationOperation != 0 {
		t.Fatalf("failed publication admission remained active: %d", model.publicationOperation)
	}
	if summary := model.review.PublicationSummary(); summary.Status != review.PublicationFailedBeforeCommit ||
		summary.Detail.String() != ErrCoordinatorClosed.Error() {
		t.Fatalf("failed publication summary = %+v", summary)
	}
	if operation := model.review.Operations()[id]; operation.Status != review.OperationFailed {
		t.Fatalf("failed publication operation = %+v", operation)
	}
	if stats := coordinator.Stats(); stats.Accepted != 0 || stats.Retained != 0 {
		t.Fatalf("failed publication retained accounting: %+v", stats)
	}
}

func TestStalePublicationOutcomeDoesNotConsumeActiveAttempt(t *testing.T) {
	model, _ := checkedPublicationModel(t)
	id, request := beginTestPublication(t, model)
	stale, _ := review.NewOperationID(id.Uint64() + 100)
	model.applyPublicationCommitOutcome(PublicationCommitOutcome{
		ID: stale,
	})
	if model.publicationOperation != id ||
		model.review.PublicationSummary().Status != review.PublicationPublishing {
		t.Fatal("stale publication outcome consumed the active attempt")
	}
	prepared := prepareTestPublicationResult(t, model, request, review.PublicationResult{
		Operation:   id,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: review.PublicationDispositionFailedBeforeCommit,
		Detail:      boundedOperationDetail("settled after stale result"),
	})
	model.applyPublicationCommitOutcome(PublicationCommitOutcome{ID: id, Publication: prepared})
}

func TestPublicationReconciliationSettlesUnknownAttempt(t *testing.T) {
	testCases := []struct {
		name              string
		disposition       review.ReconciliationDisposition
		wantSummary       review.PublicationStatus
		wantAttemptStatus review.OperationStatus
		wantReconStatus   review.OperationStatus
	}{
		{
			name:              "committed",
			disposition:       review.ReconciliationCommitted,
			wantSummary:       review.PublicationCommitted,
			wantAttemptStatus: review.OperationSucceeded,
			wantReconStatus:   review.OperationSucceeded,
		},
		{
			name:              "not committed",
			disposition:       review.ReconciliationNotCommitted,
			wantSummary:       review.PublicationConfirmedAbsent,
			wantAttemptStatus: review.OperationFailed,
			wantReconStatus:   review.OperationSucceeded,
		},
		{
			name:              "still unknown",
			disposition:       review.ReconciliationStillUnknown,
			wantSummary:       review.PublicationUnknown,
			wantAttemptStatus: review.OperationUnknown,
			wantReconStatus:   review.OperationFailed,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			model, _ := checkedPublicationModel(t)
			attempt, publicationRequest := beginTestPublication(t, model)
			unknown := review.PublicationResult{
				Operation:   attempt,
				Workspace:   publicationRequest.Workspace,
				Source:      publicationRequest.Source,
				Disposition: review.PublicationDispositionUnknown,
				Detail:      boundedOperationDetail("unknown publication"),
			}
			next, err := model.review.ApplyPublication(unknown)
			if err != nil {
				t.Fatal(err)
			}
			next = resolvePublicationOperation(
				next,
				attempt,
				review.OperationUnknown,
				unknown.Detail,
			)
			model.review = next
			model.publicationOperation = 0

			reconciliation, err := model.nextOperationID()
			if err != nil {
				t.Fatal(err)
			}
			next, request, err := model.review.StartReconciliation(reconciliation)
			if err != nil {
				t.Fatal(err)
			}
			model.review = next
			model.publicationOperation = reconciliation
			prepared := prepareTestReconciliationResult(t, model, request, review.ReconciliationResult{
				Operation:   reconciliation,
				Workspace:   request.Workspace,
				Source:      request.Source,
				Disposition: testCase.disposition,
				Detail:      boundedOperationDetail("reconciliation result"),
			})
			model.applyReconciliationOutcome(ReconciliationOutcome{
				ID: reconciliation, Reconciliation: prepared,
			})
			summary := model.review.PublicationSummary()
			if summary.Status != testCase.wantSummary || summary.Target.Repository() == "" ||
				summary.Marker == "" {
				t.Fatalf("reconciliation summary = %+v", summary)
			}
			operations := model.review.Operations()
			if operations[attempt].Status != testCase.wantAttemptStatus ||
				operations[reconciliation].Status != testCase.wantReconStatus {
				t.Fatalf(
					"operation statuses = attempt %v reconciliation %v",
					operations[attempt],
					operations[reconciliation],
				)
			}
		})
	}
}

func TestPublicationCommitPermitDefinesCancellationBoundary(t *testing.T) {
	t.Run("cancel before claim", func(t *testing.T) {
		permit := &publicationCommitPermit{}
		permit.cancel()
		if permit.claim() {
			t.Fatal("cancelled permit crossed the commit boundary")
		}
	})

	t.Run("claim before cancel", func(t *testing.T) {
		permit := &publicationCommitPermit{}
		if !permit.claim() {
			t.Fatal("open permit did not claim the commit boundary")
		}
		permit.cancel()
		if permit.state.Load() != publicationPermitClaimed {
			t.Fatalf("claimed permit state = %d", permit.state.Load())
		}
	})
}

func TestPublicationWorkerCommitBoundary(t *testing.T) {
	t.Run("cancel before claim remains retryable", func(t *testing.T) {
		model, _ := checkedPublicationModel(t)
		id, request := beginTestPublication(t, model)
		controlled := newControlledPublicationCoordinator(
			t.Context(),
			model.coordinator.service,
		)
		if err := controlled.SubmitPublication(PublicationWork{
			ID: id, Request: request,
		}); err != nil {
			t.Fatal(err)
		}
		job := <-controlled.publicationJobs
		if !controlled.Cancel(id) {
			t.Fatal("publication cancellation was not accepted")
		}
		controlled.runPublicationCommit(job)
		outcome, ok := controlled.ClaimNext()
		publication, typed := outcome.(PublicationCommitOutcome)
		if !ok || !typed ||
			publication.Publication.Disposition() != review.PublicationDispositionCancelledBeforeCommit {
			t.Fatalf("pre-claim outcome = (%T, %+v, %v)", outcome, publication, ok)
		}
		model.applyPublicationCommitOutcome(publication)
		if summary := model.review.PublicationSummary(); summary.Status != review.PublicationFailedBeforeCommit {
			t.Fatalf("pre-claim publication summary = %+v", summary)
		}
		retryID, err := model.nextOperationID()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := model.review.StartPublicationCheck(
			retryID,
			model.settings.publication,
		); err != nil {
			t.Fatalf("cancelled-before-commit publication is not retryable: %v", err)
		}
		if stats := controlled.Stats(); stats.Accepted != 0 || stats.Retained != 0 ||
			stats.RequestBytes != 0 || stats.ReservedResult != 0 {
			t.Fatalf("pre-claim accounting = %+v", stats)
		}
	})
}

func newControlledPublicationCoordinator(
	ctx context.Context,
	service *source.Service,
) *Coordinator {
	coordinator := &Coordinator{
		workContext:     ctx,
		service:         service,
		accepted:        make(map[review.OperationID]reservation),
		progress:        make(map[review.OperationID]Progress),
		required:        newRequiredScheduler(),
		ready:           make(chan struct{}, 1),
		publicationJobs: make(chan publicationJob, 1),
	}
	coordinator.stats.Store(CoordinatorStats{})
	return coordinator
}

func TestInvalidPublicationResultsSettleToSafeStates(t *testing.T) {
	t.Run("check fails closed", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		id, result := beginTestPublicationCheck(t, model)
		result.Workspace = 0
		model.applyPublicationCheckOutcome(PublicationCheckOutcome{ID: id, Check: result})
		if summary := model.review.PublicationSummary(); summary.Status != review.PublicationFailedBeforeCommit || summary.Detail.IsEmpty() {
			t.Fatalf("invalid check summary = %+v", summary)
		}
		if operation := model.review.Operations()[id]; operation.Status != review.OperationFailed {
			t.Fatalf("invalid check operation = %+v", operation)
		}
	})

	t.Run("commit becomes unknown", func(t *testing.T) {
		model, coordinator := checkedPublicationModel(t)
		id, request := beginTestPublication(t, model)
		coordinator.StopAdmissionAndCancel()
		prepared, prepareErr := coordinator.preparePublicationResult(publicationJob{
			ctx: t.Context(),
			publication: PublicationWork{
				ID: id, Request: request, Rebase: model.review.PendingRebaseSource(),
			},
		}, review.PublicationResult{
			Operation:    id,
			Workspace:    0,
			Source:       request.Source,
			Disposition:  review.PublicationDispositionCommitted,
			RemoteReview: 42,
		})
		model.applyPublicationCommitOutcome(PublicationCommitOutcome{
			ID: id, Publication: prepared, Err: prepareErr,
		})
		if summary := model.review.PublicationSummary(); summary.Status != review.PublicationUnknown || summary.Detail.IsEmpty() {
			t.Fatalf("invalid commit summary = %+v", summary)
		}
		if operation := model.review.Operations()[id]; operation.Status != review.OperationUnknown {
			t.Fatalf("invalid commit operation = %+v", operation)
		}
	})

	t.Run("reconciliation remains unknown", func(t *testing.T) {
		model, _ := checkedPublicationModel(t)
		attempt, publicationRequest := beginTestPublication(t, model)
		unknown := review.PublicationResult{
			Operation:   attempt,
			Workspace:   publicationRequest.Workspace,
			Source:      publicationRequest.Source,
			Disposition: review.PublicationDispositionUnknown,
			Detail:      boundedOperationDetail("unknown publication"),
		}
		next, err := model.review.ApplyPublication(unknown)
		if err != nil {
			t.Fatal(err)
		}
		next = resolvePublicationOperation(
			next,
			attempt,
			review.OperationUnknown,
			unknown.Detail,
		)
		model.review = next
		model.publicationOperation = 0
		reconciliation, err := model.nextOperationID()
		if err != nil {
			t.Fatal(err)
		}
		next, request, err := model.review.StartReconciliation(reconciliation)
		if err != nil {
			t.Fatal(err)
		}
		model.review = next
		model.publicationOperation = reconciliation
		prepared, prepareErr := model.coordinator.prepareReconciliationResult(publicationJob{
			ctx: t.Context(),
			reconciliation: ReconciliationWork{
				ID: reconciliation, Request: request, Rebase: model.review.PendingRebaseSource(),
			},
		}, review.ReconciliationResult{
			Operation:   reconciliation,
			Workspace:   0,
			Source:      request.Source,
			Disposition: review.ReconciliationCommitted,
		})
		model.applyReconciliationOutcome(ReconciliationOutcome{
			ID: reconciliation, Reconciliation: prepared, Err: prepareErr,
		})
		if summary := model.review.PublicationSummary(); summary.Status != review.PublicationUnknown || summary.Detail.IsEmpty() {
			t.Fatalf("invalid reconciliation summary = %+v", summary)
		}
		operations := model.review.Operations()
		if operations[attempt].Status != review.OperationUnknown ||
			operations[reconciliation].Status != review.OperationFailed {
			t.Fatalf(
				"invalid reconciliation operations = attempt %+v reconciliation %+v",
				operations[attempt],
				operations[reconciliation],
			)
		}
	})
}

func beginTestPublicationCheck(
	t *testing.T,
	model *Model,
) (review.OperationID, review.PublicationCheckResult) {
	t.Helper()
	options, err := model.publicationOptions()
	if err != nil {
		t.Fatal(err)
	}
	id, err := model.nextOperationID()
	if err != nil {
		t.Fatal(err)
	}
	current := model.review
	next, request, err := current.StartPublicationCheck(id, options)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := review.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := review.TakeCheckedPublication(
		review.CheckedPublicationDescriptor{
			Target: testPublicationTarget(t),
			Marker: marker,
		},
		prepared,
	)
	if err != nil {
		t.Fatal(err)
	}
	model.review = next
	model.publicationOperation = id
	return id, review.PublicationCheckResult{
		Operation: id,
		Workspace: request.Workspace,
		Source:    request.Source,
		Checked:   checked,
	}
}

func checkedPublicationModel(t *testing.T) (*Model, *Coordinator) {
	t.Helper()
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	model.publicationDraftReady = true
	id, result := beginTestPublicationCheck(t, model)
	model.applyPublicationCheckOutcome(PublicationCheckOutcome{ID: id, Check: result})
	if model.review.PublicationSummary().Status != review.PublicationChecked {
		t.Fatal("publication preflight did not become ready")
	}
	return model, coordinator
}

func beginTestPublication(
	t *testing.T,
	model *Model,
) (review.OperationID, review.PublicationRequest) {
	t.Helper()
	id, err := model.nextOperationID()
	if err != nil {
		t.Fatal(err)
	}
	next, request, err := model.review.StartPublication(id)
	if err != nil {
		t.Fatal(err)
	}
	model.review = next
	model.publicationOperation = id
	return id, request
}

func prepareTestPublicationResult(
	t *testing.T,
	model *Model,
	request review.PublicationRequest,
	result review.PublicationResult,
) review.PreparedPublicationResult {
	t.Helper()
	prepared, err := review.PreparePublicationResult(
		request,
		model.review.PublicationRebaseSource(),
		result,
	)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func prepareTestReconciliationResult(
	t *testing.T,
	model *Model,
	request review.ReconciliationRequest,
	result review.ReconciliationResult,
) review.PreparedReconciliationResult {
	t.Helper()
	prepared, err := review.PrepareReconciliationResult(
		request,
		model.review.PendingRebaseSource(),
		result,
	)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}
