package change

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestPreparedPatchAccountingMatchesStorageLayout(t *testing.T) {
	if got := int64(unsafe.Sizeof(PreparedPatch{})); got != preparedPatchMetadataBytes {
		t.Fatalf("PreparedPatch metadata = %d bytes, want %d", got, preparedPatchMetadataBytes)
	}
	if got := int64(unsafe.Sizeof(PatchOffset(0))); got != patchUint32StorageBytes {
		t.Fatalf("PatchOffset storage = %d bytes, want %d", got, patchUint32StorageBytes)
	}
	if got := int64(unsafe.Sizeof(PatchLineKind(0))); got != 1 {
		t.Fatalf("PatchLineKind storage = %d bytes, want 1", got)
	}
	if got := int64(unsafe.Sizeof(patchLineDescriptor(0))); got != 1 {
		t.Fatalf("patch line descriptor storage = %d bytes, want 1", got)
	}
	if MaxPreparedPatchRetainedBytes != 93_309_144 {
		t.Fatalf("maximum retained patch = %d bytes", MaxPreparedPatchRetainedBytes)
	}
	if MaxPreparedPatchConstructionBytes != 95_309_144 {
		t.Fatalf("maximum patch construction = %d bytes", MaxPreparedPatchConstructionBytes)
	}
}

func TestPreparedPatchRetainedBytesChargesTransferredCapacities(t *testing.T) {
	const textCapacity = 128
	text := make([]byte, len("@@ -1 +1 @@\n-old\n+new\n"), textCapacity)
	copy(text, "@@ -1 +1 @@\n-old\n+new\n")
	prepared, err := TakePatch(text)
	if err != nil {
		t.Fatal(err)
	}
	if cap(prepared.text) != textCapacity {
		t.Fatalf("transferred text capacity = %d, want %d", cap(prepared.text), textCapacity)
	}
	want := int64(unsafe.Sizeof(*prepared)) + int64(cap(prepared.text)) +
		int64(cap(prepared.oldLineRows))*int64(unsafe.Sizeof(uint32(0))) +
		int64(cap(prepared.hunkFirst))*int64(unsafe.Sizeof(uint32(0))) +
		int64(cap(prepared.lineOffsets))*int64(unsafe.Sizeof(PatchOffset(0))) +
		int64(cap(prepared.lineKinds))*int64(unsafe.Sizeof(patchLineDescriptor(0))) +
		int64(cap(prepared.oldLines))*int64(unsafe.Sizeof(uint32(0))) +
		int64(cap(prepared.newLines))*int64(unsafe.Sizeof(uint32(0))) +
		int64(cap(prepared.lineHunks))*int64(unsafe.Sizeof(uint32(0))) +
		int64(cap(prepared.newLineRows))*int64(unsafe.Sizeof(uint32(0))) +
		int64(cap(prepared.hunkLast))*int64(unsafe.Sizeof(uint32(0))) +
		int64(cap(prepared.hunkLines))*int64(unsafe.Sizeof(uint32(0)))
	if got := prepared.RetainedBytes(); got != want {
		t.Fatalf("retained patch = %d bytes, want %d", got, want)
	}
	if prepared.RetainedBytes() <= int64(len(text)) {
		t.Fatalf("retained patch ignored backing capacity: %d", prepared.RetainedBytes())
	}
}

func TestTakePatchRejectsExcessRetainedCapacity(t *testing.T) {
	text := make([]byte, 0, int(MaxPatchBytes)+1)
	if _, err := TakePatch(text); err == nil || !strings.Contains(err.Error(), "retained capacity") {
		t.Fatalf("excess-capacity patch error = %v", err)
	}
}

func TestSparsePatchGrowthJumpsFromHalfToExactLimit(t *testing.T) {
	const limit = 8
	values := make([]uint32, limit/2)
	for index := range values {
		values[index] = uint32(index + 1)
	}
	values = appendBoundedPatchUint32(values, 99, limit)
	if len(values) != limit/2+1 || cap(values) != limit || values[limit/2] != 99 {
		t.Fatalf("grown sparse values = len %d cap %d values %v", len(values), cap(values), values)
	}
	if oldAndNew := int64(limit+limit/2) * patchUint32StorageBytes; oldAndNew != 48 {
		t.Fatalf("sparse construction peak = %d bytes", oldAndNew)
	}
}

