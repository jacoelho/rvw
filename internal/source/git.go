package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// minGit is the oldest supported Git: 2.50 added diff-pairs, which the per-file patches and counts
// need; GIT_NO_LAZY_FETCH, from 2.44, is older.
var minGit = [2]int{2, 50}

// clearedEnv lists inherited variables that would redirect Git to another repository, index or
// configuration, or change patch output.
var clearedEnv = []string{
	"GIT_DIFF_OPTS", "GIT_EXTERNAL_DIFF", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
	"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_LITERAL_PATHSPECS",
	"GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS",
}

// maxPathspecBytes bounds the pathspec arguments of one git command, well below the platform's
// argument limit; a longer list is split across commands.
const maxPathspecBytes = 64 << 10

// maxStderr bounds the stderr kept from a failed git command.
const maxStderr = 4 << 10

// runner is the only place that starts git. Every command runs from the worktree top level with
// literal pathspecs and with clearedEnv removed from the inherited environment.
type runner struct {
	dir string
	env []string
}

func newRunner(dir string) runner {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if clearedName(name) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
	return runner{dir: dir, env: env}
}

func clearedName(name string) bool {
	if strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
		return true
	}
	return slices.Contains(clearedEnv, name)
}

// command builds a git command. extraEnv entries are rvw's own settings, such as GIT_INDEX_FILE.
func (r runner) command(ctx context.Context, extraEnv []string, args ...string) *exec.Cmd {
	full := append([]string{"-C", r.dir, "--literal-pathspecs"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...) //nolint:gosec // fixed executable, structured arguments, no shell
	cmd.Env = append(append([]string(nil), r.env...), extraEnv...)
	cmd.WaitDelay = waitDelay
	return cmd
}

// gitError reports a git command that failed.
type gitError struct {
	Err    error
	Stderr string
	Args   []string
}

func (e *gitError) Error() string {
	cmd := strings.Join(e.Args, " ")
	// Pathspecs follow "--" and may total maxPathspecBytes, too long for a message.
	if k := slices.Index(e.Args, "--"); k >= 0 && k < len(e.Args)-1 {
		cmd = fmt.Sprintf("%s -- (%d paths)", strings.Join(e.Args[:k], " "), len(e.Args)-k-1)
	}
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		return fmt.Sprintf("git %s: %v", cmd, e.Err)
	}
	return fmt.Sprintf("git %s: %v: %s", cmd, e.Err, msg)
}

func (e *gitError) Unwrap() error { return e.Err }

// exitCode returns the git exit status, or -1 when the command did not exit normally.
func (e *gitError) exitCode() int {
	if exit, ok := errors.AsType[*exec.ExitError](e.Err); ok {
		return exit.ExitCode()
	}
	return -1
}

// failure is the error of a git command that failed. A failure caused by ctx ending is ctx's
// error, so callers never mistake a cancellation for a git verdict.
func failure(ctx context.Context, args []string, stderr string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("git %s: %w", args[0], ctxErr)
	}
	return &gitError{Args: args, Stderr: stderr, Err: err}
}

// errOutputLimit is wrapped by the error of a command whose stdout exceeded its limit.
var errOutputLimit = errors.New("output exceeds its limit")

// output runs git and returns stdout, reading at most limit bytes.
func (r runner) output(ctx context.Context, limit int64, extraEnv []string, args ...string) ([]byte, error) {
	out, _, err := r.collect(ctx, limit, extraEnv, nil, args...)
	return out, err
}

// outputStderr is output that also returns the first maxStderr bytes of stderr of a command that
// succeeded, for the warnings git prints without failing.
func (r runner) outputStderr(ctx context.Context, limit int64, extraEnv []string, args ...string) ([]byte, string, error) {
	return r.collect(ctx, limit, extraEnv, nil, args...)
}

// outputStdin is output with stdin as the command's input.
func (r runner) outputStdin(ctx context.Context, limit int64, extraEnv []string, stdin io.Reader, args ...string) ([]byte, error) {
	out, _, err := r.collect(ctx, limit, extraEnv, stdin, args...)
	return out, err
}

