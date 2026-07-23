package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
	"unsafe"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

func TestCollapseCaptureAccountingIncludesChunkStorageExactly(t *testing.T) {
	if got := unsafe.Sizeof([]int32(nil)); got != collapseChunkHeaderCharge {
		t.Fatalf("slice header size = %d, want charge %d", got, collapseChunkHeaderCharge)
	}
	for _, positionCount := range []int{0, 1, maxFileRecordsPerUpdate, maxFileRecordsPerUpdate + 1, maxFileTreeNodes} {
		chunkCount := (positionCount + maxFileRecordsPerUpdate - 1) / maxFileRecordsPerUpdate
		want := int64(chunkCount)*collapseChunkHeaderCharge +
			int64(positionCount)*collapsedPositionCharge
		got, err := collapseSnapshotRequestBytes(positionCount)
		if err != nil {
			t.Fatalf("position count %d: %v", positionCount, err)
		}
		if got != want {
			t.Fatalf("position count %d charge = %d, want %d", positionCount, got, want)
		}
	}

	positions, err := collapseSnapshotRequestBytes(maxFileTreeNodes)
	if err != nil {
		t.Fatal(err)
	}
	if got, requestErr := collapseCaptureRequestBytesForBase(
		MaxQueuedRequestBytes-positions,
		maxFileTreeNodes,
	); requestErr != nil || got != MaxQueuedRequestBytes {
		t.Fatalf("exact-limit request = %d, %v; want %d", got, requestErr, MaxQueuedRequestBytes)
	}
	if _, requestErr := collapseCaptureRequestBytesForBase(
		MaxQueuedRequestBytes-positions+1,
		maxFileTreeNodes,
	); !errors.Is(requestErr, ErrCoordinatorSaturated) {
		t.Fatalf("one-over request error = %v, want coordinator saturation", requestErr)
	}
	coordinator := newCaptureTestCoordinator(t)
	firstID, _ := review.NewOperationID(1)
	secondID, _ := review.NewOperationID(2)
	if reserveErr := coordinator.reserveCollapseCapture(firstID, MaxQueuedRequestBytes); reserveErr != nil {
		t.Fatal(reserveErr)
	}
	if stats := coordinator.Stats(); stats.RequestBytes != MaxQueuedRequestBytes {
		t.Fatalf("capture reservation bytes = %d, want %d", stats.RequestBytes, MaxQueuedRequestBytes)
	}
	if reserveErr := coordinator.reserveCollapseCapture(secondID, 1); !errors.Is(reserveErr, ErrCoordinatorSaturated) {
		t.Fatalf("second capture reservation error = %v, want saturation", reserveErr)
	}
	coordinator.releaseCollapseCapture(firstID)
}

