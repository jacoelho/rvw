// Package subprocess runs bounded, cancellable external commands.
package subprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultStdoutLimit identifies a supported default stdout limit value.
const (
	DefaultStdoutLimit     int64 = 64 << 20
	DefaultStderrLimit     int64 = 1 << 20
	DefaultStdinLimit      int64 = 64 << 20
	DefaultTimeout               = 30 * time.Second
	DefaultTermGrace             = 750 * time.Millisecond
	DefaultReapBound             = 5 * time.Second
	MaxConcurrentProcesses       = 4
	MaxBorrowedFiles             = 8
	MaxSpoolStreamBytes    int64 = 128 << 20
	// MaxErrorDetailBytes bounds terminal-safe external error detail.
	MaxErrorDetailBytes int64 = 16 << 10
	// MaxErrorConstructionBytes covers captured stderr, retained detail, one
	// formatted error value, and fixed formatting metadata.
	MaxErrorConstructionBytes = DefaultStderrLimit + 2*MaxErrorDetailBytes + 256
)

var processPermits = make(chan struct{}, MaxConcurrentProcesses)

// ErrClosed identifies an closed failure.
var (
	ErrClosed      = errors.New("subprocess runner is closed")
	ErrLimited     = errors.New("subprocess output exceeded its limit")
	ErrNonGraceful = errors.New("subprocess group did not quiesce")
)

// Request describes request.
type Request struct {
	Env         map[string]string
	InputSpool  *InputSpool
	OutputSpool *OutputSpool
	// BorrowedFiles requires DirFile and is borrowed until Run returns without
	// transferring caller ownership. The target receives element i at FD 5+i;
	// FD 3 is the lifetime witness and launcher-only FD 4 is closed before exec.
	// Use BorrowedFilePath to construct target-visible paths.
	BorrowedFiles []*os.File
	Name          string
	Dir           string
	// DirFile is a directory descriptor borrowed until Run returns. It pins the
	// command's working directory even if its path is renamed or replaced.
	DirFile         *os.File
	OperationDetail string
	Args            []string
	// Stdin is borrowed until Run returns and must not be mutated concurrently.
	Stdin            []byte
	StdinText        string
	AcceptedExits    []int
	StdoutLimit      int64
	StderrLimit      int64
	InputSpoolLimit  int64
	OutputSpoolLimit int64
	Timeout          time.Duration
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
	Name      string
	Stderr    string
	ExitCode  int
	Truncated bool
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
	detail := err.Stderr
	if err.Truncated {
		detail += "…"
	}
	return fmt.Sprintf("%s exited with status %d: %s", err.Name, err.ExitCode, detail)
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
	live           map[int]*os.Process
	stopped        chan struct{}
	controlErr     error
	testAfterStart func()
	runs           sync.WaitGroup
	mu             sync.Mutex
	state          runnerControlState
}

type runnerControlState uint8

const (
	runnerOpen runnerControlState = iota
	runnerAdmissionClosed
	runnerTerminating
	runnerKillRequested
)

// Stream represents stream.
type Stream string

