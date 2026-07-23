package delivery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("RVW_DELIVERY_HELPER") == "1" {
		contents, err := io.ReadAll(os.Stdin)
		if err == nil {
			err = os.WriteFile(os.Getenv("RVW_DELIVERY_CAPTURE"), contents, 0o600)
		}
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestClipboardDeliveryCommitsTypedPayload(t *testing.T) {
	directory, capture := installDeliveryHelper(t)
	t.Setenv("PATH", directory)
	t.Setenv("RVW_DELIVERY_HELPER", "1")
	t.Setenv("RVW_DELIVERY_CAPTURE", capture)
	configureDisplay(t)

	manager := newTestManager(t)
	payload, err := NewPayload("review body")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := manager.Deliver(t.Context(), DestinationClipboard, payload)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != CommitSucceeded || outcome.Bytes != int64(len("review body")) {
		t.Fatalf("delivery outcome = %+v", outcome)
	}
	contents, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "review body" {
		t.Fatalf("captured payload = %q", contents)
	}
}

func TestPayloadUsesInt64ByteLimit(t *testing.T) {
	if _, err := NewPayload(string(make([]byte, int(MaxPayloadBytes)+1))); err == nil {
		t.Fatal("oversized payload was accepted")
	}
}

func TestPreCancelledClipboardDeliveryDoesNotFenceDestination(t *testing.T) {
	configureDisplay(t)
	manager := newTestManager(t)
	payload, err := NewPayload("review body")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	outcome, err := manager.Deliver(ctx, DestinationClipboard, payload)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Deliver() error = %v, want context cancellation", err)
	}
	if outcome.State != CommitFailedBeforeWrite {
		t.Fatalf("delivery outcome = %+v, want failed before write", outcome)
	}
	assertClipboardState(t, manager, destinationState{})
}

func TestFencedClipboardRetryReturnsTypedFailureOutcome(t *testing.T) {
	manager := newTestManager(t)
	manager.finishWrite(DestinationClipboard, CommitUnknown)
	payload, err := NewPayload("review body")
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := manager.Deliver(t.Context(), DestinationClipboard, payload)
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("Deliver() error = %v, want %v", err, ErrUnknown)
	}
	if outcome.Destination != DestinationClipboard ||
		outcome.State != CommitFailedBeforeWrite || outcome.Bytes != payload.Bytes() {
		t.Fatalf("fenced retry outcome = %+v", outcome)
	}
}

func TestLinuxClipboardWriteCommand(t *testing.T) {
	if runtime.GOOS != osLinux {
		t.Skip("Linux command selection")
	}
	tests := []struct {
		name      string
		wayland   string
		display   string
		wantName  string
		wantArgs  []string
		wantError bool
	}{
		{name: "Wayland", wayland: "wayland-test", wantName: commandWLCopy},
		{
			name: "X11", display: ":0", wantName: commandXClip,
			wantArgs: []string{argumentSelection, argumentClipboard},
		},
		{name: "headless", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("WAYLAND_DISPLAY", test.wayland)
			t.Setenv("DISPLAY", test.display)
			name, arguments, err := clipboardCommand()
			if test.wantError {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("clipboardCommand() error = %v, want %v", err, ErrUnavailable)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name != test.wantName || !slices.Equal(arguments, test.wantArgs) {
				t.Fatalf("clipboardCommand() = %q %q, want %q %q",
					name, arguments, test.wantName, test.wantArgs)
			}
		})
	}
}

func TestBoundedWriterRejectsOverflow(t *testing.T) {
	writer := &boundedWriter{limit: 3}
	written, err := writer.Write([]byte("four"))
	if written != 3 || !errors.Is(err, ErrLimited) || writer.buffer.String() != "fou" {
		t.Fatalf("Write() = %d, %v, %q", written, err, writer.buffer.String())
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	manager := NewManager()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return manager
}

func installDeliveryHelper(t *testing.T) (string, string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	name := commandPBCopy
	if runtime.GOOS == osLinux {
		name = commandWLCopy
	}
	if err := os.Symlink(binary, filepath.Join(directory, name)); err != nil {
		t.Fatal(err)
	}
	return directory, filepath.Join(directory, "capture")
}

func configureDisplay(t *testing.T) {
	t.Helper()
	if runtime.GOOS == osLinux {
		t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	}
}

func assertClipboardState(t *testing.T, manager *Manager, want destinationState) {
	t.Helper()
	manager.mu.Lock()
	got := manager.destinations[DestinationClipboard]
	manager.mu.Unlock()
	if got != want {
		t.Fatalf("clipboard state = %+v, want %+v", got, want)
	}
}
