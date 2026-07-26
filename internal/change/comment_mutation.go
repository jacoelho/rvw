package change

import (
	"errors"
	"sort"
	"strings"
)

// ErrInvalidCommentMutation identifies a malformed or stale comment mutation.
var ErrInvalidCommentMutation = errors.New("invalid comment mutation")

const synchronousCommentMutationID OperationID = ^OperationID(0)

type commentMutationIntent interface {
	apply(cancelled func() error, state *commentState) (CommentID, error)
	kind() CommentMutationKind
	commentMutationIntent()
}

// CommentMutationKind identifies one closed comment-state mutation.
type CommentMutationKind uint8

// CommentMutationBeginEdit identifies an asynchronous begin-edit mutation.
const (
	CommentMutationBeginEdit CommentMutationKind = iota + 1
	CommentMutationSave
	CommentMutationDelete
	CommentMutationResolveConflict
)

type beginEditCommentIntent struct{ id CommentID }
type saveCommentIntent struct{}
type deleteCommentIntent struct{ id CommentID }
type resolvePendingConflictIntent struct {
	id         CommentID
	resolution PendingConflictResolution
}

func (beginEditCommentIntent) commentMutationIntent()       {}
func (saveCommentIntent) commentMutationIntent()            {}
func (deleteCommentIntent) commentMutationIntent()          {}
func (resolvePendingConflictIntent) commentMutationIntent() {}

func (beginEditCommentIntent) kind() CommentMutationKind { return CommentMutationBeginEdit }
func (saveCommentIntent) kind() CommentMutationKind      { return CommentMutationSave }
func (deleteCommentIntent) kind() CommentMutationKind    { return CommentMutationDelete }
func (resolvePendingConflictIntent) kind() CommentMutationKind {
	return CommentMutationResolveConflict
}

// CommentMutation is an opaque, revision-bound comment-state operation.
type CommentMutation struct {
	intent    commentMutationIntent
	state     commentState
	operation OperationID
	workspace WorkspaceRevision
	source    SourceID
}

// Operation returns the mutation operation identity.
func (mutation CommentMutation) Operation() OperationID { return mutation.operation }

// Kind returns the closed mutation kind derived from its private intent.
func (mutation CommentMutation) Kind() CommentMutationKind {
	if mutation.intent == nil {
		return 0
	}
	return mutation.intent.kind()
}

// RetainedBytes returns the immutable input ownership prolonged by the work.
func (mutation CommentMutation) RetainedBytes() int64 {
	if !mutation.valid() {
		return 0
	}
	return mutation.state.retainedBytes + 128
}

// ConstructionBytes returns the conservative simultaneous input/result peak.
func (mutation CommentMutation) ConstructionBytes() int64 {
	if !mutation.valid() {
		return 0
	}
	return mutation.RetainedBytes()*2 + MaxCommentBodyBytes +
		int64(len(mutation.state.comments))*commentMetadataBytes + 1_024
}

func (mutation CommentMutation) valid() bool {
	return mutation.operation != 0 && mutation.workspace != 0 && mutation.source != 0 &&
		mutation.intent != nil && mutation.state.revision != 0 &&
		mutation.state.activeMutation == mutation.operation &&
		mutation.state.mutationKind == mutation.intent.kind()
}

// PreparedCommentMutation is a complete replacement comment state.
type PreparedCommentMutation struct {
	state        commentState
	operation    OperationID
	workspace    WorkspaceRevision
	source       SourceID
	baseRevision uint64
	saved        CommentID
	hasSaved     bool
	kind         CommentMutationKind
	valid        bool
}

// Operation returns the prepared mutation operation identity.
func (prepared PreparedCommentMutation) Operation() OperationID { return prepared.operation }

// SavedComment returns the saved comment identity for save operations.
func (prepared PreparedCommentMutation) SavedComment() (CommentID, bool) {
	return prepared.saved, prepared.valid && prepared.hasSaved
}

// RetainedBytes returns the complete prepared state ownership.
func (prepared PreparedCommentMutation) RetainedBytes() int64 {
	if !prepared.valid {
		return 0
	}
	return prepared.state.retainedBytes + 128
}

