package review

import "fmt"

const (
	pendingRebaseSourceMetadataBytes   int64 = 304
	preparedPendingRebaseMetadataBytes int64 = 280
)

// PendingRebaseSource is the narrow immutable state needed to merge one fresh
// pending-review capture. It excludes files, patch handles, and presentation state.
type PendingRebaseSource struct {
	remoteCommentAnchors map[commentAnchor]struct{}
	remoteThreadDetail   OperationDetail
	pendingReviewDetail  OperationDetail
	remoteThreads        []RemoteThread
	publicationDecisions []PublicationCommentDecision
	paths                remotePathProjection
	state                commentState
	workspace            WorkspaceRevision
	source               SourceID
	baseRetainedBytes    int64
	remoteRetainedBytes  int64
	retainedBytes        int64
	commentRevision      uint64
	remoteThreadStatus   RemoteThreadStatus
	pendingReviewStatus  PendingReviewStatus
	installDraft         bool
}

// PendingRebaseSource captures an O(1), revision-fenced pending-rebase input.
func (review Review) PendingRebaseSource() PendingRebaseSource {
	snapshot := review.snapshot
	base := snapshot.retainedBytes - snapshot.remoteRetainedBytes - snapshot.pendingRetainedBytes
	retained := review.retainedBytes + snapshot.remoteRetainedBytes +
		snapshot.pendingReviewDetail.Bytes() + snapshot.pathProjection.retainedBytes() +
		pendingRebaseSourceMetadataBytes
	return PendingRebaseSource{
		state:                review.commentState,
		paths:                snapshot.pathProjection,
		remoteCommentAnchors: snapshot.remoteCommentAnchors,
		remoteThreads:        snapshot.remoteThreads,
		remoteThreadDetail:   snapshot.remoteThreadDetail,
		pendingReviewDetail:  snapshot.pendingReviewDetail,
		workspace:            review.workspace,
		source:               snapshot.SourceID(),
		baseRetainedBytes:    base,
		remoteRetainedBytes:  snapshot.remoteRetainedBytes,
		retainedBytes:        retained,
		commentRevision:      review.revision,
		remoteThreadStatus:   snapshot.remoteThreadStatus,
		pendingReviewStatus:  snapshot.pendingReviewStatus,
	}
}

// PublicationRebaseSource captures the pending projection required by the
// currently checked publication. A draft publication with no locally captured
// pending state either created or reused one remotely, so its included local
// comments are removed before the fresh GitHub draft is imported.
func (review Review) PublicationRebaseSource() PendingRebaseSource {
	source := review.PendingRebaseSource()
	checked := review.publication.checked
	if checked != nil && source.state.pending == nil &&
		checked.prepared.options.event == PublicationEventDraft {
		source.installDraft = true
		source.publicationDecisions = checked.prepared.decisions
	}
	return source
}

// Ready reports whether the source can prepare a pending rebase.
func (source PendingRebaseSource) Ready() bool {
	return source.workspace != 0 && source.source != 0 && source.commentRevision != 0 &&
		(source.state.pending != nil || source.installDraft) &&
		(source.state.pending == nil || !source.installDraft) &&
		source.state.activeMutation == 0 &&
		source.state.mutationKind == 0 &&
		source.retainedBytes >= pendingRebaseSourceMetadataBytes &&
		source.baseRetainedBytes >= source.paths.metadataBytes
}

// Workspace returns the captured workspace fence.
func (source PendingRebaseSource) Workspace() WorkspaceRevision { return source.workspace }

// Source returns the captured source fence.
func (source PendingRebaseSource) Source() SourceID { return source.source }

// CommentRevision returns the captured comment-state fence.
func (source PendingRebaseSource) CommentRevision() uint64 { return source.commentRevision }

// RetainedBytes returns immutable source ownership prolonged by queued work.
func (source PendingRebaseSource) RetainedBytes() int64 {
	if !source.Ready() {
		return 0
	}
	return source.retainedBytes
}

// ConstructionBytes returns a conservative source, result, and scratch peak.
func (source PendingRebaseSource) ConstructionBytes(remote PendingReview) int64 {
	construction, err := source.constructionBytesCancellable(nil, remote)
	if err != nil {
		panic("nil pending-rebase cancellation returned an error")
	}
	return construction
}

func (source PendingRebaseSource) constructionBytesCancellable(
	cancelled func() error,
	remote PendingReview,
) (int64, error) {
	if !source.Ready() {
		return 0, nil
	}
	if source.state.pending != nil &&
		(remote.nodeID != source.state.pending.nodeID ||
			remote.databaseID != source.state.pending.databaseID) {
		return 0, nil
	}
	remoteRetained, err := remote.retainedBytesCancellable(cancelled)
	if err != nil {
		return 0, err
	}
	return 3*source.retainedBytes + 2*remoteRetained + 4_096, nil
}

