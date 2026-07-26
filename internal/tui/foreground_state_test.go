package tui

import (
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func TestForegroundDiffTargetFollowsTheLatestTrackedPatch(t *testing.T) {
	state := newForegroundState()
	first := testSourceLineTarget(t, 0, change.LineNew, 3)
	if !state.setPendingDiffTarget(first) {
		t.Fatal("first target was rejected")
	}
	start, _, ready := state.requestPatch(0)
	if !ready || !state.admitted(start, 1) {
		t.Fatal("first patch admission failed")
	}
	assertForegroundValid(t, &state)

	second := testSourceLineTarget(t, 1, change.LineOld, 8)
	if !state.setPendingDiffTarget(second) {
		t.Fatal("replacement target was rejected")
	}
	_, cancellations, ready := state.requestPatch(1)
	if ready || len(cancellations) != 1 || cancellations[0] != 1 {
		t.Fatalf("replacement cancellation = %v, ready %v", cancellations, ready)
	}
	_, completed, tracked, effect := state.completePatch(1)
	if !tracked || completed != nil || effect.kind != foregroundEffectRequestPatch || effect.file != 1 {
		t.Fatalf("superseded completion = target %+v tracked %v effect %+v", completed, tracked, effect)
	}
	assertForegroundValid(t, &state)

	retry, ok := state.preparePatch(effect.file)
	if !ok || !state.admitted(retry, 2) {
		t.Fatal("replacement patch admission failed")
	}
	_, completed, tracked, effect = state.completePatch(2)
	if !tracked || effect.kind != foregroundEffectNone || completed == nil ||
		completed.kind != diffTargetSourceLine || completed.handle.FileID() != 1 ||
		completed.side != change.LineOld || completed.line.Uint32() != 8 {
		t.Fatalf("replacement completion = target %+v tracked %v effect %+v", completed, tracked, effect)
	}
	assertForegroundValid(t, &state)
}

func TestForegroundDiffTargetOwnershipAcrossPatchRetries(t *testing.T) {
	t.Run("same-file retry retains and rebinds completed target", func(t *testing.T) {
		state := newForegroundState()
		target := testSourceLineTarget(t, 0, change.LineNew, 3)
		if !state.setPendingDiffTarget(target) {
			t.Fatal("target was rejected")
		}
		start, _, ready := state.requestPatch(0)
		if !ready || !state.admitted(start, 1) {
			t.Fatal("initial patch admission failed")
		}
		_, cancellations, ready := state.requestPatch(0)
		if ready || len(cancellations) != 1 || cancellations[0] != 1 {
			t.Fatalf("retry cancellation = %v, ready %v", cancellations, ready)
		}
		_, completed, tracked, effect := state.completePatch(1)
		if !tracked || completed == nil || effect.kind != foregroundEffectRequestPatch ||
			state.patch.target == nil || state.patch.target.operation != 0 {
			t.Fatalf(
				"first completion = target %+v retained %+v tracked %v effect %+v",
				completed,
				state.patch.target,
				tracked,
				effect,
			)
		}
		retry, ok := state.preparePatch(effect.file)
		if !ok || !state.admitted(retry, 2) || state.patch.target.operation != 2 {
			t.Fatalf("retry admission = %+v, ready %v, target %+v", retry, ok, state.patch.target)
		}
		_, completed, tracked, effect = state.completePatch(2)
		if !tracked || completed == nil || effect.kind != foregroundEffectNone ||
			state.patch.target != nil {
			t.Fatalf(
				"retry completion = target %+v retained %+v tracked %v effect %+v",
				completed,
				state.patch.target,
				tracked,
				effect,
			)
		}
		assertForegroundValid(t, &state)
	})

	t.Run("different-file retry discards completed target", func(t *testing.T) {
		state := newForegroundState()
		target := testSourceLineTarget(t, 0, change.LineOld, 8)
		if !state.setPendingDiffTarget(target) {
			t.Fatal("target was rejected")
		}
		start, _, ready := state.requestPatch(0)
		if !ready || !state.admitted(start, 1) {
			t.Fatal("initial patch admission failed")
		}
		_, _, _ = state.requestPatch(1)
		_, completed, tracked, effect := state.completePatch(1)
		if !tracked || completed == nil || effect.kind != foregroundEffectRequestPatch ||
			effect.file != 1 || state.patch.target != nil {
			t.Fatalf(
				"completion = target %+v retained %+v tracked %v effect %+v",
				completed,
				state.patch.target,
				tracked,
				effect,
			)
		}
		assertForegroundValid(t, &state)
	})

	t.Run("newer queued target is not overwritten", func(t *testing.T) {
		state := newForegroundState()
		first := testSourceLineTarget(t, 0, change.LineNew, 3)
		if !state.setPendingDiffTarget(first) {
			t.Fatal("first target was rejected")
		}
		start, _, ready := state.requestPatch(0)
		if !ready || !state.admitted(start, 1) {
			t.Fatal("initial patch admission failed")
		}
		newer := testSourceLineTarget(t, 0, change.LineNew, 9)
		if !state.setPendingDiffTarget(newer) {
			t.Fatal("newer target was rejected")
		}
		_, _, _ = state.requestPatch(0)
		_, completed, tracked, effect := state.completePatch(1)
		if !tracked || completed != nil || effect.kind != foregroundEffectRequestPatch ||
			state.patch.target == nil || state.patch.target.line != newer.line ||
			state.patch.target.operation != 0 {
			t.Fatalf(
				"completion = target %+v retained %+v tracked %v effect %+v",
				completed,
				state.patch.target,
				tracked,
				effect,
			)
		}
		assertForegroundValid(t, &state)
	})
}

func TestForegroundDiffTargetSurvivesFailedReloadAndNotSuccessfulReload(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		succeed bool
	}{
		{name: "failed reload"},
		{name: "successful reload", succeed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			state := newForegroundState()
			target := testSourceLineTarget(t, 0, change.LineNew, 4)
			if !state.setPendingDiffTarget(target) {
				t.Fatal("target was rejected")
			}
			patchStart, _, ready := state.requestPatch(0)
			if !ready || !state.admitted(patchStart, 1) {
				t.Fatal("patch admission failed")
			}
			_, cancellations, ready := state.requestReload()
			if ready || len(cancellations) != 1 || cancellations[0] != 1 {
				t.Fatalf("reload cancellation = %v, ready %v", cancellations, ready)
			}
			_, completed, tracked, effect := state.completePatch(1)
			if !tracked || completed != nil || effect.kind != foregroundEffectStartReload {
				t.Fatalf("cancelled patch completion = target %+v tracked %v effect %+v", completed, tracked, effect)
			}
			reloadStart, ok := state.prepareReload()
			if !ok || !state.admitted(reloadStart, 2) {
				t.Fatal("reload admission failed")
			}
			if testCase.succeed {
				if !state.reloadCaptureStarted(2) ||
					!state.reloadPreparationAdmitted(2, 0) {
					t.Fatal("reload preparation admission failed")
				}
				if _, ok := state.finishReloadSuccess(2, nil, reloadNeedsPatch); !ok {
					t.Fatal("reload success was rejected")
				}
				if state.patch.target != nil {
					t.Fatal("successful reload retained a navigation target")
				}
				return
			}
			if _, _, ok := state.finishReloadFailure(2); !ok {
				t.Fatal("reload failure was rejected")
			}
			retry, _, ready := state.requestPatch(0)
			if !ready || !state.admitted(retry, 3) {
				t.Fatal("restored patch admission failed")
			}
			_, completed, tracked, _ = state.completePatch(3)
			if !tracked || completed == nil || completed.line.Uint32() != 4 {
				t.Fatalf("restored completion = target %+v tracked %v", completed, tracked)
			}
		})
	}
}

