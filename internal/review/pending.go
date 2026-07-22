package review

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrInvalidPendingReview identifies an invalid pending-review capture.
var ErrInvalidPendingReview = errors.New("invalid pending review")

// MaxPendingReviewBytes bounds one fully acquired pending review.
const MaxPendingReviewBytes int64 = 8 << 20

// PendingReviewStatus describes pending-review acquisition state.
type PendingReviewStatus uint8

// PendingReviewNotRequested identifies a supported pending review not requested value.
const (
	PendingReviewNotRequested PendingReviewStatus = iota + 1
	PendingReviewAbsent
	PendingReviewReady
	PendingReviewUnavailable
	PendingReviewLimited
	PendingReviewAmbiguous
)

// String returns the receiver's string value.
func (status PendingReviewStatus) String() string {
	switch status {
	case PendingReviewNotRequested:
		return "not-requested"
	case PendingReviewAbsent:
		return "absent"
	case PendingReviewReady:
		return "ready"
	case PendingReviewUnavailable:
		return "unavailable"
	case PendingReviewLimited:
		return limitedValue
	case PendingReviewAmbiguous:
		return "ambiguous"
	default:
		return invalidValue
	}
}

// ReviewNodeID is an opaque GitHub GraphQL review identity.
type ReviewNodeID string

// NewReviewNodeID constructs and validates a review node identity.
func NewReviewNodeID(value string) (ReviewNodeID, error) {
	if err := validatePendingText(value, MaxPathBytes); err != nil {
		return "", fmt.Errorf("%w: review node ID: %w", ErrInvalidPendingReview, err)
	}
	return ReviewNodeID(strings.Clone(value)), nil
}

// String returns the receiver's string value.
func (id ReviewNodeID) String() string { return string(id) }

// CommentNodeID is an opaque GitHub GraphQL review-comment identity.
type CommentNodeID string

// NewCommentNodeID constructs and validates a comment node identity.
func NewCommentNodeID(value string) (CommentNodeID, error) {
	if err := validatePendingText(value, MaxPathBytes); err != nil {
		return "", fmt.Errorf("%w: comment node ID: %w", ErrInvalidPendingReview, err)
	}
	return CommentNodeID(strings.Clone(value)), nil
}

// String returns the receiver's string value.
func (id CommentNodeID) String() string { return string(id) }

// ReviewDatabaseID is an opaque GitHub 64-bit review database identity.
type ReviewDatabaseID uint64

// NewReviewDatabaseID constructs and validates a review database identity.
func NewReviewDatabaseID(value uint64) (ReviewDatabaseID, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: review database ID must be positive", ErrInvalidPendingReview)
	}
	return ReviewDatabaseID(value), nil
}

// Uint64 returns the receiver's uint64 value.
func (id ReviewDatabaseID) Uint64() uint64 { return uint64(id) }

// CommentDatabaseID is an opaque GitHub 64-bit review-comment database identity.
type CommentDatabaseID uint64

// NewCommentDatabaseID constructs and validates a comment database identity.
func NewCommentDatabaseID(value uint64) (CommentDatabaseID, error) {
	if value == 0 {
		return 0, fmt.Errorf("%w: comment database ID must be positive", ErrInvalidPendingReview)
	}
	return CommentDatabaseID(value), nil
}

// Uint64 returns the receiver's uint64 value.
func (id CommentDatabaseID) Uint64() uint64 { return uint64(id) }

// PendingSubject describes the immutable target class of a GitHub draft comment.
type PendingSubject uint8

// PendingSubjectLine identifies a supported line subject value.
const (
	PendingSubjectLine PendingSubject = iota + 1
	PendingSubjectFile
	PendingSubjectReply
)

// PendingCommentTarget preserves GitHub target provenance independently of its
// best-effort local navigation projection.
type PendingCommentTarget struct {
	line           *LineNumber
	originalStart  *LineNumber
	originalLine   *LineNumber
	startLine      *LineNumber
	commit         ObjectID
	originalCommit ObjectID
	replyTo        CommentNodeID
	path           Path
	replyDatabase  CommentDatabaseID
	subject        PendingSubject
	side           LineSide
	startSide      LineSide
	outdated       bool
}

