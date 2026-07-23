package subprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestRunnerCancellationWhileWaitingForGlobalPermitReleasesRegistration(t *testing.T) {
	release := holdProcessPermits(t, MaxConcurrentProcesses)
	runner := NewRunner()
	if err := runner.admit(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		result <- runner.acquireProcessPermit(ctx)
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("permit wait error = %v, want context cancellation", err)
	}
	runner.runs.Done()
	release()
	shutdownTestRunner(t, runner)
}

func TestRunnerShutdownUnblocksGlobalPermitWaiter(t *testing.T) {
	holdProcessPermits(t, MaxConcurrentProcesses)
	runner := NewRunner()
	if err := runner.admit(); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		result <- runner.acquireProcessPermit(t.Context())
	}()
	<-started
	runner.StopAdmission()
	if err := <-result; !errors.Is(err, ErrClosed) {
		t.Fatalf("permit wait error = %v, want ErrClosed", err)
	}
	runner.runs.Done()
	if err := runner.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRunnersShareOneGlobalProcessLimit(t *testing.T) {
	runners := make([]*Runner, MaxConcurrentProcesses)
	for index := range runners {
		runners[index] = NewRunner()
		if err := runners[index].admit(); err != nil {
			t.Fatal(err)
		}
		if err := runners[index].acquireProcessPermit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, runner := range runners {
			releaseProcessPermit()
			runner.runs.Done()
		}
	})

	fifth := NewRunner()
	if err := fifth.admit(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := fifth.acquireProcessPermit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("fifth permit error = %v, want context cancellation", err)
	}
	fifth.runs.Done()
}

func TestRunnerPrestartFailureReleasesGlobalPermit(t *testing.T) {
	holdProcessPermits(t, MaxConcurrentProcesses-1)
	runner := NewRunner()
	request := helperRequest("streams")
	request.Name = "rvw-command-that-does-not-exist"
	if _, err := runner.Run(t.Context(), request); err == nil {
		t.Fatal("missing executable unexpectedly started")
	}

	probe := NewRunner()
	if err := probe.admit(); err != nil {
		t.Fatal(err)
	}
	if err := probe.acquireProcessPermit(t.Context()); err != nil {
		t.Fatalf("released permit was not reusable: %v", err)
	}
	releaseProcessPermit()
	probe.runs.Done()
}

