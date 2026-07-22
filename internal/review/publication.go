package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxPublicationBytes defines the supported publication bytes limit.
const MaxPublicationBytes = 16 << 20

// ErrInvalidPublication identifies an invalid publication failure.
var ErrInvalidPublication = errors.New("invalid publication transition")

// PublicationEvent describes publication event.
type PublicationEvent uint8

// PublicationEventComment identifies a supported publication event comment value.
const (
	PublicationEventComment PublicationEvent = iota + 1
	PublicationEventDraft
	PublicationEventApprove
	PublicationEventRequestChanges
)

// String returns the receiver's string value.
func (event PublicationEvent) String() string {
	switch event {
	case PublicationEventComment:
		return publicationCommentValue
	case PublicationEventDraft:
		return draftValue
	case PublicationEventApprove:
		return "approve"
	case PublicationEventRequestChanges:
		return "request changes"
	default:
		return invalidValue
	}
}

// GitHubValue returns the receiver's git hub value value.
func (event PublicationEvent) GitHubValue() string {
	switch event {
	case PublicationEventComment:
		return "COMMENT"
	case PublicationEventDraft:
		return ""
	case PublicationEventApprove:
		return "APPROVE"
	case PublicationEventRequestChanges:
		return "REQUEST_CHANGES"
	}
	return ""
}

// PublicationPolicy represents publication policy.
type PublicationPolicy uint8

// PublicationMoveUnmappableToSummary identifies a supported publication move unmappable to summary value.
const (
	PublicationMoveUnmappableToSummary PublicationPolicy = iota + 1
	PublicationAllowStaleHead
)

// PublicationPolicies represents publication policies.
type PublicationPolicies struct {
	bits uint8
}

// NewPublicationPolicies constructs and validates publication policies.
func NewPublicationPolicies(values ...PublicationPolicy) (PublicationPolicies, error) {
	var policies PublicationPolicies
	for _, value := range values {
		if value < PublicationMoveUnmappableToSummary || value > PublicationAllowStaleHead {
			return PublicationPolicies{}, fmt.Errorf(
				"%w: unknown policy %d",
				ErrInvalidPublication,
				value,
			)
		}
		policies.bits |= 1 << (value - 1)
	}
	return policies, nil
}

// Has performs the has operation.
func (policies PublicationPolicies) Has(value PublicationPolicy) bool {
	return value >= PublicationMoveUnmappableToSummary && value <= PublicationAllowStaleHead &&
		policies.bits&(1<<(value-1)) != 0
}

func (policies PublicationPolicies) valid() bool {
	const validBits = 1<<PublicationAllowStaleHead - 1
	return policies.bits&^validBits == 0
}

// PublicationOptions describes publication options.
type PublicationOptions struct {
	excluded []CommentID
	event    PublicationEvent
	policies PublicationPolicies
}

// NewPublicationOptions constructs and validates publication options.
func NewPublicationOptions(
	event PublicationEvent,
	policies PublicationPolicies,
) (PublicationOptions, error) {
	options := PublicationOptions{event: event, policies: policies}
	if !options.valid() {
		return PublicationOptions{}, ErrInvalidPublication
	}
	return options, nil
}

// WithExcludedComments performs the with excluded comments operation.
func (options PublicationOptions) WithExcludedComments(
	comments []CommentID,
) (PublicationOptions, error) {
	if !options.valid() || len(comments) > MaxComments {
		return PublicationOptions{}, fmt.Errorf(
			"%w: excluded comments exceed %d",
			ErrInvalidPublication,
			MaxComments,
		)
	}
	owned := slices.Clone(comments)
	slices.Sort(owned)
	for index, id := range owned {
		if id.Uint32() >= MaxComments {
			return PublicationOptions{}, fmt.Errorf(
				"%w: excluded comment identity is invalid",
				ErrInvalidPublication,
			)
		}
		if index == 0 {
			continue
		}
		if owned[index] == owned[index-1] {
			return PublicationOptions{}, fmt.Errorf(
				"%w: excluded comments contain a duplicate",
				ErrInvalidPublication,
			)
		}
	}
	options.excluded = owned
	return options, nil
}

