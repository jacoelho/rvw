package tui

import (
	"cmp"
	"container/heap"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/tui/render"
)

const (
	findDebounce          = 100 * time.Millisecond
	maxFindResults        = 256
	maxFindFilesPerUpdate = 256
	maxFindBytesPerUpdate = 64 << 10
	invalidFindQuery      = "find query is not valid UTF-8"
)

type findScope uint8

const (
	findCurrent findScope = iota + 1
	findReview
	findFiles
)

type findPhase uint8

const (
	findPrompt findPhase = iota + 1
	findDebouncing
	findMatching
	findSearching
	findReady
	findFailed
)

type findReturnState struct {
	treeCursor       treeNodeIdentity
	commentSelection commentRowIdentity
	diffLeft         int
	active           change.FileID
	selected         change.FileID
	diffTop          uint32
	diffCursor       uint32
	visualAnchor     uint32
	focus            FocusOwner
	focusReturn      FocusOwner
	activeOK         bool
	selectedOK       bool
}

type fileFindResult struct {
	file  change.FileID
	score int
	order int
}

type fileFindHeap []fileFindResult

func (results *fileFindHeap) Len() int { return len(*results) }

func (results *fileFindHeap) Less(left, right int) bool {
	if (*results)[left].score != (*results)[right].score {
		return (*results)[left].score < (*results)[right].score
	}
	return (*results)[left].order > (*results)[right].order
}

func (results *fileFindHeap) Swap(left, right int) {
	(*results)[left], (*results)[right] = (*results)[right], (*results)[left]
}

func (results *fileFindHeap) Push(value any) {
	result, ok := value.(fileFindResult)
	if !ok {
		panic("file find heap received an invalid value")
	}
	*results = append(*results, result)
}

func (results *fileFindHeap) Pop() any {
	last := len(*results) - 1
	value := (*results)[last]
	*results = (*results)[:last]
	return value
}

type fileFindBuild struct {
	query      string
	best       fileFindHeap
	generation uint64
	position   int
	matches    int
}

type findPalette struct {
	candidates      []change.FileID
	fileResults     []fileFindResult
	failure         string
	submittedQuery  string
	fileBuild       *fileFindBuild
	returnState     findReturnState
	searchOperation change.OperationID
	generation      uint64
	cursor          int
	top             int
	totalMatches    int
	scope           findScope
	phase           findPhase
	limited         bool
}

type findDebounceMsg struct {
	generation uint64
}

type findChunkMsg struct {
	generation uint64
}

func (model *Model) openFind(scope findScope) tea.Cmd {
	model.abandonChord()
	model.findRestore = nil
	model.find = findPalette{
		scope:       scope,
		phase:       findPrompt,
		generation:  1,
		candidates:  model.fileOrder,
		returnState: model.captureFindReturnState(),
	}
	model.overlay = OverlayFind
	model.searchInput.SetValue("")
	model.configureFindInput()
	if scope == findFiles {
		model.prepareEmptyFileFind()
	}
	model.resizePrompts()
	return model.searchInput.Focus()
}

func (model *Model) captureFindReturnState() findReturnState {
	active, activeOK := model.review.ActiveFile()
	selected, selectedOK := model.review.SelectedFile()
	return findReturnState{
		treeCursor:       model.treeCursor,
		active:           active,
		selected:         selected,
		focus:            model.focus,
		focusReturn:      model.focusReturn,
		diffLeft:         model.diffLeft,
		commentSelection: model.currentCommentSelection(),
		diffTop:          model.diffTop,
		diffCursor:       model.diffCursor,
		visualAnchor:     model.visualAnchor,
		activeOK:         activeOK,
		selectedOK:       selectedOK,
	}
}

func (model *Model) configureFindInput() {
	model.searchInput.CharLimit = maxPromptBytes
	model.searchInput.Prompt = "/ "
	model.searchInput.Placeholder = "search current patch"
	switch model.find.scope {
	case findCurrent:
	case findReview:
		model.searchInput.Placeholder = "search visible patches"
	case findFiles:
		model.searchInput.Prompt = "> "
		model.searchInput.Placeholder = "find a visible file"
		model.searchInput.CharLimit = int(change.MaxPathBytes)
	}
}

