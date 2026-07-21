package app

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

type diffViewport struct {
	file         review.FileID
	top          uint32
	cursor       uint32
	visualAnchor uint32
}

type reloadPatchRequirement uint8

const (
	reloadNoPatch reloadPatchRequirement = iota + 1
	reloadNeedsPatch
)

const maxReloadPreparationTries = 8

// ReloadWork represents reload work.
type ReloadWork struct {
	Document *source.Document
	Options  source.OpenOptions
	ID       review.OperationID
}

// ReloadPreparationWork represents reload preparation work.
type ReloadPreparationWork struct {
	Document *source.Document
	Review   review.Review
	ID       review.OperationID
}

// ReloadAcquiredOutcome describes reload acquired outcome.
type ReloadAcquiredOutcome struct {
	Err      error
	Document *source.Document
	ID       review.OperationID
}

// ReloadPreparedOutcome describes reload prepared outcome.
type ReloadPreparedOutcome struct {
	AllFileTree     fileTree
	VisibleFileTree fileTree
	Err             error
	Document        *source.Document
	PathFiles       map[string]review.FileID
	AmbiguousPaths  map[string]struct{}
	AllFiles        []review.FileID
	VisibleFiles    []review.FileID
	Replacement     review.PreparedReplacement
	ID              review.OperationID
}

func (model *Model) startReload() {
	if len(model.cleanupDocuments) >= MaxAcceptedOperations {
		model.notice = &Notice{
			Kind: NoticeError,
			Text: "reload is disabled because source cleanup capacity is exhausted",
		}
		return
	}
	if model.foreground.reloadActive() {
		model.notice = &Notice{Kind: NoticeInformation, Text: "reload is already active"}
		return
	}
	if model.foreground.searchOperation() != 0 {
		model.notice = &Notice{Kind: NoticeWarning, Text: "reload requires active work to finish"}
		return
	}
	start, cancellations, ready := model.foreground.requestReload()
	for _, operationID := range cancellations {
		model.coordinator.Cancel(operationID)
	}
	if !ready {
		if len(cancellations) != 0 {
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "waiting for active patch cancellation",
			}
		}
		return
	}
	model.beginReload(start)
}

func (model *Model) beginReload(start foregroundStart) {
	if model.coordinator.Stats().Accepted != 0 {
		model.notice = &Notice{Kind: NoticeWarning, Text: "reload requires active work to finish"}
		model.rejectReloadStart(start)
		return
	}
	operationID, err := review.NewOperationID(model.nextOperation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: operationIdentityExhausted}
		model.rejectReloadStart(start)
		return
	}
	model.nextOperation++
	next, err := model.review.BeginOperation(operationID, review.OperationReload)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.rejectReloadStart(start)
		return
	}
	if err := model.coordinator.SubmitReload(ReloadWork{
		ID:       operationID,
		Document: model.document,
		Options:  model.document.Options(),
	}); err != nil {
		model.review = resolveRejectedOperation(next, operationID, err)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.rejectReloadStart(start)
		return
	}
	if !model.foreground.admitted(start, operationID) {
		model.coordinator.Cancel(operationID)
		err := errors.New("foreground reload admission invariant violated")
		model.review = resolveRejectedOperation(next, operationID, err)
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.notice = &Notice{Kind: NoticeInformation, Text: "reloading source"}
}

func (model *Model) rejectReloadStart(start foregroundStart) {
	effect, ok := model.foreground.rejected(start)
	if !ok {
		model.notice = &Notice{Kind: NoticeError, Text: "foreground rejection invariant violated"}
		return
	}
	if start.source == foregroundStartWaitingReload {
		model.restoreForegroundAfterReloadFailure(effect)
		return
	}
	model.applyForegroundEffect(effect)
}

func (model *Model) applyReloadAcquired(outcome ReloadAcquiredOutcome) {
	if !model.foreground.reloadAcquired(outcome.ID) {
		model.releaseReloadDocument(outcome.Document)
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale reload outcome"}
		return
	}
	if outcome.Err != nil {
		model.failReload(outcome.ID, outcome.Document, outcome.Err)
		return
	}
	if outcome.Document == nil {
		model.failReload(outcome.ID, nil, errors.New("reload acquired no source document"))
		return
	}
	if err := model.coordinator.SubmitReloadPreparation(ReloadPreparationWork{
		ID:       outcome.ID,
		Review:   model.review,
		Document: outcome.Document,
	}); err != nil {
		model.failReload(outcome.ID, outcome.Document, err)
		return
	}
	if !model.foreground.reloadPreparationAdmitted(outcome.ID) {
		model.coordinator.Cancel(outcome.ID)
		model.failReload(
			outcome.ID,
			outcome.Document,
			errors.New("foreground reload preparation invariant violated"),
		)
		return
	}
	model.notice = &Notice{Kind: NoticeInformation, Text: "reconciling reloaded source"}
}