// PendingCommentTargetDescriptor describes a pending comment target.
type PendingCommentTargetDescriptor struct {
	Line           *LineNumber
	OriginalStart  *LineNumber
	OriginalLine   *LineNumber
	StartLine      *LineNumber
	Commit         ObjectID
	OriginalCommit ObjectID
	ReplyTo        CommentNodeID
	Path           Path
	ReplyDatabase  CommentDatabaseID
	Subject        PendingSubject
	Side           LineSide
	StartSide      LineSide
	Outdated       bool
}

// NewPendingCommentTarget constructs and validates immutable target provenance.
func NewPendingCommentTarget(
	descriptor PendingCommentTargetDescriptor,
) (PendingCommentTarget, error) {
	if descriptor.Subject < PendingSubjectLine || descriptor.Subject > PendingSubjectReply {
		return PendingCommentTarget{}, ErrInvalidPendingReview
	}
	path, err := NewPath(descriptor.Path.Raw())
	if err != nil || path.raw == "" {
		return PendingCommentTarget{}, fmt.Errorf("%w: comment path", ErrInvalidPendingReview)
	}
	commit, err := NewObjectID(descriptor.Commit.String())
	if err != nil {
		return PendingCommentTarget{}, fmt.Errorf("%w: comment commit", ErrInvalidPendingReview)
	}
	originalCommit, err := NewObjectID(descriptor.OriginalCommit.String())
	if err != nil {
		return PendingCommentTarget{}, fmt.Errorf("%w: original comment commit", ErrInvalidPendingReview)
	}
	replyTo := descriptor.ReplyTo
	if replyTo != "" {
		replyTo, err = NewCommentNodeID(replyTo.String())
		if err != nil {
			return PendingCommentTarget{}, err
		}
	}
	replyDatabase := descriptor.ReplyDatabase
	if replyDatabase != 0 {
		replyDatabase, err = NewCommentDatabaseID(replyDatabase.Uint64())
		if err != nil {
			return PendingCommentTarget{}, err
		}
	}
	if descriptor.Subject == PendingSubjectReply && descriptor.ReplyTo == "" &&
		descriptor.ReplyDatabase == 0 {
		return PendingCommentTarget{}, fmt.Errorf("%w: reply target", ErrInvalidPendingReview)
	}
	if descriptor.Subject != PendingSubjectReply &&
		(descriptor.ReplyTo != "" || descriptor.ReplyDatabase != 0) {
		return PendingCommentTarget{}, fmt.Errorf("%w: reply identity on non-reply", ErrInvalidPendingReview)
	}
	lineValue, err := validatePendingLine(descriptor.Line)
	if err != nil {
		return PendingCommentTarget{}, err
	}
	startLineValue, err := validatePendingLine(descriptor.StartLine)
	if err != nil {
		return PendingCommentTarget{}, err
	}
	originalLineValue, err := validatePendingLine(descriptor.OriginalLine)
	if err != nil {
		return PendingCommentTarget{}, err
	}
	originalStartValue, err := validatePendingLine(descriptor.OriginalStart)
	if err != nil {
		return PendingCommentTarget{}, err
	}
	line := optionalPendingLine(descriptor.Line, lineValue)
	startLine := optionalPendingLine(descriptor.StartLine, startLineValue)
	originalLine := optionalPendingLine(descriptor.OriginalLine, originalLineValue)
	originalStart := optionalPendingLine(descriptor.OriginalStart, originalStartValue)
	if originalStart != nil &&
		(originalLine == nil || *originalStart > *originalLine) {
		return PendingCommentTarget{}, fmt.Errorf("%w: original line range", ErrInvalidPendingReview)
	}
	if descriptor.Subject == PendingSubjectLine {
		if line == nil || descriptor.Side != LineOld && descriptor.Side != LineNew {
			return PendingCommentTarget{}, fmt.Errorf("%w: line target", ErrInvalidPendingReview)
		}
		if startLine != nil &&
			(descriptor.StartSide != LineOld && descriptor.StartSide != LineNew ||
				*startLine > *line) {
			return PendingCommentTarget{}, fmt.Errorf("%w: line range", ErrInvalidPendingReview)
		}
	}
	return PendingCommentTarget{
		replyTo:        replyTo,
		replyDatabase:  replyDatabase,
		path:           path,
		commit:         commit,
		originalCommit: originalCommit,
		line:           line,
		startLine:      startLine,
		originalLine:   originalLine,
		originalStart:  originalStart,
		subject:        descriptor.Subject,
		side:           descriptor.Side,
		startSide:      descriptor.StartSide,
		outdated:       descriptor.Outdated,
	}, nil
}

