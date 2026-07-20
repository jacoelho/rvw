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
	patchOperations        map[review.OperationID]review.FileID
	operationProgress      map[review.OperationID]Progress
	exportOperations       map[review.OperationID]exportRequest
	deliveryOperations     map[review.OperationID]delivery.Destination
	deferredPatch          *review.FileID
	reloadOperation        review.OperationID
	reloadDeferred         bool
	reloadPreparing        bool
	reloadPreparationTries uint8
	reloadViewport         *diffViewport
	pendingStdout          *review.PreparedExport
	searchOperation        review.OperationID
	pendingSearch          *pendingSearch
	pendingPatchContext    *review.PatchContext
	searchCursor           int
	pendingSearchHit       *review.SearchHit
	clearSearchAfterCancel bool
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

	chord              []string
	chordGeneration    uint64
	repeat             uint16
	treeMovePending    int
	treeMoveGeneration uint64
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
	current, err := review.NewReview(document.Snapshot(), settings.commentKinds)
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
		patchOperations:     make(map[review.OperationID]review.FileID),
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
			model.clampDiffTop()
		}
		if wasBelowMinimum != model.belowMinimum() {
			model.chord = nil
			model.repeat = 0
		}
	case initializeMsg:
		if !model.initialized {
			model.initialized = true
			model.requestWorkspaceIndex()
		}
	case mouseActionMsg:
		var mouseCommand tea.Cmd
		if message.reviewState == model.review.StateRevision() &&
			model.mode == ModeNormal && model.overlay == OverlayNone && !model.belowMinimum() {
			model.focus = message.focus
			model.focusReturn = 0
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
		if model.belowMinimum() && model.overlay != OverlayConfirm {
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
		if message.generation == model.chordGeneration {
			model.chord = nil
			model.repeat = 0
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
	if progress, ok := outcome.Progress(); ok {
		model.operationProgress[progress.ID] = progress
		return nil
	}
	delete(model.operationProgress, outcome.operationID())
	patch, ok := outcome.Patch()
	if ok {
		model.applyPatchOutcome(patch)
		return nil
	}
	if search, ok := outcome.Search(); ok {
		model.applySearchOutcome(search)
		return nil
	}
	if publication, ok := outcome.Publication(); ok {
		model.applyPublicationOutcome(publication)
		return nil
	}
	if syntax, ok := outcome.Syntax(); ok {
		model.applySyntaxOutcome(syntax)
		return nil
	}
	if export, ok := outcome.Export(); ok {
		return model.applyExportOutcome(export)
	}
	if reload, ok := outcome.Reload(); ok {
		model.applyReloadOutcome(reload)
		return nil
	}
	if tree, ok := outcome.FileTree(); ok {
		return model.applyFileFilterTree(tree)
	}
	if index, ok := outcome.WorkspaceIndex(); ok {
		model.applyWorkspaceIndex(index)
		return nil
	}
	if deliveryOutcome, ok := outcome.Delivery(); ok {
		model.applyDeliveryOutcome(deliveryOutcome)
		return nil
	}
	model.notice = &Notice{Kind: NoticeError, Text: "unknown work outcome"}
	return nil
}

func (model *Model) applyPatchOutcome(patch PatchOutcome) {
	fileID, tracked := model.patchOperations[patch.ID]
	delete(model.patchOperations, patch.ID)
	if !tracked {
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale patch outcome"}
		return
	}
	status := review.OperationFailed
	detail := review.EmptyOperationDetail
	if patch.Err != nil {
		detail = boundedOperationDetail(patch.Err.Error())
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(patch.Err.Error())}
	} else {
		var err error
		model.review, err = model.review.ApplyPatch(patch.Result)
		if err != nil {
			detail = boundedOperationDetail(err.Error())
			model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		} else {
			status = operationStatusForPatch(patch.Result.Status)
			detail = boundedOperationDetail(patch.Result.Detail.String())
			if model.review.RetainedPatchBytes() > MaxPresentationCacheBytes {
				model.review = model.review.DropPatch(fileID)
				status = review.OperationLimited
				detail = "patch presentation cache capacity reached"
				model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(detail)}
			}
		}
	}
	if next, err := model.review.ResolveOperation(patch.ID, status, detail); err == nil {
		model.review = next
	}
	if active, exists := model.review.ActiveFile(); exists && active == fileID &&
		patch.Result.Status == review.PatchReady {
		if model.reloadViewport != nil && model.reloadViewport.file == fileID {
			model.diffTop = model.reloadViewport.top
			model.diffCursor = model.reloadViewport.cursor
			model.visualAnchor = model.reloadViewport.visualAnchor
			model.reloadViewport = nil
			model.moveDiff(0)
		} else {
			model.diffTop = 0
			model.diffCursor = 0
		}
		if model.pendingSearchHit != nil &&
			model.pendingSearchHit.FileID() == fileID {
			model.diffCursor = model.pendingSearchHit.PatchLine()
			model.pendingSearchHit = nil
			model.moveDiff(0)
		}
		model.requestSyntax()
	} else if active, exists := model.review.ActiveFile(); exists && active == fileID {
		model.reloadViewport = nil
	}
	if model.reloadDeferred && len(model.patchOperations) == 0 {
		model.reloadDeferred = false
		model.deferredPatch = nil
		model.startReload()
	} else if model.pendingSearch != nil && len(model.patchOperations) == 0 {
		pending := *model.pendingSearch
		model.pendingSearch = nil
		model.deferredPatch = nil
		model.beginSearch(pending.query, pending.scope)
	} else if model.deferredPatch != nil && len(model.patchOperations) == 0 {
		if patch.Result.Status == review.PatchReady {
			model.review = model.review.DropPatch(fileID)
		}
		model.deferredPatch = nil
		model.requestActivePatch()
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
	if len(model.patchOperations) != 0 {
		for operationID := range model.patchOperations {
			model.coordinator.Cancel(operationID)
		}
		model.deferredPatch = new(fileID)
		return
	}
	next, request, err := model.review.RequestPatch(fileID, model.settings.patchContext)
	if err != nil {
		return
	}
	operationID, err := review.NewOperationID(model.nextOperation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: "operation identity exhausted"}
		return
	}
	model.nextOperation++
	next, err = next.BeginOperation(operationID, review.OperationPatch)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
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
		return
	}
	model.review = next
	model.patchOperations[operationID] = fileID
}

