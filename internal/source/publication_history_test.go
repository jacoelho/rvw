package source

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

const publicationHistoryTestMarker = "<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->"

func TestReviewHistoryRequiresCompleteValidatedPages(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		want    int
		wantErr bool
	}{
		{
			name:    "valid sparse pages",
			encoded: `[[{"id":1,"body":"` + publicationHistoryTestMarker + `"}],[],[{"id":2,"body":""}]]`,
			want:    1,
		},
		{name: "null", encoded: `null`, wantErr: true},
		{name: "no pages", encoded: `[]`, wantErr: true},
		{name: "null page", encoded: `[null]`, wantErr: true},
		{name: "flat response", encoded: `[{"id":1,"body":"first"}]`, wantErr: true},
		{name: "missing identity", encoded: `[[{"body":"first"}]]`, wantErr: true},
		{name: "zero identity", encoded: `[[{"id":0,"body":"first"}]]`, wantErr: true},
		{
			name:    "repeated identity discards tentative marker count",
			encoded: `[[{"id":1,"body":"` + publicationHistoryTestMarker + `"}],[{"id":1,"body":"second"}]]`,
			wantErr: true,
		},
		{name: "missing body", encoded: `[[{"id":1}]]`, wantErr: true},
		{name: "null body", encoded: `[[{"id":1,"body":null}]]`, wantErr: true},
		{name: "duplicate identity field", encoded: `[[{"id":1,"id":2,"body":"first"}]]`, wantErr: true},
		{name: "duplicate body field", encoded: `[[{"id":1,"body":"first","body":"second"}]]`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matches, err := countReviewHistoryBytesForTest(
				t,
				[]byte(test.encoded),
				directCPUAdmission,
			)
			if (err != nil) != test.wantErr {
				t.Fatalf("history error = %v, want error %t", err, test.wantErr)
			}
			if matches != test.want {
				t.Fatalf("history matches = %d, want %d", matches, test.want)
			}
		})
	}
}

func TestReviewHistorySemanticPassKeepsIOOutsideAdmissionAndReleasesOwnership(t *testing.T) {
	encoded := []byte(`[[{"id":1,"body":"` + publicationHistoryTestMarker + `"}],[{"id":2,"body":"second"}]]`)
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	limits := reviewHistoryFramingLimitsForTest(t)
	metrics, err := inspectJSONWithLimits(
		t.Context(),
		bytes.NewReader(encoded),
		maxGitHubJSONTokenBytes,
		directCPUAdmission,
		ledger,
		limits,
	)
	if err != nil {
		t.Fatal(err)
	}
	baseline := ledger.used
	active := false
	reader := &reviewHistoryAdmissionReader{
		input:  bytes.NewReader(encoded),
		active: &active,
	}
	runCPU := func(_ context.Context, work func() error) error {
		if active {
			return errors.New("nested review history CPU admission")
		}
		before := reader.bytes
		active = true
		err := work()
		active = false
		if reader.bytes != before {
			return errors.Join(err, errors.New("review history read under CPU admission"))
		}
		return err
	}
	matches, err := countReviewHistoryMarkersReaderContext(
		t.Context(),
		reader,
		publicationHistoryTestMarker,
		runCPU,
		ledger,
		metrics,
	)
	if err != nil {
		t.Fatal(err)
	}
	if matches != 1 {
		t.Fatalf("history matches = %d, want 1", matches)
	}
	if reader.bytes != len(encoded) {
		t.Fatalf("post-preflight history bytes = %d, want %d", reader.bytes, len(encoded))
	}
	if ledger.used != baseline {
		t.Fatalf("history collector retained %d bytes, started with %d", ledger.used, baseline)
	}
	metrics.release(ledger)
	if ledger.used != 0 {
		t.Fatalf("history metrics retained %d bytes", ledger.used)
	}
}

