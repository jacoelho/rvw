package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/config"
	"github.com/jacoelho/rvw/internal/output"
	"github.com/jacoelho/rvw/internal/source"
)

const (
	maxErrorBytes                 int64 = 16 << 10
	maxInteractiveStdoutSafeBytes int64 = 16 << 20
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "rvw: %s\n", terminalSafeError(err))
		os.Exit(1)
	}
	os.Exit(run(runRequest{
		ctx:            context.Background(),
		args:           os.Args[1:],
		environment:    os.Environ(),
		cwd:            cwd,
		stdin:          os.Stdin,
		stdout:         os.Stdout,
		stderr:         os.Stderr,
		stdoutTerminal: isTerminal(os.Stdout),
	}))
}

type runRequest struct {
	ctx            context.Context
	args           []string
	environment    []string
	cwd            string
	stdin          io.Reader
	stdout         io.Writer
	stderr         io.Writer
	stdoutTerminal bool
}

func run(request runRequest) int {
	invocation, err := parseArgs(request.args)
	if err != nil {
		writeStartupError(request.stderr, err)
		return 1
	}
	if invocation.Help {
		if _, err := io.WriteString(request.stdout, helpText(invocation.HelpTopic)); err != nil {
			writeStartupError(request.stderr, err)
			return 1
		}
		return 0
	}

	executionContext, cancelExecution := context.WithCancel(request.ctx)
	termination := newTerminationOwner(cancelExecution)
	defer termination.Close()
	err = execute(executeRequest{
		ctx:            executionContext,
		invocation:     invocation,
		environment:    request.environment,
		cwd:            request.cwd,
		stdin:          request.stdin,
		stdout:         request.stdout,
		stdoutTerminal: request.stdoutTerminal,
		termination:    termination,
	})
	if err != nil {
		writeStartupError(request.stderr, err)
		return 1
	}
	return 0
}

type executeRequest struct {
	ctx            context.Context
	invocation     Invocation
	environment    []string
	cwd            string
	stdin          io.Reader
	stdout         io.Writer
	stdoutTerminal bool
	termination    *terminationOwner
}

func execute(request executeRequest) (returnErr error) {
	if _, err := config.LoadGlobal(config.LoadRequest{
		CWD:         request.cwd,
		Environment: request.environment,
		Overrides:   request.invocation.Overrides,
	}); err != nil {
		return err
	}
	workingDirectory, err := source.NewWorkingDirectory(request.cwd)
	if err != nil {
		return err
	}
	service, err := source.NewService(workingDirectory)
	if err != nil {
		return err
	}
	if request.termination != nil {
		unregister := request.termination.RegisterEscalator(service.Escalate)
		defer unregister()
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(request.ctx), 10*time.Second)
		defer cancel()
		returnErr = errors.Join(returnErr, service.Close(shutdownContext))
	}()

	projectRoot := request.cwd
	if request.invocation.Target.Kind() != source.TargetPullRequest {
		root, err := service.RepositoryRoot(request.ctx)
		if err != nil {
			return err
		}
		projectRoot = root.String()
	}
	resolved, err := config.Load(config.LoadRequest{
		CWD:         request.cwd,
		ProjectRoot: projectRoot,
		Environment: request.environment,
		Overrides:   request.invocation.Overrides,
	})
	if err != nil {
		return err
	}
	options, err := sourceOptions(resolved)
	if err != nil {
		return err
	}
	options.FetchThreads = request.invocation.Output == OutputInteractive &&
		resolved.GitHub().FetchThreads

	document, err := service.Open(request.ctx, request.invocation.Target, options)
	if err != nil {
		return err
	}
	if request.invocation.Output == OutputInteractive {
		return runInteractive(interactiveRequest{
			ctx:            request.ctx,
			input:          request.stdin,
			output:         request.stdout,
			resolved:       resolved,
			service:        service,
			document:       document,
			stdoutTerminal: request.stdoutTerminal,
			termination:    request.termination,
		})
	}
	defer func() {
		returnErr = errors.Join(returnErr, document.Close())
	}()

	outputOptions := output.Options{Terminal: request.stdoutTerminal}
	switch request.invocation.Output {
	case OutputHuman:
		err = output.WriteHuman(request.stdout, document, outputOptions)
	case OutputJSON:
		err = output.WriteJSON(request.stdout, document, outputOptions)
	default:
		err = errors.New("unknown output mode")
	}
	return err
}

type terminationOwner struct {
	cancel context.CancelFunc

	signals       chan os.Signal
	commands      chan terminationCommand
	done          chan struct{}
	stopped       chan struct{}
	firstReceived chan struct{}
	next          uint64
}

type terminationCommand struct {
	id       uint64
	escalate func()
	remove   bool
	ready    chan struct{}
}

func newTerminationOwner(cancel context.CancelFunc) *terminationOwner {
	owner := &terminationOwner{
		cancel:        cancel,
		signals:       make(chan os.Signal, 2),
		commands:      make(chan terminationCommand),
		done:          make(chan struct{}),
		stopped:       make(chan struct{}),
		firstReceived: make(chan struct{}),
	}
	signal.Notify(owner.signals, os.Interrupt, syscall.SIGTERM)
	go owner.run()
	return owner
}

