package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/change"
	workcoord "github.com/jacoelho/rvw/internal/coordinator"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/projection"
	"github.com/jacoelho/rvw/internal/source"
)

const (
	chordTimeout               = 750 * time.Millisecond
	commentFileOutsideReview   = "comment file is not in this review"
	commentLabel               = "comment"
	exportLabel                = "export"
	helpLabel                  = "help"
	nextRowLabel               = "next row"
	operationIdentityExhausted = "operation identity exhausted"
	keyEnter                   = "enter"
	keyEscape                  = "esc"
	keyReturn                  = "return"
	remoteCommentsReadOnly     = NoticeText("remote comments are read-only")
	readyStatusText            = "Ready"
	reloadLabel                = "reload"
	searchLabel                = "search"
	unknownValue               = "unknown"
)

func uint16FromInt(value int) uint16 {
	if value < 0 || value > math.MaxUint16 {
		panic("value cannot be represented as uint16")
	}
	return uint16(value)
}

func uint32FromInt(value int) uint32 {
	if value < 0 {
		panic("negative value cannot be represented as uint32")
	}
	converted := uint64(value)
	if converted > math.MaxUint32 {
		panic("value exceeds uint32")
	}
	return uint32(converted)
}

func int32FromInt(value int) int32 {
	converted := int64(value)
	if converted < math.MinInt32 || converted > math.MaxInt32 {
		panic("value cannot be represented as int32")
	}
	return int32(converted)
}

func uint64FromInt(value int) uint64 {
	if value < 0 {
		panic("negative value cannot be represented as uint64")
	}
	return uint64(value)
}

// FocusOwner represents focus owner.
type FocusOwner uint8

// FocusFileTree identifies a supported focus file tree value.
const (
	FocusFileTree FocusOwner = iota + 1
	FocusDiff
	FocusComments
)

// Overlay represents overlay.
type Overlay uint8

// OverlayNone identifies a supported overlay none value.
const (
	OverlayNone Overlay = iota
	OverlayHelp
	OverlayStatus
	OverlayConfirm
	OverlayExport
	OverlaySubmit
	OverlayFind
)

// InteractionMode describes interaction mode.
type InteractionMode uint8

// ModeNormal identifies a supported mode normal value.
const (
	ModeNormal InteractionMode = iota + 1
	ModeVisual
	ModeCommentEdit
	ModeCommand
)

// NoticeKind describes notice kind.
type NoticeKind uint8

// NoticeInformation identifies a supported notice information value.
const (
	NoticeInformation NoticeKind = iota + 1
	NoticeSuccess
	NoticeWarning
	NoticeError
)

// NoticeText represents notice text.
type NoticeText string

// Notice represents notice.
type Notice struct {
	Text NoticeText
	Kind NoticeKind
}

type publicationOperationIntent uint8

const (
	publicationOperationIntentNone publicationOperationIntent = iota
	publicationOperationIntentSubmit
	publicationOperationIntentCancel
)

// TerminalSize represents terminal size.
type TerminalSize struct {
	Width  int
	Height int
	Known  bool
}

type chordTimeoutMsg struct {
	generation uint64
}

type initializeMsg struct{}

type mouseActionMsg struct {
	tree           treeNodeIdentity
	reviewState    uint64
	findGeneration uint64
	delta          int
	commentIndex   int
	findIndex      int
	diffLine       uint32
	focus          FocusOwner
	target         mouseTarget
}

type fileFilterChunkMsg struct {
	generation uint64
}

type treeMoveChunkMsg struct {
	generation uint64
}

type exportRequest struct {
	format      change.ExportFormat
	destination exportDestination
}

type exportSelection struct {
	format      change.ExportFormat
	destination exportDestination
	options     change.ExportOptions
}

type pendingSearch struct {
	candidates *searchCandidates
	query      change.SearchQuery
}

