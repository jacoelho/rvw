package change

import (
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxPublicationBytes defines the supported publication bytes limit.
const (
	MaxPublicationBytes                       = 16 << 20
	MaxCheckedPublicationBytes          int64 = 2 * MaxPublicationBytes
	checkedPublicationContinuationBytes       = 512
)

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
	exclusions PublicationExclusions
	event      PublicationEvent
	policies   PublicationPolicies
}

const publicationExclusionWords = MaxComments / 64

const publicationOptionsBytes int64 = publicationExclusionWords*8 + 16

// PublicationExclusions is the bounded set of comments omitted from publication.
type PublicationExclusions struct {
	words [publicationExclusionWords]uint64
	count uint16
}

// NewPublicationExclusions constructs a validated exclusion set.
func NewPublicationExclusions(comments []CommentID) (PublicationExclusions, error) {
	if len(comments) > MaxComments {
		return PublicationExclusions{}, ErrInvalidPublication
	}
	var exclusions PublicationExclusions
	for _, id := range comments {
		if id.Uint32() >= MaxComments || exclusions.Contains(id) {
			return PublicationExclusions{}, ErrInvalidPublication
		}
		exclusions.words[id.Uint32()/64] |= uint64(1) << (id.Uint32() % 64)
		exclusions.count++
	}
	return exclusions, nil
}

// Toggle returns the set with id membership toggled.
func (exclusions PublicationExclusions) Toggle(id CommentID) (PublicationExclusions, error) {
	if !exclusions.valid() || id.Uint32() >= MaxComments {
		return PublicationExclusions{}, ErrInvalidPublication
	}
	word := id.Uint32() / 64
	bit := uint64(1) << (id.Uint32() % 64)
	if exclusions.words[word]&bit == 0 {
		exclusions.words[word] |= bit
		exclusions.count++
	} else {
		exclusions.words[word] &^= bit
		exclusions.count--
	}
	return exclusions, nil
}

// Contains reports whether id is excluded.
func (exclusions PublicationExclusions) Contains(id CommentID) bool {
	return id.Uint32() < MaxComments &&
		exclusions.words[id.Uint32()/64]&(uint64(1)<<(id.Uint32()%64)) != 0
}

// Count returns the exclusion count.
func (exclusions PublicationExclusions) Count() int { return int(exclusions.count) }

func (exclusions PublicationExclusions) valid() bool {
	count := 0
	for _, word := range exclusions.words {
		count += bits.OnesCount64(word)
	}
	return count == int(exclusions.count)
}

func (exclusions PublicationExclusions) comments() []CommentID {
	comments := make([]CommentID, 0, exclusions.Count())
	for wordIndex, word := range exclusions.words {
		for word != 0 {
			bit := bits.TrailingZeros64(word)
			comments = append(comments, CommentID(uint32FromInt(wordIndex*64+bit)))
			word &^= uint64(1) << bit
		}
	}
	return comments
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
	if !options.valid() {
		return PublicationOptions{}, fmt.Errorf(
			"%w: excluded comments exceed %d",
			ErrInvalidPublication,
			MaxComments,
		)
	}
	exclusions, err := NewPublicationExclusions(comments)
	if err != nil {
		return PublicationOptions{}, err
	}
	options.exclusions = exclusions
	return options, nil
}

// ExcludedComments returns the receiver's excluded comments value.
func (options PublicationOptions) ExcludedComments() []CommentID {
	return options.exclusions.comments()
}

// WithExclusions replaces the exclusions without boundary conversion.
func (options PublicationOptions) WithExclusions(
	exclusions PublicationExclusions,
) (PublicationOptions, error) {
	if !options.valid() || !exclusions.valid() {
		return PublicationOptions{}, ErrInvalidPublication
	}
	options.exclusions = exclusions
	return options, nil
}

// Exclusions returns the fixed exclusion value.
func (options PublicationOptions) Exclusions() PublicationExclusions {
	return options.exclusions
}

