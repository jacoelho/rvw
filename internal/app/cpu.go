package app

import (
	"context"
	"errors"
)

func (coordinator *Coordinator) runCPU(ctx context.Context, work func() error) error {
	if work == nil {
		return errors.New("CPU work is nil")
	}
	if !coordinator.acquireCPU(ctx) {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		return context.Canceled
	}
	defer coordinator.releaseCPU()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return work()
}

func (coordinator *Coordinator) acquireCPU(ctx context.Context) bool {
	select {
	case coordinator.cpuPermits <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (coordinator *Coordinator) releaseCPU() {
	<-coordinator.cpuPermits
}
