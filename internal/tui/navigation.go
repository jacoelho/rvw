package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/projection"
)

const maxFileRecordsPerUpdate = 2_048

type fileFilterBuild struct {
	query      string
	source     []change.FileID
	result     []change.FileID
	generation uint64
	position   int
}

type projectionPhase uint8

const (
	projectionInitial projectionPhase = iota + 1
	projectionLoading
	projectionReady
	projectionFailed
)

type fileProjectionState struct {
	workspaceDetail NoticeText
	filterDetail    NoticeText
	requestedQuery  string
	appliedQuery    string
	workspace       projectionPhase
	filter          projectionPhase
}

// FileTreeWork represents file tree work.
type FileTreeWork struct {
	// Files transfers exclusive ownership to the coordinator after successful submission.
	Files      []change.FileID
	Tree       fileTree
	ID         change.OperationID
	Generation uint64
}

// FileTreeOutcome describes file tree outcome.
type FileTreeOutcome struct {
	Err error
	// Files returns the unchanged work backing for transfer into model state.
	Files      []change.FileID
	Projection treeProjection
	ID         change.OperationID
	Generation uint64
}

// WorkspaceIndexWork represents workspace index work.
type WorkspaceIndexWork struct {
	Collapse collapseSnapshot
	Snapshot change.Snapshot
	ID       change.OperationID
}

// WorkspaceIndexOutcome describes workspace index outcome.
type WorkspaceIndexOutcome struct {
	Err               error
	FileTree          fileTree
	VisibleProjection treeProjection
	Collapsed         collapsedTreeState
	Index             projection.Index
	ID                change.OperationID
}

func workspaceIndexAllowance(snapshot change.Snapshot) (int64, error) {
	retained := snapshot.RetainedBytes()
	if retained < 0 || retained > MaxRetainedResultBytes {
		return 0, fmt.Errorf(
			"%w: snapshot retains %d bytes; limit is %d",
			projection.ErrRetainedLimited,
			retained,
			MaxRetainedResultBytes,
		)
	}
	return MaxRetainedResultBytes - retained, nil
}

func buildWorkspaceIndex(
	ctx context.Context,
	snapshot change.Snapshot,
) (WorkspaceIndexOutcome, error) {
	ledger, err := projection.NewLedger(MaxRetainedResultBytes, snapshot.RetainedBytes())
	if err != nil {
		return WorkspaceIndexOutcome{}, err
	}
	return buildWorkspaceIndexWithLedger(ctx, snapshot, collapseSnapshot{}, ledger)
}

func buildWorkspaceIndexWithLedger(
	ctx context.Context,
	snapshot change.Snapshot,
	collapse collapseSnapshot,
	ledger *projection.Ledger,
) (WorkspaceIndexOutcome, error) {
	var outcome WorkspaceIndexOutcome
	var err error
	outcome.Index, err = projection.BuildIndex(ctx, snapshot, ledger)
	if err != nil {
		return WorkspaceIndexOutcome{}, err
	}
	outcome.FileTree, err = buildFileTreeWithLedger(
		ctx,
		snapshot,
		outcome.Index.AllFiles(),
		ledger,
	)
	if err != nil {
		return WorkspaceIndexOutcome{}, err
	}
	if len(outcome.Index.VisibleFiles()) == len(outcome.Index.AllFiles()) {
		outcome.VisibleProjection = nil
	} else {
		outcome.VisibleProjection, err = outcome.FileTree.projectWithLedger(
			ctx,
			outcome.Index.VisibleFiles(),
			ledger,
		)
		if err != nil {
			return WorkspaceIndexOutcome{}, err
		}
	}
	outcome.Collapsed, err = buildCollapsedTreeState(ctx, outcome.FileTree, collapse, ledger)
	if err != nil {
		return WorkspaceIndexOutcome{}, err
	}
	allowance, err := workspaceIndexAllowance(snapshot)
	if err != nil {
		return WorkspaceIndexOutcome{}, err
	}
	if outcome.RetainedBytes() > allowance {
		return WorkspaceIndexOutcome{}, fmt.Errorf(
			"%w: workspace index retains %d bytes; limit is %d",
			projection.ErrRetainedLimited,
			outcome.RetainedBytes(),
			allowance,
		)
	}
	return outcome, nil
}

