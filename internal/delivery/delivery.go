// Package delivery writes review output to local destinations.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/jacoelho/rvw/internal/subprocess"
)

// MaxPayloadBytes defines the supported payload bytes limit.
const (
	MaxPayloadBytes         int64 = 16 << 20
	MaxClipboardStdoutBytes int64 = 64 << 10
	MaxClipboardStderrBytes int64 = 1 << 20
	// MaxClipboardErrorConstructionBytes includes captured stderr plus bounded
	// terminal-safe retained and formatted error representations.
	MaxClipboardErrorConstructionBytes int64 = subprocess.MaxErrorConstructionBytes
	osDarwin                                 = "darwin"
	osLinux                                  = "linux"
	commandPBCopy                            = "pbcopy"
	commandPBPaste                           = "pbpaste"
	commandWLCopy                            = "wl-copy"
	commandWLPaste                           = "wl-paste"
	commandXClip                             = "xclip"
	commandXSel                              = "xsel"
	argumentNoNewline                        = "--no-newline"
	argumentOut                              = "-out"
	argumentSelection                        = "-selection"
	argumentClipboard                        = "clipboard"
)

// ErrUnavailable identifies an unavailable failure.
var (
	ErrUnavailable = errors.New("delivery destination is unavailable")
	ErrBusy        = errors.New("delivery destination already has an active operation")
	ErrUnknown     = errors.New("delivery may have committed and is fenced")
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
	runner       *subprocess.Runner
	destinations map[Destination]destinationState
	mu           sync.Mutex
}

// NewManager constructs a manager.
func NewManager() *Manager {
	return &Manager{
		runner:       subprocess.NewRunner(),
		destinations: make(map[Destination]destinationState),
	}
}

// ClipboardReadConstructionBytes returns the maximum simultaneously live
// bytes needed to construct either a clipboard read result or its error.
func ClipboardReadConstructionBytes(stdoutLimit int64) (int64, error) {
	if err := validateClipboardReadLimit(stdoutLimit); err != nil {
		return 0, err
	}
	capturePeak := subprocess.MaxCaptureConstructionBytes(stdoutLimit) +
		subprocess.MaxCaptureConstructionBytes(MaxClipboardStderrBytes)
	resultPeak := 2*stdoutLimit + MaxClipboardStderrBytes
	errorPeak := stdoutLimit + MaxClipboardErrorConstructionBytes
	return max(capturePeak, resultPeak, errorPeak), nil
}

// ReadClipboard reads a bounded clipboard value. A successful result owns its
// bytes; unsuccessful reads never expose a partial value or fence the
// destination.
func (manager *Manager) ReadClipboard(ctx context.Context, stdoutLimit int64) (string, error) {
	if ctx == nil {
		return "", errors.New("invalid clipboard read request")
	}
	if err := validateClipboardReadLimit(stdoutLimit); err != nil {
		return "", err
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	if err := manager.reserveRead(DestinationClipboard); err != nil {
		return "", err
	}
	defer manager.finishRead(DestinationClipboard)

	name, arguments, err := clipboardReadCommand()
	if err != nil {
		return "", err
	}
	result, err := manager.runner.Run(ctx, subprocess.Request{
		Name:            name,
		Args:            arguments,
		AcceptedExits:   []int{0},
		StdoutLimit:     stdoutLimit,
		StderrLimit:     MaxClipboardStderrBytes,
		Timeout:         30 * time.Second,
		OperationDetail: "clipboard read",
	})
	if err != nil {
		return "", err
	}
	return string(result.Stdout), nil
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
	result, err := manager.runner.Run(ctx, subprocess.Request{
		Name:            name,
		Args:            arguments,
		Stdin:           []byte(payload.body),
		StdoutLimit:     MaxClipboardStdoutBytes,
		StderrLimit:     MaxClipboardStderrBytes,
		Timeout:         30 * time.Second,
		OperationDetail: "clipboard delivery",
	})
	if err != nil {
		state := CommitFailedBeforeWrite
		if result.Started {
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
	manager.runner.Escalate()
}

// Close performs the close operation.
func (manager *Manager) Close(ctx context.Context) error {
	return manager.runner.Shutdown(ctx)
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

func (manager *Manager) reserveRead(destination Destination) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.destinations[destination]
	if state.active {
		return ErrBusy
	}
	state.active = true
	manager.destinations[destination] = state
	return nil
}

func (manager *Manager) finishWrite(destination Destination, outcome CommitState) {
	manager.mu.Lock()
	state := manager.destinations[destination]
	state.active = false
	state.unknown = state.unknown || outcome == CommitUnknown
	manager.destinations[destination] = state
	manager.mu.Unlock()
}

func (manager *Manager) finishRead(destination Destination) {
	manager.mu.Lock()
	state := manager.destinations[destination]
	state.active = false
	manager.destinations[destination] = state
	manager.mu.Unlock()
}

func validateClipboardReadLimit(stdoutLimit int64) error {
	if stdoutLimit <= 0 || stdoutLimit > subprocess.DefaultStdoutLimit {
		return fmt.Errorf(
			"clipboard read stdout limit must be from 1 through %d bytes",
			subprocess.DefaultStdoutLimit,
		)
	}
	return nil
}

func clipboardReadCommand() (string, []string, error) {
	switch runtime.GOOS {
	case osDarwin:
		path, err := exec.LookPath(commandPBPaste)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %s is unavailable", ErrUnavailable, commandPBPaste)
		}
		return path, nil, nil
	case osLinux:
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			if path, err := exec.LookPath(commandWLPaste); err == nil {
				return path, []string{argumentNoNewline}, nil
			}
		}
		if os.Getenv("DISPLAY") != "" {
			if path, err := exec.LookPath(commandXClip); err == nil {
				return path, []string{argumentOut, argumentSelection, argumentClipboard}, nil
			}
			if path, err := exec.LookPath(commandXSel); err == nil {
				return path, []string{"--output", "--clipboard"}, nil
			}
		}
		return "", nil, fmt.Errorf(
			"%w: no supported clipboard read command",
			ErrUnavailable,
		)
	default:
		return "", nil, fmt.Errorf("%w: unsupported platform %s", ErrUnavailable, runtime.GOOS)
	}
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
