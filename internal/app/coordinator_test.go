package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/review"
)

func TestCoordinatorAdmissionRetainsOutcomeUntilClaim(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	workContext, cancelWork := context.WithCancel(t.Context())
	t.Cleanup(cancelWork)
	deliveryManager := newTestDeliveryManager(t)
	coordinator, err := NewCoordinator(workContext, fixture.service, deliveryManager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		coordinator.StopAdmissionAndCancel()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := coordinator.Shutdown(shutdownContext); err != nil {
			t.Error(err)
		}
	})

	current, err := review.NewReview(fixture.document.Snapshot(), []review.CommentKind{"issue"})
	if err != nil {
		t.Fatal(err)
	}
	fileID, ok := current.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	contextLines, err := review.NewContextLines(3)
	if err != nil {
		t.Fatal(err)
	}
	patchContext, err := review.ContextAround(contextLines)
	if err != nil {
		t.Fatal(err)
	}
	_, request, err := current.RequestPatch(fileID, patchContext)
	if err != nil {
		t.Fatal(err)
	}
	firstID, _ := review.NewOperationID(1)
	if err := coordinator.SubmitPatch(PatchWork{
		ID: firstID, Document: fixture.document, Request: request,
	}); err != nil {
		t.Fatal(err)
	}

	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("ready state = %d, want available", state)
	}
	if stats := coordinator.Stats(); stats.Accepted != 1 || stats.Retained != 1 {
		t.Fatalf("stats before claim = %+v", stats)
	}
	outcome, ok := coordinator.ClaimNext()
	_, patchOK := outcome.(PatchOutcome)
	if !ok || !patchOK {
		t.Fatalf("claim = (%T, %t), want patch outcome", outcome, ok)
	}
	if stats := coordinator.Stats(); stats.Accepted != 0 || stats.Retained != 0 {
		t.Fatalf("stats after claim = %+v", stats)
	}
}

func TestCoordinatorClaimClearsVacatedOutcomeSlot(t *testing.T) {
	coordinator := &Coordinator{
		accepted: make(map[review.OperationID]reservation),
		ready:    make(chan struct{}, 1),
	}
	coordinator.stats.Store(CoordinatorStats{})
	id, _ := review.NewOperationID(1)
	coordinator.outcomes = make([]Outcome, 1, 4)
	coordinator.outcomes[0] = SearchOutcome{ID: id}

	if _, ok := coordinator.ClaimNext(); !ok {
		t.Fatal("queued outcome was not claimed")
	}
	backing := coordinator.outcomes[:cap(coordinator.outcomes)]
	for index, outcome := range backing {
		if outcome != nil {
			t.Fatalf("vacated outcome slot %d retains %T", index, outcome)
		}
	}
}

func TestCoordinatorKeepsPublicationByteCapacityReserved(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	workContext, cancelWork := context.WithCancel(t.Context())
	t.Cleanup(cancelWork)
	coordinator, err := NewCoordinator(
		workContext,
		fixture.service,
		newTestDeliveryManager(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := coordinator.Shutdown(shutdownContext); err != nil {
			t.Error(err)
		}
	})

	ordinaryID, _ := review.NewOperationID(1)
	if err := coordinator.submit(
		ordinaryID,
		MaxQueuedRequestBytes-reservedPublicationRequestBytes,
		MaxRetainedResultBytes-reservedPublicationResultBytes,
		func(context.Context) bool { return true },
	); err != nil {
		t.Fatalf("fill ordinary partition: %v", err)
	}
	rejectedID, _ := review.NewOperationID(2)
	if err := coordinator.submit(
		rejectedID,
		1,
		1,
		func(context.Context) bool { return true },
	); !errors.Is(err, ErrCoordinatorSaturated) {
		t.Fatalf("ordinary overflow error = %v, want %v", err, ErrCoordinatorSaturated)
	}

	publicationID, _ := review.NewOperationID(3)
	marker, err := review.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.SubmitReconciliation(ReconciliationWork{
		ID: publicationID,
		Request: review.ReconciliationRequest{
			Operation: publicationID,
			Workspace: 1,
			Source:    1,
			Target:    testPublicationTarget(t),
			Marker:    marker,
		},
	}); err != nil {
		t.Fatalf("reserved reconciliation admission: %v", err)
	}
	stats := coordinator.Stats()
	if stats.RequestBytes != MaxQueuedRequestBytes-reservedPublicationRequestBytes+512 ||
		stats.ReservedResult != MaxRetainedResultBytes {
		t.Fatalf("full partition stats = %+v", stats)
	}
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	var reconciliationClaimed bool
	for attempts := 0; !reconciliationClaimed && attempts < 8; attempts++ {
		if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
			t.Fatalf("reconciliation readiness = %v", state)
		}
		outcome, ok := coordinator.ClaimNext()
		if !ok {
			t.Fatal("reconciliation readiness had no outcome")
		}
		_, reconciliationClaimed = outcome.(ReconciliationOutcome)
	}
	if !reconciliationClaimed {
		t.Fatal("reconciliation terminal outcome was not claimed")
	}
	stats = coordinator.Stats()
	if stats.Accepted != 1 ||
		stats.RequestBytes != MaxQueuedRequestBytes-reservedPublicationRequestBytes ||
		stats.ReservedResult != MaxRetainedResultBytes-reservedPublicationResultBytes {
		t.Fatalf("post-reconciliation stats = %+v", stats)
	}
}