// RetainedBytes returns the workspace index ownership, excluding its borrowed
// source snapshot.
func (outcome WorkspaceIndexOutcome) RetainedBytes() int64 {
	return retainedWorkspaceIndexBytes(
		outcome.FileTree,
		outcome.VisibleProjection,
		outcome.Index,
	) + outcome.Collapsed.retainedBytes()
}

func (model *Model) move(delta int) tea.Cmd {
	switch model.focus {
	case FocusFileTree:
		return model.queueTreeMove(delta)
	case FocusComments:
		model.moveComment(delta)
	default:
		model.moveDiff(delta)
	}
	return nil
}

func (model *Model) movePage(direction int) tea.Cmd {
	switch model.focus {
	case FocusFileTree:
		return model.queueTreeMove(direction * max(model.diffHeight(), 1))
	case FocusComments:
		model.moveComment(direction * max(model.diffHeight(), 1))
	default:
		model.moveDiff(direction * max(model.diffHeight(), 1))
	}
	return nil
}

func (model *Model) queueTreeMove(delta int) tea.Cmd {
	model.treeMovePending += delta
	model.treeMoveGeneration++
	return model.advanceTreeMove(treeMoveChunkMsg{
		generation: model.treeMoveGeneration,
	})
}

func (model *Model) advanceTreeMove(message treeMoveChunkMsg) tea.Cmd {
	if message.generation != model.treeMoveGeneration || model.treeMovePending == 0 {
		return nil
	}
	delta := min(max(model.treeMovePending, -maxFileRecordsPerUpdate), maxFileRecordsPerUpdate)
	model.moveTree(delta)
	model.treeMovePending -= delta
	if model.treeMovePending == 0 {
		return nil
	}
	generation := model.treeMoveGeneration
	return func() tea.Msg { return treeMoveChunkMsg{generation: generation} }
}

func (model *Model) moveFile(delta int) {
	if len(model.fileOrder) == 0 {
		return
	}
	selected, ok := model.review.SelectedFile()
	index := 0
	if ok {
		if position, exists := slices.BinarySearch(model.fileOrder, selected); exists {
			index = position
		}
	}
	index = min(max(index+delta, 0), len(model.fileOrder)-1)
	if next, err := model.review.SelectFile(model.fileOrder[index]); err == nil {
		model.review = next
	}
}

func (model *Model) moveDiff(delta int) {
	active, ok := model.review.ActiveFile()
	if !ok {
		return
	}
	lines := model.review.Patch(active).Lines
	if lines == 0 {
		model.diffTop = 0
		model.diffCursor = 0
		return
	}
	model.diffCursor = uint32FromInt(min(max(int(model.diffCursor)+delta, 0), int(lines)-1))
	height := uint32FromInt(max(model.diffHeight(), 1))
	if model.diffCursor < model.diffTop {
		model.diffTop = model.diffCursor
	} else if model.diffCursor >= model.diffTop+height {
		model.diffTop = model.diffCursor - height + 1
	}
	maximum := uint32(0)
	if lines > height {
		maximum = lines - height
	}
	model.diffTop = min(model.diffTop, maximum)
	model.requestSyntax()
}

func (model *Model) moveFirst() {
	if model.focus == FocusFileTree {
		tree := model.currentFileTree()
		if identity, ok := tree.first(); ok {
			model.selectTreeNode(identity)
		}
		return
	}
	if model.focus == FocusComments {
		model.selectCommentIndex(0)
		return
	}
	model.diffCursor = 0
	model.diffTop = 0
	model.requestSyntax()
}

func (model *Model) moveLast() {
	if model.focus == FocusFileTree {
		tree := model.currentFileTree()
		if index, ok := tree.lastIndex(); ok {
			if collapsed, hidden := model.collapsedTree.outermostContaining(index); hidden {
				index = collapsed
			}
			model.selectTreeNode(tree.nodes[index].identity)
		}
		return
	}
	if model.focus == FocusComments {
		model.selectCommentIndex(model.commentRowCount() - 1)
		return
	}
	active, ok := model.review.ActiveFile()
	if ok {
		lines := model.review.Patch(active).Lines
		if lines == 0 {
			model.diffCursor = 0
			model.diffTop = 0
			return
		}
		model.diffCursor = lines - 1
		model.moveDiff(0)
	}
}