// StreamStdin identifies a supported stream stdin value.
const (
	StreamStdin  Stream = "stdin"
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// NewRunner constructs a runner.
func NewRunner() *Runner {
	return &Runner{
		live:    make(map[int]*os.Process),
		stopped: make(chan struct{}),
	}
}

// Run performs the run operation.
func (runner *Runner) Run(ctx context.Context, request Request) (
	result Result,
	returnErr error,
) {
	request, validationErr := validatedRequest(request)
	if validationErr != nil {
		return Result{}, validationErr
	}
	if contextErr := context.Cause(ctx); contextErr != nil {
		return Result{}, contextErr
	}

	if admitErr := runner.admit(); admitErr != nil {
		return Result{}, ErrClosed
	}
	permitHeld := false
	waitStarted := false
	runDone := make(chan struct{})
	defer func() {
		close(runDone)
		if !waitStarted {
			if permitHeld {
				releaseProcessPermit()
			}
			runner.runs.Done()
		}
	}()

	if permitErr := runner.acquireProcessPermit(ctx); permitErr != nil {
		return Result{}, permitErr
	}
	permitHeld = true
	if contextErr := context.Cause(ctx); contextErr != nil {
		return Result{}, contextErr
	}
	if openErr := runner.requireOpen(); openErr != nil {
		return Result{}, openErr
	}

	runContext, cancel := context.WithTimeoutCause(
		ctx,
		request.Timeout,
		fmt.Errorf("%s exceeded %s", operationName(request), request.Timeout),
	)
	defer cancel()

	prepared, prepareErr := prepareCommand(request)
	if prepareErr != nil {
		return Result{}, prepareErr
	}
	defer func() {
		returnErr = errors.Join(returnErr, prepared.close())
	}()
	command := prepared.command
	if startErr := command.Start(); startErr != nil {
		return Result{}, &StartError{Name: operationName(request), cause: startErr}
	}
	if runner.testAfterStart != nil {
		runner.testAfterStart()
	}
	adopted := runner.adoptStartedCommand(command, prepared, runDone)
	waitStarted = true

	result, waitErr, runErr := runner.await(
		runContext,
		adopted.pid,
		adopted.waited,
		prepared.lifetime,
		prepared.streamErrors,
		adopted.setupErr,
	)
	result.Started = true
	return finishResult(
		request,
		result,
		waitErr,
		runErr,
		adopted.waited,
		prepared.stdout,
		prepared.stderr,
	)
}

type adoptedCommand struct {
	waited   *waitState
	setupErr error
	pid      int
}

func (runner *Runner) adoptStartedCommand(
	command *exec.Cmd,
	prepared *preparedCommand,
	runDone <-chan struct{},
) adoptedCommand {
	pid := command.Process.Pid
	waited := newWaitState()
	control := runner.registerStarted(pid, command.Process)
	controlErr := applyRunnerControl(pid, control)
	runner.recordControlError(controlErr)
	go func() {
		waitErr := command.Wait()
		waited.complete(waitErr)
		runner.unregister(pid)
		<-runDone
		releaseProcessPermit()
		runner.runs.Done()
	}()
	return adoptedCommand{
		pid:      pid,
		waited:   waited,
		setupErr: errors.Join(controlErr, prepared.closeStartedInputs()),
	}
}

type preparedCommand struct {
	command           *exec.Cmd
	inputFile         *os.File
	outputFile        *os.File
	inheritedLifetime *os.File
	lifetime          *processLifetime
	stderr            *capture
	stdout            boundedCapture
	streamErrors      chan error
}

func prepareCommand(request Request) (*preparedCommand, error) {
	executable, err := exec.LookPath(request.Name)
	if err != nil {
		return nil, &StartError{Name: operationName(request), cause: err}
	}
	arguments := make([]string, 1, len(request.Args)+1)
	arguments[0] = request.Name
	arguments = append(arguments, request.Args...)
	directory := request.Dir
	if request.DirFile != nil {
		executable, arguments, err = descriptorCommand(
			executable,
			arguments,
		)
		if err != nil {
			return nil, &StartError{Name: operationName(request), cause: err}
		}
		directory = ""
	}
	prepared := &preparedCommand{streamErrors: make(chan error, 1)}
	prepared.command = &exec.Cmd{Path: executable, Args: arguments}
	prepared.command.Dir = directory
	prepared.command.Env = mergedEnvironment(request.Env)
	prepared.command.Stdin = bytes.NewReader(request.Stdin)
	if request.StdinText != "" {
		prepared.command.Stdin = strings.NewReader(request.StdinText)
	}
	if request.InputSpool != nil {
		prepared.inputFile, err = request.InputSpool.duplicate(StreamStdin)
		if err != nil {
			return nil, err
		}
		if _, err = prepared.inputFile.Seek(0, io.SeekStart); err != nil {
			return nil, errors.Join(
				fmt.Errorf("rewind subprocess input spool: %w", err),
				prepared.close(),
			)
		}
		prepared.command.Stdin = prepared.inputFile
	}
	prepared.command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	prepared.command.WaitDelay = DefaultReapBound
	prepared.lifetime, prepared.inheritedLifetime, err = newProcessLifetime()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("create subprocess lifetime witness: %w", err),
			prepared.close(),
		)
	}
	prepared.command.ExtraFiles = []*os.File{prepared.inheritedLifetime}
	if request.DirFile != nil {
		prepared.command.ExtraFiles = append(prepared.command.ExtraFiles, request.DirFile)
	}
	prepared.command.ExtraFiles = append(prepared.command.ExtraFiles, request.BorrowedFiles...)
	prepared.command.Env = append(prepared.command.Env, "RVW_PROCESS_LIFETIME_FD=3")
	prepared.stdout = newCapture(request.StdoutLimit, StreamStdout, prepared.streamErrors)
	if request.OutputSpool != nil {
		prepared.outputFile, err = request.OutputSpool.duplicate(StreamStdout)
		if err != nil {
			return nil, errors.Join(err, prepared.close())
		}
		if err = prepared.outputFile.Truncate(0); err != nil {
			return nil, errors.Join(
				fmt.Errorf("truncate subprocess output spool: %w", err),
				prepared.close(),
			)
		}
		if _, err = prepared.outputFile.Seek(0, io.SeekStart); err != nil {
			return nil, errors.Join(
				fmt.Errorf("rewind subprocess output spool: %w", err),
				prepared.close(),
			)
		}
		prepared.stdout = newSpoolCapture(
			prepared.outputFile,
			request.OutputSpoolLimit,
			StreamStdout,
			prepared.streamErrors,
		)
	}
	prepared.stderr = newCapture(request.StderrLimit, StreamStderr, prepared.streamErrors)
	prepared.command.Stdout = prepared.stdout
	prepared.command.Stderr = prepared.stderr
	return prepared, nil
}

