package review

import "sort"

const (
	commentMetadataBytes int64 = 128
	commentAnchorBytes   int64 = 32
	draftMetadataBytes   int64 = 128
)

// commentSource is an immutable, revision-bound view of local comments. Review
// transitions replace commentState rather than mutating its retained slices.
type commentSource struct {
	comments      []Comment
	kinds         CommentKinds
	policies      CommentPolicies
	retainedBytes int64
	revision      uint64
}

func (state *commentState) source() commentSource {
	return commentSource{
		comments:      state.comments,
		kinds:         state.commentKinds,
		policies:      state.commentPolicies,
		retainedBytes: state.commentBytes + state.commentKinds.retained(),
		revision:      state.revision,
	}
}

func (source commentSource) ready() bool {
	return source.revision != 0 && source.kinds.valid() && source.policies.valid()
}

func (state *commentState) refresh() {
	if err := state.refreshCancellable(nil); err != nil {
		panic("nil comment-state cancellation returned an error")
	}
}

func (state *commentState) refreshCancellable(cancelled func() error) error {
	if state.revision == 0 {
		state.revision = 1
	}
	commentBytes, retained, err := state.calculateRetainedBytesCancellable(cancelled)
	if err != nil {
		return err
	}
	dirty, conflicts, err := state.calculatePendingCountsCancellable(cancelled)
	if err != nil {
		return err
	}
	state.commentBytes, state.retainedBytes = commentBytes, retained
	state.pendingDirty, state.pendingConflict = dirty, conflicts
	return nil
}

func (state *commentState) calculateRetainedBytesCancellable(
	cancelled func() error,
) (int64, int64, error) {
	pendingBytes, err := pendingStateRetainedBytesCancellable(cancelled, state.pending)
	if err != nil {
		return 0, 0, err
	}
	commentBytes := int64(cap(state.comments))*commentMetadataBytes +
		int64(len(state.commentAnchors))*commentAnchorBytes
	for index, comment := range state.comments {
		if index%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return 0, 0, err
			}
		}
		commentBytes += int64(len(comment.body))
		if path, ok := comment.target.AnchoredPath(); ok {
			commentBytes += int64(len(path.raw))
		}
	}
	return commentBytes,
		commentBytes + pendingBytes + draftRetainedBytes(state.draft) +
			state.commentKinds.retained(),
		nil
}

func draftRetainedBytes(draft *Draft) int64 {
	if draft == nil {
		return 0
	}
	retained := draftMetadataBytes + int64(len(draft.body))
	if path, ok := draft.target.AnchoredPath(); ok {
		retained += int64(len(path.raw))
	}
	return retained
}

func (state *commentState) calculatePendingCountsCancellable(
	cancelled func() error,
) (uint32, uint32, error) {
	if state.pending == nil {
		return uint32FromInt(len(state.comments)), 0, nil
	}
	var dirty, conflicts uint32
	inspected := 0
	for id, binding := range state.pending.bindings {
		if inspected%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return 0, 0, err
			}
		}
		inspected++
		if binding.conflict != nil {
			dirty++
			conflicts++
			continue
		}
		if binding.deleted {
			dirty++
			continue
		}
		index := sort.Search(len(state.comments), func(index int) bool {
			return state.comments[index].id >= id
		})
		if index == len(state.comments) || state.comments[index].id != id ||
			commentContentFrom(state.comments[index]) != binding.baseline {
			dirty++
		}
	}
	for index, comment := range state.comments {
		if index%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return 0, 0, err
			}
		}
		if _, bound := state.pending.bindings[comment.id]; !bound {
			dirty++
		}
	}
	return dirty, conflicts, nil
}

func pendingStateRetainedBytes(state *pendingReviewState) int64 {
	retained, err := pendingStateRetainedBytesCancellable(nil, state)
	if err != nil {
		panic("nil pending-state cancellation returned an error")
	}
	return retained
}

func pendingStateRetainedBytesCancellable(
	cancelled func() error,
	state *pendingReviewState,
) (int64, error) {
	if state == nil {
		return 0, nil
	}
	retained := int64(128 + len(state.bindings)*256 + len(state.byNode)*48 +
		len(state.nodeID) + len(state.commit) + len(state.bodyMarker) + len(state.bodyBase))
	inspected := 0
	for _, binding := range state.bindings {
		if inspected%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return 0, err
			}
		}
		inspected++
		retained += int64(
			len(binding.remoteNode) + len(binding.baseline.body) + len(binding.wireBaseline) +
				len(binding.marker) +
				len(binding.target.path.raw) + len(binding.target.commit) +
				len(binding.target.originalCommit) + len(binding.target.replyTo),
		)
		if binding.conflict != nil {
			retained += int64(
				len(binding.conflict.base.body) + len(binding.conflict.local.body) +
					len(binding.conflict.remote.body) + 128,
			)
		}
	}
	return retained, nil
}