type findCloseDisposition uint8

const (
	findCloseCancel findCloseDisposition = iota + 1
	findCloseAccept
)

func (model *Model) closeFind(disposition findCloseDisposition) {
	state := model.find.returnState
	if disposition == findCloseCancel && model.find.phase == findSearching {
		model.cancelSearch()
	}
	model.find.generation++
	model.find.fileBuild = nil
	model.searchInput.Blur()
	model.overlay = OverlayNone
	model.abandonChord()
	model.find = findPalette{}
	if disposition == findCloseAccept {
		if model.syntaxDeferred {
			model.requestSyntax()
		}
		return
	}
	model.restoreFindReturnState(state)
	if model.syntaxDeferred {
		model.requestSyntax()
	}
}

func (model *Model) restoreFindReturnState(state findReturnState) {
	model.foreground.clearPendingDiffTarget()
	model.findRestore = nil
	if state.activeOK {
		active, activeOK := model.review.ActiveFile()
		if !activeOK || active != state.active {
			if !model.activateFile(state.active) {
				return
			}
		}
	}
	if state.selectedOK {
		if next, err := model.review.SelectFile(state.selected); err == nil {
			model.review = next
		}
	}
	model.treeCursor = state.treeCursor
	model.reconcileCommentSelection(state.commentSelection)
	model.focus = state.focus
	model.focusReturn = state.focusReturn
	if !state.activeOK {
		return
	}
	if model.review.Patch(state.active).Status == change.PatchReady {
		model.applyFindReturnViewport(state)
		return
	}
	model.findRestore = new(state)
	model.requestActivePatch()
}

func (model *Model) applyFindReturnViewport(state findReturnState) {
	model.diffTop = state.diffTop
	model.diffCursor = state.diffCursor
	model.diffLeft = state.diffLeft
	model.visualAnchor = state.visualAnchor
	model.moveDiff(0)
	model.clampFocus()
}

func (model *Model) applyPendingFindRestore(file change.FileID) bool {
	if model.findRestore == nil || !model.findRestore.activeOK ||
		model.findRestore.active != file {
		return false
	}
	state := *model.findRestore
	model.findRestore = nil
	if model.review.Patch(file).Status == change.PatchReady {
		model.applyFindReturnViewport(state)
		return true
	}
	return false
}

func (model *Model) handleFindKey(message tea.KeyPressMsg) tea.Cmd {
	stroke := keyStrokeFrom(message)
	if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
		if action == keymap.ActionNone {
			return command
		}
		model.acknowledgeNotice()
		switch action {
		case keymap.ActionCancel, keymap.ActionInterrupt:
			model.closeFind(findCloseCancel)
		case keymap.ActionAccept:
			return model.acceptFind()
		case keymap.ActionNextSearchScope:
			return model.switchFindScope(1)
		case keymap.ActionPreviousSearchScope:
			return model.switchFindScope(-1)
		case keymap.ActionMoveUp:
			model.moveFind(-1)
		case keymap.ActionMoveDown:
			model.moveFind(1)
		case keymap.ActionPageUp:
			model.moveFind(-max(model.findResultRows(), 1))
		case keymap.ActionPageDown:
			model.moveFind(max(model.findResultRows(), 1))
		case keymap.ActionDeleteBackward:
			message = tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
		default:
			return nil
		}
		if action != keymap.ActionDeleteBackward {
			return nil
		}
	}
	return model.updateFindInput(message)
}