func validatePendingLine(line *LineNumber) (LineNumber, error) {
	if line == nil {
		return 0, nil
	}
	validated, err := NewLineNumber(line.Uint32())
	if err != nil {
		return 0, fmt.Errorf("%w: comment line", ErrInvalidPendingReview)
	}
	return validated, nil
}

func optionalPendingLine(original *LineNumber, validated LineNumber) *LineNumber {
	if original == nil {
		return nil
	}
	return new(validated)
}

func cloneLine(line *LineNumber) *LineNumber {
	if line == nil {
		return nil
	}
	return new(*line)
}

// Subject returns the receiver's subject value.
func (target PendingCommentTarget) Subject() PendingSubject { return target.subject }

// Path returns the receiver's path value.
func (target PendingCommentTarget) Path() Path { return target.path }

// Lines returns the immutable current range when one exists.
func (target PendingCommentTarget) Lines() (
	LineSide,
	*LineNumber,
	LineSide,
	*LineNumber,
) {
	return target.startSide, cloneLine(target.startLine), target.side, cloneLine(target.line)
}

// ReplyTo returns the receiver's reply target.
func (target PendingCommentTarget) ReplyTo() (CommentNodeID, bool) {
	return target.replyTo, target.replyTo != ""
}

// ReplyDatabaseID returns the REST database reply identity when available.
func (target PendingCommentTarget) ReplyDatabaseID() (CommentDatabaseID, bool) {
	return target.replyDatabase, target.replyDatabase != 0
}

// Outdated reports the receiver's outdated state.
func (target PendingCommentTarget) Outdated() bool { return target.outdated }

// PendingComment is an immutable GitHub pending-review comment capture.
type PendingComment struct {
	nodeID       CommentNodeID
	body         string
	target       PendingCommentTarget
	databaseID   CommentDatabaseID
	viewerUpdate bool
	viewerDelete bool
}

// PendingCommentDescriptor describes a pending-review comment.
type PendingCommentDescriptor struct {
	NodeID       CommentNodeID
	Body         string
	Target       PendingCommentTarget
	DatabaseID   CommentDatabaseID
	ViewerUpdate bool
	ViewerDelete bool
}

// NewPendingComment constructs and validates a pending-review comment.
func NewPendingComment(descriptor PendingCommentDescriptor) (PendingComment, error) {
	nodeID, err := NewCommentNodeID(descriptor.NodeID.String())
	if err != nil {
		return PendingComment{}, err
	}
	databaseID, err := NewCommentDatabaseID(descriptor.DatabaseID.Uint64())
	if err != nil {
		return PendingComment{}, err
	}
	target, err := NewPendingCommentTarget(PendingCommentTargetDescriptor{
		ReplyTo: descriptor.Target.replyTo, ReplyDatabase: descriptor.Target.replyDatabase,
		Path: descriptor.Target.path, Commit: descriptor.Target.commit,
		OriginalCommit: descriptor.Target.originalCommit,
		Line:           descriptor.Target.line, StartLine: descriptor.Target.startLine,
		OriginalLine:  descriptor.Target.originalLine,
		OriginalStart: descriptor.Target.originalStart,
		Subject:       descriptor.Target.subject, Side: descriptor.Target.side,
		StartSide: descriptor.Target.startSide, Outdated: descriptor.Target.outdated,
	})
	if err != nil || !utf8.ValidString(descriptor.Body) ||
		int64(len(descriptor.Body)) > MaxCommentBodyBytes ||
		strings.TrimSpace(descriptor.Body) == "" {
		return PendingComment{}, ErrInvalidPendingReview
	}
	return PendingComment{
		nodeID:       nodeID,
		databaseID:   databaseID,
		body:         strings.Clone(descriptor.Body),
		target:       target,
		viewerUpdate: descriptor.ViewerUpdate,
		viewerDelete: descriptor.ViewerDelete,
	}, nil
}