// StartBeginEditComment starts asynchronous comment editing preparation.
func (review Review) StartBeginEditComment(
	operation OperationID,
	id CommentID,
) (Review, CommentMutation, error) {
	return review.startCommentMutation(operation, beginEditCommentIntent{id: id})
}

// StartSaveComment starts asynchronous draft saving.
func (review Review) StartSaveComment(
	operation OperationID,
) (Review, CommentMutation, error) {
	return review.startCommentMutation(operation, saveCommentIntent{})
}

// StartDeleteComment starts asynchronous comment deletion.
func (review Review) StartDeleteComment(
	operation OperationID,
	id CommentID,
) (Review, CommentMutation, error) {
	return review.startCommentMutation(operation, deleteCommentIntent{id: id})
}

// StartResolvePendingConflict starts asynchronous conflict resolution.
func (review Review) StartResolvePendingConflict(
	operation OperationID,
	id CommentID,
	resolution PendingConflictResolution,
) (Review, CommentMutation, error) {
	if resolution < PendingConflictLocal || resolution > PendingConflictBase {
		return review, CommentMutation{}, ErrInvalidCommentMutation
	}
	return review.startCommentMutation(operation, resolvePendingConflictIntent{
		id: id, resolution: resolution,
	})
}

func (review Review) startCommentMutation(
	operation OperationID,
	intent commentMutationIntent,
) (Review, CommentMutation, error) {
	if operation == 0 || intent == nil || review.activeMutation != 0 ||
		review.publicationBlocksCommentMutation() {
		return review, CommentMutation{}, ErrInvalidCommentMutation
	}
	next := review.cloneCommentState()
	next.activeMutation = operation
	next.mutationKind = intent.kind()
	mutation := CommentMutation{
		operation: operation,
		workspace: review.workspace,
		source:    review.snapshot.SourceID(),
		state:     next.commentState,
		intent:    intent,
	}
	return next, mutation, nil
}

func (review Review) publicationBlocksCommentMutation() bool {
	if review.publication.pendingRefresh {
		return true
	}
	switch review.publication.status {
	case PublicationChecking, PublicationChecked, PublicationPublishing,
		PublicationUnknown, PublicationReconciling:
		return true
	default:
		return false
	}
}

// PrepareCommentMutationCancellable prepares a complete replacement state.
func PrepareCommentMutationCancellable(
	cancelled func() error,
	mutation CommentMutation,
) (PreparedCommentMutation, error) {
	if !mutation.valid() {
		return PreparedCommentMutation{}, ErrInvalidCommentMutation
	}
	state, err := cloneCommentStateCancellable(cancelled, mutation.state, mutation.intent)
	if err != nil {
		return PreparedCommentMutation{}, err
	}
	saved, err := mutation.intent.apply(cancelled, &state)
	if err != nil {
		return PreparedCommentMutation{}, err
	}
	state.activeMutation = 0
	state.mutationKind = 0
	state.revision = mutation.state.revision + 1
	if err := state.refreshCancellable(cancelled); err != nil {
		return PreparedCommentMutation{}, err
	}
	prepared := PreparedCommentMutation{
		operation:    mutation.operation,
		workspace:    mutation.workspace,
		source:       mutation.source,
		baseRevision: mutation.state.revision,
		state:        state,
		saved:        saved,
		hasSaved:     false,
		kind:         mutation.Kind(),
		valid:        true,
	}
	_, prepared.hasSaved = mutation.intent.(saveCommentIntent)
	if prepared.RetainedBytes() > mutation.ConstructionBytes() {
		return PreparedCommentMutation{}, ErrInvalidCommentMutation
	}
	return prepared, nil
}

// ApplyCommentMutation atomically installs prepared comment state.
func (review Review) ApplyCommentMutation(
	prepared PreparedCommentMutation,
) (Review, error) {
	if !prepared.valid || prepared.operation == 0 ||
		review.activeMutation != prepared.operation ||
		review.mutationKind != prepared.kind ||
		review.revision != prepared.baseRevision ||
		review.workspace != prepared.workspace ||
		review.snapshot.SourceID() != prepared.source ||
		prepared.state.activeMutation != 0 || prepared.state.mutationKind != 0 ||
		prepared.state.revision != prepared.baseRevision+1 {
		return review, ErrInvalidCommentMutation
	}
	next := review.clone()
	next.commentState = prepared.state
	return next, nil
}

