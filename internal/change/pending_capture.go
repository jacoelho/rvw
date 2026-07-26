package change

import (
	"errors"
	"fmt"
	"slices"
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
	if err := validatePendingText(value); err != nil {
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
	if err := validatePendingText(value); err != nil {
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

// MatchesPendingAddition reports whether a remote capture has the placement
// requested for one added pending comment. Server-populated provenance is not
// part of the request projection.
func (target PendingCommentTarget) MatchesPendingAddition(
	desired PendingCommentTarget,
) bool {
	if target.subject != desired.subject || target.outdated || desired.outdated ||
		target.path.raw != desired.path.raw || target.side != desired.side ||
		target.startSide != desired.startSide ||
		!pendingTargetLineEqual(target.line, desired.line) ||
		!pendingTargetLineEqual(target.startLine, desired.startLine) {
		return false
	}
	if desired.replyTo != "" && target.replyTo != desired.replyTo {
		return false
	}
	return desired.replyDatabase == 0 || target.replyDatabase == desired.replyDatabase
}

func pendingTargetLineEqual(left, right *LineNumber) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
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

// PendingCapability identifies an operation the viewer may perform on a pending comment.
type PendingCapability uint8

// PendingCapabilityUpdate identifies permission to update a pending comment.
const (
	PendingCapabilityUpdate PendingCapability = iota + 1
	PendingCapabilityDelete
)

const pendingCapabilityMask = 1<<PendingCapabilityUpdate | 1<<PendingCapabilityDelete

// PendingCapabilities is an opaque validated set of pending-comment permissions.
type PendingCapabilities struct {
	bits uint8
}

// NewPendingCapabilities constructs and validates pending-comment permissions.
func NewPendingCapabilities(values ...PendingCapability) (PendingCapabilities, error) {
	var capabilities PendingCapabilities
	for _, value := range values {
		if value != PendingCapabilityUpdate && value != PendingCapabilityDelete {
			return PendingCapabilities{}, ErrInvalidPendingReview
		}
		capabilities.bits |= 1 << value
	}
	return capabilities, nil
}

// Has reports whether the set includes a capability.
func (capabilities PendingCapabilities) Has(value PendingCapability) bool {
	if !capabilities.valid() || value != PendingCapabilityUpdate && value != PendingCapabilityDelete {
		return false
	}
	return capabilities.bits&(1<<value) != 0
}

func (capabilities PendingCapabilities) valid() bool {
	return capabilities.bits&^pendingCapabilityMask == 0
}

// PendingComment is an immutable GitHub pending-review comment capture.
type PendingComment struct {
	nodeID       CommentNodeID
	body         string
	target       PendingCommentTarget
	databaseID   CommentDatabaseID
	capabilities PendingCapabilities
}

// PendingCommentDescriptor describes a pending-review comment.
type PendingCommentDescriptor struct {
	NodeID       CommentNodeID
	Body         string
	Target       PendingCommentTarget
	DatabaseID   CommentDatabaseID
	Capabilities PendingCapabilities
}

// NewPendingComment constructs and validates a pending-review comment.
func NewPendingComment(descriptor PendingCommentDescriptor) (PendingComment, error) {
	return newPendingComment(descriptor, clonePendingCapture)
}

// TakePendingComment consumes an already constructed immutable descriptor.
func TakePendingComment(descriptor PendingCommentDescriptor) (PendingComment, error) {
	return newPendingComment(descriptor, takePendingCapture)
}

func newPendingComment(
	descriptor PendingCommentDescriptor,
	ownership pendingCaptureOwnership,
) (PendingComment, error) {
	if !descriptor.Capabilities.valid() {
		return PendingComment{}, ErrInvalidPendingReview
	}
	nonSpace, err := scanCommentText(nil, descriptor.Body)
	if err != nil || !nonSpace {
		return PendingComment{}, ErrInvalidPendingReview
	}
	nodeID := descriptor.NodeID
	databaseID := descriptor.DatabaseID
	target := descriptor.Target
	if ownership == clonePendingCapture {
		var err error
		nodeID, err = NewCommentNodeID(nodeID.String())
		if err != nil {
			return PendingComment{}, err
		}
		databaseID, err = NewCommentDatabaseID(databaseID.Uint64())
		if err != nil {
			return PendingComment{}, err
		}
		target, err = NewPendingCommentTarget(PendingCommentTargetDescriptor{
			ReplyTo: target.replyTo, ReplyDatabase: target.replyDatabase,
			Path: target.path, Commit: target.commit,
			OriginalCommit: target.originalCommit,
			Line:           target.line, StartLine: target.startLine,
			OriginalLine:  target.originalLine,
			OriginalStart: target.originalStart,
			Subject:       target.subject, Side: target.side,
			StartSide: target.startSide, Outdated: target.outdated,
		})
		if err != nil {
			return PendingComment{}, err
		}
		descriptor.Body = strings.Clone(descriptor.Body)
	} else if ownership != takePendingCapture || !validTakenPendingTarget(target) ||
		validatePendingText(nodeID.String()) != nil || databaseID == 0 {
		return PendingComment{}, ErrInvalidPendingReview
	}
	return PendingComment{
		nodeID:       nodeID,
		databaseID:   databaseID,
		body:         descriptor.Body,
		target:       target,
		capabilities: descriptor.Capabilities,
	}, nil
}

func validTakenPendingTarget(target PendingCommentTarget) bool {
	if target.subject < PendingSubjectLine || target.subject > PendingSubjectReply ||
		!validTakenPendingPath(target.path) || !validObjectID(target.commit.String()) ||
		!validObjectID(target.originalCommit.String()) ||
		target.replyTo != "" && validatePendingText(target.replyTo.String()) != nil ||
		!validTakenPendingLine(target.line) || !validTakenPendingLine(target.startLine) ||
		!validTakenPendingLine(target.originalLine) ||
		!validTakenPendingLine(target.originalStart) ||
		target.originalStart != nil &&
			(target.originalLine == nil || *target.originalStart > *target.originalLine) {
		return false
	}
	if target.subject == PendingSubjectReply {
		return target.replyTo != "" || target.replyDatabase != 0
	}
	if target.replyTo != "" || target.replyDatabase != 0 {
		return false
	}
	if target.subject != PendingSubjectLine {
		return true
	}
	return target.line != nil && (target.side == LineOld || target.side == LineNew) &&
		(target.startLine == nil ||
			(target.startSide == LineOld || target.startSide == LineNew) &&
				*target.startLine <= *target.line)
}

func validTakenPendingPath(path Path) bool {
	validated, err := NewPathString(path.raw)
	return err == nil && validated.raw != ""
}

func validTakenPendingLine(line *LineNumber) bool {
	if line == nil {
		return true
	}
	_, err := NewLineNumber(line.Uint32())
	return err == nil
}

// NodeID returns the receiver's node identity.
func (comment PendingComment) NodeID() CommentNodeID { return comment.nodeID }

// DatabaseID returns the receiver's database identity.
func (comment PendingComment) DatabaseID() CommentDatabaseID { return comment.databaseID }

// Body returns the receiver's body.
func (comment PendingComment) Body() string { return comment.body }

// DraftMarker returns the recognized terminal draft-comment marker without
// exposing marker grammar to source adapters.
func (comment PendingComment) DraftMarker() string {
	_, marker := splitPendingMarker(comment.body, draftCommentMarkerPrefix)
	return marker
}

// Target returns the receiver's target.
func (comment PendingComment) Target() PendingCommentTarget { return comment.target }

// ViewerCanUpdate reports whether the authenticated viewer can update the body.
func (comment PendingComment) ViewerCanUpdate() bool {
	return comment.capabilities.Has(PendingCapabilityUpdate)
}

// ViewerCanDelete reports whether the authenticated viewer can delete the comment.
func (comment PendingComment) ViewerCanDelete() bool {
	return comment.capabilities.Has(PendingCapabilityDelete)
}

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
	return newPendingReview(descriptor, clonePendingCapture)
}

// TakePendingReview consumes already constructed immutable descriptor values.
func TakePendingReview(descriptor PendingReviewDescriptor) (PendingReview, error) {
	return newPendingReview(descriptor, takePendingCapture)
}

func newPendingReview(
	descriptor PendingReviewDescriptor,
	ownership pendingCaptureOwnership,
) (PendingReview, error) {
	if _, err := scanCommentText(nil, descriptor.Body); err != nil {
		return PendingReview{}, ErrInvalidPendingReview
	}
	if len(descriptor.Comments) > MaxComments {
		return PendingReview{}, ErrInvalidPendingReview
	}
	nodeID := descriptor.NodeID
	databaseID := descriptor.DatabaseID
	commit := descriptor.Commit
	comments := descriptor.Comments
	if ownership == clonePendingCapture {
		var err error
		nodeID, err = NewReviewNodeID(nodeID.String())
		if err != nil {
			return PendingReview{}, err
		}
		databaseID, err = NewReviewDatabaseID(databaseID.Uint64())
		if err != nil {
			return PendingReview{}, err
		}
		commit, err = NewObjectID(commit.String())
		if err != nil {
			return PendingReview{}, err
		}
		comments = slices.Clone(comments)
		descriptor.Body = strings.Clone(descriptor.Body)
	} else if ownership != takePendingCapture ||
		validatePendingText(nodeID.String()) != nil || databaseID == 0 ||
		!validObjectID(commit.String()) {
		return PendingReview{}, ErrInvalidPendingReview
	}
	seenNodes := make(map[CommentNodeID]struct{}, len(comments))
	seenDatabases := make(map[CommentDatabaseID]struct{}, len(comments))
	bodyRetained := int64(len(descriptor.Body))
	retained := bodyRetained + int64(len(nodeID)+len(commit)+64) +
		int64(cap(comments))*256
	for index, comment := range comments {
		validated, err := newPendingComment(PendingCommentDescriptor{
			NodeID: comment.nodeID, DatabaseID: comment.databaseID, Body: comment.body,
			Target: comment.target, Capabilities: comment.capabilities,
		}, ownership)
		if err != nil {
			return PendingReview{}, err
		}
		comments[index] = validated
		comment = validated
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
				len(comment.target.replyTo),
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
		body:         descriptor.Body,
		comments:     comments,
		viewerUpdate: descriptor.ViewerUpdate,
	}, nil
}

type pendingCaptureOwnership uint8

const (
	clonePendingCapture pendingCaptureOwnership = iota + 1
	takePendingCapture
)

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

func (pending PendingReview) retainedBytes() int64 {
	total, err := pending.retainedBytesCancellable(nil)
	if err != nil {
		panic("nil pending-review cancellation returned an error")
	}
	return total
}

func (pending PendingReview) retainedBytesCancellable(cancelled func() error) (int64, error) {
	total := int64(len(pending.nodeID)+len(pending.commit)+len(pending.body)+64) +
		int64(cap(pending.comments))*256
	for index, comment := range pending.comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return 0, err
			}
		}
		total += int64(
			len(comment.body) + len(comment.nodeID) + len(comment.target.path.raw) +
				len(comment.target.commit) + len(comment.target.originalCommit) +
				len(comment.target.replyTo),
		)
	}
	if err := cancellationError(cancelled); err != nil {
		return 0, err
	}
	return total, nil
}