func TestCollapseCaptureUsesBoundedOneShotStepsAndRestartsAtCurrentGeneration(t *testing.T) {
	model, modelCoordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, modelCoordinator)
	coordinator := newCaptureTestCoordinator(t)
	model.coordinator = coordinator

	const positions = 2*maxFileRecordsPerUpdate + 17
	model.allFileTree = fileTree{nodes: make([]treeNode, positions)}
	model.collapsedTree = newTestCollapsedTreeState(positions, 0)
	model.collapsedTree.generation = 7

	command := model.requestWorkspaceIndex()
	if command == nil || model.collapseCapture == nil {
		t.Fatal("workspace collapse capture was not started")
	}
	first := collapseCaptureCommandMessage(t, command)
	next := model.advanceCollapseCapture(first)
	if next == nil || model.collapseCapture.nextPosition != maxFileRecordsPerUpdate ||
		len(model.collapseCapture.positionChunks) != 1 ||
		len(model.collapseCapture.positionChunks[0]) != maxFileRecordsPerUpdate {
		t.Fatalf("first capture step = %+v", model.collapseCapture)
	}
	if duplicate := model.advanceCollapseCapture(first); duplicate != nil ||
		model.collapseCapture.nextPosition != maxFileRecordsPerUpdate {
		t.Fatal("duplicate capture permit advanced the capture")
	}

	second := collapseCaptureCommandMessage(t, next)
	model.collapsedTree.generation++
	restarted := model.advanceCollapseCapture(second)
	if restarted == nil || model.collapseCapture == nil ||
		model.collapseCapture.nextPosition != 0 ||
		len(model.collapseCapture.positionChunks) != 0 ||
		model.collapseCapture.generation != model.collapsedTree.generation {
		t.Fatalf("generation restart = %+v", model.collapseCapture)
	}
	restartMessage := collapseCaptureCommandMessage(t, restarted)
	if restartMessage.permit <= second.permit {
		t.Fatalf("restart permit = %d, want greater than stale permit %d", restartMessage.permit, second.permit)
	}
	if stale := model.advanceCollapseCapture(second); stale != nil ||
		model.collapseCapture.nextPosition != 0 {
		t.Fatal("stale pre-restart permit advanced the capture")
	}

	command = func() tea.Msg { return restartMessage }
	steps := 0
	for command != nil {
		message := collapseCaptureCommandMessage(t, command)
		before := 0
		if model.collapseCapture != nil {
			before = model.collapseCapture.nextPosition
		}
		command = model.advanceCollapseCapture(message)
		steps++
		if model.collapseCapture != nil {
			copied := model.collapseCapture.nextPosition - before
			if copied <= 0 || copied > maxFileRecordsPerUpdate {
				t.Fatalf("step copied %d positions", copied)
			}
		}
		if steps > 8 {
			t.Fatal("collapse capture did not terminate")
		}
	}
	if model.collapseCapture != nil {
		t.Fatal("completed capture remained active")
	}
	job := <-coordinator.workspaceIndexJobs
	wantRequest := int64(collapseRequestStateCharge) +
		int64((positions+maxFileRecordsPerUpdate-1)/maxFileRecordsPerUpdate)*collapseChunkHeaderCharge +
		int64(positions)*collapsedPositionCharge
	if stats := coordinator.Stats(); stats.Accepted != 1 || stats.RequestBytes != wantRequest {
		t.Fatalf("submitted capture accounting = %+v, want one accepted/%d bytes", stats, wantRequest)
	}
	model.collapsedTree.positions[0] = 42
	if got := job.work.Collapse.positionChunks[0][0]; got != missingCollapsedPosition {
		t.Fatalf("submitted collapse position changed to %d after model mutation", got)
	}

	buildErrors := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 32 {
			ledger, ledgerErr := newRetainedLedger(MaxRetainedResultBytes, 0)
			if ledgerErr != nil {
				buildErrors <- ledgerErr
				return
			}
			if _, buildErr := buildCollapsedTreeState(
				context.Background(),
				job.work.Collapse.tree,
				job.work.Collapse,
				ledger,
			); buildErr != nil {
				buildErrors <- buildErr
				return
			}
		}
	})
	workers.Go(func() {
		for range 100_000 {
			model.collapsedTree.positions[0]++
		}
	})
	workers.Wait()
	select {
	case buildErr := <-buildErrors:
		t.Fatal(buildErr)
	default:
	}
	coordinator.required.unregister(job.work.ID)
	coordinator.complete(WorkspaceIndexOutcome{ID: job.work.ID})
	if _, ok := coordinator.ClaimNext(); !ok {
		t.Fatal("workspace capture reservation was not releasable")
	}
}

