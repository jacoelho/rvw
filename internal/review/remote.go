package review

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	MaxRemoteThreads           = 100_000
	MaxRemoteComments          = 100_000
	MaxRemoteThreadBytes int64 = 64 << 20
)

var ErrInvalidRemoteThread = errors.New("invalid remote review thread")

type RemoteThreadStatus uint8

const (
	RemoteThreadsNotRequested RemoteThreadStatus = iota + 1
	RemoteThreadsReady
	RemoteThreadsUnavailable
	RemoteThreadsLimited
)

func (status RemoteThreadStatus) String() string {
	switch status {
	case RemoteThreadsNotRequested:
		return "not-requested"
	case RemoteThreadsReady:
		return "ready"
	case RemoteThreadsUnavailable:
		return "unavailable"
	case RemoteThreadsLimited:
		return "limited"
	default:
		return "invalid"
	}
}

type RemoteThreadSummary struct {
	Status RemoteThreadStatus
	Count  int
	Detail OperationDetail
}

type RemoteComment struct {
	id        string
	author    string
	body      string
	createdAt string
	url       string
}

type RemoteCommentDescriptor struct {
	ID        string
	Author    string
	Body      string
	CreatedAt string
	URL       string
}

func NewRemoteComment(descriptor RemoteCommentDescriptor) (RemoteComment, error) {
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
	return RemoteComment{
		id:        strings.Clone(descriptor.ID),
		author:    strings.Clone(descriptor.Author),
		body:      strings.Clone(descriptor.Body),
		createdAt: strings.Clone(descriptor.CreatedAt),
		url:       strings.Clone(descriptor.URL),
	}, nil
}

func (comment RemoteComment) ID() string        { return comment.id }
func (comment RemoteComment) Author() string    { return comment.author }
func (comment RemoteComment) Body() string      { return comment.body }
func (comment RemoteComment) CreatedAt() string { return comment.createdAt }
func (comment RemoteComment) URL() string       { return comment.url }

func (comment RemoteComment) retainedBytes() int64 {
	return int64(
		len(comment.id) +
			len(comment.author) +
			len(comment.body) +
			len(comment.createdAt) +
			len(comment.url),
	)
}

type RemoteThread struct {
	id       string
	resolved bool
	outdated bool
	path     Path
	line     *LineNumber
	side     LineSide
	comments []RemoteComment
}

type RemoteThreadDescriptor struct {
	ID       string
	Resolved bool
	Outdated bool
	Path     Path
	Line     *LineNumber
	Side     LineSide
	Comments []RemoteComment
}

