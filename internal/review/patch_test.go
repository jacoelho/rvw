package review

import (
	"strings"
	"testing"
)

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

func TestTakePatchRejectsInconsistentHunkRanges(t *testing.T) {
	if _, err := TakePatch([]byte("@@ -1,2 +1,2 @@\n-old\n+new\n")); err == nil {
		t.Fatal("incomplete hunk was accepted")
	}
}
