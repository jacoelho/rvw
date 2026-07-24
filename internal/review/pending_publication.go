package review

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MaxPendingMutationBatch bounds effects in one pending-review mutation request.
const (
	MaxPendingMutationBatch      = 32
	MaxPendingMutationBatchBytes = 1 << 20
)

// PendingMutationKind describes a remote pending-review mutation.
type PendingMutationKind uint8

// PendingMutationReviewBody identifies a supported mutation kind.
const (
	PendingMutationReviewBody PendingMutationKind = iota + 1
	PendingMutationUpdateComment
	PendingMutationDeleteComment
	PendingMutationAddComment
)

// PendingMutation is an immutable, validated pending-review effect.
type PendingMutation struct {
	nodeID     CommentNodeID
	body       string
	marker     string
	baseline   string
	target     PendingCommentTarget
	databaseID CommentDatabaseID
	localID    CommentID
	kind       PendingMutationKind
}

// Kind returns the mutation kind.
func (mutation PendingMutation) Kind() PendingMutationKind { return mutation.kind }

// NodeID returns the existing comment identity when applicable.
func (mutation PendingMutation) NodeID() CommentNodeID { return mutation.nodeID }

// DatabaseID returns the existing comment database identity when applicable.
func (mutation PendingMutation) DatabaseID() CommentDatabaseID { return mutation.databaseID }

// Body returns the desired visible body.
func (mutation PendingMutation) Body() string { return mutation.body }

// Baseline returns the captured visible body.
func (mutation PendingMutation) Baseline() string { return mutation.baseline }

// Marker returns the retained comment marker, when one already exists.
func (mutation PendingMutation) Marker() string { return mutation.marker }

// Target returns immutable target provenance for additions.
func (mutation PendingMutation) Target() PendingCommentTarget { return mutation.target }

// LocalCommentID returns the local identity used to derive a stable attempt marker.
func (mutation PendingMutation) LocalCommentID() CommentID { return mutation.localID }

// RemoteBody returns the exact body used for a remote effect.
func (mutation PendingMutation) RemoteBody(
	reviewID ReviewNodeID,
	attempt PublicationMarker,
) string {
	marker := mutation.marker
	if mutation.kind == PendingMutationReviewBody {
		marker = attempt.String()
	} else if mutation.kind == PendingMutationAddComment ||
		mutation.kind == PendingMutationUpdateComment && marker == "" {
		marker = stablePendingCommentMarker(reviewID, mutation)
	}
	return joinPendingMarker(mutation.body, marker)
}

// BaselineRemoteBody returns the exact captured wire body for an existing
// pending-review comment mutation.
func (mutation PendingMutation) BaselineRemoteBody() string {
	if mutation.kind != PendingMutationUpdateComment &&
		mutation.kind != PendingMutationDeleteComment {
		return ""
	}
	return joinPendingMarker(mutation.baseline, mutation.marker)
}

// DraftMarker returns the canonical stable marker for an added pending-review
// comment. Other mutation kinds do not carry a draft-comment identity.
func (mutation PendingMutation) DraftMarker(reviewID ReviewNodeID) string {
	if mutation.kind != PendingMutationAddComment || reviewID == "" {
		return ""
	}
	return stablePendingCommentMarker(reviewID, mutation)
}

// PreparedPendingPublication is a complete continuation plan for one captured review.
type PreparedPendingPublication struct {
	desiredBody string
	mutations   []PendingMutation
	baseline    PendingReview
	submit      bool
}

// PendingPublicationView is a read-only borrowed view of one prepared pending
// publication. The view allocates and owns no additional retained memory;
// callers must account the PreparedPublication or CheckedPublication that
// owns its immutable backing.
type PendingPublicationView struct {
	pending *PreparedPendingPublication
}

// BaselineNodeID returns the captured pending-review node identity.
func (view PendingPublicationView) BaselineNodeID() ReviewNodeID {
	if view.pending == nil {
		return ""
	}
	return view.pending.baseline.nodeID
}

// BaselineDatabaseID returns the captured pending-review database identity.
func (view PendingPublicationView) BaselineDatabaseID() ReviewDatabaseID {
	if view.pending == nil {
		return 0
	}
	return view.pending.baseline.databaseID
}

// BaselineBody returns the captured pending-review body.
func (view PendingPublicationView) BaselineBody() string {
	if view.pending == nil {
		return ""
	}
	return view.pending.baseline.body
}

