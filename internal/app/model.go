package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

const chordTimeout = 750 * time.Millisecond

type FocusOwner uint8

const (
	FocusFileTree FocusOwner = iota + 1
	FocusDiff
	FocusComments
)

type Overlay uint8

const (
	OverlayNone Overlay = iota
	OverlayHelp
	OverlayStatus
	OverlayConfirm
	OverlayExport
	OverlaySubmit
)

type InteractionMode uint8

const (
	ModeNormal InteractionMode = iota + 1
	ModeVisual
	ModeCommentEdit
	ModeSearch
	ModeCommand
)

type NoticeKind uint8

const (
	NoticeInformation NoticeKind = iota + 1
	NoticeWarning
	NoticeError
)

type NoticeText string

type Notice struct {
	Kind NoticeKind
	Text NoticeText
}

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
	reviewState  uint64
	focus        FocusOwner
	delta        int
	target       mouseTarget
	tree         treeNodeIdentity
	diffLine     uint32
	commentIndex int
}

type fileFilterChunkMsg struct {
	generation uint64
}

type treeMoveChunkMsg struct {
	generation uint64
}

type exportRequest struct {
	format      review.ExportFormat
	destination delivery.Destination
}

type exportSelection struct {
	format      review.ExportFormat
	destination delivery.Destination
	options     review.ExportOptions
}

type pendingSearch struct {
	query review.SearchQuery
	scope review.SearchScope
}

type Model struct {
	commandContext   context.Context
	document         *source.Document
	cleanupDocuments []*source.Document
	coordinator      *Coordinator
	receiver         *ReadyReceiver
	settings         Settings
	help             help.Model
	commentEditor    textarea.Model
	searchInput      textinput.Model
	commandInput     textinput.Model
	review           review.Review

	size              TerminalSize
	focus             FocusOwner
	focusReturn       FocusOwner
	overlay           Overlay
	mode              InteractionMode
	notice            *Notice
	initialized       bool
	quitting          bool
	coordinatorClosed bool
	coordinatorStats  CoordinatorStats
	workspaceIndex    review.OperationID

	fileOrder            []review.FileID
	allFileOrder         []review.FileID
	visibleFileOrder     []review.FileID
	allFileTree          fileTree
	visibleFileTree      fileTree
	filteredFileTree     *fileTree
	treeCursor           treeNodeIdentity
	collapsedTrees       map[string]struct{}
	pathFiles            map[string]review.FileID
	ambiguousPaths       map[string]struct{}
	filterQuery          string
	filterGeneration     uint64
	filterBuild          *fileFilterBuild
	filterTreeBuilding   bool
	filterTreeOperation  review.OperationID
	filterTreeGeneration uint64
	filterTreeFiles      []review.FileID
	diffTop              uint32
	diffCursor           uint32
	diffLeft             int
	visualAnchor         uint32
	commentCursor        int
	commentsVisible      bool
	fileTreeVisible      bool
	helpTop              int
	statusTop            int

	nextOperation          uint64
	foreground             foregroundState
	operationProgress      map[review.OperationID]Progress
	exportOperations       map[review.OperationID]exportRequest
	deliveryOperations     map[review.OperationID]delivery.Destination
	pendingStdout          *review.PreparedExport
	searchCursor           int
	syntaxOperation        review.OperationID
	syntaxFile             review.FileID
	syntaxOld              *review.PreparedSyntax
	syntaxNew              *review.PreparedSyntax
	syntaxDeferred         bool
	syntaxSuppressed       map[review.SyntaxIdentity]review.SyntaxStatus
	publicationOperation   review.OperationID
	publicationCancel      bool
	publicationAllowStale  bool
	publicationMoveSummary bool
	publicationExcluded    map[review.CommentID]struct{}
	publicationCursor      int
	publicationDetailTop   int
	confirmation           confirmation
	exportPicker           exportSelection

	chord              pendingKeyChord
	repeat             uint16
	treeMovePending    int
	treeMoveGeneration uint64
}

type pendingKeyChord struct {
	strokes    []string
	context    KeyContext
	focus      KeyFocus
	generation uint64
}