func TestHunkGrowthKeepsLockstepArraysAtExactLimit(t *testing.T) {
	half := MaxHunks / 2
	patch := &PreparedPatch{
		hunkLines: make([]uint32, half),
		hunkFirst: make([]uint32, half),
		hunkLast:  make([]uint32, half),
	}
	patch.hunkLines[half-1] = 17
	patch.hunkFirst[half-1] = 18
	patch.hunkLast[half-1] = 19
	patch.appendHunk(20)
	if len(patch.hunkLines) != half+1 || len(patch.hunkFirst) != half+1 ||
		len(patch.hunkLast) != half+1 {
		t.Fatalf(
			"hunk lengths = %d, %d, %d",
			len(patch.hunkLines), len(patch.hunkFirst), len(patch.hunkLast),
		)
	}
	if cap(patch.hunkLines) != MaxHunks || cap(patch.hunkFirst) != MaxHunks ||
		cap(patch.hunkLast) != MaxHunks {
		t.Fatalf(
			"hunk capacities = %d, %d, %d",
			cap(patch.hunkLines), cap(patch.hunkFirst), cap(patch.hunkLast),
		)
	}
	if patch.hunkLines[half-1] != 17 || patch.hunkFirst[half-1] != 18 ||
		patch.hunkLast[half-1] != 19 || patch.hunkLines[half] != 20 ||
		patch.hunkFirst[half] != noPatchHunk || patch.hunkLast[half] != noPatchHunk {
		t.Fatal("hunk growth lost or misaligned index data")
	}
}