func (model *Model) handleKey(message tea.KeyPressMsg) tea.Cmd {
	stroke := normalizeStroke(message.String())
	if stroke == "" {
		return nil
	}
	if model.reloadPreparing {
		if action, exact, _ := model.resolveAction([]string{stroke}); exact &&
			(action == KeyActionQuit || action == KeyActionInterrupt ||
				action == KeyActionCancel) {
			return model.applyAction(action, 1)
		}
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: "finishing reload; only cancel or quit is available",
		}
		return nil
	}
	if model.acceptRepeatDigit(stroke) {
		return nil
	}
	if action, exact, prefix := model.resolveAction(append(model.chord, stroke)); exact {
		model.chord = nil
		return model.applyAction(action, model.takeRepeat(action))
	} else if prefix {
		model.chord = append(model.chord, stroke)
		model.chordGeneration++
		return chordTimeoutCmd(model.commandContext, model.chordGeneration)
	}
	model.chord = nil
	model.repeat = 0
	if action, exact, prefix := model.resolveAction([]string{stroke}); exact {
		return model.applyAction(action, 1)
	} else if prefix {
		model.chord = []string{stroke}
		model.chordGeneration++
		return chordTimeoutCmd(model.commandContext, model.chordGeneration)
	}
	return nil
}

func (model *Model) acceptRepeatDigit(stroke string) bool {
	if model.belowMinimum() || model.overlay != OverlayNone ||
		model.mode != ModeNormal && model.mode != ModeVisual ||
		len(model.chord) != 0 || len(stroke) != 1 ||
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
	if action, exact, _ := model.resolveAction([]string{stroke}); exact {
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
	if model.overlay == OverlayConfirm {
		return KeyContextConfirm
	}
	if model.belowMinimum() {
		return KeyContextMinimumSize
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
