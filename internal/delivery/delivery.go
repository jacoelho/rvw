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
	State CommitState
	Bytes int64
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
	runner    *clipboardRunner
	clipboard destinationState
	mu        sync.Mutex
}

// NewManager constructs a manager.
func NewManager() *Manager {
	return &Manager{
		runner: newClipboardRunner(),
	}
}

// Deliver performs the deliver operation.
func (manager *Manager) Deliver(
	ctx context.Context,
	payload Payload,
) (Outcome, error) {
	if ctx == nil || payload.Bytes() > MaxPayloadBytes {
		return Outcome{State: CommitFailedBeforeWrite},
			errors.New("invalid delivery request")
	}
	if err := manager.reserveWrite(); err != nil {
		return Outcome{
			State: CommitFailedBeforeWrite,
			Bytes: payload.Bytes(),
		}, err
	}

	name, arguments, err := clipboardCommand()
	if err != nil {
		manager.finishWrite(CommitFailedBeforeWrite)
		return Outcome{
			State: CommitFailedBeforeWrite,
			Bytes: payload.Bytes(),
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
		manager.finishWrite(state)
		return Outcome{
			State: state,
			Bytes: payload.Bytes(),
		}, err
	}
	manager.finishWrite(CommitSucceeded)
	return Outcome{
		State: CommitSucceeded,
		Bytes: payload.Bytes(),
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

func (manager *Manager) reserveWrite() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.clipboard
	switch {
	case state.unknown:
		return ErrUnknown
	case state.active:
		return ErrBusy
	default:
		state.active = true
		manager.clipboard = state
		return nil
	}
}

func (manager *Manager) finishWrite(outcome CommitState) {
	manager.mu.Lock()
	state := manager.clipboard
	state.active = false
	state.unknown = state.unknown || outcome == CommitUnknown
	manager.clipboard = state
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
