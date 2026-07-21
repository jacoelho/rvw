package app

import (
	"cmp"
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
	maxFileTreeRetainedBytes = 16 << 20
	fileTreeNodeCharge       = 192
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
	positions map[treeNodeIdentity]int
	files     map[review.FileID]int
	nodes     []treeNode
}

type treeBuildNode struct {
	children map[string]*treeBuildEntry
}

type treeBuildEntry struct {
	directory *treeBuildNode
	name      string
	path      string
	file      review.FileID
}

type fileTreeBudget struct {
	nodes         int
	retainedBytes int
}

func (budget *fileTreeBudget) reserve(name, path string) error {
	budget.nodes++
	budget.retainedBytes += fileTreeNodeCharge + len(name) + len(path)
	if budget.nodes > maxFileTreeNodes ||
		budget.retainedBytes > maxFileTreeRetainedBytes {
		return fmt.Errorf(
			"%w: %d nodes and %d bytes",
			ErrFileTreeLimited,
			budget.nodes,
			budget.retainedBytes,
		)
	}
	return nil
}

func buildFileTreeContext(
	ctx context.Context,
	snapshot review.Snapshot,
	files []review.FileID,
) (fileTree, error) {
	if err := context.Cause(ctx); err != nil {
		return fileTree{}, err
	}
	root := &treeBuildNode{children: make(map[string]*treeBuildEntry)}
	budget := fileTreeBudget{}
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
		if !validUTF8 {
			name := file.DisplayPath()
			if err := budget.reserve(name, ""); err != nil {
				return fileTree{}, err
			}
			root.children["\x00"+strconv.FormatUint(uint64(id.Uint32()), 10)] =
				&treeBuildEntry{name: name, file: id}
			continue
		}
		parts := strings.Split(path, "/")
		current := root
		prefixBytes := 0
		for index, part := range parts {
			if index == len(parts)-1 {
				if _, duplicate := current.children[part]; duplicate {
					return fileTree{}, errors.New("file tree contains a duplicate path")
				}
				if err := budget.reserve(part, path); err != nil {
					return fileTree{}, err
				}
				current.children[part] = &treeBuildEntry{
					name: part,
					path: path,
					file: id,
				}
				break
			}
			prefixBytes += len(part)
			directoryPath := path[:prefixBytes]
			prefixBytes++
			entry, exists := current.children[part]
			if !exists {
				if err := budget.reserve(part, directoryPath); err != nil {
					return fileTree{}, err
				}
				entry = &treeBuildEntry{
					name:      part,
					path:      directoryPath,
					directory: &treeBuildNode{children: make(map[string]*treeBuildEntry)},
				}
				current.children[part] = entry
			}
			current = entry.directory
		}
	}
	tree := fileTree{
		nodes:     make([]treeNode, 0, len(files)),
		positions: make(map[treeNodeIdentity]int, len(files)),
		files:     make(map[review.FileID]int, len(files)),
	}
	if err := flattenTree(ctx, &tree, root); err != nil {
		return fileTree{}, err
	}
	return tree, nil
}

func flattenTree(
	ctx context.Context,
	tree *fileTree,
	build *treeBuildNode,
) error {
	type frame struct {
		entries []treeBuildEntry
		next    int
		parent  int
		depth   int
		owner   int
	}
	stack := []frame{{
		entries: sortedTreeEntries(build),
		parent:  -1,
		owner:   -1,
	}}
	processed := 0
	for len(stack) != 0 {
		current := &stack[len(stack)-1]
		if current.next == len(current.entries) {
			if current.owner >= 0 {
				tree.nodes[current.owner].subtreeEnd = len(tree.nodes)
			}
			stack = stack[:len(stack)-1]
			continue
		}
		if processed%maxFileRecordsPerUpdate == 0 {
			if err := context.Cause(ctx); err != nil {
				return err
			}
		}
		processed++
		entry := current.entries[current.next]
		current.next++
		identity := treeNodeIdentity{
			path:      entry.path,
			file:      entry.file,
			directory: entry.directory != nil,
		}
		index := len(tree.nodes)
		tree.nodes = append(tree.nodes, treeNode{
			identity: identity,
			label:    entry.name,
			depth:    current.depth,
			parent:   current.parent,
		})
		tree.positions[identity] = index
		if entry.directory != nil {
			stack = append(stack, frame{
				entries: sortedTreeEntries(entry.directory),
				parent:  index,
				depth:   current.depth + 1,
				owner:   index,
			})
		} else {
			tree.files[entry.file] = index
			tree.nodes[index].subtreeEnd = len(tree.nodes)
		}
	}
	return nil
}