// CancelCommentMutation clears the matching active mutation.
func (review Review) CancelCommentMutation(operation OperationID) (Review, error) {
	if operation == 0 || review.activeMutation != operation {
		return review, ErrInvalidCommentMutation
	}
	next := review.cloneCommentState()
	next.activeMutation = 0
	next.mutationKind = 0
	return next, nil
}

// CommentMutationOperation returns the active comment mutation identity.
func (review Review) CommentMutationOperation() OperationID { return review.activeMutation }

// CommentMutationKind returns the active mutation kind, or zero when idle.
func (review Review) CommentMutationKind() CommentMutationKind { return review.mutationKind }

func cloneCommentStateCancellable(
	cancelled func() error,
	state commentState,
	intent commentMutationIntent,
) (commentState, error) {
	if err := cancellationError(cancelled); err != nil {
		return commentState{}, err
	}
	next := state
	capacity := len(state.comments)
	if _, saving := intent.(saveCommentIntent); saving && state.draft != nil &&
		state.draft.editing == nil {
		capacity++
	}
	next.comments = make([]Comment, len(state.comments), capacity)
	for start := 0; start < len(state.comments); start += 2_048 {
		if err := cancellationError(cancelled); err != nil {
			return commentState{}, err
		}
		copy(next.comments[start:min(start+2_048, len(state.comments))],
			state.comments[start:min(start+2_048, len(state.comments))])
	}
	if err := cancellationError(cancelled); err != nil {
		return commentState{}, err
	}
	next.commentAnchors = make(commentAnchorCounts, len(state.commentAnchors))
	inspected := 0
	for anchor, count := range state.commentAnchors {
		if inspected%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return commentState{}, err
			}
		}
		inspected++
		next.commentAnchors[anchor] = count
	}
	if state.draft != nil {
		draft := *state.draft
		next.draft = &draft
	}
	if state.pending != nil {
		pending, err := clonePendingStateCancellable(cancelled, state.pending)
		if err != nil {
			return commentState{}, err
		}
		next.pending = pending
	}
	return next, cancellationError(cancelled)
}

func clonePendingStateCancellable(
	cancelled func() error,
	state *pendingReviewState,
) (*pendingReviewState, error) {
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	next := *state
	next.bindings = make(map[CommentID]pendingBinding, len(state.bindings))
	inspected := 0
	for id, binding := range state.bindings {
		if inspected%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return nil, err
			}
		}
		inspected++
		if binding.conflict != nil {
			conflict := *binding.conflict
			binding.conflict = &conflict
		}
		next.bindings[id] = binding
	}
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	next.byNode = make(map[CommentNodeID]CommentID, len(state.byNode))
	inspected = 0
	for node, id := range state.byNode {
		if inspected%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return nil, err
			}
		}
		inspected++
		next.byNode[node] = id
	}
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	if state.bodyID != nil {
		next.bodyID = new(*state.bodyID)
	}
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	return &next, nil
}

func (intent beginEditCommentIntent) apply(
	cancelled func() error,
	state *commentState,
) (CommentID, error) {
	if state.draft != nil {
		return 0, ErrInvalidReviewTransition
	}
	if err := cancellationError(cancelled); err != nil {
		return 0, err
	}
	index := commentIndex(state.comments, intent.id)
	if index < 0 {
		return 0, ErrInvalidReviewTransition
	}
	comment := state.comments[index]
	if state.pending != nil {
		if binding, bound := state.pending.bindings[intent.id]; bound {
			if binding.kind == pendingBindingBody && !state.pending.bodyUpdate ||
				binding.kind == pendingBindingComment &&
					!binding.capabilities.Has(PendingCapabilityUpdate) {
				return 0, ErrInvalidReviewTransition
			}
			if binding.deleted {
				restorePendingBindingOwned(state, intent.id, binding)
			}
		}
	}
	state.draft = &Draft{
		target: comment.target, body: comment.body.String(),
		editing: new(intent.id),
	}
	return 0, nil
}

