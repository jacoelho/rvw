package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/change"
	workcoord "github.com/jacoelho/rvw/internal/coordinator"
	"github.com/jacoelho/rvw/internal/source"
)

func TestCoordinatorDirectOutcomeFamiliesRetainUntilClaim(t *testing.T) {
	fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
	testCases := []struct {
		outcome func(change.OperationID) Outcome
		name    string
	}{
		{name: "patch", outcome: func(id change.OperationID) Outcome { return PatchOutcome{ID: id} }},
		{name: searchLabel, outcome: func(id change.OperationID) Outcome { return SearchOutcome{ID: id} }},
		{name: "syntax", outcome: func(id change.OperationID) Outcome { return SyntaxOutcome{ID: id} }},
		{
			name: "publication check",
			outcome: func(id change.OperationID) Outcome {
				return PublicationCheckOutcome{ID: id}
			},
		},
		{
			name: "publication commit",
			outcome: func(id change.OperationID) Outcome {
				return PublicationCommitOutcome{ID: id}
			},
		},
		{
			name: "reconciliation",
			outcome: func(id change.OperationID) Outcome {
				return ReconciliationOutcome{ID: id}
			},
		},
		{
			name: "reload acquired",
			outcome: func(id change.OperationID) Outcome {
				return ReloadAcquiredOutcome{ID: id}
			},
		},
		{
			name: "reload prepared",
			outcome: func(id change.OperationID) Outcome {
				return ReloadPreparedOutcome{ID: id}
			},
		},
		{
			name: "file tree",
			outcome: func(id change.OperationID) Outcome {
				return FileTreeOutcome{ID: id}
			},
		},
		{
			name: "workspace index",
			outcome: func(id change.OperationID) Outcome {
				return WorkspaceIndexOutcome{ID: id}
			},
		},
		{name: exportLabel, outcome: func(id change.OperationID) Outcome { return ExportOutcome{ID: id} }},
		{
			name: "delivery",
			outcome: func(id change.OperationID) Outcome {
				return DeliveryOutcome{ID: id}
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			coordinator := newShutdownTestCoordinator(t, fixture)
			id, _ := change.NewOperationID(1)
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
		outcome func(change.OperationID, *workcoord.SourceRevision) Outcome
		name    string
	}{
		{
			name: "acquired",
			outcome: func(id change.OperationID, document *workcoord.SourceRevision) Outcome {
				return ReloadAcquiredOutcome{ID: id, Document: document}
			},
		},
		{
			name: "prepared",
			outcome: func(id change.OperationID, document *workcoord.SourceRevision) Outcome {
				return ReloadPreparedOutcome{ID: id, Document: document}
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
			coordinator := newShutdownTestCoordinator(t, fixture)
			document, workspace := openTrackedReloadDocument(t, fixture)
			id, _ := change.NewOperationID(1)
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
	marker, err := change.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	if err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		outcome            func(change.OperationID) Outcome
		publication        *publicationReservation
		name               string
		wantFacts          int
		wantDisposition    change.PublicationDisposition
		wantReconciliation change.ReconciliationDisposition
	}{
		{
			name: "check only",
			outcome: func(id change.OperationID) Outcome {
				return PublicationCheckOutcome{ID: id}
			},
		},
		{
			name: "committed",
			outcome: func(id change.OperationID) Outcome {
				return PublicationCommitOutcome{
					ID:          id,
					Disposition: change.PublicationDispositionCommitted,
					Detail:      boundedOperationDetail("committed"),
				}
			},
			publication:     &publicationReservation{target: target, marker: marker},
			wantDisposition: change.PublicationDispositionCommitted,
			wantFacts:       1,
		},
		{
			name: "failed before commit",
			outcome: func(id change.OperationID) Outcome {
				return PublicationCommitOutcome{
					ID:          id,
					Disposition: change.PublicationDispositionFailedBeforeCommit,
					Detail:      boundedOperationDetail("failed"),
				}
			},
			publication:     &publicationReservation{target: target, marker: marker},
			wantDisposition: change.PublicationDispositionFailedBeforeCommit,
			wantFacts:       1,
		},
		{
			name: "cancelled before commit",
			outcome: func(id change.OperationID) Outcome {
				return PublicationCommitOutcome{
					ID:          id,
					Disposition: change.PublicationDispositionCancelledBeforeCommit,
					Detail:      boundedOperationDetail("cancelled"),
				}
			},
			publication:     &publicationReservation{target: target, marker: marker},
			wantDisposition: change.PublicationDispositionCancelledBeforeCommit,
			wantFacts:       1,
		},
		{
			name: "unknown",
			outcome: func(id change.OperationID) Outcome {
				return PublicationCommitOutcome{
					ID:          id,
					Disposition: change.PublicationDispositionUnknown,
					Detail:      boundedOperationDetail("unknown"),
				}
			},
			publication:     &publicationReservation{target: target, marker: marker},
			wantDisposition: change.PublicationDispositionUnknown,
			wantFacts:       1,
		},
		{
			name: "reconciliation committed",
			outcome: func(id change.OperationID) Outcome {
				return ReconciliationOutcome{
					ID:          id,
					Disposition: change.ReconciliationCommitted,
					Detail:      boundedOperationDetail("found"),
				}
			},
			publication:        &publicationReservation{target: target, marker: marker},
			wantReconciliation: change.ReconciliationCommitted,
			wantFacts:          1,
		},
		{
			name: "reconciliation absent",
			outcome: func(id change.OperationID) Outcome {
				return ReconciliationOutcome{
					ID:          id,
					Disposition: change.ReconciliationNotCommitted,
					Detail:      boundedOperationDetail("absent"),
				}
			},
			publication:        &publicationReservation{target: target, marker: marker},
			wantReconciliation: change.ReconciliationNotCommitted,
			wantFacts:          1,
		},
		{
			name: "reconciliation still unknown",
			outcome: func(id change.OperationID) Outcome {
				return ReconciliationOutcome{
					ID:          id,
					Disposition: change.ReconciliationStillUnknown,
					Detail:      boundedOperationDetail("still unknown"),
				}
			},
			publication:        &publicationReservation{target: target, marker: marker},
			wantReconciliation: change.ReconciliationStillUnknown,
			wantFacts:          1,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSourceFixture(t, map[string]string{"alpha.txt": "changed\n"})
			coordinator := newShutdownTestCoordinator(t, fixture)
			id, _ := change.NewOperationID(1)
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
	id, _ := change.NewOperationID(1)
	marker, err := change.NewPublicationMarker(
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
		fact.Disposition != change.PublicationDispositionUnknown {
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
) (*workcoord.SourceRevision, string) {
	t.Helper()
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	pattern := filepath.Join(temporaryRoot, "rvw-source-*")
	opened, revision, err := fixture.service.Open(
		t.Context(), source.OpenRequest{
			Target: source.WorktreeTarget(),
			Options: source.OpenOptions{
				IncludeUntracked: true,
				BuiltinDefaults:  true,
				Gitignore:        true,
				RVWIgnore:        true,
			},
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
	return workcoord.NewSourceRevision(opened, revision), workspaces[0]
}

func testPublicationTarget(t *testing.T) change.PublicationTarget {
	t.Helper()
	base, err := change.NewObjectID("1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	head, err := change.NewObjectID("2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	target, err := change.NewPublicationTarget(change.PublicationTargetDescriptor{
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
