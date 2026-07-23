package app

import (
	"context"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

func TestSubmitSyntaxChargesRequestCapacitiesAndConstructionPeak(t *testing.T) {
	content, err := review.NewObjectID(strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	selector, err := review.NewSyntaxSelector("go")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := review.NewSyntaxIdentity(content, selector)
	if err != nil {
		t.Fatal(err)
	}
	id, err := review.NewOperationID(1)
	if err != nil {
		t.Fatal(err)
	}
	demands := make([]review.SyntaxDemand, 1, 2)
	demands[0] = review.SyntaxDemand{
		Identity: identity,
		Lines:    make([]review.LineNumber, 1, 4),
		Side:     review.LineNew,
	}
	request := review.SyntaxRequest{
		Demands: demands, Operation: id, Workspace: 2, Source: 3, File: 0,
	}
	coordinator := &Coordinator{
		workContext: context.Background(),
		syntaxJobs:  make(chan syntaxJob, 1),
		accepted:    make(map[review.OperationID]reservation),
		progress:    make(map[review.OperationID]Progress),
		required:    newRequiredScheduler(),
	}
	coordinator.stats.Store(CoordinatorStats{})
	if err := coordinator.SubmitSyntax(SyntaxWork{
		Document: &source.Document{}, Request: request, ID: id,
	}); err != nil {
		t.Fatal(err)
	}
	stats := coordinator.Stats()
	work := SyntaxWork{Document: &source.Document{}, Request: request, ID: id}
	if stats.RequestBytes != work.requestBytes() {
		t.Fatalf("request bytes = %d, want %d", stats.RequestBytes, work.requestBytes())
	}
	if stats.ReservedResult != syntaxResultConstructionBytes {
		t.Fatalf(
			"result reservation = %d, want %d",
			stats.ReservedResult,
			syntaxResultConstructionBytes,
		)
	}
	if !coordinator.Cancel(id) {
		t.Fatal("syntax reservation was not cancellable")
	}
}