// RetainedBytes returns the conservative retained size of the pending capture.
func (pending PendingReview) RetainedBytes() int64 { return pending.retainedBytes() }

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
	pending.comments = slices.Clone(pending.comments)
	return snapshot.takePendingReview(pending)
}

// TakePendingReview consumes an already constructed immutable pending review.
func (snapshot Snapshot) TakePendingReview(pending PendingReview) (Snapshot, error) {
	return snapshot.takePendingReview(pending)
}

// PendingReviewInstallationBytes returns a conservative bound for transient
// validation, deduplication, and remote-thread filtering storage.
func (snapshot Snapshot) PendingReviewInstallationBytes(pending PendingReview) int64 {
	remoteComments := 0
	for _, thread := range snapshot.remoteThreads {
		remoteComments += len(thread.comments)
	}
	return int64(len(pending.comments))*192 +
		int64(snapshot.FileCount())*160 +
		int64(len(snapshot.remoteThreads))*224 +
		int64(remoteComments)*128 + 256
}

func (snapshot Snapshot) takePendingReview(pending PendingReview) (Snapshot, error) {
	return snapshot.takePendingReviewCancellable(nil, pending)
}

func (snapshot Snapshot) takePendingReviewCancellable(
	cancelled func() error,
	pending PendingReview,
) (Snapshot, error) {
	if err := cancellationError(cancelled); err != nil {
		return Snapshot{}, err
	}
	pendingRetained := int64(len(pending.nodeID)+len(pending.commit)+len(pending.body)+64) +
		int64(cap(pending.comments))*256
	if pending.nodeID == "" || pending.databaseID == 0 || pending.commit == "" {
		return Snapshot{}, ErrInvalidPendingReview
	}
	if _, err := scanCommentText(cancelled, pending.body); err != nil {
		if err == errInvalidCommentText { //nolint:errorlint // Wrapped cancellation must remain unchanged.
			return Snapshot{}, ErrInvalidPendingReview
		}
		return Snapshot{}, err
	}
	if len(pending.comments) > MaxComments || pendingRetained > MaxPendingReviewBytes {
		return Snapshot{}, ErrInvalidPendingReview
	}
	seenNodes := make(map[CommentNodeID]struct{}, len(pending.comments))
	seenDatabases := make(map[CommentDatabaseID]struct{}, len(pending.comments))
	bodyBytes := int64(len(pending.body))
	for index, comment := range pending.comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return Snapshot{}, err
			}
		}
		if comment.nodeID == "" || comment.databaseID == 0 {
			return Snapshot{}, ErrInvalidPendingReview
		}
		nonSpace, err := scanCommentText(cancelled, comment.body)
		if err != nil {
			if err == errInvalidCommentText { //nolint:errorlint // Wrapped cancellation must remain unchanged.
				return Snapshot{}, ErrInvalidPendingReview
			}
			return Snapshot{}, err
		}
		if !nonSpace || !comment.capabilities.valid() ||
			comment.target.path.raw == "" || comment.target.commit == "" ||
			comment.target.originalCommit == "" {
			return Snapshot{}, ErrInvalidPendingReview
		}
		if _, exists := seenNodes[comment.nodeID]; exists {
			return Snapshot{}, fmt.Errorf("%w: duplicate comment node ID", ErrInvalidPendingReview)
		}
		if _, exists := seenDatabases[comment.databaseID]; exists {
			return Snapshot{}, fmt.Errorf("%w: duplicate comment database ID", ErrInvalidPendingReview)
		}
		seenNodes[comment.nodeID] = struct{}{}
		seenDatabases[comment.databaseID] = struct{}{}
		bodyBytes += int64(len(comment.body))
		pendingRetained += int64(
			len(comment.body) + len(comment.nodeID) + len(comment.target.path.raw) +
				len(comment.target.commit) + len(comment.target.originalCommit) +
				len(comment.target.replyTo),
		)
		if pendingRetained > MaxPendingReviewBytes {
			return Snapshot{}, ErrInvalidPendingReview
		}
	}
	if bodyBytes > MaxCommentBodyTotal {
		return Snapshot{}, ErrInvalidPendingReview
	}
	next := snapshot
	next.pendingReview = new(pending)
	next.pendingReviewStatus = PendingReviewReady
	next.pendingReviewDetail = EmptyOperationDetail()
	var err error
	next, err = next.stagePendingRetainedBytes(pendingRetained, ErrInvalidPendingReview)
	if err != nil {
		return Snapshot{}, err
	}
	if len(snapshot.remoteThreads) != 0 {
		remoteStatus := snapshot.remoteThreadStatus
		remoteDetail := snapshot.remoteThreadDetail
		next, err = next.withoutPendingRemoteCommentsCancellable(cancelled, pending)
		if err != nil {
			return Snapshot{}, err
		}
		if remoteStatus == RemoteThreadsUnavailable || remoteStatus == RemoteThreadsLimited {
			next, err = next.WithRemoteThreadFailure(remoteStatus, remoteDetail)
			if err != nil {
				return Snapshot{}, err
			}
		}
		return next, nil
	}
	next, err = next.validateRetainedBytes(ErrInvalidPendingReview)
	if err != nil {
		return Snapshot{}, err
	}
	if err := cancellationError(cancelled); err != nil {
		return Snapshot{}, err
	}
	return next, nil
}

