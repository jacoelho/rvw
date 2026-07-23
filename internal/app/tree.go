package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jacoelho/rvw/internal/review"
)

const (
	maxFileTreeNodes         = 200_000
	fileTreeDescriptorCharge = 32
	fileTreeNodeCharge       = 96
	fileTreeFileCharge       = 32
	fileTreeDirectoryCharge  = 48
	maxTreeSortRun           = 256
)

// ErrFileTreeLimited identifies an file tree limited failure.
var ErrFileTreeLimited = errors.New("file tree exceeds retained limits")

type treeNodeIdentity struct {
	path      string
	file      review.FileID
	directory bool
}

type treeNode struct {
	label      string
	identity   treeNodeIdentity
	depth      int
	parent     int
	subtreeEnd int
}

type fileTree struct {
	directories map[string]int
	files       map[review.FileID]int
	nodes       []treeNode
	projection  treeProjection
}

type treeProjection []int

type treeDescriptor struct {
	path    string
	file    review.FileID
	invalid bool
}

type fileTreeBudget struct {
	nodes int
}

func (budget *fileTreeBudget) reserveNodes(nodes int) error {
	budget.nodes += nodes
	if budget.nodes > maxFileTreeNodes {
		return fmt.Errorf(
			"%w: %d nodes",
			ErrFileTreeLimited,
			budget.nodes,
		)
	}
	return nil
}

func buildFileTreeContext(
	ctx context.Context,
	snapshot review.Snapshot,
	files []review.FileID,
) (fileTree, error) {
	ledger, err := newRetainedLedger(MaxRetainedResultBytes, snapshot.RetainedBytes())
	if err != nil {
		return fileTree{}, err
	}
	return buildFileTreeWithLedger(ctx, snapshot, files, ledger)
}

func buildFileTreeWithLedger(
	ctx context.Context,
	snapshot review.Snapshot,
	files []review.FileID,
	ledger *retainedLedger,
) (fileTree, error) {
	if err := context.Cause(ctx); err != nil {
		return fileTree{}, err
	}
	budget := fileTreeBudget{}
	descriptorBytes := int64(len(files) * fileTreeDescriptorCharge)
	if err := ledger.reserve(descriptorBytes); err != nil {
		return fileTree{}, err
	}
	descriptors := make([]treeDescriptor, 0, len(files))
	for index, id := range files {
		if index%maxFileRecordsPerUpdate == 0 {
			if err := context.Cause(ctx); err != nil {
				return fileTree{}, err
			}
		}
		file, ok := snapshot.File(id)
		if !ok {
			continue
		}
		path, validUTF8 := file.IdentityPath().UTF8()
		descriptors = append(descriptors, treeDescriptor{path: path, file: id, invalid: !validUTF8})
	}
	if err := ledger.reserve(descriptorBytes); err != nil {
		return fileTree{}, err
	}
	scratch := make([]treeDescriptor, len(descriptors))
	ordered, orderErr := orderTreeDescriptors(ctx, descriptors, scratch)
	if orderErr != nil {
		return fileTree{}, orderErr
	}
	descriptors = ordered
	ledger.release(descriptorBytes)
	directoryCount, err := countTreeDirectories(ctx, descriptors)
	if err != nil {
		return fileTree{}, err
	}
	nodeCount := len(descriptors) + directoryCount
	retainedBytes := nodeCount*fileTreeNodeCharge +
		len(descriptors)*fileTreeFileCharge + directoryCount*fileTreeDirectoryCharge
	if err := ledger.reserve(int64(retainedBytes)); err != nil {
		return fileTree{}, err
	}
	if err := budget.reserveNodes(nodeCount); err != nil {
		return fileTree{}, err
	}
	tree := fileTree{
		nodes:       make([]treeNode, 0, nodeCount),
		directories: make(map[string]int, directoryCount),
		files:       make(map[review.FileID]int, len(descriptors)),
	}
	if err := emitFlatTree(ctx, &tree, descriptors); err != nil {
		return fileTree{}, err
	}
	ledger.release(descriptorBytes)
	return tree, nil
}

