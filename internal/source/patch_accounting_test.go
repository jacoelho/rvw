package source

import (
	"testing"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/subprocess"
)

func TestPatchConstructionAccountingCoversEveryPhase(t *testing.T) {
	wantCapture := subprocess.MaxCaptureConstructionBytes(review.MaxPatchBytes) +
		subprocess.MaxCaptureConstructionBytes(subprocess.DefaultStderrLimit)
	if MaxPatchCaptureConstructionBytes != wantCapture {
		t.Fatalf(
			"patch capture construction = %d, want %d",
			MaxPatchCaptureConstructionBytes,
			wantCapture,
		)
	}
	wantError := review.MaxPatchBytes + MaxCommandErrorConstructionBytes
	if MaxPatchErrorConstructionBytes != wantError {
		t.Fatalf(
			"patch error construction = %d, want %d",
			MaxPatchErrorConstructionBytes,
			wantError,
		)
	}
	wantResult := max(
		wantCapture,
		wantError,
		review.MaxPreparedPatchConstructionBytes,
		review.MaxPreparedPatchRetainedBytes,
	)
	if MaxPatchResultConstructionBytes != wantResult {
		t.Fatalf(
			"patch result construction = %d, want %d",
			MaxPatchResultConstructionBytes,
			wantResult,
		)
	}
}

func TestPatchConstructionAccountingHasReviewedExactValues(t *testing.T) {
	if MaxPatchCaptureConstructionBytes != 102_236_160 {
		t.Fatalf("patch capture construction = %d", MaxPatchCaptureConstructionBytes)
	}
	if MaxPatchErrorConstructionBytes != 68_190_464 {
		t.Fatalf("patch error construction = %d", MaxPatchErrorConstructionBytes)
	}
	if MaxPatchResultConstructionBytes != 102_236_160 {
		t.Fatalf("patch result construction = %d", MaxPatchResultConstructionBytes)
	}
}