// Submit reports whether synchronization is followed by submission.
func (view PendingPublicationView) Submit() bool {
	return view.pending != nil && view.pending.submit
}

// MutationCount returns the number of immutable remote effects.
func (view PendingPublicationView) MutationCount() int {
	if view.pending == nil {
		return 0
	}
	return len(view.pending.mutations)
}

// MutationAt returns one immutable remote effect without copying the plan.
func (view PendingPublicationView) MutationAt(index int) (PendingMutation, bool) {
	if view.pending == nil || index < 0 || index >= len(view.pending.mutations) {
		return PendingMutation{}, false
	}
	return view.pending.mutations[index], true
}

// Baseline returns the immutable remote baseline used by preflight.
func (pending PreparedPendingPublication) Baseline() PendingReview {
	baseline := pending.baseline
	baseline.comments = slices.Clone(baseline.comments)
	return baseline
}

// DesiredBody returns the desired visible review body.
func (pending PreparedPendingPublication) DesiredBody() string { return pending.desiredBody }

// Submit reports whether the pending review must be submitted after synchronization.
func (pending PreparedPendingPublication) Submit() bool { return pending.submit }

// MutationCount returns the number of remote effects before submission.
func (pending PreparedPendingPublication) MutationCount() int { return len(pending.mutations) }

// Mutations returns a bounded copy of mutations.
func (pending PreparedPendingPublication) Mutations(start, count int) []PendingMutation {
	if start < 0 || start >= len(pending.mutations) || count <= 0 || count > MaxPendingMutationBatch {
		return nil
	}
	return slices.Clone(pending.mutations[start:min(start+count, len(pending.mutations))])
}

// PendingPublication returns the existing-review plan when present.
func (prepared PreparedPublication) PendingPublication() (PreparedPendingPublication, bool) {
	if prepared.pending == nil {
		return PreparedPendingPublication{}, false
	}
	pending := *prepared.pending
	pending.baseline.comments = slices.Clone(prepared.pending.baseline.comments)
	pending.mutations = slices.Clone(prepared.pending.mutations)
	return pending, true
}

// BorrowPendingPublication returns a zero-allocation read-only view. Keeping
// the view alive prolongs the owning prepared publication's immutable backing;
// it does not create separately chargeable ownership.
func (prepared PreparedPublication) BorrowPendingPublication() (PendingPublicationView, bool) {
	if prepared.pending == nil {
		return PendingPublicationView{}, false
	}
	return PendingPublicationView{pending: prepared.pending}, true
}

// ContinuesPendingPublication reports whether preflight must reacquire pending state.
func (prepared PreparedPublication) ContinuesPendingPublication() bool {
	return prepared.pending != nil
}

// PendingReuseConstructionBytes returns a conservative simultaneous input,
// result, and scratch charge for converting a new draft into mutations of an
// existing pending review.
func (prepared PreparedPublication) PendingReuseConstructionBytes(
	remote PendingReview,
) int64 {
	retained := prepared.RetainedBytes()
	if retained == 0 || prepared.pending != nil ||
		prepared.options.event != PublicationEventDraft ||
		remote.nodeID == "" || remote.databaseID == 0 || remote.commit == "" {
		return 0
	}
	return 3*retained + 2*remote.RetainedBytes() + 4_096
}

// ReusePendingPublication converts a newly prepared draft into mutations of a
// pending review that appeared after the local snapshot was captured.
func (prepared PreparedPublication) ReusePendingPublication(
	remote PendingReview,
) (PreparedPublication, error) {
	return prepared.ReusePendingPublicationCancellable(nil, remote)
}