func orderTreeDescriptors(
	ctx context.Context,
	descriptors []treeDescriptor,
	scratch []treeDescriptor,
) ([]treeDescriptor, error) {
	if len(scratch) != len(descriptors) {
		return nil, errors.New("file tree ordering scratch has the wrong size")
	}
	for start := 0; start < len(descriptors); start += maxTreeSortRun {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		end := min(start+maxTreeSortRun, len(descriptors))
		slices.SortFunc(descriptors[start:end], compareTreeDescriptors)
	}
	source, destination := descriptors, scratch
	for width := maxTreeSortRun; width < len(descriptors); width *= 2 {
		for start := 0; start < len(descriptors); start += width * 2 {
			middle := min(start+width, len(descriptors))
			end := min(start+width*2, len(descriptors))
			left, right := start, middle
			for output := start; output < end; output++ {
				if output%maxFileRecordsPerUpdate == 0 {
					if err := context.Cause(ctx); err != nil {
						return nil, err
					}
				}
				switch {
				case left == middle:
					destination[output] = source[right]
					right++
				case right == end,
					compareTreeDescriptors(source[left], source[right]) <= 0:
					destination[output] = source[left]
					left++
				default:
					destination[output] = source[right]
					right++
				}
			}
		}
		source, destination = destination, source
	}
	return source, nil
}

func compareTreeDescriptors(left, right treeDescriptor) int {
	if left.invalid != right.invalid {
		if left.invalid {
			return -1
		}
		return 1
	}
	if left.invalid {
		var leftDigits, rightDigits [10]byte
		leftValue := strconv.AppendUint(leftDigits[:0], uint64(left.file.Uint32()), 10)
		rightValue := strconv.AppendUint(rightDigits[:0], uint64(right.file.Uint32()), 10)
		return bytes.Compare(leftValue, rightValue)
	}
	return compareTreePaths(left.path, right.path)
}

func compareTreePaths(left, right string) int {
	for {
		leftPart, leftRest := cutTreePath(left)
		rightPart, rightRest := cutTreePath(right)
		if compared := strings.Compare(leftPart, rightPart); compared != 0 {
			return compared
		}
		if leftRest == "" || rightRest == "" {
			return strings.Compare(leftRest, rightRest)
		}
		left, right = leftRest, rightRest
	}
}

func cutTreePath(path string) (string, string) {
	part, rest, found := strings.Cut(path, "/")
	if !found {
		return path, ""
	}
	return part, rest
}

func countTreeDirectories(ctx context.Context, descriptors []treeDescriptor) (int, error) {
	count := 0
	previous := ""
	for index, descriptor := range descriptors {
		if index%maxFileRecordsPerUpdate == 0 {
			if err := context.Cause(ctx); err != nil {
				return 0, err
			}
		}
		if descriptor.invalid {
			previous = ""
			continue
		}
		common := commonTreeDirectories(previous, descriptor.path)
		count += strings.Count(descriptor.path, "/") - common
		previous = descriptor.path
	}
	return count, nil
}

func commonTreeDirectories(left, right string) int {
	common := 0
	for {
		leftPart, leftRest := cutTreePath(left)
		rightPart, rightRest := cutTreePath(right)
		if leftRest == "" || rightRest == "" || leftPart != rightPart {
			return common
		}
		common++
		left, right = leftRest, rightRest
	}
}

func emitFlatTree(ctx context.Context, tree *fileTree, descriptors []treeDescriptor) error {
	open := make([]int, 0, 16)
	for position, descriptor := range descriptors {
		if position%maxFileRecordsPerUpdate == 0 {
			if err := context.Cause(ctx); err != nil {
				return err
			}
		}
		if position > 0 && !descriptor.invalid &&
			!descriptors[position-1].invalid && descriptor.path == descriptors[position-1].path {
			return errors.New("file tree contains a duplicate path")
		}
		if descriptor.invalid {
			open = closeTreeDirectories(tree, open, 0)
			appendTreeFile(tree, descriptor.file, "", "", -1, 0)
			continue
		}
		open = openPathDirectories(tree, open, descriptor.path)
		parent := -1
		if len(open) > 0 {
			parent = open[len(open)-1]
		}
		leaf := descriptor.path[strings.LastIndexByte(descriptor.path, '/')+1:]
		appendTreeFile(tree, descriptor.file, descriptor.path, leaf, parent, len(open))
	}
	closeTreeDirectories(tree, open, 0)
	return nil
}

