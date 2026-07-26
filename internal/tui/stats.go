package tui

// CoordinatorStats represents coordinator stats.
type CoordinatorStats struct {
	Accepted       int
	RequestBytes   int64
	ReservedResult int64
	Retained       int
	IOWorkers      int
	CPUWorkers     int
}

// Stats returns the receiver's stats value.
func (coordinator *Coordinator) Stats() CoordinatorStats {
	stats, ok := coordinator.stats.Load().(CoordinatorStats)
	if !ok {
		panic("coordinator statistics are not initialized")
	}
	return stats
}

func (coordinator *Coordinator) updateStatsLocked() {
	coordinator.stats.Store(CoordinatorStats{
		Accepted:       len(coordinator.accepted),
		RequestBytes:   coordinator.requestBytes,
		ReservedResult: coordinator.reservedResult,
		Retained:       len(coordinator.outcomes),
		IOWorkers:      coordinator.ioCount,
		CPUWorkers:     coordinator.cpuCount,
	})
}