func TestCoordinatorChargesExportProjectionUntilRequestSaturation(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	coordinator := newShutdownTestCoordinator(t, fixture)
	kind, err := review.NewCommentKind("issue")
	if err != nil {
		t.Fatal(err)
	}
	current, err := review.NewReview(fixture.document.Snapshot(), []review.CommentKind{kind})
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", int(review.MaxCommentBodyBytes))
	for range int(review.MaxCommentBodyTotal / review.MaxCommentBodyBytes) {
		current, err = current.BeginDraft(review.ReviewCommentTarget(), kind)
		if err != nil {
			t.Fatal(err)
		}
		current, err = current.UpdateDraft(body)
		if err != nil {
			t.Fatal(err)
		}
		current, _, err = current.SaveDraft()
		if err != nil {
			t.Fatal(err)
		}
	}
	input := current.ExportInput()
	if input.RetainedBytes() <= current.RetainedCommentBytes() {
		t.Fatalf(
			"export input bytes = %d, want comment bodies plus metadata",
			input.RetainedBytes(),
		)
	}

	accepted := 0
	for value := uint64(1); value <= MaxAcceptedOperations; value++ {
		id, _ := review.NewOperationID(value)
		err := coordinator.SubmitExport(ExportWork{
			ID:          id,
			Input:       input,
			Options:     review.ExportOptions{},
			Format:      review.ExportJSON,
			Destination: delivery.DestinationStdout,
		})
		if errors.Is(err, ErrCoordinatorSaturated) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		accepted++
	}
	if accepted == 0 || accepted == MaxAcceptedOperations {
		t.Fatalf("accepted exports = %d, want request-byte saturation", accepted)
	}
	stats := coordinator.Stats()
	if stats.RequestBytes != int64(accepted)*input.RetainedBytes() ||
		stats.RequestBytes > MaxQueuedRequestBytes-reservedPublicationRequestBytes {
		t.Fatalf("export projection accounting = %+v for %d admissions", stats, accepted)
	}
	_ = shutdownTestCoordinator(t, coordinator)
}