func TestReloadCaptureOwnsCandidateAcrossInitialAdmissionRetryAndDuplicateOutcomes(t *testing.T) {
	model, modelCoordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, modelCoordinator)
	fixture := newSourceFixture(t, map[string]string{"beta.txt": "changed\n"})
	candidate := openCaptureCandidate(t, fixture)
	coordinator := newCaptureTestCoordinator(t)
	model.coordinator = coordinator
	id := beginCaptureTestReload(t, model)

	command := model.applyReloadAcquired(ReloadAcquiredOutcome{ID: id, Document: candidate})
	if command == nil || model.foreground.reload.phase != reloadCapturing ||
		model.foreground.reloadPreparationAttempts() != 0 {
		t.Fatalf("initial reload capture = %+v", model.foreground.reload)
	}
	if duplicate := model.applyReloadAcquired(ReloadAcquiredOutcome{
		ID: id, Document: candidate,
	}); duplicate != nil || !model.collapseCaptureOwnsDocument(candidate) {
		t.Fatal("duplicate acquired outcome changed capture ownership")
	}
	if candidate.Snapshot().SourceID() == 0 {
		t.Fatal("initial capture released its candidate document")
	}
	runCollapseCaptureCommands(t, model, command)
	if model.foreground.reload.phase != reloadPreparing ||
		model.foreground.reloadPreparationAttempts() != 1 ||
		model.reloadPreparationDocument != candidate {
		t.Fatalf("initial reload submission = %+v", model.foreground.reload)
	}
	job := <-coordinator.reloadPreparationJobs
	coordinator.required.unregister(job.work.ID)
	coordinator.complete(ReloadPreparedOutcome{
		ID:        id,
		Document:  candidate,
		AllFiles:  make([]review.FileID, candidate.Snapshot().FileCount()),
		Collapsed: collapsedTreeState{generation: model.collapsedTree.generation + 1},
	})
	claimed, ok := coordinator.ClaimNext()
	if !ok {
		t.Fatal("reload preparation outcome was not claimable")
	}
	prepared, ok := claimed.(ReloadPreparedOutcome)
	if !ok {
		t.Fatalf("reload preparation outcome type = %T", claimed)
	}
	retry := model.applyReloadPrepared(prepared)
	if retry == nil || model.foreground.reload.phase != reloadCapturing ||
		model.foreground.reloadPreparationAttempts() != 1 ||
		!model.collapseCaptureOwnsDocument(candidate) {
		t.Fatalf("reload retry capture = %+v", model.foreground.reload)
	}

	if duplicate := model.applyReloadPrepared(ReloadPreparedOutcome{ID: id, Document: candidate}); duplicate != nil {
		t.Fatal("duplicate reload outcome produced a command")
	}
	if candidate.Snapshot().SourceID() == 0 || !model.collapseCaptureOwnsDocument(candidate) {
		t.Fatal("duplicate reload outcome released the capture-owned candidate")
	}
	if !model.cancelReloadCollapseCapture() {
		t.Fatal("reload capture cancellation was rejected")
	}
	if model.foreground.reloadActive() || model.collapseCapture != nil ||
		candidate.Snapshot().SourceID() != 0 || coordinator.Stats().RequestBytes != 0 {
		t.Fatalf(
			"cancelled reload retained state: foreground %+v capture %v source %d stats %+v",
			model.foreground.reload,
			model.collapseCapture,
			candidate.Snapshot().SourceID(),
			coordinator.Stats(),
		)
	}
}

func TestReloadCaptureCleansCandidateOnRejectionQuitAndModelClose(t *testing.T) {
	t.Run("rejection", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		fixture := newSourceFixture(t, map[string]string{"beta.txt": "changed\n"})
		candidate := openCaptureCandidate(t, fixture)
		id := beginCaptureTestReload(t, model)
		command := model.applyReloadAcquired(ReloadAcquiredOutcome{ID: id, Document: candidate})
		if command == nil {
			t.Fatal("reload capture did not start")
		}
		coordinator.StopAdmissionAndCancel()
		runCollapseCaptureCommands(t, model, command)
		if model.foreground.reloadActive() || candidate.Snapshot().SourceID() != 0 {
			t.Fatal("rejected final submission retained its reload candidate")
		}
	})

	t.Run("quit", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		fixture := newSourceFixture(t, map[string]string{"beta.txt": "changed\n"})
		candidate := openCaptureCandidate(t, fixture)
		id := beginCaptureTestReload(t, model)
		if command := model.applyReloadAcquired(ReloadAcquiredOutcome{ID: id, Document: candidate}); command == nil {
			t.Fatal("reload capture did not start")
		}
		_ = model.quit()
		if model.collapseCapture != nil || candidate.Snapshot().SourceID() != 0 ||
			coordinator.Stats().RequestBytes != 0 {
			t.Fatalf("quit retained capture state: capture %v stats %+v", model.collapseCapture, coordinator.Stats())
		}
	})

	t.Run("model close", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		fixture := newSourceFixture(t, map[string]string{"beta.txt": "changed\n"})
		candidate := openCaptureCandidate(t, fixture)
		id := beginCaptureTestReload(t, model)
		if command := model.applyReloadAcquired(ReloadAcquiredOutcome{ID: id, Document: candidate}); command == nil {
			t.Fatal("reload capture did not start")
		}
		if err := model.CloseSourceDocument(); err != nil {
			t.Fatal(err)
		}
		if model.collapseCapture != nil || model.document != nil ||
			candidate.Snapshot().SourceID() != 0 || coordinator.Stats().RequestBytes != 0 {
			t.Fatalf("model close retained capture state: capture %v stats %+v", model.collapseCapture, coordinator.Stats())
		}
	})

	t.Run("retry quit", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		fixture := newSourceFixture(t, map[string]string{"beta.txt": "changed\n"})
		candidate := openCaptureCandidate(t, fixture)
		command := startCaptureTestRetry(t, model, coordinator, candidate)
		if command == nil || !model.collapseCaptureOwnsDocument(candidate) {
			t.Fatal("reload retry capture did not retain its candidate")
		}
		_ = model.quit()
		if model.collapseCapture != nil || candidate.Snapshot().SourceID() != 0 ||
			coordinator.Stats().RequestBytes != 0 {
			t.Fatalf("retry quit retained capture state: capture %v stats %+v", model.collapseCapture, coordinator.Stats())
		}
	})
}

func newCaptureTestCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	coordinator := &Coordinator{
		workContext:           t.Context(),
		workspaceIndexJobs:    make(chan workspaceIndexJob, 1),
		reloadPreparationJobs: make(chan reloadPreparationJob, 1),
		accepted:              make(map[review.OperationID]reservation),
		progress:              make(map[review.OperationID]Progress),
		required:              newRequiredScheduler(),
		ready:                 make(chan struct{}, 1),
	}
	coordinator.stats.Store(CoordinatorStats{})
	return coordinator
}

func beginCaptureTestReload(t *testing.T, model *Model) review.OperationID {
	t.Helper()
	start, _, ready := model.foreground.requestReload()
	if !ready {
		t.Fatal("reload start was not ready")
	}
	id, err := model.nextOperationID()
	if err != nil {
		t.Fatal(err)
	}
	next, err := model.review.BeginOperation(id, review.OperationReload)
	if err != nil {
		t.Fatal(err)
	}
	if !model.foreground.admitted(start, id) {
		t.Fatal("reload foreground admission failed")
	}
	model.review = next
	return id
}

func runCollapseCaptureCommands(t *testing.T, model *Model, command tea.Cmd) {
	t.Helper()
	for steps := 0; command != nil; steps++ {
		if steps > maxFileTreeNodes/maxFileRecordsPerUpdate+1 {
			t.Fatal("collapse capture did not terminate")
		}
		result := command()
		message, ok := result.(collapseCaptureStepMsg)
		if !ok {
			t.Fatalf("collapse command returned %T", result)
		}
		next := model.advanceCollapseCapture(message)
		position := 0
		if model.collapseCapture != nil {
			position = model.collapseCapture.nextPosition
		}
		if duplicate := model.advanceCollapseCapture(message); duplicate != nil ||
			model.collapseCapture != nil && model.collapseCapture.nextPosition != position {
			t.Fatal("duplicate collapse step advanced the capture")
		}
		command = next
	}
}

func startCaptureTestRetry(
	t *testing.T,
	model *Model,
	coordinator *Coordinator,
	document *source.Document,
) tea.Cmd {
	t.Helper()
	id := beginCaptureTestReload(t, model)
	command := model.applyReloadAcquired(ReloadAcquiredOutcome{ID: id, Document: document})
	runCollapseCaptureCommands(t, model, command)
	model.collapsedTree.generation++
	waitContext, cancelWait := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelWait()
	for range 32 {
		if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
			t.Fatalf("reload retry ready state = %d", state)
		}
		outcome, ok := coordinator.ClaimNext()
		if !ok {
			t.Fatal("reload retry outcome was not claimable")
		}
		if retry := model.applyOutcome(outcome); retry != nil {
			return retry
		}
	}
	t.Fatal("reload did not enter retry capture")
	return nil
}

func collapseCaptureCommandMessage(t *testing.T, command tea.Cmd) collapseCaptureStepMsg {
	t.Helper()
	message, ok := command().(collapseCaptureStepMsg)
	if !ok {
		t.Fatalf("collapse command returned an unexpected message")
	}
	return message
}

func openCaptureCandidate(t *testing.T, fixture sourceFixture) *source.Document {
	t.Helper()
	document, err := fixture.service.Open(
		context.Background(),
		source.WorktreeTarget(),
		fixture.document.Options(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := document.Close(); err != nil {
			t.Error(err)
		}
	})
	return document
}
