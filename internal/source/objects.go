package source

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// objectReader is one long-lived `git cat-file --batch-command`. Repo serialises every use through
// objMu. A cancelled or failed exchange leaves the pipe out of step, so the reader is then closed
// and the next use starts a fresh one.
type objectReader struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	cancel context.CancelFunc
}

type objectInfo struct {
	kind    string
	size    int64
	missing bool
}

func (r *Repo) startObjects(ctx context.Context) (*objectReader, error) {
	// The process outlives the request that starts it, so it detaches from ctx's cancellation;
	// Close cancels it.
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := r.run.command(ctx, nil, "cat-file", "--batch-command")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("cat-file: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("cat-file: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, &gitError{Args: []string{"cat-file", "--batch-command"}, Err: err}
	}
	return &objectReader{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 64<<10), cancel: cancel}, nil
}

func (o *objectReader) close() error {
	var errs []error
	if err := o.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		errs = append(errs, fmt.Errorf("cat-file: %w", err))
	}
	o.cancel()
	err := o.cmd.Wait()
	if _, exited := errors.AsType[*exec.ExitError](err); !exited && err != nil && !errors.Is(err, context.Canceled) {
		errs = append(errs, fmt.Errorf("cat-file: %w", err))
	}
	return errors.Join(errs...)
}

// withObjects runs fn with exclusive use of the object reader. If fn fails or ctx ends during the
// exchange, the reader is discarded.
func (r *Repo) withObjects(ctx context.Context, fn func(*objectReader) error) error {
	r.objMu.Lock()
	defer r.objMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.closed {
		return ErrClosed
	}
	if r.objects == nil {
		o, err := r.startObjects(ctx)
		if err != nil {
			return err
		}
		r.objects = o
	}
	o := r.objects
	// Cancelling kills cat-file, which unblocks any pending read or write in fn.
	stop := context.AfterFunc(ctx, o.cancel)
	err := fn(o)
	if !stop() || err != nil {
		closeErr := o.close()
		r.objects = nil
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(ctxErr, closeErr)
		}
		return errors.Join(err, closeErr)
	}
	return err
}

// request sends one command and parses the header line.
func (o *objectReader) request(cmd, id string) (objectInfo, error) {
	if strings.ContainsAny(id, " \n") || id == "" {
		return objectInfo{}, fmt.Errorf("invalid object id %q", id)
	}
	if _, err := fmt.Fprintf(o.stdin, "%s %s\n", cmd, id); err != nil {
		return objectInfo{}, fmt.Errorf("cat-file write: %w", err)
	}
	header, err := o.stdout.ReadString('\n')
	if err != nil {
		return objectInfo{}, fmt.Errorf("cat-file read: %w", err)
	}
	fields := strings.Fields(header)
	switch {
	case len(fields) == 2 && fields[1] == "missing":
		return objectInfo{missing: true}, nil
	case len(fields) == 3:
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return objectInfo{}, fmt.Errorf("cat-file: bad header %q", header)
		}
		return objectInfo{kind: fields[1], size: size}, nil
	default:
		return objectInfo{}, fmt.Errorf("cat-file: bad header %q", header)
	}
}

func (r *Repo) objectInfo(ctx context.Context, id string) (objectInfo, error) {
	var info objectInfo
	err := r.withObjects(ctx, func(o *objectReader) error {
		var err error
		info, err = o.request("info", id)
		return err
	})
	return info, err
}

// readBlob streams a blob's content to fn, which must consume exactly the reader it is given or
// return an error. It returns errMissing when the object is absent.
func (r *Repo) readBlob(ctx context.Context, id string, fn func(size int64, body io.Reader) error) error {
	return r.withObjects(ctx, func(o *objectReader) error {
		info, err := o.request("contents", id)
		if err != nil {
			return err
		}
		if info.missing {
			return ErrMissing
		}
		if info.kind != "blob" {
			return fmt.Errorf("object %s is a %s, not a blob", id, info.kind)
		}
		body := io.LimitReader(o.stdout, info.size)
		fnErr := fn(info.size, body)
		// Drain what fn left, then the trailing newline, to keep the pipe in step.
		if _, err := io.Copy(io.Discard, body); err != nil {
			return fmt.Errorf("cat-file read: %w", err)
		}
		if b, err := o.stdout.ReadByte(); err != nil || b != '\n' {
			return errors.New("cat-file: missing record terminator")
		}
		return fnErr
	})
}

// ErrMissing reports an object absent from the local object database.
var ErrMissing = errors.New("object missing from the local object database")

// ErrClosed is returned by reads through a Repo after Close.
var ErrClosed = errors.New("the repository has been closed")