func NewModel(
	ctx context.Context,
	document *source.Document,
	settings Settings,
	coordinator *Coordinator,
	receiver *ReadyReceiver,
) (*Model, error) {
	if ctx == nil || document == nil || coordinator == nil || receiver == nil {
		return nil, errors.New("interactive model requires context, document, coordinator, and receiver")
	}
	settings = settings.clone()
	current, err := review.NewReview(document.Snapshot())
	if err != nil {
		return nil, err
	}
	helpModel := help.New()
	helpModel.ShowAll = true
	editor := textarea.New()
	editor.Placeholder = "Write a review comment"
	editor.ShowLineNumbers = false
	editor.CharLimit = int(review.MaxCommentBodyBytes)
	editor.MaxContentHeight = 4_096
	searchInput := textinput.New()
	searchInput.Prompt = "/ "
	searchInput.Placeholder = "search changed patches"
	searchInput.CharLimit = maxPromptBytes
	commandInput := textinput.New()
	commandInput.Prompt = ": "
	commandInput.Placeholder = "command"
	commandInput.CharLimit = maxPromptBytes
	model := &Model{
		commandContext:      ctx,
		document:            document,
		coordinator:         coordinator,
		receiver:            receiver,
		settings:            settings,
		help:                helpModel,
		commentEditor:       editor,
		searchInput:         searchInput,
		commandInput:        commandInput,
		review:              current,
		focus:               FocusDiff,
		mode:                ModeNormal,
		commentsVisible:     false,
		fileTreeVisible:     true,
		nextOperation:       1,
		foreground:          newForegroundState(),
		operationProgress:   make(map[review.OperationID]Progress),
		exportOperations:    make(map[review.OperationID]exportRequest),
		deliveryOperations:  make(map[review.OperationID]delivery.Destination),
		publicationExcluded: make(map[review.CommentID]struct{}),
		syntaxSuppressed:    make(map[review.SyntaxIdentity]review.SyntaxStatus),
		collapsedTrees:      make(map[string]struct{}),
	}
	model.refreshCoordinatorStats()
	return model, nil
}

func (model *Model) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return initializeMsg{} },
		model.receiver.Arm(model.coordinator),
	)
}

func (model *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var helpCommand tea.Cmd
	var childCommand tea.Cmd
	if model.overlay == OverlayHelp {
		if _, isKey := message.(tea.KeyPressMsg); !isKey {
			model.help, helpCommand = model.help.Update(message)
		}
	}
	if _, isKey := message.(tea.KeyPressMsg); !isKey {
		switch model.mode {
		case ModeCommentEdit:
			model.commentEditor, childCommand = model.commentEditor.Update(message)
		case ModeSearch:
			model.searchInput, childCommand = model.searchInput.Update(message)
		case ModeCommand:
			model.commandInput, childCommand = model.commandInput.Update(message)
		}
	}
	switch message := message.(type) {
	case tea.WindowSizeMsg:
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
	case initializeMsg:
		if !model.initialized {
			model.initialized = true
			model.requestWorkspaceIndex()
		}
	case mouseActionMsg:
		var mouseCommand tea.Cmd
		if message.reviewState == model.review.StateRevision() &&
			model.mode == ModeNormal && model.overlay == OverlayNone && !model.belowMinimum() &&
			model.focusVisible(message.focus) {
			model.setFocus(message.focus)
			model.focusReturn = 0
			if message.delta != 0 || message.target != mouseTargetNone {
				model.foreground.clearPendingDiffTarget()
			}
			if message.delta != 0 {
				mouseCommand = model.move(message.delta)
			}
			switch message.target {
			case mouseTargetTree:
				model.selectTreeNode(message.tree)
			case mouseTargetDiff:
				model.diffCursor = message.diffLine
				model.moveDiff(0)
			case mouseTargetComment:
				model.commentCursor = message.commentIndex
			}
		}
		if mouseCommand != nil {
			model.refreshCoordinatorStats()
			return model, tea.Batch(helpCommand, childCommand, mouseCommand)
		}
	case fileFilterChunkMsg:
		command := tea.Batch(helpCommand, childCommand, model.advanceFileFilter(message))
		model.refreshCoordinatorStats()
		return model, command
	case treeMoveChunkMsg:
		command := tea.Batch(helpCommand, childCommand, model.advanceTreeMove(message))
		model.refreshCoordinatorStats()
		return model, command
	case tea.KeyPressMsg:
		var command tea.Cmd
		if model.belowMinimum() {
			command = model.handleKey(message)
		} else if model.mode == ModeCommentEdit {
			command = model.handleCommentEditorKey(message)
		} else if model.mode == ModeSearch || model.mode == ModeCommand {
			command = model.handlePromptKey(message)
		} else {
			command = model.handleKey(message)
		}
		command = tea.Batch(helpCommand, childCommand, command)
		model.refreshCoordinatorStats()
		return model, command
	case outcomeReadyMsg:
		command := tea.Batch(helpCommand, childCommand, model.handleReady(message))
		model.refreshCoordinatorStats()
		return model, command
	case chordTimeoutMsg:
		if message.generation == model.chord.generation && len(model.chord.strokes) != 0 {
			model.abandonChord()
		}
	}
	model.refreshCoordinatorStats()
	return model, tea.Batch(helpCommand, childCommand)
}