type pendingSnapshotDelta struct {
	pendingReview        *PendingReview
	remoteCommentAnchors map[commentAnchor]struct{}
	remoteThreadDetail   OperationDetail
	pendingReviewDetail  OperationDetail
	remoteThreads        []RemoteThread
	remoteRetainedBytes  int64
	pendingRetainedBytes int64
	remoteThreadStatus   RemoteThreadStatus
	pendingReviewStatus  PendingReviewStatus
}

func pendingSnapshotDeltaFrom(snapshot Snapshot) pendingSnapshotDelta {
	return pendingSnapshotDelta{
		pendingReview:        snapshot.pendingReview,
		remoteCommentAnchors: snapshot.remoteCommentAnchors,
		remoteThreads:        snapshot.remoteThreads,
		remoteThreadDetail:   snapshot.remoteThreadDetail,
		pendingReviewDetail:  snapshot.pendingReviewDetail,
		remoteRetainedBytes:  snapshot.remoteRetainedBytes,
		pendingRetainedBytes: snapshot.pendingRetainedBytes,
		remoteThreadStatus:   snapshot.remoteThreadStatus,
		pendingReviewStatus:  snapshot.pendingReviewStatus,
	}
}

func (delta pendingSnapshotDelta) install(snapshot Snapshot) Snapshot {
	next := snapshot
	next.pendingReview = delta.pendingReview
	next.remoteCommentAnchors = delta.remoteCommentAnchors
	next.remoteThreads = delta.remoteThreads
	next.remoteThreadDetail = delta.remoteThreadDetail
	next.pendingReviewDetail = delta.pendingReviewDetail
	next.remoteRetainedBytes = delta.remoteRetainedBytes
	next.pendingRetainedBytes = delta.pendingRetainedBytes
	next.remoteThreadStatus = delta.remoteThreadStatus
	next.pendingReviewStatus = delta.pendingReviewStatus
	next.retainedBytes = snapshot.retainedBytes - snapshot.remoteRetainedBytes -
		snapshot.pendingRetainedBytes + delta.remoteRetainedBytes + delta.pendingRetainedBytes
	return next
}

// PreparedPendingRebase is a complete pending snapshot delta plus replacement
// comment state, fenced to the source from which it was prepared.
type PreparedPendingRebase struct {
	delta           pendingSnapshotDelta
	state           commentState
	workspace       WorkspaceRevision
	source          SourceID
	commentRevision uint64
	retainedBytes   int64
	valid           bool
}

// RetainedBytes returns complete prepared delta ownership.
func (prepared PreparedPendingRebase) RetainedBytes() int64 {
	if !prepared.valid {
		return 0
	}
	return prepared.retainedBytes
}

// PreparePendingRebase validates and merges one fresh remote capture.
func PreparePendingRebase(
	source PendingRebaseSource,
	remote PendingReview,
) (PreparedPendingRebase, error) {
	return PreparePendingRebaseCancellable(nil, source, remote)
}

// PreparePendingRebaseCancellable validates and merges one fresh remote capture.
func PreparePendingRebaseCancellable(
	cancelled func() error,
	source PendingRebaseSource,
	remote PendingReview,
) (PreparedPendingRebase, error) {
	if err := cancellationError(cancelled); err != nil {
		return PreparedPendingRebase{}, err
	}
	construction, err := source.constructionBytesCancellable(cancelled, remote)
	if err != nil {
		return PreparedPendingRebase{}, err
	}
	if construction == 0 {
		return PreparedPendingRebase{}, ErrInvalidPendingReview
	}
	owned := remote
	comments, err := clonePendingCommentsCancellable(cancelled, remote.comments)
	if err != nil {
		return PreparedPendingRebase{}, err
	}
	owned.comments = comments
	temporary := Snapshot{
		remoteCommentAnchors: source.remoteCommentAnchors,
		remoteThreadDetail:   source.remoteThreadDetail,
		pendingReviewDetail:  source.pendingReviewDetail,
		remoteThreads:        source.remoteThreads,
		pathProjection:       source.paths,
		sourceID:             source.source,
		retainedBytes:        source.baseRetainedBytes + source.remoteRetainedBytes,
		remoteRetainedBytes:  source.remoteRetainedBytes,
		remoteThreadStatus:   source.remoteThreadStatus,
		pendingReviewStatus:  source.pendingReviewStatus,
	}
	updated, err := temporary.takePendingReviewCancellable(cancelled, owned)
	if err != nil {
		return PreparedPendingRebase{}, err
	}
	var state commentState
	if source.installDraft {
		state, err = prepareInstalledDraftCommentStateCancellable(
			cancelled,
			source.state,
			source.publicationDecisions,
			updated,
		)
	} else {
		state, err = preparePendingCommentStateCancellable(
			cancelled,
			source.state.pending,
			source.state,
			source.paths,
			owned,
		)
	}
	if err != nil {
		return PreparedPendingRebase{}, err
	}
	retained := state.retainedBytes + updated.remoteRetainedBytes +
		updated.pendingRetainedBytes + preparedPendingRebaseMetadataBytes
	prepared := PreparedPendingRebase{
		state:           state,
		delta:           pendingSnapshotDeltaFrom(updated),
		workspace:       source.workspace,
		source:          source.source,
		commentRevision: source.commentRevision,
		retainedBytes:   retained,
		valid:           true,
	}
	if err := cancellationError(cancelled); err != nil {
		return PreparedPendingRebase{}, err
	}
	if prepared.RetainedBytes() > construction {
		return PreparedPendingRebase{}, fmt.Errorf(
			"%w: pending rebase exceeds its construction bound",
			ErrInvalidPendingReview,
		)
	}
	return prepared, nil
}