// ReusePendingPublicationCancellable converts a newly prepared draft into
// mutations of a pending review that appeared after the local snapshot was
// captured.
func (prepared PreparedPublication) ReusePendingPublicationCancellable(
	cancelled func() error,
	remote PendingReview,
) (PreparedPublication, error) {
	construction := prepared.PendingReuseConstructionBytes(remote)
	if construction == 0 {
		return PreparedPublication{}, ErrInvalidPublication
	}
	if err := cancellationError(cancelled); err != nil {
		return PreparedPublication{}, err
	}
	remoteBody, _ := splitPendingMarker(remote.body, publicationMarkerPrefix)
	var summaryParts []string
	if prepared.summary != "" {
		summaryParts = []string{prepared.summary}
	}
	desiredBody, err := buildPendingPublicationBody(cancelled, remoteBody, summaryParts)
	if err != nil {
		return PreparedPublication{}, err
	}
	mutations := make([]PendingMutation, 0, len(prepared.comments)+1)
	futureBodyBytes := int64(len(remote.body))
	if desiredBody != remoteBody {
		if !remote.viewerUpdate {
			return PreparedPublication{}, fmt.Errorf(
				"%w: pending review body is not editable",
				ErrInvalidPublication,
			)
		}
		mutations = append(mutations, PendingMutation{
			kind: PendingMutationReviewBody, body: desiredBody, baseline: remoteBody,
		})
		futureBodyBytes = pendingMarkedBodyBytes(
			desiredBody,
			len(publicationMarkerPrefix)+32+len(markerSuffix),
		)
	}
	for index, comment := range remote.comments {
		if index%256 == 0 {
			if cancelErr := cancellationError(cancelled); cancelErr != nil {
				return PreparedPublication{}, cancelErr
			}
		}
		futureBodyBytes += int64(len(comment.body))
	}
	inline := 0
	for index, decision := range prepared.decisions {
		if index%256 == 0 {
			if cancelErr := cancellationError(cancelled); cancelErr != nil {
				return PreparedPublication{}, cancelErr
			}
		}
		switch decision.decision {
		case PublicationIncludedInline:
			if inline >= len(prepared.comments) {
				return PreparedPublication{}, ErrInvalidPublication
			}
			comment := prepared.comments[inline]
			inline++
			path, pathErr := NewPathString(comment.path)
			if pathErr != nil {
				return PreparedPublication{}, ErrInvalidPublication
			}
			target, targetErr := NewPendingCommentTarget(PendingCommentTargetDescriptor{
				Path: path, Commit: remote.commit, OriginalCommit: remote.commit,
				Line: new(comment.endLine), StartLine: pendingStartLine(comment.startLine, comment.endLine),
				Subject: PendingSubjectLine, Side: comment.side, StartSide: comment.side,
			})
			if targetErr != nil {
				return PreparedPublication{}, ErrInvalidPublication
			}
			mutations = append(mutations, PendingMutation{
				kind: PendingMutationAddComment, target: target,
				body: comment.body, localID: decision.id,
			})
			futureBodyBytes += pendingMarkedBodyBytes(
				comment.body,
				len(draftCommentMarkerPrefix)+32+len(markerSuffix),
			)
		case PublicationIncludedSummary, PublicationExcludedManual,
			PublicationExcludedStale, PublicationExcludedUnmappable,
			PublicationExcludedInvalid:
		default:
			return PreparedPublication{}, ErrInvalidPublication
		}
	}
	if inline != len(prepared.comments) || remote.CommentCount()+inline > MaxComments ||
		futureBodyBytes > MaxCommentBodyTotal {
		return PreparedPublication{}, ErrInvalidPublication
	}
	comments, err := clonePendingCommentsCancellable(cancelled, remote.comments)
	if err != nil {
		return PreparedPublication{}, err
	}
	baseline := remote
	baseline.comments = comments
	next := prepared
	next.summary = ""
	next.comments = nil
	next.pending = new(PreparedPendingPublication{
		desiredBody: desiredBody,
		mutations:   mutations,
		baseline:    baseline,
	})
	if err := validatePreparedPendingPublicationBytes(cancelled, *next.pending); err != nil {
		return PreparedPublication{}, err
	}
	if next.RetainedBytes() > construction {
		return PreparedPublication{}, ErrInvalidPublication
	}
	return next, nil
}

func pendingMarkedBodyBytes(body string, markerBytes int) int64 {
	bytes := int64(len(body) + markerBytes)
	if body != "" && markerBytes != 0 {
		bytes += 2
	}
	return bytes
}

// ReconcilePendingPublication accepts remote effects already equal to the
// desired local state and rejects external divergence from the captured base.
func (prepared PreparedPublication) ReconcilePendingPublication(
	remote PendingReview,
) (PreparedPublication, error) {
	return prepared.ReconcilePendingPublicationCancellable(nil, remote)
}

// PendingReconciliationConstructionBytes returns a conservative simultaneous
// input, result, and index charge for one pending-review reconciliation.
func (prepared PreparedPublication) PendingReconciliationConstructionBytes(
	remote PendingReview,
) int64 {
	if prepared.pending == nil ||
		remote.nodeID != prepared.pending.baseline.nodeID ||
		remote.databaseID != prepared.pending.baseline.databaseID {
		return 0
	}
	preparedBytes := prepared.RetainedBytes()
	if preparedBytes == 0 {
		return 0
	}
	return preparedBytes + 2*remote.RetainedBytes() +
		int64(remote.CommentCount())*1_024 +
		int64(len(prepared.pending.mutations))*320 + 1_024
}