// NodeID returns the receiver's node identity.
func (comment PendingComment) NodeID() CommentNodeID { return comment.nodeID }

// DatabaseID returns the receiver's database identity.
func (comment PendingComment) DatabaseID() CommentDatabaseID { return comment.databaseID }

// Body returns the receiver's body.
func (comment PendingComment) Body() string { return comment.body }

// Target returns the receiver's target.
func (comment PendingComment) Target() PendingCommentTarget { return comment.target }

// ViewerCanUpdate reports whether the authenticated viewer can update the body.
func (comment PendingComment) ViewerCanUpdate() bool { return comment.viewerUpdate }

// ViewerCanDelete reports whether the authenticated viewer can delete the comment.
func (comment PendingComment) ViewerCanDelete() bool { return comment.viewerDelete }

// PendingReview is an immutable, fully acquired GitHub pending review.
type PendingReview struct {
	nodeID       ReviewNodeID
	commit       ObjectID
	body         string
	comments     []PendingComment
	databaseID   ReviewDatabaseID
	viewerUpdate bool
}

// PendingReviewDescriptor describes a pending review.
type PendingReviewDescriptor struct {
	NodeID       ReviewNodeID
	Commit       ObjectID
	Body         string
	Comments     []PendingComment
	DatabaseID   ReviewDatabaseID
	ViewerUpdate bool
}

// NewPendingReview constructs and validates a complete pending review.
func NewPendingReview(descriptor PendingReviewDescriptor) (PendingReview, error) {
	nodeID, err := NewReviewNodeID(descriptor.NodeID.String())
	if err != nil {
		return PendingReview{}, err
	}
	databaseID, err := NewReviewDatabaseID(descriptor.DatabaseID.Uint64())
	if err != nil {
		return PendingReview{}, err
	}
	commit, err := NewObjectID(descriptor.Commit.String())
	if err != nil {
		return PendingReview{}, err
	}
	if !utf8.ValidString(descriptor.Body) || int64(len(descriptor.Body)) > MaxCommentBodyBytes ||
		len(descriptor.Comments) > MaxComments {
		return PendingReview{}, ErrInvalidPendingReview
	}
	comments := slices.Clone(descriptor.Comments)
	seenNodes := make(map[CommentNodeID]struct{}, len(comments))
	seenDatabases := make(map[CommentDatabaseID]struct{}, len(comments))
	bodyRetained := int64(len(descriptor.Body))
	retained := bodyRetained + int64(len(nodeID)+len(commit)+64)
	for index, comment := range comments {
		comment, err = NewPendingComment(PendingCommentDescriptor{
			NodeID: comment.nodeID, DatabaseID: comment.databaseID, Body: comment.body,
			Target: comment.target, ViewerUpdate: comment.viewerUpdate,
			ViewerDelete: comment.viewerDelete,
		})
		if err != nil {
			return PendingReview{}, err
		}
		comments[index] = comment
		if _, exists := seenNodes[comment.nodeID]; exists {
			return PendingReview{}, fmt.Errorf("%w: duplicate comment node ID", ErrInvalidPendingReview)
		}
		if _, exists := seenDatabases[comment.databaseID]; exists {
			return PendingReview{}, fmt.Errorf("%w: duplicate comment database ID", ErrInvalidPendingReview)
		}
		seenNodes[comment.nodeID] = struct{}{}
		seenDatabases[comment.databaseID] = struct{}{}
		bodyRetained += int64(len(comment.body))
		retained += int64(
			len(comment.body) + len(comment.nodeID) + len(comment.target.path.raw) +
				len(comment.target.commit) + len(comment.target.originalCommit) +
				len(comment.target.replyTo) + 128,
		)
	}
	if bodyRetained > MaxCommentBodyTotal {
		return PendingReview{}, fmt.Errorf("%w: body bytes exceed %d", ErrInvalidPendingReview, MaxCommentBodyTotal)
	}
	if retained > MaxPendingReviewBytes {
		return PendingReview{}, fmt.Errorf("%w: retained bytes exceed %d", ErrInvalidPendingReview, MaxPendingReviewBytes)
	}
	return PendingReview{
		nodeID:       nodeID,
		databaseID:   databaseID,
		commit:       commit,
		body:         strings.Clone(descriptor.Body),
		comments:     comments,
		viewerUpdate: descriptor.ViewerUpdate,
	}, nil
}

