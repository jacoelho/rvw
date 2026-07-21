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
	if model.foreground.reloadActive() {
		model.notice = &Notice{Kind: NoticeWarning, Text: "search is unavailable during reload"}
		return
	}
	model.foreground.clearPendingDiffTarget()
	hadSearch := model.foreground.searchOperation() != 0
	hadPatch := model.foreground.patchCount() != 0
	start, cancellations, ready := model.foreground.requestSearch(pendingSearch{
		query: query,
		scope: scope,
	})
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
	operationID, err := review.NewOperationID(model.nextOperation)
	if err != nil {
		model.rejectAndApplyForegroundStart(start)
		model.notice = &Notice{Kind: NoticeError, Text: operationIdentityExhausted}
		return
	}
	model.nextOperation++
	next, request, err := model.review.StartSearch(
		operationID,
		start.search.query,
		start.search.scope,
		model.settings.patchContext,
	)
	if err != nil {
		model.rejectAndApplyForegroundStart(start)
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
		model.rejectAndApplyForegroundStart(start)
		return
	}
	if !model.foreground.admitted(start, operationID) {
		model.coordinator.Cancel(operationID)
		err := errors.New("foreground search admission invariant violated")
		model.review = failSearchAdmission(next, request, err)
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
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
				review.OperationSuperseded,
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
		result, err = review.TakeSearchResult(review.SearchResultDescriptor{
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
	status := review.OperationSucceeded
	switch result.Status {
	case review.SearchComplete, review.SearchPartial:
	case review.SearchCancelled:
		status = review.OperationCancelled
	case review.SearchLimited:
		status = review.OperationLimited
	case review.SearchFailed:
		status = review.OperationFailed
	case review.SearchIdle, review.SearchActive, review.SearchSuperseded:
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
	case review.SearchIdle, review.SearchActive, review.SearchSuperseded:
		return
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
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(summary.Detail.String())}
	}
	if summary.HitCount != 0 {
		model.activateSearchHit(0)
	}
}

func (model *Model) failClaimedSearch(
	id review.OperationID,
	failure error,
	effect foregroundEffect,
) {
	model.review = model.review.ClearSearch()
	if next, err := model.review.ResolveOperation(
		id,
		review.OperationFailed,
		boundedOperationDetail(failure.Error()),
	); err == nil {
		model.review = next
	}
	model.applyForegroundEffect(effect)
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
	if model.review.Patch(hit.FileID()).Status != review.PatchReady {
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