func (model *Model) activateSelection() {
	switch model.focus {
	case FocusComments:
		model.activateCommentSelection()
		return
	case FocusDiff:
		active, ok := model.review.ActiveFile()
		if !ok {
			return
		}
		switch model.review.Patch(active).Status {
		case change.PatchUnavailable, change.PatchFailed,
			change.PatchCancelled, change.PatchLimited:
			model.foreground.clearPendingDiffTarget()
			model.requestActivePatch()
		case change.PatchPending, change.PatchReady:
		}
		return
	case FocusFileTree:
		tree := model.currentFileTree()
		if node, _, ok := tree.node(model.treeCursor); ok && node.identity.directory {
			model.activateTreeNode()
			return
		}
	default:
		return
	}
	selected, ok := model.review.SelectedFile()
	if !ok {
		return
	}
	model.foreground.clearPendingDiffTarget()
	if !model.activateFile(selected) {
		return
	}
	model.requestActivePatch()
}

func (model *Model) activateFile(file change.FileID) bool {
	previous, hadPrevious := model.review.ActiveFile()
	next, err := model.review.ActivateFile(file)
	if err != nil {
		return false
	}
	model.review = next
	if !hadPrevious || previous != file {
		model.resetSyntax()
	}
	if hadPrevious && previous != file &&
		model.review.Patch(previous).Status != change.PatchPending {
		model.review = model.review.DropPatch(previous)
	}
	model.diffTop = 0
	model.diffCursor = 0
	model.visualAnchor = 0
	return true
}

func (model *Model) applyDiffTarget(target pendingDiffTarget) {
	if !target.valid() || target.workspace != model.review.WorkspaceRevision() {
		return
	}
	fileID := target.handle.FileID()
	file, exists := model.review.Snapshot().File(fileID)
	if !exists || file.PatchHandle() != target.handle ||
		model.review.Patch(fileID).Status != change.PatchReady {
		return
	}
	var row uint32
	switch target.kind {
	case diffTargetPatchRow:
		if target.patchRow >= model.review.Patch(fileID).Lines {
			model.notice = &Notice{Kind: NoticeWarning, Text: "search result is not in the active patch"}
			return
		}
		row = target.patchRow
	case diffTargetSourceLine:
		var found bool
		row, found = model.review.PatchLineForSource(fileID, target.side, target.line)
		if !found {
			model.notice = &Notice{Kind: NoticeWarning, Text: "comment line is not in the active patch"}
			return
		}
	default:
		return
	}
	model.diffCursor = row
	model.moveDiff(0)
}

func (model *Model) cycleFocus(delta int) {
	model.focusReturn = 0
	owners := make([]FocusOwner, 0, 3)
	if model.showFileTree() {
		owners = append(owners, FocusFileTree)
	}
	if model.showDiffPane() {
		owners = append(owners, FocusDiff)
	}
	if model.showCommentsPane() {
		owners = append(owners, FocusComments)
	}
	if len(owners) == 0 {
		return
	}
	index := 0
	for position, owner := range owners {
		if owner == model.focus {
			index = position
			break
		}
	}
	delta %= len(owners)
	index = (index + delta + len(owners)) % len(owners)
	model.setFocus(owners[index])
}

func (model *Model) clampFocus() {
	if !model.focusVisible(model.focus) {
		model.focusReturn = model.focus
		switch {
		case model.showDiffPane():
			model.setFocus(FocusDiff)
		case model.showFileTree():
			model.setFocus(FocusFileTree)
		case model.showCommentsPane():
			model.setFocus(FocusComments)
		}
		return
	}
	if model.focusReturn != 0 && model.focusVisible(model.focusReturn) {
		model.setFocus(model.focusReturn)
		model.focusReturn = 0
	}
}

func (model *Model) setFocus(owner FocusOwner) {
	if model.focus == owner {
		return
	}
	model.focus = owner
	model.abandonChord()
}

func (model *Model) focusVisible(owner FocusOwner) bool {
	switch owner {
	case FocusFileTree:
		return model.showFileTree()
	case FocusDiff:
		return model.showDiffPane()
	case FocusComments:
		return model.showCommentsPane()
	default:
		return false
	}
}

func (model *Model) configFocus() keymap.Focus {
	switch model.focus {
	case FocusFileTree:
		return keymap.FocusFileTree
	case FocusComments:
		return keymap.FocusComments
	default:
		return keymap.FocusDiff
	}
}

