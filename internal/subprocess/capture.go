package subprocess

import "io"

// MaxCaptureConstructionBytes returns the maximum simultaneously live backing
// storage while growing a capture with an already validated limit.
func MaxCaptureConstructionBytes(limit int64) int64 {
	return limit + limit/2
}

type capture struct {
	streamErrors chan<- error
	stream       Stream
	buffer       []byte
	limit        int64
	exceeded     bool
}

type boundedCapture interface {
	io.Writer
	TakeBytes() []byte
	StreamError() error
}

func newCapture(limit int64, stream Stream, streamErrors chan<- error) *capture {
	if limit <= 0 || limit > DefaultStdoutLimit {
		panic("subprocess capture limit is invalid")
	}
	return &capture{limit: limit, stream: stream, streamErrors: streamErrors}
}

// Write retains at most the configured limit while preserving io.Writer
// consumption semantics. Non-final capacities never exceed half the limit, so
// the final exact-limit allocation has a proven old-plus-new peak.
func (capture *capture) Write(value []byte) (int, error) {
	original := len(value)
	remaining := capture.limit - int64(len(capture.buffer))
	if remaining > 0 {
		retained := min(remaining, int64(len(value)))
		capture.grow(int(retained))
		capture.buffer = append(capture.buffer, value[:int(retained)]...)
	}
	if int64(original) > remaining && !capture.exceeded {
		capture.exceeded = true
		select {
		case capture.streamErrors <- &LimitError{Stream: capture.stream, Limit: capture.limit}:
		default:
		}
	}
	return original, nil
}

func (capture *capture) grow(additional int) {
	required := len(capture.buffer) + additional
	if required <= cap(capture.buffer) {
		return
	}
	limit := int(capture.limit)
	half := limit / 2
	nextCapacity := limit
	if required <= half {
		nextCapacity = min(half, max(required, max(1, cap(capture.buffer)*2)))
	}
	next := make([]byte, len(capture.buffer), nextCapacity)
	copy(next, capture.buffer)
	capture.buffer = next
}

// TakeBytes transfers the bounded backing storage.
func (capture *capture) TakeBytes() []byte {
	value := capture.buffer
	capture.buffer = nil
	return value
}

// StreamError returns the receiver's stream failure, when any.
func (capture *capture) StreamError() error {
	if !capture.exceeded {
		return nil
	}
	return &LimitError{Stream: capture.stream, Limit: capture.limit}
}