// NodeID returns the receiver's node identity.
func (pending PendingReview) NodeID() ReviewNodeID { return pending.nodeID }

// DatabaseID returns the receiver's database identity.
func (pending PendingReview) DatabaseID() ReviewDatabaseID { return pending.databaseID }

// Commit returns the receiver's commit identity.
func (pending PendingReview) Commit() ObjectID { return pending.commit }

// Body returns the receiver's body.
func (pending PendingReview) Body() string { return pending.body }

// ViewerCanUpdate reports whether the authenticated viewer can update the review body.
func (pending PendingReview) ViewerCanUpdate() bool { return pending.viewerUpdate }

// CommentCount returns the receiver's comment count.
func (pending PendingReview) CommentCount() int { return len(pending.comments) }

// Comments returns a bounded copy of pending comments.
func (pending PendingReview) Comments(start, count int) []PendingComment {
	if start < 0 || count <= 0 || start >= len(pending.comments) {
		return nil
	}
	return slices.Clone(pending.comments[start:min(start+count, len(pending.comments))])
}

// PendingReviewSummary describes acquisition and local synchronization state.
type PendingReviewSummary struct {
	Detail    OperationDetail
	ReviewID  ReviewDatabaseID
	Comments  uint32
	Dirty     uint32
	Conflicts uint32
	Status    PendingReviewStatus
}

// WithPendingReview retains a validated pending review in the snapshot.
func (snapshot Snapshot) WithPendingReview(pending PendingReview) (Snapshot, error) {
	validated, err := NewPendingReview(PendingReviewDescriptor{
		NodeID:       pending.nodeID,
		DatabaseID:   pending.databaseID,
		Commit:       pending.commit,
		Body:         pending.body,
		Comments:     pending.comments,
		ViewerUpdate: pending.viewerUpdate,
	})
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.pendingReview = new(validated)
	snapshot.pendingReviewStatus = PendingReviewReady
	snapshot.pendingReviewDetail = EmptyOperationDetail()
	if snapshot.remoteThreadStatus == RemoteThreadsReady {
		snapshot, err = snapshot.withoutPendingRemoteComments(validated)
		if err != nil {
			return Snapshot{}, err
		}
	}
	return snapshot, nil
}

func (snapshot Snapshot) withoutPendingRemoteComments(
	pending PendingReview,
) (Snapshot, error) {
	pendingNodes := make(map[string]struct{}, len(pending.comments))
	for _, comment := range pending.comments {
		pendingNodes[comment.nodeID.String()] = struct{}{}
	}
	filtered := make([]RemoteThread, 0, len(snapshot.remoteThreads))
	for _, thread := range snapshot.remoteThreads {
		comments := make([]RemoteComment, 0, len(thread.comments))
		for _, comment := range thread.comments {
			if _, pendingComment := pendingNodes[comment.id]; !pendingComment {
				comments = append(comments, comment)
			}
		}
		if len(comments) == 0 {
			continue
		}
		retained, err := NewRemoteThread(RemoteThreadDescriptor{
			ID: thread.id, Resolved: thread.resolved, Outdated: thread.outdated,
			Path: thread.path, Line: thread.line, Side: thread.side, Comments: comments,
		})
		if err != nil {
			return Snapshot{}, err
		}
		filtered = append(filtered, retained)
	}
	return snapshot.WithRemoteThreads(filtered)
}

// WithPendingReviewStatus retains an explicit non-ready acquisition result.
func (snapshot Snapshot) WithPendingReviewStatus(
	status PendingReviewStatus,
	detail OperationDetail,
) (Snapshot, error) {
	if status != PendingReviewAbsent && status != PendingReviewUnavailable &&
		status != PendingReviewLimited && status != PendingReviewAmbiguous {
		return Snapshot{}, ErrInvalidPendingReview
	}
	if status != PendingReviewAbsent && detail.IsEmpty() {
		return Snapshot{}, ErrInvalidPendingReview
	}
	snapshot.pendingReview = nil
	snapshot.pendingReviewStatus = status
	snapshot.pendingReviewDetail = detail
	return snapshot, nil
}

