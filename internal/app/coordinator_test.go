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
	"github.com/jacoelho/rvw/internal/source"
)

func TestPatchResultReservationUsesLowerLevelPhaseMaximum(t *testing.T) {
	if patchResultReservation != source.MaxPatchResultConstructionBytes {
		t.Fatalf(
			"patch reservation = %d, lower-level maximum %d",
			patchResultReservation,
			source.MaxPatchResultConstructionBytes,
		)
	}
	want := max(review.MaxPreparedPatchConstructionBytes, review.MaxPreparedPatchRetainedBytes)
	if patchResultReservation != want {
		t.Fatalf("patch reservation = %d, phase maximum %d", patchResultReservation, want)
	}
	if patchResultReservation > MaxRetainedResultBytes {
		t.Fatalf("patch reservation = %d, coordinator limit %d", patchResultReservation, MaxRetainedResultBytes)
	}
}

func TestCoordinatorAcceptsExactPatchAndSearchReservationsAndRejectsPlusOne(t *testing.T) {
	query, err := review.NewSearchQuery(strings.Repeat("x", review.MaxSearchQueryBytes))
	if err != nil {
		t.Fatal(err)
	}
	request, err := review.SearchRequestConstructionBytes(query, review.MaxFiles)
	if err != nil {
		t.Fatal(err)
	}
	searchReservation := request + patchResultReservation + review.MaxSearchHitBytes +
		review.MaxSearchBatchConstructionBytes
	for name, resultBytes := range map[string]int64{
		"patch":     patchResultReservation,
		searchLabel: searchReservation,
	} {
		t.Run(name, func(t *testing.T) {
			coordinator := &Coordinator{
				workContext: t.Context(),
				accepted:    make(map[review.OperationID]reservation),
				required:    newRequiredScheduler(),
			}
			coordinator.stats.Store(CoordinatorStats{})
			fillerID, _ := review.NewOperationID(1)
			if err := coordinator.submit(
				fillerID,
				0,
				MaxRetainedResultBytes-resultBytes,
				func(context.Context) bool { return true },
			); err != nil {
				t.Fatalf("fill to exact boundary: %v", err)
			}
			operationID, _ := review.NewOperationID(2)
			if err := coordinator.submit(
				operationID,
				0,
				resultBytes,
				func(context.Context) bool { return true },
			); err != nil {
				t.Fatalf("exact reservation: %v", err)
			}
			if got := coordinator.Stats().ReservedResult; got != MaxRetainedResultBytes {
				t.Fatalf("exact reservation total = %d", got)
			}
			oneOverID, _ := review.NewOperationID(3)
			if err := coordinator.submit(
				oneOverID,
				0,
				1,
				func(context.Context) bool { return true },
			); !errors.Is(err, ErrCoordinatorSaturated) {
				t.Fatalf("one-byte-over error = %v, want %v", err, ErrCoordinatorSaturated)
			}
			if got := coordinator.Stats().ReservedResult; got != MaxRetainedResultBytes {
				t.Fatalf("rejected reservation mutated total = %d", got)
			}
		})
	}
}

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

	current, err := review.NewReview(testReviewRequest(fixture.document.Snapshot()))
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
	if stats := coordinator.Stats(); stats.Accepted != 1 || stats.Retained != 1 ||
		stats.ReservedResult != patchResultReservation {
		t.Fatalf("stats before claim = %+v", stats)
	}
	outcome, ok := coordinator.ClaimNext()
	_, patchOK := outcome.(PatchOutcome)
	if !ok || !patchOK {
		t.Fatalf("claim = (%T, %t), want patch outcome", outcome, ok)
	}
	if stats := coordinator.Stats(); stats.Accepted != 0 || stats.Retained != 0 ||
		stats.ReservedResult != 0 {
		t.Fatalf("stats after claim = %+v", stats)
	}
}

