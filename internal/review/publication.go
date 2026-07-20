package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxPublicationBytes = 16 << 20

var ErrInvalidPublication = errors.New("invalid publication transition")

type PublicationEvent uint8

const (
	PublicationEventComment PublicationEvent = iota + 1
	PublicationEventDraft
	PublicationEventApprove
	PublicationEventRequestChanges
)

func (event PublicationEvent) String() string {
	switch event {
	case PublicationEventComment:
		return "comment"
	case PublicationEventDraft:
		return "draft"
	case PublicationEventApprove:
		return "approve"
	case PublicationEventRequestChanges:
		return "request changes"
	default:
		return "invalid"
	}
}

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
	default:
		return ""
	}
}

type PublicationPolicy uint8

const (
	PublicationPrefixKinds PublicationPolicy = iota + 1
	PublicationMoveUnmappableToSummary
	PublicationAllowStaleHead
)

type PublicationPolicies struct {
	bits uint8
}

func NewPublicationPolicies(values ...PublicationPolicy) (PublicationPolicies, error) {
	var policies PublicationPolicies
	for _, value := range values {
		if value < PublicationPrefixKinds || value > PublicationAllowStaleHead {
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

func (policies PublicationPolicies) Has(value PublicationPolicy) bool {
	return value >= PublicationPrefixKinds && value <= PublicationAllowStaleHead &&
		policies.bits&(1<<(value-1)) != 0
}

func (policies PublicationPolicies) valid() bool {
	const validBits = 1<<PublicationAllowStaleHead - 1
	return policies.bits&^validBits == 0
}

type PublicationOptions struct {
	event    PublicationEvent
	policies PublicationPolicies
	excluded []CommentID
}

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

func (options PublicationOptions) ExcludedComments() []CommentID {
	return slices.Clone(options.excluded)
}

func (options PublicationOptions) Event() PublicationEvent {
	return options.event
}

func (options PublicationOptions) Policies() PublicationPolicies {
	return options.policies
}

func (options PublicationOptions) valid() bool {
	return options.event >= PublicationEventComment &&
		options.event <= PublicationEventRequestChanges &&
		options.policies.valid()
}

type PublicationTarget struct {
	repository  string
	pullRequest uint64
	base        ObjectID
	head        ObjectID
}

type PublicationTargetDescriptor struct {
	Repository  string
	PullRequest uint64
	Base        ObjectID
	Head        ObjectID
}

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

func (target PublicationTarget) Repository() string {
	return target.repository
}

func (target PublicationTarget) PullRequest() uint64 {
	return target.pullRequest
}

func (target PublicationTarget) Base() ObjectID {
	return target.base
}

func (target PublicationTarget) Head() ObjectID {
	return target.head
}

type PublicationComment struct {
	path      string
	body      string
	side      LineSide
	startLine LineNumber
	endLine   LineNumber
}

func (comment PublicationComment) Path() string {
	return comment.path
}

func (comment PublicationComment) Body() string {
	return comment.body
}

func (comment PublicationComment) Side() LineSide {
	return comment.side
}

func (comment PublicationComment) Lines() (LineNumber, LineNumber) {
	return comment.startLine, comment.endLine
}

type PublicationDecision uint8

const (
	PublicationIncludedInline PublicationDecision = iota + 1
	PublicationIncludedSummary
	PublicationExcludedManual
	PublicationExcludedStale
	PublicationExcludedUnmappable
	PublicationExcludedInvalid
)

func (decision PublicationDecision) String() string {
	switch decision {
	case PublicationIncludedInline:
		return "inline"
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
		return "invalid"
	}
}

type PublicationCommentDecision struct {
	id       CommentID
	decision PublicationDecision
}

func (decision PublicationCommentDecision) CommentID() CommentID {
	return decision.id
}

func (decision PublicationCommentDecision) Decision() PublicationDecision {
	return decision.decision
}

type PreparedPublication struct {
	workspace WorkspaceRevision
	source    SourceID
	options   PublicationOptions
	summary   string
	comments  []PublicationComment
	decisions []PublicationCommentDecision
	included  uint32
	excluded  uint32
}

func (review Review) PreparePublication(options PublicationOptions) (PreparedPublication, error) {
	if !options.valid() {
		return PreparedPublication{}, ErrInvalidPublication
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
		prefix := ""
		if options.Policies().Has(PublicationPrefixKinds) {
			prefix = "[" + comment.Kind().String() + "] "
		}
		body := prefix + comment.Body().String()
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
	var retained int64 = int64(len(prepared.summary))
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

func (prepared PreparedPublication) Workspace() WorkspaceRevision {
	return prepared.workspace
}

func (prepared PreparedPublication) Source() SourceID {
	return prepared.source
}

func (prepared PreparedPublication) Options() PublicationOptions {
	return prepared.options
}

func (prepared PreparedPublication) SummaryBody() string {
	return prepared.summary
}

func (prepared PreparedPublication) CommentCount() int {
	return len(prepared.comments)
}

func (prepared PreparedPublication) Comments(start, count int) []PublicationComment {
	if start < 0 || start >= len(prepared.comments) || count <= 0 || count > 256 {
		return nil
	}
	return slices.Clone(prepared.comments[start:min(start+count, len(prepared.comments))])
}

func (prepared PreparedPublication) IncludedCount() uint32 {
	return prepared.included
}

func (prepared PreparedPublication) ExcludedCount() uint32 {
	return prepared.excluded
}

func (prepared PreparedPublication) DecisionAt(
	index int,
) (PublicationCommentDecision, bool) {
	if index < 0 || index >= len(prepared.decisions) {
		return PublicationCommentDecision{}, false
	}
	return prepared.decisions[index], true
}

type PublicationMarker string

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

func (marker PublicationMarker) String() string {
	return string(marker)
}

type CheckedPublication struct {
	target    PublicationTarget
	marker    PublicationMarker
	payload   string
	prepared  PreparedPublication
	staleHead bool
}

type CheckedPublicationDescriptor struct {
	Target    PublicationTarget
	Marker    PublicationMarker
	StaleHead bool
}

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
	payload, err := encodePublicationPayload(target, marker, prepared)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: encode checked publication: %v",
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

type publicationPayload struct {
	CommitID string               `json:"commit_id"`
	Event    string               `json:"event,omitempty"`
	Body     string               `json:"body"`
	Comments []publicationComment `json:"comments"`
}

type publicationComment struct {
	Path      string `json:"path"`
	Body      string `json:"body"`
	Line      uint32 `json:"line"`
	Side      string `json:"side"`
	StartLine uint32 `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
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

func (checked *CheckedPublication) Target() PublicationTarget {
	if checked == nil {
		return PublicationTarget{}
	}
	return checked.target
}

func (checked *CheckedPublication) Marker() PublicationMarker {
	if checked == nil {
		return ""
	}
	return checked.marker
}

func (checked *CheckedPublication) Payload() string {
	if checked == nil {
		return ""
	}
	return checked.payload
}

func (checked *CheckedPublication) PayloadSize() int64 {
	if checked == nil {
		return 0
	}
	return int64(len(checked.payload))
}

func (checked *CheckedPublication) Prepared() PreparedPublication {
	if checked == nil {
		return PreparedPublication{}
	}
	return checked.prepared
}

func (checked *CheckedPublication) StaleHead() bool {
	return checked != nil && checked.staleHead
}

type PublicationStatus uint8

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
		return "invalid"
	}
}

type publicationState struct {
	status       PublicationStatus
	operation    OperationID
	attempt      OperationID
	checked      *CheckedPublication
	target       PublicationTarget
	marker       PublicationMarker
	remoteReview uint64
	detail       OperationDetail
	acknowledged bool
}

type PublicationSummary struct {
	Status       PublicationStatus
	Operation    OperationID
	Event        PublicationEvent
	Included     uint32
	Excluded     uint32
	StaleHead    bool
	Target       PublicationTarget
	Marker       PublicationMarker
	RemoteReview uint64
	Detail       OperationDetail
	Acknowledged bool
}

type PublicationCheckRequest struct {
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	Options   PublicationOptions
}

type PublicationCheckResult struct {
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	Checked   *CheckedPublication
	Detail    OperationDetail
}

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
	}
	return next, PublicationCheckRequest{
		Operation: id,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
		Options:   options,
	}, nil
}

func (review Review) ApplyPublicationCheck(result PublicationCheckResult) (Review, error) {
	if review.publication.status != PublicationChecking ||
		review.publication.operation != result.Operation ||
		review.workspace != result.Workspace ||
		review.snapshot.SourceID() != result.Source ||
		len(result.Detail) > int(MaxOperationDetail) ||
		!utf8.ValidString(result.Detail.String()) {
		return review, ErrInvalidPublication
	}
	next := review.clone()
	if result.Checked == nil {
		if result.Detail == "" {
			return review, ErrInvalidPublication
		}
		next.publication = publicationState{
			status:    PublicationFailedBeforeCommit,
			operation: result.Operation,
			detail:    result.Detail,
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
	}
	return next, nil
}

type PublicationRequest struct {
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	Checked   *CheckedPublication
}

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

type PublicationDisposition uint8

const (
	PublicationDispositionCommitted PublicationDisposition = iota + 1
	PublicationDispositionFailedBeforeCommit
	PublicationDispositionUnknown
	PublicationDispositionCancelledBeforeCommit
)

type PublicationResult struct {
	Operation    OperationID
	Workspace    WorkspaceRevision
	Source       SourceID
	Disposition  PublicationDisposition
	RemoteReview uint64
	Detail       OperationDetail
}

func (review Review) ApplyPublication(result PublicationResult) (Review, error) {
	if review.publication.status != PublicationPublishing ||
		review.publication.operation != result.Operation ||
		review.workspace != result.Workspace ||
		review.snapshot.SourceID() != result.Source ||
		result.Disposition < PublicationDispositionCommitted ||
		result.Disposition > PublicationDispositionCancelledBeforeCommit ||
		len(result.Detail) > int(MaxOperationDetail) ||
		!utf8.ValidString(result.Detail.String()) {
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
		next.publication.status = PublicationCommitted
	case PublicationDispositionFailedBeforeCommit, PublicationDispositionCancelledBeforeCommit:
		next.publication.status = PublicationFailedBeforeCommit
	case PublicationDispositionUnknown:
		next.publication.status = PublicationUnknown
	}
	return next, nil
}

type ReconciliationRequest struct {
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	Target    PublicationTarget
	Marker    PublicationMarker
}

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

type ReconciliationDisposition uint8

const (
	ReconciliationCommitted ReconciliationDisposition = iota + 1
	ReconciliationNotCommitted
	ReconciliationStillUnknown
)

type ReconciliationResult struct {
	Operation   OperationID
	Workspace   WorkspaceRevision
	Source      SourceID
	Disposition ReconciliationDisposition
	Detail      OperationDetail
}

func (review Review) ApplyReconciliation(result ReconciliationResult) (Review, error) {
	if review.publication.status != PublicationReconciling ||
		review.publication.operation != result.Operation ||
		review.workspace != result.Workspace ||
		review.snapshot.SourceID() != result.Source ||
		result.Disposition < ReconciliationCommitted ||
		result.Disposition > ReconciliationStillUnknown ||
		len(result.Detail) > int(MaxOperationDetail) ||
		!utf8.ValidString(result.Detail.String()) {
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

func (review Review) CancelPublicationCheck() (Review, error) {
	if review.publication.status != PublicationChecked {
		return review, ErrInvalidPublication
	}
	next := review.clone()
	next.publication = publicationState{status: PublicationIdle}
	return next, nil
}

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

func (review Review) PublicationSummary() PublicationSummary {
	summary := PublicationSummary{
		Status:       review.publication.status,
		Operation:    review.publication.operation,
		Target:       review.publication.target,
		Marker:       review.publication.marker,
		RemoteReview: review.publication.remoteReview,
		Detail:       review.publication.detail,
		Acknowledged: review.publication.acknowledged,
	}
	if review.publication.checked != nil {
		prepared := review.publication.checked.prepared
		summary.Event = prepared.options.Event()
		summary.Included = prepared.included
		summary.Excluded = prepared.excluded
		summary.StaleHead = review.publication.checked.staleHead
	}
	return summary
}

func (review Review) UnknownPublicationOperation() (OperationID, bool) {
	return review.publication.attempt,
		review.publication.attempt != 0 &&
			(review.publication.status == PublicationUnknown ||
				review.publication.status == PublicationReconciling)
}

func (review Review) PublicationPreflightBody() string {
	if review.publication.checked == nil {
		return ""
	}
	return review.publication.checked.prepared.summary
}

func (review Review) PublicationPreflightPayload() string {
	if review.publication.checked == nil {
		return ""
	}
	return review.publication.checked.payload
}

func (review Review) PublicationDecisionAt(
	index int,
) (PublicationCommentDecision, bool) {
	if review.publication.checked == nil {
		return PublicationCommentDecision{}, false
	}
	return review.publication.checked.prepared.DecisionAt(index)
}

func publicationTargetText(target PublicationTarget) string {
	if target.repository == "" {
		return ""
	}
	return target.repository + "#" + strconv.FormatUint(target.pullRequest, 10)
}
