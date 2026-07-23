package source

import (
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

const sourceBuilderCancellationBytes = 16 << 10

type sourceJSONBuilder struct {
	cancelled  func() error
	output     []byte
	limit      int64
	peak       int64
	checkpoint int64
	finished   bool
}

func newSourceJSONBuilder(limit int64) (*sourceJSONBuilder, error) {
	return newCancellableSourceJSONBuilder(limit, nil)
}

func newCancellableSourceJSONBuilder(
	limit int64,
	cancelled func() error,
) (*sourceJSONBuilder, error) {
	if limit <= 0 || uint64(limit) > uint64(^uint(0)>>1) {
		return nil, errors.New("JSON wire limit must be positive and representable")
	}
	return &sourceJSONBuilder{limit: limit, cancelled: cancelled}, nil
}

func (builder *sourceJSONBuilder) Syntax(value string) error {
	return builder.write(value)
}

// String writes one JSON string directly into the bounded buffer. It matches
// encoding/json's string escaping without allocating a second encoded scalar.
func (builder *sourceJSONBuilder) String(value string) error {
	if err := builder.write(`"`); err != nil {
		return err
	}
	remaining := value
	for remaining != "" {
		if remaining[0] < utf8.RuneSelf {
			run := sourceSafeASCIIStringRun(
				remaining,
				sourceBuilderCancellationBytes-int(builder.checkpoint),
			)
			if run != 0 {
				if err := builder.write(remaining[:run]); err != nil {
					return err
				}
				remaining = remaining[run:]
				continue
			}
			if err := builder.write(sourceEscapedASCII(remaining[0])); err != nil {
				return err
			}
			remaining = remaining[1:]
			continue
		}
		character, size := utf8.DecodeRuneInString(remaining)
		switch {
		case character == utf8.RuneError && size == 1:
			if err := builder.write(`\ufffd`); err != nil {
				return err
			}
		case character == '\u2028' || character == '\u2029':
			if err := builder.write(sourceUnicodeEscape(character)); err != nil {
				return err
			}
		default:
			if err := builder.write(remaining[:size]); err != nil {
				return err
			}
		}
		remaining = remaining[size:]
	}
	return builder.write(`"`)
}

func (builder *sourceJSONBuilder) Uint64(value uint64) error {
	return builder.write(strconv.FormatUint(value, 10))
}

func (builder *sourceJSONBuilder) Finish() (string, error) {
	if builder == nil || builder.finished {
		return "", errors.New("JSON wire builder is unavailable")
	}
	if err := builder.checkCancellation(); err != nil {
		return "", err
	}
	finished := string(builder.output)
	builder.peak = max(builder.peak, int64(cap(builder.output)+len(finished)))
	builder.output = nil
	builder.finished = true
	return finished, nil
}

func (builder *sourceJSONBuilder) constructionPeak() int64 {
	if builder == nil {
		return 0
	}
	return builder.peak
}

func (builder *sourceJSONBuilder) write(value string) error {
	if err := builder.ensureAvailable(len(value)); err != nil {
		return err
	}
	for value != "" {
		remaining := sourceBuilderCancellationBytes - int(builder.checkpoint)
		piece := value[:min(len(value), remaining)]
		if err := builder.grow(len(piece)); err != nil {
			return err
		}
		builder.output = append(builder.output, piece...)
		builder.checkpoint += int64(len(piece))
		value = value[len(piece):]
		if builder.checkpoint >= sourceBuilderCancellationBytes {
			builder.checkpoint = 0
			if err := builder.checkCancellation(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (builder *sourceJSONBuilder) ensureAvailable(bytes int) error {
	if builder == nil || builder.finished {
		return errors.New("JSON wire builder is unavailable")
	}
	if bytes < 0 || int64(bytes) > builder.limit-int64(len(builder.output)) {
		return fmt.Errorf("JSON wire value exceeds %d bytes", builder.limit)
	}
	return nil
}

func (builder *sourceJSONBuilder) checkCancellation() error {
	if builder.cancelled != nil {
		return builder.cancelled()
	}
	return nil
}

func (builder *sourceJSONBuilder) grow(addition int) error {
	required := len(builder.output) + addition
	if required <= cap(builder.output) {
		return nil
	}
	next := max(cap(builder.output)*2, 64, required)
	if int64(next) > builder.limit {
		next = int(builder.limit)
	}
	if next < required {
		return fmt.Errorf("JSON wire value exceeds %d bytes", builder.limit)
	}
	builder.peak = max(builder.peak, int64(cap(builder.output)+next))
	replacement := make([]byte, len(builder.output), next)
	copy(replacement, builder.output)
	builder.output = replacement
	return nil
}

func sourceEscapedASCII(character byte) string {
	switch character {
	case '\\':
		return `\\`
	case '"':
		return `\"`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	case '<', '>', '&':
		return sourceUnicodeEscape(rune(character))
	default:
		if character < 0x20 {
			return sourceUnicodeEscape(rune(character))
		}
		return string(character)
	}
}

func sourceSafeASCIIStringRun(value string, limit int) int {
	length := min(len(value), limit)
	for index := range length {
		character := value[index]
		if character < 0x20 || character >= utf8.RuneSelf ||
			character == '\\' || character == '"' ||
			character == '<' || character == '>' || character == '&' {
			return index
		}
	}
	return length
}

func sourceUnicodeEscape(value rune) string {
	const hexadecimal = "0123456789abcdef"
	return string([]byte{
		'\\', 'u',
		hexadecimal[value>>12&0x0f],
		hexadecimal[value>>8&0x0f],
		hexadecimal[value>>4&0x0f],
		hexadecimal[value&0x0f],
	})
}