func openPathDirectories(tree *fileTree, open []int, path string) []int {
	level := 0
	offset := 0
	for {
		relative := strings.IndexByte(path[offset:], '/')
		if relative < 0 {
			return closeTreeDirectories(tree, open, level)
		}
		end := offset + relative
		directoryPath := path[:end]
		if level < len(open) && tree.nodes[open[level]].identity.path == directoryPath {
			offset = end + 1
			level++
			continue
		}
		open = closeTreeDirectories(tree, open, level)
		parent := -1
		if level > 0 {
			parent = open[level-1]
		}
		index := len(tree.nodes)
		tree.nodes = append(tree.nodes, treeNode{
			label: path[offset:end],
			identity: treeNodeIdentity{
				path:      directoryPath,
				directory: true,
			},
			depth:  level,
			parent: parent,
		})
		tree.directories[directoryPath] = index
		open = append(open, index)
		offset = end + 1
		level++
	}
}

func closeTreeDirectories(tree *fileTree, open []int, keep int) []int {
	for index := len(open) - 1; index >= keep; index-- {
		tree.nodes[open[index]].subtreeEnd = len(tree.nodes)
	}
	return open[:keep]
}

func appendTreeFile(
	tree *fileTree,
	file review.FileID,
	path string,
	label string,
	parent int,
	depth int,
) {
	index := len(tree.nodes)
	tree.nodes = append(tree.nodes, treeNode{
		label:      label,
		identity:   treeNodeIdentity{path: path, file: file},
		depth:      depth,
		parent:     parent,
		subtreeEnd: index + 1,
	})
	tree.files[file] = index
}

func (tree fileTree) node(identity treeNodeIdentity) (treeNode, int, bool) {
	index, ok := tree.index(identity)
	if !ok || !tree.contains(index) {
		return treeNode{}, 0, false
	}
	return tree.nodes[index], index, true
}

func (tree fileTree) index(identity treeNodeIdentity) (int, bool) {
	if identity.directory {
		index, ok := tree.directories[identity.path]
		return index, ok
	}
	index, ok := tree.files[identity.file]
	return index, ok
}

func (tree fileTree) identityForFile(id review.FileID) (treeNodeIdentity, bool) {
	index, ok := tree.files[id]
	if !ok || !tree.contains(index) {
		return treeNodeIdentity{}, false
	}
	return tree.nodes[index].identity, true
}

func (tree fileTree) first() (treeNodeIdentity, bool) {
	index, ok := tree.firstIndex()
	if !ok {
		return treeNodeIdentity{}, false
	}
	return tree.nodes[index].identity, true
}

func (tree fileTree) empty() bool {
	if tree.projection != nil {
		return len(tree.projection) == 0
	}
	return len(tree.nodes) == 0
}

func (tree fileTree) contains(index int) bool {
	if index < 0 || index >= len(tree.nodes) {
		return false
	}
	if tree.projection == nil {
		return true
	}
	_, ok := slices.BinarySearch(tree.projection, index)
	return ok
}

func (tree fileTree) firstIndex() (int, bool) {
	if tree.projection != nil {
		if len(tree.projection) == 0 {
			return 0, false
		}
		return tree.projection[0], true
	}
	if len(tree.nodes) == 0 {
		return 0, false
	}
	return 0, true
}

func (tree fileTree) lastIndex() (int, bool) {
	if tree.projection != nil {
		if len(tree.projection) == 0 {
			return 0, false
		}
		return tree.projection[len(tree.projection)-1], true
	}
	if len(tree.nodes) == 0 {
		return 0, false
	}
	return len(tree.nodes) - 1, true
}

