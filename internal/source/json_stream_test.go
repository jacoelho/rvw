package source

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestValidateJSONLexicalBoundsRawStringBeforeDecode(t *testing.T) {
	if err := validateJSONLexical(strings.NewReader(`[{"body":"1234"}]`), 6); err != nil {
		t.Fatalf("exact token limit rejected: %v", err)
	}
	if err := validateJSONLexical(strings.NewReader(`[{"body":"1234567"}]`), 6); err == nil {
		t.Fatal("one-byte-over string token was accepted")
	}
}

func TestValidateJSONLexicalStopsOnCancellation(t *testing.T) {
	cause := errors.New("stop JSON validation")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if err := validateJSONLexicalContext(
		ctx,
		strings.NewReader(`[{"body":"still valid"}]`),
		64,
	); !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestValidateJSONLexicalRejectsUnsafeStructure(t *testing.T) {
	for _, value := range []string{
		`[{"body":"unterminated}]`,
		`[{"body":"bad\x"}]`,
		`[{"body":"ok"}] trailing`,
		`[{"body":"ok"}}]`,
		`[1,]`,
		`{"a":1,}`,
		`{"a" 1}`,
		`{"a":01}`,
		`[true false]`,
	} {
		if err := validateJSONLexical(strings.NewReader(value), 64); err == nil {
			t.Fatalf("invalid lexical JSON accepted: %q", value)
		}
	}
}

func TestRecognizedJSONFieldsRejectDuplicates(t *testing.T) {
	if _, err := decodeRepositoryJSON(
		t.Context(),
		[]byte(`{"nameWithOwner":"owner/one","nameWithOwner":"owner/two"}`),
		directCPUAdmission,
	); err == nil || !strings.Contains(err.Error(), "duplicate recognized field") {
		t.Fatalf("duplicate field error = %v", err)
	}
}

func TestPullRequestJSONRequiresEveryField(t *testing.T) {
	encoded := []byte(`{"merged_at":null,"user":{"login":"owner"},` +
		`"base":{"ref":"main","sha":"1111111111111111111111111111111111111111"},` +
		`"head":{"ref":"topic","sha":"2222222222222222222222222222222222222222"},` +
		`"title":"title","html_url":"https://example.invalid/pull/1",` +
		`"state":"open","number":1,"changed_files":1}`)
	if _, err := decodePullRequestJSON(t.Context(), encoded, directCPUAdmission); err == nil ||
		!strings.Contains(err.Error(), "missing a required field") {
		t.Fatalf("missing required field error = %v", err)
	}
}

func TestJSONStreamReadsOutsideCPUAdmission(t *testing.T) {
	active := false
	reader := &admissionGuardReader{
		active: &active,
		input: strings.NewReader(
			"[{}" + strings.Repeat(" ", 2*sourceStreamBufferBytes) + "]",
		),
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	admit := func(ctx context.Context, work func() error) error {
		if active {
			return errors.New("CPU admission re-entered")
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		active = true
		defer func() { active = false }()
		return work()
	}
	metrics, err := inspectJSONWithCPU(
		t.Context(), reader, maxGitHubJSONTokenBytes, admit, ledger,
	)
	if err != nil {
		t.Fatal(err)
	}
	metrics.release(ledger)
	if reader.reads < 3 {
		t.Fatalf("reader calls = %d, want multiple bounded reads", reader.reads)
	}
}

func TestValidateJSONLexicalRejectsInvalidUTF8(t *testing.T) {
	for _, value := range [][]byte{
		[]byte("[{\"body\":\"\xff\"}]"),
		[]byte("[{\"body\":\"\xe2\x82\"}]"),
		[]byte("[{\"body\":\"\xed\xa0\x80\"}]"),
	} {
		if err := validateJSONLexical(strings.NewReader(string(value)), 64); err == nil {
			t.Fatalf("invalid UTF-8 JSON accepted: %x", value)
		}
	}
}

func TestInspectJSONLexicalRecordsFramingInputs(t *testing.T) {
	const page = `{"a":{},"b":[{},{}]}`
	metrics, err := inspectJSONLexical(
		t.Context(),
		strings.NewReader("["+page+",{}]"),
		64,
	)
	if err != nil {
		t.Fatalf("inspect JSON: %v", err)
	}
	if metrics.maximumTokenBytes != 3 {
		t.Fatalf("maximum token bytes = %d, want 3", metrics.maximumTokenBytes)
	}
	if !metrics.limits.valid() {
		t.Fatal("framing limits are invalid")
	}
	wantCounts := []uint32{2, 2}
	if !slices.Equal(metrics.cardinalities, wantCounts) {
		t.Fatalf("cardinalities = %v, want %v", metrics.cardinalities, wantCounts)
	}
}

func TestGitHubPageCollectionsRejectDenseElementOverCardinality(t *testing.T) {
	encoded := "[" + strings.Repeat("{},", maxGitHubPageCollectionElements) + "{}]"
	if err := validateJSONLexical(strings.NewReader(encoded), 64); err == nil ||
		!strings.Contains(err.Error(), "exceeds 100 elements") {
		t.Fatalf("over-cardinality error = %v", err)
	}
}

func TestJSONFramingLimitsRootAndNestedCollectionsIndependently(t *testing.T) {
	limits, err := newJSONFramingLimits(101, maxGitHubPageCollectionElements)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	root := "[" + strings.Repeat("{},", 100) + "{}]"
	metrics, err := inspectJSONWithLimits(
		t.Context(), strings.NewReader(root), 64, directCPUAdmission, ledger, limits,
	)
	if err != nil {
		t.Fatalf("101 root items rejected: %v", err)
	}
	metrics.release(ledger)

	overRoot := "[" + strings.Repeat("{},", 101) + "{}]"
	if _, err := inspectJSONWithLimits(
		t.Context(), strings.NewReader(overRoot), 64, directCPUAdmission, ledger, limits,
	); err == nil || !strings.Contains(err.Error(), "exceeds 101 elements") {
		t.Fatalf("root-limit error = %v", err)
	}

	nested := `[{"nodes":[` + strings.Repeat("{},", 100) + "{}]}]"
	if _, err := inspectJSONWithLimits(
		t.Context(), strings.NewReader(nested), 64, directCPUAdmission, ledger, limits,
	); err == nil || !strings.Contains(err.Error(), "exceeds 100 elements") {
		t.Fatalf("nested-limit error = %v", err)
	}
}

func TestJSONFramingLimitsRejectInvalidBounds(t *testing.T) {
	for _, bounds := range [][2]int{{0, 1}, {1, 0}, {-1, 1}, {1, -1}} {
		if _, err := newJSONFramingLimits(bounds[0], bounds[1]); err == nil {
			t.Fatalf("invalid framing bounds accepted: %v", bounds)
		}
	}
}

func TestInspectJSONLexicalDenseInputStopsOnCancellation(t *testing.T) {
	cause := errors.New("stop dense JSON validation")
	ctx, cancel := context.WithCancelCause(t.Context())
	input := &cancelAfterFirstRead{
		input:  strings.NewReader("[" + strings.Repeat("{},", 50_000) + "{}]"),
		cancel: func() { cancel(cause) },
	}
	if _, err := inspectJSONLexical(ctx, input, 64); !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
}

type cancelAfterFirstRead struct {
	input  *strings.Reader
	cancel func()
	done   bool
}

type admissionGuardReader struct {
	input  *strings.Reader
	active *bool
	reads  int
}

func (reader *admissionGuardReader) Read(value []byte) (int, error) {
	if *reader.active {
		return 0, errors.New("external read occurred inside CPU admission")
	}
	reader.reads++
	return reader.input.Read(value)
}

func (reader *cancelAfterFirstRead) Read(value []byte) (int, error) {
	count, err := reader.input.Read(value)
	if !reader.done && count > 0 {
		reader.done = true
		reader.cancel()
	}
	return count, err
}
