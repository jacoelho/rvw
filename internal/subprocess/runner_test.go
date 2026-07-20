package subprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	request := helperRequest("exit7")
	request.AcceptedExits = []int{7}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
}

func TestRunnerErrorAfterStartPreservesCommitBoundary(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	result, err := runner.Run(t.Context(), helperRequest("exit7"))
	if err == nil {
		t.Fatal("unaccepted exit succeeded")
	}
	if !result.Started {
		t.Fatalf("started failing process lost its commit boundary: %+v", result)
	}
}

func TestRunnerCancellationEscalatesAndWaits(t *testing.T) {
	runner := newTestRunner(t)
	defer shutdownTestRunner(t, runner)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	result, err := runner.Run(ctx, helperRequest("ignore-term"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if !result.Terminated || !result.Killed || result.NonGraceful {
		t.Fatalf("unexpected cancellation result: %+v", result)
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
	case "exit7":
		os.Exit(7)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(10 * time.Second)
	case "parent-exits-child-ignores-term":
		command := exec.Command(
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
		defer null.Close()
		command.Stdin = null
		command.Stdout = null
		command.Stderr = null
		if err := command.Start(); err != nil {
			os.Exit(4)
		}
		time.Sleep(10 * time.Second)
	case "parent-exits-escaped-child":
		command := exec.Command(
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
		defer null.Close()
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
			os.Exit(4)
		}
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

func helperRequest(mode string) Request {
	return Request{
		Name: os.Args[0],
		Args: []string{"-test.run=TestSubprocessHelper", "--", mode},
		Env:  map[string]string{"RVW_SUBPROCESS_HELPER": "1"},
	}
}

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	runner, err := NewRunner(DefaultConcurrency)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func shutdownTestRunner(t *testing.T, runner *Runner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Shutdown(ctx); err != nil {
		t.Error(err)
	}
}