// RetainedBytes returns the fixed immutable options ownership.
func (options PublicationOptions) RetainedBytes() int64 {
	if !options.valid() {
		return 0
	}
	return publicationOptionsBytes
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
		options.policies.valid() && options.exclusions.valid()
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
		return summaryValue
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

// PublicationSource is the narrow immutable input needed to prepare either a
// new review or a continuation of one captured pending review.
type PublicationSource struct {
	state         *pendingReviewState
	pendingDetail OperationDetail
	baseline      PendingReview
	comments      commentSource
	workspace     WorkspaceRevision
	source        SourceID
	retainedBytes int64
	status        PendingReviewStatus
}

const publicationSourceMetadataBytes int64 = 208

// PublicationSource returns an O(1) worker source independent of presentation,
// search, operation, and publication state.
func (review Review) PublicationSource() PublicationSource {
	if review.publication.pendingRefresh {
		return PublicationSource{}
	}
	var baseline PendingReview
	hasBaseline := review.snapshot.pendingReview != nil
	if hasBaseline {
		baseline = *review.snapshot.pendingReview
	}
	retained := review.retainedBytes - draftRetainedBytes(review.draft)
	if hasBaseline {
		retained += review.snapshot.pendingRetainedBytes
	}
	pending := review.snapshot.PendingReviewSummary()
	return PublicationSource{
		state:         review.pending,
		comments:      review.source(),
		baseline:      baseline,
		status:        pending.Status,
		pendingDetail: pending.Detail,
		workspace:     review.workspace,
		source:        review.snapshot.SourceID(),
		retainedBytes: retained + publicationSourceMetadataBytes,
	}
}

// Ready reports whether the source was captured from a valid review.
func (source PublicationSource) Ready() bool {
	return source.comments.ready() && source.workspace != 0 && source.source != 0 &&
		source.retainedBytes >= publicationSourceMetadataBytes
}

// Workspace returns the captured workspace revision.
func (source PublicationSource) Workspace() WorkspaceRevision { return source.workspace }

// Source returns the captured source identity.
func (source PublicationSource) Source() SourceID { return source.source }

// RetainedBytes returns the immutable review ownership prolonged by preparation.
func (source PublicationSource) RetainedBytes() int64 {
	if !source.Ready() {
		return 0
	}
	return source.retainedBytes
}

// ConstructionBytes returns a conservative simultaneous source, result, and
// scratch charge for publication preparation.
func (source PublicationSource) ConstructionBytes() int64 {
	if !source.Ready() {
		return 0
	}
	comments := int64(len(source.comments.comments) + 1)
	return source.retainedBytes + 2*MaxPublicationBytes +
		comments*(64+8+16+256) + 1_024
}

func (prepared PreparedPublication) retainedBytes() int64 {
	total := int64(len(prepared.summary)) + int64(cap(prepared.comments))*64 +
		int64(cap(prepared.decisions))*8 + publicationExclusionWords*8 + 64
	for _, comment := range prepared.comments {
		total += int64(len(comment.path) + len(comment.body))
	}
	if prepared.pending == nil {
		return total
	}
	total += prepared.pending.baseline.retainedBytes()
	total += int64(len(prepared.pending.desiredBody)) +
		int64(cap(prepared.pending.mutations))*256
	for _, mutation := range prepared.pending.mutations {
		total += int64(
			len(mutation.nodeID) + len(mutation.body) + len(mutation.marker) +
				len(mutation.baseline) + len(mutation.target.path.raw) +
				len(mutation.target.commit) + len(mutation.target.originalCommit) +
				len(mutation.target.replyTo),
		)
	}
	return total
}

// RetainedBytes returns the complete immutable prepared-publication ownership.
func (prepared PreparedPublication) RetainedBytes() int64 {
	if prepared.workspace == 0 || prepared.source == 0 || !prepared.options.valid() {
		return 0
	}
	return prepared.retainedBytes()
}

// PreparePublication prepares publication.
func (review Review) PreparePublication(options PublicationOptions) (PreparedPublication, error) {
	return review.PublicationSource().PreparePublicationCancellable(nil, options)
}

// PreparePublicationCancellable prepares publication from a review source.
func (review Review) PreparePublicationCancellable(
	cancelled func() error,
	options PublicationOptions,
) (PreparedPublication, error) {
	return review.PublicationSource().PreparePublicationCancellable(cancelled, options)
}

// PreparePublication prepares publication synchronously from an immutable source.
func (source PublicationSource) PreparePublication(
	options PublicationOptions,
) (PreparedPublication, error) {
	return source.PreparePublicationCancellable(nil, options)
}

// PreparePublicationCancellable prepares publication from an immutable source.
func (source PublicationSource) PreparePublicationCancellable(
	cancelled func() error,
	options PublicationOptions,
) (PreparedPublication, error) {
	if !source.Ready() || !options.valid() {
		return PreparedPublication{}, ErrInvalidPublication
	}
	if err := cancellationError(cancelled); err != nil {
		return PreparedPublication{}, err
	}
	if source.state != nil {
		return source.preparePendingPublicationCancellable(cancelled, options)
	}
	status := source.status
	if status == PendingReviewUnavailable || status == PendingReviewLimited ||
		status == PendingReviewAmbiguous {
		if !source.pendingDetail.IsEmpty() {
			return PreparedPublication{}, fmt.Errorf(
				"%w: pending GitHub review is %s: %s",
				ErrInvalidPublication,
				status,
				source.pendingDetail.String(),
			)
		}
		return PreparedPublication{}, fmt.Errorf(
			"%w: pending GitHub review is %s",
			ErrInvalidPublication,
			status,
		)
	}
	prepared := PreparedPublication{
		workspace: source.workspace,
		source:    source.source,
		options:   options,
		comments:  make([]PublicationComment, 0, len(source.comments.comments)),
		decisions: make([]PublicationCommentDecision, 0, len(source.comments.comments)),
	}
	summary, err := newBoundedBuilder(MaxPublicationBytes, cancelled)
	if err != nil {
		return PreparedPublication{}, err
	}
	for index, comment := range source.comments.comments {
		if index%256 == 0 {
			if checkpointErr := summary.Checkpoint(); checkpointErr != nil {
				return PreparedPublication{}, checkpointErr
			}
		}
		if options.exclusions.Contains(comment.ID()) {
			prepared.excluded++
			prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
				id:       comment.ID(),
				decision: PublicationExcludedManual,
			})
			continue
		}
		target := comment.Target()
		if target.Kind() == TargetLine && !comment.Stale() && !comment.Unmappable() {
			path, hasPath := target.AnchoredPath()
			pathUTF8, validUTF8 := path.UTF8()
			side, start, end, hasLines := target.Lines()
			if hasPath && validUTF8 && hasLines {
				body, encodeErr := encodeCommentWire(commentContentFrom(comment))
				if encodeErr != nil {
					return PreparedPublication{}, fmt.Errorf("%w: %w", ErrInvalidPublication, encodeErr)
				}
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
			body, encodeErr := encodePublicationSummaryWire(
				commentContentFrom(comment), target,
			)
			if encodeErr != nil {
				return PreparedPublication{}, fmt.Errorf("%w: %w", ErrInvalidPublication, encodeErr)
			}
			if appendErr := appendPublicationSummary(summary, body); appendErr != nil {
				return PreparedPublication{}, publicationBuilderError("prepared summary", appendErr)
			}
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
	}
	prepared.summary, err = summary.Finish()
	if err != nil {
		return PreparedPublication{}, err
	}
	if err := validatePreparedPublicationBytes(cancelled, prepared); err != nil {
		return PreparedPublication{}, err
	}
	if prepared.RetainedBytes() > source.ConstructionBytes() {
		return PreparedPublication{}, ErrInvalidPublication
	}
	return prepared, nil
}

func publicationBuilderError(subject string, err error) error {
	if !errors.Is(err, errBoundedStringLimit) {
		return err
	}
	return fmt.Errorf(
		"%w: %s exceeds %d bytes: %w",
		ErrInvalidPublication,
		subject,
		MaxPublicationBytes,
		err,
	)
}

func appendPublicationSummary(
	builder *boundedBuilder,
	body string,
) error {
	if builder.Len() != 0 {
		if err := builder.WriteString("\n\n"); err != nil {
			return err
		}
	}
	return builder.WriteString(body)
}

func encodePublicationSummaryWire(
	content commentContent,
	target CommentTarget,
) (string, error) {
	if target.Kind() != TargetReview {
		if path, ok := target.AnchoredPath(); ok {
			content.body = "`" + markdownEscape(path.Display()) + "`: " + content.body
		}
	}
	return encodeCommentWire(content)
}

func validatePreparedPublicationBytes(
	cancelled func() error,
	prepared PreparedPublication,
) error {
	retained := int64(len(prepared.summary))
	for index, comment := range prepared.comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return err
			}
		}
		addition := int64(len(comment.path) + len(comment.body) + 32)
		if addition > MaxPublicationBytes-retained {
			return fmt.Errorf(
				"%w: prepared publication exceeds %d bytes",
				ErrInvalidPublication,
				MaxPublicationBytes,
			)
		}
		retained += addition
	}
	if err := cancellationError(cancelled); err != nil {
		return err
	}
	if retained > MaxPublicationBytes {
		return fmt.Errorf(
			"%w: prepared publication exceeds %d bytes",
			ErrInvalidPublication,
			MaxPublicationBytes,
		)
	}
	return nil
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
