package review

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxRemoteThreads defines the supported remote threads limit.
const (
	MaxRemoteThreads                = 100_000
	MaxRemoteComments               = 100_000
	MaxRemoteThreadBytes      int64 = 64 << 20
	remoteThreadStorageBytes        = 96
	remoteThreadPositionBytes       = 24
	remoteCommentStorageBytes       = 96
	remoteAnchorStorageBytes        = 32
)

// ErrInvalidRemoteThread identifies an invalid remote thread failure.
var ErrInvalidRemoteThread = errors.New("invalid remote review thread")

// RemoteThreadStatus describes remote thread status.
type RemoteThreadStatus uint8

// RemoteThreadsNotRequested identifies a supported remote threads not requested value.
const (
	RemoteThreadsNotRequested RemoteThreadStatus = iota + 1
	RemoteThreadsReady
	RemoteThreadsUnavailable
	RemoteThreadsLimited
)

// String returns the receiver's string value.
func (status RemoteThreadStatus) String() string {
	switch status {
	case RemoteThreadsNotRequested:
		return "not-requested"
	case RemoteThreadsReady:
		return "ready"
	case RemoteThreadsUnavailable:
		return "unavailable"
	case RemoteThreadsLimited:
		return limitedValue
	default:
		return invalidValue
	}
}

// RemoteThreadSummary describes remote thread summary.
type RemoteThreadSummary struct {
	Detail OperationDetail
	Count  int
	Status RemoteThreadStatus
}

// RemoteComment represents remote comment.
type RemoteComment struct {
	id        string
	author    string
	body      string
	createdAt string
	url       string
}

// RemoteCommentDescriptor describes remote comment descriptor.
type RemoteCommentDescriptor struct {
	ID        string
	Author    string
	Body      string
	CreatedAt string
	URL       string
}

// NewRemoteComment constructs and validates remote comment.
func NewRemoteComment(descriptor RemoteCommentDescriptor) (RemoteComment, error) {
	return newRemoteComment(descriptor, cloneRemoteThreads)
}

// TakeRemoteComment consumes validated immutable descriptor strings.
func TakeRemoteComment(descriptor RemoteCommentDescriptor) (RemoteComment, error) {
	return newRemoteComment(descriptor, takeRemoteThreads)
}

func newRemoteComment(
	descriptor RemoteCommentDescriptor,
	ownership remoteThreadOwnership,
) (RemoteComment, error) {
	if descriptor.ID == "" || int64(len(descriptor.ID)) > MaxPathBytes ||
		int64(len(descriptor.Author)) > MaxPathBytes ||
		int64(len(descriptor.Body)) > MaxCommentBodyBytes ||
		int64(len(descriptor.CreatedAt)) > MaxPathBytes ||
		len(descriptor.URL) > 16<<10 ||
		!utf8.ValidString(descriptor.ID) ||
		!utf8.ValidString(descriptor.Author) ||
		!utf8.ValidString(descriptor.Body) ||
		!utf8.ValidString(descriptor.CreatedAt) ||
		!utf8.ValidString(descriptor.URL) {
		return RemoteComment{}, ErrInvalidRemoteThread
	}
	if ownership == cloneRemoteThreads {
		descriptor.ID = strings.Clone(descriptor.ID)
		descriptor.Author = strings.Clone(descriptor.Author)
		descriptor.Body = strings.Clone(descriptor.Body)
		descriptor.CreatedAt = strings.Clone(descriptor.CreatedAt)
		descriptor.URL = strings.Clone(descriptor.URL)
	} else if ownership != takeRemoteThreads {
		return RemoteComment{}, ErrInvalidRemoteThread
	}
	return RemoteComment{
		id: descriptor.ID, author: descriptor.Author, body: descriptor.Body,
		createdAt: descriptor.CreatedAt, url: descriptor.URL,
	}, nil
}

// ID returns the receiver's id value.
func (comment RemoteComment) ID() string { return comment.id }

// Author returns the receiver's author value.
func (comment RemoteComment) Author() string { return comment.author }

// Body returns the receiver's body value.
func (comment RemoteComment) Body() string { return comment.body }

// CreatedAt returns the receiver's created at value.
func (comment RemoteComment) CreatedAt() string { return comment.createdAt }

// URL returns the receiver's url value.
func (comment RemoteComment) URL() string { return comment.url }

func (comment RemoteComment) retainedBytes() int64 {
	return 96 + int64(
		len(comment.id)+
			len(comment.author)+
			len(comment.body)+
			len(comment.createdAt)+
			len(comment.url),
	)
}

