package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunNoninteractiveValidatesSeedsWithoutChangingOutput(t *testing.T) {
	root := testRepository(t)
	writeRunFile(t, filepath.Join(root, "file.txt"), "first\n")
	runTestGit(t, root, "add", "file.txt")
	runTestGit(t, root, "commit", "-m", "base")
	writeRunFile(t, filepath.Join(root, "file.txt"), "first\nsecond\n")

	runOutput := func(t *testing.T, stdin string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		status := run(runRequest{
			ctx:         t.Context(),
			args:        []string{"diff", "--stdout"},
			environment: []string{"HOME=" + filepath.Join(root, "home")},
			cwd:         root,
			stdin:       strings.NewReader(stdin),
			stdout:      &stdout,
			stderr:      &stderr,
		})
		return status, stdout.String(), stderr.String()
	}
	emptyStatus, emptyOutput, emptyError := runOutput(t, "")
	seedStatus, seedOutput, seedError := runOutput(t, `{"comments":[{"target":{"type":"line","path":"file.txt","side":"new","line":2},"binding":"active","comment":"seed"}]}`)
	if emptyStatus != 0 || seedStatus != 0 || emptyError != "" || seedError != "" {
		t.Fatalf("empty=(%d,%q) seeded=(%d,%q)", emptyStatus, emptyError, seedStatus, seedError)
	}
	if seedOutput != emptyOutput {
		t.Fatalf("seeded --stdout changed output:\nempty=%q\nseeded=%q", emptyOutput, seedOutput)
	}
}

func TestRunMalformedOrInapplicableSeedsFailBeforeOutput(t *testing.T) {
	root := testRepository(t)
	writeRunFile(t, filepath.Join(root, "file.txt"), "first\n")
	runTestGit(t, root, "add", "file.txt")
	runTestGit(t, root, "commit", "-m", "base")
	writeRunFile(t, filepath.Join(root, "file.txt"), "changed\n")

	for _, test := range []struct {
		name  string
		stdin string
		want  string
	}{
		{name: "malformed", stdin: " ", want: "invalid comment seed document"},
		{name: "missing path", stdin: `{"comments":[{"target":{"type":"file","path":"missing.txt"},"binding":"active","comment":"seed"}]}`, want: "not an active file"},
		{name: "missing line", stdin: `{"comments":[{"target":{"type":"line","path":"file.txt","side":"new","line":99},"binding":"active","comment":"seed"}]}`, want: "not present in the configured patch context"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(runRequest{
				ctx:         t.Context(),
				args:        []string{"diff", "--json"},
				environment: []string{"HOME=" + filepath.Join(root, "home")},
				cwd:         root,
				stdin:       strings.NewReader(test.stdin),
				stdout:      &stdout,
				stderr:      &stderr,
			})
			if status != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("status=%d stdout=%q stderr=%q, want %q", status, stdout.String(), stderr.String(), test.want)
			}
		})
	}
}

func TestRunSeededInteractiveOpensAndClosesControllingTerminalBeforeSource(t *testing.T) {
	terminal := &trackingReadCloser{Reader: strings.NewReader("")}
	var stdout, stderr bytes.Buffer
	status := run(runRequest{
		ctx:         t.Context(),
		args:        []string{"diff"},
		environment: []string{"HOME=" + filepath.Join(t.TempDir(), "home"), "TERM=xterm-256color"},
		cwd:         t.TempDir(),
		stdin:       strings.NewReader(`{"comments":[]}`),
		stdout:      &stdout,
		stderr:      &stderr,
		terminals:   testTerminalCapabilities(t, terminalOutput),
		openTerminal: func() (io.ReadCloser, error) {
			return terminal, nil
		},
	})
	if status != 1 || !terminal.closed || !strings.Contains(stderr.String(), "discover-repository") {
		t.Fatalf("status=%d closed=%t stdout=%q stderr=%q", status, terminal.closed, stdout.String(), stderr.String())
	}
}

func TestRunSeededInteractiveReportsControllingTerminalFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	openErr := errors.New("no controlling terminal")
	status := run(runRequest{
		ctx:         t.Context(),
		args:        []string{"diff"},
		environment: []string{"TERM=xterm-256color"},
		cwd:         t.TempDir(),
		stdin:       strings.NewReader(`{"comments":[]}`),
		stdout:      &stdout,
		stderr:      &stderr,
		terminals:   testTerminalCapabilities(t, terminalOutput),
		openTerminal: func() (io.ReadCloser, error) {
			return nil, openErr
		},
	})
	if status != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "open controlling terminal") || !strings.Contains(stderr.String(), openErr.Error()) {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestRunHelpDoesNotConsumeStdin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := run(runRequest{
		ctx:    t.Context(),
		args:   []string{"help"},
		cwd:    t.TempDir(),
		stdin:  errorReader{},
		stdout: &stdout,
		stderr: &stderr,
	})
	if status != 0 || stderr.Len() != 0 || !strings.HasPrefix(stdout.String(), "usage:") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (reader *trackingReadCloser) Close() error {
	reader.closed = true
	return nil
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("stdin was consumed") }
