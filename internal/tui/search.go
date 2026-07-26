package tui

import (
	"context"
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/change"
)

func (model *Model) startSearch(value string, files []change.FileID) {
	query, err := change.NewSearchQuery(value)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.failFindSearch(err)
		return
	}
	if model.foreground.reloadActive() {
		model.notice = &Notice{Kind: NoticeWarning, Text: "search is unavailable during reload"}
		model.failFindSearch(errors.New("search is unavailable during reload"))
		return
	}
	candidates, err := newSearchCandidates(model.review, files)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.failFindSearch(err)
		return
	}
	pending := pendingSearch{query: query, candidates: candidates}
	if model.syntaxOperation != 0 {
		model.searchAfterSyntax = new(pending)
		model.syntaxDeferred = false
		model.coordinator.Cancel(model.syntaxOperation)
		model.notice = &Notice{Kind: NoticeInformation, Text: "waiting for syntax cancellation"}
		return
	}
	model.startPendingSearch(pending)
}

func (model *Model) startPendingSearch(pending pendingSearch) {
	model.foreground.clearPendingDiffTarget()
	hadSearch := model.foreground.searchOperation() != 0
	hadPatch := model.foreground.patchCount() != 0
	start, cancellations, ready := model.foreground.requestSearch(pending)
	for _, operationID := range cancellations {
		model.coordinator.Cancel(operationID)
	}
	if !ready {
		switch {
		case hadSearch:
			model.notice = &Notice{Kind: NoticeInformation, Text: "cancelling active search"}
		case hadPatch:
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "waiting for patch acquisition cancellation",
			}
		}
		return
	}
	model.beginSearch(start)
}

func (model *Model) beginSearch(start foregroundStart) {
	operationID, err := change.NewOperationID(model.nextOperation)
	if err != nil {
		model.rejectAndApplyForegroundStart(start)
		model.notice = &Notice{Kind: NoticeError, Text: operationIdentityExhausted}
		model.failFindSearch(err)
		return
	}
	model.nextOperation++
	next, admission, err := model.review.StartSearch(
		operationID,
		start.search.query,
		model.settings.patchContext,
	)
	if err != nil {
		model.rejectAndApplyForegroundStart(start)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.failFindSearch(err)
		return
	}
	if err := model.coordinator.SubmitSearch(SearchWork{
		Document:   model.document,
		Admission:  admission,
		Candidates: start.search.candidates,
	}); err != nil {
		model.review = failSearchAdmission(next, admission, err)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.rejectAndApplyForegroundStart(start)
		model.failFindSearch(err)
		return
	}
	if !model.foreground.admitted(start, operationID) {
		model.coordinator.Cancel(operationID)
		err := errors.New("foreground search admission invariant violated")
		model.review = failSearchAdmission(next, admission, err)
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		model.failFindSearch(err)
		return
	}
	model.review = next
	model.searchCursor = 0
	if model.overlay == OverlayFind && model.find.submittedQuery == start.search.query.String() {
		model.find.searchOperation = operationID
		model.find.phase = findSearching
	}
	model.notice = &Notice{Kind: NoticeInformation, Text: "searching changed patches"}
}

func failSearchAdmission(
	current change.Review,
	admission change.SearchAdmission,
	admissionErr error,
) change.Review {
	result, resultErr := change.TakeSearchResult(change.SearchResultDescriptor{
		Operation: admission.Operation(),
		Workspace: admission.Workspace(),
		Source:    admission.Source(),
		Status:    change.SearchFailed,
		Detail:    boundedOperationDetail(admissionErr.Error()),
	}, nil)
	if resultErr == nil {
		current, resultErr = current.ApplySearch(result)
	}
	if resultErr == nil {
		current, resultErr = current.ResolveOperation(
			admission.Operation(),
			change.OperationFailed,
			boundedOperationDetail(admissionErr.Error()),
		)
	}
	if resultErr != nil {
		return current.ClearSearch()
	}
	return current
}

func (model *Model) cancelSearch() {
	if model.searchAfterSyntax != nil {
		model.searchAfterSyntax = nil
		model.syntaxDeferred = true
	}
	operationID, active := model.foreground.requestClearSearch()
	if !active {
		model.review = model.review.ClearSearch()
		model.searchCursor = 0
		model.notice = &Notice{Kind: NoticeInformation, Text: "search cleared"}
		return
	}
	model.coordinator.Cancel(operationID)
	model.notice = &Notice{Kind: NoticeInformation, Text: "cancelling search"}
}

