package app

import (
	"context"
	"testing"
	"time"
)

func settleModelWork(t *testing.T, model *Model, coordinator *Coordinator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for attempts := 0; coordinator.Stats().Accepted != 0 && attempts < 32; attempts++ {
		if state := coordinator.WaitReady(ctx); state != ReadyAvailable {
			t.Fatalf("coordinator readiness = %v", state)
		}
		_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	}
	if stats := coordinator.Stats(); stats.Accepted != 0 || stats.Retained != 0 {
		t.Fatalf("coordinator did not settle: %+v", stats)
	}
}