func sortedTreeEntries(build *treeBuildNode) []treeBuildEntry {
	entries := make([]treeBuildEntry, 0, len(build.children))
	for _, entry := range build.children {
		entries = append(entries, *entry)
	}
	slices.SortFunc(entries, func(left, right treeBuildEntry) int {
		return cmp.Compare(left.name, right.name)
	})
	return entries
}

func (tree fileTree) node(identity treeNodeIdentity) (treeNode, int, bool) {
	index, ok := tree.positions[identity]
	if !ok {
		return treeNode{}, 0, false
	}
	return tree.nodes[index], index, true
}

func (tree fileTree) identityForFile(id review.FileID) (treeNodeIdentity, bool) {
	index, ok := tree.files[id]
	if !ok {
		return treeNodeIdentity{}, false
	}
	return tree.nodes[index].identity, true
}

func (tree fileTree) first() (treeNodeIdentity, bool) {
	if len(tree.nodes) == 0 {
		return treeNodeIdentity{}, false
	}
	return tree.nodes[0].identity, true
}

func (tree fileTree) nextVisible(
	index int,
	collapsed map[string]struct{},
) (int, bool) {
	node := tree.nodes[index]
	next := index + 1
	if node.identity.directory {
		if _, isCollapsed := collapsed[node.identity.path]; isCollapsed {
			next = node.subtreeEnd
		}
	}
	if next >= len(tree.nodes) {
		return index, false
	}
	return next, true
}

func (tree fileTree) previousVisible(
	index int,
	collapsed map[string]struct{},
) (int, bool) {
	if index <= 0 {
		return index, false
	}
	candidate := index - 1
	for {
		parent := tree.nodes[candidate].parent
		if parent < 0 {
			return candidate, true
		}
		if _, hidden := collapsed[tree.nodes[parent].identity.path]; !hidden {
			return candidate, true
		}
		candidate = parent
	}
}

func (tree fileTree) visibleIdentity(
	identity treeNodeIdentity,
	collapsed map[string]struct{},
) (treeNodeIdentity, bool) {
	_, index, ok := tree.node(identity)
	if !ok {
		return tree.first()
	}
	for parent := tree.nodes[index].parent; parent >= 0; parent = tree.nodes[parent].parent {
		if _, hidden := collapsed[tree.nodes[parent].identity.path]; hidden {
			return tree.nodes[parent].identity, true
		}
	}
	return identity, true
}

func (tree fileTree) window(
	cursor treeNodeIdentity,
	height int,
	collapsed map[string]struct{},
) []treeNode {
	if height <= 0 || len(tree.nodes) == 0 {
		return nil
	}
	cursor, ok := tree.visibleIdentity(cursor, collapsed)
	if !ok {
		return nil
	}
	index := tree.positions[cursor]
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
	if identity, ok := tree.visibleIdentity(model.treeCursor, model.collapsedTrees); ok {
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
	cursor, ok := tree.visibleIdentity(model.treeCursor, model.collapsedTrees)
	if !ok {
		return
	}
	index := tree.positions[cursor]
	direction := 1
	if delta < 0 {
		direction = -1
	}
	for range max(delta*direction, 0) {
		var exists bool
		if direction > 0 {
			index, exists = tree.nextVisible(index, model.collapsedTrees)
		} else {
			index, exists = tree.previousVisible(index, model.collapsedTrees)
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
	node, _, ok := tree.node(model.treeCursor)
	if !ok {
		return
	}
	if !node.identity.directory && action == treeDirectoryCollapse && node.parent >= 0 {
		node = tree.nodes[node.parent]
		model.treeCursor = node.identity
	}
	if !node.identity.directory {
		return
	}
	_, collapsed := model.collapsedTrees[node.identity.path]
	switch action {
	case treeDirectoryToggle:
		if collapsed {
			delete(model.collapsedTrees, node.identity.path)
		} else {
			model.collapsedTrees[node.identity.path] = struct{}{}
		}
	case treeDirectoryExpand:
		delete(model.collapsedTrees, node.identity.path)
	case treeDirectoryCollapse:
		model.collapsedTrees[node.identity.path] = struct{}{}
	}
}