// Model represents model.
type Model struct {
	commandContext            context.Context
	lastNotice                *Notice
	pendingStdout             *change.PreparedExport
	filteredFileTree          *fileTree
	commentEditor             *commentEditor
	receiver                  *ReadyReceiver
	operationProgress         map[change.OperationID]Progress
	filterBuild               *fileFilterBuild
	collapseCapture           *collapseCapture
	exportOperations          map[change.OperationID]exportRequest
	index                     projection.Index
	syntaxSuppressed          map[change.SyntaxIdentity]change.SyntaxStatus
	syntaxNew                 *change.PreparedSyntax
	document                  *workcoord.SourceRevision
	reloadPreparationDocument *workcoord.SourceRevision
	syntaxOld                 *change.PreparedSyntax
	coordinator               *Coordinator
	findRestore               *findReturnState
	deliveryOperations        map[change.OperationID]struct{}
	notice                    *Notice
	searchAfterSyntax         *pendingSearch
	help                      help.Model
	fileProjection            fileProjectionState
	allFileTree               fileTree
	visibleFileTree           fileTree
	fileOrder                 []change.FileID
	cleanupSources            []*source.Cleanup
	treeCursor                treeNodeIdentity
	filterTreeFiles           []change.FileID
	searchInput               textinput.Model
	commandInput              textinput.Model
	keys                      keymap.Router
	foreground                foregroundState
	commentSelection          commentRowIdentity
	collapsedTree             collapsedTreeState
	settings                  Settings
	review                    change.Review
	find                      findPalette
	coordinatorStats          CoordinatorStats
	size                      TerminalSize
	searchCursor              int
	publicationOperation      change.OperationID
	treeMoveGeneration        uint64
	nextCollapseCapturePermit uint64
	treeMovePending           int
	diffLeft                  int
	filterTreeGeneration      uint64
	commentCursor             int
	helpTop                   int
	statusTop                 int
	nextOperation             uint64
	filterTreeOperation       change.OperationID
	syntaxOperation           change.OperationID
	filterGeneration          uint64
	workspaceIndex            change.OperationID
	confirmation              confirmation
	syntaxFile                change.FileID
	visualAnchor              uint32
	diffTop                   uint32
	diffCursor                uint32
	exportPicker              exportSelection
	commentSelectionKnown     bool
	commentPaneVisibility     paneVisibility
	publicationIntent         publicationOperationIntent
	quitting                  bool
	publicationDraftReady     bool
	publicationEvent          change.PublicationEvent
	focusReturn               FocusOwner
	fileTreeVisible           bool
	filterTreeBuilding        bool
	overlay                   Overlay
	statusReturn              Overlay
	syntaxDeferred            bool
	focus                     FocusOwner
	singlePaneOverride        FocusOwner
	mode                      InteractionMode
	initialized               bool
	coordinatorClosed         bool
}

type paneVisibility uint8

const (
	paneVisibilityAuto paneVisibility = iota
	paneVisibilityShown
	paneVisibilityHidden
)

type keyStroke struct {
	value    string
	repeated bool
}