func TestReviewHistoryPaginationAndAggregateLimitsAreIndependent(t *testing.T) {
	t.Run("sparse pages beyond full-page ceiling", func(t *testing.T) {
		encoded := reviewHistoryFixture(1_001)
		if _, err := countReviewHistoryBytesForTest(t, encoded, directCPUAdmission); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("nested page over limit", func(t *testing.T) {
		var encoded strings.Builder
		encoded.WriteString("[[")
		for index := range maxGitHubPageCollectionElements + 1 {
			if index != 0 {
				encoded.WriteByte(',')
			}
			writeReviewHistoryItem(&encoded, index+1)
		}
		encoded.WriteString("]]")
		if _, err := countReviewHistoryBytesForTest(
			t,
			[]byte(encoded.String()),
			directCPUAdmission,
		); err == nil {
			t.Fatal("history page with 101 items was accepted")
		}
	})

	t.Run("root page over limit", func(t *testing.T) {
		var encoded strings.Builder
		encoded.Grow(3*maxGitHubReviewHistoryItems + 4)
		encoded.WriteByte('[')
		for page := range maxGitHubReviewHistoryItems + 1 {
			if page != 0 {
				encoded.WriteByte(',')
			}
			encoded.WriteString("[]")
		}
		encoded.WriteByte(']')
		if _, err := countReviewHistoryBytesForTest(
			t,
			[]byte(encoded.String()),
			directCPUAdmission,
		); err == nil {
			t.Fatal("history root page count above its bound was accepted")
		}
	})

	t.Run("aggregate item over limit", func(t *testing.T) {
		encoded := reviewHistoryFixture(maxGitHubReviewHistoryItems + 1)
		if _, err := countReviewHistoryBytesForTest(t, encoded, directCPUAdmission); err == nil {
			t.Fatal("history aggregate above its bound was accepted")
		}
	})
}

func TestReviewHistoryAcceptsMaximumCardinality(t *testing.T) {
	encoded := reviewHistoryFixture(maxGitHubReviewHistoryItems)
	matches, err := countReviewHistoryBytesForTest(t, encoded, directCPUAdmission)
	if err != nil {
		t.Fatal(err)
	}
	if matches != 0 {
		t.Fatalf("maximum history matches = %d", matches)
	}
}

func TestCountPublicationMarkersCancellableStopsAcrossMaximumBody(t *testing.T) {
	t.Parallel()

	marker := "<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->"
	body := strings.Repeat("x", 16<<20-len(marker)) + marker
	cause := errors.New("stop marker scan")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if _, err := countPublicationMarkersCancellable(ctx, body, marker); !errors.Is(err, cause) {
		t.Fatalf("maximum-body marker cancellation = %v", err)
	}
}

func countReviewHistoryBytesForTest(
	t *testing.T,
	encoded []byte,
	runCPU CPUAdmission,
) (int, error) {
	t.Helper()
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := inspectJSONWithLimits(
		t.Context(),
		bytes.NewReader(encoded),
		maxGitHubJSONTokenBytes,
		runCPU,
		ledger,
		reviewHistoryFramingLimitsForTest(t),
	)
	if err != nil {
		return 0, err
	}
	matches, err := countReviewHistoryMarkersReaderContext(
		t.Context(),
		bytes.NewReader(encoded),
		publicationHistoryTestMarker,
		runCPU,
		ledger,
		metrics,
	)
	metrics.release(ledger)
	if ledger.used != 0 {
		t.Fatalf("history decode retained %d bytes", ledger.used)
	}
	return matches, err
}

func reviewHistoryFramingLimitsForTest(t *testing.T) jsonFramingLimits {
	t.Helper()
	limits, err := newJSONFramingLimits(
		maxGitHubReviewHistoryItems,
		maxGitHubPageCollectionElements,
	)
	if err != nil {
		t.Fatal(err)
	}
	return limits
}

func reviewHistoryFixture(items int) []byte {
	var encoded strings.Builder
	encoded.Grow(items * 22)
	encoded.WriteByte('[')
	for item := range items {
		if item%maxGitHubPageCollectionElements == 0 {
			if item != 0 {
				encoded.WriteString("],[")
			} else {
				encoded.WriteByte('[')
			}
		} else {
			encoded.WriteByte(',')
		}
		writeReviewHistoryItem(&encoded, item+1)
	}
	if items == 0 {
		encoded.WriteString("[]")
	} else {
		encoded.WriteByte(']')
	}
	encoded.WriteByte(']')
	return []byte(encoded.String())
}

func writeReviewHistoryItem(encoded *strings.Builder, id int) {
	encoded.WriteString(`{"id":`)
	encoded.WriteString(strconv.Itoa(id))
	encoded.WriteString(`,"body":""}`)
}

type reviewHistoryAdmissionReader struct {
	input  *bytes.Reader
	active *bool
	bytes  int
}

func (reader *reviewHistoryAdmissionReader) Read(value []byte) (int, error) {
	if *reader.active {
		return 0, errors.New("review history read while CPU admission is active")
	}
	count, err := reader.input.Read(value)
	reader.bytes += count
	return count, err
}
