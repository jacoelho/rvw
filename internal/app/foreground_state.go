package app

import "github.com/jacoelho/rvw/internal/review"

type reloadPhase uint8

const (
	reloadIdle reloadPhase = iota
	reloadWaitingForPatch
	reloadAcquiring
	reloadCapturing
	reloadPreparing
)

type patchContinuationKind uint8

const (
	patchContinuationNone patchContinuationKind = iota
	patchContinuationRetry
	patchContinuationBeginSearch
)

type patchContinuation struct {
	search pendingSearch
	file   review.FileID
	kind   patchContinuationKind
}

type searchContinuationKind uint8

const (
	searchContinuationNone searchContinuationKind = iota
	searchContinuationClear
	searchContinuationReplace
	searchContinuationPatchContext
	searchContinuationPatch
)

type searchContinuation struct {
	search  pendingSearch
	context review.PatchContext
	file    review.FileID
	kind    searchContinuationKind
}

type reloadFlow struct {
	afterReload *pendingSearch
	operation   review.OperationID
	phase       reloadPhase
	attempts    uint8
}

type patchFlow struct {
	active       *activePatch
	viewport     *diffViewport
	target       *pendingDiffTarget
	continuation patchContinuation
}

type activePatch struct {
	operation review.OperationID
	file      review.FileID
}

type searchFlow struct {
	continuation searchContinuation
	active       review.OperationID
}

type diffTargetKind uint8

const (
	diffTargetPatchRow diffTargetKind = iota + 1
	diffTargetSourceLine
)