func (model *Model) updateFindInput(message tea.Msg) tea.Cmd {
	limit := maxPromptBytes
	if model.find.scope == findFiles {
		limit = int(change.MaxPathBytes)
	}
	if paste, ok := message.(tea.PasteMsg); ok {
		switch {
		case !utf8.ValidString(paste.Content):
			model.find.phase = findFailed
			model.find.failure = invalidFindQuery
			return nil
		case len(paste.Content) > limit:
			model.find.phase = findFailed
			model.find.failure = "find query exceeds " + strconv.Itoa(limit) + " bytes"
			return nil
		}
	}
	previous := model.searchInput.Value()
	previousPosition := model.searchInput.Position()
	previousFind := model.find
	updated, command := model.searchInput.Update(message)
	model.searchInput = updated
	value := model.searchInput.Value()
	if !utf8.ValidString(value) {
		model.searchInput.SetValue(previous)
		model.searchInput.SetCursor(previousPosition)
		model.find.phase = findFailed
		model.find.failure = invalidFindQuery
		return command
	}
	if len(value) > limit {
		model.searchInput.SetValue(previous)
		model.searchInput.SetCursor(previousPosition)
		model.find.phase = findFailed
		model.find.failure = "find query exceeds " + strconv.Itoa(limit) + " bytes"
		return command
	}
	if render.ContainsBidiControl(value) {
		model.searchInput.SetValue(previous)
		model.searchInput.SetCursor(previousPosition)
		model.find = previousFind
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: "find query contains bidirectional control",
		}
		return command
	}
	if value == previous {
		return command
	}
	model.find.failure = ""
	model.find.submittedQuery = ""
	model.find.cursor = 0
	model.find.top = 0
	if model.find.scope == findFiles {
		return tea.Batch(command, model.scheduleFileFind())
	}
	if model.find.phase == findSearching {
		model.cancelSearch()
	}
	model.find.searchOperation = 0
	model.find.phase = findPrompt
	return command
}

func (model *Model) switchFindScope(delta int) tea.Cmd {
	if model.find.phase == findSearching {
		model.cancelSearch()
	}
	model.find.searchOperation = 0
	model.find.generation++
	model.find.fileBuild = nil
	model.find.fileResults = nil
	model.find.failure = ""
	model.find.submittedQuery = ""
	model.find.cursor = 0
	model.find.top = 0
	model.find.totalMatches = 0
	model.find.limited = false
	scopes := [...]findScope{findCurrent, findReview, findFiles}
	value := (slices.Index(scopes[:], model.find.scope) + delta + len(scopes)) % len(scopes)
	model.find.scope = scopes[value]
	model.find.phase = findPrompt
	model.configureFindInput()
	model.resizePrompts()
	if model.find.scope == findFiles {
		return model.scheduleFileFind()
	}
	return nil
}

func (model *Model) acceptFind() tea.Cmd {
	if model.find.scope == findFiles {
		if model.find.phase != findReady || model.findResultCount() == 0 {
			return nil
		}
		model.previewFindSelection(findSelectionAccept)
		model.closeFind(findCloseAccept)
		return nil
	}
	if model.find.phase == findSearching {
		return nil
	}
	if model.find.phase == findReady && model.find.submittedQuery == model.searchInput.Value() &&
		model.findResultCount() != 0 {
		model.previewFindSelection(findSelectionAccept)
		model.closeFind(findCloseAccept)
		return nil
	}
	model.submitFindText()
	return nil
}

func (model *Model) submitFindText() {
	query := model.searchInput.Value()
	if _, err := change.NewSearchQuery(query); err != nil {
		model.find.phase = findFailed
		model.find.failure = err.Error()
		return
	}
	var files []change.FileID
	switch model.find.scope {
	case findCurrent:
		active, ok := model.review.ActiveFile()
		if !ok {
			model.find.phase = findFailed
			model.find.failure = "review has no active file"
			return
		}
		files = []change.FileID{active}
	case findReview:
		files = model.find.candidates
	default:
		return
	}
	model.find.phase = findSearching
	model.find.failure = ""
	model.find.submittedQuery = query
	model.find.cursor = 0
	model.find.top = 0
	model.startSearch(query, files)
}

func (model *Model) failFindSearch(err error) {
	if model.overlay != OverlayFind || err == nil {
		return
	}
	model.find.phase = findFailed
	model.find.failure = err.Error()
}

