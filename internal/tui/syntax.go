package tui

import (
	"iter"
	"slices"

	"github.com/jacoelho/rvw/internal/change"
)

func (model *Model) requestSyntax() {
	if !model.size.Known || model.belowMinimum() ||
		model.foreground.reloadActive() ||
		model.foreground.searchOperation() != 0 || model.searchAfterSyntax != nil ||
		model.mode != ModeNormal || model.overlay != OverlayNone {
		return
	}
	fileID, ok := model.review.ActiveFile()
	if !ok || model.review.Patch(fileID).Status != change.PatchReady {
		return
	}
	model.syntaxDeferred = false
	count := uint32FromInt(max(model.diffHeight()+viewportOverscan, 1))
	lines := model.review.PatchLines(fileID, model.diffTop, count, 4_096)
	oldLines := make([]change.LineNumber, 0, len(lines))
	newLines := make([]change.LineNumber, 0, len(lines))
	for _, line := range lines {
		if value, exists := line.OldLine(); exists {
			oldLines = append(oldLines, value)
		}
		if value, exists := line.NewLine(); exists {
			newLines = append(newLines, value)
		}
	}
	oldLines = uniqueLines(oldLines)
	newLines = uniqueLines(newLines)
	if len(oldLines)+len(newLines) == 0 ||
		model.syntaxCovered(fileID, oldLines, newLines) {
		return
	}
	if model.syntaxOperation != 0 {
		model.syntaxDeferred = true
		model.coordinator.Cancel(model.syntaxOperation)
		return
	}
	operationID, err := model.nextOperationID()
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	next, request, err := model.review.RequestSyntax(
		operationID,
		fileID,
		model.settings.syntaxSelector,
		oldLines,
		newLines,
	)
	if err != nil {
		return
	}
	for _, demand := range request.Demands {
		if _, suppressed := model.syntaxSuppressed[demand.Identity]; suppressed {
			return
		}
	}
	if err := model.coordinator.SubmitSyntax(SyntaxWork{
		ID:       operationID,
		Document: model.document,
		Request:  request,
	}); err != nil {
		if resolved, resolveErr := next.ResolveOperation(
			operationID,
			change.OperationFailed,
			boundedOperationDetail(err.Error()),
		); resolveErr == nil {
			model.review = resolved
		}
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.syntaxOperation = operationID
	model.syntaxFile = fileID
}

func uniqueLines(lines []change.LineNumber) []change.LineNumber {
	slices.Sort(lines)
	return slices.Compact(lines)
}

func (model *Model) syntaxCovered(
	fileID change.FileID,
	oldLines, newLines []change.LineNumber,
) bool {
	if model.syntaxFile != fileID {
		return false
	}
	for _, line := range oldLines {
		if !model.syntaxOld.Covers(line) {
			return false
		}
	}
	for _, line := range newLines {
		if !model.syntaxNew.Covers(line) {
			return false
		}
	}
	return true
}

func (model *Model) applySyntaxOutcome(outcome SyntaxOutcome) {
	if outcome.ID == 0 || outcome.ID != model.syntaxOperation {
		return
	}
	model.syntaxOperation = 0
	defer model.finishSyntaxDeferral()
	result := outcome.Result
	if outcome.Err != nil {
		result = change.SyntaxResult{
			Operation: outcome.ID,
			Workspace: model.review.WorkspaceRevision(),
			Source:    model.review.Snapshot().SourceID(),
			File:      model.syntaxFile,
			Status:    syntaxStatusForWorkError(outcome.Err),
			Detail:    boundedOperationDetail(outcome.Err.Error()),
		}
	}
	if result.Operation != outcome.ID ||
		result.Workspace != model.review.WorkspaceRevision() ||
		result.Source != model.review.Snapshot().SourceID() ||
		result.File != model.syntaxFile {
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale syntax outcome"}
		if next, err := model.review.ResolveOperation(
			outcome.ID,
			change.OperationSuperseded,
			boundedOperationDetail("syntax result did not match the active revision"),
		); err == nil {
			model.review = next
		}
		return
	}
	status := change.OperationSucceeded
	switch result.Status {
	case change.SyntaxReady:
		model.syntaxOld = result.Old
		model.syntaxNew = result.New
	case change.SyntaxCancelled:
		status = change.OperationCancelled
	case change.SyntaxLimited:
		status = change.OperationLimited
		model.suppressSyntaxResult(result)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(result.Detail.String())}
	case change.SyntaxUnavailable:
		status = change.OperationFailed
		model.suppressSyntaxResult(result)
	case change.SyntaxFailed:
		status = change.OperationFailed
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(result.Detail.String())}
	default:
		status = change.OperationFailed
		result.Detail = boundedOperationDetail("syntax worker returned an invalid status")
	}
	if next, err := model.review.ResolveOperation(
		outcome.ID,
		status,
		result.Detail,
	); err == nil {
		model.review = next
	}
}

func syntaxStatusForWorkError(err error) change.SyntaxStatus {
	switch operationStatusForWorkError(err) {
	case change.OperationCancelled:
		return change.SyntaxCancelled
	case change.OperationLimited:
		return change.SyntaxLimited
	default:
		return change.SyntaxFailed
	}
}

func (model *Model) finishSyntaxDeferral() {
	if model.searchAfterSyntax != nil {
		pending := *model.searchAfterSyntax
		model.searchAfterSyntax = nil
		model.startPendingSearch(pending)
		return
	}
	if model.syntaxDeferred {
		model.requestSyntax()
	}
}

func (model *Model) suppressSyntaxResult(result change.SyntaxResult) {
	if result.Old != nil {
		model.syntaxSuppressed[result.Old.Identity()] = result.Status
	}
	if result.New != nil {
		model.syntaxSuppressed[result.New.Identity()] = result.Status
	}
	if result.Old == nil && result.New == nil {
		file, ok := model.review.Snapshot().File(result.File)
		if !ok {
			return
		}
		for _, side := range []change.LineSide{change.LineOld, change.LineNew} {
			content, exists := file.Content(side)
			if !exists {
				continue
			}
			identity, err := change.NewSyntaxIdentity(content, model.settings.syntaxSelector)
			if err == nil {
				model.syntaxSuppressed[identity] = result.Status
			}
		}
	}
}

func (model *Model) resetSyntax() {
	if model.syntaxOperation != 0 {
		model.coordinator.Cancel(model.syntaxOperation)
		model.syntaxDeferred = true
	}
	model.syntaxOld = nil
	model.syntaxNew = nil
	model.syntaxSuppressed = make(map[change.SyntaxIdentity]change.SyntaxStatus)
}

func (model *Model) syntaxSpans(line change.PatchLine) iter.Seq[change.SyntaxSpan] {
	if sourceLine, ok := line.NewLine(); ok && line.Kind() != change.PatchLineDeletion {
		return model.syntaxNew.Spans(sourceLine)
	}
	if sourceLine, ok := line.OldLine(); ok {
		return model.syntaxOld.Spans(sourceLine)
	}
	return nil
}