func (tree fileTree) indexAtOrAfter(index int) (int, bool) {
	if tree.projection == nil {
		if index < len(tree.nodes) {
			return index, true
		}
		return 0, false
	}
	position, _ := slices.BinarySearch(tree.projection, index)
	if position == len(tree.projection) {
		return 0, false
	}
	return tree.projection[position], true
}

func (tree fileTree) indexBefore(index int) (int, bool) {
	if tree.projection == nil {
		if index > 0 {
			return index - 1, true
		}
		return 0, false
	}
	position, _ := slices.BinarySearch(tree.projection, index)
	if position == 0 {
		return 0, false
	}
	return tree.projection[position-1], true
}

func (tree fileTree) project(
	ctx context.Context,
	files []review.FileID,
) (treeProjection, error) {
	ledger, err := newRetainedLedger(fileTreeResultReservation, int64(cap(files))*4)
	if err != nil {
		return nil, err
	}
	return tree.projectWithLedger(ctx, files, ledger)
}

func (tree fileTree) projectWithLedger(
	ctx context.Context,
	files []review.FileID,
	ledger *retainedLedger,
) (treeProjection, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	includedBytes := int64(len(tree.nodes))
	if err := ledger.reserve(includedBytes); err != nil {
		return nil, err
	}
	included := make([]bool, len(tree.nodes))
	for position, id := range files {
		if position%maxFileRecordsPerUpdate == 0 {
			if err := context.Cause(ctx); err != nil {
				return nil, err
			}
		}
		index, ok := tree.files[id]
		if !ok {
			continue
		}
		for index >= 0 && !included[index] {
			included[index] = true
			index = tree.nodes[index].parent
		}
	}
	projectionLength := 0
	for _, keep := range included {
		if keep {
			projectionLength++
		}
	}
	projectionBytes := int64(max(projectionLength, 1)) * 8
	if err := ledger.reserve(projectionBytes); err != nil {
		return nil, err
	}
	projection := make([]int, 0, max(projectionLength, 1))
	for index, keep := range included {
		if index%maxFileRecordsPerUpdate == 0 {
			if err := context.Cause(ctx); err != nil {
				return nil, err
			}
		}
		if keep {
			projection = append(projection, index)
		}
	}
	ledger.release(includedBytes)
	return projection, nil
}

func (tree fileTree) withProjection(projection treeProjection) fileTree {
	tree.projection = projection
	return tree
}

func (tree fileTree) retainedBytes() int64 {
	return int64(
		cap(tree.nodes)*fileTreeNodeCharge +
			len(tree.files)*fileTreeFileCharge +
			len(tree.directories)*fileTreeDirectoryCharge +
			cap(tree.projection)*8,
	)
}

func (tree fileTree) nextVisible(
	index int,
	collapsed collapsedTreeState,
) (int, bool) {
	node := tree.nodes[index]
	next := index + 1
	if node.identity.directory {
		if collapsed.isCollapsed(index) {
			next = node.subtreeEnd
		}
	}
	next, ok := tree.indexAtOrAfter(next)
	if !ok {
		return index, false
	}
	return next, true
}

func (tree fileTree) previousVisible(
	index int,
	collapsed collapsedTreeState,
) (int, bool) {
	candidate, ok := tree.indexBefore(index)
	if !ok {
		return index, false
	}
	if directory, hidden := collapsed.outermostContaining(candidate); hidden {
		return directory, true
	}
	return candidate, true
}

func (tree fileTree) visibleIdentity(
	identity treeNodeIdentity,
	collapsed collapsedTreeState,
) (treeNodeIdentity, bool) {
	_, index, ok := tree.node(identity)
	if !ok {
		return tree.first()
	}
	if directory, hidden := collapsed.outermostContaining(index); hidden {
		return tree.nodes[directory].identity, true
	}
	return identity, true
}

