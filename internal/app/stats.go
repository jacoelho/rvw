package app

type CoordinatorStats struct {
	Accepted       int
	RequestBytes   int64
	ReservedResult int64
	Retained       int
	IOWorkers      int
	CPUWorkers     int
}

func (coordinator *Coordinator) Stats() CoordinatorStats {
	return coordinator.stats.Load().(CoordinatorStats)
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
