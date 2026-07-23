package review

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

func TestSyntaxStorageChargesCoverCompiledLayouts(t *testing.T) {
	charges := []struct {
		name   string
		charge int64
		layout uintptr
	}{
		{"prepared", preparedSyntaxMetadataBytes, unsafe.Sizeof(PreparedSyntax{})},
		{"line index", syntaxLineIndexBytes, unsafe.Sizeof(syntaxLineIndex{})},
		{"span", syntaxSpanBytes, unsafe.Sizeof(SyntaxSpan{})},
		{"demand", syntaxDemandMetadataBytes, unsafe.Sizeof(SyntaxDemand{})},
		{"request", syntaxRequestMetadataBytes, unsafe.Sizeof(SyntaxRequest{})},
	}
	for _, test := range charges {
		if test.charge != int64(test.layout) {
			t.Errorf("%s charge = %d, compiled layout %d", test.name, test.charge, test.layout)
		}
	}
}

func TestSyntaxRequestRetainedBytesUsesSliceCapacitiesAndIdentityStrings(t *testing.T) {
	identity := syntaxTestIdentity(t)
	demands := make([]SyntaxDemand, 1, 2)
	demands[0] = SyntaxDemand{
		Identity: identity,
		Lines:    make([]LineNumber, 1, 4),
		Side:     LineNew,
	}
	request := SyntaxRequest{
		Demands: demands, Operation: 1, Workspace: 2, Source: 3, File: 4,
	}
	want := syntaxRequestMetadataBytes + 2*syntaxDemandMetadataBytes + 4*4 +
		int64(len(identity.content)+len(identity.selector))
	if got := request.RetainedBytes(); got != want {
		t.Fatalf("request retained bytes = %d, want %d", got, want)
	}
}

func TestPreparedSyntaxReturnsNilForUnavailableOrEmptyLine(t *testing.T) {
	line, err := NewLineNumber(1)
	if err != nil {
		t.Fatal(err)
	}
	var unavailable *PreparedSyntax
	if spans := unavailable.Spans(line); spans != nil {
		t.Fatal("nil prepared syntax returned a non-nil iterator")
	}
	empty := &PreparedSyntax{lines: []syntaxLineIndex{{line: line}}}
	if spans := empty.Spans(line); spans != nil {
		t.Fatal("empty syntax line returned a non-nil iterator")
	}
	if spans := empty.Spans(line + 1); spans != nil {
		t.Fatal("uncovered syntax line returned a non-nil iterator")
	}
}

func TestPreparedSyntaxFlatIndexCoversEmptyLinesAndReturnsBorrowedSpans(t *testing.T) {
	identity := syntaxTestIdentity(t)
	lines := make([]LineNumber, MaxSyntaxLines)
	var blob strings.Builder
	for index := range lines {
		lines[index] = LineNumber(index + 1)
		blob.WriteByte('\n')
	}
	prepared, err := PrepareSyntax(identity, lines, []byte(blob.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.lines) != MaxSyntaxLines || len(prepared.spans) != 0 {
		t.Fatalf("flat result = %d lines, %d spans", len(prepared.lines), len(prepared.spans))
	}
	if !prepared.Covers(lines[0]) || !prepared.Covers(lines[len(lines)-1]) ||
		prepared.Covers(LineNumber(MaxSyntaxLines+1)) {
		t.Fatal("flat line index coverage is incorrect")
	}
	want := preparedSyntaxMetadataBytes +
		int64(len(identity.content)+len(identity.selector)) +
		int64(cap(prepared.lines))*syntaxLineIndexBytes
	if prepared.RetainedBytes() != want {
		t.Fatalf("retained bytes = %d, want %d", prepared.RetainedBytes(), want)
	}
	if prepared.ConstructionBytes() < prepared.RetainedBytes()+int64(cap(lines))*4 {
		t.Fatalf("construction bytes = %d, retained = %d", prepared.ConstructionBytes(), prepared.RetainedBytes())
	}
}

func TestPreparedSyntaxFlatSpansPreserveLineRanges(t *testing.T) {
	identity := syntaxTestIdentity(t)
	lines := []LineNumber{1, 2, 3}
	prepared, err := PrepareSyntax(
		identity,
		lines,
		[]byte("var first = 1\nplain\n// comment"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.lines) != 3 || len(prepared.spans) != 3 {
		t.Fatalf("flat result = %d lines, %d spans", len(prepared.lines), len(prepared.spans))
	}
	if got := prepared.Spans(2); got != nil {
		t.Fatal("plain line returned a non-nil span iterator")
	}
	third := slices.Collect(prepared.Spans(3))
	if len(third) != 1 || third[0].Kind != SyntaxComment {
		t.Fatalf("third line spans = %#v", third)
	}
	if allocations := testing.AllocsPerRun(100, func() {
		_ = prepared.Covers(3)
	}); allocations != 0 {
		t.Fatalf("Covers allocations = %v", allocations)
	}
}

func TestPreparedSyntaxExactMaximumSpanCapacityAndOneOver(t *testing.T) {
	identity := syntaxTestIdentity(t)
	const linesAtLimit = MaxSyntaxSpans / 256
	line := strings.Repeat("1 ", 256)
	lines := make([]LineNumber, linesAtLimit)
	blob := strings.Repeat(line+"\n", linesAtLimit)
	for index := range lines {
		lines[index] = LineNumber(index + 1)
	}
	prepared, err := PrepareSyntax(identity, lines, []byte(blob))
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.spans) != MaxSyntaxSpans || cap(prepared.spans) != MaxSyntaxSpans {
		t.Fatalf("span storage = len %d cap %d", len(prepared.spans), cap(prepared.spans))
	}
	want := preparedSyntaxMetadataBytes +
		int64(len(identity.content)+len(identity.selector)) +
		int64(cap(prepared.lines))*syntaxLineIndexBytes +
		int64(cap(prepared.spans))*syntaxSpanBytes
	if prepared.RetainedBytes() != want || want > MaxSyntaxBytes {
		t.Fatalf("retained bytes = %d, want %d under %d", prepared.RetainedBytes(), want, MaxSyntaxBytes)
	}

	overLines := make([]LineNumber, len(lines)+1)
	copy(overLines, lines)
	overLines[len(lines)] = LineNumber(linesAtLimit + 1)
	blob += line + "\n"
	if _, err := PrepareSyntax(identity, overLines, []byte(blob)); !errors.Is(err, ErrSyntaxLimited) {
		t.Fatalf("one-over span error = %v", err)
	}
}

func syntaxTestIdentity(t *testing.T) SyntaxIdentity {
	t.Helper()
	content, err := NewObjectID(strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	selector, err := NewSyntaxSelector("go")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := NewSyntaxIdentity(content, selector)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestPrepareSyntaxCancellableStopsDuringDenseBlobScan(t *testing.T) {
	content, err := NewObjectID(strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	selector, err := NewSyntaxSelector("go")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := NewSyntaxIdentity(content, selector)
	if err != nil {
		t.Fatal(err)
	}
	line, err := NewLineNumber(1)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("stop syntax preparation")
	checks := 0
	_, err = PrepareSyntaxCancellable(func() error {
		checks++
		if checks >= 4 {
			return cause
		}
		return nil
	}, identity, []LineNumber{line}, bytes.Repeat([]byte{'x'}, 8*syntaxPollBytes))
	if !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
	if checks != 4 {
		t.Fatalf("cancellation checks = %d, want 4", checks)
	}
}