type pendingDiffTarget struct {
	handle    review.PatchHandle
	workspace review.WorkspaceRevision
	operation review.OperationID
	patchRow  uint32
	line      review.LineNumber
	kind      diffTargetKind
	side      review.LineSide
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
// Its zero value is valid.
type foregroundState struct {
	reload reloadFlow
	patch  patchFlow
	search searchFlow
}

func (search pendingSearch) valid() bool {
	return search.query != "" && search.candidates.valid()
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
	return foregroundState{}
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
	search pendingSearch
	file   review.FileID
	kind   foregroundStartKind
	source foregroundStartSource
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
	search  pendingSearch
	file    review.FileID
	context review.PatchContext
	kind    foregroundEffectKind
}

func (state *foregroundState) valid() bool {
	if state == nil || !state.validPatchFlow() || !state.validReloadFlow() {
		return false
	}
	if state.search.active != 0 && state.patch.active != nil {
		return false
	}
	if state.reload.phase != reloadIdle &&
		state.patch.continuation.kind == patchContinuationBeginSearch {
		return false
	}
	return state.validPatchContinuation() && state.validSearchContinuation()
}

func (state *foregroundState) validPatchFlow() bool {
	if active := state.patch.active; active != nil {
		if active.operation == 0 || active.file.Uint32() >= review.MaxFiles {
			return false
		}
	}
	if state.patch.target != nil {
		target := state.patch.target
		if !target.valid() {
			return false
		}
		if target.operation != 0 {
			active := state.patch.active
			if active == nil || active.operation != target.operation ||
				active.file != target.handle.FileID() {
				return false
			}
		}
	}
	return true
}

func (state *foregroundState) validReloadFlow() bool {
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
	case reloadCapturing:
		if state.reload.operation == 0 ||
			state.reload.attempts >= maxReloadPreparationTries {
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
	return true
}

func (state *foregroundState) validPatchContinuation() bool {
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
	return true
}

func (state *foregroundState) validSearchContinuation() bool {
	switch state.search.continuation.kind {
	case searchContinuationNone:
	case searchContinuationReplace:
		// A replacement remains represented after completion until its
		// synchronous admission decision is accepted or rejected.
		if !state.search.continuation.search.valid() {
			return false
		}
	case searchContinuationClear, searchContinuationPatchContext, searchContinuationPatch:
		if state.search.active == 0 {
			return false
		}
		if state.search.continuation.kind == searchContinuationPatchContext &&
			!validForegroundPatchContext(state.search.continuation.context) {
			return false
		}
		if state.search.continuation.kind == searchContinuationPatch &&
			state.search.continuation.file.Uint32() >= review.MaxFiles {
			return false
		}
	default:
		return false
	}
	return true
}

func (state *foregroundState) patchCount() int {
	if state.patch.active == nil {
		return 0
	}
	return 1
}

func (state *foregroundState) patchOperationIDs() []review.OperationID {
	if state.patch.active == nil {
		return nil
	}
	return []review.OperationID{state.patch.active.operation}
}

func (state *foregroundState) reloadActive() bool {
	return state.reload.phase != reloadIdle
}

func (state *foregroundState) reloadIsPreparing() bool {
	return state.reload.phase == reloadCapturing || state.reload.phase == reloadPreparing
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
	if file.Uint32() >= review.MaxFiles || state.patch.active != nil ||
		state.reload.phase != reloadIdle || state.search.active != 0 {
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
		switch {
		case state.patch.continuation.kind == patchContinuationBeginSearch:
			if state.patch.active != nil {
				return foregroundStart{}, false
			}
			if state.patch.continuation.search != search {
				return foregroundStart{}, false
			}
			start.source = foregroundStartPatchSearch
		case state.patch.continuation.kind == patchContinuationNone &&
			state.patch.active == nil:
			start.source = foregroundStartDirect
		default:
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
		if state.patch.active != nil {
			return foregroundStart{}, false
		}
		return foregroundStart{
			kind: foregroundStartReload, source: foregroundStartDirect,
		}, true
	case reloadWaitingForPatch:
		if state.patch.active != nil {
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
		if state.patch.active != nil {
			return false
		}
		prepared, ok := state.preparePatch(start.file)
		if !ok || prepared.source != start.source {
			return false
		}
		if start.source == foregroundStartPatchRetry {
			state.patch.continuation = patchContinuation{}
		}
		state.patch.active = &activePatch{operation: operation, file: start.file}
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
		case foregroundStartDirect:
		case foregroundStartPatchSearch:
			state.patch.continuation = patchContinuation{}
		case foregroundStartReplacementSearch:
			state.search.continuation = searchContinuation{}
		case foregroundStartPatchRetry,
			foregroundStartPostReloadPatch,
			foregroundStartWaitingReload:
			return false
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
	case foregroundStartNone:
		return foregroundEffect{}, false
	}
	if !valid || prepared != start {
		return foregroundEffect{}, false
	}

	var effect foregroundEffect
	switch start.kind {
	case foregroundStartNone:
		return foregroundEffect{}, false
	case foregroundStartPatch:
		switch start.source {
		case foregroundStartDirect:
		case foregroundStartPatchRetry:
			state.patch.continuation = patchContinuation{}
		case foregroundStartPostReloadPatch:
			effect = foregroundEffect{
				kind:   foregroundEffectStartSearch,
				search: state.patch.continuation.search,
			}
			state.patch.continuation = patchContinuation{}
		case foregroundStartPatchSearch,
			foregroundStartReplacementSearch,
			foregroundStartWaitingReload:
			return foregroundEffect{}, false
		}
		if state.patch.target != nil && state.patch.target.operation == 0 &&
			state.patch.target.handle.FileID() == start.file {
			state.patch.target = nil
		}
	case foregroundStartSearch:
		switch start.source {
		case foregroundStartDirect:
		case foregroundStartPatchSearch:
			state.patch.continuation = patchContinuation{}
		case foregroundStartReplacementSearch:
			state.search.continuation = searchContinuation{}
		case foregroundStartPatchRetry,
			foregroundStartPostReloadPatch,
			foregroundStartWaitingReload:
			return foregroundEffect{}, false
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
	if state.reload.phase != reloadIdle || file.Uint32() >= review.MaxFiles {
		return foregroundStart{}, nil, false
	}
	if state.search.active != 0 {
		if state.search.continuation.kind != searchContinuationPatchContext {
			state.search.continuation = searchContinuation{
				kind: searchContinuationPatch,
				file: file,
			}
		}
		return foregroundStart{}, []review.OperationID{state.search.active}, false
	}
	if state.patch.active == nil {
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
	if state.patch.active != nil {
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
	if state.patch.continuation.kind == patchContinuationBeginSearch {
		state.patch.continuation = patchContinuation{}
	}
	if state.reload.afterReload != nil {
		state.reload.afterReload = nil
	}
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
	if state.patch.active == nil {
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
	active := state.patch.active
	if active == nil || active.operation != id {
		return 0, nil, false, foregroundEffect{}
	}
	file := active.file
	var target *pendingDiffTarget
	if state.patch.target != nil && state.patch.target.operation == id {
		value := *state.patch.target
		target = &value
		state.patch.target = nil
	}
	state.patch.active = nil
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
	case searchContinuationPatch:
		effect := foregroundEffect{
			kind: foregroundEffectRequestPatch,
			file: state.search.continuation.file,
		}
		state.search.continuation = searchContinuation{}
		return true, effect
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

func (state *foregroundState) reloadCaptureStarted(id review.OperationID) bool {
	if id == 0 || state.reload.operation != id ||
		state.reload.phase != reloadAcquiring && state.reload.phase != reloadPreparing ||
		state.reload.attempts >= maxReloadPreparationTries {
		return false
	}
	state.reload.phase = reloadCapturing
	return state.valid()
}

func (state *foregroundState) reloadPreparationAdmitted(
	id review.OperationID,
	capturedAttempts uint8,
) bool {
	if id == 0 || state.reload.phase != reloadCapturing ||
		state.reload.operation != id ||
		state.reload.attempts != capturedAttempts ||
		capturedAttempts >= maxReloadPreparationTries {
		return false
	}
	state.reload.attempts++
	state.reload.phase = reloadPreparing
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
		state.reload.phase != reloadAcquiring && state.reload.phase != reloadCapturing &&
			state.reload.phase != reloadPreparing {
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
