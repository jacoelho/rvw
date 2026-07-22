package app

import (
	"sync"

	"github.com/jacoelho/rvw/internal/review"
)

const maxRequiredBypasses = 8

type scheduledOperation struct {
	start    chan struct{}
	id       review.OperationID
	ready    bool
	bypasses uint8
}

type requiredScheduler struct {
	byID    map[review.OperationID]*scheduledOperation
	pending []*scheduledOperation
	mu      sync.Mutex
}

func newRequiredScheduler() *requiredScheduler {
	return &requiredScheduler{
		byID: make(map[review.OperationID]*scheduledOperation),
	}
}

func (scheduler *requiredScheduler) register(id review.OperationID) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	record := &scheduledOperation{id: id, start: make(chan struct{})}
	scheduler.pending = append(scheduler.pending, record)
	scheduler.byID[id] = record
}

func (scheduler *requiredScheduler) unregister(id review.OperationID) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	record, exists := scheduler.byID[id]
	if !exists {
		return
	}
	delete(scheduler.byID, id)
	for index, candidate := range scheduler.pending {
		if candidate == record {
			scheduler.pending =
				append(scheduler.pending[:index], scheduler.pending[index+1:]...)
			break
		}
	}
	scheduler.dispatchLocked()
}

func (scheduler *requiredScheduler) wait(id review.OperationID) {
	start := scheduler.markReady(id)
	<-start
}

func (scheduler *requiredScheduler) markReady(id review.OperationID) <-chan struct{} {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	record := scheduler.byID[id]
	if record == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	record.ready = true
	scheduler.dispatchLocked()
	return record.start
}

func (scheduler *requiredScheduler) dispatchLocked() {
	for len(scheduler.pending) != 0 {
		oldest := scheduler.pending[0]
		if oldest.ready {
			scheduler.grantLocked(0)
			continue
		}
		if oldest.bypasses >= maxRequiredBypasses {
			return
		}
		ready := -1
		for index := 1; index < len(scheduler.pending); index++ {
			if scheduler.pending[index].ready {
				ready = index
				break
			}
		}
		if ready < 0 {
			return
		}
		oldest.bypasses++
		scheduler.grantLocked(ready)
	}
}

func (scheduler *requiredScheduler) grantLocked(index int) {
	record := scheduler.pending[index]
	delete(scheduler.byID, record.id)
	scheduler.pending =
		append(scheduler.pending[:index], scheduler.pending[index+1:]...)
	close(record.start)
}
