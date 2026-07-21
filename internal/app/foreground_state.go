package app

import "github.com/jacoelho/rvw/internal/review"

type reloadPhase uint8

const (
	reloadIdle reloadPhase = iota
	reloadWaitingForPatch
	reloadAcquiring
	reloadPreparing
)

type patchContinuationKind uint8

const (
	patchContinuationNone patchContinuationKind = iota
	patchContinuationRetry
	patchContinuationBeginSearch
)

type patchContinuation struct {
	kind   patchContinuationKind
	file   review.FileID
	search pendingSearch
}

type searchContinuationKind uint8

const (
	searchContinuationNone searchContinuationKind = iota
	searchContinuationClear
	searchContinuationReplace
	searchContinuationPatchContext
)

type searchContinuation struct {
	kind    searchContinuationKind
	search  pendingSearch
	context review.PatchContext
}

type reloadFlow struct {
	phase       reloadPhase
	operation   review.OperationID
	attempts    uint8
	afterReload *pendingSearch
}

type patchFlow struct {
	active       map[review.OperationID]review.FileID
	continuation patchContinuation
	viewport     *diffViewport
	target       *pendingDiffTarget
}

type searchFlow struct {
	active       review.OperationID
	continuation searchContinuation
}

type diffTargetKind uint8

const (
	diffTargetPatchRow diffTargetKind = iota + 1
	diffTargetSourceLine
)

type pendingDiffTarget struct {
	kind      diffTargetKind
	workspace review.WorkspaceRevision
	handle    review.PatchHandle
	operation review.OperationID
	patchRow  uint32
	side      review.LineSide
	line      review.LineNumber
}

func newPatchRowTarget(
	workspace review.WorkspaceRevision,
	handle review.PatchHandle,
	row uint32,
) pendingDiffTarget {
	return pendingDiffTarget{
		kind:      diffTargetPatchRow,
		workspace: workspace,
		handle:    handle,
		patchRow:  row,
	}
}

func newSourceLineTarget(
	workspace review.WorkspaceRevision,
	handle review.PatchHandle,
	side review.LineSide,
	line review.LineNumber,
) pendingDiffTarget {
	return pendingDiffTarget{
		kind:      diffTargetSourceLine,
		workspace: workspace,
		handle:    handle,
		side:      side,
		line:      line,
	}
}

func (target pendingDiffTarget) valid() bool {
	if target.workspace.Uint64() == 0 || target.handle.ID().Uint64() == 0 ||
		target.handle.SourceID().Uint64() == 0 ||
		target.handle.FileID().Uint32() >= review.MaxFiles {
		return false
	}
	switch target.kind {
	case diffTargetPatchRow:
		return target.side == 0 && target.line == 0
	case diffTargetSourceLine:
		return (target.side == review.LineOld || target.side == review.LineNew) &&
			target.line != 0
	default:
		return false
	}
}

// foregroundState is the sole owner of reload, patch, and search coordination.
// Its zero value is not valid because patch admission requires an initialized,
// bounded operation set.
type foregroundState struct {
	reload reloadFlow
	patch  patchFlow
	search searchFlow
}

func (search pendingSearch) valid() bool {
	query, err := review.NewSearchQuery(search.query.String())
	return err == nil && query == search.query &&
		(search.scope == review.SearchCurrentFile || search.scope == review.SearchAllFiles)
}

func validForegroundPatchContext(context review.PatchContext) bool {
	if context.WholeFile() {
		return true
	}
	lines, ok := context.Lines()
	if !ok {
		return false
	}
	_, err := review.ContextAround(lines)
	return err == nil
}

func newForegroundState() foregroundState {
	return foregroundState{
		patch: patchFlow{active: make(map[review.OperationID]review.FileID)},
	}
}

type foregroundStartKind uint8

const (
	foregroundStartNone foregroundStartKind = iota
	foregroundStartPatch
	foregroundStartSearch
	foregroundStartReload
)

type foregroundStartSource uint8

const (
	foregroundStartDirect foregroundStartSource = iota + 1
	foregroundStartPatchRetry
	foregroundStartPostReloadPatch
	foregroundStartPatchSearch
	foregroundStartReplacementSearch
	foregroundStartWaitingReload
)

// foregroundStart is an immutable admission decision. Preparing one does not
// mutate foregroundState; admitted and rejected are the only terminal paths.
type foregroundStart struct {
	kind   foregroundStartKind
	source foregroundStartSource
	file   review.FileID
	search pendingSearch
}