func (model *Model) applyReloadPrepared(outcome ReloadPreparedOutcome) {
	if !model.foreground.reloadPrepared(outcome.ID) {
		model.releaseReloadDocument(outcome.Document)
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale reload outcome"}
		return
	}
	if outcome.Err != nil {
		model.failReload(outcome.ID, outcome.Document, outcome.Err)
		return
	}
	if outcome.Document == nil ||
		len(outcome.AllFiles) != outcome.Document.Snapshot().FileCount() {
		model.failReload(
			outcome.ID,
			outcome.Document,
			errors.New("reload preparation is incomplete"),
		)
		return
	}
	committed, report, ok := model.commitReloadReplacement(outcome)
	if !ok {
		return
	}
	committed, err := committed.ResolveOperation(
		outcome.ID,
		review.OperationSucceeded,
		review.EmptyOperationDetail(),
	)
	if err != nil {
		model.failReload(outcome.ID, outcome.Document, err)
		return
	}
	viewport := model.preservedReloadViewport(committed, report)
	_, hadNewActive := committed.ActiveFile()
	requirement := reloadNoPatch
	if hadNewActive {
		requirement = reloadNeedsPatch
	}
	effect, settled := model.foreground.finishReloadSuccess(
		outcome.ID,
		viewport,
		requirement,
	)
	if !settled {
		model.releaseReloadDocument(outcome.Document)
		model.notice = &Notice{
			Kind: NoticeError,
			Text: "foreground reload completion invariant violated",
		}
		return
	}

	oldDocument := model.installReloadState(outcome, committed, viewport)
	model.finishReloadDocument(oldDocument, report)
	if requirement == reloadNeedsPatch {
		model.requestActivePatch()
	} else {
		model.applyForegroundEffect(effect)
	}
}

func (model *Model) commitReloadReplacement(
	outcome ReloadPreparedOutcome,
) (review.Review, review.ReloadReport, bool) {
	committed, report, err := model.review.CommitReplacement(outcome.Replacement)
	if err == nil {
		return committed, report, true
	}
	if errors.Is(err, review.ErrStaleReloadPreparation) &&
		model.foreground.reloadPreparationAttempts() < maxReloadPreparationTries {
		submitErr := model.coordinator.SubmitReloadPreparation(ReloadPreparationWork{
			ID:       outcome.ID,
			Review:   model.review,
			Document: outcome.Document,
		})
		if submitErr == nil {
			if !model.foreground.reloadPreparationRetryAdmitted(outcome.ID) {
				model.coordinator.Cancel(outcome.ID)
				model.failReload(
					outcome.ID,
					outcome.Document,
					errors.New("foreground reload retry invariant violated"),
				)
				return review.Review{}, review.ReloadReport{}, false
			}
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "review changed during reload; reconciling latest state",
			}
			return review.Review{}, review.ReloadReport{}, false
		}
		err = submitErr
	}
	model.failReload(outcome.ID, outcome.Document, err)
	return review.Review{}, review.ReloadReport{}, false
}

func (model *Model) preservedReloadViewport(
	committed review.Review,
	report review.ReloadReport,
) *diffViewport {
	oldActive, hadOldActive := model.review.ActiveFile()
	newActive, hadNewActive := committed.ActiveFile()
	if !report.ActivePreserved || !hadOldActive || !hadNewActive {
		return nil
	}
	oldFile, oldExists := model.review.Snapshot().File(oldActive)
	newFile, newExists := committed.Snapshot().File(newActive)
	if !oldExists || !newExists || !sameFileContentIdentity(oldFile, newFile) {
		return nil
	}
	return &diffViewport{
		file:         newActive,
		top:          model.diffTop,
		cursor:       model.diffCursor,
		visualAnchor: model.visualAnchor,
	}
}

