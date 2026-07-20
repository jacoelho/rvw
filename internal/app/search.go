package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/review"
)

func (model *Model) startSearch(value string, scope review.SearchScope) {
	query, err := review.NewSearchQuery(value)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	if model.reloadOperation != 0 || model.reloadDeferred || model.reloadPreparing {
		model.notice = &Notice{Kind: NoticeWarning, Text: "search is unavailable during reload"}
		return
	}
	if model.searchOperation != 0 {
		model.pendingSearch = &pendingSearch{query: query, scope: scope}
		model.clearSearchAfterCancel = false
		model.coordinator.Cancel(model.searchOperation)
		model.notice = &Notice{Kind: NoticeInformation, Text: "replacing active search"}
		return
	}
	if len(model.patchOperations) != 0 {
		for operationID := range model.patchOperations {
			model.coordinator.Cancel(operationID)
		}
		model.deferredPatch = nil
		model.pendingSearch = &pendingSearch{query: query, scope: scope}
		model.notice = &Notice{Kind: NoticeInformation, Text: "waiting for patch acquisition cancellation"}
		return
	}
	model.beginSearch(query, scope)
}

func (model *Model) beginSearch(query review.SearchQuery, scope review.SearchScope) {
	operationID, err := review.NewOperationID(model.nextOperation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: "operation identity exhausted"}
		return
	}
	model.nextOperation++
	next, request, err := model.review.StartSearch(
		operationID,
		query,
		scope,
		model.settings.patchContext,
	)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	if err := model.coordinator.SubmitSearch(SearchWork{
		ID:       operationID,
		Document: model.document,
		Request:  request,
	}); err != nil {
		model.review = failSearchAdmission(next, request, err)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.searchOperation = operationID
	model.searchCursor = 0
	model.notice = &Notice{Kind: NoticeInformation, Text: "searching changed patches"}
}

func failSearchAdmission(
	current review.Review,
	request review.SearchRequest,
	admissionErr error,
) review.Review {
	result, resultErr := review.TakeSearchResult(review.SearchResultDescriptor{
		Operation: request.Operation,
		Workspace: request.Workspace,
		Source:    request.Source,
		Status:    review.SearchFailed,
		Detail:    boundedOperationDetail(admissionErr.Error()),
	}, nil)
	if resultErr == nil {
		current, resultErr = current.ApplySearch(result)
	}
	if resultErr == nil {
		current, resultErr = current.ResolveOperation(
			request.Operation,
			review.OperationFailed,
			boundedOperationDetail(admissionErr.Error()),
		)
	}
	if resultErr != nil {
		return current.ClearSearch()
	}
	return current
}

func (model *Model) cancelSearch() {
	model.pendingSearch = nil
	model.pendingPatchContext = nil
	if model.searchOperation == 0 {
		model.review = model.review.ClearSearch()
		model.searchCursor = 0
		model.notice = &Notice{Kind: NoticeInformation, Text: "search cleared"}
		return
	}
	model.clearSearchAfterCancel = true
	model.coordinator.Cancel(model.searchOperation)
	model.notice = &Notice{Kind: NoticeInformation, Text: "cancelling search"}
}

func (model *Model) applySearchOutcome(outcome SearchOutcome) {
	if outcome.ID == 0 || outcome.ID != model.searchOperation {
		if outcome.ID != 0 {
			if next, err := model.review.ResolveOperation(
				outcome.ID,
				review.OperationSuperseded,
				"search result was superseded",
			); err == nil {
				model.review = next
			}
		}
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale search outcome"}
		return
	}
	model.searchOperation = 0
	result := outcome.Result
	if outcome.Err != nil {
		var err error
		result, err = review.TakeSearchResult(review.SearchResultDescriptor{
			Operation: outcome.ID,
			Workspace: model.review.WorkspaceRevision(),
			Source:    model.review.Snapshot().SourceID(),
			Status:    searchFailureStatus(outcome.Err),
			Detail:    boundedOperationDetail(outcome.Err.Error()),
		}, nil)
		if err != nil {
			model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
			model.failClaimedSearch(outcome.ID, err)
			return
		}
	}
	next, err := model.review.ApplySearch(result)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.failClaimedSearch(outcome.ID, err)
		return
	}
	status := review.OperationSucceeded
	switch result.Status {
	case review.SearchCancelled:
		status = review.OperationCancelled
	case review.SearchLimited:
		status = review.OperationLimited
	case review.SearchFailed:
		status = review.OperationFailed
	}
	next, err = next.ResolveOperation(outcome.ID, status, result.Detail())
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.finishSearchDeferrals()
		return
	}
	model.review = next

	if model.finishSearchDeferrals() {
		return
	}
	summary := model.review.SearchSummary()
	switch summary.Status {
	case review.SearchComplete:
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: NoticeText(fmt.Sprintf("%d search hits", summary.HitCount)),
		}
	case review.SearchPartial:
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText(fmt.Sprintf(
				"%d search hits; %d files unavailable",
				summary.HitCount,
				summary.UnavailableFiles,
			)),
		}
	case review.SearchLimited:
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText(fmt.Sprintf("%d search hits; result limit reached", summary.HitCount)),
		}
	case review.SearchCancelled:
		model.notice = &Notice{Kind: NoticeInformation, Text: "search cancelled"}
	case review.SearchFailed:
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(summary.Detail)}
	}
	if summary.HitCount != 0 {
		model.activateSearchHit(0)
	}
}

func (model *Model) failClaimedSearch(id review.OperationID, failure error) {
	model.review = model.review.ClearSearch()
	if next, err := model.review.ResolveOperation(
		id,
		review.OperationFailed,
		boundedOperationDetail(failure.Error()),
	); err == nil {
		model.review = next
	}
	model.finishSearchDeferrals()
}

func (model *Model) finishSearchDeferrals() bool {
	if model.pendingPatchContext != nil {
		context := *model.pendingPatchContext
		model.pendingPatchContext = nil
		model.pendingSearch = nil
		model.clearSearchAfterCancel = false
		model.review = model.review.ClearSearch()
		model.applyPatchContext(context)
		return true
	}
	if model.pendingSearch != nil {
		pending := *model.pendingSearch
		model.pendingSearch = nil
		model.clearSearchAfterCancel = false
		model.beginSearch(pending.query, pending.scope)
		return true
	}
	if model.clearSearchAfterCancel {
		model.clearSearchAfterCancel = false
		model.review = model.review.ClearSearch()
		model.searchCursor = 0
		model.notice = &Notice{Kind: NoticeInformation, Text: "search cleared"}
		return true
	}
	return false
}

func searchFailureStatus(err error) review.SearchStatus {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return review.SearchCancelled
	}
	return review.SearchFailed
}

func (model *Model) activateSearchHit(index int) {
	hit, ok := model.review.SearchHit(index)
	if !ok {
		return
	}
	if active, exists := model.review.ActiveFile(); !exists || active != hit.FileID() {
		next, err := model.review.ActivateFile(hit.FileID())
		if err != nil {
			return
		}
		model.review = next
		model.resetSyntax()
		model.pendingSearchHit = new(hit)
		model.requestActivePatch()
		model.searchCursor = index
		return
	}
	if model.review.Patch(hit.FileID()).Status != review.PatchReady {
		model.pendingSearchHit = new(hit)
		model.requestActivePatch()
		model.searchCursor = index
		return
	}
	model.searchCursor = index
	model.diffCursor = hit.PatchLine()
	model.moveDiff(0)
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
