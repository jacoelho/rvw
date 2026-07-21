package subprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	DefaultStdoutLimit int64 = 64 << 20
	DefaultStderrLimit int64 = 1 << 20
	DefaultStdinLimit  int64 = 64 << 20
	DefaultTimeout           = 30 * time.Second
	DefaultTermGrace         = 750 * time.Millisecond
	DefaultReapBound         = 5 * time.Second
	DefaultConcurrency       = 4
)

var (
	ErrClosed      = errors.New("subprocess runner is closed")
	ErrLimited     = errors.New("subprocess output exceeded its limit")
	ErrNonGraceful = errors.New("subprocess group did not quiesce")
)

type Request struct {
	Name            string
	Args            []string
	Dir             string
	Env             map[string]string
	Stdin           []byte
	StdoutLimit     int64
	StderrLimit     int64
	Timeout         time.Duration
	AcceptedExits   []int
	OperationDetail string
}

type Result struct {
	Stdout      []byte
	Stderr      []byte
	ExitCode    int
	Started     bool
	Terminated  bool
	Killed      bool
	NonGraceful bool
}

type LimitError struct {
	Stream Stream
	Limit  int64
}

func (err *LimitError) Error() string {
	return fmt.Sprintf("%s: %s exceeded %d bytes", ErrLimited, err.Stream, err.Limit)
}

func (err *LimitError) Unwrap() error {
	return ErrLimited
}

type ExitError struct {
	Name     string
	ExitCode int
	Stderr   string
}

type StartError struct {
	Name  string
	cause error
}

func (err *StartError) Error() string {
	return fmt.Sprintf("%s start: %v", err.Name, err.cause)
}

func (err *StartError) Unwrap() error {
	return err.cause
}

func (err *ExitError) Error() string {
	if err.Stderr == "" {
		return fmt.Sprintf("%s exited with status %d", err.Name, err.ExitCode)
	}
	return fmt.Sprintf("%s exited with status %d: %s", err.Name, err.ExitCode, err.Stderr)
}

type NonGracefulError struct {
	PID int
}

func (err *NonGracefulError) Error() string {
	return fmt.Sprintf("%s: process tree rooted at %d did not quiesce", ErrNonGraceful, err.PID)
}

func (err *NonGracefulError) Unwrap() error {
	return ErrNonGraceful
}

type Runner struct {
	limits chan struct{}

	mu     sync.Mutex
	closed bool
	live   map[int]*os.Process
	runs   sync.WaitGroup
}

type Stream string

