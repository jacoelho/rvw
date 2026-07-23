// Package delivery writes review output to local destinations.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
)

// MaxPayloadBytes defines the supported payload bytes limit.
const (
	MaxPayloadBytes         int64 = 16 << 20
	MaxClipboardStdoutBytes int64 = 64 << 10
	MaxClipboardStderrBytes int64 = 1 << 20
	// MaxClipboardErrorConstructionBytes includes captured stderr plus bounded
	// terminal-safe retained and formatted error representations.
	MaxClipboardErrorConstructionBytes int64 = MaxClipboardStderrBytes + 2*(16<<10) + 256
	osDarwin                                 = "darwin"
	osLinux                                  = "linux"
	commandPBCopy                            = "pbcopy"
	commandWLCopy                            = "wl-copy"
	commandXClip                             = "xclip"
	argumentSelection                        = "-selection"
	argumentClipboard                        = "clipboard"
)

// ErrUnavailable identifies an unavailable failure.
var (
	ErrUnavailable = errors.New("delivery destination is unavailable")
	ErrBusy        = errors.New("delivery destination already has an active operation")
	ErrUnknown     = errors.New("delivery may have committed and is fenced")
	ErrLimited     = errors.New("delivery command output exceeded its limit")
)

// Destination describes destination.
type Destination uint8

// DestinationClipboard identifies a supported destination clipboard value.
const (
	DestinationClipboard Destination = iota + 1
	DestinationStdout
)

// CommitState describes commit state.
type CommitState uint8

// CommitFailedBeforeWrite identifies a supported commit failed before write value.
const (
	CommitFailedBeforeWrite CommitState = iota + 1
	CommitSucceeded
	CommitUnknown
)

// Outcome describes outcome.
type Outcome struct {
	Destination Destination
	State       CommitState
	Bytes       int64
}

// Payload represents payload.
type Payload struct {
	body string
}

// NewPayload constructs and validates payload.
func NewPayload(body string) (Payload, error) {
	if int64(len(body)) > MaxPayloadBytes {
		return Payload{}, fmt.Errorf("delivery payload exceeds %d bytes", MaxPayloadBytes)
	}
	return Payload{body: body}, nil
}

// Bytes returns the receiver's bytes value.
func (payload Payload) Bytes() int64 {
	return int64(len(payload.body))
}

type destinationState struct {
	active  bool
	unknown bool
}

// Manager represents manager.
type Manager struct {
	runner       *clipboardRunner
	destinations map[Destination]destinationState
	mu           sync.Mutex
}

// NewManager constructs a manager.
func NewManager() *Manager {
	return &Manager{
		runner:       newClipboardRunner(),
		destinations: make(map[Destination]destinationState),
	}
}

// Deliver performs the deliver operation.
func (manager *Manager) Deliver(
	ctx context.Context,
	destination Destination,
	payload Payload,
) (Outcome, error) {
	if ctx == nil || payload.Bytes() > MaxPayloadBytes {
		return Outcome{Destination: destination, State: CommitFailedBeforeWrite},
			errors.New("invalid delivery request")
	}
	if destination != DestinationClipboard {
		return Outcome{Destination: destination, State: CommitFailedBeforeWrite},
			fmt.Errorf("%w: destination %d is post-program only", ErrUnavailable, destination)
	}
	if err := manager.reserveWrite(destination); err != nil {
		return Outcome{
			Destination: destination,
			State:       CommitFailedBeforeWrite,
			Bytes:       payload.Bytes(),
		}, err
	}

	name, arguments, err := clipboardCommand()
	if err != nil {
		manager.finishWrite(destination, CommitFailedBeforeWrite)
		return Outcome{
			Destination: destination,
			State:       CommitFailedBeforeWrite,
			Bytes:       payload.Bytes(),
		}, err
	}
	result, err := manager.runner.run(
		ctx, name, arguments, []byte(payload.body),
		MaxClipboardStdoutBytes, MaxClipboardStderrBytes,
	)
	if err != nil {
		state := CommitFailedBeforeWrite
		if result.attempted {
			state = CommitUnknown
			err = errors.Join(err, ErrUnknown)
		}
		manager.finishWrite(destination, state)
		return Outcome{
			Destination: destination,
			State:       state,
			Bytes:       payload.Bytes(),
		}, err
	}
	manager.finishWrite(destination, CommitSucceeded)
	return Outcome{
		Destination: destination,
		State:       CommitSucceeded,
		Bytes:       payload.Bytes(),
	}, nil
}

// Escalate performs the escalate operation.
func (manager *Manager) Escalate() {
	manager.runner.escalate()
}

// Close performs the close operation.
func (manager *Manager) Close(ctx context.Context) error {
	return manager.runner.close(ctx)
}

func (manager *Manager) reserveWrite(destination Destination) error {
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

func (manager *Manager) finishWrite(destination Destination, outcome CommitState) {
	manager.mu.Lock()
	state := manager.destinations[destination]
	state.active = false
	state.unknown = state.unknown || outcome == CommitUnknown
	manager.destinations[destination] = state
	manager.mu.Unlock()
}

func clipboardCommand() (string, []string, error) {
	switch runtime.GOOS {
	case osDarwin:
		return commandPBCopy, nil, nil
	case osLinux:
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return commandWLCopy, nil, nil
		}
		if os.Getenv("DISPLAY") != "" {
			return commandXClip, []string{argumentSelection, argumentClipboard}, nil
		}
		return "", nil, fmt.Errorf("%w: no Wayland or X11 display", ErrUnavailable)
	default:
		return "", nil, fmt.Errorf("%w: unsupported platform %s", ErrUnavailable, runtime.GOOS)
	}
}