// ExcludedComments returns the receiver's excluded comments value.
func (options PublicationOptions) ExcludedComments() []CommentID {
	return slices.Clone(options.excluded)
}

// Event returns the receiver's event value.
func (options PublicationOptions) Event() PublicationEvent {
	return options.event
}

// Policies returns the receiver's policies value.
func (options PublicationOptions) Policies() PublicationPolicies {
	return options.policies
}

func (options PublicationOptions) valid() bool {
	return options.event >= PublicationEventComment &&
		options.event <= PublicationEventRequestChanges &&
		options.policies.valid()
}

// PublicationTarget represents publication target.
type PublicationTarget struct {
	repository  string
	base        ObjectID
	head        ObjectID
	pullRequest uint64
}

// PublicationTargetDescriptor describes publication target descriptor.
type PublicationTargetDescriptor struct {
	Repository  string
	Base        ObjectID
	Head        ObjectID
	PullRequest uint64
}

// NewPublicationTarget constructs and validates publication target.
func NewPublicationTarget(descriptor PublicationTargetDescriptor) (PublicationTarget, error) {
	base, baseErr := NewObjectID(descriptor.Base.String())
	head, headErr := NewObjectID(descriptor.Head.String())
	if !validPublicationRepository(descriptor.Repository) ||
		descriptor.PullRequest == 0 || baseErr != nil || headErr != nil ||
		len(base.String()) != len(head.String()) ||
		len(descriptor.Repository) > 512 ||
		!utf8.ValidString(descriptor.Repository) ||
		strings.IndexByte(descriptor.Repository, 0) >= 0 {
		return PublicationTarget{}, fmt.Errorf("%w: target is invalid", ErrInvalidPublication)
	}
	for _, character := range descriptor.Repository {
		if unicode.IsControl(character) {
			return PublicationTarget{}, fmt.Errorf(
				"%w: repository contains a control character",
				ErrInvalidPublication,
			)
		}
	}
	return PublicationTarget{
		repository:  descriptor.Repository,
		pullRequest: descriptor.PullRequest,
		base:        base,
		head:        head,
	}, nil
}