type pendingMarkerMatch struct {
	comment PendingComment
	count   uint8
}

type pendingRemotePublicationIndex struct {
	comments map[CommentNodeID]PendingComment
	markers  map[string]pendingMarkerMatch
}

type pendingMutationReconciliation uint8

const (
	pendingMutationAlreadyApplied pendingMutationReconciliation = iota + 1
	pendingMutationRetain
	pendingMutationDiverged
)

func buildPendingRemotePublicationIndexCancellable(
	cancelled func() error,
	remote PendingReview,
) (pendingRemotePublicationIndex, error) {
	index := pendingRemotePublicationIndex{
		comments: make(map[CommentNodeID]PendingComment, remote.CommentCount()),
		markers:  make(map[string]pendingMarkerMatch, remote.CommentCount()),
	}
	for position, comment := range remote.comments {
		if position%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return pendingRemotePublicationIndex{}, err
			}
		}
		index.comments[comment.nodeID] = comment
		_, marker := splitPendingMarker(comment.body, draftCommentMarkerPrefix)
		if marker == "" {
			continue
		}
		match := index.markers[marker]
		match.comment = comment
		if match.count < 2 {
			match.count++
		}
		index.markers[marker] = match
	}
	return index, nil
}

func pendingReconciliationMarkerCancellable(
	cancelled func() error,
	pending *PreparedPendingPublication,
	mutation PendingMutation,
) (string, error) {
	if mutation.kind != PendingMutationAddComment {
		return "", nil
	}
	return stablePendingCommentMarkerCancellable(
		cancelled,
		pending.baseline.nodeID,
		mutation,
	)
}

func classifyPendingPublicationMutation(
	pending *PreparedPendingPublication,
	remote PendingReview,
	index pendingRemotePublicationIndex,
	mutation PendingMutation,
	marker string,
) (pendingMutationReconciliation, error) {
	switch mutation.kind {
	case PendingMutationReviewBody:
		visible, _ := splitPendingMarker(remote.body, publicationMarkerPrefix)
		if visible == mutation.body && !pending.submit {
			return pendingMutationAlreadyApplied, nil
		}
		if visible == mutation.body {
			return pendingMutationRetain, nil
		}
		baseline, _ := splitPendingMarker(pending.baseline.body, publicationMarkerPrefix)
		if visible != baseline {
			return pendingMutationDiverged,
				errors.New("pending review body changed on GitHub")
		}
		return pendingMutationRetain, nil
	case PendingMutationUpdateComment:
		comment, exists := index.comments[mutation.nodeID]
		if !exists {
			return pendingMutationDiverged,
				errors.New("pending review comment disappeared on GitHub")
		}
		visible, _ := splitPendingMarker(comment.body, draftCommentMarkerPrefix)
		if visible == mutation.body {
			return pendingMutationAlreadyApplied, nil
		}
		if visible != mutation.baseline {
			return pendingMutationDiverged,
				errors.New("pending review comment changed on GitHub")
		}
		return pendingMutationRetain, nil
	case PendingMutationDeleteComment:
		comment, exists := index.comments[mutation.nodeID]
		if !exists {
			return pendingMutationAlreadyApplied, nil
		}
		visible, _ := splitPendingMarker(comment.body, draftCommentMarkerPrefix)
		if visible != mutation.baseline {
			return pendingMutationDiverged,
				errors.New("pending review comment changed on GitHub")
		}
		return pendingMutationRetain, nil
	case PendingMutationAddComment:
		match := index.markers[marker]
		if match.count > 1 {
			return pendingMutationDiverged,
				errors.New("pending review comment marker is ambiguous")
		}
		if match.count == 0 {
			return pendingMutationRetain, nil
		}
		visible, _ := splitPendingMarker(match.comment.body, draftCommentMarkerPrefix)
		if visible != mutation.body ||
			!match.comment.target.MatchesPendingAddition(mutation.target) {
			return pendingMutationDiverged,
				errors.New("pending review comment marker diverged on GitHub")
		}
		return pendingMutationAlreadyApplied, nil
	default:
		return pendingMutationDiverged, ErrInvalidPublication
	}
}

