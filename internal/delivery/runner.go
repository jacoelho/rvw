package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

type clipboardRunResult struct{ attempted bool }

type clipboardRunner struct {
	live   map[*os.Process]struct{}
	closed bool
	runs   sync.WaitGroup
	mu     sync.Mutex
}

func newClipboardRunner() *clipboardRunner {
	return &clipboardRunner{live: make(map[*os.Process]struct{})}
}

type attemptedReader struct {
	reader    *bytes.Reader
	attempted *bool
}

func (reader attemptedReader) Read(buffer []byte) (int, error) {
	*reader.attempted = true
	return reader.reader.Read(buffer)
}

type boundedWriter struct {
	buffer bytes.Buffer
	limit  int64
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	remaining := writer.limit - int64(writer.buffer.Len())
	if remaining <= 0 {
		return 0, ErrLimited
	}
	if int64(len(value)) > remaining {
		_, _ = writer.buffer.Write(value[:remaining])
		return int(remaining), ErrLimited
	}
	return writer.buffer.Write(value)
}

func (runner *clipboardRunner) run(
	ctx context.Context,
	name string,
	arguments []string,
	input []byte,
	stdoutLimit, stderrLimit int64,
) (clipboardRunResult, error) {
	if err := context.Cause(ctx); err != nil {
		return clipboardRunResult{}, err
	}
	runner.mu.Lock()
	if runner.closed {
		runner.mu.Unlock()
		return clipboardRunResult{}, errors.New("clipboard runner is closed")
	}
	runner.runs.Add(1)
	runner.mu.Unlock()
	defer runner.runs.Done()

	attempted := false
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, name, arguments...) //nolint:gosec // name is selected from the fixed clipboard allowlist.
	command.WaitDelay = 750 * time.Millisecond
	command.Stdin = attemptedReader{reader: bytes.NewReader(input), attempted: &attempted}
	stdout := &boundedWriter{limit: stdoutLimit}
	stderr := &boundedWriter{limit: stderrLimit}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		return clipboardRunResult{}, err
	}
	runner.mu.Lock()
	var killErr error
	if runner.closed {
		killErr = command.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
	}
	runner.live[command.Process] = struct{}{}
	runner.mu.Unlock()
	err := errors.Join(command.Wait(), killErr)
	runner.mu.Lock()
	delete(runner.live, command.Process)
	runner.mu.Unlock()
	result := clipboardRunResult{attempted: attempted}
	if err != nil {
		if context.Cause(ctx) != nil {
			return result, context.Cause(ctx)
		}
		if errors.Is(err, ErrLimited) {
			return result, ErrLimited
		}
		detail := stderr.buffer.String()
		if len(detail) > 16<<10 {
			detail = detail[:16<<10]
		}
		return result, fmt.Errorf("clipboard command: %w: %s", err, detail)
	}
	return result, nil
}

func (runner *clipboardRunner) escalate() {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for process := range runner.live {
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			continue
		}
	}
}

func (runner *clipboardRunner) close(ctx context.Context) error {
	runner.mu.Lock()
	runner.closed = true
	for process := range runner.live {
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			continue
		}
	}
	runner.mu.Unlock()
	done := make(chan struct{})
	go func() {
		runner.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

var _ io.Writer = (*boundedWriter)(nil)