const (
	StreamStdin  Stream = "stdin"
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

func NewRunner(concurrency int) (*Runner, error) {
	if concurrency <= 0 || concurrency > DefaultConcurrency {
		return nil, fmt.Errorf("subprocess concurrency must be from 1 through %d", DefaultConcurrency)
	}
	return &Runner{
		limits: make(chan struct{}, concurrency),
		live:   make(map[int]*os.Process),
	}, nil
}

func (runner *Runner) Run(ctx context.Context, request Request) (Result, error) {
	request, err := validatedRequest(request)
	if err != nil {
		return Result{}, err
	}
	if err := context.Cause(ctx); err != nil {
		return Result{}, err
	}

	select {
	case runner.limits <- struct{}{}:
	case <-ctx.Done():
		return Result{}, context.Cause(ctx)
	}

	if err := runner.admit(); err != nil {
		<-runner.limits
		return Result{}, ErrClosed
	}
	waitStarted := false
	runDone := make(chan struct{})
	defer func() {
		close(runDone)
		if !waitStarted {
			runner.release()
		}
	}()

	runContext, cancel := context.WithTimeoutCause(
		ctx,
		request.Timeout,
		fmt.Errorf("%s exceeded %s", operationName(request), request.Timeout),
	)
	defer cancel()

	command := exec.Command(request.Name, request.Args...)
	command.Dir = request.Dir
	command.Env = mergedEnvironment(request.Env)
	command.Stdin = bytes.NewReader(request.Stdin)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = DefaultReapBound
	lifetime, inheritedLifetime, err := newProcessLifetime()
	if err != nil {
		return Result{}, fmt.Errorf("create subprocess lifetime witness: %w", err)
	}
	defer lifetime.Close()
	command.ExtraFiles = []*os.File{inheritedLifetime}
	command.Env = append(command.Env, "RVW_PROCESS_LIFETIME_FD=3")
	limited := make(chan *LimitError, 1)
	stdout := newCapture(request.StdoutLimit, StreamStdout, limited)
	stderr := newCapture(request.StderrLimit, StreamStderr, limited)
	command.Stdout = stdout
	command.Stderr = stderr

	if err := command.Start(); err != nil {
		_ = inheritedLifetime.Close()
		return Result{}, &StartError{Name: operationName(request), cause: err}
	}
	_ = inheritedLifetime.Close()
	pid := command.Process.Pid
	if err := runner.register(pid, command.Process); err != nil {
		_ = signalProcessGroup(pid, syscall.SIGKILL)
		_ = command.Wait()
		return Result{Started: true}, err
	}

	waited := newWaitState()
	waitStarted = true
	go func() {
		waitErr := command.Wait()
		waited.complete(waitErr)
		runner.unregister(pid)
		<-runDone
		runner.release()
	}()

	result, waitErr, runErr := runner.await(
		runContext,
		request,
		pid,
		waited,
		lifetime,
		limited,
	)
	result.Started = true
	if completed := waited.completed(); completed {
		result.Stdout = stdout.TakeBytes()
		result.Stderr = stderr.TakeBytes()
		if stdout.Exceeded() {
			return result, &LimitError{Stream: StreamStdout, Limit: request.StdoutLimit}
		}
		if stderr.Exceeded() {
			return result, &LimitError{Stream: StreamStderr, Limit: request.StderrLimit}
		}
	}
	if runErr != nil {
		return result, runErr
	}
	if waitErr != nil && !slices.Contains(request.AcceptedExits, result.ExitCode) {
		return result, &ExitError{
			Name:     operationName(request),
			ExitCode: result.ExitCode,
			Stderr:   strings.TrimSpace(string(result.Stderr)),
		}
	}
	return result, nil
}

func (runner *Runner) StopAdmission() {
	runner.mu.Lock()
	runner.closed = true
	runner.mu.Unlock()
}

func (runner *Runner) Escalate() {
	runner.mu.Lock()
	processes := make(map[int]*os.Process, len(runner.live))
	for pid, process := range runner.live {
		processes[pid] = process
	}
	runner.mu.Unlock()

	for pid := range processes {
		_ = signalProcessGroup(pid, syscall.SIGKILL)
	}
}

func (runner *Runner) Shutdown(ctx context.Context) error {
	runner.StopAdmission()
	runner.signalAll(syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		runner.runs.Wait()
		close(done)
	}()

	grace := time.NewTimer(DefaultTermGrace)
	defer grace.Stop()
	select {
	case <-done:
		return nil
	case <-grace.C:
		runner.Escalate()
	case <-ctx.Done():
		runner.Escalate()
		return fmt.Errorf("shutdown subprocesses: %w", context.Cause(ctx))
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		runner.Escalate()
		return fmt.Errorf("shutdown subprocesses: %w", context.Cause(ctx))
	}
}

func (runner *Runner) await(
	ctx context.Context,
	request Request,
	pid int,
	waited *waitState,
	lifetime *processLifetime,
	limited <-chan *LimitError,
) (Result, error, error) {
	select {
	case <-waited.done:
		waitErr := waited.err()
		result := resultFromWait(waitErr)
		if errors.Is(waitErr, exec.ErrWaitDelay) {
			result.NonGraceful = true
			result.Killed = true
			_ = signalProcessGroup(pid, syscall.SIGKILL)
			_ = waitForQuiescence(pid, lifetime, DefaultReapBound)
			return result, waitErr, &NonGracefulError{PID: pid}
		}
		if processTreeQuiescent(pid, lifetime) {
			return result, waitErr, nil
		}
		result.Terminated = true
		_ = signalProcessGroup(pid, syscall.SIGTERM)
		if waitForQuiescence(pid, lifetime, DefaultTermGrace) {
			return result, waitErr, nil
		}
		return runner.escalate(
			pid,
			nil,
			lifetime,
			result,
			waitErr,
			processAlreadyWaited,
		)
	case limitErr := <-limited:
		result := Result{ExitCode: -1, Terminated: true}
		_ = signalProcessGroup(pid, syscall.SIGTERM)
		waitErr, completed := waitForProcessTree(
			pid,
			waited,
			lifetime,
			DefaultTermGrace,
		)
		if completed {
			result = resultFromWait(waitErr)
			result.Terminated = true
			return result, waitErr, limitErr
		}
		return runner.escalate(
			pid,
			waited,
			lifetime,
			result,
			waitErr,
			processWaitPending,
			limitErr,
		)
	case <-ctx.Done():
		result := Result{ExitCode: -1, Terminated: true}
		_ = signalProcessGroup(pid, syscall.SIGTERM)
		waitErr, completed := waitForProcessTree(
			pid,
			waited,
			lifetime,
			DefaultTermGrace,
		)
		if completed {
			result = resultFromWait(waitErr)
			result.Terminated = true
			return result, waitErr, context.Cause(ctx)
		}
		return runner.escalate(
			pid,
			waited,
			lifetime,
			result,
			waitErr,
			processWaitPending,
			context.Cause(ctx),
		)
	}
}

type processWaitState uint8

const (
	processWaitPending processWaitState = iota
	processAlreadyWaited
)

func (runner *Runner) escalate(
	pid int,
	waited *waitState,
	lifetime *processLifetime,
	result Result,
	waitErr error,
	waitState processWaitState,
	causes ...error,
) (Result, error, error) {
	result.Killed = true
	_ = signalProcessGroup(pid, syscall.SIGKILL)
	if waitState != processAlreadyWaited {
		var completed bool
		waitErr, completed = waitForProcessTree(
			pid,
			waited,
			lifetime,
			DefaultReapBound,
		)
		if !completed {
			result.NonGraceful = true
			return result, waitErr, errors.Join(append(causes, &NonGracefulError{PID: pid})...)
		}
		result.ExitCode = exitCode(waitErr)
	}

	if !waitForQuiescence(pid, lifetime, DefaultReapBound) {
		result.NonGraceful = true
		return result, waitErr, errors.Join(append(causes, &NonGracefulError{PID: pid})...)
	}
	if len(causes) != 0 {
		return result, waitErr, errors.Join(causes...)
	}
	return result, waitErr, nil
}

func (runner *Runner) admit() error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.closed {
		return ErrClosed
	}
	runner.runs.Add(1)
	return nil
}

