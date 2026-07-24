package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

func TestCoordinatorDirectOutcomeFamiliesRetainUntilClaim(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	testCases := []struct {
		outcome func(review.OperationID) Outcome
		name    string
	}{
		{name: "patch", outcome: func(id review.OperationID) Outcome { return PatchOutcome{ID: id} }},
		{name: searchLabel, outcome: func(id review.OperationID) Outcome { return SearchOutcome{ID: id} }},
		{name: "syntax", outcome: func(id review.OperationID) Outcome { return SyntaxOutcome{ID: id} }},
		{
			name: "publication check",
			outcome: func(id review.OperationID) Outcome {
				return PublicationCheckOutcome{ID: id}
			},
		},
		{
			name: "publication commit",
			outcome: func(id review.OperationID) Outcome {
				return PublicationCommitOutcome{ID: id}
			},
		},
		{
			name: "reconciliation",
			outcome: func(id review.OperationID) Outcome {
				return ReconciliationOutcome{ID: id}
			},
		},
		{
			name: "reload acquired",
			outcome: func(id review.OperationID) Outcome {
				return ReloadAcquiredOutcome{ID: id}
			},
		},
		{
			name: "reload prepared",
			outcome: func(id review.OperationID) Outcome {
				return ReloadPreparedOutcome{ID: id}
			},
		},
		{
			name: "file tree",
			outcome: func(id review.OperationID) Outcome {
				return FileTreeOutcome{ID: id}
			},
		},
		{
			name: "workspace index",
			outcome: func(id review.OperationID) Outcome {
				return WorkspaceIndexOutcome{ID: id}
			},
		},
		{name: exportLabel, outcome: func(id review.OperationID) Outcome { return ExportOutcome{ID: id} }},
		{
			name: "delivery",
			outcome: func(id review.OperationID) Outcome {
				return DeliveryOutcome{ID: id}
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			coordinator := newShutdownTestCoordinator(t, fixture)
			id, _ := review.NewOperationID(1)
			if err := coordinator.submit(id, 1, 1, func(context.Context) bool {
				return true
			}); err != nil {
				t.Fatal(err)
			}
			want := testCase.outcome(id)
			coordinator.complete(want)
			if stats := coordinator.Stats(); stats.Accepted != 1 || stats.Retained != 1 {
				t.Fatalf("stats before claim = %+v", stats)
			}
			got, ok := coordinator.ClaimNext()
			if !ok || reflect.TypeOf(got) != reflect.TypeOf(want) {
				t.Fatalf("claim = (%T, %v), want %T", got, ok, want)
			}
			assertCoordinatorReleased(t, coordinator)
			coordinator.complete(want)
			if _, ok := coordinator.ClaimNext(); ok {
				t.Fatal("duplicate terminal completion was retained")
			}
			_ = shutdownTestCoordinator(t, coordinator)
		})
	}
}

func TestShutdownClosesDocumentsFromDirectReloadVariants(t *testing.T) {
	testCases := []struct {
		outcome func(review.OperationID, *source.Document) Outcome
		name    string
	}{
		{
			name: "acquired",
			outcome: func(id review.OperationID, document *source.Document) Outcome {
				return ReloadAcquiredOutcome{ID: id, Document: document}
			},
		},
		{
			name: "prepared",
			outcome: func(id review.OperationID, document *source.Document) Outcome {
				return ReloadPreparedOutcome{ID: id, Document: document}
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
			coordinator := newShutdownTestCoordinator(t, fixture)
			document, workspace := openTrackedReloadDocument(t, fixture)
			id, _ := review.NewOperationID(1)
			if err := coordinator.submit(id, 1, 1, func(context.Context) bool {
				return true
			}); err != nil {
				t.Fatal(err)
			}
			coordinator.complete(testCase.outcome(id, document))

			settlement := shutdownTestCoordinator(t, coordinator)
			if len(settlement.Publications) != 0 {
				t.Fatalf("reload settlement publications = %d", len(settlement.Publications))
			}
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("reload workspace still exists after shutdown: %s (%v)", workspace, err)
			}
			assertCoordinatorReleased(t, coordinator)
		})
	}
}