func validPublicationRepository(value string) bool {
	owner, repository, ok := strings.Cut(value, "/")
	if !ok || owner == "" || repository == "" || strings.Contains(repository, "/") ||
		strings.HasPrefix(owner, "-") || strings.HasSuffix(owner, "-") ||
		repository == "." || repository == ".." {
		return false
	}
	for _, character := range owner {
		if !asciiLetterOrDigit(character) && character != '-' {
			return false
		}
	}
	for _, character := range repository {
		if !asciiLetterOrDigit(character) &&
			character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func asciiLetterOrDigit(value rune) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9'
}

// Repository returns the receiver's repository value.
func (target PublicationTarget) Repository() string {
	return target.repository
}

// PullRequest returns the receiver's pull request value.
func (target PublicationTarget) PullRequest() uint64 {
	return target.pullRequest
}

// Base returns the receiver's base value.
func (target PublicationTarget) Base() ObjectID {
	return target.base
}

// Head returns the receiver's head value.
func (target PublicationTarget) Head() ObjectID {
	return target.head
}

// PublicationComment represents publication comment.
type PublicationComment struct {
	path      string
	body      string
	side      LineSide
	startLine LineNumber
	endLine   LineNumber
}

// Path returns the receiver's path value.
func (comment PublicationComment) Path() string {
	return comment.path
}

// Body returns the receiver's body value.
func (comment PublicationComment) Body() string {
	return comment.body
}

// Side returns the receiver's side value.
func (comment PublicationComment) Side() LineSide {
	return comment.side
}

// Lines returns the receiver's lines value.
func (comment PublicationComment) Lines() (LineNumber, LineNumber) {
	return comment.startLine, comment.endLine
}

// PublicationDecision represents publication decision.
type PublicationDecision uint8

// PublicationIncludedInline identifies a supported publication included inline value.
const (
	PublicationIncludedInline PublicationDecision = iota + 1
	PublicationIncludedSummary
	PublicationExcludedManual
	PublicationExcludedStale
	PublicationExcludedUnmappable
	PublicationExcludedInvalid
)

// String returns the receiver's string value.
func (decision PublicationDecision) String() string {
	switch decision {
	case PublicationIncludedInline:
		return inlineValue
	case PublicationIncludedSummary:
		return "summary"
	case PublicationExcludedManual:
		return "excluded manually"
	case PublicationExcludedStale:
		return "excluded stale"
	case PublicationExcludedUnmappable:
		return "excluded unmappable"
	case PublicationExcludedInvalid:
		return "excluded invalid"
	default:
		return invalidValue
	}
}

// PublicationCommentDecision represents publication comment decision.
type PublicationCommentDecision struct {
	id       CommentID
	decision PublicationDecision
}

// CommentID returns the receiver's comment id value.
func (decision PublicationCommentDecision) CommentID() CommentID {
	return decision.id
}

// Decision returns the receiver's decision value.
func (decision PublicationCommentDecision) Decision() PublicationDecision {
	return decision.decision
}

// PreparedPublication represents prepared publication.
type PreparedPublication struct {
	pending   *PreparedPendingPublication
	summary   string
	comments  []PublicationComment
	decisions []PublicationCommentDecision
	options   PublicationOptions
	workspace WorkspaceRevision
	source    SourceID
	included  uint32
	excluded  uint32
}

// PreparePublication prepares publication.
func (review Review) PreparePublication(options PublicationOptions) (PreparedPublication, error) {
	if !options.valid() {
		return PreparedPublication{}, ErrInvalidPublication
	}
	if review.pending != nil {
		return review.preparePendingPublication(options)
	}
	status := review.snapshot.PendingReviewSummary().Status
	if status == PendingReviewUnavailable || status == PendingReviewLimited ||
		status == PendingReviewAmbiguous {
		return PreparedPublication{}, fmt.Errorf(
			"%w: pending GitHub review is %s",
			ErrInvalidPublication,
			status,
		)
	}
	prepared := PreparedPublication{
		workspace: review.workspace,
		source:    review.snapshot.SourceID(),
		options:   options,
		comments:  make([]PublicationComment, 0, len(review.comments)),
		decisions: make([]PublicationCommentDecision, 0, len(review.comments)),
	}
	for _, comment := range review.comments {
		if _, excluded := slices.BinarySearch(options.excluded, comment.ID()); excluded {
			prepared.excluded++
			prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
				id:       comment.ID(),
				decision: PublicationExcludedManual,
			})
			continue
		}
		body := comment.Body().String()
		target := comment.Target()
		if target.Kind() == TargetLine && !comment.Stale() && !comment.Unmappable() {
			path, hasPath := target.AnchoredPath()
			pathUTF8, validUTF8 := path.UTF8()
			side, start, end, hasLines := target.Lines()
			if hasPath && validUTF8 && hasLines {
				prepared.comments = append(prepared.comments, PublicationComment{
					path:      pathUTF8,
					body:      body,
					side:      side,
					startLine: start,
					endLine:   end,
				})
				prepared.included++
				prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
					id:       comment.ID(),
					decision: PublicationIncludedInline,
				})
				continue
			}
		}
		if target.Kind() == TargetReview || target.Kind() == TargetFile ||
			options.Policies().Has(PublicationMoveUnmappableToSummary) {
			if target.Kind() != TargetReview {
				if path, ok := target.AnchoredPath(); ok {
					body = "`" + markdownEscape(path.Display()) + "`: " + body
				}
			}
			if prepared.summary != "" {
				prepared.summary += "\n\n"
			}
			prepared.summary += body
			prepared.included++
			prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
				id:       comment.ID(),
				decision: PublicationIncludedSummary,
			})
		} else {
			prepared.excluded++
			decision := PublicationExcludedInvalid
			if comment.Stale() {
				decision = PublicationExcludedStale
			} else if comment.Unmappable() {
				decision = PublicationExcludedUnmappable
			}
			prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
				id:       comment.ID(),
				decision: decision,
			})
		}
		if len(prepared.summary) > MaxPublicationBytes {
			return PreparedPublication{}, fmt.Errorf(
				"%w: prepared summary exceeds %d bytes",
				ErrInvalidPublication,
				MaxPublicationBytes,
			)
		}
	}
	var retained = int64(len(prepared.summary))
	for _, comment := range prepared.comments {
		retained += int64(len(comment.path) + len(comment.body) + 32)
	}
	if retained > MaxPublicationBytes {
		return PreparedPublication{}, fmt.Errorf(
			"%w: prepared publication exceeds %d bytes",
			ErrInvalidPublication,
			MaxPublicationBytes,
		)
	}
	return prepared, nil
}