type foregroundEffectKind uint8

const (
	foregroundEffectNone foregroundEffectKind = iota
	foregroundEffectRequestPatch
	foregroundEffectStartSearch
	foregroundEffectStartReload
	foregroundEffectApplyPatchContext
	foregroundEffectClearSearch
)

type foregroundEffect struct {
	kind    foregroundEffectKind
	file    review.FileID
	search  pendingSearch
	context review.PatchContext
}

func (state *foregroundState) valid() bool {
	if state == nil || state.patch.active == nil ||
		len(state.patch.active) > MaxAcceptedOperations {
		return false
	}
	for operation, file := range state.patch.active {
		if operation == 0 || file.Uint32() >= review.MaxFiles {
			return false
		}
	}
	if state.patch.target != nil {
		target := state.patch.target
		if !target.valid() {
			return false
		}
		if target.operation != 0 {
			file, exists := state.patch.active[target.operation]
			if !exists || file != target.handle.FileID() {
				return false
			}
		}
	}
	if state.reload.afterReload != nil && !state.reload.afterReload.valid() {
		return false
	}
	switch state.reload.phase {
	case reloadIdle:
		if state.reload.operation != 0 || state.reload.attempts != 0 ||
			state.reload.afterReload != nil {
			return false
		}
	case reloadWaitingForPatch:
		if state.reload.operation != 0 || state.reload.attempts != 0 {
			return false
		}
	case reloadAcquiring:
		if state.reload.operation == 0 || state.reload.attempts != 0 {
			return false
		}
	case reloadPreparing:
		if state.reload.operation == 0 || state.reload.attempts == 0 ||
			state.reload.attempts > maxReloadPreparationTries {
			return false
		}
	default:
		return false
	}
	if state.reload.phase != reloadIdle &&
		state.patch.continuation.kind == patchContinuationBeginSearch {
		return false
	}
	switch state.patch.continuation.kind {
	case patchContinuationNone:
	case patchContinuationRetry:
		// A retry remains represented after completion until its synchronous
		// admission decision is accepted or rejected.
		if state.patch.continuation.file.Uint32() >= review.MaxFiles {
			return false
		}
	case patchContinuationBeginSearch:
		if state.search.active != 0 ||
			state.search.continuation.kind != searchContinuationNone ||
			!state.patch.continuation.search.valid() {
			return false
		}
	default:
		return false
	}
	switch state.search.continuation.kind {
	case searchContinuationNone:
	case searchContinuationReplace:
		// A replacement remains represented after completion until its
		// synchronous admission decision is accepted or rejected.
		if !state.search.continuation.search.valid() {
			return false
		}
	case searchContinuationClear, searchContinuationPatchContext:
		if state.search.active == 0 {
			return false
		}
		if state.search.continuation.kind == searchContinuationPatchContext &&
			!validForegroundPatchContext(state.search.continuation.context) {
			return false
		}
	default:
		return false
	}
	return true
}

func (state *foregroundState) patchCount() int {
	return len(state.patch.active)
}

func (state *foregroundState) patchOperationIDs() []review.OperationID {
	ids := make([]review.OperationID, 0, len(state.patch.active))
	for id := range state.patch.active {
		ids = append(ids, id)
	}
	return ids
}

func (state *foregroundState) reloadActive() bool {
	return state.reload.phase != reloadIdle
}

func (state *foregroundState) reloadIsPreparing() bool {
	return state.reload.phase == reloadPreparing
}

func (state *foregroundState) reloadOperation() review.OperationID {
	return state.reload.operation
}

func (state *foregroundState) reloadPreparationAttempts() uint8 {
	return state.reload.attempts
}

func (state *foregroundState) searchOperation() review.OperationID {
	return state.search.active
}

func (state *foregroundState) preparePatch(file review.FileID) (foregroundStart, bool) {
	if file.Uint32() >= review.MaxFiles || len(state.patch.active) != 0 ||
		state.reload.phase != reloadIdle {
		return foregroundStart{}, false
	}
	start := foregroundStart{kind: foregroundStartPatch, file: file}
	switch state.patch.continuation.kind {
	case patchContinuationNone:
		start.source = foregroundStartDirect
	case patchContinuationRetry:
		if state.patch.continuation.file != file {
			return foregroundStart{}, false
		}
		start.source = foregroundStartPatchRetry
	case patchContinuationBeginSearch:
		start.source = foregroundStartPostReloadPatch
	default:
		return foregroundStart{}, false
	}
	return start, true
}