func (model *Model) requestWorkspaceIndex() tea.Cmd {
	model.fileProjection.workspace = projectionLoading
	model.fileProjection.workspaceDetail = ""
	operationID, err := change.NewOperationID(model.nextOperation)
	if err != nil {
		model.fileProjection.workspace = projectionFailed
		model.fileProjection.workspaceDetail = operationIdentityExhausted
		model.notice = &Notice{Kind: NoticeError, Text: operationIdentityExhausted}
		return nil
	}
	model.nextOperation++
	return model.beginCollapseCapture(workspaceCollapseCaptureIntent{
		id:       operationID,
		snapshot: model.review.Snapshot(),
	})
}

func (model *Model) applyWorkspaceIndex(outcome WorkspaceIndexOutcome) tea.Cmd {
	if outcome.ID == 0 || outcome.ID != model.workspaceIndex {
		return nil
	}
	model.workspaceIndex = 0
	if outcome.Err != nil {
		model.fileProjection.workspace = projectionFailed
		model.fileProjection.workspaceDetail = NoticeText(outcome.Err.Error())
		model.notice = &Notice{
			Kind: NoticeError,
			Text: NoticeText("prepare review workspace: " + outcome.Err.Error()),
		}
		return nil
	}
	if outcome.Collapsed.generation != model.collapsedTree.generation {
		return model.requestWorkspaceIndex()
	}
	model.index = outcome.Index
	model.allFileTree = outcome.FileTree
	model.visibleFileTree = outcome.FileTree.withProjection(outcome.VisibleProjection)
	model.collapsedTree = outcome.Collapsed
	model.fileProjection.workspace = projectionReady
	model.fileProjection.workspaceDetail = ""
	model.selectFileOrder()
	model.notice = nil
	model.requestActivePatch()
	return nil
}

func retainedWorkspaceIndexBytes(
	tree fileTree,
	visible treeProjection,
	index projection.Index,
) int64 {
	return tree.retainedBytes() + int64(cap(visible)*8) + index.RetainedBytes()
}

func (model *Model) selectFileOrder() {
	if model.review.ShowHidden() {
		model.fileOrder = model.index.AllFiles()
	} else {
		model.fileOrder = model.index.VisibleFiles()
	}
	if len(model.fileOrder) == 0 {
		model.selectTreeCursor()
		return
	}
	if selected, ok := model.review.SelectedFile(); ok {
		if _, exists := slices.BinarySearch(model.fileOrder, selected); exists {
			model.selectTreeCursor()
			return
		}
	}
	target := model.fileOrder[0]
	if active, ok := model.review.ActiveFile(); ok {
		if _, exists := slices.BinarySearch(model.fileOrder, active); exists {
			target = active
		}
	}
	if next, err := model.review.SelectFile(target); err == nil {
		model.review = next
	}
	model.selectTreeCursor()
}

func (model *Model) clampDiffTop() {
	model.moveDiff(0)
	model.clampFocus()
}

func (model *Model) activatePath(path string) {
	model.foreground.clearPendingDiffTarget()
	fileID, status := model.index.LookupPath(path)
	if status == projection.PathAmbiguous {
		model.notice = &Notice{Kind: NoticeWarning, Text: "path is ambiguous in this review"}
		return
	}
	if status != projection.PathUnique {
		model.notice = &Notice{Kind: NoticeWarning, Text: "path is not in this review"}
		return
	}
	next, err := model.review.SelectFile(fileID)
	if err != nil && !model.review.ShowHidden() {
		model.review = model.review.ShowHiddenFiles()
		model.selectFileOrder()
		next, err = model.review.SelectFile(fileID)
	}
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.activateSelection()
}

func (model *Model) selectSourceLine(side change.LineSide, value uint32) {
	model.foreground.clearPendingDiffTarget()
	line, err := change.NewLineNumber(value)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	active, ok := model.review.ActiveFile()
	if !ok {
		model.notice = &Notice{Kind: NoticeWarning, Text: "review has no active file"}
		return
	}
	row, ok := model.review.PatchLineForSource(active, side, line)
	if !ok {
		model.notice = &Notice{Kind: NoticeWarning, Text: "source line is not in the active patch"}
		return
	}
	model.diffCursor = row
	model.moveDiff(0)
}

