package app

import (
	"errors"
	"time"

	"github.com/jacoelho/rvw/internal/review"
)

const (
	MaxProgressBytes          int64 = 256 << 10
	MaxOperationProgressBytes int64 = 8 << 10
)

type Progress struct {
	ID        review.OperationID
	Completed uint64
	Total     uint64
	Detail    review.OperationDetail
}

func (coordinator *Coordinator) ReportProgress(progress Progress) error {
	if progress.ID == 0 ||
		progress.Total != 0 && progress.Completed > progress.Total ||
		progress.Detail.Bytes() > MaxOperationProgressBytes {
		return errors.New("invalid operation progress")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	reservation, accepted := coordinator.accepted[progress.ID]
	if coordinator.closed || !accepted || reservation.completed {
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