func (runner *Runner) release() {
	<-runner.limits
	runner.runs.Done()
}

func (runner *Runner) register(pid int, process *os.Process) error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.closed {
		return ErrClosed
	}
	runner.live[pid] = process
	return nil
}

func (runner *Runner) unregister(pid int) {
	runner.mu.Lock()
	delete(runner.live, pid)
	runner.mu.Unlock()
}

type capture struct {
	limit    int64
	stream   Stream
	limited  chan<- *LimitError
	buffer   bytes.Buffer
	exceeded bool
}

type waitState struct {
	done    chan struct{}
	waitErr error
}

type processLifetime struct {
	reader *os.File
	done   chan struct{}
}

func newProcessLifetime() (*processLifetime, *os.File, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	lifetime := &processLifetime{reader: reader, done: make(chan struct{})}
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(lifetime.done)
	}()
	return lifetime, writer, nil
}

func (lifetime *processLifetime) completed() bool {
	select {
	case <-lifetime.done:
		return true
	default:
		return false
	}
}

func (lifetime *processLifetime) Close() {
	if lifetime != nil && lifetime.reader != nil {
		_ = lifetime.reader.Close()
	}
}

func newWaitState() *waitState {
	return &waitState{done: make(chan struct{})}
}

func (state *waitState) complete(err error) {
	state.waitErr = err
	close(state.done)
}

func (state *waitState) completed() bool {
	select {
	case <-state.done:
		return true
	default:
		return false
	}
}

func (state *waitState) err() error {
	<-state.done
	return state.waitErr
}

func newCapture(limit int64, stream Stream, limited chan<- *LimitError) *capture {
	return &capture{limit: limit, stream: stream, limited: limited}
}

func (capture *capture) Write(value []byte) (int, error) {
	original := len(value)
	remaining := capture.limit - int64(capture.buffer.Len())
	if remaining > 0 {
		retained := min(remaining, int64(len(value)))
		_, _ = capture.buffer.Write(value[:int(retained)])
	}
	if int64(original) > remaining {
		if !capture.exceeded {
			capture.exceeded = true
			select {
			case capture.limited <- &LimitError{Stream: capture.stream, Limit: capture.limit}:
			default:
			}
		}
	}
	return original, nil
}

func (capture *capture) TakeBytes() []byte {
	value := capture.buffer.Bytes()
	capture.buffer = bytes.Buffer{}
	return value
}