func testSourceLineTarget(
	t *testing.T,
	fileValue uint32,
	side change.LineSide,
	lineValue uint32,
) pendingDiffTarget {
	t.Helper()
	workspace, _ := change.NewWorkspaceRevision(1)
	source, _ := change.NewSourceID(1)
	file, _ := change.NewFileID(fileValue)
	patch, _ := change.NewPatchID(uint64(fileValue) + 1)
	handle, err := change.NewPatchHandle(patch, source, file)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := change.NewLineNumber(lineValue)
	return newSourceLineTarget(workspace, handle, side, line)
}

func TestForegroundStatePatchRetryAdmission(t *testing.T) {
	state := newForegroundState()
	assertForegroundValid(t, &state)

	start, cancellations, ready := state.requestPatch(1)
	if !ready || len(cancellations) != 0 || start.source != foregroundStartDirect {
		t.Fatalf("direct patch decision = %+v, cancellations %v, ready %v", start, cancellations, ready)
	}
	if state.patchCount() != 0 {
		t.Fatal("preparing a patch mutated active state")
	}
	if !state.admitted(start, 1) {
		t.Fatal("direct patch admission failed")
	}
	assertForegroundValid(t, &state)

	_, cancellations, ready = state.requestPatch(2)
	if ready || len(cancellations) != 1 || cancellations[0] != 1 {
		t.Fatalf("patch retry cancellation = %v, ready %v", cancellations, ready)
	}
	_, _, tracked, effect := state.completePatch(1)
	if !tracked || effect.kind != foregroundEffectRequestPatch || effect.file != 2 {
		t.Fatalf("patch completion effect = %+v, tracked %v", effect, tracked)
	}
	assertForegroundValid(t, &state)

	retry, ok := state.preparePatch(effect.file)
	if !ok || retry.source != foregroundStartPatchRetry {
		t.Fatalf("retry decision = %+v, ready %v", retry, ok)
	}
	if !state.admitted(retry, 2) {
		t.Fatal("patch retry admission failed")
	}
	if state.patchCount() != 1 {
		t.Fatalf("active patches = %d, want 1", state.patchCount())
	}
	assertForegroundValid(t, &state)
}