// NewModel constructs and validates model.
func NewModel(
	ctx context.Context,
	opened source.Opened,
	revision *source.Revision,
	current change.Review,
	settings Settings,
	coordinator *Coordinator,
	receiver *ReadyReceiver,
) (*Model, error) {
	document := workcoord.NewSourceRevision(opened, revision)
	if ctx == nil || document == nil || coordinator == nil || receiver == nil {
		return nil, errors.New("interactive model requires context, source revision, coordinator, and receiver")
	}
	if current.Snapshot().SourceID() == 0 ||
		current.Snapshot().SourceID() != document.Snapshot().SourceID() {
		return nil, errors.New("interactive model requires a review for its source revision")
	}
	settings = settings.clone()
	helpModel := help.New()
	helpModel.ShowAll = true
	editor := newCommentEditor(change.MaxCommentBodyBytes)
	searchInput := textinput.New()
	searchInput.Prompt = "/ "
	searchInput.Placeholder = "search changed patches"
	searchInput.CharLimit = maxPromptBytes
	commandInput := textinput.New()
	commandInput.Prompt = ": "
	commandInput.CharLimit = maxPromptBytes
	model := &Model{
		commandContext:   ctx,
		document:         document,
		coordinator:      coordinator,
		receiver:         receiver,
		settings:         settings,
		keys:             keymap.NewRouter(settings.keymap),
		publicationEvent: settings.publicationEvent,
		help:             helpModel,
		commentEditor:    editor,
		searchInput:      searchInput,
		commandInput:     commandInput,
		review:           current,
		focus:            FocusDiff,
		mode:             ModeNormal,
		fileTreeVisible:  true,
		nextOperation:    1,
		foreground:       newForegroundState(),
		fileProjection: fileProjectionState{
			workspace: projectionInitial,
			filter:    projectionReady,
		},
		operationProgress:  make(map[change.OperationID]Progress),
		exportOperations:   make(map[change.OperationID]exportRequest),
		deliveryOperations: make(map[change.OperationID]struct{}),
		syntaxSuppressed:   make(map[change.SyntaxIdentity]change.SyntaxStatus),
	}
	model.refreshCoordinatorStats()
	pending := current.PendingReviewSummary()
	switch pending.Status {
	case change.PendingReviewNotRequested, change.PendingReviewAbsent:
	case change.PendingReviewReady:
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: NoticeText(fmt.Sprintf(
				"loaded pending GitHub review %d with %d comments",
				pending.ReviewID.Uint64(),
				pending.Comments,
			)),
		}
	case change.PendingReviewUnavailable, change.PendingReviewLimited,
		change.PendingReviewAmbiguous:
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText("pending GitHub review is " + pending.Status.String() +
				": " + pending.Detail.String()),
		}
	}
	return model, nil
}

// Init returns the receiver's init value.
func (model *Model) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return initializeMsg{} },
		model.receiver.Arm(model.coordinator),
	)
}

// Update performs the update operation.
func (model *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	helpCommand, childCommand := model.updateChildren(message)
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		model.applyWindowSize(message)
	case initializeMsg:
		if !model.initialized {
			model.initialized = true
			return model.finishUpdate(
				helpCommand,
				childCommand,
				model.requestWorkspaceIndex(),
			)
		}
	case mouseActionMsg:
		if mouseCommand := model.applyMouseAction(message); mouseCommand != nil {
			return model.finishUpdate(helpCommand, childCommand, mouseCommand)
		}
	case fileFilterChunkMsg:
		return model.finishUpdate(helpCommand, childCommand, model.advanceFileFilter(message))
	case treeMoveChunkMsg:
		return model.finishUpdate(helpCommand, childCommand, model.advanceTreeMove(message))
	case collapseCaptureStepMsg:
		return model.finishUpdate(helpCommand, childCommand, model.advanceCollapseCapture(message))
	case findDebounceMsg:
		return model.finishUpdate(helpCommand, childCommand, model.beginFileFind(message))
	case findChunkMsg:
		return model.finishUpdate(helpCommand, childCommand, model.advanceFileFind(message))
	case tea.KeyPressMsg:
		return model.finishUpdate(helpCommand, childCommand, model.applyKeyPress(message))
	case outcomeReadyMsg:
		return model.finishUpdate(helpCommand, childCommand, model.handleReady(message))
	case chordTimeoutMsg:
		model.keys.Timeout(message.generation)
	}
	return model.finishUpdate(helpCommand, childCommand)
}

func (model *Model) updateChildren(message tea.Msg) (tea.Cmd, tea.Cmd) {
	if _, isKey := message.(tea.KeyPressMsg); isKey {
		return nil, nil
	}
	var helpCommand tea.Cmd
	if model.overlay == OverlayHelp {
		model.help, helpCommand = model.help.Update(message)
	}
	if _, isPaste := message.(tea.PasteMsg); isPaste &&
		(model.belowMinimum() || model.overlay != OverlayNone && model.overlay != OverlayFind) {
		return helpCommand, nil
	}
	var childCommand tea.Cmd
	if model.overlay == OverlayFind {
		return helpCommand, model.updateFindInput(message)
	}
	switch model.mode {
	case ModeNormal, ModeVisual:
	case ModeCommentEdit:
		childCommand = model.updateCommentEditor(message)
	case ModeCommand:
		childCommand = model.updatePromptInput(message)
	}
	return helpCommand, childCommand
}

