package source

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestSourceJSONBuilderStringMatchesEncodingJSON(t *testing.T) {
	values := []string{
		"",
		"plain",
		"\b\f\n\r\t\x00\x1f",
		`quote " slash \\ <>&`,
		"\u2028\u2029\U0001f642",
		string([]byte{'a', 0xff, 'b'}),
	}
	for _, value := range values {
		builder, err := newSourceJSONBuilder(1 << 20)
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

func TestSourceJSONBuilderEscapedScalarHonorsExactLimitAndCancellation(t *testing.T) {
	builder, err := newSourceJSONBuilder(8)
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.String("\x00"); err != nil {
		t.Fatal(err)
	}
	encoded, err := builder.Finish()
	if err != nil || encoded != `"\u0000"` {
		t.Fatalf("encoded scalar = %q, %v", encoded, err)
	}
	builder, _ = newSourceJSONBuilder(7)
	if err := builder.String("\x00"); err == nil {
		t.Fatal("one-byte-over escaped scalar succeeded")
	}

	cause := errors.New("cancel source JSON")
	checks := 0
	builder, _ = newCancellableSourceJSONBuilder(64<<10, func() error {
		checks++
		if checks == 1 {
			return cause
		}
		return nil
	})
	if err := builder.String(strings.Repeat("x", 17<<10)); !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestSourceJSONBuilderChecksLimit(t *testing.T) {
	builder, _ := newSourceJSONBuilder(4)
	if err := builder.String("abc"); err == nil {
		t.Fatal("over-limit string succeeded")
	}
}

func TestSourceJSONBuilderFragmentedWritesCheckCancellationAtExactQuantum(t *testing.T) {
	cause := errors.New("stop fragmented JSON")
	checks := 0
	builder, err := newCancellableSourceJSONBuilder(
		2*sourceBuilderCancellationBytes,
		func() error {
			checks++
			return cause
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.Syntax(strings.Repeat("x", sourceBuilderCancellationBytes-1)); err != nil {
		t.Fatal(err)
	}
	if err := builder.Syntax(strings.Repeat("y", sourceBuilderCancellationBytes)); !errors.Is(err, cause) {
		t.Fatalf("fragmented cancellation = %v", err)
	}
	if len(builder.output) != sourceBuilderCancellationBytes || checks != 1 {
		t.Fatalf("cancelled after %d bytes and %d checks", len(builder.output), checks)
	}
}

func TestSourceJSONBuilderFinishCopiesExactlyAndReleasesMutableBacking(t *testing.T) {
	builder, err := newSourceJSONBuilder(1 << 20)
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
	if want := int64(capacity + len(finished)); builder.constructionPeak() < want {
		t.Fatalf("construction peak = %d, want at least %d", builder.constructionPeak(), want)
	}
	if _, err := builder.Finish(); err == nil {
		t.Fatal("finished builder was reusable")
	}
}

func TestSourceJSONBuilderGrowthNeverExceedsLimitAndChargesOldPlusNew(t *testing.T) {
	builder, err := newSourceJSONBuilder(129)
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
	if builder.constructionPeak() < 64+128 {
		t.Fatalf("growth peak = %d, want old-plus-new capacity", builder.constructionPeak())
	}
	if err := builder.Syntax(strings.Repeat("y", 65)); err == nil {
		t.Fatal("one-byte-over write succeeded")
	}
}
