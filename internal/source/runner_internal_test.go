package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// blobRepo returns a runner on a new repository holding one blob of data, and the blob's ID.
func blobRepo(t *testing.T, data []byte) (runner, string) {
	t.Helper()
	dir := t.TempDir()
	git := func(stdin []byte, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	git(nil, "init", "-q")
	return newRunner(dir), git(data, "hash-object", "-w", "--stdin")
}

// logRunnerPIDs makes every git command started from now on append its process ID to the returned
// log, then exec git, which keeps the ID. It mirrors package source_test's logGitPIDs, which this
// package cannot see. Not for parallel tests: it changes PATH.
func logRunnerPIDs(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log")
	script := "#!/bin/sh\necho $$ >> '" + logPath + "'\nexec '" + gitPath + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// requireRunnerPIDsGone fails the test unless at least one PID was logged at path and each has
// ended and been reaped: an unreaped child is a zombie, which still exists.
func requireRunnerPIDsGone(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no git process was logged; the check would prove nothing: %v", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		t.Fatal("no git process was logged; the check would prove nothing")
	}
	for _, f := range fields {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("PID log %s: %v", path, err)
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("git process %d still exists (kill 0: %v)", pid, err)
		}
	}
}

// TestStreamOutputLimit: exactly limit bytes succeed; one byte more fails with errOutputLimit and
// leaves no git process. The blob is larger than a pipe holds, so git is still writing when the
// limit is hit. Not parallel: the PID log changes PATH.
func TestStreamOutputLimit(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 16<<10)
	run, id := blobRepo(t, data)
	pidLog := logRunnerPIDs(t)
	n := int64(len(data))

	out, err := run.output(t.Context(), n, nil, "cat-file", "blob", id)
	if err != nil || !bytes.Equal(out, data) {
		t.Errorf("output with limit %d = %d bytes, %v; want the %d-byte blob", n, len(out), err, n)
	}
	if _, err := run.output(t.Context(), n-1, nil, "cat-file", "blob", id); !errors.Is(err, errOutputLimit) {
		t.Errorf("output with limit %d = %v; want errOutputLimit", n-1, err)
	}
	requireRunnerPIDsGone(t, pidLog)
}

// TestStreamReaderFailsEarly: a reader that gives up before the output ends stops git and gets its
// own error back, promptly, and git has ended. Not parallel: the PID log changes PATH.
func TestStreamReaderFailsEarly(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1<<20)
	run, id := blobRepo(t, data)
	pidLog := logRunnerPIDs(t)
	giveUp := errors.New("reader gave up")
	done := make(chan error, 1)
	go func() {
		_, err := run.stream(t.Context(), int64(len(data)), nil, nil, func(r *bufio.Reader) error {
			if _, err := r.Peek(1); err != nil {
				return err
			}
			return giveUp
		}, "cat-file", "blob", id)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, giveUp) {
			t.Errorf("stream = %v; want the reader's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not return after its reader failed")
	}
	requireRunnerPIDsGone(t, pidLog)
}

// gate is a reader that blocks until open is closed, then ends.
type gate struct{ open <-chan struct{} }

func (g gate) Read([]byte) (int, error) {
	<-g.open
	return 0, io.EOF
}

// TestStreamReadsWhileStdinIsOpen: read gets output while stdin is still open. stdin sends one
// request and then blocks until read has seen its reply, so the call completes only if output is
// passed on as git writes it.
func TestStreamReadsWhileStdinIsOpen(t *testing.T) {
	t.Parallel()
	data := []byte("streamed\n")
	run, id := blobRepo(t, data)
	open := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(open) }) }
	t.Cleanup(release)
	var replied atomic.Bool
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := run.stream(ctx, 1<<20, nil, io.MultiReader(strings.NewReader(id+"\n"), gate{open}), func(r *bufio.Reader) error {
			header, err := r.ReadString('\n')
			if err != nil {
				return err
			}
			replied.Store(true)
			release()
			if want := id + " blob " + strconv.Itoa(len(data)) + "\n"; header != want {
				return errors.New("header " + strconv.Quote(header) + ", want " + strconv.Quote(want))
			}
			_, err = io.Copy(io.Discard, r)
			return err
		}, "cat-file", "--batch")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("stream: %v", err)
		}
	case <-time.After(10 * time.Second):
		if !replied.Load() {
			t.Error("read got no output while stdin was open")
		} else {
			t.Error("stream did not return after stdin closed")
		}
		release()
		cancel()
		<-done
	}
}