// PendingReview returns the complete captured pending review when ready.
func (snapshot Snapshot) PendingReview() (PendingReview, bool) {
	if snapshot.pendingReview == nil {
		return PendingReview{}, false
	}
	pending := *snapshot.pendingReview
	pending.comments = slices.Clone(pending.comments)
	return pending, true
}

// PendingReviewSummary returns acquisition state.
func (snapshot Snapshot) PendingReviewSummary() PendingReviewSummary {
	status := snapshot.pendingReviewStatus
	if status == 0 {
		status = PendingReviewNotRequested
	}
	summary := PendingReviewSummary{Status: status, Detail: snapshot.pendingReviewDetail}
	if snapshot.pendingReview != nil {
		summary.ReviewID = snapshot.pendingReview.databaseID
		summary.Comments = uint32FromInt(len(snapshot.pendingReview.comments))
	}
	return summary
}

func validatePendingText(value string, maximum int64) error {
	if strings.TrimSpace(value) == "" || int64(len(value)) > maximum || !utf8.ValidString(value) {
		return errors.New("value is empty, too long, or invalid UTF-8")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("value contains a control character")
		}
	}
	return nil
}

type pendingBindingKind uint8

const (
	pendingBindingBody pendingBindingKind = iota + 1
	pendingBindingComment
)

type pendingConflict struct {
	base         string
	local        string
	remote       string
	localDeleted bool
}

type pendingBinding struct {
	conflict   *pendingConflict
	remoteNode CommentNodeID
	baseline   string
	marker     string
	target     PendingCommentTarget
	remoteID   CommentDatabaseID
	kind       pendingBindingKind
	deleted    bool
	canUpdate  bool
	canDelete  bool
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
	if current.pending == nil || current.draft != nil || resolution < PendingConflictLocal ||
		resolution > PendingConflictBase {
		return current, ErrInvalidReviewTransition
	}
	binding, exists := current.pending.bindings[id]
	if !exists || binding.conflict == nil {
		return current, ErrInvalidReviewTransition
	}
	next := current.clone()
	next.pending = current.pending.clone()
	binding = next.pending.bindings[id]
	conflict := *binding.conflict
	chooseDeletion := resolution == PendingConflictLocal && conflict.localDeleted ||
		resolution == PendingConflictBase && conflict.base == ""
	if chooseDeletion {
		if conflict.remote == "" {
			removePendingComments(&next, map[CommentID]struct{}{id: {}})
			delete(next.pending.bindings, id)
			delete(next.pending.byNode, binding.remoteNode)
			if binding.kind == pendingBindingBody {
				next.pending.bodyID = nil
			}
			clearEmptyPendingState(&next)
			return next, nil
		}
		binding.baseline = strings.Clone(conflict.remote)
		binding.conflict = nil
		binding.deleted = true
		next.pending.bindings[id] = binding
		return next, nil
	}
	selected := conflict.local
	switch resolution {
	case PendingConflictLocal:
	case PendingConflictRemote:
		selected = conflict.remote
	case PendingConflictBase:
		selected = conflict.base
	}
	if conflict.remote == "" && resolution != PendingConflictRemote {
		delete(next.pending.bindings, id)
		delete(next.pending.byNode, binding.remoteNode)
		if binding.kind == pendingBindingBody {
			next.pending.bodyID = nil
		}
		if err := replacePendingCommentBody(&next, id, selected); err != nil {
			return current, ErrInvalidReviewTransition
		}
		clearEmptyPendingState(&next)
		return next, nil
	}
	if selected == "" {
		removePendingComments(&next, map[CommentID]struct{}{id: {}})
		delete(next.pending.bindings, id)
		delete(next.pending.byNode, binding.remoteNode)
		if binding.kind == pendingBindingBody {
			next.pending.bodyID = nil
		}
		clearEmptyPendingState(&next)
		return next, nil
	}
	if err := replacePendingCommentBody(&next, id, selected); err != nil {
		return current, ErrInvalidReviewTransition
	}
	binding.baseline = strings.Clone(conflict.remote)
	binding.conflict = nil
	binding.deleted = false
	next.pending.bindings[id] = binding
	return next, nil
}

