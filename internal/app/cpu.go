package app

import "context"

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
