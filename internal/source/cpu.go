package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

const (
	sourceCPUPollBytes      = 16 << 10
	sourceStreamBufferBytes = 64 << 10
)

// CPUAdmission synchronously admits one CPU-only closure. Implementations must
// invoke work at most once, must not retain it, and must return only after work
// has completed or admission has failed.
type CPUAdmission func(context.Context, func() error) error

func directCPUAdmission(ctx context.Context, work func() error) error {
	if work == nil {
		return errors.New("CPU work is nil")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return work()
}

func indexSourceByteCancellable(ctx context.Context, value []byte, target byte) (int, error) {
	for start := 0; start < len(value); start += sourceCPUPollBytes {
		if err := context.Cause(ctx); err != nil {
			return -1, err
		}
		end := min(start+sourceCPUPollBytes, len(value))
		if index := bytes.IndexByte(value[start:end], target); index >= 0 {
			return start + index, nil
		}
	}
	return -1, context.Cause(ctx)
}

// consumeSourceStream keeps every external read outside CPU admission and
// advances one resumable, memory-only consumer inside a leaf admission. The
// input buffer is borrowed only for the duration of consume.
func consumeSourceStream(
	ctx context.Context,
	input io.Reader,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	consume func([]byte) error,
	finish func() error,
) error {
	if input == nil || runCPU == nil || ledger == nil || consume == nil || finish == nil {
		return errors.New("source stream consumer is incomplete")
	}
	if err := ledger.reserve(sourceStreamBufferBytes); err != nil {
		return err
	}
	buffer := make([]byte, sourceStreamBufferBytes)
	defer ledger.release(int64(cap(buffer)))
	for {
		count, readErr := input.Read(buffer)
		if count > 0 {
			if err := runCPU(ctx, func() error {
				return consume(buffer[:count])
			}); err != nil {
				return err
			}
		}
		switch {
		case errors.Is(readErr, io.EOF):
			return runCPU(ctx, finish)
		case readErr != nil:
			return fmt.Errorf("read source stream: %w", readErr)
		case count == 0:
			if err := context.Cause(ctx); err != nil {
				return err
			}
			return errors.New("source stream returned no data and no error")
		}
	}
}
