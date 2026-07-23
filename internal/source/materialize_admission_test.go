package source

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestPatchAcquisitionFinishesUnderOneCPUAdmission(t *testing.T) {
	acquisition := PatchAcquisition{
		result:  review.PatchResult{Workspace: 1},
		text:    []byte("@@ -1 +1 @@\n-old\n+new\n"),
		valid:   true,
		prepare: true,
	}
	calls := 0
	result, err := acquisition.Finish(t.Context(), func(ctx context.Context, work func() error) error {
		calls++
		if err := context.Cause(ctx); err != nil {
			return err
		}
		return work()
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Status != review.PatchReady || result.Prepared == nil {
		t.Fatalf("calls=%d result=%+v", calls, result)
	}
	if acquisition.RequiresPreparation() {
		t.Fatal("consumed acquisition still requires preparation")
	}
	if _, err := acquisition.Finish(t.Context(), directCPUAdmission); err == nil {
		t.Fatal("consumed acquisition finished twice")
	}
}

func TestPatchAcquisitionTerminalResultSkipsCPUAdmission(t *testing.T) {
	acquisition := PatchAcquisition{
		result: review.PatchResult{Status: review.PatchUnavailable},
		valid:  true,
	}
	result, err := acquisition.Finish(t.Context(), func(context.Context, func() error) error {
		t.Fatal("terminal patch result requested CPU admission")
		return nil
	})
	if err != nil || result.Status != review.PatchUnavailable {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestPatchAcquisitionCancellationReleasesOwnedText(t *testing.T) {
	cause := errors.New("stop patch admission")
	acquisition := PatchAcquisition{
		text:    []byte("patch"),
		valid:   true,
		prepare: true,
	}
	result, err := acquisition.Finish(t.Context(), func(context.Context, func() error) error {
		return cause
	})
	if !errors.Is(err, cause) || result != (review.PatchResult{}) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if acquisition.text != nil || acquisition.valid {
		t.Fatal("failed admission retained patch ownership")
	}
}

func TestSyntaxAcquisitionFinishesUnderOneCPUAdmission(t *testing.T) {
	content, err := review.NewObjectID(strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	selector, err := review.NewSyntaxSelector("go")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := review.NewSyntaxIdentity(content, selector)
	if err != nil {
		t.Fatal(err)
	}
	line, err := review.NewLineNumber(1)
	if err != nil {
		t.Fatal(err)
	}
	acquisition := SyntaxAcquisition{
		identity: identity,
		lines:    []review.LineNumber{line},
		blob:     []byte("package main\n"),
		valid:    true,
	}
	calls := 0
	prepared, err := acquisition.Finish(t.Context(), func(_ context.Context, work func() error) error {
		calls++
		return work()
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || prepared == nil || !prepared.Covers(line) {
		t.Fatalf("calls=%d prepared=%+v", calls, prepared)
	}
	if acquisition.blob != nil || acquisition.valid {
		t.Fatal("finished syntax acquisition retained blob ownership")
	}
}
