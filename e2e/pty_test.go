//go:build e2e && (darwin || linux)

package e2e_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

const (
	maximumTranscriptBytes = 8 << 20
	diagnosticTailBytes    = 32 << 10
)

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(value)
}

type tailBuffer struct {
	contents []byte
	omitted  uint64
}

func (b *tailBuffer) Write(value []byte) (int, error) {
	size := len(value)
	if size >= maximumTranscriptBytes {
		b.omitted += uint64(len(b.contents) + size - maximumTranscriptBytes)
		b.contents = append(b.contents[:0], value[size-maximumTranscriptBytes:]...)
		return size, nil
	}
	overflow := len(b.contents) + size - maximumTranscriptBytes
	if overflow > 0 {
		b.omitted += uint64(overflow)
		copy(b.contents, b.contents[overflow:])
		b.contents = b.contents[:len(b.contents)-overflow]
	}
	b.contents = append(b.contents, value...)
	return size, nil
}

func (b *tailBuffer) bytes() []byte {
	return bytes.Clone(b.contents)
}

type ptySession struct {
	world      *world
	command    *exec.Cmd
	input      *lockedWriter
	inputRead  *os.File
	inputWrite *os.File
	outputRead *os.File
	terminal   *vt.Emulator

	mu         sync.Mutex
	transcript tailBuffer
	waitErr    error
	exited     bool
	outputDone bool
	closed     bool
	events     chan struct{}
	exit       chan struct{}
	drained    chan struct{}
}

func (w *world) launchPTY(t testing.TB, width, height int, args ...string) *ptySession {
	t.Helper()
	if w.pty != nil {
		t.Fatal("scenario already owns a PTY session")
	}
	commandLine := shellQuote(rvwBinary)
	for _, argument := range args {
		commandLine += " " + shellQuote(argument)
	}
	shell := fmt.Sprintf(
		"stty cols %d rows %d; stty -pendin 2>/dev/null || true; "+
			"before=$(stty -g); %s; status=$?; "+
			"stty -pendin 2>/dev/null || true; after=$(stty -g); "+
			"printf '\\n__RVW_E2E_STATUS__=%%s __RVW_E2E_BEFORE__=%%s __RVW_E2E_AFTER__=%%s\\n' "+
			"\"$status\" \"$before\" \"$after\"",
		width,
		height,
		commandLine,
	)
	command := newScriptCommand(w.context, shell)
	command.Dir = w.repository
	command.Env = append([]string{}, w.environment...)

	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		_ = inputRead.Close()
		_ = inputWrite.Close()
		t.Fatal(err)
	}
	command.Stdin = inputRead
	command.Stdout = outputWrite
	command.Stderr = outputWrite
	terminal := vt.NewEmulator(width, height)
	session := &ptySession{
		world:      w,
		command:    command,
		input:      &lockedWriter{writer: inputWrite},
		inputRead:  inputRead,
		inputWrite: inputWrite,
		outputRead: outputRead,
		terminal:   terminal,
		events:     make(chan struct{}, 1),
		exit:       make(chan struct{}),
		drained:    make(chan struct{}),
	}
	if err := command.Start(); err != nil {
		_ = inputRead.Close()
		_ = inputWrite.Close()
		_ = outputRead.Close()
		_ = outputWrite.Close()
		_ = terminal.Close()
		t.Fatalf("start PTY: %v", err)
	}
	_ = inputRead.Close()
	_ = outputWrite.Close()
	w.pty = session

	go session.relayTerminalReplies()
	go session.consumeOutput()
	go session.wait()
	return session
}

func (p *ptySession) relayTerminalReplies() {
	_, _ = io.Copy(p.input, p.terminal)
}

func (p *ptySession) consumeOutput() {
	defer close(p.drained)
	defer func() {
		p.mu.Lock()
		p.outputDone = true
		p.mu.Unlock()
		p.signalEvent()
	}()
	buffer := make([]byte, 16<<10)
	for {
		count, err := p.outputRead.Read(buffer)
		if count > 0 {
			p.mu.Lock()
			_, _ = p.transcript.Write(buffer[:count])
			_, _ = p.terminal.Write(buffer[:count])
			p.mu.Unlock()
			p.signalEvent()
		}
		if err != nil {
			return
		}
	}
}

func (p *ptySession) wait() {
	err := p.command.Wait()
	p.mu.Lock()
	p.waitErr = err
	p.exited = true
	p.mu.Unlock()
	close(p.exit)
	p.signalEvent()
}

func (p *ptySession) signalEvent() {
	select {
	case p.events <- struct{}{}:
	default:
	}
}

