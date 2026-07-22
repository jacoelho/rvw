// Package subprocess runs bounded, cancellable external commands.
package subprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultStdoutLimit identifies a supported default stdout limit value.
const (
	DefaultStdoutLimit int64 = 64 << 20
	DefaultStderrLimit int64 = 1 << 20
	DefaultStdinLimit  int64 = 64 << 20
	DefaultTimeout           = 30 * time.Second
	DefaultTermGrace         = 750 * time.Millisecond
	DefaultReapBound         = 5 * time.Second
	DefaultConcurrency       = 4
)

// ErrClosed identifies an closed failure.
var (
	ErrClosed      = errors.New("subprocess runner is closed")
	ErrLimited     = errors.New("subprocess output exceeded its limit")
	ErrNonGraceful = errors.New("subprocess group did not quiesce")
)

// Request describes request.
type Request struct {
	Env             map[string]string
	Name            string
	Dir             string
	OperationDetail string
	Args            []string
	Stdin           []byte
	AcceptedExits   []int
	StdoutLimit     int64
	StderrLimit     int64
	Timeout         time.Duration
}

// Result describes result.
type Result struct {
	Stdout      []byte
	Stderr      []byte
	ExitCode    int
	Started     bool
	Terminated  bool
	Killed      bool
	NonGraceful bool
}

// LimitError describes limit error.
type LimitError struct {
	Stream Stream
	Limit  int64
}

// Error returns the receiver's error value.
func (err *LimitError) Error() string {
	return fmt.Sprintf("%s: %s exceeded %d bytes", ErrLimited, err.Stream, err.Limit)
}

// Unwrap returns the receiver's unwrap value.
func (err *LimitError) Unwrap() error {
	return ErrLimited
}

// ExitError describes exit error.
type ExitError struct {
	Name     string
	Stderr   string
	ExitCode int
}

// StartError describes start error.
type StartError struct {
	cause error
	Name  string
}

// Error returns the receiver's error value.
func (err *StartError) Error() string {
	return fmt.Sprintf("%s start: %v", err.Name, err.cause)
}

// Unwrap returns the receiver's unwrap value.
func (err *StartError) Unwrap() error {
	return err.cause
}

// Error returns the receiver's error value.
func (err *ExitError) Error() string {
	if err.Stderr == "" {
		return fmt.Sprintf("%s exited with status %d", err.Name, err.ExitCode)
	}
	return fmt.Sprintf("%s exited with status %d: %s", err.Name, err.ExitCode, err.Stderr)
}

// NonGracefulError describes non graceful error.
type NonGracefulError struct {
	PID int
}

// Error returns the receiver's error value.
func (err *NonGracefulError) Error() string {
	return fmt.Sprintf("%s: process tree rooted at %d did not quiesce", ErrNonGraceful, err.PID)
}

// Unwrap returns the receiver's unwrap value.
func (err *NonGracefulError) Unwrap() error {
	return ErrNonGraceful
}

// Runner represents runner.
type Runner struct {
	limits     chan struct{}
	live       map[int]*os.Process
	controlErr error
	runs       sync.WaitGroup
	mu         sync.Mutex
	closed     bool
}

// Stream represents stream.
type Stream string

