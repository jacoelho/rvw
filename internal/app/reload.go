package app

import (
	"context"
	"errors"
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

const maxReloadPreparationTries = 8

type ReloadWork struct {
	ID       review.OperationID
	Document *source.Document
	Options  source.OpenOptions
}

type ReloadPreparationWork struct {
	ID       review.OperationID
	Review   review.Review
	Document *source.Document
}

type ReloadStage uint8

const (
	ReloadAcquired ReloadStage = iota + 1
	ReloadPrepared
)

type ReloadOutcome struct {
	ID              review.OperationID
	Stage           ReloadStage
	Document        *source.Document
	Replacement     review.PreparedReplacement
	AllFiles        []review.FileID
	VisibleFiles    []review.FileID
	AllFileTree     fileTree
	VisibleFileTree fileTree
	PathFiles       map[string]review.FileID
	AmbiguousPaths  map[string]struct{}
	Err             error
}

func (model *Model) startReload() {
	if len(model.cleanupDocuments) >= MaxAcceptedOperations {
		model.notice = &Notice{
			Kind: NoticeError,
			Text: "reload is disabled because source cleanup capacity is exhausted",
		}
		return
	}
	if model.reloadOperation != 0 || model.reloadDeferred {
		model.notice = &Notice{Kind: NoticeInformation, Text: "reload is already active"}
		return
	}
	if len(model.patchOperations) != 0 {
		for operationID := range model.patchOperations {
			model.coordinator.Cancel(operationID)
		}
		model.reloadDeferred = true
		model.notice = &Notice{Kind: NoticeInformation, Text: "waiting for active patch cancellation"}
		return
	}
	if model.coordinator.Stats().Accepted != 0 {
		model.notice = &Notice{Kind: NoticeWarning, Text: "reload requires active work to finish"}
		return
	}
	operationID, err := review.NewOperationID(model.nextOperation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: "operation identity exhausted"}
		return
	}
	model.nextOperation++
	next, err := model.review.BeginOperation(operationID, review.OperationReload)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	if err := model.coordinator.SubmitReload(ReloadWork{
		ID:       operationID,
		Document: model.document,
		Options:  model.document.Options(),
	}); err != nil {
		model.review = resolveRejectedOperation(next, operationID, err)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.reloadOperation = operationID
	model.reloadPreparationTries = 0
	model.notice = &Notice{Kind: NoticeInformation, Text: "reloading source"}
}

func (model *Model) applyReloadOutcome(outcome ReloadOutcome) {
	if outcome.ID == 0 || outcome.ID != model.reloadOperation {
		model.releaseReloadDocument(outcome.Document)
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale reload outcome"}
		return
	}
	switch outcome.Stage {
	case ReloadAcquired:
		model.applyReloadAcquired(outcome)
	case ReloadPrepared:
		model.applyReloadPrepared(outcome)
	default:
		model.failReload(outcome.Document, errors.New("reload outcome has an invalid stage"))
	}
}

func (model *Model) applyReloadAcquired(outcome ReloadOutcome) {
	if outcome.Err != nil {
		model.failReload(outcome.Document, outcome.Err)
		return
	}
	if outcome.Document == nil {
		model.failReload(nil, errors.New("reload acquired no source document"))
		return
	}
	if err := model.coordinator.SubmitReloadPreparation(ReloadPreparationWork{
		ID:       outcome.ID,
		Review:   model.review,
		Document: outcome.Document,
	}); err != nil {
		model.failReload(outcome.Document, err)
		return
	}
	model.reloadPreparing = true
	model.reloadPreparationTries = 1
	model.notice = &Notice{Kind: NoticeInformation, Text: "reconciling reloaded source"}
}

func (model *Model) applyReloadPrepared(outcome ReloadOutcome) {
	model.reloadPreparing = false
	if outcome.Err != nil {
		model.failReload(outcome.Document, outcome.Err)
		return
	}
	if outcome.Document == nil ||
		len(outcome.AllFiles) != outcome.Document.Snapshot().FileCount() {
		model.failReload(outcome.Document, errors.New("reload preparation is incomplete"))
		return
	}
	committed, report, err := model.review.CommitReplacement(outcome.Replacement)
	if err != nil {
		if errors.Is(err, review.ErrStaleReloadPreparation) &&
			model.reloadPreparationTries < maxReloadPreparationTries {
			if submitErr := model.coordinator.SubmitReloadPreparation(
				ReloadPreparationWork{
					ID:       outcome.ID,
					Review:   model.review,
					Document: outcome.Document,
				},
			); submitErr == nil {
				model.reloadPreparing = true
				model.reloadPreparationTries++
				model.notice = &Notice{
					Kind: NoticeInformation,
					Text: "review changed during reload; reconciling latest state",
				}
				return
			} else {
				err = submitErr
			}
		}
		model.failReload(outcome.Document, err)
		return
	}
	committed, err = committed.ResolveOperation(
		outcome.ID,
		review.OperationSucceeded,
		review.EmptyOperationDetail,
	)
	if err != nil {
		model.failReload(outcome.Document, err)
		return
	}
	var viewport *diffViewport
	oldActive, hadOldActive := model.review.ActiveFile()
	newActive, hadNewActive := committed.ActiveFile()
	if report.ActivePreserved && hadOldActive && hadNewActive {
		oldFile, oldExists := model.review.Snapshot().File(oldActive)
		newFile, newExists := committed.Snapshot().File(newActive)
		if oldExists && newExists && sameFileContentIdentity(oldFile, newFile) {
			viewport = &diffViewport{
				file:         newActive,
				top:          model.diffTop,
				cursor:       model.diffCursor,
				visualAnchor: model.visualAnchor,
			}
		}
	}

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
	model.reloadViewport = viewport
	if viewport == nil {
		model.diffTop = 0
		model.diffCursor = 0
		model.visualAnchor = 0
	}
	model.commentCursor = min(model.commentCursor, max(model.commentRowCount()-1, 0))
	model.reloadOperation = 0
	model.reloadPreparationTries = 0

	if err := model.coordinator.ReleaseDocument(oldDocument); err != nil {
		model.retainDocumentForCleanup(oldDocument)
		model.notice = &Notice{
			Kind: NoticeError,
			Text: NoticeText("reload committed but old source cleanup was not admitted: " + err.Error()),
		}
	} else {
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
	model.requestActivePatch()
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

func (model *Model) failReload(document *source.Document, err error) {
	model.reloadPreparing = false
	model.reloadPreparationTries = 0
	operationID := model.reloadOperation
	model.reloadOperation = 0
	model.releaseReloadDocument(document)
	if operationID != 0 {
		status := operationStatusForWorkError(err)
		if resolveErr := model.settleOperation(
			operationID,
			status,
			boundedOperationDetail(err.Error()),
		); resolveErr != nil {
			model.notice = operationTransitionNotice(resolveErr)
			return
		}
	}
	model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
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
	for _, existing := range model.cleanupDocuments {
		if existing == document {
			return
		}
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
		var failure *source.Failure
		if errors.As(err, &failure) {
			switch failure.Kind {
			case source.FailureCancelled:
				return review.OperationCancelled
			case source.FailureLimited:
				return review.OperationLimited
			}
		}
		return review.OperationFailed
	}
}