// RemoteThread represents remote thread.
type RemoteThread struct {
	line     *LineNumber
	id       string
	path     Path
	comments []RemoteComment
	file     FileID
	resolved bool
	outdated bool
	side     LineSide
	mapping  RemoteMappingStatus
}

// RemoteMappingStatus describes remote mapping status.
type RemoteMappingStatus uint8

// RemoteMappingMissing identifies a supported remote mapping missing value.
const (
	RemoteMappingMissing RemoteMappingStatus = iota + 1
	RemoteMappingMapped
	RemoteMappingAmbiguous
)

// RemoteThreadDescriptor describes remote thread descriptor.
type RemoteThreadDescriptor struct {
	Line     *LineNumber
	ID       string
	Path     Path
	Comments []RemoteComment
	Resolved bool
	Outdated bool
	Side     LineSide
}

// NewRemoteThread constructs and validates remote thread.
func NewRemoteThread(descriptor RemoteThreadDescriptor) (RemoteThread, error) {
	return newRemoteThreadCancellable(nil, descriptor, cloneRemoteThreads)
}

// TakeRemoteThread consumes the descriptor's immutable path, strings, and
// comments after complete validation succeeds.
func TakeRemoteThread(descriptor RemoteThreadDescriptor) (RemoteThread, error) {
	return TakeRemoteThreadCancellable(nil, descriptor)
}

// TakeRemoteThreadCancellable consumes the descriptor's immutable path,
// strings, and comments after complete validation succeeds.
func TakeRemoteThreadCancellable(
	cancelled func() error,
	descriptor RemoteThreadDescriptor,
) (RemoteThread, error) {
	return newRemoteThreadCancellable(cancelled, descriptor, takeRemoteThreads)
}

func newRemoteThreadCancellable(
	cancelled func() error,
	descriptor RemoteThreadDescriptor,
	ownership remoteThreadOwnership,
) (RemoteThread, error) {
	if descriptor.ID == "" || int64(len(descriptor.ID)) > MaxPathBytes ||
		!utf8.ValidString(descriptor.ID) ||
		descriptor.Side != LineOld && descriptor.Side != LineNew ||
		len(descriptor.Comments) > MaxRemoteComments {
		return RemoteThread{}, ErrInvalidRemoteThread
	}
	if err := cancellationError(cancelled); err != nil {
		return RemoteThread{}, err
	}
	pathValue := descriptor.Path.raw
	if ownership == cloneRemoteThreads {
		pathValue = strings.Clone(pathValue)
		descriptor.ID = strings.Clone(descriptor.ID)
	} else if ownership != takeRemoteThreads {
		return RemoteThread{}, ErrInvalidRemoteThread
	}
	path, err := NewPathString(pathValue)
	if err != nil || path.raw == "" {
		return RemoteThread{}, ErrInvalidRemoteThread
	}
	if descriptor.Line != nil && *descriptor.Line == 0 {
		return RemoteThread{}, ErrInvalidRemoteThread
	}
	comments := descriptor.Comments
	if ownership == cloneRemoteThreads {
		comments = slices.Clone(comments)
	}
	seen := make(map[string]struct{}, len(comments))
	for _, comment := range comments {
		if err := cancellationError(cancelled); err != nil {
			return RemoteThread{}, err
		}
		if comment.id == "" {
			return RemoteThread{}, ErrInvalidRemoteThread
		}
		if _, exists := seen[comment.id]; exists {
			return RemoteThread{}, fmt.Errorf(
				"%w: duplicate remote comment %q",
				ErrInvalidRemoteThread,
				comment.id,
			)
		}
		seen[comment.id] = struct{}{}
	}
	if err := cancellationError(cancelled); err != nil {
		return RemoteThread{}, err
	}
	var line *LineNumber
	if descriptor.Line != nil {
		line = new(*descriptor.Line)
	}
	return RemoteThread{
		id:       descriptor.ID,
		resolved: descriptor.Resolved,
		outdated: descriptor.Outdated,
		path:     path,
		line:     line,
		side:     descriptor.Side,
		comments: comments,
	}, nil
}

// ID returns the receiver's id value.
func (thread RemoteThread) ID() string { return thread.id }

// Resolved reports the receiver's resolved state.
func (thread RemoteThread) Resolved() bool { return thread.resolved }

// Outdated reports the receiver's outdated state.
func (thread RemoteThread) Outdated() bool { return thread.outdated }

// Path returns the receiver's path value.
func (thread RemoteThread) Path() Path { return thread.path }

// Side returns the receiver's side value.
func (thread RemoteThread) Side() LineSide { return thread.side }