// StreamStdin identifies a supported stream stdin value.
const (
	StreamStdin  Stream = "stdin"
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// NewRunner constructs and validates runner.
func NewRunner(concurrency int) (*Runner, error) {
	if concurrency <= 0 || concurrency > DefaultConcurrency {
		return nil, fmt.Errorf("subprocess concurrency must be from 1 through %d", DefaultConcurrency)
	}
	return &Runner{
		limits: make(chan struct{}, concurrency),
		live:   make(map[int]*os.Process),
	}, nil
}

// Run performs the run operation.
func (runner *Runner) Run(ctx context.Context, request Request) (
	result Result,
	returnErr error,
) {
	request, err := validatedRequest(request)
	if err != nil {
		return Result{}, err
	}
	if contextErr := context.Cause(ctx); contextErr != nil {
		return Result{}, contextErr
	}

	select {
	case runner.limits <- struct{}{}:
	case <-ctx.Done():
		return Result{}, context.Cause(ctx)
	}

	if admitErr := runner.admit(); admitErr != nil {
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

	executable, err := exec.LookPath(request.Name)
	if err != nil {
		return Result{}, &StartError{Name: operationName(request), cause: err}
	}
	arguments := make([]string, 1, len(request.Args)+1)
	arguments[0] = request.Name
	arguments = append(arguments, request.Args...)
	command := &exec.Cmd{Path: executable, Args: arguments}
	command.Dir = request.Dir
	command.Env = mergedEnvironment(request.Env)
	command.Stdin = bytes.NewReader(request.Stdin)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = DefaultReapBound
	lifetime, inheritedLifetime, err := newProcessLifetime()
	if err != nil {
		return Result{}, fmt.Errorf("create subprocess lifetime witness: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, lifetime.Close())
	}()
	command.ExtraFiles = []*os.File{inheritedLifetime}
	command.Env = append(command.Env, "RVW_PROCESS_LIFETIME_FD=3")
	limited := make(chan *LimitError, 1)
	stdout := newCapture(request.StdoutLimit, StreamStdout, limited)
	stderr := newCapture(request.StderrLimit, StreamStderr, limited)
	command.Stdout = stdout
	command.Stderr = stderr

	if err := command.Start(); err != nil {
		return Result{}, errors.Join(
			&StartError{Name: operationName(request), cause: err},
			inheritedLifetime.Close(),
		)
	}
	pid := command.Process.Pid
	if err := inheritedLifetime.Close(); err != nil {
		return Result{Started: true}, errors.Join(
			fmt.Errorf("close inherited subprocess lifetime witness: %w", err),
			terminateStartedCommand(command, pid),
		)
	}
	if err := runner.register(pid, command.Process); err != nil {
		return Result{Started: true}, errors.Join(err, terminateStartedCommand(command, pid))
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
		pid,
		waited,
		lifetime,
		limited,
	)
	result.Started = true
	return finishResult(request, result, waitErr, runErr, waited, stdout, stderr)
}

func finishResult(
	request Request,
	result Result,
	waitErr error,
	runErr error,
	waited *waitState,
	stdout *capture,
	stderr *capture,
) (Result, error) {
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

// StopAdmission performs the stop admission operation.
func (runner *Runner) StopAdmission() {
	runner.mu.Lock()
	runner.closed = true
	runner.mu.Unlock()
}

// Escalate performs the escalate operation.
func (runner *Runner) Escalate() {
	runner.mu.Lock()
	processes := make(map[int]*os.Process, len(runner.live))
	maps.Copy(processes, runner.live)
	runner.mu.Unlock()

	for pid := range processes {
		runner.recordControlError(signalProcessGroup(pid, syscall.SIGKILL))
	}
}

// Shutdown performs the shutdown operation.
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
		return runner.controlError()
	case <-grace.C:
		runner.Escalate()
	case <-ctx.Done():
		runner.Escalate()
		return errors.Join(
			fmt.Errorf("shutdown subprocesses: %w", context.Cause(ctx)),
			runner.controlError(),
		)
	}

	select {
	case <-done:
		return runner.controlError()
	case <-ctx.Done():
		runner.Escalate()
		return errors.Join(
			fmt.Errorf("shutdown subprocesses: %w", context.Cause(ctx)),
			runner.controlError(),
		)
	}
}

func (runner *Runner) await(
	ctx context.Context,
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
			killErr := signalProcessGroup(pid, syscall.SIGKILL)
			waitForQuiescence(pid, lifetime, DefaultReapBound)
			return result, waitErr, errors.Join(&NonGracefulError{PID: pid}, killErr)
		}
		if processTreeQuiescent(pid, lifetime) {
			return result, waitErr, nil
		}
		result.Terminated = true
		if err := signalProcessGroup(pid, syscall.SIGTERM); err != nil {
			return result, waitErr, fmt.Errorf("terminate process group %d: %w", pid, err)
		}
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
		termErr := signalProcessGroup(pid, syscall.SIGTERM)
		completed, waitErr := waitForProcessTree(
			pid,
			waited,
			lifetime,
			DefaultTermGrace,
		)
		if completed {
			result = resultFromWait(waitErr)
			result.Terminated = true
			return result, waitErr, errors.Join(limitErr, termErr)
		}
		return runner.escalate(
			pid,
			waited,
			lifetime,
			result,
			waitErr,
			processWaitPending,
			limitErr,
			termErr,
		)
	case <-ctx.Done():
		result := Result{ExitCode: -1, Terminated: true}
		termErr := signalProcessGroup(pid, syscall.SIGTERM)
		completed, waitErr := waitForProcessTree(
			pid,
			waited,
			lifetime,
			DefaultTermGrace,
		)
		if completed {
			result = resultFromWait(waitErr)
			result.Terminated = true
			return result, waitErr, errors.Join(context.Cause(ctx), termErr)
		}
		return runner.escalate(
			pid,
			waited,
			lifetime,
			result,
			waitErr,
			processWaitPending,
			context.Cause(ctx),
			termErr,
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
	if err := signalProcessGroup(pid, syscall.SIGKILL); err != nil {
		causes = append(causes, fmt.Errorf("kill process group %d: %w", pid, err))
	}
	if waitState != processAlreadyWaited {
		var completed bool
		completed, waitErr = waitForProcessTree(
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
	limited  chan<- *LimitError
	stream   Stream
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

type waitState struct {
	done    chan struct{}
	waitErr error
}

type processLifetime struct {
	reader  *os.File
	done    chan struct{}
	copyErr error
}

func newProcessLifetime() (*processLifetime, *os.File, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	lifetime := &processLifetime{reader: reader, done: make(chan struct{})}
	go func() {
		_, lifetime.copyErr = io.Copy(io.Discard, reader)
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

// Close returns the receiver's close value.
func (lifetime *processLifetime) Close() error {
	if lifetime == nil || lifetime.reader == nil {
		return nil
	}
	closeErr := lifetime.reader.Close()
	<-lifetime.done
	if errors.Is(closeErr, os.ErrClosed) {
		closeErr = nil
	}
	if errors.Is(lifetime.copyErr, os.ErrClosed) {
		lifetime.copyErr = nil
	}
	return errors.Join(closeErr, lifetime.copyErr)
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

// Write performs the write operation.
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

// TakeBytes takes ownership of bytes.
func (capture *capture) TakeBytes() []byte {
	value := capture.buffer.Bytes()
	capture.buffer = bytes.Buffer{}
	return value
}

// Exceeded reports the receiver's exceeded state.
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
) (bool, error) {
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
			return true, waitErr
		}
		select {
		case <-waitDone:
			waitErr = waited.err()
			waitedOK = true
			waitDone = nil
		case <-ticker.C:
		case <-deadline.C:
			return false, waitErr
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
		runner.recordControlError(signalProcessGroup(pid, signal))
	}
}

func (runner *Runner) recordControlError(err error) {
	if err == nil {
		return
	}
	runner.mu.Lock()
	runner.controlErr = errors.Join(runner.controlErr, err)
	runner.mu.Unlock()
}

func (runner *Runner) controlError() error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.controlErr
}

func terminateStartedCommand(command *exec.Cmd, pid int) error {
	signalErr := signalProcessGroup(pid, syscall.SIGKILL)
	waitErr := command.Wait()
	if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok && exitErr != nil {
		waitErr = nil
	}
	return errors.Join(signalErr, waitErr)
}
