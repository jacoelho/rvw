package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/jacoelho/rvw/internal/subprocess"
)

const MaxPayloadBytes int64 = 16 << 20

var (
	ErrUnavailable = errors.New("delivery destination is unavailable")
	ErrBusy        = errors.New("delivery destination already has an active write")
	ErrUnknown     = errors.New("delivery may have committed and is fenced")
)

type Destination uint8

const (
	DestinationClipboard Destination = iota + 1
	DestinationStdout
)

type CommitState uint8

const (
	CommitFailedBeforeWrite CommitState = iota + 1
	CommitSucceeded
	CommitUnknown
)

type Outcome struct {
	Destination Destination
	State       CommitState
	Bytes       int64
}

type Payload struct {
	body string
}

func NewPayload(body string) (Payload, error) {
	if int64(len(body)) > MaxPayloadBytes {
		return Payload{}, fmt.Errorf("delivery payload exceeds %d bytes", MaxPayloadBytes)
	}
	return Payload{body: body}, nil
}

func (payload Payload) Bytes() int64 {
	return int64(len(payload.body))
}

type destinationState struct {
	active  bool
	unknown bool
}

type Manager struct {
	runner *subprocess.Runner

	mu           sync.Mutex
	destinations map[Destination]destinationState
}

func NewManager() (*Manager, error) {
	runner, err := subprocess.NewRunner(subprocess.DefaultConcurrency)
	if err != nil {
		return nil, fmt.Errorf("create delivery subprocess runner: %w", err)
	}
	return &Manager{
		runner:       runner,
		destinations: make(map[Destination]destinationState),
	}, nil
}

func (manager *Manager) Deliver(
	ctx context.Context,
	destination Destination,
	payload Payload,
) (Outcome, error) {
	if ctx == nil || payload.Bytes() > MaxPayloadBytes {
		return Outcome{}, errors.New("invalid delivery request")
	}
	if destination != DestinationClipboard {
		return Outcome{}, fmt.Errorf("%w: destination %d is post-program only", ErrUnavailable, destination)
	}
	if err := manager.reserve(destination); err != nil {
		return Outcome{}, err
	}

	name, arguments, err := clipboardCommand()
	if err != nil {
		manager.finish(destination, CommitFailedBeforeWrite)
		return Outcome{
			Destination: destination,
			State:       CommitFailedBeforeWrite,
			Bytes:       payload.Bytes(),
		}, err
	}
	result, err := manager.runner.Run(ctx, subprocess.Request{
		Name:            name,
		Args:            arguments,
		Stdin:           []byte(payload.body),
		StdoutLimit:     64 << 10,
		StderrLimit:     1 << 20,
		Timeout:         30 * time.Second,
		OperationDetail: "clipboard delivery",
	})
	if err != nil {
		state := CommitFailedBeforeWrite
		if result.Started {
			state = CommitUnknown
			err = errors.Join(err, ErrUnknown)
		}
		manager.finish(destination, state)
		return Outcome{
			Destination: destination,
			State:       state,
			Bytes:       payload.Bytes(),
		}, err
	}
	manager.finish(destination, CommitSucceeded)
	return Outcome{
		Destination: destination,
		State:       CommitSucceeded,
		Bytes:       payload.Bytes(),
	}, nil
}

func (manager *Manager) Escalate() {
	manager.runner.Escalate()
}

func (manager *Manager) Close(ctx context.Context) error {
	return manager.runner.Shutdown(ctx)
}

func (manager *Manager) reserve(destination Destination) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.destinations[destination]
	switch {
	case state.unknown:
		return ErrUnknown
	case state.active:
		return ErrBusy
	default:
		state.active = true
		manager.destinations[destination] = state
		return nil
	}
}

func (manager *Manager) finish(destination Destination, outcome CommitState) {
	manager.mu.Lock()
	state := manager.destinations[destination]
	state.active = false
	state.unknown = state.unknown || outcome == CommitUnknown
	manager.destinations[destination] = state
	manager.mu.Unlock()
}

func clipboardCommand() (string, []string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "pbcopy", nil, nil
	case "linux":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return "wl-copy", nil, nil
		}
		if os.Getenv("DISPLAY") != "" {
			return "xclip", []string{"-selection", "clipboard"}, nil
		}
		return "", nil, fmt.Errorf("%w: no Wayland or X11 display", ErrUnavailable)
	default:
		return "", nil, fmt.Errorf("%w: unsupported platform %s", ErrUnavailable, runtime.GOOS)
	}
}
