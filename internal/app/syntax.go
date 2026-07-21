package app

import (
	"iter"
	"slices"

	"github.com/jacoelho/rvw/internal/review"
)

func (model *Model) requestSyntax() {
	if !model.size.Known || model.belowMinimum() ||
		model.foreground.reloadActive() ||
		model.mode != ModeNormal || model.overlay != OverlayNone {
		return
	}
	fileID, ok := model.review.ActiveFile()
	if !ok || model.review.Patch(fileID).Status != review.PatchReady {
		return
	}
	count := uint32(max(model.diffHeight()+viewportOverscan, 1))
	lines := model.review.PatchLines(fileID, model.diffTop, count, 4_096)
	oldLines := make([]review.LineNumber, 0, len(lines))
	newLines := make([]review.LineNumber, 0, len(lines))
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
			review.OperationFailed,
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

func uniqueLines(lines []review.LineNumber) []review.LineNumber {
	slices.Sort(lines)
	return slices.Compact(lines)
}

func (model *Model) syntaxCovered(
	fileID review.FileID,
	oldLines, newLines []review.LineNumber,
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
		result = review.SyntaxResult{
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
			review.OperationSuperseded,
			boundedOperationDetail("syntax result did not match the active revision"),
		); err == nil {
			model.review = next
		}
		return
	}
	status := review.OperationSucceeded
	switch result.Status {
	case review.SyntaxReady:
		model.syntaxOld = result.Old
		model.syntaxNew = result.New
	case review.SyntaxCancelled:
		status = review.OperationCancelled
	case review.SyntaxLimited:
		status = review.OperationLimited
		model.suppressSyntaxResult(result)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(result.Detail.String())}
	case review.SyntaxUnavailable:
		status = review.OperationFailed
		model.suppressSyntaxResult(result)
	case review.SyntaxFailed:
		status = review.OperationFailed
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(result.Detail.String())}
	default:
		status = review.OperationFailed
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

func syntaxStatusForWorkError(err error) review.SyntaxStatus {
	switch operationStatusForWorkError(err) {
	case review.OperationCancelled:
		return review.SyntaxCancelled
	case review.OperationLimited:
		return review.SyntaxLimited
	default:
		return review.SyntaxFailed
	}
}

func (model *Model) finishSyntaxDeferral() {
	if model.syntaxDeferred {
		model.syntaxDeferred = false
		model.requestSyntax()
	}
}

func (model *Model) suppressSyntaxResult(result review.SyntaxResult) {
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
		for _, side := range []review.LineSide{review.LineOld, review.LineNew} {
			content, exists := file.Content(side)
			if !exists {
				continue
			}
			identity, err := review.NewSyntaxIdentity(content, model.settings.syntaxSelector)
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
	model.syntaxSuppressed = make(map[review.SyntaxIdentity]review.SyntaxStatus)
}

func (model *Model) syntaxSpans(line review.PatchLine) iter.Seq[review.SyntaxSpan] {
	if sourceLine, ok := line.NewLine(); ok && line.Kind() != review.PatchLineDeletion {
		return model.syntaxNew.Spans(sourceLine)
	}
	if sourceLine, ok := line.OldLine(); ok {
		return model.syntaxOld.Spans(sourceLine)
	}
	return nil
}
