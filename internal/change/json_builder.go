package change

import (
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

const cancellationBytes = 16 << 10

var errBoundedStringLimit = errors.New("bounded string limit exceeded")

// builderCancel returns a cancellation cause, when any.
type builderCancel func() error

// boundedBuilder incrementally owns one bounded string. It is the shared
// construction boundary for JSON wire values and review publication bodies.
type boundedBuilder struct {
	cancel     builderCancel
	output     []byte
	limit      int64
	peak       int64
	checkpoint int64
	finished   bool
}

// jsonBuilder incrementally owns one bounded JSON document.
type jsonBuilder struct {
	*boundedBuilder
}

func newBoundedBuilder(limit int64, cancel builderCancel) (*boundedBuilder, error) {
	if limit <= 0 || uint64(limit) > uint64(^uint(0)>>1) {
		return nil, errors.New("bounded string limit must be positive")
	}
	return &boundedBuilder{limit: limit, cancel: cancel}, nil
}

// newJSONBuilder constructs a checked JSON builder.
func newJSONBuilder(limit int64, cancel builderCancel) (*jsonBuilder, error) {
	bounded, err := newBoundedBuilder(limit, cancel)
	if err != nil {
		return nil, err
	}
	return &jsonBuilder{boundedBuilder: bounded}, nil
}

// Syntax writes already validated JSON syntax.
func (builder *jsonBuilder) Syntax(value string) error {
	return builder.WriteString(value)
}

// String writes one JSON string without joining its parts.
func (builder *jsonBuilder) String(parts ...string) error {
	if err := builder.WriteString(`"`); err != nil {
		return err
	}
	for _, part := range parts {
		if err := builder.writeStringValue(part, nil); err != nil {
			return err
		}
	}
	return builder.WriteString(`"`)
}

func (builder *jsonBuilder) writeStringValue(value string, forceEscape func(rune) bool) error {
	for value != "" {
		if value[0] < utf8.RuneSelf {
			run := safeASCIIStringRun(
				value,
				cancellationBytes-int(builder.checkpoint),
			)
			if run != 0 {
				if err := builder.WriteString(value[:run]); err != nil {
					return err
				}
				value = value[run:]
				continue
			}
			if err := builder.WriteString(escapedASCII(value[0])); err != nil {
				return err
			}
			value = value[1:]
			continue
		}
		character, size := utf8.DecodeRuneInString(value)
		switch {
		case character == utf8.RuneError && size == 1:
			if err := builder.WriteString(`\ufffd`); err != nil {
				return err
			}
		case character == '\u2028' || character == '\u2029' ||
			forceEscape != nil && forceEscape(character):
			if err := builder.WriteString(unicodeEscape(character)); err != nil {
				return err
			}
		default:
			if err := builder.WriteString(value[:size]); err != nil {
				return err
			}
		}
		value = value[size:]
	}
	return nil
}

// ByteString writes one raw-byte JSON string. Invalid UTF-8 bytes are emitted
// as byte-preserving Unicode escapes. forceEscape may request extra rune
// escaping for terminal-sensitive code points.
func (builder *jsonBuilder) ByteString(raw []byte, forceEscape func(rune) bool) error {
	if err := builder.WriteString(`"`); err != nil {
		return err
	}
	if err := builder.writeStringBytes(raw, forceEscape); err != nil {
		return err
	}
	return builder.WriteString(`"`)
}

// Uint64 writes a decimal unsigned integer.
func (builder *jsonBuilder) Uint64(value uint64) error {
	return builder.WriteString(strconv.FormatUint(value, 10))
}

// Checkpoint checks cancellation without writing output.
func (builder *boundedBuilder) Checkpoint() error {
	if builder == nil || builder.finished {
		return errors.New("bounded string builder is unavailable")
	}
	if builder.cancel != nil {
		return builder.cancel()
	}
	return nil
}

// Len returns constructed bytes.
func (builder *boundedBuilder) Len() int64 {
	if builder == nil {
		return 0
	}
	return int64(len(builder.output))
}

// ConstructionPeak returns the maximum simultaneous backing storage owned by
// the builder. It excludes the small builder value itself.
func (builder *boundedBuilder) ConstructionPeak() int64 {
	if builder == nil {
		return 0
	}
	return builder.peak
}

// ReserveExact establishes an exact requested capacity without changing the
// constructed length. It is used when a preceding bounded size pass already
// proved the final length.
func (builder *boundedBuilder) ReserveExact(capacity int64) error {
	if builder == nil || builder.finished {
		return errors.New("bounded string builder is unavailable")
	}
	if capacity < int64(len(builder.output)) || capacity > builder.limit {
		return fmt.Errorf("%w: %d bytes", errBoundedStringLimit, builder.limit)
	}
	if capacity <= int64(cap(builder.output)) {
		return nil
	}
	builder.peak = max(builder.peak, int64(cap(builder.output))+capacity)
	replacement := make([]byte, len(builder.output), int(capacity))
	copy(replacement, builder.output)
	builder.output = replacement
	return nil
}

// Finish consumes the builder, makes one exact immutable copy, and releases
// the mutable construction buffer.
func (builder *boundedBuilder) Finish() (string, error) {
	if err := builder.Checkpoint(); err != nil {
		return "", err
	}
	finished := string(builder.output)
	builder.peak = max(builder.peak, int64(cap(builder.output)+len(finished)))
	builder.output = nil
	builder.finished = true
	return finished, nil
}

// WriteString appends one string while enforcing the byte and cancellation
// bounds. Large values are split so cancellation latency is independent of
// caller chunking.
func (builder *boundedBuilder) WriteString(value string) error {
	if builder == nil || builder.finished {
		return errors.New("bounded string builder is unavailable")
	}
	if int64(len(value)) > builder.limit-int64(len(builder.output)) {
		return fmt.Errorf("%w: %d bytes", errBoundedStringLimit, builder.limit)
	}
	for value != "" {
		remaining := cancellationBytes - int(builder.checkpoint)
		piece := value[:min(len(value), remaining)]
		if err := builder.grow(len(piece)); err != nil {
			return err
		}
		builder.output = append(builder.output, piece...)
		builder.checkpoint += int64(len(piece))
		value = value[len(piece):]
		if builder.checkpoint >= cancellationBytes {
			builder.checkpoint = 0
			if err := builder.Checkpoint(); err != nil {
				return err
			}
		}
	}
	return nil
}

// grow reserves enough capacity for addition without allowing append to grow
// implicitly. During growth both the old and new backing arrays are charged.
func (builder *boundedBuilder) grow(addition int) error {
	if addition < 0 || int64(addition) > builder.limit-int64(len(builder.output)) {
		return fmt.Errorf("%w: %d bytes", errBoundedStringLimit, builder.limit)
	}
	required := len(builder.output) + addition
	if required <= cap(builder.output) {
		return nil
	}
	next := max(cap(builder.output)*2, 64, required)
	if int64(next) > builder.limit {
		next = int(builder.limit)
	}
	if next < required {
		return fmt.Errorf("%w: %d bytes", errBoundedStringLimit, builder.limit)
	}
	builder.peak = max(builder.peak, int64(cap(builder.output)+next))
	replacement := make([]byte, len(builder.output), next)
	copy(replacement, builder.output)
	builder.output = replacement
	return nil
}

func (builder *jsonBuilder) writeStringBytes(raw []byte, forceEscape func(rune) bool) error {
	for len(raw) > 0 {
		if raw[0] < utf8.RuneSelf {
			run := safeASCIIBytesRun(
				raw,
				cancellationBytes-int(builder.checkpoint),
			)
			if run != 0 {
				if err := builder.WriteBytes(raw[:run]); err != nil {
					return err
				}
				raw = raw[run:]
				continue
			}
			escaped := escapedASCII(raw[0])
			raw = raw[1:]
			if err := builder.WriteString(escaped); err != nil {
				return err
			}
			continue
		}
		size, err := builder.writeNonASCII(raw, forceEscape)
		if err != nil {
			return err
		}
		raw = raw[size:]
	}
	return nil
}

func (builder *jsonBuilder) writeNonASCII(raw []byte, forceEscape func(rune) bool) (int, error) {
	character, size := utf8.DecodeRune(raw)
	if character == utf8.RuneError && size == 1 {
		if forceEscape != nil {
			return 1, builder.WriteString(byteEscape(raw[0]))
		}
		return 1, builder.WriteString(`\ufffd`)
	}
	if character == '\u2028' || character == '\u2029' ||
		forceEscape != nil && forceEscape(character) {
		return size, builder.WriteString(unicodeEscape(character))
	}
	return size, builder.WriteBytes(raw[:size])
}

// WriteBytes appends raw bytes under the same construction and cancellation
// bounds as WriteString.
func (builder *boundedBuilder) WriteBytes(value []byte) error {
	if builder == nil || builder.finished {
		return errors.New("bounded string builder is unavailable")
	}
	if int64(len(value)) > builder.limit-int64(len(builder.output)) {
		return fmt.Errorf("%w: %d bytes", errBoundedStringLimit, builder.limit)
	}
	for len(value) != 0 {
		remaining := cancellationBytes - int(builder.checkpoint)
		piece := value[:min(len(value), remaining)]
		if err := builder.grow(len(piece)); err != nil {
			return err
		}
		builder.output = append(builder.output, piece...)
		builder.checkpoint += int64(len(piece))
		value = value[len(piece):]
		if builder.checkpoint == cancellationBytes {
			builder.checkpoint = 0
			if err := builder.Checkpoint(); err != nil {
				return err
			}
		}
	}
	return nil
}

func safeASCIIStringRun(value string, limit int) int {
	length := min(len(value), limit)
	for index := range length {
		if !safeJSONASCII(value[index]) {
			return index
		}
	}
	return length
}

func safeASCIIBytesRun(value []byte, limit int) int {
	length := min(len(value), limit)
	for index := range length {
		if !safeJSONASCII(value[index]) {
			return index
		}
	}
	return length
}

func safeJSONASCII(character byte) bool {
	return character >= 0x20 && character < utf8.RuneSelf &&
		character != '\\' && character != '"' &&
		character != '<' && character != '>' && character != '&'
}

func escapedASCII(character byte) string {
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
		return unicodeEscape(rune(character))
	default:
		if character < 0x20 {
			return unicodeEscape(rune(character))
		}
		return string(character)
	}
}

func byteEscape(value byte) string {
	const hexadecimal = "0123456789abcdef"
	return string([]byte{'\\', 'u', '0', '0', hexadecimal[value>>4], hexadecimal[value&0x0f]})
}

func unicodeEscape(value rune) string {
	const hexadecimal = "0123456789abcdef"
	if value <= 0xffff {
		return string([]byte{
			'\\', 'u',
			hexadecimal[value>>12&0x0f],
			hexadecimal[value>>8&0x0f],
			hexadecimal[value>>4&0x0f],
			hexadecimal[value&0x0f],
		})
	}
	value -= 0x10000
	return unicodeEscape(0xd800+value>>10) + unicodeEscape(0xdc00+value&0x3ff)
}
