package coordinator

import (
	"context"
	"errors"
)

// CPUPool bounds concurrent CPU work independently of I/O workers.
type CPUPool struct {
	permits chan struct{}
}

// NewCPUPool constructs a pool with a positive concurrency bound.
func NewCPUPool(workers int) (*CPUPool, error) {
	if workers <= 0 {
		return nil, errors.New("CPU worker count must be positive")
	}
	return &CPUPool{permits: make(chan struct{}, workers)}, nil
}

// Run acquires a permit, fences cancellation, and executes work.
func (pool *CPUPool) Run(ctx context.Context, work func() error) error {
	if work == nil {
		return errors.New("CPU work is nil")
	}
	if !pool.Acquire(ctx) {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		return context.Canceled
	}
	defer pool.Release()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return work()
}

// Acquire waits for capacity or cancellation.
func (pool *CPUPool) Acquire(ctx context.Context) bool {
	if pool == nil || ctx == nil {
		return false
	}
	select {
	case pool.permits <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// Release returns one acquired permit.
func (pool *CPUPool) Release() {
	if pool == nil {
		panic("release from nil CPU pool")
	}
	<-pool.permits
}

// Active returns the number of currently held permits.
func (pool *CPUPool) Active() int {
	if pool == nil {
		return 0
	}
	return len(pool.permits)
}
