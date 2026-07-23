package app

import (
	"context"
	"errors"
	"slices"
	"strconv"

	tea "charm.land/bubbletea/v2"

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
	Collapse collapseSnapshot
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
	Err               error
	Document          *source.Document
	PathFiles         map[string]review.FileID
	AmbiguousPaths    map[string]struct{}
	AllFiles          []review.FileID
	VisibleFiles      []review.FileID
	FileTree          fileTree
	VisibleProjection treeProjection
	Collapsed         collapsedTreeState
	Replacement       review.PreparedReplacement
	ID                review.OperationID
}

// RetainedBytes returns the complete reload result ownership. The candidate
// snapshot is counted once even though the replacement and tree refer to it.
func (outcome ReloadPreparedOutcome) RetainedBytes() int64 {
	if outcome.Document == nil {
		return 0
	}
	return outcome.Document.Snapshot().RetainedBytes() + outcome.Replacement.RetainedBytes() +
		retainedWorkspaceIndexBytes(
			outcome.FileTree,
			outcome.VisibleProjection,
			outcome.AllFiles,
			outcome.VisibleFiles,
			outcome.PathFiles,
			outcome.AmbiguousPaths,
		) + outcome.Collapsed.retainedBytes()
}

func (model *Model) startReload() {
	if len(model.cleanupSources) >= MaxAcceptedOperations {
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

func (model *Model) applyReloadAcquired(outcome ReloadAcquiredOutcome) tea.Cmd {
	if !model.foreground.reloadAcquired(outcome.ID) {
		model.releaseReloadDocument(outcome.Document)
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale reload outcome"}
		return nil
	}
	if outcome.Err != nil {
		model.failReload(outcome.ID, outcome.Document, outcome.Err)
		return nil
	}
	if outcome.Document == nil {
		model.failReload(outcome.ID, nil, errors.New("reload acquired no source document"))
		return nil
	}
	if !model.foreground.reloadCaptureStarted(outcome.ID) {
		model.failReload(
			outcome.ID,
			outcome.Document,
			errors.New("foreground reload capture invariant violated"),
		)
		return nil
	}
	return model.beginCollapseCapture(reloadCollapseCaptureIntent{
		id:       outcome.ID,
		document: outcome.Document,
		review:   model.review,
		detail:   "reconciling reloaded source",
	})
}

func (model *Model) applyReloadPrepared(outcome ReloadPreparedOutcome) tea.Cmd {
	if !model.foreground.reloadPrepared(outcome.ID) {
		model.releaseReloadDocument(outcome.Document)
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale reload outcome"}
		return nil
	}
	if model.reloadPreparationDocument != nil &&
		model.reloadPreparationDocument != outcome.Document {
		expected := model.reloadPreparationDocument
		model.reloadPreparationDocument = nil
		model.releaseReloadDocument(outcome.Document)
		model.failReload(
			outcome.ID,
			expected,
			errors.New("reload preparation returned a different source document"),
		)
		return nil
	}
	model.reloadPreparationDocument = nil
	if outcome.Err != nil {
		model.failReload(outcome.ID, outcome.Document, outcome.Err)
		return nil
	}
	if outcome.Document == nil ||
		len(outcome.AllFiles) != outcome.Document.Snapshot().FileCount() {
		model.failReload(
			outcome.ID,
			outcome.Document,
			errors.New("reload preparation is incomplete"),
		)
		return nil
	}
	if outcome.Collapsed.generation != model.collapsedTree.generation {
		return model.retryReloadPreparation(outcome, "tree collapse changed during reload")
	}
	committed, report, command, ok := model.commitReloadReplacement(outcome)
	if !ok {
		return command
	}
	committed, err := committed.ResolveOperation(
		outcome.ID,
		review.OperationSucceeded,
		review.EmptyOperationDetail(),
	)
	if err != nil {
		model.failReload(outcome.ID, outcome.Document, err)
		return nil
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
		return nil
	}

	oldDocument := model.installReloadState(outcome, committed, viewport)
	model.finishReloadDocument(oldDocument, report)
	if requirement == reloadNeedsPatch {
		model.requestActivePatch()
	} else {
		model.applyForegroundEffect(effect)
	}
	return nil
}

func (model *Model) commitReloadReplacement(
	outcome ReloadPreparedOutcome,
) (review.Review, review.ReloadReport, tea.Cmd, bool) {
	committed, report, err := model.review.CommitReplacement(outcome.Replacement)
	if err == nil {
		return committed, report, nil, true
	}
	if errors.Is(err, review.ErrStaleReloadPreparation) &&
		model.foreground.reloadPreparationAttempts() < maxReloadPreparationTries {
		command := model.retryReloadPreparation(
			outcome,
			"review changed during reload; reconciling latest state",
		)
		return review.Review{}, review.ReloadReport{}, command, false
	}
	model.failReload(outcome.ID, outcome.Document, err)
	return review.Review{}, review.ReloadReport{}, nil, false
}

func (model *Model) retryReloadPreparation(
	outcome ReloadPreparedOutcome,
	detail NoticeText,
) tea.Cmd {
	if model.foreground.reloadPreparationAttempts() >= maxReloadPreparationTries {
		model.failReload(
			outcome.ID,
			outcome.Document,
			errors.New("reload preparation did not reach a stable collapse generation"),
		)
		return nil
	}
	attempts := model.foreground.reloadPreparationAttempts()
	if !model.foreground.reloadCaptureStarted(outcome.ID) {
		model.failReload(
			outcome.ID,
			outcome.Document,
			errors.New("foreground reload retry capture invariant violated"),
		)
		return nil
	}
	return model.beginCollapseCapture(reloadCollapseCaptureIntent{
		id:       outcome.ID,
		document: outcome.Document,
		review:   model.review,
		detail:   detail,
		attempts: attempts,
	})
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
	model.allFileTree = outcome.FileTree
	model.visibleFileTree = outcome.FileTree.withProjection(outcome.VisibleProjection)
	model.pathFiles = outcome.PathFiles
	model.ambiguousPaths = outcome.AmbiguousPaths
	model.collapsedTree = outcome.Collapsed
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
	cleanup := oldDocument.DetachCleanup()
	if err := model.coordinator.ReleaseCleanup(cleanup); err != nil {
		model.retainCleanup(cleanup)
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
	if document == nil || document == model.document ||
		document == model.reloadPreparationDocument ||
		model.collapseCaptureOwnsDocument(document) {
		return
	}
	cleanup := document.DetachCleanup()
	if err := model.coordinator.ReleaseCleanup(cleanup); err != nil {
		model.retainCleanup(cleanup)
		model.notice = &Notice{
			Kind: NoticeError,
			Text: NoticeText("release reload document: " + err.Error()),
		}
	}
}

func (model *Model) retainCleanup(cleanup *source.Cleanup) {
	if cleanup == nil {
		return
	}
	if slices.Contains(model.cleanupSources, cleanup) {
		return
	}
	model.cleanupSources = append(model.cleanupSources, cleanup)
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
		if failure, ok := errors.AsType[*source.FailureError](err); ok {
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