func TestShutdownDirectPublicationVariants(t *testing.T) {
	target := testPublicationTarget(t)
	marker, err := review.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	if err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		outcome            func(review.OperationID) Outcome
		publication        *publicationReservation
		name               string
		wantFacts          int
		wantDisposition    review.PublicationDisposition
		wantReconciliation review.ReconciliationDisposition
	}{
		{
			name: "check only",
			outcome: func(id review.OperationID) Outcome {
				return PublicationCheckOutcome{ID: id}
			},
		},
		{
			name: "committed",
			outcome: func(id review.OperationID) Outcome {
				return PublicationCommitOutcome{
					ID:          id,
					Disposition: review.PublicationDispositionCommitted,
					Detail:      boundedOperationDetail("committed"),
				}
			},
			publication:     &publicationReservation{target: target, marker: marker},
			wantDisposition: review.PublicationDispositionCommitted,
			wantFacts:       1,
		},
		{
			name: "failed before commit",
			outcome: func(id review.OperationID) Outcome {
				return PublicationCommitOutcome{
					ID:          id,
					Disposition: review.PublicationDispositionFailedBeforeCommit,
					Detail:      boundedOperationDetail("failed"),
				}
			},
			publication:     &publicationReservation{target: target, marker: marker},
			wantDisposition: review.PublicationDispositionFailedBeforeCommit,
			wantFacts:       1,
		},
		{
			name: "cancelled before commit",
			outcome: func(id review.OperationID) Outcome {
				return PublicationCommitOutcome{
					ID:          id,
					Disposition: review.PublicationDispositionCancelledBeforeCommit,
					Detail:      boundedOperationDetail("cancelled"),
				}
			},
			publication:     &publicationReservation{target: target, marker: marker},
			wantDisposition: review.PublicationDispositionCancelledBeforeCommit,
			wantFacts:       1,
		},
		{
			name: "unknown",
			outcome: func(id review.OperationID) Outcome {
				return PublicationCommitOutcome{
					ID:          id,
					Disposition: review.PublicationDispositionUnknown,
					Detail:      boundedOperationDetail("unknown"),
				}
			},
			publication:     &publicationReservation{target: target, marker: marker},
			wantDisposition: review.PublicationDispositionUnknown,
			wantFacts:       1,
		},
		{
			name: "reconciliation committed",
			outcome: func(id review.OperationID) Outcome {
				return ReconciliationOutcome{
					ID:          id,
					Disposition: review.ReconciliationCommitted,
					Detail:      boundedOperationDetail("found"),
				}
			},
			publication:        &publicationReservation{target: target, marker: marker},
			wantReconciliation: review.ReconciliationCommitted,
			wantFacts:          1,
		},
		{
			name: "reconciliation absent",
			outcome: func(id review.OperationID) Outcome {
				return ReconciliationOutcome{
					ID:          id,
					Disposition: review.ReconciliationNotCommitted,
					Detail:      boundedOperationDetail("absent"),
				}
			},
			publication:        &publicationReservation{target: target, marker: marker},
			wantReconciliation: review.ReconciliationNotCommitted,
			wantFacts:          1,
		},
		{
			name: "reconciliation still unknown",
			outcome: func(id review.OperationID) Outcome {
				return ReconciliationOutcome{
					ID:          id,
					Disposition: review.ReconciliationStillUnknown,
					Detail:      boundedOperationDetail("still unknown"),
				}
			},
			publication:        &publicationReservation{target: target, marker: marker},
			wantReconciliation: review.ReconciliationStillUnknown,
			wantFacts:          1,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
			coordinator := newShutdownTestCoordinator(t, fixture)
			id, _ := review.NewOperationID(1)
			if err := coordinator.submitReservedPublication(
				id,
				1,
				1,
				testCase.publication,
				nil,
				func(context.Context) bool { return true },
			); err != nil {
				t.Fatal(err)
			}
			coordinator.complete(testCase.outcome(id))

			settlement := shutdownTestCoordinator(t, coordinator)
			if len(settlement.Publications) != testCase.wantFacts {
				t.Fatalf("publication facts = %d, want %d", len(settlement.Publications), testCase.wantFacts)
			}
			if testCase.wantFacts != 0 {
				fact := settlement.Publications[0]
				if fact.Target != target || fact.Marker != marker ||
					fact.Disposition != testCase.wantDisposition ||
					fact.Reconciliation != testCase.wantReconciliation {
					t.Fatalf("publication fact = %+v", fact)
				}
			}
			assertCoordinatorReleased(t, coordinator)

			second := shutdownTestCoordinator(t, coordinator)
			if len(second.Publications) != 0 {
				t.Fatalf("second shutdown repeated %d facts", len(second.Publications))
			}
		})
	}
}

