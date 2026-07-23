package delivery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/subprocess"
)

func TestMain(m *testing.M) {
	if os.Getenv("RVW_DELIVERY_HELPER") == "1" {
		os.Exit(runDeliveryHelper())
	}
	os.Exit(m.Run())
}

func runDeliveryHelper() int {
	if path := os.Getenv("RVW_DELIVERY_ARGS_CAPTURE"); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(os.Args[1:], "\x00")), 0o600); err != nil {
			return 1
		}
	}
	mode := os.Getenv("RVW_DELIVERY_HELPER_MODE")
	switch {
	case mode == "exit":
		_, _ = io.WriteString(os.Stderr, "clipboard read failed")
		code, err := strconv.Atoi(os.Getenv("RVW_DELIVERY_EXIT_CODE"))
		if err != nil {
			return 1
		}
		return code
	case mode == "block":
		return blockDeliveryHelper()
	case mode == "block-read" && deliveryHelperIsRead():
		return blockDeliveryHelper()
	case mode == "block-write" && !deliveryHelperIsRead():
		return blockDeliveryHelper()
	case deliveryHelperIsRead():
		contents, err := os.ReadFile(os.Getenv("RVW_DELIVERY_OUTPUT"))
		if err == nil {
			_, err = os.Stdout.Write(contents)
		}
		if err != nil {
			return 1
		}
		return 0
	default:
		contents, err := io.ReadAll(os.Stdin)
		if err == nil {
			err = os.WriteFile(os.Getenv("RVW_DELIVERY_CAPTURE"), contents, 0o600)
		}
		if err != nil {
			return 1
		}
		return 0
	}
}

func deliveryHelperIsRead() bool {
	switch filepath.Base(os.Args[0]) {
	case "pbpaste", "wl-paste", "xsel":
		return true
	case "xclip":
		return slices.Contains(os.Args[1:], "-out")
	default:
		return false
	}
}

func blockDeliveryHelper() int {
	if err := os.WriteFile(os.Getenv("RVW_DELIVERY_STARTED"), nil, 0o600); err != nil {
		return 1
	}
	for {
		time.Sleep(time.Hour)
	}
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

	manager := NewManager()
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
	manager := NewManager()
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
	manager := NewManager()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
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

func TestClipboardReadConstructionBytesCoversEveryConstructionPhase(t *testing.T) {
	const stdoutLimit int64 = (64 << 10) + 1
	want := max(
		subprocess.MaxCaptureConstructionBytes(stdoutLimit)+
			subprocess.MaxCaptureConstructionBytes(MaxClipboardStderrBytes),
		2*stdoutLimit+MaxClipboardStderrBytes,
		stdoutLimit+MaxClipboardErrorConstructionBytes,
	)
	got, err := ClipboardReadConstructionBytes(stdoutLimit)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ClipboardReadConstructionBytes() = %d, want %d", got, want)
	}

	for _, invalid := range []int64{0, subprocess.DefaultStdoutLimit + 1} {
		if _, err := ClipboardReadConstructionBytes(invalid); err == nil {
			t.Fatalf("ClipboardReadConstructionBytes(%d) accepted invalid limit", invalid)
		}
	}
}

