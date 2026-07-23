package review

import (
	"maps"
	"sort"
	"strings"
)

type pendingBindingKind uint8

const (
	pendingBindingBody pendingBindingKind = iota + 1
	pendingBindingComment
)

type pendingConflict struct {
	base         commentContent
	local        commentContent
	remote       commentContent
	localDeleted bool
}

type pendingBinding struct {
	conflict     *pendingConflict
	remoteNode   CommentNodeID
	baseline     commentContent
	wireBaseline string
	marker       string
	target       PendingCommentTarget
	remoteID     CommentDatabaseID
	kind         pendingBindingKind
	capabilities PendingCapabilities
	deleted      bool
}

type pendingReviewState struct {
	bindings   map[CommentID]pendingBinding
	byNode     map[CommentNodeID]CommentID
	bodyID     *CommentID
	nodeID     ReviewNodeID
	commit     ObjectID
	bodyMarker string
	bodyBase   string
	databaseID ReviewDatabaseID
	bodyUpdate bool
}

// PendingBindingState describes a local comment's relationship to GitHub.
type PendingBindingState uint8

// PendingBindingNew identifies a supported new binding state.
const (
	PendingBindingNew PendingBindingState = iota + 1
	PendingBindingClean
	PendingBindingModified
	PendingBindingDeleted
	PendingBindingConflict
)

// String returns the receiver's string value.
func (state PendingBindingState) String() string {
	switch state {
	case PendingBindingNew:
		return "new"
	case PendingBindingClean:
		return draftValue
	case PendingBindingModified:
		return "modified"
	case PendingBindingDeleted:
		return "deleted"
	case PendingBindingConflict:
		return "conflict"
	default:
		return invalidValue
	}
}

// PendingConflictResolution describes an explicit three-way merge choice.
type PendingConflictResolution uint8

// PendingConflictLocal identifies a supported conflict resolution.
const (
	PendingConflictLocal PendingConflictResolution = iota + 1
	PendingConflictRemote
	PendingConflictBase
)

// ResolvePendingConflict resolves one pending-review merge conflict.
func (current Review) ResolvePendingConflict(
	id CommentID,
	resolution PendingConflictResolution,
) (Review, error) {
	started, mutation, err := current.StartResolvePendingConflict(
		synchronousCommentMutationID,
		id,
		resolution,
	)
	if err != nil {
		return current, ErrInvalidReviewTransition
	}
	prepared, err := PrepareCommentMutationCancellable(nil, mutation)
	if err != nil {
		return current, ErrInvalidReviewTransition
	}
	next, err := started.ApplyCommentMutation(prepared)
	if err != nil {
		return current, ErrInvalidReviewTransition
	}
	return next, nil
}

const (
	publicationMarkerPrefix  = "<!-- rvw-publication:v1:"
	draftCommentMarkerPrefix = "<!-- rvw-draft-comment:v1:"
	markerSuffix             = " -->"
)

func splitPendingMarker(body string, prefix string) (string, string) {
	markerSize := len(prefix) + 32 + len(markerSuffix)
	if len(body) < markerSize {
		return body, ""
	}
	marker := body[len(body)-markerSize:]
	if !validPendingMarker(marker, prefix) {
		return body, ""
	}
	visible := strings.TrimSuffix(body[:len(body)-markerSize], "\n\n")
	return visible, marker
}

func validPendingMarker(marker, prefix string) bool {
	if len(marker) != len(prefix)+32+len(markerSuffix) ||
		!strings.HasPrefix(marker, prefix) || !strings.HasSuffix(marker, markerSuffix) {
		return false
	}
	for _, character := range marker[len(prefix) : len(prefix)+32] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func importPendingReviewCancellable(
	current Review,
	cancelled func() error,
) (Review, error) {
	pending, ok := current.snapshot.PendingReview()
	if !ok {
		return current, nil
	}
	state := pendingReviewState{
		bindings:   make(map[CommentID]pendingBinding, pending.CommentCount()+1),
		byNode:     make(map[CommentNodeID]CommentID, pending.CommentCount()),
		nodeID:     pending.nodeID,
		databaseID: pending.databaseID,
		commit:     pending.commit,
		bodyUpdate: pending.viewerUpdate,
	}
	body, marker := splitPendingMarker(pending.body, publicationMarkerPrefix)
	content := commentContent{kind: current.commentKinds.first(), body: body}
	if marker != "" {
		content = decodeOwnedCommentWire(body, current.commentKinds)
	}
	state.bodyBase = strings.Clone(body)
	state.bodyMarker = strings.Clone(marker)
	pathMappings, err := current.snapshot.remotePathMappingsCancellable(cancelled)
	if err != nil {
		return Review{}, err
	}
	if strings.TrimSpace(body) != "" {
		id, err := appendPendingCommentUnindexed(
			&current,
			content,
			ReviewCommentTarget(),
			commentFlags{},
		)
		if err != nil {
			return Review{}, err
		}
		state.bodyID = new(id)
		state.bindings[id] = pendingBinding{
			baseline:     cloneCommentContent(content),
			wireBaseline: strings.Clone(body),
			marker:       strings.Clone(marker),
			kind:         pendingBindingBody,
		}
	}
	for index, remote := range pending.comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return Review{}, err
			}
		}
		body, commentMarker := splitPendingMarker(remote.body, draftCommentMarkerPrefix)
		content := commentContent{kind: current.commentKinds.first(), body: body}
		if commentMarker != "" {
			content = decodeOwnedCommentWire(body, current.commentKinds)
		}
		target, flags := projectPendingTargetWithMappings(pathMappings, remote.target)
		id, err := appendPendingCommentUnindexed(&current, content, target, flags)
		if err != nil {
			return Review{}, err
		}
		state.bindings[id] = pendingBinding{
			target:       remote.target,
			remoteNode:   remote.nodeID,
			remoteID:     remote.databaseID,
			baseline:     cloneCommentContent(content),
			wireBaseline: strings.Clone(body),
			marker:       strings.Clone(commentMarker),
			kind:         pendingBindingComment,
			capabilities: remote.capabilities,
		}
		state.byNode[remote.nodeID] = id
	}
	current.pending = new(state)
	if err := current.rebuildCommentAnchorsCancellable(cancelled); err != nil {
		return Review{}, err
	}
	return current, nil
}

