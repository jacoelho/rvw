package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

func TestReloadAcquisitionAndPreparationCommitAtomicallyOffUpdate(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"a.txt": "changed\n"})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})

	local, ok := model.document.Local()
	if !ok || local.Repo == nil {
		t.Fatal("test document has no local repository")
	}
	if err := os.WriteFile(
		filepath.Join(local.Repo.String(), "b.txt"),
		[]byte("untracked\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	oldSource := model.review.Snapshot().SourceID()
	oldWorkspace := model.review.WorkspaceRevision()
	model.startReload()
	if model.foreground.reloadOperation() == 0 || model.foreground.reloadIsPreparing() {
		t.Fatalf(
			"reload acquisition state = operation %d preparing %v",
			model.foreground.reloadOperation(),
			model.foreground.reloadIsPreparing(),
		)
	}

	for attempts := 0; !model.foreground.reloadIsPreparing() && attempts < 8; attempts++ {
		if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
			t.Fatalf("reload acquisition ready state = %d", state)
		}
		outcome, ok := coordinator.ClaimNext()
		if !ok {
			t.Fatal("reload acquisition outcome was not claimable")
		}
		runCollapseCaptureCommands(t, model, model.applyOutcome(outcome))
	}
	if !model.foreground.reloadIsPreparing() {
		t.Fatal("reload did not enter off-loop reconciliation")
	}
	if model.review.Snapshot().SourceID() != oldSource {
		t.Fatal("source became observable before prepared replacement committed")
	}
	var err error
	model.review, err = model.review.ToggleFileReviewed(0)
	if err != nil {
		t.Fatal(err)
	}

	for attempts := 0; model.foreground.reloadOperation() != 0 && attempts < 8; attempts++ {
		if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
			t.Fatalf("reload preparation ready state = %d", state)
		}
		outcome, ok := coordinator.ClaimNext()
		if !ok {
			t.Fatal("reload preparation outcome was not claimable")
		}
		runCollapseCaptureCommands(t, model, model.applyOutcome(outcome))
	}
	if model.foreground.reloadOperation() != 0 || model.foreground.reloadIsPreparing() {
		t.Fatalf(
			"reload did not settle: operation %d preparing %v",
			model.foreground.reloadOperation(),
			model.foreground.reloadIsPreparing(),
		)
	}
	if model.review.Snapshot().SourceID() == oldSource ||
		model.review.WorkspaceRevision().Uint64() != oldWorkspace.Uint64()+1 ||
		model.review.Snapshot().FileCount() != 2 {
		t.Fatalf(
			"replacement = source %d workspace %d files %d",
			model.review.Snapshot().SourceID(),
			model.review.WorkspaceRevision(),
			model.review.Snapshot().FileCount(),
		)
	}
	if len(model.allFileOrder) != 2 {
		t.Fatalf("prepared file order length = %d", len(model.allFileOrder))
	}
	if !model.review.FileReviewed(0) {
		t.Fatal("reload retry lost review state changed during preparation")
	}
}

func TestSourceConstructionLimitMapsToLimitedOperation(t *testing.T) {
	err := fmt.Errorf("construction: %w", source.ErrSourceLimited)
	if status := operationStatusForWorkError(err); status != review.OperationLimited {
		t.Fatalf("operation status = %s", status)
	}
}

func TestPatchAndReloadAdmissionFailureLeaveNoPhantomOperations(t *testing.T) {
	t.Run("patch", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		active, ok := model.review.ActiveFile()
		if !ok {
			t.Fatal("missing active file")
		}
		model.review = model.review.DropPatch(active)
		id, _ := review.NewOperationID(model.nextOperation)
		coordinator.StopAdmissionAndCancel()
		model.requestActivePatch()
		if model.foreground.patchCount() != 0 {
			t.Fatal("failed patch admission remained active")
		}
		if operation := model.review.Operations()[id]; operation.Status != review.OperationFailed {
			t.Fatalf("failed patch operation = %+v", operation)
		}
		if patch := model.review.Patch(active); patch.Status != review.PatchFailed {
			t.Fatalf("failed patch state = %+v", patch)
		}
	})

	t.Run("reload", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		oldSource := model.review.Snapshot().SourceID()
		id, _ := review.NewOperationID(model.nextOperation)
		coordinator.StopAdmissionAndCancel()
		model.startReload()
		if model.foreground.reloadActive() {
			t.Fatal("failed reload admission remained active")
		}
		if model.review.Snapshot().SourceID() != oldSource {
			t.Fatal("failed reload replaced the active source")
		}
		if operation := model.review.Operations()[id]; operation.Status != review.OperationFailed {
			t.Fatalf("failed reload operation = %+v", operation)
		}
	})
}