func TestForegroundStateSearchReplacementSupersedesQueuedPatch(t *testing.T) {
	state := newForegroundState()
	first := testPendingSearch(t, "first")
	replacement := testPendingSearch(t, "replacement")

	searchStart, _, ready := state.requestSearch(first)
	if !ready || !state.admitted(searchStart, 1) {
		t.Fatal("initial search admission failed")
	}
	patchStart, _, ready := state.requestPatch(1)
	if ready || patchStart.kind != foregroundStartNone {
		t.Fatal("patch did not wait for the active search")
	}
	_, cancellations, ready := state.requestSearch(replacement)
	if ready || len(cancellations) != 1 || cancellations[0] != 1 {
		t.Fatalf("replacement cancellation = %v, ready %v", cancellations, ready)
	}
	tracked, searchEffect := state.completeSearch(1)
	if !tracked || searchEffect.kind != foregroundEffectStartSearch ||
		searchEffect.search != replacement {
		t.Fatalf("replacement effect = %+v, tracked %v", searchEffect, tracked)
	}
	replacementStart, ok := state.prepareSearch(searchEffect.search)
	if !ok || replacementStart.source != foregroundStartReplacementSearch {
		t.Fatalf("replacement decision = %+v, ready %v", replacementStart, ok)
	}
	if !state.admitted(replacementStart, 3) {
		t.Fatal("replacement search admission failed")
	}
	assertForegroundValid(t, &state)
}

func TestForegroundStatePatchStartsAfterSearchCompletion(t *testing.T) {
	state := newForegroundState()
	searchStart, _, ready := state.requestSearch(testPendingSearch(t, "active"))
	if !ready || !state.admitted(searchStart, 1) {
		t.Fatal("search admission failed")
	}
	patchStart, cancellations, ready := state.requestPatch(2)
	if ready || patchStart.kind != foregroundStartNone || len(cancellations) != 1 ||
		cancellations[0] != 1 {
		t.Fatalf("queued patch = start %+v cancellations %v ready %v", patchStart, cancellations, ready)
	}
	tracked, effect := state.completeSearch(1)
	if !tracked || effect.kind != foregroundEffectRequestPatch || effect.file != 2 {
		t.Fatalf("search completion = tracked %v effect %+v", tracked, effect)
	}
	start, ok := state.preparePatch(effect.file)
	if !ok || !state.admitted(start, 2) {
		t.Fatal("deferred patch admission failed")
	}
	assertForegroundValid(t, &state)
}

