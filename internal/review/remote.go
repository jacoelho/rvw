package review

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// MaxRemoteThreads defines the supported remote threads limit.
const (
	MaxRemoteThreads           = 100_000
	MaxRemoteComments          = 100_000
	MaxRemoteThreadBytes int64 = 64 << 20
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
		return "limited"
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
	return int64(
		len(comment.id) +
			len(comment.author) +
			len(comment.body) +
			len(comment.createdAt) +
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
	if descriptor.ID == "" || int64(len(descriptor.ID)) > MaxPathBytes ||
		!utf8.ValidString(descriptor.ID) ||
		descriptor.Side != LineOld && descriptor.Side != LineNew ||
		len(descriptor.Comments) > MaxRemoteComments {
		return RemoteThread{}, ErrInvalidRemoteThread
	}
	path, err := NewPath(descriptor.Path.Raw())
	if err != nil || path.raw == "" {
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

func (thread RemoteThread) retainedBytes() int64 {
	total := int64(len(thread.id)) + int64(len(thread.path.raw))
	for _, comment := range thread.comments {
		total += comment.retainedBytes()
	}
	return total
}

// WithRemoteThreads performs the with remote threads operation.
func (snapshot Snapshot) WithRemoteThreads(threads []RemoteThread) (Snapshot, error) {
	if len(threads) > MaxRemoteThreads {
		return Snapshot{}, fmt.Errorf(
			"%w: remote thread count exceeds %d",
			ErrInvalidRemoteThread,
			MaxRemoteThreads,
		)
	}
	owned := slices.Clone(threads)
	pathMappings := snapshot.remotePathMappings()
	anchors := make(map[commentAnchor]struct{}, min(len(owned), MaxRemoteThreads))
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
		mapping, exists := pathMappings[validated.path.raw]
		if !exists {
			mapping.status = RemoteMappingMissing
		}
		validated.mapping = mapping.status
		validated.file = mapping.file
		if validated.mapping == RemoteMappingMapped && !validated.outdated && validated.line != nil {
			anchors[commentAnchor{
				file: validated.file,
				side: validated.side,
				line: *validated.line,
			}] = struct{}{}
		}
		owned[index] = validated
	}
	snapshot.remoteThreads = owned
	snapshot.remoteCommentAnchors = anchors
	snapshot.remoteThreadStatus = RemoteThreadsReady
	snapshot.remoteThreadDetail = EmptyOperationDetail()
	return snapshot, nil
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
	snapshot.remoteThreads = nil
	snapshot.remoteCommentAnchors = nil
	snapshot.remoteThreadStatus = status
	snapshot.remoteThreadDetail = detail
	return snapshot, nil
}

type remotePathMapping struct {
	status RemoteMappingStatus
	file   FileID
}

func (snapshot Snapshot) remotePathMappings() map[string]remotePathMapping {
	mappings := make(map[string]remotePathMapping, snapshot.FileCount()*2)
	for file := range snapshot.AllFiles() {
		for _, path := range fileAliases(file) {
			mapping, exists := mappings[path]
			switch {
			case !exists:
				mappings[path] = remotePathMapping{
					status: RemoteMappingMapped,
					file:   file.ID(),
				}
			case mapping.status == RemoteMappingMapped && mapping.file != file.ID():
				mappings[path] = remotePathMapping{status: RemoteMappingAmbiguous}
			}
		}
	}
	return mappings
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

// RemoteThreads performs the remote threads operation.
func (snapshot Snapshot) RemoteThreads(start, count int) []RemoteThread {
	if start < 0 || count <= 0 || start >= len(snapshot.remoteThreads) {
		return nil
	}
	return slices.Clone(snapshot.remoteThreads[start:min(start+count, len(snapshot.remoteThreads))])
}