// CommentCount returns the receiver's comment count value.
func (thread RemoteThread) CommentCount() int { return len(thread.comments) }

// FileMapping returns the receiver's file mapping value.
func (thread RemoteThread) FileMapping() (FileID, RemoteMappingStatus) {
	if thread.mapping == 0 {
		return 0, RemoteMappingMissing
	}
	return thread.file, thread.mapping
}

// Line returns the receiver's line value.
func (thread RemoteThread) Line() (LineNumber, bool) {
	if thread.line == nil {
		return 0, false
	}
	return *thread.line, true
}

// Comments performs the comments operation.
func (thread RemoteThread) Comments(start, count int) []RemoteComment {
	if start < 0 || count <= 0 || start >= len(thread.comments) {
		return nil
	}
	return slices.Clone(thread.comments[start:min(start+count, len(thread.comments))])
}

// Comment returns one immutable remote comment without cloning the collection.
func (thread RemoteThread) Comment(index int) (RemoteComment, bool) {
	if index < 0 || index >= len(thread.comments) {
		return RemoteComment{}, false
	}
	return thread.comments[index], true
}

type remoteThreadOwnership uint8

const (
	cloneRemoteThreads remoteThreadOwnership = iota + 1
	takeRemoteThreads
)

type remoteThreadPosition struct {
	id    string
	index int
}

// WithRemoteThreads performs the with remote threads operation.
func (snapshot Snapshot) WithRemoteThreads(threads []RemoteThread) (Snapshot, error) {
	return snapshot.withRemoteThreads(threads, cloneRemoteThreads)
}

// TakeRemoteThreads consumes an already constructed immutable thread slice.
// The caller must not retain or mutate the slice after this call.
func (snapshot Snapshot) TakeRemoteThreads(threads []RemoteThread) (Snapshot, error) {
	return snapshot.withRemoteThreads(threads, takeRemoteThreads)
}

// TakeRemoteThreadsCancellable consumes an immutable thread slice while
// bounding validation and projection cancellation latency.
func (snapshot Snapshot) TakeRemoteThreadsCancellable(
	cancelled func() error,
	threads []RemoteThread,
) (Snapshot, error) {
	return snapshot.withRemoteThreadsCancellable(cancelled, threads, takeRemoteThreads)
}

func (snapshot Snapshot) withRemoteThreads(
	threads []RemoteThread,
	ownership remoteThreadOwnership,
) (Snapshot, error) {
	return snapshot.withRemoteThreadsCancellable(nil, threads, ownership)
}

func (snapshot Snapshot) withRemoteThreadsCancellable(
	cancelled func() error,
	threads []RemoteThread,
	ownership remoteThreadOwnership,
) (Snapshot, error) {
	if err := cancellationError(cancelled); err != nil {
		return Snapshot{}, err
	}
	if len(threads) > MaxRemoteThreads {
		return Snapshot{}, fmt.Errorf(
			"%w: remote thread count exceeds %d",
			ErrInvalidRemoteThread,
			MaxRemoteThreads,
		)
	}
	owned := threads
	if ownership == cloneRemoteThreads {
		owned = slices.Clone(threads)
	} else if ownership != takeRemoteThreads {
		return Snapshot{}, ErrInvalidRemoteThread
	}
	pathMappings := snapshot.remotePathMappings()
	anchors := make(map[commentAnchor]struct{})
	retained, validationErr := validateRemoteThreadsCancellable(cancelled, owned)
	if validationErr != nil {
		return Snapshot{}, validationErr
	}
	if err := cancellationError(cancelled); err != nil {
		return Snapshot{}, err
	}
	positions := make([]remoteThreadPosition, len(owned))
	for index, thread := range owned {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return Snapshot{}, err
			}
		}
		mapping, exists := pathMappings.lookup(thread.path.raw)
		if !exists {
			mapping.status = RemoteMappingMissing
		}
		thread.mapping = mapping.status
		thread.file = mapping.file
		if thread.mapping == RemoteMappingMapped && !thread.outdated && thread.line != nil {
			anchors[commentAnchor{
				file: thread.file,
				side: thread.side,
				line: *thread.line,
			}] = struct{}{}
		}
		owned[index] = thread
		positions[index] = remoteThreadPosition{id: thread.id, index: index}
	}
	if err := cancellationError(cancelled); err != nil {
		return Snapshot{}, err
	}
	if err := sortRemoteThreadPositionsCancellable(cancelled, positions); err != nil {
		return Snapshot{}, err
	}
	retained += int64(cap(positions)) * remoteThreadPositionBytes
	if retained > MaxRemoteThreadBytes {
		return Snapshot{}, fmt.Errorf(
			"%w: remote thread data exceeds its retained bound",
			ErrInvalidRemoteThread,
		)
	}
	next := snapshot
	next.remoteThreads = owned
	next.remoteThreadPositions = positions
	next.remoteCommentAnchors = anchors
	next.remoteThreadStatus = RemoteThreadsReady
	next.remoteThreadDetail = EmptyOperationDetail()
	retained += int64(len(anchors)) * remoteAnchorStorageBytes
	updated, retainedErr := next.withRemoteRetainedBytes(retained, ErrInvalidRemoteThread)
	if retainedErr != nil {
		return Snapshot{}, retainedErr
	}
	if err := cancellationError(cancelled); err != nil {
		return Snapshot{}, err
	}
	return updated, nil
}