// Workspace returns the receiver's workspace value.
func (prepared PreparedPublication) Workspace() WorkspaceRevision {
	return prepared.workspace
}

// Source returns the receiver's source value.
func (prepared PreparedPublication) Source() SourceID {
	return prepared.source
}

// Options returns the receiver's options value.
func (prepared PreparedPublication) Options() PublicationOptions {
	return prepared.options
}

// SummaryBody returns the receiver's summary body value.
func (prepared PreparedPublication) SummaryBody() string {
	return prepared.summary
}

// CommentCount returns the receiver's comment count value.
func (prepared PreparedPublication) CommentCount() int {
	return len(prepared.comments)
}

// Comments performs the comments operation.
func (prepared PreparedPublication) Comments(start, count int) []PublicationComment {
	if start < 0 || start >= len(prepared.comments) || count <= 0 || count > 256 {
		return nil
	}
	return slices.Clone(prepared.comments[start:min(start+count, len(prepared.comments))])
}

// IncludedCount returns the receiver's included count value.
func (prepared PreparedPublication) IncludedCount() uint32 {
	return prepared.included
}

// ExcludedCount returns the receiver's excluded count value.
func (prepared PreparedPublication) ExcludedCount() uint32 {
	return prepared.excluded
}

// DecisionAt performs the decision at operation.
func (prepared PreparedPublication) DecisionAt(
	index int,
) (PublicationCommentDecision, bool) {
	if index < 0 || index >= len(prepared.decisions) {
		return PublicationCommentDecision{}, false
	}
	return prepared.decisions[index], true
}

// PublicationMarker represents publication marker.
type PublicationMarker string

// NewPublicationMarker constructs and validates publication marker.
func NewPublicationMarker(value string) (PublicationMarker, error) {
	const prefix = "<!-- rvw-publication:v1:"
	const suffix = " -->"
	if len(value) != len(prefix)+32+len(suffix) ||
		!strings.HasPrefix(value, prefix) ||
		!strings.HasSuffix(value, suffix) {
		return "", fmt.Errorf("%w: marker has invalid shape", ErrInvalidPublication)
	}
	for _, value := range value[len(prefix) : len(prefix)+32] {
		if value < '0' || value > '9' && value < 'a' || value > 'f' {
			return "", fmt.Errorf("%w: marker is not lowercase hexadecimal", ErrInvalidPublication)
		}
	}
	return PublicationMarker(value), nil
}

// String returns the receiver's string value.
func (marker PublicationMarker) String() string {
	return string(marker)
}

// CheckedPublication represents checked publication.
type CheckedPublication struct {
	target    PublicationTarget
	marker    PublicationMarker
	payload   string
	prepared  PreparedPublication
	staleHead bool
}

// CheckedPublicationDescriptor describes checked publication descriptor.
type CheckedPublicationDescriptor struct {
	Marker    PublicationMarker
	Target    PublicationTarget
	StaleHead bool
}

