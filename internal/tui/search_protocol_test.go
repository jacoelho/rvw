package tui

import (
	"context"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/change"
)

func TestSearchReplacementAndClearSettleDeterministically(t *testing.T) {
	t.Run("replacement", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		startTestSearch(t, model, "changed")
		first := model.foreground.searchOperation()
		if first == 0 {
			t.Fatal("search was not admitted")
		}
		startTestSearch(t, model, "absent")
		if model.foreground.searchOperation() != first {
			t.Fatal("replacement became active before the first search settled")
		}
		settleModelWork(t, model, coordinator)
		if model.foreground.searchOperation() != 0 {
			t.Fatal("replacement search did not settle")
		}
		summary := model.review.SearchSummary()
		if summary.Query.String() != "absent" || summary.Status != change.SearchComplete ||
			summary.HitCount != 0 {
			t.Fatalf("replacement summary = %+v", summary)
		}
	})

	t.Run("clear", func(t *testing.T) {
		model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
		settleModelWork(t, model, coordinator)
		startTestSearch(t, model, "changed")
		if model.foreground.searchOperation() == 0 {
			t.Fatal("search was not admitted")
		}
		model.cancelSearch()
		settleModelWork(t, model, coordinator)
		if summary := model.review.SearchSummary(); summary.Status != change.SearchIdle {
			t.Fatalf("cleared search summary = %+v", summary)
		}
		if model.searchCursor != 0 {
			t.Fatalf("search cursor = %d, want 0", model.searchCursor)
		}
	})
}

func TestCancellingSearchDeferredBehindSyntaxRestartsSyntaxWhenEligible(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "changed\n",
		"beta.txt":  "changed\n",
	})
	settleModelWork(t, model, coordinator)
	model.size = TerminalSize{Width: normalWidth, Height: 24, Known: true}
	restoredFile, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing restored file")
	}
	model.overlay = OverlayFind
	model.find = findPalette{
		phase:       findSearching,
		returnState: model.captureFindReturnState(),
	}
	previewFile := restoredFile
	for _, file := range model.fileOrder {
		if file != restoredFile {
			previewFile = file
			break
		}
	}
	if previewFile == restoredFile || !model.activateFile(previewFile) {
		t.Fatal("missing preview file")
	}
	oldSyntax, _ := change.NewOperationID(model.nextOperation + 100)
	model.syntaxOperation = oldSyntax
	model.syntaxFile = previewFile

	startTestSearch(t, model, "changed")
	if model.searchAfterSyntax == nil {
		t.Fatal("search did not wait for active syntax")
	}
	model.cancelSearch()
	if model.searchAfterSyntax != nil || !model.syntaxDeferred {
		t.Fatal("deferred search cancellation did not preserve syntax restart")
	}
	model.applySyntaxOutcome(SyntaxOutcome{ID: oldSyntax, Err: context.Canceled})
	if model.syntaxOperation != 0 || !model.syntaxDeferred {
		t.Fatal("ineligible overlay consumed deferred syntax restart")
	}

	model.closeFind(findCloseCancel)
	if active, _ := model.review.ActiveFile(); active != restoredFile {
		t.Fatalf("cancelled find restored file %d, want %d", active, restoredFile)
	}
	if model.syntaxOperation != 0 || !model.syntaxDeferred {
		t.Fatal("syntax restarted before the restored patch was ready")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for model.syntaxOperation == 0 {
		if state := coordinator.WaitReady(ctx); state != ReadyAvailable {
			t.Fatalf("restored patch readiness = %v", state)
		}
		_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	}
	if model.syntaxOperation == oldSyntax || model.syntaxFile != restoredFile || model.syntaxDeferred {
		t.Fatalf(
			"syntax restart = operation %d file %d deferred %t",
			model.syntaxOperation,
			model.syntaxFile,
			model.syntaxDeferred,
		)
	}
}

func TestSearchAdmissionFailureLeavesNoPhantomOperation(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	coordinator.StopAdmissionAndCancel()
	startTestSearch(t, model, "changed")
	if model.foreground.searchOperation() != 0 {
		t.Fatal("failed search admission left an active operation")
	}
	if stats := coordinator.Stats(); stats.Accepted != 0 || stats.Retained != 0 {
		t.Fatalf("failed search admission retained coordinator accounting: %+v", stats)
	}
	summary := model.review.SearchSummary()
	if summary.Status != change.SearchFailed ||
		summary.Detail.String() != ErrCoordinatorClosed.Error() {
		t.Fatalf("failed search summary = %+v", summary)
	}
}

func TestSearchSaturationLeavesNoPhantomOperation(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	filler, _ := change.NewOperationID(10_000)
	if err := coordinator.submit(
		filler,
		MaxQueuedRequestBytes,
		MaxRetainedResultBytes-publicationResultReservation,
		func(context.Context) bool { return true },
	); err != nil {
		t.Fatal(err)
	}
	startTestSearch(t, model, "changed")
	if model.foreground.searchOperation() != 0 {
		t.Fatal("saturated search admission left an active operation")
	}
	if summary := model.review.SearchSummary(); summary.Status != change.SearchFailed ||
		summary.Detail.String() != ErrCoordinatorSaturated.Error() {
		t.Fatalf("saturated search summary = %+v", summary)
	}
}

func TestOperationIdentityExhaustionLeavesForegroundIdle(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	model.nextOperation = 0
	startTestSearch(t, model, "changed")
	if model.foreground.searchOperation() != 0 ||
		model.review.SearchSummary().Status != change.SearchIdle {
		t.Fatal("operation identity exhaustion changed foreground state")
	}
	if model.notice == nil || model.notice.Text != "operation identity exhausted" {
		t.Fatalf("identity exhaustion notice = %+v", model.notice)
	}
}

func TestStaleSearchOutcomeDoesNotConsumeActiveSearch(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	startTestSearch(t, model, "changed")
	active := model.foreground.searchOperation()
	if active == 0 {
		t.Fatal("search was not admitted")
	}
	stale, _ := change.NewOperationID(active.Uint64() + 100)
	model.applySearchOutcome(SearchOutcome{ID: stale})
	if model.foreground.searchOperation() != active {
		t.Fatal("stale search outcome consumed the active search")
	}
	model.cancelSearch()
	settleModelWork(t, model, coordinator)
}

func startTestSearch(t *testing.T, model *Model, query string) {
	t.Helper()
	model.startSearch(query, model.fileOrder)
}