func (tree fileTree) window(
	cursor treeNodeIdentity,
	height int,
	collapsed collapsedTreeState,
) []treeNode {
	if height <= 0 || tree.empty() {
		return nil
	}
	cursor, ok := tree.visibleIdentity(cursor, collapsed)
	if !ok {
		return nil
	}
	index, _ := tree.index(cursor)
	start := index
	for range height / 2 {
		previous, exists := tree.previousVisible(start, collapsed)
		if !exists {
			break
		}
		start = previous
	}
	rows := make([]treeNode, 0, height)
	for len(rows) < height {
		rows = append(rows, tree.nodes[start])
		next, exists := tree.nextVisible(start, collapsed)
		if !exists {
			break
		}
		start = next
	}
	return rows
}

func (model *Model) currentFileTree() fileTree {
	if model.filterQuery != "" && model.filteredFileTree != nil {
		return *model.filteredFileTree
	}
	if model.review.ShowHidden() {
		return model.allFileTree
	}
	return model.visibleFileTree
}

func (model *Model) selectTreeCursor() {
	tree := model.currentFileTree()
	if identity, ok := tree.visibleIdentity(model.treeCursor, model.collapsedTree); ok {
		model.treeCursor = identity
		return
	}
	if selected, ok := model.review.SelectedFile(); ok {
		if identity, exists := tree.identityForFile(selected); exists {
			model.treeCursor = identity
			return
		}
	}
	model.treeCursor, _ = tree.first()
}

func (model *Model) selectTreeNode(identity treeNodeIdentity) {
	tree := model.currentFileTree()
	node, _, ok := tree.node(identity)
	if !ok {
		return
	}
	model.treeCursor = identity
	if !node.identity.directory {
		if next, err := model.review.SelectFile(node.identity.file); err == nil {
			model.review = next
		}
	}
}

func (model *Model) moveTree(delta int) {
	tree := model.currentFileTree()
	cursor, ok := tree.visibleIdentity(model.treeCursor, model.collapsedTree)
	if !ok {
		return
	}
	index, _ := tree.index(cursor)
	direction := 1
	if delta < 0 {
		direction = -1
	}
	for range max(delta*direction, 0) {
		var exists bool
		if direction > 0 {
			index, exists = tree.nextVisible(index, model.collapsedTree)
		} else {
			index, exists = tree.previousVisible(index, model.collapsedTree)
		}
		if !exists {
			break
		}
	}
	model.selectTreeNode(tree.nodes[index].identity)
}

func (model *Model) activateTreeNode() {
	tree := model.currentFileTree()
	node, _, ok := tree.node(model.treeCursor)
	if !ok {
		return
	}
	if node.identity.directory {
		model.toggleTreeDirectory(treeDirectoryToggle)
		return
	}
	model.activateSelection()
}

type treeDirectoryAction uint8

const (
	treeDirectoryToggle treeDirectoryAction = iota + 1
	treeDirectoryExpand
	treeDirectoryCollapse
)

func (model *Model) toggleTreeDirectory(action treeDirectoryAction) {
	if model.focus != FocusFileTree {
		return
	}
	tree := model.currentFileTree()
	node, index, ok := tree.node(model.treeCursor)
	if !ok {
		return
	}
	if !node.identity.directory && action == treeDirectoryCollapse && node.parent >= 0 {
		index = node.parent
		node = tree.nodes[node.parent]
		model.treeCursor = node.identity
	}
	if !node.identity.directory {
		return
	}
	collapsed := model.collapsedTree.isCollapsed(index)
	disposition := collapseExpanded
	if collapsed {
		disposition = collapseCollapsed
	}
	switch action {
	case treeDirectoryToggle:
		if collapsed {
			disposition = collapseExpanded
		} else {
			disposition = collapseCollapsed
		}
	case treeDirectoryExpand:
		disposition = collapseExpanded
	case treeDirectoryCollapse:
		disposition = collapseCollapsed
	}
	if _, err := model.collapsedTree.set(index, node.subtreeEnd, disposition); err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
	}
}