// TakeCheckedPublication takes ownership of checked publication.
func TakeCheckedPublication(
	descriptor CheckedPublicationDescriptor,
	prepared PreparedPublication,
) (*CheckedPublication, error) {
	marker, markerErr := NewPublicationMarker(descriptor.Marker.String())
	target, targetErr := NewPublicationTarget(PublicationTargetDescriptor{
		Repository:  descriptor.Target.repository,
		PullRequest: descriptor.Target.pullRequest,
		Base:        descriptor.Target.base,
		Head:        descriptor.Target.head,
	})
	if targetErr != nil || markerErr != nil ||
		prepared.workspace == 0 || prepared.source == 0 ||
		!prepared.options.valid() {
		return nil, fmt.Errorf("%w: checked publication is malformed", ErrInvalidPublication)
	}
	var payload string
	var err error
	if prepared.pending != nil {
		payload, err = encodePendingPublicationEnvelope(marker, prepared)
	} else {
		payload, err = encodePublicationPayload(target, marker, prepared)
	}
	if err != nil {
		return nil, fmt.Errorf(
			"%w: encode checked publication: %w",
			ErrInvalidPublication,
			err,
		)
	}
	return &CheckedPublication{
		target:    target,
		marker:    marker,
		payload:   payload,
		prepared:  prepared,
		staleHead: descriptor.StaleHead,
	}, nil
}

// Pending reports whether this publication continues an existing pending review.
func (checked *CheckedPublication) Pending() bool {
	return checked != nil && checked.prepared.pending != nil
}

type publicationPayload struct {
	CommitID string               `json:"commit_id"`
	Event    string               `json:"event,omitempty"`
	Body     string               `json:"body"`
	Comments []publicationComment `json:"comments"`
}

type publicationComment struct {
	Path      string `json:"path"`
	Body      string `json:"body"`
	Side      string `json:"side"`
	StartSide string `json:"start_side,omitempty"`
	Line      uint32 `json:"line"`
	StartLine uint32 `json:"start_line,omitempty"`
}