func (saveCommentIntent) apply(
	cancelled func() error,
	state *commentState,
) (CommentID, error) {
	if state.draft == nil {
		return 0, ErrInvalidReviewTransition
	}
	body, err := commentBodyCancellable(cancelled, state.draft.body)
	if err != nil {
		if errors.Is(err, ErrInvalidComment) {
			return 0, ErrInvalidReviewTransition
		}
		return 0, err
	}
	var id CommentID
	if state.draft.editing != nil {
		index := commentIndex(state.comments, *state.draft.editing)
		if index < 0 ||
			state.commentBody-int64(len(state.comments[index].body))+int64(len(body)) >
				MaxCommentBodyTotal {
			return 0, ErrInvalidReviewTransition
		}
		id = state.comments[index].id
		beforeComment := state.comments[index]
		before := state.commentAnchorMembership(&state.comments[index])
		state.commentBody -= int64(len(beforeComment.body))
		state.comments[index].target = state.draft.target
		state.comments[index].body = body
		state.commentBody += int64(len(body))
		state.applyCommentAnchorTransition(
			before,
			state.commentAnchorMembership(&state.comments[index]),
		)
	} else {
		if len(state.comments) >= MaxComments || state.nextComment >= MaxComments ||
			state.commentBody+int64(len(body)) > MaxCommentBodyTotal {
			return 0, ErrInvalidReviewTransition
		}
		id = CommentID(state.nextComment)
		comment := Comment{
			id: id, target: state.draft.target, body: body,
		}
		state.comments = append(state.comments, comment)
		state.commentBody += int64(len(body))
		state.nextComment++
		state.applyCommentAnchorTransition(
			commentAnchorMembership{},
			state.commentAnchorMembership(&state.comments[len(state.comments)-1]),
		)
	}
	state.draft = nil
	return id, cancellationError(cancelled)
}

func (intent deleteCommentIntent) apply(
	cancelled func() error,
	state *commentState,
) (CommentID, error) {
	if err := cancellationError(cancelled); err != nil {
		return 0, err
	}
	index := commentIndex(state.comments, intent.id)
	if index < 0 {
		return 0, ErrInvalidReviewTransition
	}
	if state.pending != nil {
		if binding, bound := state.pending.bindings[intent.id]; bound {
			if binding.kind == pendingBindingBody && !state.pending.bodyUpdate ||
				binding.kind == pendingBindingComment &&
					!binding.capabilities.Has(PendingCapabilityDelete) {
				return 0, ErrInvalidReviewTransition
			}
			tombstonePendingBindingOwned(state, intent.id, binding)
			return 0, nil
		}
	}
	removeCommentOwned(state, intent.id)
	return 0, nil
}

