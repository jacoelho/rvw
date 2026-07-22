package delivery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	commandName := "pbcopy"
	if runtime.GOOS == "linux" {
		commandName = "wl-copy"
		t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	}
	if err := os.Symlink(binary, filepath.Join(directory, commandName)); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(directory, "capture")
	t.Setenv("PATH", directory)
	t.Setenv("RVW_DELIVERY_HELPER", "1")
	t.Setenv("RVW_DELIVERY_CAPTURE", capture)

	manager, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
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
	if runtime.GOOS == "linux" {
		t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	}
	manager, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	payload, err := NewPayload("review body")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	outcome, err := manager.Deliver(ctx, DestinationClipboard, payload)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Deliver() error = %v, want context cancelled", err)
	}
	if outcome.State != CommitFailedBeforeWrite {
		t.Fatalf("delivery outcome = %+v, want failed before write", outcome)
	}
	manager.mu.Lock()
	fenced := manager.destinations[DestinationClipboard].unknown
	manager.mu.Unlock()
	if fenced {
		t.Fatal("pre-start cancellation fenced the clipboard")
	}
}

func TestFencedClipboardRetryReturnsTypedFailureOutcome(t *testing.T) {
	manager, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	manager.finish(DestinationClipboard, CommitUnknown)
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
