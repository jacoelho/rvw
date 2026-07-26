package coordinator

import (
	"sync"

	"github.com/jacoelho/rvw/internal/change"
)

const maxRequiredBypasses = 8

type scheduledOperation struct {
	start    chan struct{}
	id       change.OperationID
	ready    bool
	bypasses uint8
}

// Scheduler orders required work while allowing a bounded number of ready
// operations to bypass an older operation that has not reached its worker.
type Scheduler struct {
	byID    map[change.OperationID]*scheduledOperation
	pending []*scheduledOperation
	mu      sync.Mutex
}

// NewScheduler constructs a required-work scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{byID: make(map[change.OperationID]*scheduledOperation)}
}

// Register admits an operation before it is published to a worker queue.
func (scheduler *Scheduler) Register(id change.OperationID) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	record := &scheduledOperation{id: id, start: make(chan struct{})}
	scheduler.pending = append(scheduler.pending, record)
	scheduler.byID[id] = record
}

// Unregister removes an operation that did not reach or no longer needs a worker.
func (scheduler *Scheduler) Unregister(id change.OperationID) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	record, exists := scheduler.byID[id]
	if !exists {
		return
	}
	delete(scheduler.byID, id)
	for index, candidate := range scheduler.pending {
		if candidate == record {
			scheduler.pending = append(scheduler.pending[:index], scheduler.pending[index+1:]...)
			break
		}
	}
	scheduler.dispatchLocked()
}

// Wait marks an operation worker-ready and blocks until fairness admits it.
func (scheduler *Scheduler) Wait(id change.OperationID) {
	start := scheduler.markReady(id)
	<-start
}

func (scheduler *Scheduler) markReady(id change.OperationID) <-chan struct{} {
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

func (scheduler *Scheduler) dispatchLocked() {
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

func (scheduler *Scheduler) grantLocked(index int) {
	record := scheduler.pending[index]
	delete(scheduler.byID, record.id)
	scheduler.pending = append(scheduler.pending[:index], scheduler.pending[index+1:]...)
	close(record.start)
}
