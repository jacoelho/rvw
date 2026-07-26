package tui

import (
	"context"
)

func (coordinator *Coordinator) runCPU(ctx context.Context, work func() error) error {
	return coordinator.cpu.Run(ctx, work)
}

func (coordinator *Coordinator) acquireCPU(ctx context.Context) bool {
	return coordinator.cpu.Acquire(ctx)
}

func (coordinator *Coordinator) releaseCPU() {
	coordinator.cpu.Release()
}