func sortRemoteThreadPositionsCancellable(
	cancelled func() error,
	positions []remoteThreadPosition,
) error {
	work := 0
	poll := func() error {
		work++
		if work%256 != 0 {
			return nil
		}
		return cancellationError(cancelled)
	}
	less := func(left, right remoteThreadPosition) bool {
		return left.id < right.id
	}
	siftDown := func(root, end int) error {
		for child := root*2 + 1; child < end; child = root*2 + 1 {
			if err := poll(); err != nil {
				return err
			}
			if child+1 < end && less(positions[child], positions[child+1]) {
				child++
			}
			if !less(positions[root], positions[child]) {
				return nil
			}
			positions[root], positions[child] = positions[child], positions[root]
			root = child
		}
		return nil
	}
	if err := cancellationError(cancelled); err != nil {
		return err
	}
	for root := len(positions)/2 - 1; root >= 0; root-- {
		if err := siftDown(root, len(positions)); err != nil {
			return err
		}
	}
	for end := len(positions) - 1; end > 0; end-- {
		positions[0], positions[end] = positions[end], positions[0]
		if err := siftDown(0, end); err != nil {
			return err
		}
	}
	return cancellationError(cancelled)
}

func validateRemoteThreadsCancellable(
	cancelled func() error,
	threads []RemoteThread,
) (int64, error) {
	threadIDs := make(map[string]struct{}, len(threads))
	commentIDs := make(map[string]struct{})
	retained := int64(cap(threads)-len(threads)) * remoteThreadStorageBytes
	for index, thread := range threads {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return 0, err
			}
		}
		if thread.id == "" || thread.path.raw == "" ||
			thread.side != LineOld && thread.side != LineNew ||
			len(thread.comments) > MaxRemoteComments {
			return 0, ErrInvalidRemoteThread
		}
		if _, exists := threadIDs[thread.id]; exists {
			return 0, fmt.Errorf(
				"%w: duplicate remote thread %q",
				ErrInvalidRemoteThread,
				thread.id,
			)
		}
		threadIDs[thread.id] = struct{}{}
		threadRetained := int64(remoteThreadStorageBytes+len(thread.id)+len(thread.path.raw)) +
			int64(cap(thread.comments)-len(thread.comments))*remoteCommentStorageBytes
		for _, comment := range thread.comments {
			if err := cancellationError(cancelled); err != nil {
				return 0, err
			}
			if comment.id == "" || !utf8.ValidString(comment.id) ||
				!utf8.ValidString(comment.author) || !utf8.ValidString(comment.body) ||
				!utf8.ValidString(comment.createdAt) || !utf8.ValidString(comment.url) {
				return 0, ErrInvalidRemoteThread
			}
			if _, exists := commentIDs[comment.id]; exists {
				return 0, fmt.Errorf(
					"%w: duplicate remote comment %q",
					ErrInvalidRemoteThread,
					comment.id,
				)
			}
			commentIDs[comment.id] = struct{}{}
			threadRetained += comment.retainedBytes()
		}
		retained += threadRetained
		if retained > MaxRemoteThreadBytes ||
			len(threadIDs)+len(commentIDs) > MaxRemoteThreads {
			return 0, fmt.Errorf(
				"%w: remote thread data exceeds its retained bound",
				ErrInvalidRemoteThread,
			)
		}
	}
	return retained, nil
}

// WithRemoteThreadFailure performs the with remote thread failure operation.
func (snapshot Snapshot) WithRemoteThreadFailure(
	status RemoteThreadStatus,
	detail OperationDetail,
) (Snapshot, error) {
	if status != RemoteThreadsUnavailable && status != RemoteThreadsLimited ||
		detail.IsEmpty() {
		return Snapshot{}, ErrInvalidRemoteThread
	}
	next := snapshot
	next.remoteThreadStatus = status
	next.remoteThreadDetail = detail
	retained := snapshot.remoteRetainedBytes - snapshot.remoteThreadDetail.Bytes() + detail.Bytes()
	if retained > MaxRemoteThreadBytes {
		return Snapshot{}, ErrInvalidRemoteThread
	}
	return next.withRemoteRetainedBytes(retained, ErrInvalidRemoteThread)
}