func TestCoordinatorCoalescesProgressBehindTerminalPriority(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	workContext, cancelWork := context.WithCancel(t.Context())
	t.Cleanup(cancelWork)
	coordinator, err := NewCoordinator(
		workContext,
		fixture.service,
		newTestDeliveryManager(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := coordinator.Shutdown(shutdownContext); err != nil {
			t.Error(err)
		}
	})
	id, _ := review.NewOperationID(1)
	if err := coordinator.submit(
		id,
		1,
		1,
		func(context.Context) bool { return true },
	); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReportProgress(Progress{
		ID: id, Completed: 1, Total: 3, Detail: boundedOperationDetail("first"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReportProgress(Progress{
		ID: id, Completed: 2, Total: 3, Detail: boundedOperationDetail("latest"),
	}); err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("progress ready state = %d", state)
	}
	outcome, ok := coordinator.ClaimNext()
	progress, progressOK := outcome.(Progress)
	if !ok || !progressOK || progress.Completed != 2 ||
		progress.Detail.String() != "latest" {
		t.Fatalf("coalesced progress = %+v, outcome type %T", progress, outcome)
	}

	if err := coordinator.ReportProgress(Progress{
		ID: id, Completed: 3, Total: 3, Detail: boundedOperationDetail("done"),
	}); err != nil {
		t.Fatal(err)
	}
	coordinator.complete(PatchOutcome{ID: id})
	outcome, ok = coordinator.ClaimNext()
	_, patchOK := outcome.(PatchOutcome)
	if !ok || !patchOK {
		t.Fatalf("terminal priority claim = (%T, %t)", outcome, ok)
	}
	if _, ok := coordinator.ClaimNext(); ok {
		t.Fatal("progress survived terminal completion")
	}
}

func TestTerminalClaimResignalsRetainedProgress(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	coordinator := newShutdownTestCoordinator(t, fixture)
	progressID, _ := review.NewOperationID(1)
	terminalID, _ := review.NewOperationID(2)
	for _, id := range []review.OperationID{progressID, terminalID} {
		if err := coordinator.submit(
			id,
			1,
			1,
			func(context.Context) bool { return true },
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := coordinator.ReportProgress(Progress{
		ID: progressID, Completed: 1, Total: 2,
		Detail: boundedOperationDetail("still running"),
	}); err != nil {
		t.Fatal(err)
	}

	// Deterministically model the notifier placing its sole readiness token
	// before another operation completes and finds that channel full.
	coordinator.mu.Lock()
	coordinator.progressDirty = false
	coordinator.signalReadyLocked()
	coordinator.mu.Unlock()
	coordinator.complete(PatchOutcome{ID: terminalID})

	waitContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("terminal readiness = %v", state)
	}
	if outcome, ok := coordinator.ClaimNext(); !ok || outcome.operationID() != terminalID {
		t.Fatalf("first claim = (%T, %v), want terminal %d", outcome, ok, terminalID)
	}
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("retained progress readiness = %v", state)
	}
	outcome, ok := coordinator.ClaimNext()
	progress, progressOK := outcome.(Progress)
	if !ok || !progressOK || progress.ID != progressID {
		t.Fatalf("second claim = (%T, %v), want progress %d", outcome, ok, progressID)
	}
}

func TestRequiredSchedulerAllowsAtMostEightBypasses(t *testing.T) {
	scheduler := newRequiredScheduler()
	ids := make([]review.OperationID, 10)
	for index := range ids {
		ids[index], _ = review.NewOperationID(uint64(index + 1))
		scheduler.register(ids[index])
	}
	starts := make([]<-chan struct{}, len(ids))
	for index := 1; index < len(ids); index++ {
		starts[index] = scheduler.markReady(ids[index])
	}
	for index := 1; index <= maxRequiredBypasses; index++ {
		select {
		case <-starts[index]:
		default:
			t.Fatalf("bypass %d was not granted", index)
		}
	}
	select {
	case <-starts[9]:
		t.Fatal("ninth bypass was granted before the oldest operation")
	default:
	}
	starts[0] = scheduler.markReady(ids[0])
	select {
	case <-starts[0]:
	default:
		t.Fatal("oldest ready operation was not granted")
	}
	select {
	case <-starts[9]:
	default:
		t.Fatal("blocked later operation did not run after the oldest")
	}
}

func TestReadyReceiverStopIsABoundedCompletionBarrier(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	workContext, cancelWork := context.WithCancel(t.Context())
	t.Cleanup(cancelWork)
	deliveryManager := newTestDeliveryManager(t)
	coordinator, err := NewCoordinator(workContext, fixture.service, deliveryManager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		coordinator.StopAdmissionAndCancel()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := coordinator.Shutdown(shutdownContext); err != nil {
			t.Error(err)
		}
	})
	receiver, err := NewReadyReceiver(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	command := receiver.Arm(coordinator)
	if command == nil {
		t.Fatal("receiver did not arm")
	}
	commandDone := make(chan tea.Msg, 1)
	go func() {
		commandDone <- command()
	}()

	barrierContext, cancelBarrier := context.WithTimeout(t.Context(), time.Second)
	defer cancelBarrier()
	if err := receiver.StopAndWait(barrierContext); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-commandDone:
		ready, ok := message.(outcomeReadyMsg)
		if !ok || ready.state != ReadyCancelled {
			t.Fatalf("receiver message = %#v, want cancellation", message)
		}
	case <-time.After(time.Second):
		t.Fatal("receiver command remained blocked after barrier")
	}
}