func TestRunnerWaitCompletionReleasesGlobalPermit(t *testing.T) {
	runner := NewRunner()
	defer shutdownTestRunner(t, runner)
	request := helperRequest("exit7")
	request.AcceptedExits = []int{7}
	for range MaxConcurrentProcesses + 1 {
		if _, err := runner.Run(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunnerCapturesConcurrentStreamsAndExit(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	result, err := runner.Run(t.Context(), helperRequest("streams"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.Stdout), strings.Repeat("o", 16<<10); got != want {
		t.Fatalf("stdout length = %d, want %d", len(got), len(want))
	}
	if got, want := string(result.Stderr), strings.Repeat("e", 16<<10); got != want {
		t.Fatalf("stderr length = %d, want %d", len(got), len(want))
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
	if !result.Started {
		t.Fatal("successful process was not marked started")
	}
}

func TestRunnerPreCancelledRequestNeverStarts(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := runner.Run(ctx, helperRequest("streams"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancelled", err)
	}
	if result.Started {
		t.Fatalf("pre-cancelled process was marked started: %+v", result)
	}
}

func TestRunnerReportsOutputLimit(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	request := helperRequest("stdout-forever")
	request.StdoutLimit = 4
	request.Timeout = 5 * time.Second
	started := time.Now()
	result, err := runner.Run(t.Context(), request)
	if !errors.Is(err, ErrLimited) {
		t.Fatalf("Run() error = %v, want ErrLimited", err)
	}
	if string(result.Stdout) != "xxxx" {
		t.Fatalf("captured stdout = %q", result.Stdout)
	}
	if !result.Terminated {
		t.Fatalf("output-limited process was not terminated: %+v", result)
	}
	if elapsed := time.Since(started); elapsed >= request.Timeout {
		t.Fatalf("output limit waited for process timeout: %s", elapsed)
	}
}

func TestRunnerAcceptedExitCodes(t *testing.T) {
	tests := []struct {
		name          string
		mode          string
		acceptedExits []int
		wantExit      int
		wantError     bool
	}{
		{name: "default accepts zero", mode: "stdout", wantExit: 0},
		{name: "explicit set accepts zero", mode: "stdout", acceptedExits: []int{0}, wantExit: 0},
		{name: "explicit set accepts nonzero", mode: "exit7", acceptedExits: []int{7}, wantExit: 7},
		{name: "explicit set rejects zero", mode: "stdout", acceptedExits: []int{1}, wantExit: 0, wantError: true},
		{name: "explicit set rejects nonzero", mode: "exit7", acceptedExits: []int{0}, wantExit: 7, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := newTestRunner(t)
			defer shutdownTestRunner(t, runner)
			request := helperRequest(test.mode)
			request.AcceptedExits = test.acceptedExits
			result, err := runner.Run(t.Context(), request)
			if result.ExitCode != test.wantExit || !result.Started ||
				result.Terminated || result.Killed || result.NonGraceful {
				t.Fatalf(
					"result = %+v, want started clean exit %d",
					result,
					test.wantExit,
				)
			}
			if !test.wantError {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			exitErr, ok := errors.AsType[*ExitError](err)
			if !ok || exitErr.ExitCode != test.wantExit {
				t.Fatalf("exit error = %v, want *ExitError with code %d", err, test.wantExit)
			}
		})
	}
}

func TestRunnerErrorAfterStartPreservesCommitBoundary(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	result, err := runner.Run(t.Context(), helperRequest("exit7-stderr"))
	if err == nil {
		t.Fatal("unaccepted exit succeeded")
	}
	if !result.Started {
		t.Fatalf("started failing process lost its commit boundary: %+v", result)
	}
	if len(result.Stderr) != 0 {
		t.Fatalf("failing result retained stderr alongside its error: %q", result.Stderr)
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Stderr != "failure detail" {
		t.Fatalf("exit error did not take stderr ownership: %v", err)
	}
}

func TestRunnerRegularFileSpoolsTransferStreams(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	input := createTestSpool(t, []byte("spooled input"))
	output := createTestSpool(t, nil)
	inputSpool, err := NewInputSpool(input)
	if err != nil {
		t.Fatal(err)
	}
	outputSpool, err := NewOutputSpool(output)
	if err != nil {
		t.Fatal(err)
	}
	request := helperRequest("stdin-copy")
	request.InputSpool = inputSpool
	request.InputSpoolLimit = 128
	request.OutputSpool = outputSpool
	request.OutputSpoolLimit = DefaultStdoutLimit + 1
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stdout) != 0 {
		t.Fatalf("spooled result retained captured stdout: %q", result.Stdout)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "spooled input" {
		t.Fatalf("spooled output = %q", got)
	}
}

func TestRunnerOutputSpoolAcceptsProtocolStreamAboveCaptureLimit(t *testing.T) {
	const size = DefaultStdoutLimit + 1<<20
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	output := createTestSpool(t, nil)
	spool, err := NewOutputSpool(output)
	if err != nil {
		t.Fatal(err)
	}
	request := helperRequest("stdout-bytes")
	request.Env["RVW_OUTPUT_BYTES"] = strconv.FormatInt(size, 10)
	request.OutputSpool = spool
	request.OutputSpoolLimit = size
	if _, err := runner.Run(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	info, err := output.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Fatalf("spooled bytes = %d, want %d", info.Size(), size)
	}
}

func TestRunnerOutputSpoolLimitTerminatesAndReleasesPermit(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	output := createTestSpool(t, nil)
	spool, err := NewOutputSpool(output)
	if err != nil {
		t.Fatal(err)
	}
	request := helperRequest("stdout-forever")
	request.OutputSpool = spool
	request.OutputSpoolLimit = 16 << 10
	result, err := runner.Run(t.Context(), request)
	var limitErr *LimitError
	if !errors.As(err, &limitErr) || limitErr.Stream != StreamStdout {
		t.Fatalf("spool limit error = %v", err)
	}
	if !result.Started || !result.Terminated {
		t.Fatalf("spool-limited process was not terminated: %+v", result)
	}
	if _, err := runner.Run(t.Context(), helperRequest("stdout")); err != nil {
		t.Fatalf("permit was not reusable: %v", err)
	}
}

func TestSpoolCaptureFileFailureSignalsRunFailure(t *testing.T) {
	file := createTestSpool(t, nil)
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	streamErrors := make(chan error, 1)
	capture := newSpoolCapture(file, 16, StreamStdout, streamErrors)
	if _, err := capture.Write([]byte("value")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-streamErrors:
		if err == nil || !strings.Contains(err.Error(), "write stdout spool") {
			t.Fatalf("spool failure = %v", err)
		}
	default:
		t.Fatal("spool failure was not signaled")
	}
}

func TestTerminalSafeErrorDetailBoundsAndEscapesExternalBytes(t *testing.T) {
	unsafe := append([]byte(" \x1b[31m\xe2\x80\xae\xffdetail"),
		bytes.Repeat([]byte("x"), int(MaxErrorDetailBytes))...)
	detail, truncated := terminalSafeErrorDetail(unsafe)
	if !truncated {
		t.Fatal("oversized detail was not marked truncated")
	}
	if int64(len(detail)) > MaxErrorDetailBytes {
		t.Fatalf("detail uses %d bytes, limit %d", len(detail), MaxErrorDetailBytes)
	}
	for _, forbidden := range []string{"\x1b", "\u202e", string([]byte{0xff})} {
		if strings.Contains(detail, forbidden) {
			t.Fatalf("detail retained unsafe bytes %q: %q", forbidden, detail)
		}
	}
	for _, escaped := range []string{`\u{001B}`, `\u{202E}`, `\xFF`} {
		if !strings.Contains(detail, escaped) {
			t.Fatalf("detail lacks escape %q: %q", escaped, detail)
		}
	}
	if got, truncated := terminalSafeErrorDetail([]byte(" \t\n ")); got != "" || truncated {
		t.Fatalf("whitespace detail = %q, truncated=%t", got, truncated)
	}
}

func TestRunnerCancellationEscalatesAndWaits(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	request := helperRequest("ignore-term")
	request.DirFile = directory
	result, err := runner.Run(ctx, request)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if !result.Terminated || !result.Killed || result.NonGraceful {
		t.Fatalf("unexpected cancellation result: %+v", result)
	}
	if _, statErr := directory.Stat(); statErr != nil {
		t.Fatalf("cancellation closed borrowed directory descriptor: %v", statErr)
	}
}

func TestRunnerEscalatesAfterParentWaitCompletes(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	result, err := runner.Run(ctx, helperRequest("parent-exits-child-ignores-term"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if !result.Terminated || !result.Killed || result.NonGraceful {
		t.Fatalf("unexpected escalation result: %+v", result)
	}
}

func TestRunnerReportsEscapedDescendantAsNonGraceful(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	pidFile := t.TempDir() + "/escaped.pid"
	request := helperRequest("parent-exits-escaped-child")
	request.Env["RVW_ESCAPED_PID_FILE"] = pidFile
	result, err := runner.Run(t.Context(), request)
	if !errors.Is(err, ErrNonGraceful) {
		t.Fatalf("Run() error = %v, want ErrNonGraceful", err)
	}
	if !result.NonGraceful {
		t.Fatalf("escaped descendant result = %+v", result)
	}
	pidBytes, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read escaped descendant identity: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(string(pidBytes))
	if parseErr != nil {
		t.Fatalf("parse escaped descendant identity: %v", parseErr)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func TestRunnerAdoptsEscapedDescendantAfterShutdownRecordsTERM(t *testing.T) {
	runner := NewRunner()
	started := make(chan struct{})
	release := make(chan struct{})
	runner.testAfterStart = func() {
		close(started)
		<-release
	}
	pidFile := t.TempDir() + "/escaped.pid"
	request := helperRequest("parent-exits-escaped-child")
	request.Env["RVW_ESCAPED_PID_FILE"] = pidFile
	type runOutcome struct {
		result Result
		err    error
	}
	runDone := make(chan runOutcome, 1)
	go func() {
		result, err := runner.Run(t.Context(), request)
		runDone <- runOutcome{result: result, err: err}
	}()
	<-started
	escapedPID := waitForTestPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(escapedPID, syscall.SIGKILL) })

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- runner.Shutdown(shutdownContext) }()
	<-runner.stopped
	close(release)

	outcome := <-runDone
	if !errors.Is(outcome.err, ErrNonGraceful) {
		t.Fatalf("Run() error = %v, want ErrNonGraceful", outcome.err)
	}
	if !outcome.result.Started || !outcome.result.Terminated ||
		!outcome.result.Killed || !outcome.result.NonGraceful {
		t.Fatalf("raced TERM result = %+v", outcome.result)
	}
	if err := <-shutdownDone; err != nil && !errors.Is(err, syscall.EPERM) {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestRunnerAdoptsEscapedDescendantAfterKILLRequested(t *testing.T) {
	runner := NewRunner()
	started := make(chan struct{})
	release := make(chan struct{})
	runner.testAfterStart = func() {
		close(started)
		<-release
	}
	pidFile := t.TempDir() + "/escaped.pid"
	request := helperRequest("parent-exits-escaped-child")
	request.Env["RVW_ESCAPED_PID_FILE"] = pidFile
	type runOutcome struct {
		result Result
		err    error
	}
	runDone := make(chan runOutcome, 1)
	go func() {
		result, err := runner.Run(t.Context(), request)
		runDone <- runOutcome{result: result, err: err}
	}()
	<-started
	escapedPID := waitForTestPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(escapedPID, syscall.SIGKILL) })

	runner.Escalate()
	close(release)
	outcome := <-runDone
	if !errors.Is(outcome.err, ErrNonGraceful) {
		t.Fatalf("Run() error = %v, want ErrNonGraceful", outcome.err)
	}
	if !outcome.result.Started || !outcome.result.Terminated ||
		!outcome.result.Killed || !outcome.result.NonGraceful {
		t.Fatalf("raced KILL result = %+v", outcome.result)
	}
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := runner.Shutdown(shutdownContext); err != nil && !errors.Is(err, syscall.EPERM) {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestRunnerRejectsUnsafeRequests(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	for _, request := range []Request{
		{},
		{Name: "echo", Args: []string{"a\x00b"}},
		{Name: "echo", Env: map[string]string{"BAD=KEY": "value"}},
		{Name: "echo", Stdin: make([]byte, int(DefaultStdinLimit)+1)},
	} {
		if _, err := runner.Run(t.Context(), request); err == nil {
			t.Fatalf("Run(%+v) succeeded", request)
		}
	}
}

func TestRunnerBorrowedFileUsesStableChildDescriptorAndRetainsCallerOwnership(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	borrowed := createTestSpool(t, []byte("borrowed capability"))
	request := helperRequest("borrowed-file")
	request.DirFile = directory
	request.BorrowedFiles = []*os.File{borrowed}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Stdout); got != "borrowed capability" {
		t.Fatalf("borrowed file contents = %q", got)
	}
	if _, err := borrowed.Stat(); err != nil {
		t.Fatalf("Run closed caller-owned borrowed file: %v", err)
	}
}

func TestRunnerRejectsInvalidBorrowedFiles(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	borrowed := createTestSpool(t, nil)
	closed := createTestSpool(t, nil)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]*os.File, MaxBorrowedFiles+1)
	for index := range tooMany {
		tooMany[index] = borrowed
	}
	for name, request := range map[string]Request{
		"without directory": {
			Name: "true", BorrowedFiles: []*os.File{borrowed},
		},
		"nil": {
			Name: "true", DirFile: directory, BorrowedFiles: []*os.File{nil},
		},
		"closed": {
			Name: "true", DirFile: directory, BorrowedFiles: []*os.File{closed},
		},
		"over limit": {
			Name: "true", DirFile: directory, BorrowedFiles: tooMany,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runner.Run(t.Context(), request); err == nil {
				t.Fatal("invalid borrowed files unexpectedly accepted")
			}
		})
	}
}

func TestRunnerDirectoryDescriptorPinsRenamedWorkingDirectory(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	parent := t.TempDir()
	original := parent + "/repository"
	moved := parent + "/repository-moved"
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original+"/marker", []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(original)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original+"/marker", []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := helperRequest("cwd-marker")
	request.DirFile = directory
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Stdout); got != "original" {
		t.Fatalf("working directory marker = %q, want original", got)
	}
	if _, err := directory.Stat(); err != nil {
		t.Fatalf("Run closed borrowed directory descriptor: %v", err)
	}
}

func TestRunnerRejectsInvalidDirectoryDescriptors(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := helperRequest("stdout")
	request.Dir = t.TempDir()
	request.DirFile = directory
	if _, err := runner.Run(t.Context(), request); err == nil {
		t.Fatal("path and descriptor working directories were accepted together")
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	request.Dir = ""
	if _, err := runner.Run(t.Context(), request); err == nil {
		t.Fatal("closed working directory descriptor was accepted")
	}
	file, err := os.CreateTemp(t.TempDir(), "not-directory-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	request.DirFile = file
	if _, err := runner.Run(t.Context(), request); err == nil {
		t.Fatal("regular file working directory descriptor was accepted")
	}
}

func TestSubprocessHelper(t *testing.T) {
	if os.Getenv("RVW_SUBPROCESS_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "streams":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("o", 16<<10))
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("e", 16<<10))
	case "stdout":
		_, _ = fmt.Fprint(os.Stdout, "abcde")
	case "stdout-forever":
		block := strings.Repeat("x", 16<<10)
		for {
			_, _ = fmt.Fprint(os.Stdout, block)
		}
	case "stdout-bytes":
		remaining, err := strconv.ParseInt(os.Getenv("RVW_OUTPUT_BYTES"), 10, 64)
		if err != nil || remaining < 0 {
			os.Exit(3)
		}
		block := []byte(strings.Repeat("x", 64<<10))
		for remaining > 0 {
			chunk := int(min(remaining, int64(len(block))))
			written, writeErr := os.Stdout.Write(block[:chunk])
			if writeErr != nil {
				os.Exit(4)
			}
			remaining -= int64(written)
		}
	case "stdin-copy":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "cwd-marker":
		contents, err := os.ReadFile("marker")
		if err != nil {
			os.Exit(8)
		}
		_, _ = os.Stdout.Write(contents)
	case "borrowed-file":
		borrowed := os.NewFile(firstBorrowedFileDescriptor, "rvw-borrowed-file")
		if borrowed == nil {
			os.Exit(9)
		}
		contents, err := io.ReadAll(io.LimitReader(borrowed, 1<<10))
		if err != nil {
			os.Exit(10)
		}
		_, _ = os.Stdout.Write(contents)
	case "descriptor-layout":
		var witness, borrowed syscall.Stat_t
		if err := syscall.Fstat(3, &witness); err != nil ||
			witness.Mode&syscall.S_IFMT != syscall.S_IFIFO {
			os.Exit(11)
		}
		var directory syscall.Stat_t
		if err := syscall.Fstat(4, &directory); !errors.Is(err, syscall.EBADF) {
			os.Exit(12)
		}
		if err := syscall.Fstat(firstBorrowedFileDescriptor, &borrowed); err != nil {
			os.Exit(13)
		}
		_, _ = fmt.Fprint(os.Stdout, "fd3-witness fd4-closed fd5-borrowed")
	case "exit7":
		os.Exit(7)
	case "exit7-stderr":
		_, _ = fmt.Fprint(os.Stderr, "failure detail")
		os.Exit(7)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(10 * time.Second)
	case "parent-exits-child-ignores-term":
		command := exec.CommandContext(
			context.Background(),
			os.Args[0],
			"-test.run=TestSubprocessHelper",
			"--",
			"ignore-term",
		)
		command.Env = append(os.Environ(), "RVW_SUBPROCESS_HELPER=1")
		null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			os.Exit(3)
		}
		command.Stdin = null
		command.Stdout = null
		command.Stderr = null
		if err := command.Start(); err != nil {
			closeHelperFiles(null)
			os.Exit(4)
		}
		closeHelperFiles(null)
		time.Sleep(10 * time.Second)
	case "parent-exits-escaped-child":
		command := exec.CommandContext(
			context.Background(),
			os.Args[0],
			"-test.run=TestSubprocessHelper",
			"--",
			"escaped-child",
		)
		command.Env = append(os.Environ(), "RVW_SUBPROCESS_HELPER=1")
		null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			os.Exit(3)
		}
		command.Stdin = null
		command.Stdout = null
		command.Stderr = null
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		lifetime := os.NewFile(3, "rvw-process-lifetime")
		if lifetime == nil {
			os.Exit(5)
		}
		command.ExtraFiles = []*os.File{lifetime}
		if err := command.Start(); err != nil {
			closeHelperFiles(null, lifetime)
			os.Exit(4)
		}
		closeHelperFiles(null, lifetime)
	case "escaped-child":
		pidFile := os.Getenv("RVW_ESCAPED_PID_FILE")
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(6)
		}
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(10 * time.Second)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func createTestSpool(t *testing.T, contents []byte) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "spool-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.Write(contents); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	return file
}

func waitForTestPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		value, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(value))
			if parseErr != nil {
				t.Fatalf("parse escaped descendant identity: %v", parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read escaped descendant identity: %v", err)
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("timed out waiting for escaped descendant identity")
		}
	}
}

func closeHelperFiles(files ...*os.File) {
	for _, file := range files {
		if err := file.Close(); err != nil {
			os.Exit(7)
		}
	}
}

func helperRequest(mode string) Request {
	return Request{
		Name: os.Args[0],
		Args: []string{"-test.run=TestSubprocessHelper", "--", mode},
		Env:  map[string]string{"RVW_SUBPROCESS_HELPER": "1"},
	}
}

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	return NewRunner()
}

func shutdownTestRunner(t *testing.T, runner *Runner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Shutdown(ctx); err != nil {
		t.Error(err)
	}
}

func holdProcessPermits(tb testing.TB, count int) func() {
	tb.Helper()
	for range count {
		processPermits <- struct{}{}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for range count {
				releaseProcessPermit()
			}
		})
	}
	tb.Cleanup(release)
	return release
}