// ReconcilePendingPublicationCancellable accepts remote effects already equal
// to the desired local state and rejects external divergence from the captured
// base.
func (prepared PreparedPublication) ReconcilePendingPublicationCancellable(
	cancelled func() error,
	remote PendingReview,
) (PreparedPublication, error) {
	construction := prepared.PendingReconciliationConstructionBytes(remote)
	if construction == 0 {
		return PreparedPublication{}, ErrInvalidPublication
	}
	if err := cancellationError(cancelled); err != nil {
		return PreparedPublication{}, err
	}
	remoteIndex, err := buildPendingRemotePublicationIndexCancellable(cancelled, remote)
	if err != nil {
		return PreparedPublication{}, err
	}
	remaining := make([]PendingMutation, 0, len(prepared.pending.mutations))
	for index, mutation := range prepared.pending.mutations {
		if index%256 == 0 {
			if cancelErr := cancellationError(cancelled); cancelErr != nil {
				return PreparedPublication{}, cancelErr
			}
		}
		marker, markerErr := pendingReconciliationMarkerCancellable(
			cancelled,
			prepared.pending,
			mutation,
		)
		if markerErr != nil {
			return PreparedPublication{}, markerErr
		}
		disposition, classifyErr := classifyPendingPublicationMutation(
			prepared.pending,
			remote,
			remoteIndex,
			mutation,
			marker,
		)
		switch disposition {
		case pendingMutationAlreadyApplied:
			continue
		case pendingMutationRetain:
			remaining = append(remaining, mutation)
		case pendingMutationDiverged:
			return PreparedPublication{}, classifyErr
		default:
			return PreparedPublication{}, ErrInvalidPublication
		}
	}
	remoteComments, err := clonePendingCommentsCancellable(cancelled, remote.comments)
	if err != nil {
		return PreparedPublication{}, err
	}
	next := prepared
	pending := *prepared.pending
	pending.baseline = remote
	pending.baseline.comments = remoteComments
	pending.mutations = remaining
	next.pending = new(pending)
	if err := cancellationError(cancelled); err != nil {
		return PreparedPublication{}, err
	}
	if next.RetainedBytes() > construction {
		return PreparedPublication{}, ErrInvalidPublication
	}
	return next, nil
}

func clonePendingCommentsCancellable(
	cancelled func() error,
	comments []PendingComment,
) ([]PendingComment, error) {
	cloned := make([]PendingComment, len(comments))
	for start := 0; start < len(comments); start += 256 {
		if err := cancellationError(cancelled); err != nil {
			return nil, err
		}
		end := min(start+256, len(comments))
		copy(cloned[start:end], comments[start:end])
	}
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	return cloned, nil
}

func (source PublicationSource) preparePendingPublicationCancellable(
	cancelled func() error,
	options PublicationOptions,
) (PreparedPublication, error) {
	if source.state == nil || source.state.nodeID == "" ||
		source.state.databaseID == 0 || source.state.commit == "" {
		return PreparedPublication{}, ErrInvalidPublication
	}
	if status := source.status; status != PendingReviewReady {
		return PreparedPublication{}, fmt.Errorf(
			"%w: pending GitHub review is %s",
			ErrInvalidPublication,
			status,
		)
	}
	inspected := 0
	for _, binding := range source.state.bindings {
		if inspected%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return PreparedPublication{}, err
			}
		}
		inspected++
		if binding.conflict != nil {
			return PreparedPublication{}, fmt.Errorf(
				"%w: pending review has unresolved conflicts",
				ErrInvalidPublication,
			)
		}
	}
	baseline := source.baseline
	if baseline.nodeID != source.state.nodeID ||
		baseline.databaseID != source.state.databaseID {
		return PreparedPublication{}, ErrInvalidPublication
	}
	prepared := PreparedPublication{
		workspace: source.workspace,
		source:    source.source,
		options:   options,
		decisions: make([]PublicationCommentDecision, 0, len(source.comments.comments)),
	}
	pending := PreparedPendingPublication{
		baseline:  baseline,
		submit:    options.event != PublicationEventDraft,
		mutations: make([]PendingMutation, 1, len(source.comments.comments)+1),
	}
	summaryParts, desiredBody, err := source.appendPendingPublicationCommentsCancellable(
		cancelled,
		options,
		&prepared,
		&pending,
	)
	if err != nil {
		return PreparedPublication{}, err
	}
	body, err := buildPendingPublicationBody(cancelled, desiredBody, summaryParts)
	if err != nil {
		return PreparedPublication{}, err
	}
	pending.desiredBody = body
	if !source.state.bodyUpdate &&
		(pending.submit || pending.desiredBody != source.state.bodyBase) {
		return PreparedPublication{}, fmt.Errorf(
			"%w: pending review body is not editable",
			ErrInvalidPublication,
		)
	}
	pending.mutations[0] = PendingMutation{
		kind:     PendingMutationReviewBody,
		body:     pending.desiredBody,
		baseline: source.state.bodyBase,
	}
	prepared.pending = new(pending)
	if err := validatePreparedPendingPublicationBytes(cancelled, pending); err != nil {
		return PreparedPublication{}, err
	}
	if prepared.RetainedBytes() > source.ConstructionBytes() {
		return PreparedPublication{}, ErrInvalidPublication
	}
	return prepared, nil
}

