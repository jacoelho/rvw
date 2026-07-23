package subprocess

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// InputSpool identifies a validated regular file replayed as subprocess stdin.
// The caller retains the original descriptor and must not use it until Run
// returns.
type InputSpool struct {
	file *os.File
}

// OutputSpool identifies a validated regular file receiving subprocess stdout.
// The caller retains the original descriptor and must not use it until Run
// returns.
type OutputSpool struct {
	file *os.File
}

// NewInputSpool validates a regular-file input endpoint.
func NewInputSpool(file *os.File) (*InputSpool, error) {
	spool := &InputSpool{file: file}
	if _, err := spool.stat(StreamStdin); err != nil {
		return nil, err
	}
	return spool, nil
}

// NewOutputSpool validates a regular-file output endpoint.
func NewOutputSpool(file *os.File) (*OutputSpool, error) {
	spool := &OutputSpool{file: file}
	if err := spool.validate(StreamStdout); err != nil {
		return nil, err
	}
	return spool, nil
}

func (spool *InputSpool) stat(stream Stream) (os.FileInfo, error) {
	if spool == nil {
		return nil, fmt.Errorf("%s spool is nil", stream)
	}
	return regularFileInfo(spool.file, stream)
}

func (spool *OutputSpool) validate(stream Stream) error {
	if spool == nil {
		return fmt.Errorf("%s spool is nil", stream)
	}
	_, err := regularFileInfo(spool.file, stream)
	return err
}

func (spool *InputSpool) duplicate(stream Stream) (*os.File, error) {
	if _, err := spool.stat(stream); err != nil {
		return nil, err
	}
	return duplicateFile(spool.file, stream)
}

func (spool *OutputSpool) duplicate(stream Stream) (*os.File, error) {
	if err := spool.validate(stream); err != nil {
		return nil, err
	}
	return duplicateFile(spool.file, stream)
}

func regularFileInfo(file *os.File, stream Stream) (os.FileInfo, error) {
	if file == nil {
		return nil, fmt.Errorf("%s spool file is nil", stream)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect %s spool: %w", stream, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s spool is not a regular file", stream)
	}
	return info, nil
}

func duplicateFile(file *os.File, stream Stream) (*os.File, error) {
	descriptor, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate %s spool: %w", stream, err)
	}
	return os.NewFile(uintptr(descriptor), file.Name()), nil
}

func closeFile(file *os.File) error {
	if file == nil {
		return nil
	}
	err := file.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

type spoolCapture struct {
	file         *os.File
	streamErrors chan<- error
	failure      error
	stream       Stream
	limit        int64
	written      int64
}

func newSpoolCapture(
	file *os.File,
	limit int64,
	stream Stream,
	streamErrors chan<- error,
) *spoolCapture {
	return &spoolCapture{
		file:         file,
		streamErrors: streamErrors,
		stream:       stream,
		limit:        limit,
	}
}

func (capture *spoolCapture) Write(value []byte) (int, error) {
	original := len(value)
	if capture.failure != nil {
		return original, capture.failure
	}
	remaining := capture.limit - capture.written
	retained := min(remaining, int64(len(value)))
	if retained > 0 {
		written, err := capture.file.Write(value[:int(retained)])
		capture.written += int64(written)
		if err == nil && written != int(retained) {
			err = errors.New("short output spool write")
		}
		if err != nil {
			capture.fail(fmt.Errorf("write %s spool: %w", capture.stream, err))
			return original, nil
		}
	}
	if int64(original) > remaining {
		capture.fail(&LimitError{Stream: capture.stream, Limit: capture.limit})
	}
	return original, nil
}

func (capture *spoolCapture) fail(err error) {
	if capture.failure != nil {
		return
	}
	capture.failure = err
	select {
	case capture.streamErrors <- err:
	default:
	}
}

func (capture *spoolCapture) TakeBytes() []byte { return nil }

func (capture *spoolCapture) StreamError() error { return capture.failure }
