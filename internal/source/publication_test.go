package source

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestDecodePublicationReviewIDUsesCanonicalFramer(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		want    uint64
	}{
		{name: "valid", encoded: `{"id":7,"unknown":{"nested":true}}`, want: 7},
		{name: "missing", encoded: `{}`},
		{name: "zero", encoded: `{"id":0}`},
		{name: "string", encoded: `{"id":"7"}`},
		{name: "duplicate", encoded: `{"id":7,"id":8}`},
		{name: "container", encoded: `{"id":{}}`},
		{name: "root array", encoded: `[{"id":7}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodePublicationReviewID(t.Context(), []byte(test.encoded))
			if test.want == 0 {
				if err == nil {
					t.Fatalf("invalid response returned review %d", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("review identity = (%d, %v), want %d", got, err, test.want)
			}
		})
	}
}

func TestDecodePublicationReviewIDPropagatesCancellation(t *testing.T) {
	cause := errors.New("stop publication response")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if _, err := decodePublicationReviewID(ctx, []byte(`{"id":7}`)); !errors.Is(err, cause) {
		t.Fatalf("publication response cancellation = %v", err)
	}
}

func TestCancellationReaderCapsUnderlyingReads(t *testing.T) {
	t.Parallel()

	input := &recordingReader{remaining: maxSourceProtocolToken * 2}
	reader := newCancellationReader(t.Context(), input)
	buffer := make([]byte, maxSourceProtocolToken*2)
	if count, err := reader.Read(buffer); err != nil || count != maxSourceProtocolToken {
		t.Fatalf("first read = (%d, %v)", count, err)
	}
	if input.maximumRequest != maxSourceProtocolToken {
		t.Fatalf("maximum underlying request = %d, want %d", input.maximumRequest, maxSourceProtocolToken)
	}
}

type recordingReader struct {
	remaining      int
	maximumRequest int
}

func (reader *recordingReader) Read(value []byte) (int, error) {
	reader.maximumRequest = max(reader.maximumRequest, len(value))
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	count := min(len(value), reader.remaining)
	reader.remaining -= count
	return count, nil
}
