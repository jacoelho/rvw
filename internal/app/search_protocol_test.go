package app

import (
	"context"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
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
		if summary.Query.String() != "absent" || summary.Status != review.SearchComplete ||
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
		if summary := model.review.SearchSummary(); summary.Status != review.SearchIdle {
			t.Fatalf("cleared search summary = %+v", summary)
		}
		if model.searchCursor != 0 {
			t.Fatalf("search cursor = %d, want 0", model.searchCursor)
		}
	})
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
	if summary.Status != review.SearchFailed ||
		summary.Detail.String() != ErrCoordinatorClosed.Error() {
		t.Fatalf("failed search summary = %+v", summary)
	}
}

func TestSearchSaturationLeavesNoPhantomOperation(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	filler, _ := review.NewOperationID(10_000)
	if err := coordinator.submit(
		filler,
		MaxQueuedRequestBytes-reservedPublicationRequestBytes,
		MaxRetainedResultBytes-reservedPublicationResultBytes,
		func(context.Context) bool { return true },
	); err != nil {
		t.Fatal(err)
	}
	startTestSearch(t, model, "changed")
	if model.foreground.searchOperation() != 0 {
		t.Fatal("saturated search admission left an active operation")
	}
	if summary := model.review.SearchSummary(); summary.Status != review.SearchFailed ||
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
		model.review.SearchSummary().Status != review.SearchIdle {
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
	stale, _ := review.NewOperationID(active.Uint64() + 100)
	model.applySearchOutcome(SearchOutcome{ID: stale})
	if model.foreground.searchOperation() != active {
		t.Fatal("stale search outcome consumed the active search")
	}
	model.cancelSearch()
	settleModelWork(t, model, coordinator)
}

func startTestSearch(t *testing.T, model *Model, query string) {
	t.Helper()
	target, err := review.NewSearchTarget(model.review.Snapshot(), model.fileOrder)
	if err != nil {
		t.Fatal(err)
	}
	model.startSearch(query, target)
}