func (model *Model) applyWindowSize(message tea.WindowSizeMsg) {
	wasBelowMinimum := model.belowMinimum()
	if message.Width > 0 && message.Height > 0 {
		model.size = TerminalSize{Width: message.Width, Height: message.Height, Known: true}
		model.help.SetWidth(message.Width)
		model.resizeCommentEditor()
		model.resizePrompts()
		model.clampFocus()
		model.clampDiffTop()
	}
	if wasBelowMinimum != model.belowMinimum() {
		model.abandonChord()
	}
}

func (model *Model) applyMouseAction(message mouseActionMsg) tea.Cmd {
	if model.overlay == OverlayFind {
		if message.findGeneration != model.find.generation || model.find.phase != findReady {
			return nil
		}
		handled := message.delta != 0 ||
			message.target == mouseTargetFind &&
				message.findIndex >= 0 && message.findIndex < model.findResultCount()
		if handled {
			model.acknowledgeNotice()
		}
		if message.delta != 0 {
			model.moveFind(message.delta)
		}
		if message.target == mouseTargetFind &&
			message.findIndex >= 0 && message.findIndex < model.findResultCount() {
			model.find.cursor = message.findIndex
			model.clampFindTop()
			model.previewFindSelection(findSelectionPreview)
		}
		return nil
	}
	if message.reviewState != model.review.StateRevision() ||
		model.mode != ModeNormal || model.overlay != OverlayNone || model.belowMinimum() ||
		!model.focusVisible(message.focus) {
		return nil
	}
	model.acknowledgeNotice()
	model.setFocus(message.focus)
	model.focusReturn = 0
	if message.delta != 0 || message.target != mouseTargetNone {
		model.foreground.clearPendingDiffTarget()
	}
	var command tea.Cmd
	if message.delta != 0 {
		command = model.move(message.delta)
	}
	switch message.target {
	case mouseTargetNone, mouseTargetFind:
	case mouseTargetTree:
		model.selectTreeNode(message.tree)
	case mouseTargetDiff:
		model.diffCursor = message.diffLine
		model.moveDiff(0)
	case mouseTargetComment:
		model.selectCommentIndex(message.commentIndex)
	}
	return command
}

func (model *Model) applyKeyPress(message tea.KeyPressMsg) tea.Cmd {
	switch {
	case model.belowMinimum():
		return model.handleKey(message)
	case model.overlay == OverlayFind:
		return model.handleFindKey(message)
	case model.mode == ModeCommentEdit:
		return model.handleCommentEditorKey(message)
	case model.mode == ModeCommand:
		return model.handlePromptKey(message)
	default:
		return model.handleKey(message)
	}
}

func (model *Model) finishUpdate(commands ...tea.Cmd) (tea.Model, tea.Cmd) {
	model.refreshCoordinatorStats()
	return model, tea.Batch(commands...)
}

// Review returns the receiver's review value.
func (model *Model) Review() change.Review {
	return model.review
}

// TerminalSize returns the receiver's terminal size value.
func (model *Model) TerminalSize() TerminalSize {
	return model.size
}

// CloseSourceDocument releases the current source after Program.Run and the
// coordinator's worker join have completed.
func (model *Model) CloseSourceDocument() error {
	cleanups := model.cleanupSources
	if cleanup := model.detachCollapseCaptureCleanup(); cleanup != nil {
		cleanups = append(cleanups, cleanup)
	}
	if model.document != nil {
		cleanups = append(cleanups, model.document.Retire())
	}
	var closeErrors []error
	for _, cleanup := range cleanups {
		if cleanup != nil {
			closeErrors = append(closeErrors, cleanup.Close())
		}
	}
	model.document = nil
	model.cleanupSources = nil
	return errors.Join(closeErrors...)
}