func TestTimedOutShutdownSettlementFencesActivePublicationAsUnknown(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	coordinator := newShutdownTestCoordinator(t, fixture)
	id, _ := review.NewOperationID(1)
	marker, err := review.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	if err != nil {
		t.Fatal(err)
	}
	target := testPublicationTarget(t)
	if err := coordinator.submitReservedPublication(
		id,
		1,
		1,
		&publicationReservation{target: target, marker: marker},
		nil,
		func(context.Context) bool { return true },
	); err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	settlement := coordinator.shutdownSettlementLocked(settleActivePublicationAsUnknown)
	coordinator.mu.Unlock()
	if len(settlement.Publications) != 1 {
		t.Fatalf("active publication facts = %d, want 1", len(settlement.Publications))
	}
	fact := settlement.Publications[0]
	if fact.Target != target || fact.Marker != marker ||
		fact.Disposition != review.PublicationDispositionUnknown {
		t.Fatalf("active publication fact = %+v", fact)
	}
	_ = shutdownTestCoordinator(t, coordinator)
}

func newShutdownTestCoordinator(t *testing.T, fixture sourceFixture) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(
		t.Context(),
		fixture.service,
		newTestDeliveryManager(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func shutdownTestCoordinator(t *testing.T, coordinator *Coordinator) ShutdownSettlement {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	settlement, err := coordinator.ShutdownWithSettlement(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return settlement
}

func openTrackedReloadDocument(
	t *testing.T,
	fixture sourceFixture,
) (*source.Document, string) {
	t.Helper()
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	pattern := filepath.Join(temporaryRoot, "rvw-source-*")
	document, err := fixture.service.Open(
		t.Context(),
		source.WorktreeTarget(),
		source.OpenOptions{
			IncludeUntracked: true,
			BuiltinDefaults:  true,
			Gitignore:        true,
			RVWIgnore:        true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaces, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("source workspaces = %v, want exactly one", workspaces)
	}
	return document, workspaces[0]
}

func testPublicationTarget(t *testing.T) review.PublicationTarget {
	t.Helper()
	base, err := review.NewObjectID("1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	head, err := review.NewObjectID("2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	target, err := review.NewPublicationTarget(review.PublicationTargetDescriptor{
		Repository:  "owner/repository",
		PullRequest: 7,
		Base:        base,
		Head:        head,
	})
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func assertCoordinatorReleased(t *testing.T, coordinator *Coordinator) {
	t.Helper()
	stats := coordinator.Stats()
	if stats.Accepted != 0 || stats.Retained != 0 || stats.RequestBytes != 0 ||
		stats.ReservedResult != 0 {
		t.Fatalf("coordinator retained accounting after shutdown: %+v", stats)
	}
	if _, ok := coordinator.ClaimNext(); ok {
		t.Fatal("coordinator retained an outcome after shutdown")
	}
}
