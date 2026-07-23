package source

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/subprocess"
)

const (
	maxSourceProtocolToken            = 16 << 10
	rawDiffWireLimit            int64 = review.MaxSourcePaths + review.MaxFiles*256
	numstatWireLimit            int64 = review.MaxSourcePaths + review.MaxFiles*64
	stageInventoryWireLimit     int64 = review.MaxSourcePaths + review.MaxFiles*128
	flagInventoryWireLimit      int64 = review.MaxSourcePaths + review.MaxFiles*4
	untrackedInventoryWireLimit int64 = review.MaxSourcePaths + review.MaxFiles*2
)

type commandSpool struct {
	file *os.File
}

func (spool *commandSpool) Close() error {
	if spool == nil || spool.file == nil {
		return nil
	}
	err := spool.file.Close()
	spool.file = nil
	return err
}

func (spool *commandSpool) Rewind() error {
	if spool == nil || spool.file == nil {
		return errors.New("source command spool is closed")
	}
	_, err := spool.file.Seek(0, io.SeekStart)
	return err
}

func (spool *commandSpool) Reader() io.Reader { return spool.file }

func (spool *commandSpool) Size() (int64, error) {
	if spool == nil || spool.file == nil {
		return 0, errors.New("source command spool is closed")
	}
	info, err := spool.file.Stat()
	if err != nil {
		return 0, fmt.Errorf("inspect source command spool: %w", err)
	}
	return info.Size(), nil
}

func (spool *commandSpool) Input() (*subprocess.InputSpool, error) {
	if err := spool.Rewind(); err != nil {
		return nil, err
	}
	return subprocess.NewInputSpool(spool.file)
}

func (service *Service) runGHToSpool(
	ctx context.Context,
	arguments []string,
	wireLimit int64,
) (*commandSpool, error) {
	file, err := os.CreateTemp("", "rvw-github-stream-*")
	if err != nil {
		return nil, fmt.Errorf("create GitHub response spool: %w", err)
	}
	if removeErr := os.Remove(file.Name()); removeErr != nil {
		return nil, errors.Join(fmt.Errorf("unlink GitHub response spool: %w", removeErr), file.Close())
	}
	spool := &commandSpool{file: file}
	output, err := subprocess.NewOutputSpool(file)
	if err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	_, err = service.runner.Run(ctx, subprocess.Request{
		Name:             "gh",
		Args:             arguments,
		Dir:              service.cwd.String(),
		Env:              map[string]string{localeAllKey: "C", localeLanguageKey: "C", ghPromptDisabled: "1"},
		OutputSpool:      output,
		OutputSpoolLimit: wireLimit,
		StderrLimit:      MaxCommandStderrBytes,
		OperationDetail:  "gh " + strings.Join(arguments[:min(2, len(arguments))], " "),
	})
	if err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	if err := spool.Rewind(); err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	return spool, nil
}

func (service *Service) runGitToSpoolIn(
	ctx context.Context,
	directory gitDirectory,
	environment map[string]string,
	arguments []string,
	wireLimit int64,
) (*commandSpool, error) {
	file, err := os.CreateTemp("", "rvw-source-stream-*")
	if err != nil {
		return nil, fmt.Errorf("create source command spool: %w", err)
	}
	name := file.Name()
	if removeErr := os.Remove(name); removeErr != nil {
		return nil, errors.Join(fmt.Errorf("unlink source command spool: %w", removeErr), file.Close())
	}
	spool := &commandSpool{file: file}
	output, err := subprocess.NewOutputSpool(file)
	if err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	merged := baseGitEnvironment()
	maps.Copy(merged, environment)
	if directory.repository != nil {
		if refreshErr := directory.repository.refreshEnvironment(merged); refreshErr != nil {
			return nil, errors.Join(refreshErr, spool.Close())
		}
	}
	_, err = service.runner.Run(ctx, subprocess.Request{
		Name:             gitCommand,
		Args:             arguments,
		Dir:              directory.path,
		DirFile:          directory.file,
		BorrowedFiles:    directory.borrowed,
		Env:              merged,
		OutputSpool:      output,
		OutputSpoolLimit: wireLimit,
		StderrLimit:      MaxCommandStderrBytes,
		OperationDetail:  "git " + strings.Join(arguments[:min(2, len(arguments))], " "),
	})
	if err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	if err := spool.Rewind(); err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	return spool, nil
}

func (service *Service) runGitFromSpoolIn(
	ctx context.Context,
	directory gitDirectory,
	environment map[string]string,
	arguments []string,
	spool *commandSpool,
	stdinLimit int64,
	stdoutLimit int64,
) error {
	input, err := spool.Input()
	if err != nil {
		return err
	}
	merged := baseGitEnvironment()
	maps.Copy(merged, environment)
	if directory.repository != nil {
		if refreshErr := directory.repository.refreshEnvironment(merged); refreshErr != nil {
			return refreshErr
		}
	}
	_, err = service.runner.Run(ctx, subprocess.Request{
		Name:            gitCommand,
		Args:            arguments,
		Dir:             directory.path,
		DirFile:         directory.file,
		BorrowedFiles:   directory.borrowed,
		Env:             merged,
		InputSpool:      input,
		InputSpoolLimit: stdinLimit,
		StdoutLimit:     stdoutLimit,
		StderrLimit:     MaxCommandStderrBytes,
		OperationDetail: "git " + strings.Join(arguments[:min(2, len(arguments))], " "),
	})
	return err
}

type nulTokenReader struct {
	reader *bufio.Reader
	limit  int
}

func newNULTokenReaderContext(ctx context.Context, input io.Reader) *nulTokenReader {
	return &nulTokenReader{
		reader: bufio.NewReaderSize(newCancellationReader(ctx, input), maxSourceProtocolToken+1),
		limit:  maxSourceProtocolToken,
	}
}

type cancellationReader struct {
	ctx   context.Context
	input io.Reader
	read  int
}

func newCancellationReader(ctx context.Context, input io.Reader) *cancellationReader {
	return &cancellationReader{ctx: ctx, input: input}
}

func (reader *cancellationReader) Read(value []byte) (int, error) {
	if err := context.Cause(reader.ctx); err != nil {
		return 0, err
	}
	remaining := maxSourceProtocolToken - reader.read
	if len(value) > remaining {
		value = value[:remaining]
	}
	count, err := reader.input.Read(value)
	reader.read += count
	if reader.read >= maxSourceProtocolToken {
		reader.read = 0
		if cancellationErr := context.Cause(reader.ctx); cancellationErr != nil {
			return count, cancellationErr
		}
	}
	return count, err
}

func (reader *nulTokenReader) Next() ([]byte, error) {
	value, err := reader.reader.ReadSlice(0)
	switch {
	case err == nil:
		value = value[:len(value)-1]
		if len(value) > reader.limit {
			return nil, fmt.Errorf("source protocol token exceeds %d bytes", reader.limit)
		}
		return value, nil
	case errors.Is(err, io.EOF) && len(value) == 0:
		return nil, io.EOF
	case errors.Is(err, bufio.ErrBufferFull):
		return nil, fmt.Errorf("source protocol token exceeds %d bytes", reader.limit)
	case errors.Is(err, io.EOF):
		return nil, errors.New("source protocol token is not NUL terminated")
	default:
		return nil, err
	}
}
