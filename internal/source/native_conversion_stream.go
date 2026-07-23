package source

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
)

const nativeIdentPrefix = "$Id:"

type nativeIdentWriter struct {
	file           *os.File
	buffer         *bufio.Writer
	candidateStart int64
	written        int64
	prefixLength   int
	prefix         [len(nativeIdentPrefix)]byte
	candidate      bool
	enabled        bool
}

func newNativeIdentWriter(file *os.File, enabled bool) *nativeIdentWriter {
	return &nativeIdentWriter{
		buffer:  bufio.NewWriterSize(file, 64<<10),
		file:    file,
		enabled: enabled,
	}
}

func (writer *nativeIdentWriter) writeByte(value byte) error {
	if !writer.enabled {
		return writer.appendByte(value)
	}
	if writer.candidate {
		if value == '$' {
			if err := writer.buffer.Flush(); err != nil {
				return err
			}
			if err := writer.file.Truncate(writer.candidateStart); err != nil {
				return err
			}
			if _, err := writer.file.Seek(writer.candidateStart, io.SeekStart); err != nil {
				return err
			}
			writer.buffer.Reset(writer.file)
			writer.written = writer.candidateStart
			if err := writer.appendBytes([]byte("$Id$")); err != nil {
				return err
			}
			writer.candidate = false
			return nil
		}
		if err := writer.appendByte(value); err != nil {
			return err
		}
		if value == '\n' {
			writer.candidate = false
		}
		return nil
	}
	if writer.prefixLength != 0 {
		if value == nativeIdentPrefix[writer.prefixLength] {
			writer.prefix[writer.prefixLength] = value
			writer.prefixLength++
			if writer.prefixLength == len(nativeIdentPrefix) {
				writer.candidateStart = writer.written
				if err := writer.appendBytes(writer.prefix[:]); err != nil {
					return err
				}
				writer.prefixLength = 0
				writer.candidate = true
			}
			return nil
		}
		if err := writer.appendBytes(writer.prefix[:writer.prefixLength]); err != nil {
			return err
		}
		writer.prefixLength = 0
		return writer.writeByte(value)
	}
	if value == nativeIdentPrefix[0] {
		writer.prefix[0] = value
		writer.prefixLength = 1
		return nil
	}
	return writer.appendByte(value)
}

func (writer *nativeIdentWriter) appendByte(value byte) error {
	if err := writer.buffer.WriteByte(value); err != nil {
		return err
	}
	writer.written++
	return nil
}

func (writer *nativeIdentWriter) appendBytes(value []byte) error {
	written, err := writer.buffer.Write(value)
	writer.written += int64(written)
	if err != nil {
		return err
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

func (writer *nativeIdentWriter) finish() (int64, error) {
	if writer.prefixLength != 0 {
		if err := writer.appendBytes(writer.prefix[:writer.prefixLength]); err != nil {
			return 0, err
		}
		writer.prefixLength = 0
	}
	if err := writer.buffer.Flush(); err != nil {
		return 0, err
	}
	return writer.written, nil
}

//nolint:gocognit // CRLF and ident carry state across the same cancellable streaming pass.
func streamNativeConversion(
	ctx context.Context,
	inputPath string,
	output *os.File,
	conversion nativeConversion,
) (int64, error) {
	input, err := os.Open(inputPath) //nolint:gosec // inputPath is an owner-created private spool.
	if err != nil {
		return 0, err
	}
	defer input.Close() //nolint:errcheck // Read-only spool close cannot alter the result.
	probe := make([]byte, 8_000)
	probeBytes, probeErr := io.ReadFull(input, probe)
	if probeErr != nil && !errors.Is(probeErr, io.EOF) && !errors.Is(probeErr, io.ErrUnexpectedEOF) {
		return 0, probeErr
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	normalizeEOL := conversion.normalizeEOL &&
		(!conversion.autoText || !nativeBinary(probe[:probeBytes]))
	writer := newNativeIdentWriter(output, conversion.ident)
	buffer := make([]byte, 64<<10)
	pendingCR := false
	var processed uint64
	for {
		count, readErr := input.Read(buffer)
		for _, value := range buffer[:count] {
			processed++
			if processed%(16<<10) == 0 {
				if err := context.Cause(ctx); err != nil {
					return 0, err
				}
			}
			if !normalizeEOL {
				if err := writer.writeByte(value); err != nil {
					return 0, err
				}
				continue
			}
			if pendingCR {
				pendingCR = false
				if value == '\n' {
					if err := writer.writeByte('\n'); err != nil {
						return 0, err
					}
					continue
				}
				if err := writer.writeByte('\r'); err != nil {
					return 0, err
				}
			}
			if value == '\r' {
				pendingCR = true
				continue
			}
			if err := writer.writeByte(value); err != nil {
				return 0, err
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return 0, readErr
			}
			break
		}
		if count == 0 {
			return 0, io.ErrNoProgress
		}
	}
	if pendingCR {
		if err := writer.writeByte('\r'); err != nil {
			return 0, err
		}
	}
	if err := context.Cause(ctx); err != nil {
		return 0, err
	}
	return writer.finish()
}