// MaterializedRows returns the receiver's materialized rows value.
func (model *Model) MaterializedRows() int {
	if !model.size.Known || model.belowMinimum() ||
		model.overlay != OverlayNone && model.overlay != OverlayFind {
		_, height := model.renderSize()
		return height
	}
	findRows := 0
	if model.overlay == OverlayFind {
		findRows = model.findResultRows()
	}
	bodyHeight := max(min(model.size.Height, maxRenderHeight)-2, 1)
	rows := 0
	if model.showFileTree() {
		rows += min(len(model.fileOrder), bodyHeight)
	}
	active, ok := model.review.ActiveFile()
	if !ok {
		return rows + 1 + findRows
	}
	state := model.review.Patch(active)
	if state.Status != change.PatchReady {
		return rows + 1 + findRows
	}
	remaining := max(int(state.Lines)-int(model.diffTop), 0)
	return rows + max(1, min(remaining, bodyHeight+viewportOverscan)) + findRows
}

func (model *Model) handleReady(message outcomeReadyMsg) tea.Cmd {
	var outcomeCommand tea.Cmd
	switch message.state {
	case ReadyAvailable:
		outcome, ok := model.coordinator.ClaimNext()
		if ok {
			outcomeCommand = model.applyOutcome(outcome)
		}
	case ReadyClosed:
		model.coordinatorClosed = true
	case ReadyCancelled:
		if !model.quitting {
			model.notice = &Notice{Kind: NoticeWarning, Text: "work result receiver stopped"}
		}
	}
	if model.coordinatorClosed || model.quitting {
		return outcomeCommand
	}
	return tea.Batch(outcomeCommand, model.receiver.Arm(model.coordinator))
}

func (model *Model) applyOutcome(outcome Outcome) tea.Cmd {
	if progress, ok := outcome.(Progress); ok {
		model.operationProgress[progress.ID] = progress
		return nil
	}
	delete(model.operationProgress, outcome.operationID())
	switch outcome := outcome.(type) {
	case PatchOutcome:
		model.applyPatchOutcome(outcome)
		return nil
	case commentMutationOutcome:
		return model.applyCommentMutationOutcome(outcome)
	case SearchOutcome:
		model.applySearchOutcome(outcome)
		return nil
	case PublicationCheckOutcome:
		model.applyPublicationCheckOutcome(outcome)
		return nil
	case PublicationCommitOutcome:
		model.applyPublicationCommitOutcome(outcome)
		return nil
	case ReconciliationOutcome:
		model.applyReconciliationOutcome(outcome)
		return nil
	case SyntaxOutcome:
		model.applySyntaxOutcome(outcome)
		return nil
	case ExportOutcome:
		return model.applyExportOutcome(outcome)
	case ReloadAcquiredOutcome:
		return model.applyReloadAcquired(outcome)
	case ReloadPreparedOutcome:
		return model.applyReloadPrepared(outcome)
	case FileTreeOutcome:
		return model.applyFileFilterTree(outcome)
	case WorkspaceIndexOutcome:
		return model.applyWorkspaceIndex(outcome)
	case DeliveryOutcome:
		model.applyDeliveryOutcome(outcome)
		return nil
	default:
		model.notice = &Notice{Kind: NoticeError, Text: "unknown work outcome"}
		return nil
	}
}

