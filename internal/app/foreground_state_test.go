package app

import (
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

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
	_, tracked, effect := state.completePatch(1)
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

func TestForegroundStateSearchReplacementWhilePatchActive(t *testing.T) {
	state := newForegroundState()
	first := testPendingSearch(t, "first")
	replacement := testPendingSearch(t, "replacement")

	searchStart, _, ready := state.requestSearch(first)
	if !ready || !state.admitted(searchStart, 1) {
		t.Fatal("initial search admission failed")
	}
	patchStart, _, ready := state.requestPatch(1)
	if !ready || !state.admitted(patchStart, 2) {
		t.Fatal("concurrent patch admission failed")
	}
	_, cancellations, ready := state.requestSearch(replacement)
	if ready || len(cancellations) != 1 || cancellations[0] != 1 {
		t.Fatalf("replacement cancellation = %v, ready %v", cancellations, ready)
	}
	_, tracked, patchEffect := state.completePatch(2)
	if !tracked || patchEffect.kind != foregroundEffectNone {
		t.Fatalf("patch consumed search continuation: tracked %v effect %+v", tracked, patchEffect)
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

func TestForegroundStateConcurrentCompletionOrder(t *testing.T) {
	testCases := []struct {
		name       string
		configure  func(*testing.T, *foregroundState)
		wantPatch  int
		wantSearch bool
	}{
		{name: "none"},
		{
			name: "patch retry",
			configure: func(t *testing.T, state *foregroundState) {
				t.Helper()
				_, cancellations, ready := state.requestPatch(2)
				if ready || len(cancellations) != 1 || cancellations[0] != 2 {
					t.Fatalf("retry cancellation = %v, ready %v", cancellations, ready)
				}
			},
			wantPatch: 1,
		},
		{
			name: "replace search",
			configure: func(t *testing.T, state *foregroundState) {
				t.Helper()
				_, cancellations, ready := state.requestSearch(
					testPendingSearch(t, "replacement"),
				)
				if ready || len(cancellations) != 1 || cancellations[0] != 1 {
					t.Fatalf("replacement cancellation = %v, ready %v", cancellations, ready)
				}
			},
			wantSearch: true,
		},
		{
			name: "clear search",
			configure: func(t *testing.T, state *foregroundState) {
				t.Helper()
				if operation, active := state.requestClearSearch(); !active || operation != 1 {
					t.Fatalf("clear cancellation = %d, active %v", operation, active)
				}
			},
		},
		{
			name: "apply patch context",
			configure: func(t *testing.T, state *foregroundState) {
				t.Helper()
				if operation, active := state.requestPatchContext(
					review.WholeFileContext(),
				); !active || operation != 1 {
					t.Fatalf("context cancellation = %d, active %v", operation, active)
				}
			},
			wantPatch: 1,
		},
	}
	for _, testCase := range testCases {
		for _, searchFirst := range []bool{false, true} {
			order := "patch-first"
			if searchFirst {
				order = "search-first"
			}
			t.Run(testCase.name+"/"+order, func(t *testing.T) {
				state := newForegroundState()
				searchStart, _, ready := state.requestSearch(testPendingSearch(t, "active"))
				if !ready || !state.admitted(searchStart, 1) {
					t.Fatal("search admission failed")
				}
				patchStart, _, ready := state.requestPatch(1)
				if !ready || !state.admitted(patchStart, 2) {
					t.Fatal("patch admission failed")
				}
				if testCase.configure != nil {
					testCase.configure(t, &state)
				}

				nextOperation := review.OperationID(10)
				applyEffect := func(effect foregroundEffect) {
					t.Helper()
					switch effect.kind {
					case foregroundEffectNone, foregroundEffectClearSearch:
					case foregroundEffectRequestPatch:
						start, ok := state.preparePatch(effect.file)
						if !ok || !state.admitted(start, nextOperation) {
							t.Fatalf("patch effect admission failed: %+v", effect)
						}
						nextOperation++
					case foregroundEffectStartSearch:
						start, ok := state.prepareSearch(effect.search)
						if !ok || !state.admitted(start, nextOperation) {
							t.Fatalf("search effect admission failed: %+v", effect)
						}
						nextOperation++
					case foregroundEffectApplyPatchContext:
						start, cancellations, ready := state.requestPatch(2)
						if ready {
							if !state.admitted(start, nextOperation) {
								t.Fatal("context patch admission failed")
							}
							nextOperation++
						} else if len(cancellations) != 1 || cancellations[0] != 2 {
							t.Fatalf("context patch cancellations = %v", cancellations)
						}
					default:
						t.Fatalf("unexpected effect: %+v", effect)
					}
				}
				completePatch := func() {
					t.Helper()
					_, tracked, effect := state.completePatch(2)
					if !tracked {
						t.Fatal("patch completion was not tracked")
					}
					applyEffect(effect)
				}
				completeSearch := func() {
					t.Helper()
					tracked, effect := state.completeSearch(1)
					if !tracked {
						t.Fatal("search completion was not tracked")
					}
					applyEffect(effect)
				}
				if searchFirst {
					completeSearch()
					completePatch()
				} else {
					completePatch()
					completeSearch()
				}
				if state.patchCount() != testCase.wantPatch {
					t.Fatalf("active patches = %d, want %d", state.patchCount(), testCase.wantPatch)
				}
				if active := state.searchOperation() != 0; active != testCase.wantSearch {
					t.Fatalf("active search = %v, want %v", active, testCase.wantSearch)
				}
				assertForegroundValid(t, &state)
			})
		}
	}
}

func TestForegroundStateSearchContinuationPrecedence(t *testing.T) {
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
		context := review.WholeFileContext()
		_, _, _ = state.requestSearch(testPendingSearch(t, "discarded"))
		cancelled, active := state.requestPatchContext(context)
		if !active || cancelled != operation {
			t.Fatalf("context cancellation = %d, active %v", cancelled, active)
		}
		_, _, _ = state.requestSearch(testPendingSearch(t, "also discarded"))
		_, _ = state.requestClearSearch()
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

	_, tracked, effect := state.completePatch(1)
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
	if !state.reloadPreparationAdmitted(reloadID) {
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
		_, tracked, effect := state.completePatch(1)
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
	if !state.reloadPreparationAdmitted(reloadID) {
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
	if !state.reloadAcquired(1) || !state.reloadPreparationAdmitted(1) {
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

func activeForegroundSearch(t *testing.T) (foregroundState, review.OperationID) {
	t.Helper()
	state := newForegroundState()
	start, _, ready := state.requestSearch(testPendingSearch(t, "active"))
	const operation review.OperationID = 1
	if !ready || !state.admitted(start, operation) {
		t.Fatal("search admission failed")
	}
	return state, operation
}

func foregroundReloadWithSearch(
	t *testing.T,
) (foregroundState, pendingSearch, review.OperationID) {
	t.Helper()
	state := newForegroundState()
	patchStart, _, ready := state.requestPatch(1)
	if !ready || !state.admitted(patchStart, 1) {
		t.Fatal("patch admission failed")
	}
	pending := testPendingSearch(t, "after reload")
	_, _, _ = state.requestSearch(pending)
	_, _, _ = state.requestReload()
	_, _, effect := state.completePatch(1)
	if effect.kind != foregroundEffectStartReload {
		t.Fatalf("reload effect = %+v", effect)
	}
	start, ready := state.prepareReload()
	const reloadID review.OperationID = 2
	if !ready || !state.admitted(start, reloadID) {
		t.Fatal("reload admission failed")
	}
	return state, pending, reloadID
}

func testPendingSearch(t *testing.T, value string) pendingSearch {
	t.Helper()
	query, err := review.NewSearchQuery(value)
	if err != nil {
		t.Fatal(err)
	}
	return pendingSearch{query: query, scope: review.SearchAllFiles}
}

func assertForegroundValid(t *testing.T, state *foregroundState) {
	t.Helper()
	if !state.valid() {
		t.Fatalf("invalid foreground state: %+v", *state)
	}
}