func TestForegroundStateSearchContinuationPrecedence(t *testing.T) {
	t.Run("clear discards search queued behind patch", func(t *testing.T) {
		state := newForegroundState()
		patchStart, _, ready := state.requestPatch(0)
		if !ready || !state.admitted(patchStart, 1) {
			t.Fatal("patch admission failed")
		}
		pending := testPendingSearch(t, "queued")
		_, cancellations, ready := state.requestSearch(pending)
		if ready || len(cancellations) != 1 || cancellations[0] != 1 {
			t.Fatalf("queued search cancellations=%v ready=%v", cancellations, ready)
		}
		if operation, active := state.requestClearSearch(); active || operation != 0 {
			t.Fatalf("queued search clear operation=%d active=%v", operation, active)
		}
		_, _, tracked, effect := state.completePatch(1)
		if !tracked || effect.kind != foregroundEffectNone {
			t.Fatalf("cleared queued search produced effect %+v tracked=%v", effect, tracked)
		}
		assertForegroundValid(t, &state)
	})

	t.Run("clear replaces replacement", func(t *testing.T) {
		state, operation := activeForegroundSearch(t)
		replacement := testPendingSearch(t, "replacement")
		_, _, _ = state.requestSearch(replacement)
		cancelled, active := state.requestClearSearch()
		if !active || cancelled != operation {
			t.Fatalf("clear cancellation = %d, active %v", cancelled, active)
		}
		_, effect := state.completeSearch(operation)
		if effect.kind != foregroundEffectClearSearch {
			t.Fatalf("clear effect = %+v", effect)
		}
		assertForegroundValid(t, &state)
	})

	t.Run("replacement replaces clear", func(t *testing.T) {
		state, operation := activeForegroundSearch(t)
		_, _ = state.requestClearSearch()
		replacement := testPendingSearch(t, "replacement")
		_, _, _ = state.requestSearch(replacement)
		_, effect := state.completeSearch(operation)
		if effect.kind != foregroundEffectStartSearch || effect.search != replacement {
			t.Fatalf("replacement effect = %+v", effect)
		}
		assertForegroundValid(t, &state)
	})

	t.Run("patch context is sticky", func(t *testing.T) {
		state, operation := activeForegroundSearch(t)
		context := change.WholeFileContext()
		_, _, _ = state.requestSearch(testPendingSearch(t, "discarded"))
		cancelled, active := state.requestPatchContext(context)
		if !active || cancelled != operation {
			t.Fatalf("context cancellation = %d, active %v", cancelled, active)
		}
		_, _, _ = state.requestSearch(testPendingSearch(t, "also discarded"))
		_, _ = state.requestClearSearch()
		patchStart, cancellations, ready := state.requestPatch(2)
		if ready || patchStart.kind != foregroundStartNone || len(cancellations) != 1 ||
			cancellations[0] != operation {
			t.Fatalf(
				"patch during context cancellation = start %+v cancellations %v ready %v",
				patchStart, cancellations, ready,
			)
		}
		_, effect := state.completeSearch(operation)
		if effect.kind != foregroundEffectApplyPatchContext ||
			effect.context != context {
			t.Fatalf("patch-context effect = %+v", effect)
		}
		assertForegroundValid(t, &state)
	})
}