func clearEmptyPendingState(current *Review) {
	if current.pending != nil && len(current.pending.bindings) == 0 {
		current.pending = nil
	}
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

func importPendingReview(current Review) (Review, error) {
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
	state.bodyBase = strings.Clone(body)
	state.bodyMarker = strings.Clone(marker)
	if strings.TrimSpace(body) != "" {
		id, err := appendPendingComment(&current, body, ReviewCommentTarget(), false, false)
		if err != nil {
			return Review{}, err
		}
		state.bodyID = new(id)
		state.bindings[id] = pendingBinding{
			baseline:  strings.Clone(body),
			marker:    strings.Clone(marker),
			kind:      pendingBindingBody,
			canUpdate: pending.viewerUpdate,
		}
	}
	for _, remote := range pending.comments {
		body, commentMarker := splitPendingMarker(remote.body, draftCommentMarkerPrefix)
		target, stale, unmappable := projectPendingTarget(current.snapshot, remote.target)
		id, err := appendPendingComment(&current, body, target, stale, unmappable)
		if err != nil {
			return Review{}, err
		}
		state.bindings[id] = pendingBinding{
			target:     remote.target,
			remoteNode: remote.nodeID,
			remoteID:   remote.databaseID,
			baseline:   strings.Clone(body),
			marker:     strings.Clone(commentMarker),
			kind:       pendingBindingComment,
			canUpdate:  remote.viewerUpdate,
			canDelete:  remote.viewerDelete,
		}
		state.byNode[remote.nodeID] = id
	}
	current.pending = new(state)
	return current, nil
}

func appendPendingComment(
	current *Review,
	body string,
	target CommentTarget,
	stale, unmappable bool,
) (CommentID, error) {
	validated, err := NewCommentBody(body)
	if err != nil || len(current.comments) >= MaxComments || current.nextComment >= MaxComments ||
		current.commentBody+int64(len(validated)) > MaxCommentBodyTotal {
		return 0, ErrInvalidPendingReview
	}
	id := CommentID(current.nextComment)
	comment := Comment{
		id:         id,
		target:     target,
		body:       validated,
		stale:      stale,
		unmappable: unmappable,
	}
	current.comments = append(current.comments, comment)
	current.commentAnchors = replaceCommentAnchor(current.commentAnchors, nil, &comment)
	current.commentBody += int64(len(validated))
	current.nextComment++
	return id, nil
}

func projectPendingTarget(
	snapshot Snapshot,
	target PendingCommentTarget,
) (CommentTarget, bool, bool) {
	mapping, exists := snapshot.remotePathMappings()[target.path.raw]
	if !exists || mapping.status != RemoteMappingMapped {
		return ReviewCommentTarget(), target.outdated, true
	}
	if target.subject == PendingSubjectLine && target.line != nil {
		start := *target.line
		if target.startLine != nil {
			start = *target.startLine
		}
		if target.startLine == nil || target.startSide == target.side {
			lineTarget, err := LineCommentTarget(mapping.file, target.side, start, *target.line)
			if err == nil {
				return lineTarget.withPath(target.path), target.outdated, false
			}
		}
	}
	fileTarget, err := FileCommentTarget(mapping.file)
	if err != nil {
		return ReviewCommentTarget(), target.outdated, true
	}
	return fileTarget.withPath(target.path), target.outdated, false
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
	if !ok || comment.body.String() != binding.baseline {
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
	for id := range current.pending.bindings {
		switch current.PendingBindingState(id) {
		case PendingBindingNew, PendingBindingClean:
		case PendingBindingModified, PendingBindingDeleted:
			summary.Dirty++
		case PendingBindingConflict:
			summary.Dirty++
			summary.Conflicts++
		}
	}
	for _, comment := range current.comments {
		if _, bound := current.pending.bindings[comment.id]; !bound {
			summary.Dirty++
		}
	}
	return summary
}

func (current Review) hasPendingLocalChanges() bool {
	if current.pending == nil {
		return len(current.comments) != 0
	}
	return current.PendingReviewSummary().Dirty != 0
}