func TestPatchPreparationTransfersBoundedImmutableResult(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	if _, _, err := current.RequestPatch(0, PatchContext{lines: ContextLines(10_001)}); err == nil {
		t.Fatal("cast context value above the bound was accepted")
	}

	contextLines, _ := NewContextLines(3)
	patchContext, _ := ContextAround(contextLines)
	pending, request, err := current.RequestPatch(0, patchContext)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := TakePatch([]byte("@@ -1 +1 @@\n-" +
		strings.Repeat("a", 8_192) + "\n+short\n"))
	if err != nil {
		t.Fatal(err)
	}
	ready, err := pending.ApplyPatch(PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    PatchReady,
		Prepared:  prepared,
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := ready.PatchLines(0, 0, 10, 64)
	if len(lines) != 3 || !lines[1].Truncated || len(lines[1].Text) != 64 {
		t.Fatalf("bounded patch lines = %+v", lines)
	}
	if ready.RetainedPatchBytes() != prepared.RetainedBytes() {
		t.Fatalf(
			"retained patch bytes = %d, want %d",
			ready.RetainedPatchBytes(),
			prepared.RetainedBytes(),
		)
	}
}

func TestTerminalPatchFailureRejectsPreparedData(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	contextLines, _ := NewContextLines(3)
	patchContext, _ := ContextAround(contextLines)
	pending, request, err := current.RequestPatch(0, patchContext)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := TakePatch([]byte("patch\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pending.ApplyPatch(PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    PatchFailed,
		Prepared:  prepared,
	}); err == nil {
		t.Fatal("failed patch carrying prepared data was accepted")
	}
}

func TestPatchRequestReplacesInactivePresentationSlot(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go", "two.go"))
	context := WholeFileContext()
	pending, first, err := current.RequestPatch(0, context)
	if err != nil {
		t.Fatal(err)
	}
	current, err = pending.ApplyPatch(PatchResult{
		Workspace: first.Workspace,
		Handle:    first.Handle,
		Status:    PatchCancelled,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, second, err := current.RequestPatch(1, context)
	if err != nil {
		t.Fatal(err)
	}
	if pending.patch.file != 1 || !pending.patch.set ||
		pending.Patch(0).Status != PatchUnavailable ||
		pending.Patch(1).Status != PatchPending ||
		pending.RetainedPatchBytes() != 0 {
		t.Fatalf("replacement patch slot = %+v", pending.patch)
	}
	if second.Handle.FileID() != 1 {
		t.Fatalf("replacement handle file = %d", second.Handle.FileID())
	}
}

func TestPreparedPatchCarriesValidatedSemanticLineIdentity(t *testing.T) {
	prepared, err := TakePatch([]byte(
		"diff --git a/one.go b/one.go\n" +
			"--- a/one.go\n" +
			"+++ b/one.go\n" +
			"@@ -7,2 +7,3 @@ func example() {\n" +
			" same\n" +
			"-old\n" +
			"+new\n" +
			"+more\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	contextLines, _ := NewContextLines(3)
	patchContext, _ := ContextAround(contextLines)
	pending, request, err := current.RequestPatch(0, patchContext)
	if err != nil {
		t.Fatal(err)
	}
	current, err = pending.ApplyPatch(PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    PatchReady,
		Prepared:  prepared,
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := current.PatchLines(0, 3, 5, 4_096)
	if len(lines) != 5 || lines[0].Kind() != PatchLineHunkHeader ||
		lines[1].Kind() != PatchLineContext ||
		lines[2].Kind() != PatchLineDeletion ||
		lines[3].Kind() != PatchLineAddition ||
		lines[4].Kind() != PatchLineAddition {
		t.Fatalf("semantic line kinds = %+v", lines)
	}
	if oldLine, ok := lines[1].OldLine(); !ok || oldLine.Uint32() != 7 {
		t.Fatalf("context old line = %d %v", oldLine, ok)
	}
	if newLine, ok := lines[1].NewLine(); !ok || newLine.Uint32() != 7 {
		t.Fatalf("context new line = %d %v", newLine, ok)
	}
	if _, ok := lines[2].NewLine(); ok {
		t.Fatal("deletion unexpectedly has a new-side line")
	}
	if newLine, ok := lines[4].NewLine(); !ok || newLine.Uint32() != 9 {
		t.Fatalf("last addition new line = %d %v", newLine, ok)
	}
	if hunk, ok := lines[4].Hunk(0); !ok || hunk.Index().Uint32() != 0 {
		t.Fatalf("last addition hunk = %+v %v", hunk, ok)
	}
	state := current.Patch(0)
	if oldLine, ok := state.MaxLineNumber(LineOld); !ok || oldLine.Uint32() != 8 {
		t.Fatalf("maximum old line = %d %v", oldLine, ok)
	}
	if newLine, ok := state.MaxLineNumber(LineNew); !ok || newLine.Uint32() != 9 {
		t.Fatalf("maximum new line = %d %v", newLine, ok)
	}
}

func TestPreparedPatchNoNewlineMarkersOwnPrecedingSides(t *testing.T) {
	prepared, err := TakePatch([]byte(
		"@@ -1 +1 @@\n" +
			"-old\n" +
			noNewlineMarkerText + "\n" +
			"+new\n" +
			noNewlineMarkerText + "\n" +
			"@@ -3 +3 @@\n" +
			" same\n" +
			noNewlineMarkerText + "\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	lines := patchLinesFromPrepared(t, prepared)
	for _, test := range []struct {
		name    string
		index   int
		oldSide bool
		newSide bool
	}{
		{name: "deletion", index: 2, oldSide: true},
		{name: "addition", index: 4, newSide: true},
		{name: "context", index: 7, oldSide: true, newSide: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			line := lines[test.index]
			if line.Kind() != PatchLineNoNewline {
				t.Fatalf("marker kind = %d, want %d", line.Kind(), PatchLineNoNewline)
			}
			if line.AppliesToSide(LineOld) != test.oldSide ||
				line.AppliesToSide(LineNew) != test.newSide {
				t.Fatalf(
					"marker sides = old %t new %t, want old %t new %t",
					line.AppliesToSide(LineOld),
					line.AppliesToSide(LineNew),
					test.oldSide,
					test.newSide,
				)
			}
			if line.AppliesToSide(0) || line.AppliesToSide(LineSide(255)) {
				t.Fatal("marker applies to an invalid side")
			}
			if prepared.lineKinds[test.index] ==
				plainPatchLineDescriptor(PatchLineNoNewline) {
				t.Fatal("marker side membership was not packed into lineKinds")
			}
		})
	}
	if lines[1].AppliesToSide(LineOld) || lines[1].AppliesToSide(LineNew) {
		t.Fatal("material line unexpectedly carries marker side membership")
	}
}

func TestTakePatchRejectsInvalidNoNewlineMarkerPlacement(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
	}{
		{
			name: "orphan outside hunk",
			text: noNewlineMarkerText + "\n",
		},
		{
			name: "orphan after hunk header",
			text: "@@ -0,0 +0,0 @@\n" + noNewlineMarkerText + "\n",
		},
		{
			name: "repeated marker",
			text: "@@ -1 +1 @@\n same\n" + noNewlineMarkerText + "\n" +
				noNewlineMarkerText + "\n",
		},
		{
			name: "canonical prefix with trailing bytes",
			text: "@@ -1 +1 @@\n same\n" + noNewlineMarkerText + " trailing\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := TakePatch([]byte(test.text)); err == nil {
				t.Fatal("invalid no-newline marker was accepted")
			}
		})
	}
}

func TestTakePatchRejectsInconsistentHunkRanges(t *testing.T) {
	if _, err := TakePatch([]byte("@@ -1,2 +1,2 @@\n-old\n+new\n")); err == nil {
		t.Fatal("incomplete hunk was accepted")
	}
}

func TestTakePatchCancellableStopsDuringDenseLineScan(t *testing.T) {
	cause := errors.New("stop patch preparation")
	checks := 0
	_, err := TakePatchCancellable(func() error {
		checks++
		if checks >= 3 {
			return cause
		}
		return nil
	}, bytes.Repeat([]byte{'x'}, 8*patchPollBytes))
	if !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
	if checks != 3 {
		t.Fatalf("cancellation checks = %d, want 3", checks)
	}
}

func TestTakePatchDelegatesToCancellablePreparation(t *testing.T) {
	text := []byte("@@ -1 +1 @@\n-old\n+new\n")
	prepared, err := TakePatch(text)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Lines() != 3 || prepared.Hunks() != 1 {
		t.Fatalf("prepared patch = %d lines, %d hunks", prepared.Lines(), prepared.Hunks())
	}
}

func patchLinesFromPrepared(t *testing.T, prepared *PreparedPatch) []PatchLine {
	t.Helper()
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	pending, request, err := current.RequestPatch(0, WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	ready, err := pending.ApplyPatch(PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    PatchReady,
		Prepared:  prepared,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ready.PatchLines(0, 0, prepared.Lines(), MaxPatchBytes)
}