func TestForegroundStatePatchSearchReloadFailureResumesSearch(t *testing.T) {
	state := newForegroundState()
	patchStart, _, ready := state.requestPatch(1)
	if !ready || !state.admitted(patchStart, 1) {
		t.Fatal("patch admission failed")
	}
	pending := testPendingSearch(t, "after reload")
	_, cancellations, ready := state.requestSearch(pending)
	if ready || len(cancellations) != 1 || cancellations[0] != 1 {
		t.Fatalf("search wait = cancellations %v ready %v", cancellations, ready)
	}
	_, cancellations, ready = state.requestReload()
	if ready || len(cancellations) != 1 || cancellations[0] != 1 {
		t.Fatalf("reload wait = cancellations %v ready %v", cancellations, ready)
	}
	assertForegroundValid(t, &state)

	_, _, tracked, effect := state.completePatch(1)
	if !tracked || effect.kind != foregroundEffectStartReload {
		t.Fatalf("patch-to-reload effect = %+v, tracked %v", effect, tracked)
	}
	reloadStart, ok := state.prepareReload()
	if !ok || reloadStart.source != foregroundStartWaitingReload ||
		!state.admitted(reloadStart, 2) {
		t.Fatalf("deferred reload decision = %+v, ready %v", reloadStart, ok)
	}
	_, effect, tracked = state.finishReloadFailure(2)
	if !tracked || effect.kind != foregroundEffectStartSearch || effect.search != pending {
		t.Fatalf("reload failure effect = %+v, tracked %v", effect, tracked)
	}
	searchStart, ok := state.prepareSearch(effect.search)
	if !ok || !state.admitted(searchStart, 3) {
		t.Fatal("post-failure search admission failed")
	}
	assertForegroundValid(t, &state)
}

func TestForegroundStatePostReloadPatchRejectionResumesSearch(t *testing.T) {
	state, pending, reloadID := foregroundReloadWithSearch(t)
	if !state.reloadCaptureStarted(reloadID) ||
		!state.reloadPreparationAdmitted(reloadID, 0) {
		t.Fatal("reload preparation admission failed")
	}
	effect, tracked := state.finishReloadSuccess(reloadID, nil, reloadNeedsPatch)
	if !tracked || effect.kind != foregroundEffectNone {
		t.Fatalf("reload success = effect %+v, tracked %v", effect, tracked)
	}
	patchStart, ok := state.preparePatch(1)
	if !ok || patchStart.source != foregroundStartPostReloadPatch {
		t.Fatalf("post-reload patch decision = %+v, ready %v", patchStart, ok)
	}
	effect, rejected := state.rejected(patchStart)
	if !rejected || effect.kind != foregroundEffectStartSearch || effect.search != pending {
		t.Fatalf("post-reload patch rejection effect = %+v", effect)
	}
	searchStart, ok := state.prepareSearch(effect.search)
	if !ok || !state.admitted(searchStart, 3) {
		t.Fatal("post-patch-rejection search admission failed")
	}
	assertForegroundValid(t, &state)
}

func TestForegroundStateRejectsOnlyCurrentAdmissionDecision(t *testing.T) {
	t.Run("patch retry", func(t *testing.T) {
		state := newForegroundState()
		start, _, ready := state.requestPatch(1)
		if !ready || !state.admitted(start, 1) {
			t.Fatal("initial patch admission failed")
		}
		_, cancellations, ready := state.requestPatch(2)
		if ready || len(cancellations) != 1 {
			t.Fatalf("patch retry cancellation = %v, ready %v", cancellations, ready)
		}
		_, _, tracked, effect := state.completePatch(1)
		if !tracked || effect.kind != foregroundEffectRequestPatch {
			t.Fatalf("patch retry effect = %+v, tracked %v", effect, tracked)
		}
		stale, ok := state.preparePatch(2)
		if !ok {
			t.Fatal("stale retry decision was not prepared")
		}
		state.patch.continuation = patchContinuation{
			kind: patchContinuationRetry,
			file: 3,
		}
		if _, rejected := state.rejected(stale); rejected {
			t.Fatal("stale patch retry decision was accepted for rejection")
		}
		if continuation := state.patch.continuation; continuation.kind != patchContinuationRetry ||
			continuation.file != 3 {
			t.Fatalf("newer patch continuation was consumed: %+v", continuation)
		}
		assertForegroundValid(t, &state)
	})

	t.Run("replacement search", func(t *testing.T) {
		state, operation := activeForegroundSearch(t)
		first := testPendingSearch(t, "first replacement")
		_, _, _ = state.requestSearch(first)
		_, effect := state.completeSearch(operation)
		stale, ok := state.prepareSearch(effect.search)
		if !ok {
			t.Fatal("stale replacement decision was not prepared")
		}
		newer := testPendingSearch(t, "newer replacement")
		state.search.continuation = searchContinuation{
			kind:   searchContinuationReplace,
			search: newer,
		}
		if _, rejected := state.rejected(stale); rejected {
			t.Fatal("stale replacement-search decision was accepted for rejection")
		}
		if continuation := state.search.continuation; continuation.kind != searchContinuationReplace ||
			continuation.search != newer {
			t.Fatalf("newer search continuation was consumed: %+v", continuation)
		}
		assertForegroundValid(t, &state)
	})
}