func retainRemoteThreadsAfterFailure(
	cancelled func() error,
	previous, replacement Snapshot,
) (Snapshot, error) {
	status := replacement.remoteThreadStatus
	if status != RemoteThreadsUnavailable && status != RemoteThreadsLimited ||
		len(previous.remoteThreads) == 0 {
		return replacement, nil
	}
	detail := replacement.remoteThreadDetail
	threads := slices.Clone(previous.remoteThreads)
	next, err := replacement.withRemoteThreadsCancellable(
		cancelled,
		threads,
		takeRemoteThreads,
	)
	if err != nil {
		if cause := cancellationError(cancelled); cause != nil {
			return Snapshot{}, cause
		}
		if errors.Is(err, ErrInvalidRemoteThread) || errors.Is(err, ErrInvalidSnapshot) {
			return replacement, nil
		}
		return Snapshot{}, err
	}
	if replacement.pendingReviewStatus == PendingReviewReady &&
		replacement.pendingReview != nil {
		next, err = next.withoutPendingRemoteCommentsCancellable(
			cancelled,
			*replacement.pendingReview,
		)
		if err != nil {
			if cause := cancellationError(cancelled); cause != nil {
				return Snapshot{}, cause
			}
			if errors.Is(err, ErrInvalidRemoteThread) || errors.Is(err, ErrInvalidSnapshot) {
				return replacement, nil
			}
			return Snapshot{}, err
		}
	}
	if next.remoteRetainedBytes+detail.Bytes() > MaxRemoteThreadBytes {
		return replacement, nil
	}
	retained, err := next.WithRemoteThreadFailure(status, detail)
	if err != nil && (errors.Is(err, ErrInvalidRemoteThread) || errors.Is(err, ErrInvalidSnapshot)) {
		return replacement, nil
	}
	return retained, err
}

type remotePathMapping struct {
	status RemoteMappingStatus
	file   FileID
}

func (snapshot Snapshot) remotePathMappings() remotePathProjection {
	mappings, err := snapshot.remotePathMappingsCancellable(nil)
	if err != nil {
		panic("nil cancellation callback returned an error")
	}
	return mappings
}

func (snapshot Snapshot) remotePathMappingsCancellable(
	cancelled func() error,
) (remotePathProjection, error) {
	if err := cancellationError(cancelled); err != nil {
		return remotePathProjection{}, err
	}
	return snapshot.pathProjection, nil
}

// RemoteThreadSummary returns the receiver's remote thread summary value.
func (snapshot Snapshot) RemoteThreadSummary() RemoteThreadSummary {
	status := snapshot.remoteThreadStatus
	if status == 0 {
		status = RemoteThreadsNotRequested
	}
	return RemoteThreadSummary{
		Status: status,
		Count:  len(snapshot.remoteThreads),
		Detail: snapshot.remoteThreadDetail,
	}
}

// RemoteThreadCount returns the receiver's remote thread count value.
func (snapshot Snapshot) RemoteThreadCount() int {
	return len(snapshot.remoteThreads)
}

// RemoteThread returns one immutable remote thread without cloning the collection.
func (snapshot Snapshot) RemoteThread(index int) (RemoteThread, bool) {
	if index < 0 || index >= len(snapshot.remoteThreads) {
		return RemoteThread{}, false
	}
	return snapshot.remoteThreads[index], true
}

// RemoteThreadIndex returns the immutable row index for one stable thread ID.
func (snapshot Snapshot) RemoteThreadIndex(id string) (int, bool) {
	index := sort.Search(len(snapshot.remoteThreadPositions), func(index int) bool {
		return snapshot.remoteThreadPositions[index].id >= id
	})
	if index == len(snapshot.remoteThreadPositions) ||
		snapshot.remoteThreadPositions[index].id != id {
		return 0, false
	}
	return snapshot.remoteThreadPositions[index].index, true
}

// RemoteThreads performs the remote threads operation.
func (snapshot Snapshot) RemoteThreads(start, count int) []RemoteThread {
	if start < 0 || count <= 0 || start >= len(snapshot.remoteThreads) {
		return nil
	}
	return slices.Clone(snapshot.remoteThreads[start:min(start+count, len(snapshot.remoteThreads))])
}
