package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
	workcoord "github.com/jacoelho/rvw/internal/coordinator"
)

func TestSubmitSyntaxChargesRequestCapacitiesAndConstructionPeak(t *testing.T) {
	content, err := change.NewObjectID(strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	selector, err := change.NewSyntaxSelector("go")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := change.NewSyntaxIdentity(content, selector)
	if err != nil {
		t.Fatal(err)
	}
	id, err := change.NewOperationID(1)
	if err != nil {
		t.Fatal(err)
	}
	demands := make([]change.SyntaxDemand, 1, 2)
	demands[0] = change.SyntaxDemand{
		Identity: identity,
		Lines:    make([]change.LineNumber, 1, 4),
		Side:     change.LineNew,
	}
	request := change.SyntaxRequest{
		Demands: demands, Operation: id, Workspace: 2, Source: 3, File: 0,
	}
	coordinator := &Coordinator{
		workContext: context.Background(),
		syntaxJobs:  make(chan syntaxJob, 1),
		accepted:    make(map[change.OperationID]reservation),
		progress:    make(map[change.OperationID]Progress),
		required:    workcoord.NewScheduler(),
	}
	coordinator.stats.Store(CoordinatorStats{})
	if err := coordinator.SubmitSyntax(SyntaxWork{
		Document: &workcoord.SourceRevision{}, Request: request, ID: id,
	}); err != nil {
		t.Fatal(err)
	}
	stats := coordinator.Stats()
	work := SyntaxWork{Document: &workcoord.SourceRevision{}, Request: request, ID: id}
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