func (state *foregroundState) prepareSearch(search pendingSearch) (foregroundStart, bool) {
	if !search.valid() || state.reload.phase != reloadIdle || state.search.active != 0 {
		return foregroundStart{}, false
	}
	start := foregroundStart{kind: foregroundStartSearch, search: search}
	switch state.search.continuation.kind {
	case searchContinuationNone:
		if state.patch.continuation.kind == patchContinuationBeginSearch {
			if len(state.patch.active) != 0 {
				return foregroundStart{}, false
			}
			if state.patch.continuation.search != search {
				return foregroundStart{}, false
			}
			start.source = foregroundStartPatchSearch
		} else if state.patch.continuation.kind == patchContinuationNone &&
			len(state.patch.active) == 0 {
			start.source = foregroundStartDirect
		} else {
			return foregroundStart{}, false
		}
	case searchContinuationReplace:
		if state.search.continuation.search != search {
			return foregroundStart{}, false
		}
		start.source = foregroundStartReplacementSearch
	default:
		return foregroundStart{}, false
	}
	return start, true
}

func (state *foregroundState) prepareReload() (foregroundStart, bool) {
	switch state.reload.phase {
	case reloadIdle:
		if len(state.patch.active) != 0 {
			return foregroundStart{}, false
		}
		return foregroundStart{
			kind: foregroundStartReload, source: foregroundStartDirect,
		}, true
	case reloadWaitingForPatch:
		if len(state.patch.active) != 0 {
			return foregroundStart{}, false
		}
		return foregroundStart{
			kind: foregroundStartReload, source: foregroundStartWaitingReload,
		}, true
	default:
		return foregroundStart{}, false
	}
}

func (state *foregroundState) admitted(
	start foregroundStart,
	operation review.OperationID,
) bool {
	if operation == 0 || !state.valid() {
		return false
	}
	switch start.kind {
	case foregroundStartPatch:
		if len(state.patch.active) >= MaxAcceptedOperations {
			return false
		}
		prepared, ok := state.preparePatch(start.file)
		if !ok || prepared.source != start.source {
			return false
		}
		if start.source == foregroundStartPatchRetry {
			state.patch.continuation = patchContinuation{}
		}
		state.patch.active[operation] = start.file
		if state.patch.target != nil && state.patch.target.operation == 0 &&
			state.patch.target.handle.FileID() == start.file {
			state.patch.target.operation = operation
		}
	case foregroundStartSearch:
		prepared, ok := state.prepareSearch(start.search)
		if !ok || prepared.source != start.source {
			return false
		}
		switch start.source {
		case foregroundStartPatchSearch:
			state.patch.continuation = patchContinuation{}
		case foregroundStartReplacementSearch:
			state.search.continuation = searchContinuation{}
		}
		state.search.active = operation
	case foregroundStartReload:
		prepared, ok := state.prepareReload()
		if !ok || prepared.source != start.source {
			return false
		}
		state.reload.phase = reloadAcquiring
		state.reload.operation = operation
	default:
		return false
	}
	return state.valid()
}

func (state *foregroundState) rejected(start foregroundStart) (foregroundEffect, bool) {
	if !state.valid() {
		return foregroundEffect{}, false
	}
	var (
		prepared foregroundStart
		valid    bool
	)
	switch start.kind {
	case foregroundStartPatch:
		prepared, valid = state.preparePatch(start.file)
	case foregroundStartSearch:
		prepared, valid = state.prepareSearch(start.search)
	case foregroundStartReload:
		prepared, valid = state.prepareReload()
	default:
		return foregroundEffect{}, false
	}
	if !valid || prepared != start {
		return foregroundEffect{}, false
	}

	var effect foregroundEffect
	switch start.kind {
	case foregroundStartPatch:
		switch start.source {
		case foregroundStartPatchRetry:
			state.patch.continuation = patchContinuation{}
		case foregroundStartPostReloadPatch:
			effect = foregroundEffect{
				kind:   foregroundEffectStartSearch,
				search: state.patch.continuation.search,
			}
			state.patch.continuation = patchContinuation{}
		}
		if state.patch.target != nil && state.patch.target.operation == 0 &&
			state.patch.target.handle.FileID() == start.file {
			state.patch.target = nil
		}
	case foregroundStartSearch:
		switch start.source {
		case foregroundStartPatchSearch:
			state.patch.continuation = patchContinuation{}
		case foregroundStartReplacementSearch:
			state.search.continuation = searchContinuation{}
		}
	case foregroundStartReload:
		if start.source == foregroundStartWaitingReload {
			if state.reload.afterReload != nil {
				effect = foregroundEffect{
					kind:   foregroundEffectStartSearch,
					search: *state.reload.afterReload,
				}
			}
			state.reload = reloadFlow{}
		}
	}
	return effect, state.valid()
}