func encodePublicationPayload(
	target PublicationTarget,
	marker PublicationMarker,
	prepared PreparedPublication,
) (string, error) {
	body := prepared.summary
	if body != "" {
		body += "\n\n"
	}
	body += marker.String()
	payload := publicationPayload{
		CommitID: target.head.String(),
		Event:    prepared.options.event.GitHubValue(),
		Body:     body,
		Comments: make([]publicationComment, 0, len(prepared.comments)),
	}
	for _, comment := range prepared.comments {
		side := "RIGHT"
		if comment.side == LineOld {
			side = "LEFT"
		}
		item := publicationComment{
			Path: comment.path,
			Body: comment.body,
			Line: comment.endLine.Uint32(),
			Side: side,
		}
		if comment.startLine != comment.endLine {
			item.StartLine = comment.startLine.Uint32()
			item.StartSide = side
		}
		payload.Comments = append(payload.Comments, item)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if len(encoded) > MaxPublicationBytes {
		return "", fmt.Errorf("payload exceeds %d bytes", MaxPublicationBytes)
	}
	return string(encoded), nil
}

// Target returns the receiver's target value.
func (checked *CheckedPublication) Target() PublicationTarget {
	if checked == nil {
		return PublicationTarget{}
	}
	return checked.target
}

// Marker returns the receiver's marker value.
func (checked *CheckedPublication) Marker() PublicationMarker {
	if checked == nil {
		return ""
	}
	return checked.marker
}

// Payload returns the receiver's payload value.
func (checked *CheckedPublication) Payload() string {
	if checked == nil {
		return ""
	}
	return checked.payload
}

// PayloadSize returns the receiver's payload size value.
func (checked *CheckedPublication) PayloadSize() int64 {
	if checked == nil {
		return 0
	}
	return int64(len(checked.payload))
}

// Prepared prepares d.
func (checked *CheckedPublication) Prepared() PreparedPublication {
	if checked == nil {
		return PreparedPublication{}
	}
	return checked.prepared
}

// StaleHead reports the receiver's stale head state.
func (checked *CheckedPublication) StaleHead() bool {
	return checked != nil && checked.staleHead
}

// PublicationStatus describes publication status.
type PublicationStatus uint8

// PublicationIdle identifies a supported publication idle value.
const (
	PublicationIdle PublicationStatus = iota + 1
	PublicationChecking
	PublicationChecked
	PublicationPublishing
	PublicationCommitted
	PublicationFailedBeforeCommit
	PublicationUnknown
	PublicationReconciling
	PublicationConfirmedAbsent
)

// String returns the receiver's string value.
func (status PublicationStatus) String() string {
	switch status {
	case PublicationIdle:
		return "idle"
	case PublicationChecking:
		return "checking"
	case PublicationChecked:
		return "checked"
	case PublicationPublishing:
		return "publishing"
	case PublicationCommitted:
		return "committed"
	case PublicationFailedBeforeCommit:
		return "failed-before-commit"
	case PublicationUnknown:
		return "unknown"
	case PublicationReconciling:
		return "reconciling"
	case PublicationConfirmedAbsent:
		return "confirmed-absent"
	default:
		return invalidValue
	}
}

type publicationState struct {
	checked      *CheckedPublication
	marker       PublicationMarker
	detail       OperationDetail
	target       PublicationTarget
	operation    OperationID
	attempt      OperationID
	remoteReview uint64
	included     uint32
	excluded     uint32
	status       PublicationStatus
	event        PublicationEvent
	acknowledged bool
}

// PublicationSummary describes publication summary.
type PublicationSummary struct {
	Marker       PublicationMarker
	Detail       OperationDetail
	Target       PublicationTarget
	Operation    OperationID
	RemoteReview uint64
	Included     uint32
	Excluded     uint32
	Status       PublicationStatus
	Event        PublicationEvent
	StaleHead    bool
	Acknowledged bool
}

// PublicationCheckRequest describes publication check request.
type PublicationCheckRequest struct {
	Options   PublicationOptions
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
}

// PublicationCheckResult describes publication check result.
type PublicationCheckResult struct {
	Checked   *CheckedPublication
	Detail    OperationDetail
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
}

// StartPublicationCheck performs the start publication check operation.
func (review Review) StartPublicationCheck(
	id OperationID,
	options PublicationOptions,
) (Review, PublicationCheckRequest, error) {
	if !options.valid() || review.draft != nil ||
		review.publication.status == PublicationChecking ||
		review.publication.status == PublicationPublishing ||
		review.publication.status == PublicationUnknown ||
		review.publication.status == PublicationReconciling {
		return review, PublicationCheckRequest{}, ErrInvalidPublication
	}
	next, err := review.BeginOperation(id, OperationPublication)
	if err != nil {
		return review, PublicationCheckRequest{}, err
	}
	next.publication = publicationState{
		status:    PublicationChecking,
		operation: id,
		event:     options.event,
	}
	return next, PublicationCheckRequest{
		Operation: id,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
		Options:   options,
	}, nil
}

// ApplyPublicationCheck performs the apply publication check operation.
func (review Review) ApplyPublicationCheck(result PublicationCheckResult) (Review, error) {
	if review.publication.status != PublicationChecking ||
		review.publication.operation != result.Operation ||
		review.workspace != result.Workspace ||
		review.snapshot.SourceID() != result.Source {
		return review, ErrInvalidPublication
	}
	next := review.clone()
	if result.Checked == nil {
		if result.Detail.IsEmpty() {
			return review, ErrInvalidPublication
		}
		next.publication = publicationState{
			status:    PublicationFailedBeforeCommit,
			operation: result.Operation,
			detail:    result.Detail,
			event:     review.publication.event,
		}
		return next, nil
	}
	if result.Checked.prepared.workspace != result.Workspace ||
		result.Checked.prepared.source != result.Source {
		return review, ErrInvalidPublication
	}
	next.publication = publicationState{
		status:    PublicationChecked,
		operation: result.Operation,
		checked:   result.Checked,
		target:    result.Checked.target,
		marker:    result.Checked.marker,
		event:     result.Checked.prepared.options.event,
		included:  result.Checked.prepared.included,
		excluded:  result.Checked.prepared.excluded,
	}
	return next, nil
}

// PublicationRequest describes publication request.
type PublicationRequest struct {
	Checked   *CheckedPublication
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
}

// StartPublication performs the start publication operation.
func (review Review) StartPublication(
	id OperationID,
) (Review, PublicationRequest, error) {
	if review.publication.status != PublicationChecked ||
		review.publication.checked == nil {
		return review, PublicationRequest{}, ErrInvalidPublication
	}
	next, err := review.BeginOperation(id, OperationPublication)
	if err != nil {
		return review, PublicationRequest{}, err
	}
	next.publication.status = PublicationPublishing
	next.publication.operation = id
	next.publication.attempt = id
	return next, PublicationRequest{
		Operation: id,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
		Checked:   review.publication.checked,
	}, nil
}

// PublicationDisposition represents publication disposition.
type PublicationDisposition uint8

// PublicationDispositionCommitted identifies a supported publication disposition committed value.
const (
	PublicationDispositionCommitted PublicationDisposition = iota + 1
	PublicationDispositionFailedBeforeCommit
	PublicationDispositionUnknown
	PublicationDispositionCancelledBeforeCommit
)

// PublicationResult describes publication result.
type PublicationResult struct {
	Pending      *PendingReview
	Detail       OperationDetail
	Operation    OperationID
	Workspace    WorkspaceRevision
	Source       SourceID
	RemoteReview uint64
	Disposition  PublicationDisposition
}

// ApplyPublication performs the apply publication operation.
func (review Review) ApplyPublication(result PublicationResult) (Review, error) {
	if review.publication.status != PublicationPublishing ||
		review.publication.operation != result.Operation ||
		review.workspace != result.Workspace ||
		review.snapshot.SourceID() != result.Source ||
		result.Disposition < PublicationDispositionCommitted ||
		result.Disposition > PublicationDispositionCancelledBeforeCommit {
		return review, ErrInvalidPublication
	}
	next := review.clone()
	next.publication.operation = result.Operation
	next.publication.attempt = result.Operation
	next.publication.detail = result.Detail
	next.publication.remoteReview = result.RemoteReview
	next.publication.checked = nil
	switch result.Disposition {
	case PublicationDispositionCommitted:
		if result.RemoteReview == 0 {
			return review, fmt.Errorf("%w: committed result has no remote identity", ErrInvalidPublication)
		}
		if result.Pending != nil {
			if result.Pending.databaseID.Uint64() != result.RemoteReview || next.pending == nil {
				return review, fmt.Errorf("%w: committed pending review is inconsistent", ErrInvalidPublication)
			}
			rebased, err := rebasePendingCapture(next, *result.Pending)
			if err != nil {
				return review, fmt.Errorf("%w: rebase committed pending review: %w", ErrInvalidPublication, err)
			}
			next = rebased
		}
		next.publication.status = PublicationCommitted
	case PublicationDispositionFailedBeforeCommit, PublicationDispositionCancelledBeforeCommit:
		next.publication.status = PublicationFailedBeforeCommit
	case PublicationDispositionUnknown:
		next.publication.status = PublicationUnknown
	}
	return next, nil
}

// ReconciliationRequest describes reconciliation request.
type ReconciliationRequest struct {
	Marker    PublicationMarker
	Target    PublicationTarget
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
}

// StartReconciliation performs the start reconciliation operation.
func (review Review) StartReconciliation(
	id OperationID,
) (Review, ReconciliationRequest, error) {
	if review.publication.status != PublicationUnknown ||
		review.publication.marker == "" ||
		review.publication.target.repository == "" {
		return review, ReconciliationRequest{}, ErrInvalidPublication
	}
	next, err := review.BeginOperation(id, OperationReconciliation)
	if err != nil {
		return review, ReconciliationRequest{}, err
	}
	next.publication.status = PublicationReconciling
	next.publication.operation = id
	return next, ReconciliationRequest{
		Operation: id,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
		Target:    review.publication.target,
		Marker:    review.publication.marker,
	}, nil
}

// ReconciliationDisposition represents reconciliation disposition.
type ReconciliationDisposition uint8

// ReconciliationCommitted identifies a supported reconciliation committed value.
const (
	ReconciliationCommitted ReconciliationDisposition = iota + 1
	ReconciliationNotCommitted
	ReconciliationStillUnknown
)

// ReconciliationResult describes reconciliation result.
type ReconciliationResult struct {
	Detail      OperationDetail
	Operation   OperationID
	Workspace   WorkspaceRevision
	Source      SourceID
	Disposition ReconciliationDisposition
}

// ApplyReconciliation performs the apply reconciliation operation.
func (review Review) ApplyReconciliation(result ReconciliationResult) (Review, error) {
	if review.publication.status != PublicationReconciling ||
		review.publication.operation != result.Operation ||
		review.workspace != result.Workspace ||
		review.snapshot.SourceID() != result.Source ||
		result.Disposition < ReconciliationCommitted ||
		result.Disposition > ReconciliationStillUnknown {
		return review, ErrInvalidPublication
	}
	next := review.clone()
	next.publication.operation = result.Operation
	next.publication.detail = result.Detail
	switch result.Disposition {
	case ReconciliationCommitted:
		next.publication.status = PublicationCommitted
	case ReconciliationNotCommitted:
		next.publication.status = PublicationConfirmedAbsent
	case ReconciliationStillUnknown:
		next.publication.status = PublicationUnknown
	}
	return next, nil
}

// CancelPublicationCheck returns the receiver's cancel publication check value.
func (review Review) CancelPublicationCheck() (Review, error) {
	if review.publication.status != PublicationChecked {
		return review, ErrInvalidPublication
	}
	next := review.clone()
	next.publication = publicationState{status: PublicationIdle}
	return next, nil
}

// AcknowledgePublication returns the receiver's acknowledge publication value.
func (review Review) AcknowledgePublication() (Review, error) {
	if review.publication.status == PublicationUnknown ||
		review.publication.status == PublicationReconciling ||
		review.publication.status == PublicationIdle {
		return review, ErrInvalidPublication
	}
	next := review.clone()
	next.publication.acknowledged = true
	return next, nil
}

// PublicationSummary returns the receiver's publication summary value.
func (review Review) PublicationSummary() PublicationSummary {
	summary := PublicationSummary{
		Status:       review.publication.status,
		Operation:    review.publication.operation,
		Target:       review.publication.target,
		Marker:       review.publication.marker,
		RemoteReview: review.publication.remoteReview,
		Detail:       review.publication.detail,
		Acknowledged: review.publication.acknowledged,
		Event:        review.publication.event,
		Included:     review.publication.included,
		Excluded:     review.publication.excluded,
	}
	if review.publication.checked != nil {
		summary.StaleHead = review.publication.checked.staleHead
	}
	return summary
}

// UnknownPublicationOperation returns the receiver's unknown publication operation value.
func (review Review) UnknownPublicationOperation() (OperationID, bool) {
	return review.publication.attempt,
		review.publication.attempt != 0 &&
			(review.publication.status == PublicationUnknown ||
				review.publication.status == PublicationReconciling)
}

// PublicationPreflightBody returns the receiver's publication preflight body value.
func (review Review) PublicationPreflightBody() string {
	if review.publication.checked == nil {
		return ""
	}
	return review.publication.checked.prepared.summary
}

// PublicationPreflightPayload returns the receiver's publication preflight payload value.
func (review Review) PublicationPreflightPayload() string {
	if review.publication.checked == nil {
		return ""
	}
	return review.publication.checked.payload
}

// PublicationDecisionAt performs the publication decision at operation.
func (review Review) PublicationDecisionAt(
	index int,
) (PublicationCommentDecision, bool) {
	if review.publication.checked == nil {
		return PublicationCommentDecision{}, false
	}
	return review.publication.checked.prepared.DecisionAt(index)
}