func (model *Model) completeFindSearch(operation change.OperationID) {
	if model.overlay != OverlayFind || model.find.searchOperation != operation {
		return
	}
	summary := model.review.SearchSummary()
	if summary.Status == change.SearchFailed {
		model.find.phase = findFailed
		model.find.failure = summary.Detail.String()
		return
	}
	model.find.phase = findReady
	model.find.cursor = 0
	model.find.top = 0
	model.find.failure = ""
}

func (model *Model) scheduleFileFind() tea.Cmd {
	model.find.generation++
	model.find.fileBuild = nil
	model.find.fileResults = nil
	model.find.totalMatches = 0
	model.find.limited = false
	model.find.cursor = 0
	model.find.top = 0
	if model.searchInput.Value() == "" {
		model.prepareEmptyFileFind()
		return nil
	}
	model.find.phase = findDebouncing
	generation := model.find.generation
	return findDebounceCmd(model.commandContext, generation)
}

func (model *Model) prepareEmptyFileFind() {
	model.find.phase = findReady
	model.find.totalMatches = len(model.find.candidates)
	model.find.cursor = 0
	if active, ok := model.review.ActiveFile(); ok {
		if position, exists := slices.BinarySearch(model.find.candidates, active); exists {
			model.find.cursor = position
		}
	}
	model.clampFindTop()
}

func findDebounceCmd(ctx context.Context, generation uint64) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(findDebounce)
		defer timer.Stop()
		select {
		case <-timer.C:
			return findDebounceMsg{generation: generation}
		case <-ctx.Done():
			return nil
		}
	}
}

func (model *Model) beginFileFind(message findDebounceMsg) tea.Cmd {
	if model.overlay != OverlayFind || model.find.scope != findFiles ||
		model.find.generation != message.generation || model.searchInput.Value() == "" {
		return nil
	}
	model.find.phase = findMatching
	model.find.fileBuild = &fileFindBuild{
		query:      model.searchInput.Value(),
		generation: message.generation,
		best:       make(fileFindHeap, 0, maxFindResults),
	}
	return func() tea.Msg { return findChunkMsg(message) }
}

func (model *Model) advanceFileFind(message findChunkMsg) tea.Cmd {
	build := model.find.fileBuild
	if model.overlay != OverlayFind || model.find.scope != findFiles || build == nil ||
		build.generation != message.generation || model.find.generation != message.generation {
		return nil
	}
	start := build.position
	end := start
	pathBytes := 0
	for end < len(model.find.candidates) && end-start < maxFindFilesPerUpdate {
		file, exists := model.review.Snapshot().File(model.find.candidates[end])
		if !exists {
			model.find.fileBuild = nil
			model.find.phase = findFailed
			model.find.failure = "find candidate is unavailable"
			return nil
		}
		path := file.DisplayPath()
		if end != start && pathBytes+len(path) > maxFindBytesPerUpdate {
			break
		}
		pathBytes += len(path)
		order := end
		end++
		score, matched := scoreFileFind(build.query, path)
		if !matched {
			continue
		}
		result := fileFindResult{
			file:  model.find.candidates[order],
			score: score,
			order: order,
		}
		build.matches++
		if len(build.best) < maxFindResults {
			heap.Push(&build.best, result)
			continue
		}
		worst := build.best[0]
		if betterFileFindResult(result, worst) {
			heap.Pop(&build.best)
			heap.Push(&build.best, result)
		}
	}
	build.position = end
	if end < len(model.find.candidates) {
		generation := build.generation
		return func() tea.Msg { return findChunkMsg{generation: generation} }
	}
	results := slices.Clone(build.best)
	slices.SortFunc(results, compareFileFindResults)
	model.find.fileResults = results
	model.find.totalMatches = build.matches
	model.find.limited = build.matches > len(results)
	model.find.fileBuild = nil
	model.find.phase = findReady
	model.find.cursor = 0
	model.find.top = 0
	if len(results) != 0 {
		model.previewFindSelection(findSelectionPreview)
	}
	return nil
}

