package tui

import (
	"errors"
	"time"

	"github.com/jacoelho/rvw/internal/change"
)

// MaxProgressBytes defines the supported progress bytes limit.
const (
	MaxProgressBytes          int64 = 256 << 10
	MaxOperationProgressBytes int64 = 8 << 10
)

// Progress represents progress.
type Progress struct {
	Detail    change.OperationDetail
	ID        change.OperationID
	Completed uint64
	Total     uint64
}

// ReportProgress performs the report progress operation.
func (coordinator *Coordinator) ReportProgress(progress Progress) error {
	if progress.ID == 0 ||
		progress.Total != 0 && progress.Completed > progress.Total ||
		progress.Detail.Bytes() > MaxOperationProgressBytes {
		return errors.New("invalid operation progress")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	acceptedReservation, accepted := coordinator.accepted[progress.ID]
	if coordinator.closed || !accepted || acceptedReservation.completed {
		return ErrCoordinatorClosed
	}
	nextBytes := coordinator.progressBytes + progressRetainedBytes(progress)
	if previous, exists := coordinator.progress[progress.ID]; exists {
		nextBytes -= progressRetainedBytes(previous)
	} else {
		coordinator.progressOrder = append(coordinator.progressOrder, progress.ID)
	}
	if nextBytes > MaxProgressBytes {
		if _, exists := coordinator.progress[progress.ID]; !exists {
			coordinator.progressOrder =
				coordinator.progressOrder[:len(coordinator.progressOrder)-1]
		}
		return ErrCoordinatorSaturated
	}
	coordinator.progress[progress.ID] = progress
	coordinator.progressBytes = nextBytes
	coordinator.progressDirty = true
	return nil
}

func (coordinator *Coordinator) reportProgress(progress Progress) {
	err := coordinator.ReportProgress(progress)
	if err == nil ||
		errors.Is(err, ErrCoordinatorClosed) ||
		errors.Is(err, ErrCoordinatorSaturated) {
		return
	}
	panic(err)
}

func progressRetainedBytes(progress Progress) int64 {
	return progress.Detail.Bytes() + 32
}

func (coordinator *Coordinator) runProgressNotifier() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			coordinator.mu.Lock()
			if coordinator.progressDirty && len(coordinator.progressOrder) != 0 {
				coordinator.progressDirty = false
				coordinator.signalReadyLocked()
			}
			coordinator.mu.Unlock()
		case <-coordinator.progressStop:
			return
		}
	}
}
