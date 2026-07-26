package coordinator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestRunCPUReleasesPermitOnWorkFailure(t *testing.T) {
	pool, err := NewCPUPool(1)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("CPU work failed")
	if err := pool.Run(t.Context(), func() error { return cause }); !errors.Is(err, cause) {
		t.Fatalf("work error = %v", err)
	}
	if pool.Active() != 0 {
		t.Fatal("failed CPU work leaked its permit")
	}
}

func TestRunCPUCancelledBeforeAdmissionSkipsWork(t *testing.T) {
	pool, err := NewCPUPool(1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("cancel CPU admission")
	cancel(cause)
	called := false
	if err := pool.Run(ctx, func() error {
		called = true
		return nil
	}); !errors.Is(err, cause) {
		t.Fatalf("admission error = %v", err)
	}
	if called || pool.Active() != 0 {
		t.Fatalf("called=%v permits=%d", called, pool.Active())
	}
}

func TestRunCPUSerializesConcurrentWork(t *testing.T) {
	pool, err := NewCPUPool(1)
	if err != nil {
		t.Fatal(err)
	}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	done := make(chan error, 2)
	var active atomic.Int32
	var maximum atomic.Int32
	run := func(entered chan<- struct{}, release <-chan struct{}) {
		done <- pool.Run(t.Context(), func() error {
			current := active.Add(1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			close(entered)
			if release != nil {
				<-release
			}
			active.Add(-1)
			return nil
		})
	}
	go run(firstEntered, releaseFirst)
	<-firstEntered
	go run(secondEntered, nil)
	select {
	case <-secondEntered:
		t.Fatal("second CPU closure entered before the first released")
	default:
	}
	close(releaseFirst)
	<-secondEntered
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if maximum.Load() != 1 || pool.Active() != 0 {
		t.Fatalf("maximum=%d permits=%d", maximum.Load(), pool.Active())
	}
}