func (p *ptySession) send(t testing.TB, value string) {
	t.Helper()
	if _, err := io.WriteString(p.input, value); err != nil {
		t.Fatalf("write PTY input: %v\n%s", err, p.diagnostics(t))
	}
}

func (p *ptySession) awaitScreen(
	t testing.TB,
	description string,
	predicate func(string) bool,
) string {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		screen := p.screen()
		if predicate(screen) {
			return screen
		}
		p.mu.Lock()
		exited := p.exited
		outputDone := p.outputDone
		p.mu.Unlock()
		if exited && outputDone {
			t.Fatalf("process exited before %s\n%s", description, p.diagnostics(t))
		}
		select {
		case <-p.events:
		case <-timer.C:
			t.Fatalf("timed out awaiting %s\n%s", description, p.diagnostics(t))
		}
	}
}

func (p *ptySession) awaitExit(t testing.TB) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case <-p.exit:
	case <-timer.C:
		t.Fatalf("timed out awaiting PTY process exit\n%s", p.diagnostics(t))
	}
	select {
	case <-p.drained:
	case <-timer.C:
		t.Fatalf("timed out draining PTY output\n%s", p.diagnostics(t))
	}
	if p.waitError() != nil {
		t.Fatalf("script process failed: %v\n%s", p.waitError(), p.diagnostics(t))
	}
}

func (p *ptySession) screen() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return normalizeScreen(p.terminal.String())
}

func (p *ptySession) rawTranscript() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.transcript.bytes()
}

func (p *ptySession) waitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *ptySession) terminalState() (status int, before, after string, ok bool) {
	for _, field := range strings.Fields(string(p.rawTranscript())) {
		switch {
		case strings.HasPrefix(field, "__RVW_E2E_STATUS__="):
			value := strings.TrimPrefix(field, "__RVW_E2E_STATUS__=")
			status, _ = strconv.Atoi(value)
			ok = true
		case strings.HasPrefix(field, "__RVW_E2E_BEFORE__="):
			before = strings.TrimPrefix(field, "__RVW_E2E_BEFORE__=")
		case strings.HasPrefix(field, "__RVW_E2E_AFTER__="):
			after = strings.TrimPrefix(field, "__RVW_E2E_AFTER__=")
		}
	}
	return status, before, after, ok && before != "" && after != ""
}

func (p *ptySession) diagnostics(t testing.TB) string {
	t.Helper()
	p.mu.Lock()
	screen := normalizeScreen(p.terminal.String())
	transcript := p.transcript.bytes()
	omitted := p.transcript.omitted
	exited := p.exited
	outputDone := p.outputDone
	waitErr := p.waitErr
	p.mu.Unlock()
	if len(transcript) > diagnosticTailBytes {
		omitted += uint64(len(transcript) - diagnosticTailBytes)
		transcript = transcript[len(transcript)-diagnosticTailBytes:]
	}
	calls, callErr := readCallsForDiagnostics(p.world.callLog)
	return fmt.Sprintf(
		"screen:\n%s\n\ntranscript tail (%d omitted bytes):\n%q\n\nprocess: exited=%t output_done=%t error=%v\n\nexternal calls:\n%s",
		screen,
		omitted,
		transcript,
		exited,
		outputDone,
		waitErr,
		formatDiagnosticCalls(calls, callErr),
	)
}

func readCallsForDiagnostics(path string) ([]externalCall, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var calls []externalCall
	for line := range bytes.Lines(contents) {
		var call externalCall
		if err := jsonUnmarshal(line, &call); err != nil {
			return calls, err
		}
		calls = append(calls, call)
	}
	return calls, nil
}

// jsonUnmarshal is kept at this diagnostic boundary so the hot output loop
// never performs JSON work.
func jsonUnmarshal(value []byte, target any) error {
	return json.Unmarshal(value, target)
}

func formatDiagnosticCalls(calls []externalCall, err error) string {
	if err != nil {
		return err.Error()
	}
	if len(calls) == 0 {
		return "(none)"
	}
	var output strings.Builder
	for _, call := range calls {
		_, _ = fmt.Fprintf(&output, "%s %q stdin=%q\n", call.Command, call.Args, boundedString(call.Stdin, 1<<10))
	}
	return strings.TrimSuffix(output.String(), "\n")
}

func boundedString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + fmt.Sprintf("… <%d bytes omitted>", len(value)-limit)
}

func normalizeScreen(screen string) string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n")) + "\n"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (p *ptySession) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	_ = p.inputWrite.Close()
	_ = p.outputRead.Close()
	_ = p.terminal.Close()
	if p.command.Process != nil {
		_ = p.command.Process.Kill()
	}
}