func (source PublicationSource) appendPendingPublicationCommentsCancellable(
	cancelled func() error,
	options PublicationOptions,
	prepared *PreparedPublication,
	pending *PreparedPendingPublication,
) ([]string, string, error) {
	var summaryParts []string
	desiredBody := source.state.bodyBase
	for index, comment := range source.comments.comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return nil, "", err
			}
		}
		binding, bound := source.state.bindings[comment.id]
		manuallyExcluded := options.exclusions.Contains(comment.id)
		if bound {
			if manuallyExcluded {
				return nil, "", fmt.Errorf(
					"%w: an existing GitHub draft cannot be excluded without deleting it",
					ErrInvalidPublication,
				)
			}
			if err := source.appendBoundPendingPublicationComment(
				comment,
				binding,
				prepared,
				pending,
				&desiredBody,
			); err != nil {
				return nil, "", err
			}
			continue
		}
		if manuallyExcluded {
			prepared.excluded++
			prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
				id: comment.id, decision: PublicationExcludedManual,
			})
			continue
		}
		if err := source.appendUnboundPendingPublicationComment(
			comment,
			options,
			prepared,
			pending,
			&summaryParts,
		); err != nil {
			return nil, "", err
		}
	}
	if err := cancellationError(cancelled); err != nil {
		return nil, "", err
	}
	return summaryParts, desiredBody, nil
}

func (source PublicationSource) appendBoundPendingPublicationComment(
	comment Comment,
	binding pendingBinding,
	prepared *PreparedPublication,
	pending *PreparedPendingPublication,
	desiredBody *string,
) error {
	if binding.deleted {
		if binding.kind == pendingBindingComment &&
			!binding.capabilities.Has(PendingCapabilityDelete) {
			return fmt.Errorf(
				"%w: pending review comment is not deletable",
				ErrInvalidPublication,
			)
		}
		prepared.excluded++
		prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
			id: comment.id, decision: PublicationExcludedManual,
		})
		if binding.kind == pendingBindingComment {
			pending.mutations = append(pending.mutations, PendingMutation{
				kind:       PendingMutationDeleteComment,
				nodeID:     binding.remoteNode,
				databaseID: binding.remoteID,
				baseline:   binding.wireBaseline,
				marker:     binding.marker,
				localID:    comment.id,
			})
		} else {
			*desiredBody = ""
		}
		return nil
	}
	prepared.included++
	decision := PublicationIncludedInline
	if binding.kind == pendingBindingBody {
		if commentContentFrom(comment) == binding.baseline {
			*desiredBody = binding.wireBaseline
		} else {
			wireBody, err := encodeCommentWire(commentContentFrom(comment))
			if err != nil {
				return fmt.Errorf("%w: %w", ErrInvalidPublication, err)
			}
			*desiredBody = wireBody
		}
		decision = PublicationIncludedSummary
	} else if commentContentFrom(comment) != binding.baseline {
		if !binding.capabilities.Has(PendingCapabilityUpdate) {
			return fmt.Errorf(
				"%w: pending review comment is not editable",
				ErrInvalidPublication,
			)
		}
		wireBody, err := encodeCommentWire(commentContentFrom(comment))
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidPublication, err)
		}
		pending.mutations = append(pending.mutations, PendingMutation{
			kind:       PendingMutationUpdateComment,
			nodeID:     binding.remoteNode,
			databaseID: binding.remoteID,
			body:       wireBody,
			baseline:   binding.wireBaseline,
			marker:     binding.marker,
			localID:    comment.id,
		})
	}
	prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
		id: comment.id, decision: decision,
	})
	return nil
}