func (model *Model) applyPatchOutcome(patch PatchOutcome) {
	fileID, target, tracked, effect := model.foreground.completePatch(patch.ID)
	if !tracked {
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale patch outcome"}
		return
	}
	status := change.OperationFailed
	detail := change.EmptyOperationDetail()
	result := patch.Result
	if patch.Err != nil {
		detail = boundedOperationDetail(patch.Err.Error())
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(patch.Err.Error())}
		result = model.patchFailureResult(
			fileID,
			patchStatusForWorkError(patch.Err),
			patch.Err,
		)
	}
	next, applyErr := model.review.ApplyPatch(result)
	if applyErr != nil && patch.Err == nil {
		detail = boundedOperationDetail(applyErr.Error())
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(applyErr.Error())}
		result = model.patchFailureResult(fileID, change.PatchFailed, applyErr)
		next, applyErr = model.review.ApplyPatch(result)
	}
	if applyErr != nil {
		detail = boundedOperationDetail(applyErr.Error())
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(applyErr.Error())}
	} else {
		model.review = next
		status = operationStatusForPatch(result.Status)
		if patch.Err == nil {
			detail = boundedOperationDetail(result.Detail.String())
		}
		if model.review.RetainedPatchBytes() > MaxPresentationCacheBytes {
			model.review = model.review.DropPatch(fileID)
			status = change.OperationLimited
			detail = boundedOperationDetail("patch presentation cache capacity reached")
			model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(detail.String())}
		}
	}
	if next, err := model.review.ResolveOperation(patch.ID, status, detail); err == nil {
		model.review = next
	}
	file, fileExists := model.review.Snapshot().File(fileID)
	if target != nil && (!fileExists || target.workspace != model.review.WorkspaceRevision() ||
		target.handle != file.PatchHandle()) {
		target = nil
	}
	patchReady := model.review.Patch(fileID).Status == change.PatchReady
	if active, exists := model.review.ActiveFile(); exists && active == fileID && patchReady {
		if viewport, ok := model.foreground.takeReloadViewport(fileID); ok {
			model.diffTop = viewport.top
			model.diffCursor = viewport.cursor
			model.visualAnchor = viewport.visualAnchor
			model.moveDiff(0)
		} else {
			model.diffTop = 0
			model.diffCursor = 0
		}
		if target != nil {
			model.applyDiffTarget(*target)
		}
	} else if active, exists := model.review.ActiveFile(); exists && active == fileID {
		model.foreground.clearReloadViewport(fileID)
	}
	restoredViewport := false
	if active, exists := model.review.ActiveFile(); exists && active == fileID {
		restoredViewport = model.applyPendingFindRestore(fileID)
	}
	if active, exists := model.review.ActiveFile(); exists && active == fileID && patchReady &&
		!restoredViewport {
		model.requestSyntax()
	}
	if effect.kind == foregroundEffectRequestPatch &&
		result.Status == change.PatchReady {
		model.review = model.review.DropPatch(fileID)
	}
	model.applyForegroundEffect(effect)
}

func (model *Model) patchFailureResult(
	fileID change.FileID,
	status change.PatchStatus,
	failure error,
) change.PatchResult {
	file, _ := model.review.Snapshot().File(fileID)
	return change.PatchResult{
		Workspace: model.review.WorkspaceRevision(),
		Handle:    file.PatchHandle(),
		Status:    status,
		Detail:    change.PatchDetail(boundedOperationDetail(failure.Error()).String()),
	}
}

func patchStatusForWorkError(err error) change.PatchStatus {
	switch operationStatusForWorkError(err) {
	case change.OperationCancelled:
		return change.PatchCancelled
	case change.OperationLimited:
		return change.PatchLimited
	default:
		return change.PatchFailed
	}
}

func operationStatusForPatch(status change.PatchStatus) change.OperationStatus {
	switch status {
	case change.PatchPending:
		panic("pending patch cannot resolve an operation")
	case change.PatchReady:
		return change.OperationSucceeded
	case change.PatchCancelled:
		return change.OperationCancelled
	case change.PatchLimited:
		return change.OperationLimited
	case change.PatchUnavailable, change.PatchFailed:
		return change.OperationFailed
	}
	panic("invalid patch status")
}

