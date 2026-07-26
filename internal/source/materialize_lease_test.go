package source

import (
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func TestPatchAcquisitionCopyCannotConsumeLeaseTwice(t *testing.T) {
	acquisition := newPatchAcquisition(change.PatchResult{
		Status: change.PatchUnavailable,
	}, nil, false)
	copyOfAcquisition := acquisition

	result, err := acquisition.Finish(t.Context(), directCPUAdmission)
	if err != nil || result.Status != change.PatchUnavailable {
		t.Fatalf("first Finish() = %+v, %v", result, err)
	}
	if _, err := copyOfAcquisition.Finish(t.Context(), directCPUAdmission); err == nil ||
		!strings.Contains(err.Error(), "acquisition is invalid") {
		t.Fatalf("copied Finish() error = %v, want consumed lease", err)
	}
}

func TestSyntaxAcquisitionCopySharesClose(t *testing.T) {
	acquisition := SyntaxAcquisition{state: &syntaxAcquisitionState{valid: true}}
	copyOfAcquisition := acquisition

	acquisition.Close()
	if _, err := copyOfAcquisition.Finish(t.Context(), directCPUAdmission); err == nil ||
		!strings.Contains(err.Error(), "acquisition is invalid") {
		t.Fatalf("copied Finish() error = %v, want closed lease", err)
	}
}