func (source PublicationSource) appendUnboundPendingPublicationComment(
	comment Comment,
	options PublicationOptions,
	prepared *PreparedPublication,
	pending *PreparedPendingPublication,
	summaryParts *[]string,
) error {
	mutation, decision, summary, err := prepareNewPendingComment(
		comment,
		options,
		source.state.commit,
	)
	if err != nil {
		return err
	}
	prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
		id: comment.id, decision: decision,
	})
	if decision == PublicationExcludedInvalid || decision == PublicationExcludedStale ||
		decision == PublicationExcludedUnmappable {
		prepared.excluded++
		return nil
	}
	prepared.included++
	if summary != "" {
		*summaryParts = append(*summaryParts, summary)
		return nil
	}
	pending.mutations = append(pending.mutations, mutation)
	return nil
}

func buildPendingPublicationBody(
	cancelled func() error,
	desiredBody string,
	summaryParts []string,
) (string, error) {
	builder, err := newBoundedBuilder(MaxPublicationBytes, cancelled)
	if err != nil {
		return "", err
	}
	if err := builder.WriteString(desiredBody); err != nil {
		return "", publicationBuilderError("prepared pending review", err)
	}
	for index, part := range summaryParts {
		if index%256 == 0 {
			if err := builder.Checkpoint(); err != nil {
				return "", err
			}
		}
		if builder.Len() != 0 {
			if err := builder.WriteString("\n\n"); err != nil {
				return "", publicationBuilderError("prepared pending review", err)
			}
		}
		if err := builder.WriteString(part); err != nil {
			return "", publicationBuilderError("prepared pending review", err)
		}
	}
	return builder.Finish()
}

func validatePreparedPendingPublicationBytes(
	cancelled func() error,
	pending PreparedPendingPublication,
) error {
	retained := int64(len(pending.desiredBody))
	for index, mutation := range pending.mutations {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return err
			}
		}
		addition := int64(
			len(mutation.body) + len(mutation.baseline) + len(mutation.marker) + 128,
		)
		if addition > MaxPublicationBytes-retained {
			return fmt.Errorf(
				"%w: prepared pending review exceeds %d bytes",
				ErrInvalidPublication,
				MaxPublicationBytes,
			)
		}
		retained += addition
		var markerBytes int
		switch mutation.kind {
		case PendingMutationReviewBody:
			markerBytes = len(publicationMarkerPrefix) + 32 + len(markerSuffix)
		case PendingMutationAddComment:
			markerBytes = len(draftCommentMarkerPrefix) + 32 + len(markerSuffix)
		case PendingMutationUpdateComment:
			markerBytes = len(mutation.marker)
			if markerBytes == 0 {
				markerBytes = len(draftCommentMarkerPrefix) + 32 + len(markerSuffix)
			}
		case PendingMutationDeleteComment:
			continue
		default:
			return ErrInvalidPublication
		}
		wireBytes := len(mutation.body) + markerBytes
		if mutation.body != "" && markerBytes != 0 {
			wireBytes += 2
		}
		if int64(wireBytes) > MaxCommentBodyBytes {
			return fmt.Errorf(
				"%w: pending GitHub comment exceeds %d bytes",
				ErrInvalidPublication,
				MaxCommentBodyBytes,
			)
		}
	}
	return cancellationError(cancelled)
}