func (intent resolvePendingConflictIntent) apply(
	cancelled func() error,
	state *commentState,
) (CommentID, error) {
	if state.pending == nil || state.draft != nil {
		return 0, ErrInvalidReviewTransition
	}
	binding, exists := state.pending.bindings[intent.id]
	if !exists || binding.conflict == nil {
		return 0, ErrInvalidReviewTransition
	}
	conflict := *binding.conflict
	chooseDeletion := intent.resolution == PendingConflictLocal && conflict.localDeleted ||
		intent.resolution == PendingConflictBase && conflict.base.body == ""
	if chooseDeletion {
		if conflict.remote.body == "" {
			removeCommentOwned(state, intent.id)
			removePendingBindingOwned(state, intent.id, binding)
			return 0, nil
		}
		binding.conflict = nil
		tombstonePendingBindingOwned(state, intent.id, binding)
		return 0, nil
	}
	selected := conflict.local
	switch intent.resolution {
	case PendingConflictLocal:
	case PendingConflictRemote:
		selected = conflict.remote
	case PendingConflictBase:
		selected = conflict.base
	default:
		return 0, ErrInvalidReviewTransition
	}
	if conflict.remote.body == "" && intent.resolution != PendingConflictRemote {
		removePendingBindingOwned(state, intent.id, binding)
		if err := replaceCommentContentOwned(cancelled, state, intent.id, selected); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if selected.body == "" {
		removeCommentOwned(state, intent.id)
		removePendingBindingOwned(state, intent.id, binding)
		return 0, nil
	}
	if err := replaceCommentContentOwned(cancelled, state, intent.id, selected); err != nil {
		return 0, err
	}
	binding.conflict = nil
	restorePendingBindingOwned(state, intent.id, binding)
	return 0, nil
}

func commentIndex(comments []Comment, id CommentID) int {
	index := sort.Search(len(comments), func(index int) bool { return comments[index].id >= id })
	if index == len(comments) || comments[index].id != id {
		return -1
	}
	return index
}

func commentBodyCancellable(cancelled func() error, value string) (CommentBody, error) {
	if value == "" {
		return "", ErrInvalidComment
	}
	nonSpace, err := scanCommentText(cancelled, value)
	if err != nil {
		if err == errInvalidCommentText { //nolint:errorlint // Wrapped cancellation must remain unchanged.
			return "", ErrInvalidComment
		}
		return "", err
	}
	if !nonSpace {
		return "", ErrInvalidComment
	}
	return CommentBody(strings.Clone(value)), nil
}

func (state *commentState) applyCommentAnchorTransition(
	before, after commentAnchorMembership,
) {
	if before.present == after.present && (!before.present || before.anchor == after.anchor) {
		return
	}
	if before.present {
		if state.commentAnchors[before.anchor] <= 1 {
			delete(state.commentAnchors, before.anchor)
		} else {
			state.commentAnchors[before.anchor]--
		}
	}
	if after.present {
		if state.commentAnchors == nil {
			state.commentAnchors = make(commentAnchorCounts)
		}
		state.commentAnchors[after.anchor]++
	}
}

func removeCommentOwned(state *commentState, id CommentID) {
	index := commentIndex(state.comments, id)
	if index < 0 {
		return
	}
	before := state.commentAnchorMembership(&state.comments[index])
	removed := state.comments[index]
	state.commentBody -= int64(len(removed.body))
	copy(state.comments[index:], state.comments[index+1:])
	state.comments[len(state.comments)-1] = Comment{}
	state.comments = state.comments[:len(state.comments)-1]
	state.applyCommentAnchorTransition(before, commentAnchorMembership{})
}

func restorePendingBindingOwned(state *commentState, id CommentID, binding pendingBinding) {
	binding.deleted = false
	storePendingBindingOwned(state, id, binding)
}

func tombstonePendingBindingOwned(state *commentState, id CommentID, binding pendingBinding) {
	binding.deleted = true
	storePendingBindingOwned(state, id, binding)
}

func storePendingBindingOwned(state *commentState, id CommentID, binding pendingBinding) {
	before := state.commentAnchorMembershipByID(id)
	state.pending.bindings[id] = binding
	after := state.commentAnchorMembershipByID(id)
	state.applyCommentAnchorTransition(before, after)
}

func removePendingBindingOwned(state *commentState, id CommentID, binding pendingBinding) {
	before := state.commentAnchorMembershipByID(id)
	delete(state.pending.bindings, id)
	delete(state.pending.byNode, binding.remoteNode)
	if binding.kind == pendingBindingBody {
		state.pending.bodyID = nil
	}
	if len(state.pending.bindings) == 0 {
		state.pending = nil
	}
	after := state.commentAnchorMembershipByID(id)
	state.applyCommentAnchorTransition(before, after)
}

func replaceCommentContentOwned(
	cancelled func() error,
	state *commentState,
	id CommentID,
	content commentContent,
) error {
	validated, err := commentBodyCancellable(cancelled, content.body)
	if err != nil {
		if errors.Is(err, ErrInvalidComment) {
			return ErrInvalidReviewTransition
		}
		return err
	}
	index := commentIndex(state.comments, id)
	if index < 0 || state.commentBody-int64(len(state.comments[index].body))+
		int64(len(validated)) > MaxCommentBodyTotal {
		return ErrInvalidReviewTransition
	}
	state.commentBody -= int64(len(state.comments[index].body))
	state.comments[index].body = validated
	state.commentBody += int64(len(validated))
	return nil
}