func (capture *capture) Exceeded() bool {
	return capture.exceeded
}

func validatedRequest(request Request) (Request, error) {
	if request.Name == "" || strings.IndexByte(request.Name, 0) >= 0 {
		return Request{}, errors.New("subprocess executable must be nonempty and contain no NUL")
	}
	for _, argument := range request.Args {
		if strings.IndexByte(argument, 0) >= 0 {
			return Request{}, errors.New("subprocess argument contains NUL")
		}
	}
	for key, value := range request.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 {
			return Request{}, fmt.Errorf("invalid subprocess environment key %q", key)
		}
	}
	if int64(len(request.Stdin)) > DefaultStdinLimit {
		return Request{}, &LimitError{Stream: StreamStdin, Limit: DefaultStdinLimit}
	}
	if request.StdoutLimit == 0 {
		request.StdoutLimit = DefaultStdoutLimit
	}
	if request.StderrLimit == 0 {
		request.StderrLimit = DefaultStderrLimit
	}
	if request.StdoutLimit < 0 || request.StdoutLimit > DefaultStdoutLimit {
		return Request{}, fmt.Errorf("stdout limit must be from 1 through %d", DefaultStdoutLimit)
	}
	if request.StderrLimit < 0 || request.StderrLimit > DefaultStderrLimit {
		return Request{}, fmt.Errorf("stderr limit must be from 1 through %d", DefaultStderrLimit)
	}
	if request.Timeout == 0 {
		request.Timeout = DefaultTimeout
	}
	if request.Timeout < 0 || request.Timeout > DefaultTimeout {
		return Request{}, fmt.Errorf("timeout must be from 1ns through %s", DefaultTimeout)
	}
	if len(request.AcceptedExits) == 0 {
		request.AcceptedExits = []int{0}
	} else {
		request.AcceptedExits = slices.Clone(request.AcceptedExits)
		for _, code := range request.AcceptedExits {
			if code < 0 || code > 255 {
				return Request{}, fmt.Errorf("accepted exit status %d is outside 0..255", code)
			}
		}
	}
	request.Args = slices.Clone(request.Args)
	request.Stdin = bytes.Clone(request.Stdin)
	return request, nil
}

func mergedEnvironment(overrides map[string]string) []string {
	environment := os.Environ()
	if len(overrides) == 0 {
		return environment
	}
	for key, value := range overrides {
		prefix := key + "="
		replaced := false
		for index, entry := range environment {
			if strings.HasPrefix(entry, prefix) {
				environment[index] = prefix + value
				replaced = true
				break
			}
		}
		if !replaced {
			environment = append(environment, prefix+value)
		}
	}
	return environment
}

func operationName(request Request) string {
	if request.OperationDetail != "" {
		return request.OperationDetail
	}
	return request.Name
}

func waitForProcessTree(
	pid int,
	waited *waitState,
	lifetime *processLifetime,
	bound time.Duration,
) (error, bool) {
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var (
		waitErr  error
		waitedOK = waited.completed()
		waitDone = waited.done
	)
	if waitedOK {
		waitErr = waited.err()
		waitDone = nil
	}
	for {
		if waitedOK && processTreeQuiescent(pid, lifetime) {
			return waitErr, true
		}
		select {
		case <-waitDone:
			waitErr = waited.err()
			waitedOK = true
			waitDone = nil
		case <-ticker.C:
		case <-deadline.C:
			return waitErr, false
		}
	}
}

func resultFromWait(err error) Result {
	return Result{ExitCode: exitCode(err)}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processGroupExists(pid int) bool {
	err := syscall.Kill(-pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func processTreeQuiescent(pid int, lifetime *processLifetime) bool {
	return !processGroupExists(pid) && lifetime.completed()
}

func waitForQuiescence(
	pid int,
	lifetime *processLifetime,
	bound time.Duration,
) bool {
	deadline := time.Now().Add(bound)
	for !processTreeQuiescent(pid, lifetime) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return processTreeQuiescent(pid, lifetime)
}

func (runner *Runner) signalAll(signal syscall.Signal) {
	runner.mu.Lock()
	pids := make([]int, 0, len(runner.live))
	for pid := range runner.live {
		pids = append(pids, pid)
	}
	runner.mu.Unlock()
	for _, pid := range pids {
		_ = signalProcessGroup(pid, signal)
	}
}
