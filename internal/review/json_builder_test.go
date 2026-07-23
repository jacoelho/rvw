package review

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestBoundedBuilderFragmentedWritesCheckCancellationAtExactQuantum(t *testing.T) {
	cause := errors.New("stop fragmented JSON")
	checks := 0
	builder, err := newBoundedBuilder(2*cancellationBytes, func() error {
		checks++
		return cause
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.WriteString(strings.Repeat("x", cancellationBytes-1)); err != nil {
		t.Fatal(err)
	}
	if err := builder.WriteString(strings.Repeat("y", cancellationBytes)); !errors.Is(err, cause) {
		t.Fatalf("fragmented cancellation = %v", err)
	}
	if len(builder.output) != cancellationBytes || checks != 1 {
		t.Fatalf("cancelled after %d bytes and %d checks", len(builder.output), checks)
	}
}

func TestJSONBuilderStringMatchesEncodingJSON(t *testing.T) {
	values := []string{
		"",
		"plain",
		"\b\f\n\r\t\x00\x1f",
		`quote " slash \\ <>&`,
		"\u2028\u2029\U0001f642",
		string([]byte{'a', 0xff, 'b'}),
	}
	for _, value := range values {
		builder, err := newJSONBuilder(1<<20, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := builder.String(value); err != nil {
			t.Fatal(err)
		}
		got, err := builder.Finish()
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Fatalf("String(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestJSONBuilderChecksLimitAndCancellation(t *testing.T) {
	builder, _ := newJSONBuilder(4, nil)
	if err := builder.String("abc"); err == nil {
		t.Fatal("over-limit string succeeded")
	}
	cancelled := false
	builder, _ = newJSONBuilder(32<<10, func() error {
		cancelled = true
		return nil
	})
	if err := builder.String(strings.Repeat("x", 17<<10)); err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		t.Fatal("large scalar did not check cancellation")
	}
}

func TestJSONBuilderFinishCopiesExactlyAndReleasesMutableBacking(t *testing.T) {
	builder, err := newJSONBuilder(1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.String(strings.Repeat("x", 64<<10)); err != nil {
		t.Fatal(err)
	}
	backing := unsafe.SliceData(builder.output)
	capacity := cap(builder.output)
	finished, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.StringData(finished) == backing {
		t.Fatal("Finish retained the mutable JSON backing")
	}
	if builder.output != nil {
		t.Fatal("Finish did not release the mutable JSON backing")
	}
	if want := int64(capacity + len(finished)); builder.ConstructionPeak() < want {
		t.Fatalf("construction peak = %d, want at least %d", builder.ConstructionPeak(), want)
	}
	if _, err := builder.Finish(); err == nil {
		t.Fatal("finished builder was reusable")
	}
}

func TestJSONBuilderGrowthNeverExceedsLimitAndChargesOldPlusNew(t *testing.T) {
	builder, err := newJSONBuilder(129, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.Syntax(strings.Repeat("x", 64)); err != nil {
		t.Fatal(err)
	}
	if err := builder.Syntax("x"); err != nil {
		t.Fatal(err)
	}
	if cap(builder.output) > 129 {
		t.Fatalf("capacity = %d, limit 129", cap(builder.output))
	}
	if builder.ConstructionPeak() < 64+128 {
		t.Fatalf("growth peak = %d, want old-plus-new capacity", builder.ConstructionPeak())
	}
	if err := builder.Syntax(strings.Repeat("y", 65)); err == nil {
		t.Fatal("one-byte-over write succeeded")
	}
}
