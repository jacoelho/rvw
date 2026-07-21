package app

import (
	"testing"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/review"
)

func TestMismatchedExportOutcomeDoesNotConsumeActiveExport(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	id, err := model.nextOperationID()
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = model.review.BeginOperation(id, review.OperationExport)
	if err != nil {
		t.Fatal(err)
	}
	model.exportOperations[id] = exportRequest{
		format:      review.ExportMarkdown,
		destination: delivery.DestinationStdout,
	}
	prepared, err := model.review.PrepareMarkdown(
		model.settings.exportTemplates,
		review.ExportOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}

	model.applyExportOutcome(ExportOutcome{
		ID:          id,
		Prepared:    prepared,
		Destination: delivery.DestinationClipboard,
	})
	if request, exists := model.exportOperations[id]; !exists ||
		request.destination != delivery.DestinationStdout {
		t.Fatalf("active export = %+v, exists %v", request, exists)
	}
	if operation := model.review.Operations()[id]; operation.Status != review.OperationPending {
		t.Fatalf("export operation = %+v, want pending", operation)
	}

	command := model.applyExportOutcome(ExportOutcome{
		ID:          id,
		Prepared:    prepared,
		Destination: delivery.DestinationStdout,
	})
	if command == nil {
		t.Fatal("matching stdout outcome did not initiate quit")
	}
	if _, exists := model.exportOperations[id]; exists {
		t.Fatal("settled export remained tracked")
	}
	if operation := model.review.Operations()[id]; operation.Status != review.OperationSucceeded {
		t.Fatalf("export operation = %+v, want succeeded", operation)
	}
}