func (state *foregroundState) requestPatch(
	file review.FileID,
) (foregroundStart, []review.OperationID, bool) {
	if state.reload.phase != reloadIdle {
		return foregroundStart{}, nil, false
	}
	if len(state.patch.active) == 0 {
		start, ok := state.preparePatch(file)
		return start, nil, ok
	}
	state.patch.continuation = patchContinuation{
		kind: patchContinuationRetry,
		file: file,
	}
	return foregroundStart{}, state.patchOperationIDs(), false
}

func (state *foregroundState) requestSearch(
	search pendingSearch,
) (foregroundStart, []review.OperationID, bool) {
	if state.reload.phase != reloadIdle {
		return foregroundStart{}, nil, false
	}
	if state.search.active != 0 {
		if state.search.continuation.kind != searchContinuationPatchContext {
			state.search.continuation = searchContinuation{
				kind:   searchContinuationReplace,
				search: search,
			}
		}
		return foregroundStart{}, []review.OperationID{state.search.active}, false
	}
	if len(state.patch.active) != 0 {
		state.patch.continuation = patchContinuation{
			kind:   patchContinuationBeginSearch,
			search: search,
		}
		return foregroundStart{}, state.patchOperationIDs(), false
	}
	start, ok := state.prepareSearch(search)
	return start, nil, ok
}

func (state *foregroundState) requestClearSearch() (review.OperationID, bool) {
	if state.search.active == 0 {
		return 0, false
	}
	if state.search.continuation.kind != searchContinuationPatchContext {
		state.search.continuation = searchContinuation{kind: searchContinuationClear}
	}
	return state.search.active, true
}

func (state *foregroundState) requestPatchContext(
	context review.PatchContext,
) (review.OperationID, bool) {
	if state.search.active == 0 || !validForegroundPatchContext(context) {
		return 0, false
	}
	state.search.continuation = searchContinuation{
		kind:    searchContinuationPatchContext,
		context: context,
	}
	return state.search.active, true
}

func (state *foregroundState) requestReload() (foregroundStart, []review.OperationID, bool) {
	if state.reload.phase != reloadIdle || state.search.active != 0 {
		return foregroundStart{}, nil, false
	}
	if len(state.patch.active) == 0 {
		start, ok := state.prepareReload()
		return start, nil, ok
	}
	if state.patch.target != nil {
		state.patch.target.operation = 0
	}
	var afterReload *pendingSearch
	if state.patch.continuation.kind == patchContinuationBeginSearch {
		pending := state.patch.continuation.search
		afterReload = &pending
	}
	state.patch.continuation = patchContinuation{}
	state.reload = reloadFlow{
		phase:       reloadWaitingForPatch,
		afterReload: afterReload,
	}
	return foregroundStart{}, state.patchOperationIDs(), false
}

func (state *foregroundState) completePatch(
	id review.OperationID,
) (review.FileID, *pendingDiffTarget, bool, foregroundEffect) {
	file, ok := state.patch.active[id]
	if !ok {
		return 0, nil, false, foregroundEffect{}
	}
	var target *pendingDiffTarget
	if state.patch.target != nil && state.patch.target.operation == id {
		value := *state.patch.target
		target = &value
		state.patch.target = nil
	}
	delete(state.patch.active, id)
	if len(state.patch.active) != 0 {
		return file, target, true, foregroundEffect{}
	}
	if state.reload.phase == reloadWaitingForPatch {
		return file, target, true, foregroundEffect{kind: foregroundEffectStartReload}
	}
	switch state.patch.continuation.kind {
	case patchContinuationRetry:
		return file, target, true, foregroundEffect{
			kind: foregroundEffectRequestPatch,
			file: state.patch.continuation.file,
		}
	case patchContinuationBeginSearch:
		return file, target, true, foregroundEffect{
			kind:   foregroundEffectStartSearch,
			search: state.patch.continuation.search,
		}
	default:
		return file, target, true, foregroundEffect{}
	}
}