func prepareNewPendingComment(
	comment Comment,
	options PublicationOptions,
	commit ObjectID,
) (PendingMutation, PublicationDecision, string, error) {
	target := comment.target
	if target.Kind() == TargetLine && !comment.Stale() && !comment.Unmappable() {
		path, hasPath := target.AnchoredPath()
		side, start, end, hasLines := target.Lines()
		pathUTF8, validUTF8 := path.UTF8()
		if hasPath && validUTF8 && hasLines {
			remoteTarget, targetErr := NewPendingCommentTarget(PendingCommentTargetDescriptor{
				Path:           path,
				Commit:         commit,
				OriginalCommit: commit,
				Line:           new(end),
				StartLine:      pendingStartLine(start, end),
				Subject:        PendingSubjectLine,
				Side:           side,
				StartSide:      side,
			})
			if targetErr == nil && pathUTF8 != "" {
				body, err := encodeCommentWire(commentContentFrom(comment))
				if err != nil {
					return PendingMutation{}, 0, "", fmt.Errorf("%w: %w", ErrInvalidPublication, err)
				}
				return PendingMutation{
					kind: PendingMutationAddComment, target: remoteTarget,
					body: body, localID: comment.id,
				}, PublicationIncludedInline, "", nil
			}
		}
	}
	if target.Kind() == TargetFile && !comment.Unmappable() {
		path, hasPath := target.AnchoredPath()
		if pathUTF8, validUTF8 := path.UTF8(); hasPath && validUTF8 && pathUTF8 != "" {
			remoteTarget, targetErr := NewPendingCommentTarget(PendingCommentTargetDescriptor{
				Path:           path,
				Commit:         commit,
				OriginalCommit: commit,
				Subject:        PendingSubjectFile,
			})
			if targetErr == nil {
				body, err := encodeCommentWire(commentContentFrom(comment))
				if err != nil {
					return PendingMutation{}, 0, "", fmt.Errorf("%w: %w", ErrInvalidPublication, err)
				}
				return PendingMutation{
					kind: PendingMutationAddComment, target: remoteTarget,
					body: body, localID: comment.id,
				}, PublicationIncludedInline, "", nil
			}
		}
	}
	if target.Kind() == TargetReview || target.Kind() == TargetFile ||
		options.policies.Has(PublicationMoveUnmappableToSummary) {
		body, err := encodePublicationSummaryWire(
			commentContentFrom(comment), target,
		)
		if err != nil {
			return PendingMutation{}, 0, "", fmt.Errorf("%w: %w", ErrInvalidPublication, err)
		}
		return PendingMutation{}, PublicationIncludedSummary, body, nil
	}
	decision := PublicationExcludedInvalid
	if comment.Stale() {
		decision = PublicationExcludedStale
	} else if comment.Unmappable() {
		decision = PublicationExcludedUnmappable
	}
	return PendingMutation{}, decision, "", nil
}

func pendingStartLine(start, end LineNumber) *LineNumber {
	if start == end {
		return nil
	}
	return new(start)
}

func encodePendingPublicationEnvelopeCancellable(
	cancelled func() error,
	marker PublicationMarker,
	prepared PreparedPublication,
) (string, error) {
	if prepared.pending == nil || marker == "" {
		return "", ErrInvalidPublication
	}
	builder, err := newJSONBuilder(MaxPublicationBytes, cancelled)
	if err == nil {
		err = builder.Syntax(`{"marker":`)
	}
	if err == nil {
		err = builder.String(marker.String())
	}
	if err == nil {
		err = builder.Syntax(`,"review":`)
	}
	if err == nil {
		err = builder.Uint64(prepared.pending.baseline.databaseID.Uint64())
	}
	if err == nil {
		err = builder.Syntax(`,"effects":`)
	}
	if err == nil {
		err = builder.Uint64(uint64(len(prepared.pending.mutations)))
	}
	if err == nil {
		err = builder.Syntax("}")
	}
	var encoded string
	if err == nil {
		encoded, err = builder.Finish()
	}
	if err != nil {
		return "", errors.New("pending publication envelope is invalid")
	}
	return encoded, nil
}

func joinPendingMarker(body, marker string) string {
	if marker == "" {
		return strings.Clone(body)
	}
	if body == "" {
		return marker
	}
	return body + "\n\n" + marker
}

func stablePendingCommentMarker(reviewID ReviewNodeID, mutation PendingMutation) string {
	marker, err := stablePendingCommentMarkerCancellable(nil, reviewID, mutation)
	if err != nil {
		panic("nil pending marker cancellation returned an error")
	}
	return marker
}

func stablePendingCommentMarkerCancellable(
	cancelled func() error,
	reviewID ReviewNodeID,
	mutation PendingMutation,
) (string, error) {
	hash := sha256.New()
	parts := []string{
		string(reviewID),
		strconv.FormatUint(uint64(mutation.localID), 10),
		mutation.body,
		mutation.target.path.raw,
	}
	for index, part := range parts {
		if index != 0 {
			_, _ = hash.Write([]byte{0})
		}
		for part != "" {
			if err := cancellationError(cancelled); err != nil {
				return "", err
			}
			piece := part[:min(len(part), cancellationBytes)]
			_, _ = hash.Write([]byte(piece))
			part = part[len(piece):]
		}
	}
	if err := cancellationError(cancelled); err != nil {
		return "", err
	}
	sum := hash.Sum(nil)
	return draftCommentMarkerPrefix + fmt.Sprintf("%x", sum[:16]) + markerSuffix, nil
}
