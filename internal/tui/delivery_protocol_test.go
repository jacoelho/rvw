package tui

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/delivery"
)

func TestDeliveryOutcomeFencingAndSettlement(t *testing.T) {
	testCases := []struct {
		err        error
		name       string
		outcome    delivery.Outcome
		wantStatus change.OperationStatus
	}{
		{
			name: "success",
			outcome: delivery.Outcome{
				State: delivery.CommitSucceeded,
			},
			wantStatus: change.OperationSucceeded,
		},
		{
			name: "failed before write",
			outcome: delivery.Outcome{
				State: delivery.CommitFailedBeforeWrite,
			},
			err:        errors.New("clipboard failed before write"),
			wantStatus: change.OperationFailed,
		},
		{
			name: "unknown",
			outcome: delivery.Outcome{
				State: delivery.CommitUnknown,
			},
			err:        delivery.ErrUnknown,
			wantStatus: change.OperationUnknown,
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
			if testCase.outcome.State == delivery.CommitUnknown &&
				(model.notice == nil || !strings.Contains(string(model.notice.Text), "fenced")) {
				t.Fatalf("unknown delivery notice = %+v", model.notice)
			}
			model.applyDeliveryOutcome(DeliveryOutcome{ID: id, Outcome: testCase.outcome})
			if model.review.Operations()[id].Status != testCase.wantStatus {
				t.Fatal("duplicate delivery outcome changed terminal status")
			}
		})
	}
}

func TestDeliveryAdmissionFailureLeavesNoPhantomOperation(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	prepared, err := model.review.PrepareMarkdown(
		model.settings.exportTemplates,
		change.DefaultExportOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := change.NewOperationID(model.nextOperation)
	coordinator.StopAdmissionAndCancel()
	model.startDelivery(prepared)
	if _, exists := model.deliveryOperations[id]; exists {
		t.Fatal("failed delivery admission remained tracked")
	}
	operation := model.review.Operations()[id]
	if operation.Status != change.OperationFailed ||
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
		change.DefaultExportOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}

	firstID, _ := change.NewOperationID(model.nextOperation)
	model.startDelivery(prepared)
	settleModelWork(t, model, coordinator)
	if operation := model.review.Operations()[firstID]; operation.Status != change.OperationUnknown {
		t.Fatalf("ambiguous delivery operation = %+v", operation)
	}
	if _, tracked := model.deliveryOperations[firstID]; tracked {
		t.Fatal("ambiguous delivery remained tracked after settlement")
	}

	retryID, _ := change.NewOperationID(model.nextOperation)
	model.startDelivery(prepared)
	settleModelWork(t, model, coordinator)
	if operation := model.review.Operations()[retryID]; operation.Status != change.OperationFailed ||
		operation.Detail.String() != delivery.ErrUnknown.Error() {
		t.Fatalf("fenced retry operation = %+v", operation)
	}
	if _, tracked := model.deliveryOperations[retryID]; tracked {
		t.Fatal("fenced retry remained tracked after settlement")
	}
}

func beginTestDeliveryOperation(t *testing.T, model *Model) change.OperationID {
	t.Helper()
	id, err := model.nextOperationID()
	if err != nil {
		t.Fatal(err)
	}
	next, err := model.review.BeginOperation(id, change.OperationDelivery)
	if err != nil {
		t.Fatal(err)
	}
	model.review = next
	model.deliveryOperations[id] = struct{}{}
	return id
}