func TestPatchWorkerErrorSettlesPendingPatch(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	model.review = model.review.DropPatch(active)
	start, _, ready := model.foreground.requestPatch(active)
	if !ready {
		t.Fatal("patch start was not prepared")
	}
	next, _, err := model.review.RequestPatch(active, model.settings.patchContext)
	if err != nil {
		t.Fatal(err)
	}
	id, err := model.nextOperationID()
	if err != nil {
		t.Fatal(err)
	}
	next, err = next.BeginOperation(id, review.OperationPatch)
	if err != nil {
		t.Fatal(err)
	}
	if !model.foreground.admitted(start, id) {
		t.Fatal("patch admission failed")
	}
	model.review = next
	model.applyPatchOutcome(PatchOutcome{ID: id, Err: errors.New("patch worker failed")})
	if patch := model.review.Patch(active); patch.Status != review.PatchFailed ||
		patch.Detail.String() != "patch worker failed" {
		t.Fatalf("worker failure patch = %+v", patch)
	}
	if operation := model.review.Operations()[id]; operation.Status != review.OperationFailed {
		t.Fatalf("worker failure operation = %+v", operation)
	}
	if model.foreground.patchCount() != 0 {
		t.Fatal("worker failure left patch active")
	}
}

func TestDeferredReloadRejectionResumesSearchAgainstOldReview(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	oldSource := model.review.Snapshot().SourceID()
	model.review = model.review.DropPatch(0)
	patchStart, _, ready := model.foreground.requestPatch(0)
	if !ready || !model.foreground.admitted(patchStart, 10_000) {
		t.Fatal("synthetic patch admission failed")
	}
	pending := testPendingSearch(t, "after rejected reload")
	_, _, _ = model.foreground.requestSearch(pending)
	_, _, _ = model.foreground.requestReload()
	_, _, tracked, effect := model.foreground.completePatch(10_000)
	if !tracked || effect.kind != foregroundEffectStartReload {
		t.Fatalf("deferred reload effect = %+v, tracked %v", effect, tracked)
	}
	reloadID, _ := review.NewOperationID(model.nextOperation)
	patchID, _ := review.NewOperationID(model.nextOperation + 1)
	searchID, _ := review.NewOperationID(model.nextOperation + 2)
	coordinator.StopAdmissionAndCancel()
	model.applyForegroundEffect(effect)
	if model.foreground.reloadActive() || model.foreground.searchOperation() != 0 ||
		model.foreground.patchCount() != 0 {
		t.Fatalf("foreground state did not settle: %+v", model.foreground)
	}
	if model.review.Snapshot().SourceID() != oldSource {
		t.Fatal("rejected deferred reload replaced the active source")
	}
	if summary := model.review.SearchSummary(); summary.Status != review.SearchFailed || summary.Query != pending.query {
		t.Fatalf("resumed search summary = %+v", summary)
	}
	operations := model.review.Operations()
	if operations[reloadID].Status != review.OperationFailed ||
		operations[patchID].Status != review.OperationFailed ||
		operations[searchID].Status != review.OperationFailed {
		t.Fatalf(
			"deferred rejection operations = reload %+v patch %+v search %+v",
			operations[reloadID],
			operations[patchID],
			operations[searchID],
		)
	}
}