func (snapshot Snapshot) withoutPendingRemoteCommentsCancellable(
	cancelled func() error,
	pending PendingReview,
) (Snapshot, error) {
	pendingNodes := make(map[string]struct{}, len(pending.comments))
	for index, comment := range pending.comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return Snapshot{}, err
			}
		}
		pendingNodes[comment.nodeID.String()] = struct{}{}
	}
	filtered := make([]RemoteThread, 0, len(snapshot.remoteThreads))
	commentsInspected := 0
	for index, thread := range snapshot.remoteThreads {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return Snapshot{}, err
			}
		}
		comments := make([]RemoteComment, 0, len(thread.comments))
		for _, comment := range thread.comments {
			if commentsInspected%256 == 0 {
				if err := cancellationError(cancelled); err != nil {
					return Snapshot{}, err
				}
			}
			commentsInspected++
			if _, pendingComment := pendingNodes[comment.id]; !pendingComment {
				comments = append(comments, comment)
			}
		}
		if len(comments) == 0 {
			continue
		}
		retained := thread
		retained.comments = comments
		filtered = append(filtered, retained)
	}
	return snapshot.withRemoteThreadsCancellable(cancelled, filtered, takeRemoteThreads)
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
	next := snapshot
	next.pendingReview = nil
	next.pendingReviewStatus = status
	next.pendingReviewDetail = detail
	return next.withPendingRetainedBytes(detail.Bytes(), ErrInvalidPendingReview)
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

func validatePendingText(value string) error {
	if strings.TrimSpace(value) == "" || int64(len(value)) > MaxPathBytes || !utf8.ValidString(value) {
		return errors.New("value is empty, too long, or invalid UTF-8")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("value contains a control character")
		}
	}
	return nil
}
