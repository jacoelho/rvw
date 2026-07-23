package review

type pendingRefreshMode uint8

const (
	pendingRefreshNone pendingRefreshMode = iota + 1
	pendingRefreshInstall
	pendingRefreshReloadRequired
)

// preparedPendingRefresh is the closed result of optional pending projection
// preparation. Exactly one mode is representable by constructors: no pending
// refresh, an installable delta, or an unchanged projection requiring reload.
type preparedPendingRefresh struct {
	rebase          PreparedPendingRebase
	commentRevision uint64
	mode            pendingRefreshMode
}

func noPendingRefresh() preparedPendingRefresh {
	return preparedPendingRefresh{mode: pendingRefreshNone}
}

func preparePendingRefreshCancellable(
	cancelled func() error,
	source PendingRebaseSource,
	workspace WorkspaceRevision,
	sourceID SourceID,
	acquisition PendingRefreshAcquisition,
	pending *PendingReview,
) (preparedPendingRefresh, error) {
	switch acquisition {
	case PendingRefreshNotRequested:
		if pending != nil {
			return preparedPendingRefresh{}, ErrInvalidPublication
		}
		return noPendingRefresh(), nil
	case PendingRefreshUnavailable:
		if pending != nil || !pendingRebaseSourceMatchesRequest(source, workspace, sourceID) {
			return preparedPendingRefresh{}, ErrInvalidPublication
		}
		return preparedPendingRefresh{
			commentRevision: source.CommentRevision(),
			mode:            pendingRefreshReloadRequired,
		}, nil
	case PendingRefreshAcquired:
		if pending == nil {
			return preparedPendingRefresh{}, ErrInvalidPublication
		}
	default:
		return preparedPendingRefresh{}, ErrInvalidPublication
	}
	rebase, err := PreparePendingRebaseCancellable(cancelled, source, *pending)
	if err != nil {
		if cancellationErr := cancellationError(cancelled); cancellationErr != nil {
			return preparedPendingRefresh{}, cancellationErr
		}
		if source.CommentRevision() == 0 {
			return preparedPendingRefresh{}, ErrInvalidPublication
		}
		return preparedPendingRefresh{
			commentRevision: source.CommentRevision(),
			mode:            pendingRefreshReloadRequired,
		}, nil
	}
	return preparedPendingRefresh{
		rebase:          rebase,
		commentRevision: source.CommentRevision(),
		mode:            pendingRefreshInstall,
	}, nil
}

func preparePendingRefreshFallback(
	source PendingRebaseSource,
	workspace WorkspaceRevision,
	sourceID SourceID,
	acquisition PendingRefreshAcquisition,
	pending *PendingReview,
) (preparedPendingRefresh, error) {
	switch acquisition {
	case PendingRefreshNotRequested:
		if pending != nil {
			return preparedPendingRefresh{}, ErrInvalidPublication
		}
		return noPendingRefresh(), nil
	case PendingRefreshUnavailable:
		if pending != nil || !pendingRebaseSourceMatchesRequest(source, workspace, sourceID) {
			return preparedPendingRefresh{}, ErrInvalidPublication
		}
		return preparedPendingRefresh{
			commentRevision: source.CommentRevision(),
			mode:            pendingRefreshReloadRequired,
		}, nil
	case PendingRefreshAcquired:
		if pending == nil {
			return preparedPendingRefresh{}, ErrInvalidPublication
		}
	default:
		return preparedPendingRefresh{}, ErrInvalidPublication
	}
	if !pendingRebaseSourceMatchesResult(source, workspace, sourceID, *pending) {
		return preparedPendingRefresh{}, ErrInvalidPublication
	}
	return preparedPendingRefresh{
		commentRevision: source.CommentRevision(),
		mode:            pendingRefreshReloadRequired,
	}, nil
}

func (refresh preparedPendingRefresh) valid() bool {
	switch refresh.mode {
	case pendingRefreshNone:
		return !refresh.rebase.valid && refresh.commentRevision == 0
	case pendingRefreshInstall:
		return refresh.rebase.valid && refresh.commentRevision != 0 &&
			refresh.rebase.commentRevision == refresh.commentRevision
	case pendingRefreshReloadRequired:
		return !refresh.rebase.valid && refresh.commentRevision != 0
	default:
		return false
	}
}

func (refresh preparedPendingRefresh) retainedBytes() int64 {
	if !refresh.valid() || refresh.mode != pendingRefreshInstall {
		return 0
	}
	return refresh.rebase.RetainedBytes()
}

func (refresh preparedPendingRefresh) reloadRequired() bool {
	return refresh.valid() && refresh.mode == pendingRefreshReloadRequired
}

func (refresh preparedPendingRefresh) install(review Review) (Review, error) {
	if !refresh.valid() {
		return review, ErrInvalidPublication
	}
	switch refresh.mode {
	case pendingRefreshNone:
	case pendingRefreshInstall:
		return review.ApplyPendingRebase(refresh.rebase)
	case pendingRefreshReloadRequired:
		if review.revision != refresh.commentRevision ||
			review.activeMutation != 0 || review.mutationKind != 0 {
			return review, ErrInvalidPublication
		}
	}
	next := review
	next.state++
	return next, nil
}