func TestClipboardReadPreservesExactOutputAndEnforcesLimit(t *testing.T) {
	directory := installDeliveryHelperCommands(t, platformReadCommandName(t))
	configurePlatformReadEnvironment(t)
	output := filepath.Join(directory, "output")
	t.Setenv("RVW_DELIVERY_OUTPUT", output)
	manager := newTestManager(t)

	// The app supplies its comment-body limit plus one so it can distinguish an
	// oversized clipboard value without materializing more than one extra byte.
	const limit int64 = (64 << 10) + 1
	if err := os.WriteFile(output, []byte(strings.Repeat("x", int(limit))), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := manager.ReadClipboard(t.Context(), limit)
	if err != nil {
		t.Fatal(err)
	}
	if value != strings.Repeat("x", int(limit)) {
		t.Fatalf("ReadClipboard() = %q", value)
	}

	if err := os.WriteFile(output, []byte(strings.Repeat("y", int(limit)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err = manager.ReadClipboard(t.Context(), limit)
	if !errors.Is(err, subprocess.ErrLimited) {
		t.Fatalf("ReadClipboard() error = %v, want %v", err, subprocess.ErrLimited)
	}
	if value != "" {
		t.Fatalf("limited ReadClipboard() returned partial value %q", value)
	}
}

func TestClipboardReadPreservesInvalidUTF8Bytes(t *testing.T) {
	directory := installDeliveryHelperCommands(t, platformReadCommandName(t))
	configurePlatformReadEnvironment(t)
	output := filepath.Join(directory, "output")
	want := []byte{0xff, 'a', 0xc0, 0x80, 0xfe}
	if err := os.WriteFile(output, want, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RVW_DELIVERY_OUTPUT", output)
	manager := newTestManager(t)

	value, err := manager.ReadClipboard(t.Context(), int64(len(want)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal([]byte(value), want) {
		t.Fatalf("ReadClipboard() bytes = %x, want %x", []byte(value), want)
	}
}

func TestClipboardReadRejectsNonzeroExit(t *testing.T) {
	installDeliveryHelperCommands(t, platformReadCommandName(t))
	configurePlatformReadEnvironment(t)
	t.Setenv("RVW_DELIVERY_HELPER_MODE", "exit")
	t.Setenv("RVW_DELIVERY_EXIT_CODE", "17")
	manager := newTestManager(t)

	value, err := manager.ReadClipboard(t.Context(), 32)
	var exitError *subprocess.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("ReadClipboard() error = %v, want subprocess.ExitError", err)
	}
	if exitError.ExitCode != 17 || exitError.Stderr != "clipboard read failed" {
		t.Fatalf("ReadClipboard() exit error = %+v", exitError)
	}
	if value != "" {
		t.Fatalf("failed ReadClipboard() returned %q", value)
	}
}

func TestClipboardReadReportsUnavailableCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	configurePlatformReadEnvironment(t)
	manager := newTestManager(t)

	value, err := manager.ReadClipboard(t.Context(), 32)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ReadClipboard() error = %v, want %v", err, ErrUnavailable)
	}
	if value != "" {
		t.Fatalf("unavailable ReadClipboard() returned %q", value)
	}
	assertClipboardState(t, manager, destinationState{})
}

func TestClipboardReadCancellationClearsActiveStateWithoutFencing(t *testing.T) {
	directory := installDeliveryHelperCommands(t, platformReadCommandName(t))
	configurePlatformReadEnvironment(t)
	started := filepath.Join(directory, "started")
	t.Setenv("RVW_DELIVERY_STARTED", started)
	t.Setenv("RVW_DELIVERY_HELPER_MODE", "block")
	manager := newTestManager(t)
	ctx, cancel := context.WithCancel(t.Context())
	resultChannel := make(chan clipboardReadResult, 1)
	go func() {
		value, err := manager.ReadClipboard(ctx, 32)
		resultChannel <- clipboardReadResult{value: value, err: err}
	}()
	waitForDeliveryHelper(t, started)
	cancel()

	readResult := receiveReadResult(t, resultChannel)
	if !errors.Is(readResult.err, context.Canceled) {
		t.Fatalf("ReadClipboard() error = %v, want context cancellation", readResult.err)
	}
	if readResult.value != "" {
		t.Fatalf("cancelled ReadClipboard() returned %q", readResult.value)
	}
	assertClipboardState(t, manager, destinationState{})
}

func TestPreCancelledClipboardReadWinsBeforeCommandDiscovery(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	manager := newTestManager(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	value, err := manager.ReadClipboard(ctx, 32)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadClipboard() error = %v, want context cancellation", err)
	}
	if value != "" {
		t.Fatalf("pre-cancelled ReadClipboard() returned %q", value)
	}
	assertClipboardState(t, manager, destinationState{})
}

func TestClipboardReadAndWriteSerialize(t *testing.T) {
	t.Run("active read rejects write", func(t *testing.T) {
		directory := installDeliveryHelperCommands(
			t,
			platformReadCommandName(t),
			platformWriteCommandName(t),
		)
		configurePlatformReadEnvironment(t)
		started := filepath.Join(directory, "read-started")
		t.Setenv("RVW_DELIVERY_STARTED", started)
		t.Setenv("RVW_DELIVERY_HELPER_MODE", "block-read")
		manager := newTestManager(t)
		ctx, cancel := context.WithCancel(t.Context())
		resultChannel := make(chan error, 1)
		go func() {
			_, err := manager.ReadClipboard(ctx, 32)
			resultChannel <- err
		}()
		waitForDeliveryHelper(t, started)

		payload, err := NewPayload("review body")
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := manager.Deliver(t.Context(), DestinationClipboard, payload)
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("Deliver() error = %v, want %v", err, ErrBusy)
		}
		if outcome.State != CommitFailedBeforeWrite {
			t.Fatalf("Deliver() outcome = %+v", outcome)
		}
		cancel()
		if err := receiveError(t, resultChannel); !errors.Is(err, context.Canceled) {
			t.Fatalf("ReadClipboard() error = %v, want context cancellation", err)
		}
		assertClipboardState(t, manager, destinationState{})
	})

	t.Run("active write rejects read", func(t *testing.T) {
		directory := installDeliveryHelperCommands(
			t,
			platformReadCommandName(t),
			platformWriteCommandName(t),
		)
		configurePlatformReadEnvironment(t)
		started := filepath.Join(directory, "write-started")
		t.Setenv("RVW_DELIVERY_STARTED", started)
		t.Setenv("RVW_DELIVERY_HELPER_MODE", "block-write")
		manager := newTestManager(t)
		ctx, cancel := context.WithCancel(t.Context())
		payload, err := NewPayload("review body")
		if err != nil {
			t.Fatal(err)
		}
		resultChannel := make(chan clipboardWriteResult, 1)
		go func() {
			outcome, err := manager.Deliver(ctx, DestinationClipboard, payload)
			resultChannel <- clipboardWriteResult{outcome: outcome, err: err}
		}()
		waitForDeliveryHelper(t, started)

		value, err := manager.ReadClipboard(t.Context(), 32)
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("ReadClipboard() error = %v, want %v", err, ErrBusy)
		}
		if value != "" {
			t.Fatalf("busy ReadClipboard() returned %q", value)
		}
		cancel()
		writeResult := receiveWriteResult(t, resultChannel)
		if !errors.Is(writeResult.err, context.Canceled) ||
			!errors.Is(writeResult.err, ErrUnknown) {
			t.Fatalf("Deliver() error = %v, want cancellation and unknown fence", writeResult.err)
		}
		if writeResult.outcome.State != CommitUnknown {
			t.Fatalf("Deliver() outcome = %+v", writeResult.outcome)
		}
		assertClipboardState(t, manager, destinationState{unknown: true})
	})
}

func TestClipboardReadIgnoresAndPreservesUnknownWriteFence(t *testing.T) {
	directory := installDeliveryHelperCommands(t, platformReadCommandName(t))
	configurePlatformReadEnvironment(t)
	output := filepath.Join(directory, "output")
	if err := os.WriteFile(output, []byte("clipboard value"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RVW_DELIVERY_OUTPUT", output)
	manager := newTestManager(t)
	manager.finishWrite(DestinationClipboard, CommitUnknown)

	value, err := manager.ReadClipboard(t.Context(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if value != "clipboard value" {
		t.Fatalf("ReadClipboard() = %q", value)
	}
	assertClipboardState(t, manager, destinationState{unknown: true})

	payload, err := NewPayload("review body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Deliver(t.Context(), DestinationClipboard, payload); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Deliver() error = %v, want %v", err, ErrUnknown)
	}
}

func TestClipboardReadUsesNativeCommandArguments(t *testing.T) {
	directory := installDeliveryHelperCommands(t, platformReadCommandName(t))
	configurePlatformReadEnvironment(t)
	output := filepath.Join(directory, "output")
	if err := os.WriteFile(output, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	argumentsCapture := filepath.Join(directory, "arguments")
	t.Setenv("RVW_DELIVERY_OUTPUT", output)
	t.Setenv("RVW_DELIVERY_ARGS_CAPTURE", argumentsCapture)
	manager := newTestManager(t)

	if _, err := manager.ReadClipboard(t.Context(), 32); err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(argumentsCapture)
	if err != nil {
		t.Fatal(err)
	}
	want := ""
	if runtime.GOOS == "linux" {
		want = "--no-newline"
	}
	if string(arguments) != want {
		t.Fatalf("clipboard command arguments = %q, want %q", arguments, want)
	}
}

func TestLinuxClipboardReadCommandFallbacks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux command selection")
	}
	tests := []struct {
		name        string
		wayland     string
		display     string
		commands    []string
		wantCommand string
		wantArgs    []string
		wantError   bool
	}{
		{
			name:        "Wayland",
			wayland:     "wayland-test",
			commands:    []string{"wl-paste", "xclip", "xsel"},
			wantCommand: "wl-paste",
			wantArgs:    []string{"--no-newline"},
		},
		{
			name:        "Wayland falls back to xclip",
			wayland:     "wayland-test",
			display:     ":0",
			commands:    []string{"xclip", "xsel"},
			wantCommand: "xclip",
			wantArgs:    []string{"-out", "-selection", "clipboard"},
		},
		{
			name:        "X11 xclip",
			display:     ":0",
			commands:    []string{"xclip", "xsel"},
			wantCommand: "xclip",
			wantArgs:    []string{"-out", "-selection", "clipboard"},
		},
		{
			name:        "X11 xsel fallback",
			display:     ":0",
			commands:    []string{"xsel"},
			wantCommand: "xsel",
			wantArgs:    []string{"--output", "--clipboard"},
		},
		{
			name:      "headless ignores installed X11 commands",
			commands:  []string{"xclip", "xsel"},
			wantError: true,
		},
		{name: "unavailable", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := installDeliveryHelperCommands(t, test.commands...)
			t.Setenv("PATH", directory)
			t.Setenv("WAYLAND_DISPLAY", test.wayland)
			t.Setenv("DISPLAY", test.display)
			command, arguments, err := clipboardReadCommand()
			if test.wantError {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("clipboardReadCommand() error = %v, want %v", err, ErrUnavailable)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(command) != test.wantCommand || !slices.Equal(arguments, test.wantArgs) {
				t.Fatalf("clipboardReadCommand() = %q %q, want %q %q",
					command, arguments, test.wantCommand, test.wantArgs)
			}
		})
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

func installDeliveryHelperCommands(t *testing.T, names ...string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for _, name := range names {
		if err := os.Symlink(binary, filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory)
	t.Setenv("RVW_DELIVERY_HELPER", "1")
	return directory
}

func configurePlatformReadEnvironment(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	}
}

func platformReadCommandName(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		return "pbpaste"
	case "linux":
		return "wl-paste"
	default:
		t.Fatalf("unsupported test platform %s", runtime.GOOS)
		return ""
	}
}

func platformWriteCommandName(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		return "pbcopy"
	case "linux":
		return "wl-copy"
	default:
		t.Fatalf("unsupported test platform %s", runtime.GOOS)
		return ""
	}
}

func waitForDeliveryHelper(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("delivery helper did not create %s", path)
		case <-poll.C:
		}
	}
}

func receiveError(t *testing.T, resultChannel <-chan error) error {
	t.Helper()
	select {
	case err := <-resultChannel:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("operation did not finish")
		return nil
	}
}

type clipboardReadResult struct {
	value string
	err   error
}

type clipboardWriteResult struct {
	outcome Outcome
	err     error
}

func receiveReadResult(
	t *testing.T,
	resultChannel <-chan clipboardReadResult,
) clipboardReadResult {
	t.Helper()
	select {
	case result := <-resultChannel:
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("clipboard read did not finish")
		return clipboardReadResult{}
	}
}

func receiveWriteResult(
	t *testing.T,
	resultChannel <-chan clipboardWriteResult,
) clipboardWriteResult {
	t.Helper()
	select {
	case result := <-resultChannel:
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("clipboard write did not finish")
		return clipboardWriteResult{}
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