func (state *foregroundState) completeSearch(
	id review.OperationID,
) (bool, foregroundEffect) {
	if id == 0 || state.search.active != id {
		return false, foregroundEffect{}
	}
	state.search.active = 0
	switch state.search.continuation.kind {
	case searchContinuationPatchContext:
		effect := foregroundEffect{
			kind:    foregroundEffectApplyPatchContext,
			context: state.search.continuation.context,
		}
		state.search.continuation = searchContinuation{}
		return true, effect
	case searchContinuationReplace:
		return true, foregroundEffect{
			kind:   foregroundEffectStartSearch,
			search: state.search.continuation.search,
		}
	case searchContinuationClear:
		state.search.continuation = searchContinuation{}
		return true, foregroundEffect{kind: foregroundEffectClearSearch}
	default:
		return true, foregroundEffect{}
	}
}

func (state *foregroundState) reloadAcquired(id review.OperationID) bool {
	return id != 0 && state.reload.phase == reloadAcquiring &&
		state.reload.operation == id
}

func (state *foregroundState) reloadPreparationAdmitted(id review.OperationID) bool {
	if !state.reloadAcquired(id) {
		return false
	}
	state.reload.phase = reloadPreparing
	state.reload.attempts = 1
	return state.valid()
}

func (state *foregroundState) reloadPreparationRetryAdmitted(
	id review.OperationID,
) bool {
	if id == 0 || state.reload.phase != reloadPreparing ||
		state.reload.operation != id ||
		state.reload.attempts >= maxReloadPreparationTries {
		return false
	}
	state.reload.attempts++
	return state.valid()
}

func (state *foregroundState) reloadPrepared(id review.OperationID) bool {
	return id != 0 && state.reload.phase == reloadPreparing &&
		state.reload.operation == id
}

func (state *foregroundState) finishReloadFailure(
	id review.OperationID,
) (review.OperationID, foregroundEffect, bool) {
	if id == 0 || id != state.reload.operation ||
		state.reload.phase != reloadAcquiring && state.reload.phase != reloadPreparing {
		return 0, foregroundEffect{}, false
	}
	var effect foregroundEffect
	if state.reload.afterReload != nil {
		effect = foregroundEffect{
			kind:   foregroundEffectStartSearch,
			search: *state.reload.afterReload,
		}
	}
	operation := state.reload.operation
	state.reload = reloadFlow{}
	return operation, effect, true
}

func (state *foregroundState) finishReloadSuccess(
	id review.OperationID,
	viewport *diffViewport,
	patchRequirement reloadPatchRequirement,
) (foregroundEffect, bool) {
	if id == 0 || id != state.reload.operation ||
		state.reload.phase != reloadPreparing ||
		patchRequirement != reloadNoPatch && patchRequirement != reloadNeedsPatch {
		return foregroundEffect{}, false
	}
	afterReload := state.reload.afterReload
	state.reload = reloadFlow{}
	state.patch.target = nil
	state.patch.viewport = viewport
	if afterReload == nil {
		return foregroundEffect{}, true
	}
	if patchRequirement == reloadNeedsPatch {
		state.patch.continuation = patchContinuation{
			kind:   patchContinuationBeginSearch,
			search: *afterReload,
		}
		return foregroundEffect{}, true
	}
	return foregroundEffect{
		kind:   foregroundEffectStartSearch,
		search: *afterReload,
	}, true
}

func (state *foregroundState) takeReloadViewport(file review.FileID) (*diffViewport, bool) {
	viewport := state.patch.viewport
	if viewport == nil || viewport.file != file {
		return nil, false
	}
	state.patch.viewport = nil
	return viewport, true
}

func (state *foregroundState) clearReloadViewport(file review.FileID) {
	if state.patch.viewport != nil && state.patch.viewport.file == file {
		state.patch.viewport = nil
	}
}

func (state *foregroundState) setPendingDiffTarget(target pendingDiffTarget) bool {
	if !target.valid() || target.operation != 0 {
		return false
	}
	state.patch.target = new(target)
	return true
}

func (state *foregroundState) clearPendingDiffTarget() {
	state.patch.target = nil
}