func (model *Model) installReloadState(
	outcome ReloadPreparedOutcome,
	committed review.Review,
	viewport *diffViewport,
) *source.Document {
	oldDocument := model.document
	model.document = outcome.Document
	model.review = committed
	model.resetSyntax()
	model.allFileOrder = outcome.AllFiles
	model.visibleFileOrder = outcome.VisibleFiles
	model.allFileTree = outcome.AllFileTree
	model.visibleFileTree = outcome.VisibleFileTree
	model.pathFiles = outcome.PathFiles
	model.ambiguousPaths = outcome.AmbiguousPaths
	model.filterQuery = ""
	model.filterGeneration++
	model.filterBuild = nil
	if model.filterTreeOperation != 0 {
		model.coordinator.Cancel(model.filterTreeOperation)
	}
	model.filterTreeOperation = 0
	model.filterTreeBuilding = false
	model.filterTreeGeneration = 0
	model.filterTreeFiles = nil
	model.filteredFileTree = nil
	if model.review.ShowHidden() {
		model.fileOrder = model.allFileOrder
	} else {
		model.fileOrder = model.visibleFileOrder
	}
	model.selectTreeCursor()
	if viewport == nil {
		model.diffTop = 0
		model.diffCursor = 0
		model.visualAnchor = 0
	}
	model.commentCursor = min(model.commentCursor, max(model.commentRowCount()-1, 0))
	return oldDocument
}

func (model *Model) finishReloadDocument(
	oldDocument *source.Document,
	report review.ReloadReport,
) {
	if err := model.coordinator.ReleaseDocument(oldDocument); err != nil {
		model.retainDocumentForCleanup(oldDocument)
		model.notice = &Notice{
			Kind: NoticeError,
			Text: NoticeText("reload committed but old source cleanup was not admitted: " + err.Error()),
		}
		return
	}
	model.notice = &Notice{
		Kind: NoticeInformation,
		Text: NoticeText(
			"reload complete: " +
				strconv.FormatUint(uint64(report.FilesPreserved), 10) +
				" files preserved, " +
				strconv.FormatUint(uint64(report.FilesRemoved), 10) +
				" removed",
		),
	}
}

func sameFileContentIdentity(left, right review.File) bool {
	compared := false
	for _, side := range []review.LineSide{review.LineOld, review.LineNew} {
		leftObject, leftExists := left.Content(side)
		rightObject, rightExists := right.Content(side)
		if leftExists != rightExists ||
			leftExists && leftObject != rightObject {
			return false
		}
		compared = compared || leftExists
	}
	return compared
}

func (model *Model) failReload(
	id review.OperationID,
	document *source.Document,
	err error,
) {
	operationID, effect, tracked := model.foreground.finishReloadFailure(id)
	model.releaseReloadDocument(document)
	if tracked {
		status := operationStatusForWorkError(err)
		if resolveErr := model.settleOperation(
			operationID,
			status,
			boundedOperationDetail(err.Error()),
		); resolveErr != nil {
			model.restoreForegroundAfterReloadFailure(effect)
			model.notice = operationTransitionNotice(resolveErr)
			return
		}
	}
	model.restoreForegroundAfterReloadFailure(effect)
	model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
}

func (model *Model) restoreForegroundAfterReloadFailure(effect foregroundEffect) {
	model.applyForegroundEffect(effect)
	model.resumeActivePatchAfterReloadFailure()
}

func (model *Model) resumeActivePatchAfterReloadFailure() {
	active, ok := model.review.ActiveFile()
	if !ok || model.review.Patch(active).Status == review.PatchReady {
		return
	}
	if model.review.Patch(active).Status == review.PatchPending &&
		model.foreground.patchCount() == 0 {
		model.review = model.review.DropPatch(active)
	}
	model.requestActivePatch()
}

func (model *Model) releaseReloadDocument(document *source.Document) {
	if document == nil {
		return
	}
	if err := model.coordinator.ReleaseDocument(document); err != nil {
		model.retainDocumentForCleanup(document)
		model.notice = &Notice{
			Kind: NoticeError,
			Text: NoticeText("release reload document: " + err.Error()),
		}
	}
}

func (model *Model) retainDocumentForCleanup(document *source.Document) {
	if document == nil {
		return
	}
	if slices.Contains(model.cleanupDocuments, document) {
		return
	}
	model.cleanupDocuments = append(model.cleanupDocuments, document)
}

func resolveRejectedOperation(
	current review.Review,
	id review.OperationID,
	err error,
) review.Review {
	next, resolveErr := current.ResolveOperation(
		id,
		operationStatusForWorkError(err),
		boundedOperationDetail(err.Error()),
	)
	if resolveErr != nil {
		return current
	}
	return next
}

func operationStatusForWorkError(err error) review.OperationStatus {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return review.OperationCancelled
	case errors.Is(err, ErrCoordinatorSaturated), errors.Is(err, source.ErrSourceLimited):
		return review.OperationLimited
	default:
		var failure *source.FailureError
		if errors.As(err, &failure) {
			switch failure.Kind {
			case source.FailureUnavailable,
				source.FailureStale,
				source.FailureFailed,
				source.FailureNonGraceful:
			case source.FailureCancelled:
				return review.OperationCancelled
			case source.FailureLimited:
				return review.OperationLimited
			}
		}
		return review.OperationFailed
	}
}
