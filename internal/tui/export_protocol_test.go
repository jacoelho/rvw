package tui

import (
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func TestMismatchedExportOutcomeDoesNotConsumeActiveExport(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	id, err := model.nextOperationID()
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = model.review.BeginOperation(id, change.OperationExport)
	if err != nil {
		t.Fatal(err)
	}
	model.exportOperations[id] = exportRequest{
		format:      change.ExportMarkdown,
		destination: destinationStdout,
	}
	prepared, err := model.review.PrepareMarkdown(
		model.settings.exportTemplates,
		change.DefaultExportOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}

	model.applyExportOutcome(ExportOutcome{
		ID:          id,
		Prepared:    prepared,
		Destination: destinationClipboard,
	})
	if request, exists := model.exportOperations[id]; !exists ||
		request.destination != destinationStdout {
		t.Fatalf("active export = %+v, exists %v", request, exists)
	}
	if operation := model.review.Operations()[id]; operation.Status != change.OperationPending {
		t.Fatalf("export operation = %+v, want pending", operation)
	}

	command := model.applyExportOutcome(ExportOutcome{
		ID:          id,
		Prepared:    prepared,
		Destination: destinationStdout,
	})
	if command == nil {
		t.Fatal("matching stdout outcome did not initiate quit")
	}
	if _, exists := model.exportOperations[id]; exists {
		t.Fatal("settled export remained tracked")
	}
	if operation := model.review.Operations()[id]; operation.Status != change.OperationSucceeded {
		t.Fatalf("export operation = %+v, want succeeded", operation)
	}
}