func TestReloadFailureRestoresSearchThenPatchAdmission(t *testing.T) {
	t.Run("acquisition failure", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		pending, reloadStart := prepareDeferredReloadState(t, model, "after reload failure")
		reloadID, err := model.nextOperationID()
		if err != nil {
			t.Fatal(err)
		}
		next, err := model.review.BeginOperation(reloadID, review.OperationReload)
		if err != nil {
			t.Fatal(err)
		}
		if !model.foreground.admitted(reloadStart, reloadID) {
			t.Fatal("synthetic reload admission failed")
		}
		model.review = next

		model.failReload(reloadID, nil, errors.New("reload acquisition failed"))
		assertSerializedRestoredSearchAndPatch(t, model, coordinator, pending)
		settleModelWork(t, model, coordinator)
	})

	t.Run("deferred admission rejection", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		pending, reloadStart := prepareDeferredReloadState(t, model, "after rejected reload")
		fillerID, _ := review.NewOperationID(20_000)
		if err := coordinator.submit(fillerID, 1, 1, func(context.Context) bool {
			return true
		}); err != nil {
			t.Fatal(err)
		}

		model.beginReload(reloadStart)
		assertSerializedRestoredSearchAndPatch(t, model, coordinator, pending)
		coordinator.complete(SearchOutcome{ID: fillerID})
		settleModelWork(t, model, coordinator)
	})
}

func TestReloadPreparedOutcomeCountsAggregateOwnershipOnce(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	current, err := review.NewReview(testReviewRequest(fixture.document.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	document, err := fixture.service.Reload(
		t.Context(),
		fixture.document,
		fixture.document.Options(),
		func(_ context.Context, work func() error) error { return work() },
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := document.Close(); err != nil {
			t.Error(err)
		}
	})
	snapshot := document.Snapshot()
	replacement, err := review.PrepareReplacement(current, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	index, err := buildWorkspaceIndex(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	outcome := ReloadPreparedOutcome{
		Document:          document,
		Replacement:       replacement,
		AllFiles:          index.AllFiles,
		VisibleFiles:      index.VisibleFiles,
		FileTree:          index.FileTree,
		VisibleProjection: index.VisibleProjection,
		Collapsed:         index.Collapsed,
		PathFiles:         index.PathFiles,
		AmbiguousPaths:    index.AmbiguousPaths,
	}
	want := snapshot.RetainedBytes() + replacement.RetainedBytes() + index.RetainedBytes()
	if got := outcome.RetainedBytes(); got != want {
		t.Fatalf("reload retained bytes = %d, want aggregate %d", got, want)
	}
}

func prepareDeferredReloadState(
	t *testing.T,
	model *Model,
	query string,
) (pendingSearch, foregroundStart) {
	t.Helper()
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	model.review = model.review.DropPatch(active)
	patchStart, _, ready := model.foreground.requestPatch(active)
	if !ready || !model.foreground.admitted(patchStart, 10_000) {
		t.Fatal("synthetic patch admission failed")
	}
	pending := testPendingSearch(t, query)
	_, searchCancellations, ready := model.foreground.requestSearch(pending)
	if ready || len(searchCancellations) != 1 {
		t.Fatalf("deferred search = cancellations %v ready %v", searchCancellations, ready)
	}
	_, reloadCancellations, ready := model.foreground.requestReload()
	if ready || len(reloadCancellations) != 1 {
		t.Fatalf("deferred reload = cancellations %v ready %v", reloadCancellations, ready)
	}
	_, _, tracked, effect := model.foreground.completePatch(10_000)
	if !tracked || effect.kind != foregroundEffectStartReload {
		t.Fatalf("reload continuation = %+v, tracked %v", effect, tracked)
	}
	reloadStart, ok := model.foreground.prepareReload()
	if !ok || reloadStart.source != foregroundStartWaitingReload {
		t.Fatalf("reload start = %+v, ready %v", reloadStart, ok)
	}
	return pending, reloadStart
}

func assertSerializedRestoredSearchAndPatch(
	t *testing.T,
	model *Model,
	coordinator *Coordinator,
	pending pendingSearch,
) {
	t.Helper()
	if model.foreground.searchOperation() == 0 || model.foreground.patchCount() != 0 ||
		model.foreground.search.continuation.kind != searchContinuationPatch {
		t.Fatalf("restored foreground ownership = %+v", model.foreground)
	}
	if summary := model.review.SearchSummary(); summary.Status != review.SearchActive ||
		summary.Query != pending.query {
		t.Fatalf("restored search summary = %+v", summary)
	}
	if stats := coordinator.Stats(); stats.Accepted < 1 {
		t.Fatalf("restored coordinator admission = %+v, want active serialized search", stats)
	}
}