func TestWorkspaceIndexReservesSnapshotDerivedAllowanceUntilClaim(t *testing.T) {
	coordinator := &Coordinator{
		workContext:        t.Context(),
		workspaceIndexJobs: make(chan workspaceIndexJob, 1),
		accepted:           make(map[review.OperationID]reservation),
		progress:           make(map[review.OperationID]Progress),
		required:           newRequiredScheduler(),
		ready:              make(chan struct{}, 1),
	}
	coordinator.stats.Store(CoordinatorStats{})
	id, err := review.NewOperationID(1)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := fileTreeSnapshot(t, []string{"alpha.txt"})
	want, err := workspaceIndexAllowance(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.SubmitWorkspaceIndex(WorkspaceIndexWork{
		ID:       id,
		Snapshot: snapshot,
	}); err != nil {
		t.Fatal(err)
	}
	if got := coordinator.Stats().ReservedResult; got != want {
		t.Fatalf("workspace reservation = %d, want snapshot-derived %d", got, want)
	}
	coordinator.complete(WorkspaceIndexOutcome{ID: id})
	if got := coordinator.Stats().ReservedResult; got != want {
		t.Fatalf("completed workspace reservation = %d, want retained %d", got, want)
	}
	if _, ok := coordinator.ClaimNext(); !ok {
		t.Fatal("workspace outcome was not claimable")
	}
	if got := coordinator.Stats().ReservedResult; got != 0 {
		t.Fatalf("workspace reservation after claim = %d, want 0", got)
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

func TestCoordinatorGrowsResultReservationBeforeProportionalConstruction(t *testing.T) {
	id, _ := review.NewOperationID(1)
	coordinator := &Coordinator{
		accepted: map[review.OperationID]reservation{
			id: {resultBytes: 1},
		},
		reservedResult: 1,
	}
	coordinator.stats.Store(CoordinatorStats{})
	if err := coordinator.growResultReservation(id, MaxRetainedResultBytes); err != nil {
		t.Fatal(err)
	}
	if stats := coordinator.Stats(); stats.ReservedResult != MaxRetainedResultBytes {
		t.Fatalf("grown result reservation = %+v", stats)
	}
	other, _ := review.NewOperationID(2)
	coordinator.accepted[other] = reservation{resultBytes: 1}
	coordinator.reservedResult++
	if err := coordinator.growResultReservation(other, 2); !errors.Is(err, ErrCoordinatorSaturated) {
		t.Fatalf("overflow growth error = %v, want %v", err, ErrCoordinatorSaturated)
	}
	if coordinator.accepted[other].resultBytes != 1 {
		t.Fatal("rejected growth mutated reservation")
	}
}

func TestCoordinatorAccountsPublicationWithinGlobalByteCapacity(t *testing.T) {
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

	publicationID, _ := review.NewOperationID(3)
	marker, err := review.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	if err != nil {
		t.Fatal(err)
	}
	reconciliation := ReconciliationWork{
		ID: publicationID,
		Request: review.ReconciliationRequest{
			Operation: publicationID,
			Workspace: 1,
			Source:    1,
			Target:    testPublicationTarget(t),
			Marker:    marker,
		},
	}
	reconciliationBytes := reconciliation.Request.RetainedBytes()
	ordinaryID, _ := review.NewOperationID(1)
	if err := coordinator.submit(
		ordinaryID,
		MaxQueuedRequestBytes-reconciliationBytes,
		MaxRetainedResultBytes-publicationResultReservation,
		func(context.Context) bool { return true },
	); err != nil {
		t.Fatalf("fill ordinary partition: %v", err)
	}
	if err := coordinator.SubmitReconciliation(reconciliation); err != nil {
		t.Fatalf("reserved reconciliation admission: %v", err)
	}
	stats := coordinator.Stats()
	if stats.RequestBytes != MaxQueuedRequestBytes ||
		stats.ReservedResult != MaxRetainedResultBytes {
		t.Fatalf("full partition stats = %+v", stats)
	}
	rejectedID, _ := review.NewOperationID(2)
	if err := coordinator.submit(
		rejectedID,
		1,
		1,
		func(context.Context) bool { return true },
	); !errors.Is(err, ErrCoordinatorSaturated) {
		t.Fatalf("global overflow error = %v, want %v", err, ErrCoordinatorSaturated)
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
		stats.RequestBytes != MaxQueuedRequestBytes-reconciliationBytes ||
		stats.ReservedResult != MaxRetainedResultBytes-publicationResultReservation {
		t.Fatalf("post-reconciliation stats = %+v", stats)
	}
}

func TestCoordinatorSerializesMaximumSearchAndPatchReservations(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	coordinator := newShutdownTestCoordinator(t, fixture)
	searchID, _ := review.NewOperationID(1)
	query, _ := review.NewSearchQuery(strings.Repeat("x", review.MaxSearchQueryBytes))
	construction, err := review.SearchRequestConstructionBytes(query, review.MaxFiles)
	if err != nil {
		t.Fatal(err)
	}
	searchReservation := construction + patchResultReservation + review.MaxSearchHitBytes +
		review.MaxSearchBatchConstructionBytes
	if err := coordinator.submit(
		searchID,
		1,
		searchReservation,
		func(context.Context) bool { return true },
	); err != nil {
		t.Fatalf("maximum search reservation: %v", err)
	}
	if stats := coordinator.Stats(); stats.ReservedResult != searchReservation {
		t.Fatalf("search reservation stats = %+v", stats)
	}

	patchID, _ := review.NewOperationID(2)
	if err := coordinator.submit(
		patchID,
		1,
		patchResultReservation,
		func(context.Context) bool { return true },
	); !errors.Is(err, ErrCoordinatorSaturated) {
		t.Fatalf("concurrent patch error = %v, want %v", err, ErrCoordinatorSaturated)
	}

	coordinator.complete(SearchOutcome{ID: searchID})
	if _, ok := coordinator.ClaimNext(); !ok {
		t.Fatal("completed search was not claimable")
	}
	if stats := coordinator.Stats(); stats.ReservedResult != 0 {
		t.Fatalf("claimed search retained result credit: %+v", stats)
	}
	if err := coordinator.submit(
		patchID,
		1,
		patchResultReservation,
		func(context.Context) bool { return true },
	); err != nil {
		t.Fatalf("patch after search claim: %v", err)
	}
	coordinator.complete(PatchOutcome{ID: patchID})
	if _, ok := coordinator.ClaimNext(); !ok {
		t.Fatal("completed patch was not claimable")
	}
}

func TestCoordinatorChargesExportProjectionUntilRequestSaturation(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	coordinator := newShutdownTestCoordinator(t, fixture)
	current, err := review.NewReview(testReviewRequest(fixture.document.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", int(review.MaxCommentBodyBytes))
	for range int(review.MaxCommentBodyTotal / review.MaxCommentBodyBytes) {
		current, err = current.BeginDraft(review.ReviewCommentTarget())
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
	source := current.ExportSource()
	if source.RetainedBytes() == 0 || source.ConstructionBytes() <= source.RetainedBytes() {
		t.Fatalf(
			"export source bytes = %d construction %d",
			source.RetainedBytes(), source.ConstructionBytes(),
		)
	}

	accepted := 0
	for value := uint64(1); value <= MaxAcceptedOperations; value++ {
		id, _ := review.NewOperationID(value)
		err := coordinator.SubmitExport(ExportWork{
			ID:          id,
			Source:      source,
			Options:     review.DefaultExportOptions(),
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
	if stats.RequestBytes != int64(accepted)*source.RetainedBytes() ||
		stats.RequestBytes > MaxQueuedRequestBytes {
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