func scoreFileFind(query, path string) (int, bool) {
	if query == "" {
		return 0, false
	}
	queryOffset := 0
	queryRune, querySize := utf8.DecodeRuneInString(query)
	lastMatch := -2
	position := 0
	score := 0
	var previous rune
	for _, candidate := range path {
		if equalFoldFindRune(queryRune, candidate) {
			if queryOffset == 0 {
				score -= min(position, 15)
				if position == 0 {
					score += 10
				}
			}
			if position == lastMatch+1 {
				score += 5
			}
			if position != 0 && strings.ContainsRune("/-_ .\\", previous) {
				score += 20
			}
			if unicode.IsLower(previous) && unicode.IsUpper(candidate) {
				score += 20
			}
			if queryRune == candidate {
				score++
			}
			lastMatch = position
			queryOffset += querySize
			if queryOffset == len(query) {
				score -= utf8.RuneCountInString(path) - position - 1
				return score, true
			}
			queryRune, querySize = utf8.DecodeRuneInString(query[queryOffset:])
		}
		previous = candidate
		position++
	}
	return 0, false
}

func equalFoldFindRune(left, right rune) bool {
	return left == right || unicode.ToLower(left) == unicode.ToLower(right)
}

func betterFileFindResult(left, right fileFindResult) bool {
	return compareFileFindResults(left, right) < 0
}

func compareFileFindResults(left, right fileFindResult) int {
	if score := cmp.Compare(right.score, left.score); score != 0 {
		return score
	}
	return cmp.Compare(left.order, right.order)
}

func (model *Model) findResultCount() int {
	if model.find.scope == findFiles {
		if model.searchInput.Value() == "" {
			return len(model.find.candidates)
		}
		return len(model.find.fileResults)
	}
	if model.find.phase != findReady || model.find.submittedQuery != model.searchInput.Value() {
		return 0
	}
	return model.review.SearchSummary().HitCount
}

func (model *Model) findFileAt(index int) (change.FileID, bool) {
	if index < 0 || index >= model.findResultCount() {
		return 0, false
	}
	if model.searchInput.Value() == "" {
		return model.find.candidates[index], true
	}
	return model.find.fileResults[index].file, true
}

func (model *Model) moveFind(delta int) {
	count := model.findResultCount()
	if count == 0 || model.find.phase != findReady {
		return
	}
	model.find.cursor = min(max(model.find.cursor+delta, 0), count-1)
	model.clampFindTop()
	model.previewFindSelection(findSelectionPreview)
}

func (model *Model) clampFindTop() {
	rows := max(model.findResultRows(), 1)
	if model.find.cursor < model.find.top {
		model.find.top = model.find.cursor
	} else if model.find.cursor >= model.find.top+rows {
		model.find.top = model.find.cursor - rows + 1
	}
	maximum := max(model.findResultCount()-rows, 0)
	model.find.top = min(max(model.find.top, 0), maximum)
}

type findSelectionDisposition uint8

const (
	findSelectionPreview findSelectionDisposition = iota + 1
	findSelectionAccept
)

func (model *Model) previewFindSelection(disposition findSelectionDisposition) {
	if model.find.scope == findFiles {
		file, ok := model.findFileAt(model.find.cursor)
		if !ok || disposition == findSelectionPreview && !model.find.returnState.activeOK {
			return
		}
		model.foreground.clearPendingDiffTarget()
		if model.activateFile(file) {
			model.requestActivePatch()
		}
		return
	}
	if model.find.cursor < 0 || model.find.cursor >= model.findResultCount() {
		return
	}
	model.activateSearchHit(model.find.cursor)
}