func (model *Model) Review() review.Review {
	return model.review
}

func (model *Model) TerminalSize() TerminalSize {
	return model.size
}

// CloseSourceDocument releases the current source after Program.Run and the
// coordinator's worker join have completed.
func (model *Model) CloseSourceDocument() error {
	documents := model.cleanupDocuments
	if model.document != nil {
		documents = append(documents, model.document)
	}
	var closeErrors []error
	for _, document := range documents {
		if document != nil {
			closeErrors = append(closeErrors, document.Close())
		}
	}
	model.document = nil
	model.cleanupDocuments = nil
	return errors.Join(closeErrors...)
}

func (model *Model) MaterializedRows() int {
	if !model.size.Known || model.belowMinimum() || model.overlay != OverlayNone {
		_, height := model.renderSize()
		return height
	}
	bodyHeight := max(min(model.size.Height, maxRenderHeight)-2, 1)
	rows := 0
	if model.showFileTree() {
		rows += min(len(model.fileOrder), bodyHeight)
	}
	active, ok := model.review.ActiveFile()
	if !ok {
		return rows + 1
	}
	state := model.review.Patch(active)
	if state.Status != review.PatchReady {
		return rows + 1
	}
	remaining := max(int(state.Lines)-int(model.diffTop), 0)
	return rows + max(1, min(remaining, bodyHeight+viewportOverscan))
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
		model.applyReloadAcquired(outcome)
		return nil
	case ReloadPreparedOutcome:
		model.applyReloadPrepared(outcome)
		return nil
	case FileTreeOutcome:
		return model.applyFileFilterTree(outcome)
	case WorkspaceIndexOutcome:
		model.applyWorkspaceIndex(outcome)
		return nil
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
	status := review.OperationFailed
	detail := review.EmptyOperationDetail()
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
		result = model.patchFailureResult(fileID, review.PatchFailed, applyErr)
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
			status = review.OperationLimited
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
	patchReady := model.review.Patch(fileID).Status == review.PatchReady
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
		model.requestSyntax()
	} else if active, exists := model.review.ActiveFile(); exists && active == fileID {
		model.foreground.clearReloadViewport(fileID)
	}
	if effect.kind == foregroundEffectRequestPatch &&
		result.Status == review.PatchReady {
		model.review = model.review.DropPatch(fileID)
	}
	model.applyForegroundEffect(effect)
}

func (model *Model) patchFailureResult(
	fileID review.FileID,
	status review.PatchStatus,
	failure error,
) review.PatchResult {
	file, _ := model.review.Snapshot().File(fileID)
	return review.PatchResult{
		Workspace: model.review.WorkspaceRevision(),
		Handle:    file.PatchHandle(),
		Status:    status,
		Detail:    review.PatchDetail(boundedOperationDetail(failure.Error()).String()),
	}
}

func patchStatusForWorkError(err error) review.PatchStatus {
	switch operationStatusForWorkError(err) {
	case review.OperationCancelled:
		return review.PatchCancelled
	case review.OperationLimited:
		return review.PatchLimited
	default:
		return review.PatchFailed
	}
}

func operationStatusForPatch(status review.PatchStatus) review.OperationStatus {
	switch status {
	case review.PatchReady:
		return review.OperationSucceeded
	case review.PatchCancelled:
		return review.OperationCancelled
	case review.PatchLimited:
		return review.OperationLimited
	case review.PatchUnavailable:
		return review.OperationFailed
	default:
		return review.OperationFailed
	}
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
	operationID, err := review.NewOperationID(model.nextOperation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: "operation identity exhausted"}
		model.rejectAndApplyForegroundStart(start)
		return
	}
	model.nextOperation++
	next, err = next.BeginOperation(operationID, review.OperationPatch)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		model.rejectAndApplyForegroundStart(start)
		return
	}
	if err := model.coordinator.SubmitPatch(PatchWork{
		ID: operationID, Document: model.document, Request: request,
	}); err != nil {
		status := review.PatchFailed
		operationStatus := review.OperationFailed
		if errors.Is(err, ErrCoordinatorSaturated) {
			status = review.PatchLimited
			operationStatus = review.OperationLimited
		}
		failed := review.PatchResult{
			Workspace: request.Workspace,
			Handle:    request.Handle,
			Status:    status,
			Detail:    review.PatchDetail(err.Error()),
		}
		if next, applyErr := next.ApplyPatch(failed); applyErr == nil {
			model.review = next
			if next, resolveErr := model.review.ResolveOperation(
				operationID,
				operationStatus,
				boundedOperationDetail(err.Error()),
			); resolveErr == nil {
				model.review = next
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

func (model *Model) handleKey(message tea.KeyPressMsg) tea.Cmd {
	stroke := normalizeStroke(message.String())
	if stroke == "" {
		return nil
	}
	if model.foreground.reloadIsPreparing() {
		if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
			if action == KeyActionNone {
				return command
			}
			repeat := model.takeRepeat(action)
			if action == KeyActionQuit || action == KeyActionInterrupt ||
				action == KeyActionCancel {
				return model.applyAction(action, repeat)
			}
		}
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: "finishing reload; only cancel or quit is available",
		}
		return nil
	}
	model.invalidateChordForOwnerChange()
	if model.acceptRepeatDigit(stroke) {
		return nil
	}
	if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
		if action == KeyActionNone {
			return command
		}
		return model.applyAction(action, model.takeRepeat(action))
	}
	model.repeat = 0
	return nil
}

