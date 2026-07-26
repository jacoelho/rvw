package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/source"
)

func TestFileContextOverridesRemainBoundedAndIsolated(t *testing.T) {
	var overrides fileContextOverrides
	first, _ := change.NewFileID(0)
	last, _ := change.NewFileID(change.MaxFiles - 1)
	if !overrides.toggle(first) || !overrides.has(first) || overrides.has(last) {
		t.Fatal("first override did not remain isolated")
	}
	if !overrides.toggle(last) || !overrides.has(last) {
		t.Fatal("last valid override was not retained")
	}
	wantWords := (change.MaxFiles + fileContextWordBits - 1) / fileContextWordBits
	if len(overrides.words) != wantWords || cap(overrides.words) != wantWords {
		t.Fatalf("override words = len %d cap %d, want %d", len(overrides.words), cap(overrides.words), wantWords)
	}
	invalid := change.FileID(change.MaxFiles)
	if overrides.toggle(invalid) || overrides.has(invalid) || len(overrides.words) != wantWords {
		t.Fatal("invalid file identity changed the bounded override set")
	}
	if overrides.toggle(first) || overrides.has(first) || !overrides.has(last) {
		t.Fatal("collapsing one file changed another override")
	}
	overrides.clear()
	if len(overrides.words) != 0 || overrides.has(last) {
		t.Fatal("clear retained per-file context state")
	}
}

func TestEffectivePatchContextAndGlobalContextReset(t *testing.T) {
	lines, _ := change.NewContextLines(3)
	configured, _ := change.ContextAround(lines)
	model := &Model{settings: Settings{patchContext: configured}}
	file, _ := change.NewFileID(7)
	other, _ := change.NewFileID(8)

	if model.effectivePatchContext(file).WholeFile() {
		t.Fatal("configured context unexpectedly resolved as whole-file")
	}
	model.fullFileOverrides.toggle(file)
	if !model.effectivePatchContext(file).WholeFile() || model.effectivePatchContext(other).WholeFile() {
		t.Fatal("per-file context leaked to another file")
	}
	model.applyPatchContext(configured)
	if model.effectivePatchContext(file).WholeFile() || len(model.fullFileOverrides.words) != 0 {
		t.Fatal("review-wide context change retained a per-file override")
	}
}

func TestToggleFullFileIsDiffFocusedAndWholeFileConfigurationIsNoOp(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	setModelKeybindings(t, model, keymap.Binding{
		Context: keymap.ContextNormal,
		Focus:   keymap.FocusDiff,
		Chord:   "z e",
		Action:  keymap.ActionToggleFullFile,
	})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}

	model.focus = FocusFileTree
	_, _ = model.Update(keyMessage("z"))
	_, _ = model.Update(keyMessage("e"))
	if model.fullFileOverrides.has(active) {
		t.Fatal("tree-focused chord changed diff context")
	}

	model.focus = FocusDiff
	model.settings.patchContext = change.WholeFileContext()
	accepted := coordinator.Stats().Accepted
	_, _ = model.Update(keyMessage("z"))
	_, _ = model.Update(keyMessage("e"))
	if model.fullFileOverrides.has(active) || coordinator.Stats().Accepted != accepted {
		t.Fatal("review-wide whole-file context issued redundant patch work")
	}
	if model.notice == nil || model.notice.Text != wholeFileContextActiveText {
		t.Fatalf("whole-file no-op notice = %+v", model.notice)
	}
}

func TestToggleFullFilePersistsAcrossNavigationAndCollapsesIndependently(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "alpha changed\n",
		"beta.txt":  "beta changed\n",
	})
	settleModelWork(t, model, coordinator)
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	var other change.FileID
	for file := range model.review.Snapshot().AllFiles() {
		if file.ID() != active {
			other = file.ID()
			break
		}
	}
	if other == active {
		t.Fatal("missing second file")
	}

	model.toggleFullFileContext()
	if !model.fullFileOverrides.has(active) || !model.effectivePatchContext(active).WholeFile() {
		t.Fatal("expansion intent was not retained for the active file")
	}
	settleModelWork(t, model, coordinator)
	if !model.activateFile(other) {
		t.Fatal("could not navigate to second file")
	}
	model.requestActivePatch()
	settleModelWork(t, model, coordinator)
	if !model.fullFileOverrides.has(active) || model.effectivePatchContext(other).WholeFile() {
		t.Fatal("navigation lost or leaked the first file's expansion")
	}
	if !model.activateFile(active) {
		t.Fatal("could not return to expanded file")
	}
	model.requestActivePatch()
	settleModelWork(t, model, coordinator)
	if !model.effectivePatchContext(active).WholeFile() {
		t.Fatal("returning to the file lost its expansion")
	}

	model.toggleFullFileContext()
	if model.fullFileOverrides.has(active) || model.effectivePatchContext(active).WholeFile() {
		t.Fatal("second toggle did not restore configured context")
	}
	settleModelWork(t, model, coordinator)
}

func TestRapidFullFileTogglesSettleToLatestIntentAndRestoreSourceLine(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	active, _ := model.review.ActiveFile()
	line, _ := change.NewLineNumber(1)
	row, ok := model.review.PatchLineForSource(active, change.LineNew, line)
	if !ok {
		t.Fatal("fixture new-side line is unavailable")
	}
	model.diffCursor = row

	model.toggleFullFileContext()
	model.toggleFullFileContext()
	model.toggleFullFileContext()
	if !model.fullFileOverrides.has(active) {
		t.Fatal("rapid toggles did not retain the final expanded intent")
	}
	settleModelWork(t, model, coordinator)
	want, ok := model.review.PatchLineForSource(active, change.LineNew, line)
	if !ok || model.diffCursor != want || model.foreground.patch.target != nil ||
		model.foreground.patchCount() != 0 {
		t.Fatalf(
			"settled toggle = cursor %d want %d/%v target %v patches %d",
			model.diffCursor,
			want,
			ok,
			model.foreground.patch.target,
			model.foreground.patchCount(),
		)
	}
}