func (model *Model) findStatusText() string {
	switch model.find.phase {
	case findPrompt:
		if model.find.scope == findFiles {
			return "Type to find a visible file"
		}
		return "Enter searches changed patch lines"
	case findDebouncing:
		return "Waiting for input…"
	case findMatching:
		if model.find.fileBuild == nil {
			return "Matching visible files…"
		}
		return fmt.Sprintf(
			"Matching visible files %d/%d…",
			model.find.fileBuild.position,
			len(model.find.candidates),
		)
	case findSearching:
		if progress, ok := model.operationProgress[model.find.searchOperation]; ok && progress.Total != 0 {
			return fmt.Sprintf("Searching patches %d/%d…", progress.Completed, progress.Total)
		}
		return "Searching changed patches…"
	case findFailed:
		return "Error: " + model.find.failure
	case findReady:
		count := model.findResultCount()
		position := 0
		if count != 0 {
			position = min(max(model.find.cursor, 0), count-1) + 1
		}
		matchPosition := fmt.Sprintf("%d of %d", position, count)
		if model.find.scope != findFiles {
			summary := model.review.SearchSummary()
			switch summary.Status {
			case change.SearchPartial:
				return fmt.Sprintf("%s · %d files unavailable", matchPosition, summary.UnavailableFiles)
			case change.SearchLimited:
				return matchPosition + " · limit reached"
			case change.SearchCancelled:
				return "Search cancelled"
			case change.SearchFailed:
				return "Error: " + summary.Detail.String()
			case change.SearchIdle, change.SearchActive, change.SearchComplete,
				change.SearchSuperseded:
			}
		}
		if model.find.scope == findFiles && model.find.limited {
			return fmt.Sprintf("%s · %d total", matchPosition, model.find.totalMatches)
		}
		return matchPosition
	default:
		return ""
	}
}

func (scope findScope) label() string {
	switch scope {
	case findCurrent:
		return "Current"
	case findReview:
		return "Review"
	case findFiles:
		return "Files"
	default:
		return "Find"
	}
}

func findLineLocation(hit change.SearchHit) string {
	oldLine, oldOK := hit.OldLine()
	newLine, newOK := hit.NewLine()
	switch {
	case oldOK && newOK:
		return strconv.FormatUint(uint64(oldLine.Uint32()), 10) + "/" +
			strconv.FormatUint(uint64(newLine.Uint32()), 10)
	case newOK:
		return "+" + strconv.FormatUint(uint64(newLine.Uint32()), 10)
	case oldOK:
		return "-" + strconv.FormatUint(uint64(oldLine.Uint32()), 10)
	default:
		return "patch " + strconv.FormatUint(uint64(hit.PatchLine()+1), 10)
	}
}

func findTabs(active findScope, styles semanticStyles) string {
	labels := make([]string, 0, 3)
	for scope := findCurrent; scope <= findFiles; scope++ {
		label := " " + scope.label() + " "
		if scope == active {
			label = styles.selectedControl.Render(label)
		} else {
			label = styles.muted.Render(label)
		}
		labels = append(labels, label)
	}
	return strings.Join(labels, " ")
}

type findModalLayout struct {
	dialog     dialogLayout
	inputX     int
	inputY     int
	resultY    int
	resultRows int
}

func newFindModalLayout(width, height int) findModalLayout {
	dialog := newDialogLayout(width, height, dialogSpec{
		preferredInner: dialogSize{width: 96, height: 20},
		borderMinimum:  dialogSize{width: 48, height: 12},
	})
	layout := findModalLayout{
		dialog:  dialog,
		inputX:  dialog.inner.x,
		inputY:  dialog.inner.y + 1,
		resultY: dialog.inner.y + 3,
	}
	layout.resultRows = max(dialog.inner.height-5, 1)
	return layout
}

func (model *Model) findResultRows() int {
	width, height := model.renderSize()
	return newFindModalLayout(width, height).resultRows
}