func appendPendingCommentUnindexed(
	current *Review,
	content commentContent,
	target CommentTarget,
	flags commentFlags,
) (CommentID, error) {
	validated, err := NewCommentBody(content.body)
	if err != nil || len(current.comments) >= MaxComments || current.nextComment >= MaxComments ||
		current.commentBody+int64(len(validated)) > MaxCommentBodyTotal || !flags.valid() {
		return 0, ErrInvalidPendingReview
	}
	id := CommentID(current.nextComment)
	comment := Comment{
		id:     id,
		target: target,
		body:   validated,
		kind:   content.kind,
		flags:  flags,
	}
	current.comments = append(current.comments, comment)
	current.commentBody += int64(len(validated))
	current.nextComment++
	return id, nil
}

func projectPendingTargetWithMappings(
	mappings remotePathProjection,
	target PendingCommentTarget,
) (CommentTarget, commentFlags) {
	mapping, exists := mappings.lookup(target.path.raw)
	if !exists {
		mapping = remotePathMapping{status: RemoteMappingMissing}
	}
	return projectPendingTargetFile(mapping, target)
}

func projectPendingTargetFile(
	mapping remotePathMapping,
	target PendingCommentTarget,
) (CommentTarget, commentFlags) {
	flags := commentFlags{}
	if target.outdated {
		flags = flags.markStale()
	}
	if mapping.status != RemoteMappingMapped {
		return ReviewCommentTarget(), flags.markUnmappable()
	}
	if target.subject == PendingSubjectLine && target.line != nil {
		start := *target.line
		if target.startLine != nil {
			start = *target.startLine
		}
		if target.startLine == nil || target.startSide == target.side {
			lineTarget, err := LineCommentTarget(mapping.file, target.side, start, *target.line)
			if err == nil {
				return lineTarget.withPath(target.path), flags
			}
		}
	}
	fileTarget, err := FileCommentTarget(mapping.file)
	if err != nil {
		return ReviewCommentTarget(), flags.markUnmappable()
	}
	return fileTarget.withPath(target.path), flags
}

func (state *pendingReviewState) clone() *pendingReviewState {
	if state == nil {
		return nil
	}
	next := *state
	next.bindings = make(map[CommentID]pendingBinding, len(state.bindings))
	for id, binding := range state.bindings {
		if binding.conflict != nil {
			conflict := *binding.conflict
			binding.conflict = new(conflict)
		}
		next.bindings[id] = binding
	}
	next.byNode = make(map[CommentNodeID]CommentID, len(state.byNode))
	maps.Copy(next.byNode, state.byNode)
	if state.bodyID != nil {
		next.bodyID = new(*state.bodyID)
	}
	return &next
}

// PendingBindingState returns a comment's derived synchronization state.
func (current Review) PendingBindingState(id CommentID) PendingBindingState {
	if current.pending == nil {
		return PendingBindingNew
	}
	binding, bound := current.pending.bindings[id]
	if !bound {
		return PendingBindingNew
	}
	if binding.conflict != nil {
		return PendingBindingConflict
	}
	if binding.deleted {
		return PendingBindingDeleted
	}
	comment, ok := current.commentByID(id)
	if !ok || commentContentFrom(comment) != binding.baseline {
		return PendingBindingModified
	}
	return PendingBindingClean
}

func (current Review) commentByID(id CommentID) (Comment, bool) {
	index := sort.Search(len(current.comments), func(index int) bool {
		return current.comments[index].id >= id
	})
	if index == len(current.comments) || current.comments[index].id != id {
		return Comment{}, false
	}
	return current.comments[index], true
}

// PendingReviewSummary returns acquisition and local synchronization state.
func (current Review) PendingReviewSummary() PendingReviewSummary {
	summary := current.snapshot.PendingReviewSummary()
	if current.pending == nil {
		return summary
	}
	summary.ReviewID = current.pending.databaseID
	summary.Dirty = current.pendingDirty
	summary.Conflicts = current.pendingConflict
	return summary
}

func (current Review) hasPendingLocalChanges() bool {
	return current.pendingDirty != 0
}