func TestFailedAndLimitedExpansionIntentSurvivesForRetry(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		failure error
		status  change.PatchStatus
	}{
		{name: "failed", failure: errors.New("patch failed"), status: change.PatchFailed},
		{name: "limited", failure: source.ErrSourceLimited, status: change.PatchLimited},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
			settleModelWork(t, model, coordinator)
			active, _ := model.review.ActiveFile()
			model.fullFileOverrides.toggle(active)
			start, _, ready := model.foreground.requestPatch(active)
			if !ready {
				t.Fatal("synthetic expansion was not ready")
			}
			pending, _, err := model.review.RequestPatch(active, model.effectivePatchContext(active))
			if err != nil {
				t.Fatal(err)
			}
			const operation change.OperationID = 20_000
			pending, err = pending.BeginOperation(operation, change.OperationPatch)
			if err != nil {
				t.Fatal(err)
			}
			if !model.foreground.admitted(start, operation) {
				t.Fatal("synthetic expansion admission failed")
			}
			model.review = pending

			model.applyPatchOutcome(PatchOutcome{ID: operation, Err: testCase.failure})
			if model.review.Patch(active).Status != testCase.status ||
				!model.fullFileOverrides.has(active) ||
				!model.effectivePatchContext(active).WholeFile() {
				t.Fatalf(
					"terminal expansion = status %v override %t whole %t",
					model.review.Patch(active).Status,
					model.fullFileOverrides.has(active),
					model.effectivePatchContext(active).WholeFile(),
				)
			}

			model.requestActivePatch()
			if model.review.Patch(active).Status != change.PatchPending {
				t.Fatal("explicit retry did not admit another whole-file patch")
			}
			settleModelWork(t, model, coordinator)
		})
	}
}

func TestCursorSourceTargetPrefersNewSideAndMetadataFallsBack(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	active, _ := model.review.ActiveFile()
	prepared, err := change.TakePatch([]byte(
		"diff --git a/alpha.txt b/alpha.txt\n" +
			"@@ -7,2 +7,2 @@\n" +
			" both\n" +
			"-old\n" +
			"+new\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	pending, request, err := model.review.RequestPatch(active, change.WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = pending.ApplyPatch(change.PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    change.PatchReady,
		Prepared:  prepared,
	})
	if err != nil {
		t.Fatal(err)
	}

	model.diffCursor = 2
	target := model.cursorSourceTarget(active)
	if target == nil || target.kind != diffTargetNearestSourceLine || target.side != change.LineNew || target.line.Uint32() != 7 {
		t.Fatalf("context-row target = %+v", target)
	}
	model.diffCursor = 3
	target = model.cursorSourceTarget(active)
	if target == nil || target.side != change.LineOld || target.line.Uint32() != 8 {
		t.Fatalf("deletion-row target = %+v", target)
	}
	model.diffCursor = 0
	if target = model.cursorSourceTarget(active); target != nil {
		t.Fatalf("metadata-row target = %+v, want nil", target)
	}
}

func TestSearchHitActivationUsesSourceIdentityAcrossPatchContexts(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	settleModelWork(t, model, coordinator)
	active, _ := model.review.ActiveFile()
	configured, err := change.TakePatch([]byte(
		"@@ -49 +49,2 @@\n" +
			" unchanged\n" +
			"+needle\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	replaceReadyPatch(t, model, active, configured)
	query, _ := change.NewSearchQuery("needle")
	batch := change.ScanSearchBatch(configured, active, change.PrepareSearchQuery(query), change.SearchCursor{})
	hit, ok := batch.Hit(0)
	if !ok {
		t.Fatal("configured patch produced no search hit")
	}
	const operation change.OperationID = 10_000
	searching, _, err := model.review.StartSearch(operation, query, model.settings.patchContext)
	if err != nil {
		t.Fatal(err)
	}
	result, err := change.TakeSearchResult(change.SearchResultDescriptor{
		Operation: operation,
		Workspace: searching.WorkspaceRevision(),
		Source:    searching.Snapshot().SourceID(),
		Status:    change.SearchComplete,
	}, []change.SearchHit{hit})
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = searching.ApplySearch(result)
	if err != nil {
		t.Fatal(err)
	}

	var expanded strings.Builder
	expanded.WriteString("@@ -1,49 +1,50 @@\n")
	for line := 1; line <= 49; line++ {
		expanded.WriteString(" context\n")
	}
	expanded.WriteString("+needle\n")
	whole, err := change.TakePatch([]byte(expanded.String()))
	if err != nil {
		t.Fatal(err)
	}
	replaceReadyPatch(t, model, active, whole)
	want, ok := model.review.PatchLineForSource(active, change.LineNew, 50)
	if !ok || want == hit.PatchLine() {
		t.Fatalf("expanded source row = %d, %v; configured patch row = %d", want, ok, hit.PatchLine())
	}

	model.activateSearchHit(0)
	if model.diffCursor != want {
		t.Fatalf("search activation row = %d, want source-identity row %d", model.diffCursor, want)
	}
}

func replaceReadyPatch(
	t *testing.T,
	model *Model,
	file change.FileID,
	prepared *change.PreparedPatch,
) {
	t.Helper()
	pending, request, err := model.review.RequestPatch(file, change.WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = pending.ApplyPatch(change.PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    change.PatchReady,
		Prepared:  prepared,
	})
	if err != nil {
		t.Fatal(err)
	}
}