func (model *Model) findModalView(
	width, height int,
	styles semanticStyles,
) (string, findModalLayout, []int) {
	layout := newFindModalLayout(width, height)
	innerWidth := layout.dialog.inner.width
	innerHeight := layout.dialog.inner.height
	lines := make([]string, 0, innerHeight)
	title := modalTitle(styles, "Find") + "  " + findTabs(model.find.scope, styles)
	lines = append(lines, fitCellWidth(title, innerWidth))
	searchInput := model.searchInput
	inputStyles := searchInput.Styles()
	inputStyles.Focused.Prompt = styles.heading.Inherit(styles.panel)
	inputStyles.Focused.Text = styles.panel
	inputStyles.Focused.Placeholder = styles.muted.Inherit(styles.panel)
	inputStyles.Focused.Suggestion = styles.muted.Inherit(styles.panel)
	inputStyles.Blurred = inputStyles.Focused
	inputStyles.Cursor.Color = styles.panel.GetForeground()
	searchInput.SetStyles(inputStyles)
	lines = append(lines, fitCellWidth(searchInput.View(), innerWidth))
	status := safeAndClip(model.findStatusText(), innerWidth)
	if model.find.phase == findFailed {
		status = styles.failure.Render(status)
	} else {
		status = styles.muted.Render(status)
	}
	lines = append(lines, fitCellWidth(status, innerWidth))

	rows := make([]int, 0, layout.resultRows)
	count := model.findResultCount()
	start := min(max(model.find.top, 0), max(count-1, 0))
	for offset := range layout.resultRows {
		index := start + offset
		line := ""
		if index < count {
			line = model.findResultLine(index, innerWidth, styles)
			rows = append(rows, index)
		} else {
			rows = append(rows, -1)
			if offset == 0 && model.find.phase == findReady {
				line = styles.muted.Render("  No matches")
			}
		}
		lines = append(lines, fitCellWidth(line, innerWidth))
	}
	actions := []keymap.Action{keymap.ActionCancel}
	acceptLabel := model.findAcceptLabel()
	if acceptLabel != "" {
		actions = append(actions, keymap.ActionAccept)
	}
	actions = append(actions, keymap.ActionNextSearchScope, keymap.ActionMoveDown)
	controls := bindingBar{
		bindings: model.settings.keymap.BindingsFor(
			keymap.Scope{Context: keymap.ContextSearch}, actions,
		),
		labels: map[keymap.Action]string{keymap.ActionAccept: acceptLabel},
	}.RenderRows(innerWidth, 2, styles, styles.panel)
	for len(lines)+len(controls) < innerHeight {
		lines = append(lines, strings.Repeat(" ", innerWidth))
	}
	for _, control := range controls {
		lines = append(lines, fitStyledCellWidth(control, innerWidth, styles.panel))
	}
	content := strings.Join(lines[:innerHeight], "\n")
	return frameDialogContent(content, layout.dialog, styles), layout, rows
}

func (model *Model) findAcceptLabel() string {
	if model.find.phase == findSearching {
		return ""
	}
	if model.find.scope == findFiles {
		if model.find.phase == findReady && model.findResultCount() != 0 {
			return "open file"
		}
		return ""
	}
	if model.find.phase == findReady && model.find.submittedQuery == model.searchInput.Value() &&
		model.findResultCount() != 0 {
		return "open result"
	}
	return searchLabel
}

func (model *Model) findResultLine(index, width int, styles semanticStyles) string {
	prefix := "  "
	if index == model.find.cursor {
		prefix = "> "
	}
	line := prefix
	if model.find.scope == findFiles {
		fileID, ok := model.findFileAt(index)
		if !ok {
			return ""
		}
		file, ok := model.review.Snapshot().File(fileID)
		if !ok {
			return ""
		}
		line += safeAndClip(file.DisplayPath(), max(width-2, 1))
	} else {
		hit, ok := model.review.SearchHit(index)
		if !ok {
			return ""
		}
		file, ok := model.review.Snapshot().File(hit.FileID())
		if !ok {
			return ""
		}
		location := findLineLocation(hit)
		pathWidth := max(width-len(location)-6, 1)
		line += safeAndClip(file.DisplayPath(), pathWidth) + ":" + location
		if excerpt := hit.Excerpt(); excerpt != "" {
			line += "  " + safeAndClip(excerpt, max(width-lipgloss.Width(line)-2, 1))
		}
	}
	line = fitCellWidth(line, width)
	if index == model.find.cursor {
		return styles.activeCursorRow.Render(line)
	}
	return line
}