func TestForegroundStateReloadWithoutActiveFileResumesSearch(t *testing.T) {
	state, pending, reloadID := foregroundReloadWithSearch(t)
	if !state.reloadCaptureStarted(reloadID) ||
		!state.reloadPreparationAdmitted(reloadID, 0) {
		t.Fatal("reload preparation admission failed")
	}
	effect, tracked := state.finishReloadSuccess(reloadID, nil, reloadNoPatch)
	if !tracked || effect.kind != foregroundEffectStartSearch || effect.search != pending {
		t.Fatalf("reload completion effect = %+v, tracked %v", effect, tracked)
	}
	assertForegroundValid(t, &state)
}

func TestForegroundStateReloadRejectsNewSearchAndIgnoresStaleResults(t *testing.T) {
	state := newForegroundState()
	start, _, ready := state.requestReload()
	if !ready || !state.admitted(start, 1) {
		t.Fatal("reload admission failed")
	}
	if _, cancellations, ready := state.requestSearch(
		testPendingSearch(t, "rejected"),
	); ready || len(cancellations) != 0 {
		t.Fatalf("search during reload = cancellations %v, ready %v", cancellations, ready)
	}
	if _, _, tracked := state.finishReloadFailure(2); tracked {
		t.Fatal("stale reload failure consumed the active reload")
	}
	if !state.reloadAcquired(1) || !state.reloadCaptureStarted(1) ||
		!state.reloadPreparationAdmitted(1, 0) {
		t.Fatal("reload did not enter preparation")
	}
	if effect, tracked := state.finishReloadSuccess(2, nil, reloadNoPatch); tracked ||
		effect.kind != foregroundEffectNone {
		t.Fatalf("stale reload success = effect %+v, tracked %v", effect, tracked)
	}
	if !state.reloadPrepared(1) {
		t.Fatal("stale result changed the active reload")
	}
	assertForegroundValid(t, &state)
}

func activeForegroundSearch(t *testing.T) (foregroundState, change.OperationID) {
	t.Helper()
	state := newForegroundState()
	start, _, ready := state.requestSearch(testPendingSearch(t, "active"))
	const operation change.OperationID = 1
	if !ready || !state.admitted(start, operation) {
		t.Fatal("search admission failed")
	}
	return state, operation
}

func foregroundReloadWithSearch(
	t *testing.T,
) (foregroundState, pendingSearch, change.OperationID) {
	t.Helper()
	state := newForegroundState()
	patchStart, _, ready := state.requestPatch(1)
	if !ready || !state.admitted(patchStart, 1) {
		t.Fatal("patch admission failed")
	}
	pending := testPendingSearch(t, "after reload")
	_, _, _ = state.requestSearch(pending)
	_, _, _ = state.requestReload()
	_, _, _, effect := state.completePatch(1)
	if effect.kind != foregroundEffectStartReload {
		t.Fatalf("reload effect = %+v", effect)
	}
	start, ready := state.prepareReload()
	const reloadID change.OperationID = 2
	if !ready || !state.admitted(start, reloadID) {
		t.Fatal("reload admission failed")
	}
	return state, pending, reloadID
}

func testPendingSearch(t *testing.T, value string) pendingSearch {
	t.Helper()
	query, err := change.NewSearchQuery(value)
	if err != nil {
		t.Fatal(err)
	}
	model, _ := newTestModel(t, map[string]string{"search.txt": "changed\n"})
	candidates, err := newSearchCandidates(model.review, []change.FileID{0})
	if err != nil {
		t.Fatal(err)
	}
	return pendingSearch{query: query, candidates: candidates}
}

func assertForegroundValid(t *testing.T, state *foregroundState) {
	t.Helper()
	if !state.valid() {
		t.Fatalf("invalid foreground state: %+v", *state)
	}
}