func (prepared *preparedCommand) closeStartedInputs() error {
	inputErr := closeFile(prepared.inputFile)
	prepared.inputFile = nil
	lifetimeErr := closeFile(prepared.inheritedLifetime)
	prepared.inheritedLifetime = nil
	return errors.Join(inputErr, lifetimeErr)
}

func (prepared *preparedCommand) close() error {
	if prepared == nil {
		return nil
	}
	return errors.Join(
		closeFile(prepared.inputFile),
		closeFile(prepared.outputFile),
		closeFile(prepared.inheritedLifetime),
		prepared.lifetime.Close(),
	)
}

// StopAdmission performs the stop admission operation.
func (runner *Runner) StopAdmission() {
	runner.mu.Lock()
	runner.advanceControlLocked(runnerAdmissionClosed)
	runner.mu.Unlock()
}

// Escalate performs the escalate operation.
func (runner *Runner) Escalate() {
	_, pids := runner.advanceControl(runnerKillRequested)
	for _, pid := range pids {
		runner.recordControlError(signalProcessGroup(pid, syscall.SIGKILL))
	}
}

// Shutdown performs the shutdown operation.
func (runner *Runner) Shutdown(ctx context.Context) error {
	control, pids := runner.advanceControl(runnerTerminating)
	runner.signalSnapshot(pids, control.signal())

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
	streamErrors <-chan error,
	setupErr error,
) (Result, error, error) {
	if setupErr != nil {
		return runner.terminateAndAwait(pid, waited, lifetime, setupErr)
	}
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
	case streamErr := <-streamErrors:
		return runner.terminateAndAwait(pid, waited, lifetime, streamErr)
	case <-ctx.Done():
		return runner.terminateAndAwait(pid, waited, lifetime, context.Cause(ctx))
	}
}

func (runner *Runner) terminateAndAwait(
	pid int,
	waited *waitState,
	lifetime *processLifetime,
	cause error,
) (Result, error, error) {
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
		return result, waitErr, errors.Join(cause, termErr)
	}
	return runner.escalate(
		pid,
		waited,
		lifetime,
		result,
		waitErr,
		processWaitPending,
		cause,
		termErr,
	)
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
	if runner.state != runnerOpen {
		return ErrClosed
	}
	runner.runs.Add(1)
	return nil
}

func (runner *Runner) acquireProcessPermit(ctx context.Context) error {
	select {
	case processPermits <- struct{}{}:
		return nil
	case <-runner.stopped:
		return ErrClosed
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (runner *Runner) requireOpen() error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.state != runnerOpen {
		return ErrClosed
	}
	return nil
}

func releaseProcessPermit() {
	<-processPermits
}

func (runner *Runner) registerStarted(pid int, process *os.Process) runnerControlState {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.live[pid] = process
	return runner.state
}

func (runner *Runner) unregister(pid int) {
	runner.mu.Lock()
	delete(runner.live, pid)
	runner.mu.Unlock()
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

func (runner *Runner) advanceControl(next runnerControlState) (runnerControlState, []int) {
	runner.mu.Lock()
	runner.advanceControlLocked(next)
	pids := make([]int, 0, len(runner.live))
	for pid := range runner.live {
		pids = append(pids, pid)
	}
	state := runner.state
	runner.mu.Unlock()
	return state, pids
}

func (runner *Runner) advanceControlLocked(next runnerControlState) {
	if next <= runner.state {
		return
	}
	wasOpen := runner.state == runnerOpen
	runner.state = next
	if wasOpen {
		close(runner.stopped)
	}
}

func (state runnerControlState) signal() syscall.Signal {
	switch state {
	case runnerTerminating:
		return syscall.SIGTERM
	case runnerKillRequested:
		return syscall.SIGKILL
	default:
		return 0
	}
}

func applyRunnerControl(pid int, state runnerControlState) error {
	signal := state.signal()
	if signal == 0 {
		return nil
	}
	return signalProcessGroup(pid, signal)
}

func (runner *Runner) signalSnapshot(pids []int, signal syscall.Signal) {
	if signal == 0 {
		return
	}
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
