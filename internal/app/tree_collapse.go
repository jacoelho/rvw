package app

import (
	"context"
	"errors"
	"fmt"
	"math"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

const (
	collapsedIntervalNodeCharge = 32
	collapsedPositionCharge     = 4
	collapsedStateCharge        = 64
	collapseChunkHeaderCharge   = 24
	collapseRequestStateCharge  = 128
	missingCollapsedPosition    = int32(-1)
)

// collapsedIntervalNode is an AVL node for a canonical preorder interval.
// Its charged size is intentionally fixed so the complete toggle capacity is
// reserved before the state reaches the terminal event loop.
type collapsedIntervalNode struct {
	start  int32
	end    int32
	maxEnd int32
	left   int32
	right  int32
	parent int32
	height int32
	_      int32
}

// collapsedTreeState is the sole mutable collapse state. positions maps a
// canonical tree node to its AVL slot, or -1 when the directory is expanded.
type collapsedTreeState struct {
	nodes      []collapsedIntervalNode
	positions  []int32
	generation uint64
	root       int32
	_          int32
}

// collapseSnapshot transfers an immutable generation of canonical collapse
// membership to a worker. tree is borrowed and position chunks are exclusively
// owned. Chunks avoid a second full-sized flattening allocation in the worker.
type collapseSnapshot struct {
	tree           fileTree
	positionChunks [][]int32
	positionCount  int
	generation     uint64
}

func collapseSnapshotRequestBytes(positionCount int) (int64, error) {
	if positionCount < 0 || positionCount > maxFileTreeNodes {
		return 0, errors.New("collapse snapshot position count is invalid")
	}
	chunkCount := (positionCount + maxFileRecordsPerUpdate - 1) / maxFileRecordsPerUpdate
	return int64(chunkCount)*collapseChunkHeaderCharge +
		int64(positionCount)*collapsedPositionCharge, nil
}

func (snapshot collapseSnapshot) requestBytes() (int64, error) {
	if snapshot.positionCount < 0 || snapshot.positionCount > maxFileTreeNodes ||
		len(snapshot.positionChunks) !=
			(snapshot.positionCount+maxFileRecordsPerUpdate-1)/maxFileRecordsPerUpdate ||
		(snapshot.positionCount != 0 && snapshot.positionCount != len(snapshot.tree.nodes)) ||
		(snapshot.positionCount == 0 && len(snapshot.tree.nodes) != 0) {
		return 0, errors.New("collapse snapshot does not match its canonical tree")
	}
	retained := int64(cap(snapshot.positionChunks)) * collapseChunkHeaderCharge
	count := 0
	for index, chunk := range snapshot.positionChunks {
		expected := min(maxFileRecordsPerUpdate, snapshot.positionCount-count)
		if len(chunk) != expected || cap(chunk) != expected ||
			index+1 < len(snapshot.positionChunks) && expected != maxFileRecordsPerUpdate {
			return 0, errors.New("collapse snapshot contains an invalid position chunk")
		}
		count += len(chunk)
		retained += int64(cap(chunk)) * collapsedPositionCharge
	}
	if count != snapshot.positionCount {
		return 0, errors.New("collapse snapshot position count is incomplete")
	}
	return retained, nil
}

type collapseCaptureStepMsg struct {
	permit uint64
}

type collapseCaptureIntent interface {
	isCollapseCaptureIntent()
}

type workspaceCollapseCaptureIntent struct {
	snapshot review.Snapshot
	id       review.OperationID
}

func (workspaceCollapseCaptureIntent) isCollapseCaptureIntent() {}

type reloadCollapseCaptureIntent struct {
	document *source.Document
	detail   NoticeText
	review   review.Review
	id       review.OperationID
	attempts uint8
}

func (reloadCollapseCaptureIntent) isCollapseCaptureIntent() {}

// collapseCapture is foreground-owned until its coordinator reservation is
// either converted into accepted work or released. A step permit is one-shot:
// consuming it precedes every validation or copy.
type collapseCapture struct {
	firstNode          *treeNode
	intent             collapseCaptureIntent
	positionChunks     [][]int32
	tree               fileTree
	operation          review.OperationID
	generation         uint64
	permit             uint64
	nextPosition       int
	positionCount      int
	workspace          review.WorkspaceRevision
	preparationAttempt uint8
}

func collapseTreeFirstNode(tree fileTree) *treeNode {
	if len(tree.nodes) == 0 {
		return nil
	}
	return &tree.nodes[0]
}

func collapseCaptureRequestBytes(intent collapseCaptureIntent, positionCount int) (int64, error) {
	base := int64(collapseRequestStateCharge)
	switch intent := intent.(type) {
	case workspaceCollapseCaptureIntent:
		if intent.id == 0 || intent.snapshot.SourceID() == 0 {
			return 0, errors.New("invalid workspace collapse capture")
		}
	case reloadCollapseCaptureIntent:
		if intent.id == 0 || intent.document == nil ||
			intent.review.Snapshot().SourceID() == 0 {
			return 0, errors.New("invalid reload collapse capture")
		}
		base += intent.review.RetainedCommentBytes()
	default:
		return 0, errors.New("unknown collapse capture intent")
	}
	return collapseCaptureRequestBytesForBase(base, positionCount)
}

func collapseCaptureRequestBytesForBase(base int64, positionCount int) (int64, error) {
	positions, err := collapseSnapshotRequestBytes(positionCount)
	if err != nil {
		return 0, err
	}
	if base < 0 || base > MaxQueuedRequestBytes-positions {
		retained := int64(math.MaxInt64)
		if base <= math.MaxInt64-positions {
			retained = base + positions
		}
		return 0, fmt.Errorf(
			"%w: collapse capture retains %d bytes; limit is %d",
			ErrCoordinatorSaturated,
			retained,
			MaxQueuedRequestBytes,
		)
	}
	return base + positions, nil
}

func (model *Model) beginCollapseCapture(intent collapseCaptureIntent) tea.Cmd {
	if model.collapseCapture != nil {
		model.failCollapseCaptureIntent(intent, errors.New("collapse capture is already active"))
		return nil
	}
	return model.restartCollapseCapture(intent)
}

func (model *Model) restartCollapseCapture(intent collapseCaptureIntent) tea.Cmd {
	var operation review.OperationID
	attempts := uint8(0)
	switch captured := intent.(type) {
	case workspaceCollapseCaptureIntent:
		captured.snapshot = model.review.Snapshot()
		intent = captured
		operation = captured.id
	case reloadCollapseCaptureIntent:
		captured.review = model.review
		intent = captured
		operation = captured.id
		attempts = captured.attempts
	default:
		model.failCollapseCaptureIntent(intent, errors.New("unknown collapse capture intent"))
		return nil
	}
	if len(model.collapsedTree.positions) != len(model.allFileTree.nodes) {
		model.coordinator.releaseCollapseCapture(operation)
		model.failCollapseCaptureIntent(
			intent,
			errors.New("collapse state does not match its canonical tree"),
		)
		return nil
	}
	requestBytes, err := collapseCaptureRequestBytes(intent, len(model.collapsedTree.positions))
	if err != nil {
		model.coordinator.releaseCollapseCapture(operation)
		model.failCollapseCaptureIntent(intent, err)
		return nil
	}
	if err := model.coordinator.reserveCollapseCapture(operation, requestBytes); err != nil {
		model.coordinator.releaseCollapseCapture(operation)
		model.failCollapseCaptureIntent(intent, err)
		return nil
	}
	positionCount := len(model.collapsedTree.positions)
	chunkCount := (positionCount + maxFileRecordsPerUpdate - 1) / maxFileRecordsPerUpdate
	model.collapseCapture = &collapseCapture{
		intent:             intent,
		tree:               model.allFileTree,
		positionChunks:     make([][]int32, 0, chunkCount),
		firstNode:          collapseTreeFirstNode(model.allFileTree),
		workspace:          model.review.WorkspaceRevision(),
		operation:          operation,
		generation:         model.collapsedTree.generation,
		positionCount:      positionCount,
		preparationAttempt: attempts,
	}
	if positionCount == 0 {
		model.submitCollapseCapture()
		return nil
	}
	return model.issueCollapseCaptureStep()
}

func (model *Model) issueCollapseCaptureStep() tea.Cmd {
	capture := model.collapseCapture
	if capture == nil {
		return nil
	}
	if model.nextCollapseCapturePermit == math.MaxUint64 {
		model.failActiveCollapseCapture(errors.New("collapse capture permit identity exhausted"))
		return nil
	}
	model.nextCollapseCapturePermit++
	capture.permit = model.nextCollapseCapturePermit
	permit := capture.permit
	return func() tea.Msg { return collapseCaptureStepMsg{permit: permit} }
}

func (model *Model) advanceCollapseCapture(message collapseCaptureStepMsg) tea.Cmd {
	capture := model.collapseCapture
	if capture == nil || message.permit == 0 || capture.permit != message.permit {
		return nil
	}
	capture.permit = 0
	if capture.workspace != model.review.WorkspaceRevision() ||
		capture.generation != model.collapsedTree.generation ||
		capture.positionCount != len(model.collapsedTree.positions) ||
		capture.positionCount != len(model.allFileTree.nodes) ||
		capture.firstNode != collapseTreeFirstNode(model.allFileTree) {
		intent := capture.intent
		model.collapseCapture = nil
		return model.restartCollapseCapture(intent)
	}
	end := min(capture.nextPosition+maxFileRecordsPerUpdate, capture.positionCount)
	chunk := make([]int32, end-capture.nextPosition)
	copy(chunk, model.collapsedTree.positions[capture.nextPosition:end])
	capture.positionChunks = append(capture.positionChunks, chunk)
	capture.nextPosition = end
	if end != capture.positionCount {
		return model.issueCollapseCaptureStep()
	}
	model.submitCollapseCapture()
	return nil
}

func (model *Model) submitCollapseCapture() {
	capture := model.collapseCapture
	if capture == nil {
		return
	}
	model.collapseCapture = nil
	snapshot := collapseSnapshot{
		tree:           capture.tree,
		positionChunks: capture.positionChunks,
		positionCount:  capture.positionCount,
		generation:     capture.generation,
	}
	switch intent := capture.intent.(type) {
	case workspaceCollapseCaptureIntent:
		err := model.coordinator.SubmitWorkspaceIndex(WorkspaceIndexWork{
			ID:       intent.id,
			Snapshot: intent.snapshot,
			Collapse: snapshot,
		})
		if err != nil {
			model.coordinator.releaseCollapseCapture(intent.id)
			model.failCollapseCaptureIntent(intent, err)
			return
		}
		model.workspaceIndex = intent.id
		model.notice = &Notice{Kind: NoticeInformation, Text: "preparing review workspace"}
	case reloadCollapseCaptureIntent:
		err := model.coordinator.SubmitReloadPreparation(ReloadPreparationWork{
			ID:       intent.id,
			Review:   intent.review,
			Document: intent.document,
			Collapse: snapshot,
		})
		if err != nil {
			model.coordinator.releaseCollapseCapture(intent.id)
			model.failCollapseCaptureIntent(intent, err)
			return
		}
		model.reloadPreparationDocument = intent.document
		if !model.foreground.reloadPreparationAdmitted(intent.id, capture.preparationAttempt) {
			model.coordinator.Cancel(intent.id)
			model.reloadPreparationDocument = nil
			model.failReload(
				intent.id,
				nil,
				errors.New("foreground reload preparation invariant violated"),
			)
			return
		}
		model.notice = &Notice{Kind: NoticeInformation, Text: intent.detail}
	default:
		model.coordinator.releaseCollapseCapture(capture.operation)
		model.failCollapseCaptureIntent(capture.intent, errors.New("unknown collapse capture intent"))
	}
}

func (model *Model) failActiveCollapseCapture(err error) {
	capture := model.collapseCapture
	if capture == nil {
		return
	}
	model.collapseCapture = nil
	model.coordinator.releaseCollapseCapture(capture.operation)
	model.failCollapseCaptureIntent(capture.intent, err)
}

func (model *Model) failCollapseCaptureIntent(intent collapseCaptureIntent, err error) {
	switch intent := intent.(type) {
	case workspaceCollapseCaptureIntent:
		model.fileProjection.workspace = projectionFailed
		model.fileProjection.workspaceDetail = NoticeText(err.Error())
		model.notice = &Notice{
			Kind: NoticeError,
			Text: NoticeText("prepare review workspace: " + err.Error()),
		}
	case reloadCollapseCaptureIntent:
		model.failReload(intent.id, intent.document, err)
	default:
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
	}
}

func (model *Model) collapseCaptureOwnsDocument(document *source.Document) bool {
	if document == nil || model.collapseCapture == nil {
		return false
	}
	intent, ok := model.collapseCapture.intent.(reloadCollapseCaptureIntent)
	return ok && intent.document == document
}

func (model *Model) cancelReloadCollapseCapture() bool {
	capture := model.collapseCapture
	if capture == nil {
		return false
	}
	if _, ok := capture.intent.(reloadCollapseCaptureIntent); !ok {
		return false
	}
	model.failActiveCollapseCapture(context.Canceled)
	model.notice = &Notice{
		Kind: NoticeInformation,
		Text: "reload cancelled; the current source remains active",
	}
	return true
}

func (model *Model) abandonCollapseCapture() {
	capture := model.collapseCapture
	if capture == nil {
		return
	}
	model.collapseCapture = nil
	model.coordinator.releaseCollapseCapture(capture.operation)
	if intent, ok := capture.intent.(reloadCollapseCaptureIntent); ok && intent.document != nil {
		model.retainCleanup(intent.document.DetachCleanup())
	}
}

func (model *Model) detachCollapseCaptureCleanup() *source.Cleanup {
	capture := model.collapseCapture
	if capture == nil {
		return nil
	}
	model.collapseCapture = nil
	model.coordinator.releaseCollapseCapture(capture.operation)
	intent, ok := capture.intent.(reloadCollapseCaptureIntent)
	if !ok || intent.document == nil {
		return nil
	}
	return intent.document.DetachCleanup()
}

func buildCollapsedTreeState(
	ctx context.Context,
	tree fileTree,
	snapshot collapseSnapshot,
	ledger *retainedLedger,
) (collapsedTreeState, error) {
	if err := context.Cause(ctx); err != nil {
		return collapsedTreeState{}, err
	}
	if len(tree.nodes) > maxFileTreeNodes {
		return collapsedTreeState{}, ErrFileTreeLimited
	}
	if _, err := snapshot.requestBytes(); err != nil {
		return collapsedTreeState{}, err
	}
	directoryCapacity := min(len(tree.directories), review.MaxFiles)
	retained := int64(directoryCapacity)*collapsedIntervalNodeCharge +
		int64(len(tree.nodes))*collapsedPositionCharge + collapsedStateCharge
	if err := ledger.reserve(retained); err != nil {
		return collapsedTreeState{}, err
	}
	state := collapsedTreeState{
		nodes:      make([]collapsedIntervalNode, 0, directoryCapacity),
		positions:  make([]int32, len(tree.nodes)),
		generation: snapshot.generation,
		root:       missingCollapsedPosition,
	}
	for index := range state.positions {
		if index%maxFileRecordsPerUpdate == 0 {
			if err := context.Cause(ctx); err != nil {
				return collapsedTreeState{}, err
			}
		}
		state.positions[index] = missingCollapsedPosition
	}
	index := 0
	for _, chunk := range snapshot.positionChunks {
		if err := context.Cause(ctx); err != nil {
			return collapsedTreeState{}, err
		}
		for _, position := range chunk {
			if position != missingCollapsedPosition {
				if position < 0 || index >= len(snapshot.tree.nodes) {
					return collapsedTreeState{}, errors.New("collapse snapshot contains an invalid position")
				}
				oldNode := snapshot.tree.nodes[index]
				if !oldNode.identity.directory {
					return collapsedTreeState{}, errors.New("collapse snapshot contains a file node")
				}
				newIndex, exists := tree.directories[oldNode.identity.path]
				if exists {
					if err := state.insert(newIndex, tree.nodes[newIndex].subtreeEnd); err != nil {
						return collapsedTreeState{}, err
					}
				}
			}
			index++
		}
	}
	return state, nil
}

func (state *collapsedTreeState) retainedBytes() int64 {
	if state.positions == nil && state.nodes == nil {
		return 0
	}
	return int64(cap(state.nodes))*collapsedIntervalNodeCharge +
		int64(cap(state.positions))*collapsedPositionCharge + collapsedStateCharge
}

func (state *collapsedTreeState) isCollapsed(index int) bool {
	return index >= 0 && index < len(state.positions) &&
		state.positions[index] != missingCollapsedPosition
}

type collapseDisposition uint8

const (
	collapseExpanded collapseDisposition = iota + 1
	collapseCollapsed
)

func (state *collapsedTreeState) set(
	index, subtreeEnd int,
	disposition collapseDisposition,
) (bool, error) {
	if state == nil || index < 0 || index >= len(state.positions) ||
		subtreeEnd <= index || subtreeEnd > len(state.positions) ||
		disposition != collapseExpanded && disposition != collapseCollapsed {
		return false, errors.New("collapse target is outside the canonical tree")
	}
	collapsed := disposition == collapseCollapsed
	current := state.isCollapsed(index)
	if current == collapsed {
		return false, nil
	}
	if state.generation == math.MaxUint64 {
		return false, errors.New("collapse generation exhausted")
	}
	if collapsed {
		if err := state.insert(index, subtreeEnd); err != nil {
			return false, err
		}
	} else {
		state.remove(index)
	}
	state.generation++
	return true, nil
}

func (state *collapsedTreeState) insert(index, subtreeEnd int) error {
	if index < 0 || index >= len(state.positions) || subtreeEnd <= index ||
		subtreeEnd > len(state.positions) {
		return errors.New("collapse interval is outside the canonical tree")
	}
	if state.positions[index] != missingCollapsedPosition {
		return nil
	}
	if len(state.nodes) == cap(state.nodes) {
		return fmt.Errorf("%w: collapsed directory capacity reached", ErrFileTreeLimited)
	}
	start := int32FromInt(index + 1)
	end := int32FromInt(subtreeEnd)
	parent := missingCollapsedPosition
	current := state.root
	for current >= 0 {
		parent = current
		if start < state.nodes[current].start {
			current = state.nodes[current].left
		} else {
			current = state.nodes[current].right
		}
	}
	slot := int32FromInt(len(state.nodes))
	state.nodes = append(state.nodes, collapsedIntervalNode{
		start:  start,
		end:    end,
		maxEnd: end,
		left:   missingCollapsedPosition,
		right:  missingCollapsedPosition,
		parent: parent,
		height: 1,
	})
	state.positions[index] = slot
	switch {
	case parent < 0:
		state.root = slot
	case start < state.nodes[parent].start:
		state.nodes[parent].left = slot
	default:
		state.nodes[parent].right = slot
	}
	state.rebalance(parent)
	return nil
}

func (state *collapsedTreeState) remove(index int) {
	slot := state.positions[index]
	if slot < 0 {
		return
	}
	if state.nodes[slot].left >= 0 && state.nodes[slot].right >= 0 {
		successor := state.nodes[slot].right
		for state.nodes[successor].left >= 0 {
			successor = state.nodes[successor].left
		}
		state.nodes[slot].start, state.nodes[successor].start =
			state.nodes[successor].start, state.nodes[slot].start
		state.nodes[slot].end, state.nodes[successor].end =
			state.nodes[successor].end, state.nodes[slot].end
		state.positions[int(state.nodes[slot].start)-1] = slot
		state.positions[int(state.nodes[successor].start)-1] = successor
		slot = successor
	}
	node := state.nodes[slot]
	child := node.left
	if child < 0 {
		child = node.right
	}
	state.replaceChild(node.parent, slot, child)
	state.positions[index] = missingCollapsedPosition
	state.rebalance(node.parent)
	state.removeSlot(slot)
}

func (state *collapsedTreeState) removeSlot(slot int32) {
	last := int32FromInt(len(state.nodes) - 1)
	if slot != last {
		moved := state.nodes[last]
		state.nodes[slot] = moved
		switch {
		case moved.parent < 0:
			state.root = slot
		case state.nodes[moved.parent].left == last:
			state.nodes[moved.parent].left = slot
		default:
			state.nodes[moved.parent].right = slot
		}
		if moved.left >= 0 {
			state.nodes[moved.left].parent = slot
		}
		if moved.right >= 0 {
			state.nodes[moved.right].parent = slot
		}
		state.positions[int(moved.start)-1] = slot
	}
	state.nodes = state.nodes[:last]
}

func (state *collapsedTreeState) replaceChild(parent, previous, next int32) {
	switch {
	case parent < 0:
		state.root = next
	case state.nodes[parent].left == previous:
		state.nodes[parent].left = next
	default:
		state.nodes[parent].right = next
	}
	if next >= 0 {
		state.nodes[next].parent = parent
	}
}

func (state *collapsedTreeState) rebalance(index int32) {
	for index >= 0 {
		state.recompute(index)
		balance := state.height(state.nodes[index].left) - state.height(state.nodes[index].right)
		subtreeRoot := index
		switch {
		case balance > 1:
			left := state.nodes[index].left
			if state.height(state.nodes[left].left) < state.height(state.nodes[left].right) {
				state.rotateLeft(left)
			}
			subtreeRoot = state.rotateRight(index)
		case balance < -1:
			right := state.nodes[index].right
			if state.height(state.nodes[right].right) < state.height(state.nodes[right].left) {
				state.rotateRight(right)
			}
			subtreeRoot = state.rotateLeft(index)
		}
		index = state.nodes[subtreeRoot].parent
	}
}

func (state *collapsedTreeState) rotateLeft(pivot int32) int32 {
	next := state.nodes[pivot].right
	middle := state.nodes[next].left
	parent := state.nodes[pivot].parent
	state.replaceChild(parent, pivot, next)
	state.nodes[next].left = pivot
	state.nodes[pivot].parent = next
	state.nodes[pivot].right = middle
	if middle >= 0 {
		state.nodes[middle].parent = pivot
	}
	state.recompute(pivot)
	state.recompute(next)
	return next
}

func (state *collapsedTreeState) rotateRight(pivot int32) int32 {
	next := state.nodes[pivot].left
	middle := state.nodes[next].right
	parent := state.nodes[pivot].parent
	state.replaceChild(parent, pivot, next)
	state.nodes[next].right = pivot
	state.nodes[pivot].parent = next
	state.nodes[pivot].left = middle
	if middle >= 0 {
		state.nodes[middle].parent = pivot
	}
	state.recompute(pivot)
	state.recompute(next)
	return next
}

func (state *collapsedTreeState) recompute(index int32) {
	node := &state.nodes[index]
	node.height = max(state.height(node.left), state.height(node.right)) + 1
	node.maxEnd = node.end
	if node.left >= 0 {
		node.maxEnd = max(node.maxEnd, state.nodes[node.left].maxEnd)
	}
	if node.right >= 0 {
		node.maxEnd = max(node.maxEnd, state.nodes[node.right].maxEnd)
	}
}

func (state *collapsedTreeState) height(index int32) int32 {
	if index < 0 {
		return 0
	}
	return state.nodes[index].height
}

// outermostContaining returns the canonical directory whose collapsed interval
// is the first interval in preorder containing index.
func (state *collapsedTreeState) outermostContaining(index int) (int, bool) {
	if index < 0 || index >= len(state.positions) || state.root < 0 || len(state.nodes) == 0 {
		return 0, false
	}
	slot, ok := state.firstContaining(state.root, int32FromInt(index))
	if !ok {
		return 0, false
	}
	return int(state.nodes[slot].start) - 1, true
}

func (state *collapsedTreeState) firstContaining(slot, point int32) (int32, bool) {
	if slot < 0 {
		return 0, false
	}
	node := state.nodes[slot]
	if node.left >= 0 && state.nodes[node.left].maxEnd > point {
		if found, ok := state.firstContaining(node.left, point); ok {
			return found, true
		}
	}
	if node.start <= point && point < node.end {
		return slot, true
	}
	if node.start > point {
		return 0, false
	}
	return state.firstContaining(node.right, point)
}
