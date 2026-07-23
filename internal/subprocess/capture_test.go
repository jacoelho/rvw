package subprocess

import (
	"bytes"
	"errors"
	"testing"
)

func TestCaptureGrowthKeepsNonFinalCapacityAtHalfLimit(t *testing.T) {
	const limit int64 = 80
	streamErrors := make(chan error, 1)
	capture := newCapture(limit, StreamStdout, streamErrors)
	peak := int64(0)
	write := func(size int) {
		t.Helper()
		before := cap(capture.buffer)
		if written, err := capture.Write(bytes.Repeat([]byte{'x'}, size)); err != nil || written != size {
			t.Fatalf("write %d = %d, %v", size, written, err)
		}
		after := cap(capture.buffer)
		if after != before {
			peak = max(peak, int64(before+after))
		}
	}

	write(3 * int(limit) / 8)
	if got := cap(capture.buffer); got != 3*int(limit)/8 {
		t.Fatalf("three-eighth capacity = %d", got)
	}
	write(int(limit) / 8)
	if got := cap(capture.buffer); got != int(limit)/2 {
		t.Fatalf("half capacity = %d", got)
	}
	write(1)
	if got := cap(capture.buffer); got != int(limit) {
		t.Fatalf("final capacity = %d", got)
	}
	if peak != MaxCaptureConstructionBytes(limit) {
		t.Fatalf("construction peak = %d, want %d", peak, MaxCaptureConstructionBytes(limit))
	}
}

func TestCaptureIrregularWritesRetainBoundedTransferredCapacity(t *testing.T) {
	const limit int64 = 31
	streamErrors := make(chan error, 1)
	capture := newCapture(limit, StreamStdout, streamErrors)
	want := make([]byte, 0, limit)
	for _, value := range []string{"abc", "defghij", "kl", "mnopqrst", "uvwxy"} {
		before := cap(capture.buffer)
		if _, err := capture.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
		want = append(want, value...)
		after := cap(capture.buffer)
		if after > int(limit) {
			t.Fatalf("capacity = %d, limit %d", after, limit)
		}
		if len(capture.buffer) <= int(limit)/2 && after > int(limit)/2 {
			t.Fatalf("non-final capacity grew from %d to %d", before, after)
		}
	}
	transferred := capture.TakeBytes()
	if !bytes.Equal(transferred, want) {
		t.Fatalf("transferred bytes = %q, want %q", transferred, want)
	}
	if cap(transferred) > int(limit) {
		t.Fatalf("transferred capacity = %d, limit %d", cap(transferred), limit)
	}
	if capture.buffer != nil || capture.TakeBytes() != nil {
		t.Fatal("taken capture retained backing storage")
	}
	if err := capture.StreamError(); err != nil {
		t.Fatalf("bounded capture error = %v", err)
	}
}

func TestCaptureExactAndOverLimitPreserveWriterSemantics(t *testing.T) {
	const limit int64 = 8
	streamErrors := make(chan error, 1)
	capture := newCapture(limit, StreamStdout, streamErrors)
	if written, err := capture.Write([]byte("12345678")); err != nil || written != int(limit) {
		t.Fatalf("exact write = %d, %v", written, err)
	}
	if cap(capture.buffer) != int(limit) || capture.StreamError() != nil {
		t.Fatalf("exact capture = len %d cap %d error %v", len(capture.buffer), cap(capture.buffer), capture.StreamError())
	}
	if written, err := capture.Write([]byte("overflow")); err != nil || written != len("overflow") {
		t.Fatalf("over-limit write = %d, %v", written, err)
	}
	if len(capture.buffer) != int(limit) || cap(capture.buffer) != int(limit) {
		t.Fatalf("over-limit capture = len %d cap %d", len(capture.buffer), cap(capture.buffer))
	}
	if err := capture.StreamError(); err == nil {
		t.Fatal("over-limit capture lacks stream error")
	}
	select {
	case err := <-streamErrors:
		limitErr, ok := errors.AsType[*LimitError](err)
		if !ok || limitErr.Stream != StreamStdout || limitErr.Limit != limit {
			t.Fatalf("signaled error = %v", err)
		}
	default:
		t.Fatal("over-limit capture did not signal the runner")
	}
}

func TestMaxCaptureConstructionBytesCoversOddAndEvenLimits(t *testing.T) {
	for limit, want := range map[int64]int64{1: 1, 5: 7, 8: 12, DefaultStdoutLimit: 96 << 20} {
		if got := MaxCaptureConstructionBytes(limit); got != want {
			t.Fatalf("limit %d construction = %d, want %d", limit, got, want)
		}
	}
}