// collect is stream with a reader that keeps the whole output.
func (r runner) collect(ctx context.Context, limit int64, extraEnv []string, stdin io.Reader, args ...string) ([]byte, string, error) {
	var out bytes.Buffer
	stderr, err := r.stream(ctx, limit, extraEnv, stdin, func(br *bufio.Reader) error {
		_, err := br.WriteTo(&out)
		return err
	}, args...)
	if err != nil {
		return nil, "", err
	}
	return out.Bytes(), stderr, nil
}

// stream runs one git command and is the one place a one-shot command's output is read. read
// gets stdout, which fails with errOutputLimit past limit bytes, and must consume it to EOF;
// stderr is kept up to maxStderr and returned when the command succeeds. stdin, when not nil, is
// the command's input: exec feeds it from its own goroutine, so a command that writes before it
// has read everything cannot deadlock against the feed, and the feed ends with the process.
//
// When read fails or leaves output unread, the process is killed and reaped and read's error is
// returned, joined with git's own failure when git had already exited with one. A command that
// exits with a failure is failure's error, so (*gitError).exitCode still applies.
func (r runner) stream(ctx context.Context, limit int64, extraEnv []string, stdin io.Reader, read func(*bufio.Reader) error, args ...string) (string, error) {
	cmd := r.command(ctx, extraEnv, args...)
	cmd.Stdin = stdin
	stderr := &cappedBuffer{limit: maxStderr}
	cmd.Stderr = stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	if err := cmd.Start(); err != nil {
		return "", &gitError{Args: args, Err: err}
	}
	capped := &cappedReader{r: pipe, left: limit, err: fmt.Errorf("git %s: output exceeds %d bytes: %w", args[0], limit, errOutputLimit)}
	br := bufio.NewReaderSize(capped, 64<<10)
	readErr := read(br)
	if readErr == nil {
		if _, err := br.ReadByte(); err == nil {
			readErr = fmt.Errorf("git %s: output left unread", args[0])
		} else if !errors.Is(err, io.EOF) {
			readErr = err
		}
	}
	if readErr != nil {
		return "", stopEarly(ctx, cmd, args, stderr, readErr)
	}
	if err := cmd.Wait(); err != nil {
		return "", failure(ctx, args, stderr.String(), err)
	}
	return stderr.String(), nil
}

// stopEarly kills and reaps a command whose output reader gave up with readErr, and returns
// readErr with what else explains the stop: ctx ending, or git having failed on its own, whose
// stderr says why its output was cut short. The exit status the kill causes is expected.
func stopEarly(ctx context.Context, cmd *exec.Cmd, args []string, stderr *cappedBuffer, readErr error) error {
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return errors.Join(readErr, fmt.Errorf("stop git %s: %w", args[0], err))
	}
	waitErr := cmd.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(readErr, ctxErr) {
			return readErr
		}
		return errors.Join(readErr, fmt.Errorf("git %s: %w", args[0], ctxErr))
	}
	exit, exited := errors.AsType[*exec.ExitError](waitErr)
	switch {
	case waitErr == nil, exited && exit.ExitCode() < 0: // exited cleanly, or the kill ended it
		return readErr
	case exited:
		return errors.Join(readErr, &gitError{Args: args, Stderr: stderr.String(), Err: waitErr})
	default:
		return errors.Join(readErr, fmt.Errorf("stop git %s: %w", args[0], waitErr))
	}
}

// cappedReader reads r up to left bytes; one byte more fails with err.
type cappedReader struct {
	r    io.Reader
	err  error
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, c.err
	}
	n, err := c.r.Read(p[:min(int64(len(p)), c.left+1)])
	if int64(n) > c.left {
		n, c.left = int(c.left), -1
		return n, c.err
	}
	c.left -= int64(n)
	return n, err
}

// line runs git and returns its single line of output.
func (r runner) line(ctx context.Context, args ...string) (string, error) {
	out, err := r.output(ctx, 64<<10, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }

// checkVersion parses `git version` output and enforces minGit.
func checkVersion(out string) error {
	fields := strings.Fields(out)
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return fmt.Errorf("unrecognised git version output %q", out)
	}
	parts := strings.SplitN(fields[2], ".", 3)
	if len(parts) < 2 {
		return fmt.Errorf("unrecognised git version %q", fields[2])
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return fmt.Errorf("unrecognised git version %q", fields[2])
	}
	if major < minGit[0] || major == minGit[0] && minor < minGit[1] {
		return fmt.Errorf("git %s is older than the required %d.%d", fields[2], minGit[0], minGit[1])
	}
	return nil
}