func NewRemoteThread(descriptor RemoteThreadDescriptor) (RemoteThread, error) {
	if descriptor.ID == "" || int64(len(descriptor.ID)) > MaxPathBytes ||
		!utf8.ValidString(descriptor.ID) ||
		descriptor.Side != LineOld && descriptor.Side != LineNew ||
		len(descriptor.Comments) > MaxRemoteComments {
		return RemoteThread{}, ErrInvalidRemoteThread
	}
	path, err := NewPath(descriptor.Path.Raw())
	if err != nil || len(path.raw) == 0 {
		return RemoteThread{}, ErrInvalidRemoteThread
	}
	if descriptor.Line != nil && *descriptor.Line == 0 {
		return RemoteThread{}, ErrInvalidRemoteThread
	}
	comments := slices.Clone(descriptor.Comments)
	seen := make(map[string]struct{}, len(comments))
	for _, comment := range comments {
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
	var line *LineNumber
	if descriptor.Line != nil {
		line = new(*descriptor.Line)
	}
	return RemoteThread{
		id:       strings.Clone(descriptor.ID),
		resolved: descriptor.Resolved,
		outdated: descriptor.Outdated,
		path:     path,
		line:     line,
		side:     descriptor.Side,
		comments: comments,
	}, nil
}

func (thread RemoteThread) ID() string        { return thread.id }
func (thread RemoteThread) Resolved() bool    { return thread.resolved }
func (thread RemoteThread) Outdated() bool    { return thread.outdated }
func (thread RemoteThread) Path() Path        { return thread.path }
func (thread RemoteThread) Side() LineSide    { return thread.side }
func (thread RemoteThread) CommentCount() int { return len(thread.comments) }

func (thread RemoteThread) Line() (LineNumber, bool) {
	if thread.line == nil {
		return 0, false
	}
	return *thread.line, true
}

func (thread RemoteThread) Comments(start, count int) []RemoteComment {
	if start < 0 || count <= 0 || start >= len(thread.comments) {
		return nil
	}
	return slices.Clone(thread.comments[start:min(start+count, len(thread.comments))])
}

func (thread RemoteThread) retainedBytes() int64 {
	total := int64(len(thread.id)) + int64(len(thread.path.raw))
	for _, comment := range thread.comments {
		total += comment.retainedBytes()
	}
	return total
}

func (snapshot Snapshot) WithRemoteThreads(threads []RemoteThread) (Snapshot, error) {
	if len(threads) > MaxRemoteThreads {
		return Snapshot{}, fmt.Errorf(
			"%w: remote thread count exceeds %d",
			ErrInvalidRemoteThread,
			MaxRemoteThreads,
		)
	}
	owned := slices.Clone(threads)
	threadIDs := make(map[string]struct{}, len(owned))
	commentIDs := make(map[string]struct{})
	var retained int64
	for index, thread := range owned {
		validated, err := NewRemoteThread(RemoteThreadDescriptor{
			ID:       thread.id,
			Resolved: thread.resolved,
			Outdated: thread.outdated,
			Path:     thread.path,
			Line:     thread.line,
			Side:     thread.side,
			Comments: thread.comments,
		})
		if err != nil {
			return Snapshot{}, err
		}
		if _, exists := threadIDs[validated.id]; exists {
			return Snapshot{}, fmt.Errorf(
				"%w: duplicate remote thread %q",
				ErrInvalidRemoteThread,
				validated.id,
			)
		}
		threadIDs[validated.id] = struct{}{}
		for _, comment := range validated.comments {
			if _, exists := commentIDs[comment.id]; exists {
				return Snapshot{}, fmt.Errorf(
					"%w: duplicate remote comment %q",
					ErrInvalidRemoteThread,
					comment.id,
				)
			}
			commentIDs[comment.id] = struct{}{}
		}
		retained += validated.retainedBytes()
		if retained > MaxRemoteThreadBytes ||
			len(threadIDs)+len(commentIDs) > MaxRemoteThreads {
			return Snapshot{}, fmt.Errorf(
				"%w: remote thread data exceeds its retained bound",
				ErrInvalidRemoteThread,
			)
		}
		owned[index] = validated
	}
	snapshot.remoteThreads = owned
	snapshot.remoteThreadStatus = RemoteThreadsReady
	snapshot.remoteThreadDetail = EmptyOperationDetail()
	return snapshot, nil
}

func (snapshot Snapshot) WithRemoteThreadFailure(
	status RemoteThreadStatus,
	detail OperationDetail,
) (Snapshot, error) {
	if status != RemoteThreadsUnavailable && status != RemoteThreadsLimited ||
		detail.IsEmpty() {
		return Snapshot{}, ErrInvalidRemoteThread
	}
	snapshot.remoteThreads = nil
	snapshot.remoteThreadStatus = status
	snapshot.remoteThreadDetail = detail
	return snapshot, nil
}

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

func (snapshot Snapshot) RemoteThreadCount() int {
	return len(snapshot.remoteThreads)
}

func (snapshot Snapshot) RemoteThreads(start, count int) []RemoteThread {
	if start < 0 || count <= 0 || start >= len(snapshot.remoteThreads) {
		return nil
	}
	return slices.Clone(snapshot.remoteThreads[start:min(start+count, len(snapshot.remoteThreads))])
}