func (model *Model) requestActivePatch() {
	fileID, ok := model.review.ActiveFile()
	if !ok {
		return
	}
	start, cancellations, ready := model.foreground.requestPatch(fileID)
	for _, operationID := range cancellations {
		model.coordinator.Cancel(operationID)
	}
	if !ready {
		return
	}
	model.beginPatch(start)
}

func (model *Model) beginPatch(start foregroundStart) {
	next, request, err := model.review.RequestPatch(start.file, model.settings.patchContext)
	if err != nil {
		model.rejectAndApplyForegroundStart(start)
		return
	}
	operationID, err := change.NewOperationID(model.nextOperation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: operationIdentityExhausted}
		model.rejectAndApplyForegroundStart(start)
		return
	}
	model.nextOperation++
	next, err = next.BeginOperation(operationID, change.OperationPatch)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		model.rejectAndApplyForegroundStart(start)
		return
	}
	if err := model.coordinator.SubmitPatch(PatchWork{
		ID: operationID, Document: model.document, Request: request,
	}); err != nil {
		status := change.PatchFailed
		operationStatus := change.OperationFailed
		if errors.Is(err, ErrCoordinatorSaturated) {
			status = change.PatchLimited
			operationStatus = change.OperationLimited
		}
		failed := change.PatchResult{
			Workspace: request.Workspace,
			Handle:    request.Handle,
			Status:    status,
			Detail:    change.PatchDetail(err.Error()),
		}
		if failedReview, applyErr := next.ApplyPatch(failed); applyErr == nil {
			model.review = failedReview
			if resolvedReview, resolveErr := model.review.ResolveOperation(
				operationID,
				operationStatus,
				boundedOperationDetail(err.Error()),
			); resolveErr == nil {
				model.review = resolvedReview
			}
		}
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		model.rejectAndApplyForegroundStart(start)
		return
	}
	if !model.foreground.admitted(start, operationID) {
		model.coordinator.Cancel(operationID)
		model.foreground.clearPendingDiffTarget()
		err := errors.New("foreground patch admission invariant violated")
		model.review = resolveRejectedOperation(next, operationID, err)
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
}

func (model *Model) rejectAndApplyForegroundStart(start foregroundStart) {
	effect, ok := model.foreground.rejected(start)
	if !ok {
		model.notice = &Notice{
			Kind: NoticeError,
			Text: "foreground rejection invariant violated",
		}
		return
	}
	model.applyForegroundEffect(effect)
}

func (model *Model) applyForegroundEffect(effect foregroundEffect) {
	switch effect.kind {
	case foregroundEffectNone:
		return
	case foregroundEffectRequestPatch:
		start, ok := model.foreground.preparePatch(effect.file)
		if !ok {
			model.notice = &Notice{Kind: NoticeError, Text: "foreground patch continuation invariant violated"}
			return
		}
		model.beginPatch(start)
	case foregroundEffectStartSearch:
		start, ok := model.foreground.prepareSearch(effect.search)
		if !ok {
			model.notice = &Notice{Kind: NoticeError, Text: "foreground search continuation invariant violated"}
			return
		}
		model.beginSearch(start)
	case foregroundEffectStartReload:
		start, ok := model.foreground.prepareReload()
		if !ok {
			model.notice = &Notice{Kind: NoticeError, Text: "foreground reload continuation invariant violated"}
			return
		}
		model.beginReload(start)
	case foregroundEffectApplyPatchContext:
		model.review = model.review.ClearSearch()
		model.applyPatchContext(effect.context)
	case foregroundEffectClearSearch:
		model.review = model.review.ClearSearch()
		model.searchCursor = 0
		model.notice = &Notice{Kind: NoticeInformation, Text: "search cleared"}
	}
}

func (model *Model) operationSummary() string {
	stats := model.coordinatorStats
	if stats.Accepted == 0 {
		return readyStatusText
	}
	return "Working " + strconv.Itoa(stats.Accepted)
}

func (model *Model) refreshCoordinatorStats() {
	model.coordinatorStats = model.coordinator.Stats()
}