// resolveKeyStroke owns chord accumulation for every key context. A pending
// prefix is consumed; when it fails to extend, the current stroke is retried as
// the start of a new chord so modal text inputs receive only unbound strokes.
func (model *Model) resolveKeyStroke(stroke string) (KeyAction, bool, tea.Cmd) {
	if stroke == "" {
		return KeyActionNone, false, nil
	}
	model.invalidateChordForOwnerChange()
	if action, exact, prefix := model.resolveAction(append(model.chord.strokes, stroke)); exact {
		model.completeChord()
		return action, true, nil
	} else if prefix {
		model.chord.strokes = append(model.chord.strokes, stroke)
		if len(model.chord.strokes) == 1 {
			model.chord.context = model.keyContext()
			model.chord.focus = model.configFocus()
		}
		model.chord.generation++
		return KeyActionNone, true,
			chordTimeoutCmd(model.commandContext, model.chord.generation)
	}
	model.abandonChord()
	if action, exact, prefix := model.resolveAction([]string{stroke}); exact {
		return action, true, nil
	} else if prefix {
		model.chord.strokes = []string{stroke}
		model.chord.context = model.keyContext()
		model.chord.focus = model.configFocus()
		model.chord.generation++
		return KeyActionNone, true,
			chordTimeoutCmd(model.commandContext, model.chord.generation)
	}
	return KeyActionNone, false, nil
}

func (model *Model) invalidateChordForOwnerChange() {
	if len(model.chord.strokes) == 0 ||
		model.chord.context == model.keyContext() &&
			model.chord.focus == model.configFocus() {
		return
	}
	model.abandonChord()
}

func (model *Model) completeChord() {
	if len(model.chord.strokes) != 0 {
		model.chord.generation++
	}
	model.chord.strokes = nil
	model.chord.context = ""
	model.chord.focus = ""
}

func (model *Model) abandonChord() {
	model.completeChord()
	model.repeat = 0
}

func (model *Model) acceptRepeatDigit(stroke string) bool {
	if model.belowMinimum() || model.overlay != OverlayNone ||
		model.mode != ModeNormal && model.mode != ModeVisual ||
		len(model.chord.strokes) != 0 || len(stroke) != 1 ||
		stroke[0] < '0' || stroke[0] > '9' {
		return false
	}
	digit := uint16(stroke[0] - '0')
	if model.repeat == 0 && digit == 0 {
		return false
	}
	next := uint32(model.repeat)*10 + uint32(digit)
	if next > 9_999 {
		model.repeat = 0
		model.notice = &Notice{Kind: NoticeWarning, Text: "numeric repeat must not exceed 9999"}
		return true
	}
	model.repeat = uint16(next)
	return true
}

func (model *Model) takeRepeat(action KeyAction) int {
	repeat := model.repeat
	model.repeat = 0
	if repeat == 0 || !repeatableAction(action) {
		return 1
	}
	return int(repeat)
}

func repeatableAction(action KeyAction) bool {
	switch action {
	case KeyActionFocusNextPane, KeyActionFocusPreviousPane,
		KeyActionMoveUp, KeyActionMoveDown,
		KeyActionPageUp, KeyActionPageDown,
		KeyActionHalfPageUp, KeyActionHalfPageDown,
		KeyActionNextFile, KeyActionPreviousFile,
		KeyActionNextHunk, KeyActionPreviousHunk,
		KeyActionNextComment, KeyActionPreviousComment,
		KeyActionExtendUp, KeyActionExtendDown:
		return true
	default:
		return false
	}
}