func (model *Model) applySearchOutcome(outcome SearchOutcome) {
	tracked, effect := model.foreground.completeSearch(outcome.ID)
	if !tracked {
		if outcome.ID != 0 {
			if next, err := model.review.ResolveOperation(
				outcome.ID,
				change.OperationSuperseded,
				boundedOperationDetail("search result was superseded"),
			); err == nil {
				model.review = next
			}
		}
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale search outcome"}
		return
	}
	result := outcome.Result
	if outcome.Err != nil {
		var err error
		result, err = change.TakeSearchResult(change.SearchResultDescriptor{
			Operation: outcome.ID,
			Workspace: model.review.WorkspaceRevision(),
			Source:    model.review.Snapshot().SourceID(),
			Status:    searchFailureStatus(outcome.Err),
			Detail:    boundedOperationDetail(outcome.Err.Error()),
		}, nil)
		if err != nil {
			model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
			model.failClaimedSearch(outcome.ID, err, effect)
			return
		}
	}
	next, err := model.review.ApplySearch(result)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.failClaimedSearch(outcome.ID, err, effect)
		return
	}
	status := change.OperationSucceeded
	switch result.Status() {
	case change.SearchComplete, change.SearchPartial:
	case change.SearchCancelled:
		status = change.OperationCancelled
	case change.SearchLimited:
		status = change.OperationLimited
	case change.SearchFailed:
		status = change.OperationFailed
	case change.SearchIdle, change.SearchActive, change.SearchSuperseded:
		panic("review accepted a non-terminal search result")
	}
	next, err = next.ResolveOperation(outcome.ID, status, result.Detail())
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.applyForegroundEffect(effect)
		return
	}
	model.review = next

	if effect.kind != foregroundEffectNone {
		model.applyForegroundEffect(effect)
		return
	}
	summary := model.review.SearchSummary()
	switch summary.Status {
	case change.SearchIdle, change.SearchActive, change.SearchSuperseded:
		return
	case change.SearchComplete:
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: NoticeText(fmt.Sprintf("%d search hits", summary.HitCount)),
		}
	case change.SearchPartial:
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText(fmt.Sprintf(
				"%d search hits; %d files unavailable",
				summary.HitCount,
				summary.UnavailableFiles,
			)),
		}
	case change.SearchLimited:
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText(fmt.Sprintf("%d search hits; result limit reached", summary.HitCount)),
		}
	case change.SearchCancelled:
		model.notice = &Notice{Kind: NoticeInformation, Text: "search cancelled"}
	case change.SearchFailed:
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(summary.Detail.String())}
	}
	model.completeFindSearch(outcome.ID)
	if summary.HitCount != 0 {
		model.activateSearchHit(0)
	}
}

func (model *Model) failClaimedSearch(
	id change.OperationID,
	failure error,
	effect foregroundEffect,
) {
	model.review = model.review.ClearSearch()
	if next, err := model.review.ResolveOperation(
		id,
		change.OperationFailed,
		boundedOperationDetail(failure.Error()),
	); err == nil {
		model.review = next
	}
	model.applyForegroundEffect(effect)
}

func searchFailureStatus(err error) change.SearchStatus {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return change.SearchCancelled
	}
	return change.SearchFailed
}

func (model *Model) activateSearchHit(index int) {
	hit, ok := model.review.SearchHit(index)
	if !ok {
		return
	}
	file, exists := model.review.Snapshot().File(hit.FileID())
	if !exists {
		return
	}
	target := newPatchRowTarget(
		model.review.WorkspaceRevision(),
		file.PatchHandle(),
		hit.PatchLine(),
	)
	model.foreground.clearPendingDiffTarget()
	if active, exists := model.review.ActiveFile(); !exists || active != hit.FileID() {
		if !model.activateFile(hit.FileID()) {
			return
		}
		if !model.foreground.setPendingDiffTarget(target) {
			return
		}
		model.requestActivePatch()
		model.searchCursor = index
		return
	}
	if model.review.Patch(hit.FileID()).Status != change.PatchReady {
		if !model.foreground.setPendingDiffTarget(target) {
			return
		}
		model.requestActivePatch()
		model.searchCursor = index
		return
	}
	model.searchCursor = index
	model.applyDiffTarget(target)
}

func (model *Model) moveSearchHit(delta int) {
	count := model.review.SearchSummary().HitCount
	if count == 0 || delta == 0 {
		return
	}
	index := (model.searchCursor + delta) % count
	if index < 0 {
		index += count
	}
	model.activateSearchHit(index)
}