func (owner *terminationOwner) run() {
	defer close(owner.stopped)
	escalators := make(map[uint64]func())
	received := false
	for {
		select {
		case <-owner.done:
			return
		case command := <-owner.commands:
			if command.remove {
				delete(escalators, command.id)
			} else {
				escalators[command.id] = command.escalate
			}
			close(command.ready)
		case <-owner.signals:
			if !received {
				received = true
				close(owner.firstReceived)
				owner.cancel()
				continue
			}
			for _, escalate := range escalators {
				escalate()
			}
		}
	}
}

func (owner *terminationOwner) RegisterEscalator(escalate func()) func() {
	if owner == nil || escalate == nil {
		return func() {}
	}
	owner.next++
	id := owner.next
	ready := make(chan struct{})
	owner.commands <- terminationCommand{id: id, escalate: escalate, ready: ready}
	<-ready
	return func() {
		ready := make(chan struct{})
		owner.commands <- terminationCommand{id: id, remove: true, ready: ready}
		<-ready
	}
}

func (owner *terminationOwner) Received() bool {
	if owner == nil {
		return false
	}
	select {
	case <-owner.firstReceived:
		return true
	default:
		return false
	}
}

func (owner *terminationOwner) Close() {
	if owner == nil {
		return
	}
	signal.Stop(owner.signals)
	close(owner.done)
	<-owner.stopped
}

func sourceOptions(resolved config.Config) (source.OpenOptions, error) {
	diff := resolved.Diff()
	filter := resolved.Filter()
	ignoreFile, err := source.NewIgnoreFile(filter.IgnoreFile.String())
	if err != nil {
		return source.OpenOptions{}, err
	}
	options := source.OpenOptions{
		IncludeUntracked: diff.IncludeUntracked,
		BuiltinDefaults:  filter.BuiltinDefaults,
		Gitignore:        filter.Gitignore,
		RVWIgnore:        filter.RVWIgnore,
		IgnoreFile:       ignoreFile,
		Include:          make([]source.FilterPattern, len(filter.Include)),
		Exclude:          make([]source.FilterPattern, len(filter.Exclude)),
	}
	for index, pattern := range filter.Include {
		options.Include[index], err = source.NewFilterPattern(pattern.String())
		if err != nil {
			return source.OpenOptions{}, err
		}
	}
	for index, pattern := range filter.Exclude {
		options.Exclude[index], err = source.NewFilterPattern(pattern.String())
		if err != nil {
			return source.OpenOptions{}, err
		}
	}
	return options, nil
}

func writeStartupError(writer io.Writer, err error) {
	_, _ = fmt.Fprintf(writer, "rvw: %s\n", terminalSafeError(err))
}

func terminalSafeError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	var safe []byte
	safe = make([]byte, 0, min(len(value), int(maxErrorBytes)))
	for len(value) > 0 && int64(len(safe)) < maxErrorBytes {
		character, size := utf8.DecodeRuneInString(value)
		if character == utf8.RuneError && size == 1 {
			safe = appendBounded(safe, []byte(`\x`+hexByte(value[0])))
			value = value[1:]
			continue
		}
		var encoded string
		switch {
		case character == '\\':
			encoded = `\\`
		case unicode.IsControl(character), isBidiControl(character):
			encoded = fmt.Sprintf(`\u{%04X}`, character)
		default:
			encoded = string(character)
		}
		safe = appendBounded(safe, []byte(encoded))
		value = value[size:]
	}
	return string(safe)
}

func terminalSafePayload(value string) (string, error) {
	safe := make([]byte, 0, min(len(value), int(maxInteractiveStdoutSafeBytes)))
	for len(value) > 0 {
		character, size := utf8.DecodeRuneInString(value)
		var encoded string
		switch {
		case character == utf8.RuneError && size == 1:
			encoded = `\x` + hexByte(value[0])
		case character == '\n' || character == '\t':
			encoded = string(character)
		case unicode.IsControl(character), isBidiControl(character):
			encoded = fmt.Sprintf(`\u{%04X}`, character)
		default:
			encoded = string(character)
		}
		if int64(len(safe)+len(encoded)) > maxInteractiveStdoutSafeBytes {
			return "", errors.New("terminal-safe interactive output exceeds 16777216 bytes")
		}
		safe = append(safe, encoded...)
		value = value[size:]
	}
	return string(safe), nil
}

func appendBounded(destination, value []byte) []byte {
	remaining := maxErrorBytes - int64(len(destination))
	if int64(len(value)) > remaining {
		return destination
	}
	return append(destination, value...)
}

func hexByte(value byte) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[value>>4], digits[value&0x0f]})
}

func isBidiControl(character rune) bool {
	return character == '\u061c' || character == '\u200e' || character == '\u200f' ||
		character >= '\u202a' && character <= '\u202e' ||
		character >= '\u2066' && character <= '\u2069'
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