func (model *Model) handleCommentEditorKey(message tea.KeyPressMsg) tea.Cmd {
	stroke := normalizeStroke(message.String())
	if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
		if action == KeyActionNone {
			return command
		}
		switch action {
		case KeyActionDeleteBackward:
			return model.updateCommentEditor(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
		case KeyActionInsertNewline:
			return model.updateCommentEditor(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		default:
			return model.applyAction(action, 1)
		}
	}
	return model.updateCommentEditor(message)
}

func (model *Model) updateCommentEditor(message tea.KeyPressMsg) tea.Cmd {
	previous := model.commentEditor.Value()
	var command tea.Cmd
	model.commentEditor, command = model.commentEditor.Update(message)
	value := model.commentEditor.Value()
	if int64(len(value)) > review.MaxCommentBodyBytes {
		model.commentEditor.SetValue(previous)
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText(
				"comment body limit reached: " + strconv.FormatInt(review.MaxCommentBodyBytes, 10) +
					" bytes",
			),
		}
		return nil
	}
	next, err := model.review.UpdateDraft(value)
	if err != nil {
		model.commentEditor.SetValue(previous)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return nil
	}
	model.review = next
	return command
}

func (model *Model) resolveAction(strokes []string) (KeyAction, bool, bool) {
	contextName := model.keyContext()
	var (
		exactAction KeyAction
		prefix      bool
	)
	for _, binding := range model.effectiveBindings(contextName) {
		bindingStrokes := normalizedChord(binding.Chord)
		if len(strokes) > len(bindingStrokes) {
			continue
		}
		matches := true
		for index := range strokes {
			if strokes[index] != bindingStrokes[index] {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if len(strokes) == len(bindingStrokes) {
			exactAction = binding.Action
		} else {
			prefix = true
		}
	}
	return exactAction, exactAction != KeyActionNone, prefix
}

func (model *Model) keyContext() KeyContext {
	if model.belowMinimum() {
		return KeyContextMinimumSize
	}
	if model.overlay == OverlayConfirm {
		return KeyContextConfirm
	}
	switch {
	case model.mode == ModeVisual:
		return KeyContextVisual
	case model.mode == ModeCommentEdit:
		return KeyContextCommentEdit
	case model.mode == ModeSearch:
		return KeyContextSearch
	case model.mode == ModeCommand:
		return KeyContextCommand
	case model.overlay == OverlayHelp:
		return KeyContextHelp
	case model.overlay == OverlayStatus:
		return KeyContextStatusDetails
	case model.overlay == OverlayExport:
		return KeyContextExportPicker
	case model.overlay == OverlaySubmit:
		return KeyContextSubmit
	default:
		return KeyContextNormal
	}
}

func (model *Model) effectiveBindings(contextName KeyContext) []Keybinding {
	focus := model.configFocus()
	bindings := make([]Keybinding, 0, len(model.settings.keybindings))
	positions := make(map[string]int)
	for _, binding := range model.settings.keybindings {
		if binding.Context != contextName ||
			binding.Focus != KeyFocusNone && binding.Focus != focus {
			continue
		}
		if contextName == KeyContextMinimumSize &&
			binding.Action != KeyActionQuit &&
			binding.Action != KeyActionInterrupt {
			continue
		}
		chord := strings.Join(normalizedChord(binding.Chord), " ")
		if position, exists := positions[chord]; exists {
			if binding.Focus != KeyFocusNone {
				bindings[position] = binding
			}
			continue
		}
		positions[chord] = len(bindings)
		bindings = append(bindings, binding)
	}
	return bindings
}

func chordTimeoutCmd(ctx context.Context, generation uint64) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(chordTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			return chordTimeoutMsg{generation: generation}
		case <-ctx.Done():
			return nil
		}
	}
}

func normalizedChord(chord KeyChord) []string {
	fields := strings.Fields(chord.String())
	for index := range fields {
		fields[index] = normalizeStroke(fields[index])
	}
	return fields
}

func normalizeStroke(value string) string {
	if value == " " {
		return "space"
	}
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "+", "-")
	if len([]rune(value)) == 1 {
		return value
	}
	switch strings.ToLower(value) {
	case "return":
		return "enter"
	case "escape":
		return "esc"
	default:
		return strings.ToLower(value)
	}
}

func (model *Model) operationSummary() string {
	stats := model.coordinatorStats
	return "work " + strconv.Itoa(stats.Accepted) + "/" + strconv.Itoa(MaxAcceptedOperations)
}

func (model *Model) refreshCoordinatorStats() {
	model.coordinatorStats = model.coordinator.Stats()
}