func prepareInstalledDraftCommentStateCancellable(
	cancelled func() error,
	state commentState,
	decisions []PublicationCommentDecision,
	snapshot Snapshot,
) (commentState, error) {
	if state.pending != nil || len(decisions) != len(state.comments) ||
		snapshot.pendingReviewStatus != PendingReviewReady {
		return commentState{}, ErrInvalidPendingReview
	}
	next, err := cloneCommentStateCancellable(cancelled, state, nil)
	if err != nil {
		return commentState{}, err
	}
	retained := next.comments[:0]
	next.commentBody = 0
	for index, comment := range next.comments {
		if index%256 == 0 {
			if cancellationErr := cancellationError(cancelled); cancellationErr != nil {
				return commentState{}, cancellationErr
			}
		}
		decision := decisions[index]
		if decision.id != comment.id {
			return commentState{}, ErrInvalidPendingReview
		}
		switch decision.decision {
		case PublicationIncludedInline, PublicationIncludedSummary:
			continue
		case PublicationExcludedManual, PublicationExcludedStale,
			PublicationExcludedUnmappable, PublicationExcludedInvalid:
		default:
			return commentState{}, ErrInvalidPendingReview
		}
		retained = append(retained, comment)
		next.commentBody += int64(len(comment.body))
	}
	clear(next.comments[len(retained):])
	next.comments = retained
	next.pending = nil
	current, err := importPendingReviewCancellable(Review{
		snapshot:     snapshot,
		commentState: next,
	}, cancelled)
	if err != nil {
		return commentState{}, err
	}
	result := current.commentState
	result.revision = state.revision + 1
	result.activeMutation = 0
	result.mutationKind = 0
	if err := result.rebuildCommentAnchorsCancellable(cancelled); err != nil {
		return commentState{}, err
	}
	if err := result.refreshCancellable(cancelled); err != nil {
		return commentState{}, err
	}
	return result, nil
}

// ApplyPendingRebase atomically installs a prepared snapshot delta and comment state.
func (review Review) ApplyPendingRebase(
	prepared PreparedPendingRebase,
) (Review, error) {
	if !prepared.valid || prepared.workspace != review.workspace ||
		prepared.source != review.snapshot.SourceID() ||
		prepared.commentRevision != review.revision ||
		review.activeMutation != 0 || review.mutationKind != 0 ||
		prepared.state.revision != prepared.commentRevision+1 ||
		prepared.state.activeMutation != 0 || prepared.state.mutationKind != 0 {
		return review, ErrInvalidPendingReview
	}
	next := review
	next.state++
	next.snapshot = prepared.delta.install(review.snapshot)
	next.commentState = prepared.state
	next.publication.pendingRefresh = false
	return next, nil
}

func preparePendingCommentStateCancellable(
	cancelled func() error,
	original *pendingReviewState,
	state commentState,
	paths remotePathProjection,
	remote PendingReview,
) (commentState, error) {
	if original == nil || remote.nodeID != original.nodeID ||
		remote.databaseID != original.databaseID {
		return commentState{}, ErrInvalidPendingReview
	}
	next, err := cloneCommentStateCancellable(cancelled, state, nil)
	if err != nil {
		return commentState{}, err
	}
	commentIndexes, err := indexPendingCommentsCancellable(cancelled, next.comments)
	if err != nil {
		return commentState{}, err
	}
	preparation := replacementPreparation{
		cancelled:      cancelled,
		original:       Review{commentState: commentState{pending: original}},
		next:           Review{commentState: next},
		commentIndexes: commentIndexes,
		pendingFiles:   paths,
	}
	if mergeErr := preparation.mergeReadyPendingReview(remote); mergeErr != nil {
		return commentState{}, mergeErr
	}
	result := preparation.next.commentState
	result.revision = state.revision + 1
	result.activeMutation = 0
	result.mutationKind = 0
	if err := result.rebuildCommentAnchorsCancellable(cancelled); err != nil {
		return commentState{}, err
	}
	if err := result.refreshCancellable(cancelled); err != nil {
		return commentState{}, err
	}
	return result, nil
}
