package tui

import (
	"context"
	"errors"
	"fmt"
	"sync"

	tea "charm.land/bubbletea/v2"
)

type outcomeReadyMsg struct {
	state ReadyState
}

// ReadyReceiver represents ready receiver.
type ReadyReceiver struct {
	context  context.Context
	cancel   context.CancelFunc
	finished chan struct{}
	mu       sync.Mutex
	active   bool
	started  bool
	stopped  bool
}

// NewReadyReceiver constructs and validates ready receiver.
func NewReadyReceiver(ctx context.Context) (*ReadyReceiver, error) {
	if ctx == nil {
		return nil, errors.New("readiness receiver requires a context")
	}
	receiverContext, cancel := context.WithCancel(ctx)
	return &ReadyReceiver{context: receiverContext, cancel: cancel}, nil
}

// Arm performs the arm operation.
func (receiver *ReadyReceiver) Arm(coordinator *Coordinator) tea.Cmd {
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	if receiver.stopped || receiver.active || coordinator == nil {
		return nil
	}
	receiver.active = true
	receiver.started = false
	finished := make(chan struct{})
	receiver.finished = finished
	receiverContext := receiver.context
	return func() tea.Msg {
		receiver.mu.Lock()
		if receiver.stopped || !receiver.active {
			receiver.mu.Unlock()
			return outcomeReadyMsg{state: ReadyCancelled}
		}
		receiver.started = true
		receiver.mu.Unlock()
		state := coordinator.WaitReady(receiverContext)
		receiver.mu.Lock()
		receiver.active = false
		receiver.started = false
		close(finished)
		receiver.mu.Unlock()
		return outcomeReadyMsg{state: state}
	}
}

// StopAndWait performs the stop and wait operation.
func (receiver *ReadyReceiver) StopAndWait(ctx context.Context) error {
	receiver.mu.Lock()
	if !receiver.stopped {
		receiver.stopped = true
		receiver.cancel()
	}
	finished := receiver.finished
	active := receiver.active
	if active && !receiver.started {
		receiver.active = false
		close(finished)
		active = false
	}
	receiver.mu.Unlock()
	if !active {
		return nil
	}
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("readiness receiver did not stop: %w", context.Cause(ctx))
	}
}