func (model *Model) startFileFilter(query string) tea.Cmd {
	query = strings.TrimSpace(query)
	model.filterGeneration++
	model.fileProjection.requestedQuery = query
	model.fileProjection.filterDetail = ""
	model.filterBuild = nil
	source := model.index.VisibleFiles()
	if model.review.ShowHidden() {
		source = model.index.AllFiles()
	}
	if query == "" {
		if model.filterTreeOperation != 0 {
			model.coordinator.Cancel(model.filterTreeOperation)
		}
		model.fileOrder = source
		model.filteredFileTree = nil
		model.fileProjection.appliedQuery = ""
		model.fileProjection.filter = projectionReady
		model.filterTreeGeneration = 0
		model.filterTreeFiles = nil
		model.selectFileOrder()
		model.notice = &Notice{Kind: NoticeInformation, Text: "file filter cleared"}
		return nil
	}
	model.fileProjection.filter = projectionLoading
	model.filterBuild = &fileFilterBuild{
		generation: model.filterGeneration,
		query:      strings.ToLower(query),
		source:     source,
		result:     make([]change.FileID, 0, min(len(source), maxFileRecordsPerUpdate)),
	}
	generation := model.filterGeneration
	return func() tea.Msg { return fileFilterChunkMsg{generation: generation} }
}

func (model *Model) advanceFileFilter(message fileFilterChunkMsg) tea.Cmd {
	build := model.filterBuild
	if build == nil || build.generation != message.generation {
		return nil
	}
	end := min(build.position+maxFileRecordsPerUpdate, len(build.source))
	for _, fileID := range build.source[build.position:end] {
		file, ok := model.review.Snapshot().File(fileID)
		if ok && strings.Contains(strings.ToLower(file.DisplayPath()), build.query) {
			build.result = append(build.result, fileID)
		}
	}
	build.position = end
	if end < len(build.source) {
		generation := build.generation
		return func() tea.Msg { return fileFilterChunkMsg{generation: generation} }
	}
	model.filterBuild = nil
	model.filterTreeGeneration = build.generation
	model.filterTreeFiles = build.result
	if model.filterTreeBuilding {
		return nil
	}
	return model.startFileFilterTree()
}

func (model *Model) startFileFilterTree() tea.Cmd {
	if model.filterTreeGeneration == 0 {
		return nil
	}
	operationID, err := change.NewOperationID(model.nextOperation)
	if err != nil {
		model.filterTreeGeneration = 0
		model.filterTreeFiles = nil
		model.fileProjection.filter = projectionFailed
		model.fileProjection.filterDetail = NoticeText(err.Error())
		model.notice = &Notice{Kind: NoticeError, Text: operationIdentityExhausted}
		return nil
	}
	model.nextOperation++
	files := model.filterTreeFiles
	err = model.coordinator.SubmitFileTree(FileTreeWork{
		ID:         operationID,
		Generation: model.filterTreeGeneration,
		Tree:       model.allFileTree,
		Files:      files,
	})
	if err != nil {
		model.filterTreeGeneration = 0
		model.filterTreeFiles = nil
		model.fileProjection.filter = projectionFailed
		model.fileProjection.filterDetail = NoticeText(err.Error())
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText("build filtered file tree: " + err.Error()),
		}
		return nil
	}
	model.filterTreeFiles = nil
	model.filterTreeBuilding = true
	model.filterTreeOperation = operationID
	return nil
}

func (model *Model) applyFileFilterTree(outcome FileTreeOutcome) tea.Cmd {
	if outcome.ID != model.filterTreeOperation {
		return nil
	}
	model.filterTreeBuilding = false
	model.filterTreeOperation = 0
	if outcome.Err != nil && outcome.Generation == model.filterGeneration {
		model.filterTreeGeneration = 0
		model.filterTreeFiles = nil
		model.fileProjection.filter = projectionFailed
		model.fileProjection.filterDetail = NoticeText(outcome.Err.Error())
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText("build filtered file tree: " + outcome.Err.Error()),
		}
	}
	if outcome.Err == nil && outcome.Generation == model.filterGeneration &&
		outcome.Generation == model.filterTreeGeneration {
		model.fileOrder = outcome.Files
		filtered := model.allFileTree.withProjection(outcome.Projection)
		model.filteredFileTree = &filtered
		model.fileProjection.appliedQuery = model.fileProjection.requestedQuery
		model.fileProjection.filter = projectionReady
		model.fileProjection.filterDetail = ""
		model.filterTreeGeneration = 0
		model.filterTreeFiles = nil
		model.selectFileOrder()
		model.notice = &Notice{Kind: NoticeSuccess, Text: "file filter applied"}
		return nil
	}
	if model.filterTreeGeneration != 0 &&
		model.filterTreeGeneration != outcome.Generation {
		return model.startFileFilterTree()
	}
	return nil
}
