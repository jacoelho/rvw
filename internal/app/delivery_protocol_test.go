package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/review"
)

func TestDeliveryOutcomeFencingAndSettlement(t *testing.T) {
	testCases := []struct {
		name       string
		outcome    delivery.Outcome
		err        error
		wantStatus review.OperationStatus
	}{
		{
			name: "success",
			outcome: delivery.Outcome{
				Destination: delivery.DestinationClipboard,
				State:       delivery.CommitSucceeded,
			},
			wantStatus: review.OperationSucceeded,
		},
		{
			name: "failed before write",
			outcome: delivery.Outcome{
				Destination: delivery.DestinationClipboard,
				State:       delivery.CommitFailedBeforeWrite,
			},
			err:        errors.New("clipboard failed before write"),
			wantStatus: review.OperationFailed,
		},
		{
			name: "unknown",
			outcome: delivery.Outcome{
				Destination: delivery.DestinationClipboard,
				State:       delivery.CommitUnknown,
			},
			err:        delivery.ErrUnknown,
			wantStatus: review.OperationUnknown,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			settleModelWork(t, model, coordinator)
			id := beginTestDeliveryOperation(t, model)
			model.applyDeliveryOutcome(DeliveryOutcome{
				ID:      id,
				Outcome: testCase.outcome,
				Err:     testCase.err,
			})
			operation := model.review.Operations()[id]
			if operation.Status != testCase.wantStatus {
				t.Fatalf("delivery operation = %+v, want status %v", operation, testCase.wantStatus)
			}
			if _, exists := model.deliveryOperations[id]; exists {
				t.Fatal("settled delivery remained tracked")
			}
			model.applyDeliveryOutcome(DeliveryOutcome{ID: id, Outcome: testCase.outcome})
			if model.review.Operations()[id].Status != testCase.wantStatus {
				t.Fatal("duplicate delivery outcome changed terminal status")
			}
		})
	}
}

func TestMismatchedDeliveryOutcomeDoesNotConsumeActiveDelivery(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	id := beginTestDeliveryOperation(t, model)
	model.applyDeliveryOutcome(DeliveryOutcome{
		ID: id,
		Outcome: delivery.Outcome{
			Destination: delivery.DestinationStdout,
			State:       delivery.CommitSucceeded,
		},
	})
	if destination, exists := model.deliveryOperations[id]; !exists ||
		destination != delivery.DestinationClipboard {
		t.Fatalf("active delivery = %v, exists %v", destination, exists)
	}
	if operation := model.review.Operations()[id]; operation.Status != review.OperationPending {
		t.Fatalf("delivery operation = %+v, want pending", operation)
	}
}

func TestDeliveryAdmissionFailureLeavesNoPhantomOperation(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	prepared, err := model.review.PrepareMarkdown(
		model.settings.exportTemplates,
		review.ExportOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := review.NewOperationID(model.nextOperation)
	coordinator.StopAdmissionAndCancel()
	model.startDelivery(prepared, delivery.DestinationClipboard)
	if _, exists := model.deliveryOperations[id]; exists {
		t.Fatal("failed delivery admission remained tracked")
	}
	operation := model.review.Operations()[id]
	if operation.Status != review.OperationFailed ||
		operation.Detail.String() != ErrCoordinatorClosed.Error() {
		t.Fatalf("failed delivery operation = %+v", operation)
	}
}

func TestFencedDeliveryRetrySettlesThroughWorker(t *testing.T) {
	directory := t.TempDir()
	commandName := "pbcopy"
	if runtime.GOOS == "linux" {
		commandName = "wl-copy"
		t.Setenv("WAYLAND_DISPLAY", "rvw-test")
	}
	if err := os.WriteFile(
		filepath.Join(directory, commandName),
		[]byte("#!/bin/sh\nexit 1\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))

	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	prepared, err := model.review.PrepareMarkdown(
		model.settings.exportTemplates,
		review.ExportOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}

	firstID, _ := review.NewOperationID(model.nextOperation)
	model.startDelivery(prepared, delivery.DestinationClipboard)
	settleModelWork(t, model, coordinator)
	if operation := model.review.Operations()[firstID]; operation.Status != review.OperationUnknown {
		t.Fatalf("ambiguous delivery operation = %+v", operation)
	}
	if _, tracked := model.deliveryOperations[firstID]; tracked {
		t.Fatal("ambiguous delivery remained tracked after settlement")
	}

	retryID, _ := review.NewOperationID(model.nextOperation)
	model.startDelivery(prepared, delivery.DestinationClipboard)
	settleModelWork(t, model, coordinator)
	if operation := model.review.Operations()[retryID]; operation.Status != review.OperationFailed ||
		operation.Detail.String() != delivery.ErrUnknown.Error() {
		t.Fatalf("fenced retry operation = %+v", operation)
	}
	if _, tracked := model.deliveryOperations[retryID]; tracked {
		t.Fatal("fenced retry remained tracked after settlement")
	}
}

func beginTestDeliveryOperation(t *testing.T, model *Model) review.OperationID {
	t.Helper()
	id, err := model.nextOperationID()
	if err != nil {
		t.Fatal(err)
	}
	next, err := model.review.BeginOperation(id, review.OperationDelivery)
	if err != nil {
		t.Fatal(err)
	}
	model.review = next
	model.deliveryOperations[id] = delivery.DestinationClipboard
	return id
}
