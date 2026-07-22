package review

import (
	"crypto/sha256"
	"encoding/json"
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

// PreparedPendingPublication is a complete continuation plan for one captured review.
type PreparedPendingPublication struct {
	desiredBody string
	mutations   []PendingMutation
	baseline    PendingReview
	submit      bool
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

// ReconcilePendingPublication accepts remote effects already equal to the
// desired local state and rejects external divergence from the captured base.
func (prepared PreparedPublication) ReconcilePendingPublication(
	remote PendingReview,
) (PreparedPublication, error) {
	if prepared.pending == nil || remote.nodeID != prepared.pending.baseline.nodeID ||
		remote.databaseID != prepared.pending.baseline.databaseID {
		return PreparedPublication{}, ErrInvalidPublication
	}
	comments := make(map[CommentNodeID]PendingComment, remote.CommentCount())
	markers := make(map[string][]PendingComment, remote.CommentCount())
	for _, comment := range remote.comments {
		comments[comment.nodeID] = comment
		_, marker := splitPendingMarker(comment.body, draftCommentMarkerPrefix)
		if marker != "" {
			markers[marker] = append(markers[marker], comment)
		}
	}
	remaining := make([]PendingMutation, 0, len(prepared.pending.mutations))
	for _, mutation := range prepared.pending.mutations {
		switch mutation.kind {
		case PendingMutationReviewBody:
			visible, _ := splitPendingMarker(remote.body, publicationMarkerPrefix)
			if visible == mutation.body && !prepared.pending.submit {
				continue
			}
			if visible == mutation.body {
				break
			}
			baseline, _ := splitPendingMarker(prepared.pending.baseline.body, publicationMarkerPrefix)
			if visible != baseline {
				return PreparedPublication{}, errors.New("pending review body changed on GitHub")
			}
		case PendingMutationUpdateComment:
			comment, exists := comments[mutation.nodeID]
			if !exists {
				return PreparedPublication{}, errors.New("pending review comment disappeared on GitHub")
			}
			visible, _ := splitPendingMarker(comment.body, draftCommentMarkerPrefix)
			if visible == mutation.body {
				continue
			}
			if visible != mutation.baseline {
				return PreparedPublication{}, errors.New("pending review comment changed on GitHub")
			}
		case PendingMutationDeleteComment:
			comment, exists := comments[mutation.nodeID]
			if !exists {
				continue
			}
			visible, _ := splitPendingMarker(comment.body, draftCommentMarkerPrefix)
			if visible != mutation.baseline {
				return PreparedPublication{}, errors.New("pending review comment changed on GitHub")
			}
		case PendingMutationAddComment:
			marker := stablePendingCommentMarker(prepared.pending.baseline.nodeID, mutation)
			matches := markers[marker]
			if len(matches) > 1 {
				return PreparedPublication{}, errors.New("pending review comment marker is ambiguous")
			}
			if len(matches) == 1 {
				visible, _ := splitPendingMarker(matches[0].body, draftCommentMarkerPrefix)
				if visible != mutation.body ||
					!pendingAdditionTargetEqual(matches[0].target, mutation.target) {
					return PreparedPublication{}, errors.New("pending review comment marker diverged on GitHub")
				}
				continue
			}
		default:
			return PreparedPublication{}, ErrInvalidPublication
		}
		remaining = append(remaining, mutation)
	}
	next := prepared
	pending := *prepared.pending
	pending.baseline = remote
	pending.baseline.comments = slices.Clone(remote.comments)
	pending.mutations = remaining
	next.pending = new(pending)
	return next, nil
}

func pendingAdditionTargetEqual(remote, desired PendingCommentTarget) bool {
	if remote.subject != desired.subject || remote.outdated ||
		remote.path.raw != desired.path.raw || remote.side != desired.side ||
		remote.startSide != desired.startSide {
		return false
	}
	return pendingLineEqual(remote.line, desired.line) &&
		pendingLineEqual(remote.startLine, desired.startLine)
}

func pendingLineEqual(left, right *LineNumber) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (current Review) preparePendingPublication(
	options PublicationOptions,
) (PreparedPublication, error) {
	if current.pending == nil || current.pending.nodeID == "" ||
		current.pending.databaseID == 0 || current.pending.commit == "" {
		return PreparedPublication{}, ErrInvalidPublication
	}
	if status := current.snapshot.PendingReviewSummary().Status; status != PendingReviewReady {
		return PreparedPublication{}, fmt.Errorf(
			"%w: pending GitHub review is %s",
			ErrInvalidPublication,
			status,
		)
	}
	if current.PendingReviewSummary().Conflicts != 0 {
		return PreparedPublication{}, fmt.Errorf("%w: pending review has unresolved conflicts", ErrInvalidPublication)
	}
	baseline, ok := current.snapshot.PendingReview()
	if !ok || baseline.nodeID != current.pending.nodeID ||
		baseline.databaseID != current.pending.databaseID {
		return PreparedPublication{}, ErrInvalidPublication
	}
	prepared := PreparedPublication{
		workspace: current.workspace,
		source:    current.snapshot.SourceID(),
		options:   options,
		decisions: make([]PublicationCommentDecision, 0, len(current.comments)),
	}
	pending := PreparedPendingPublication{
		baseline:    baseline,
		desiredBody: current.pending.bodyBase,
		submit:      options.event != PublicationEventDraft,
		mutations:   make([]PendingMutation, 0, len(current.comments)+1),
	}
	excluded := make(map[CommentID]struct{}, len(options.excluded))
	for _, id := range options.excluded {
		excluded[id] = struct{}{}
	}
	if err := current.appendPendingPublicationComments(
		options,
		excluded,
		&prepared,
		&pending,
	); err != nil {
		return PreparedPublication{}, err
	}
	if !current.pending.bodyUpdate &&
		(pending.submit || pending.desiredBody != current.pending.bodyBase) {
		return PreparedPublication{}, fmt.Errorf(
			"%w: pending review body is not editable",
			ErrInvalidPublication,
		)
	}
	pending.mutations = append([]PendingMutation{{
		kind:     PendingMutationReviewBody,
		body:     pending.desiredBody,
		baseline: current.pending.bodyBase,
	}}, pending.mutations...)
	prepared.pending = new(pending)
	retained := int64(len(pending.desiredBody))
	for _, mutation := range pending.mutations {
		retained += int64(len(mutation.body) + len(mutation.baseline) + len(mutation.marker) + 128)
	}
	if retained > MaxPublicationBytes {
		return PreparedPublication{}, fmt.Errorf("%w: prepared pending review exceeds %d bytes", ErrInvalidPublication, MaxPublicationBytes)
	}
	return prepared, nil
}

func (current Review) appendPendingPublicationComments(
	options PublicationOptions,
	excluded map[CommentID]struct{},
	prepared *PreparedPublication,
	pending *PreparedPendingPublication,
) error {
	for _, comment := range current.comments {
		binding, bound := current.pending.bindings[comment.id]
		_, manuallyExcluded := excluded[comment.id]
		if bound && manuallyExcluded {
			return fmt.Errorf(
				"%w: an existing GitHub draft cannot be excluded without deleting it",
				ErrInvalidPublication,
			)
		}
		if bound {
			if binding.deleted {
				if binding.kind == pendingBindingComment && !binding.canDelete {
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
						baseline:   binding.baseline,
						marker:     binding.marker,
						localID:    comment.id,
					})
				} else {
					pending.desiredBody = ""
				}
				continue
			}
			prepared.included++
			decision := PublicationIncludedInline
			if binding.kind == pendingBindingBody {
				pending.desiredBody = comment.body.String()
				decision = PublicationIncludedSummary
			} else if comment.body.String() != binding.baseline {
				if !binding.canUpdate {
					return fmt.Errorf(
						"%w: pending review comment is not editable",
						ErrInvalidPublication,
					)
				}
				pending.mutations = append(pending.mutations, PendingMutation{
					kind:       PendingMutationUpdateComment,
					nodeID:     binding.remoteNode,
					databaseID: binding.remoteID,
					body:       comment.body.String(),
					baseline:   binding.baseline,
					marker:     binding.marker,
					localID:    comment.id,
				})
			}
			prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
				id: comment.id, decision: decision,
			})
			continue
		}
		if manuallyExcluded {
			prepared.excluded++
			prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
				id: comment.id, decision: PublicationExcludedManual,
			})
			continue
		}
		mutation, decision, summary := prepareNewPendingComment(
			comment,
			options,
			current.pending.commit,
		)
		prepared.decisions = append(prepared.decisions, PublicationCommentDecision{
			id: comment.id, decision: decision,
		})
		if decision == PublicationExcludedInvalid || decision == PublicationExcludedStale ||
			decision == PublicationExcludedUnmappable {
			prepared.excluded++
			continue
		}
		prepared.included++
		if summary != "" {
			if pending.desiredBody != "" {
				pending.desiredBody += "\n\n"
			}
			pending.desiredBody += summary
			continue
		}
		pending.mutations = append(pending.mutations, mutation)
	}
	return nil
}

func prepareNewPendingComment(
	comment Comment,
	options PublicationOptions,
	commit ObjectID,
) (PendingMutation, PublicationDecision, string) {
	body := comment.body.String()
	target := comment.target
	if target.Kind() == TargetLine && !comment.stale && !comment.unmappable {
		path, hasPath := target.AnchoredPath()
		side, start, end, hasLines := target.Lines()
		pathUTF8, validUTF8 := path.UTF8()
		if hasPath && validUTF8 && hasLines {
			remoteTarget, err := NewPendingCommentTarget(PendingCommentTargetDescriptor{
				Path:           path,
				Commit:         commit,
				OriginalCommit: commit,
				Line:           new(end),
				StartLine:      pendingStartLine(start, end),
				Subject:        PendingSubjectLine,
				Side:           side,
				StartSide:      side,
			})
			if err == nil && pathUTF8 != "" {
				return PendingMutation{
					kind: PendingMutationAddComment, target: remoteTarget,
					body: body, localID: comment.id,
				}, PublicationIncludedInline, ""
			}
		}
	}
	if target.Kind() == TargetFile && !comment.unmappable {
		path, hasPath := target.AnchoredPath()
		if pathUTF8, validUTF8 := path.UTF8(); hasPath && validUTF8 && pathUTF8 != "" {
			remoteTarget, err := NewPendingCommentTarget(PendingCommentTargetDescriptor{
				Path:           path,
				Commit:         commit,
				OriginalCommit: commit,
				Subject:        PendingSubjectFile,
			})
			if err == nil {
				return PendingMutation{
					kind: PendingMutationAddComment, target: remoteTarget,
					body: body, localID: comment.id,
				}, PublicationIncludedInline, ""
			}
		}
	}
	if target.Kind() == TargetReview || target.Kind() == TargetFile ||
		options.policies.Has(PublicationMoveUnmappableToSummary) {
		if target.Kind() != TargetReview {
			if path, ok := target.AnchoredPath(); ok {
				body = "`" + markdownEscape(path.Display()) + "`: " + body
			}
		}
		return PendingMutation{}, PublicationIncludedSummary, body
	}
	decision := PublicationExcludedInvalid
	if comment.stale {
		decision = PublicationExcludedStale
	} else if comment.unmappable {
		decision = PublicationExcludedUnmappable
	}
	return PendingMutation{}, decision, ""
}

func pendingStartLine(start, end LineNumber) *LineNumber {
	if start == end {
		return nil
	}
	return new(start)
}

func encodePendingPublicationEnvelope(
	marker PublicationMarker,
	prepared PreparedPublication,
) (string, error) {
	if prepared.pending == nil || marker == "" {
		return "", ErrInvalidPublication
	}
	encoded, err := json.Marshal(struct {
		Marker  string `json:"marker"`
		Review  uint64 `json:"review"`
		Effects int    `json:"effects"`
	}{
		Marker:  marker.String(),
		Review:  prepared.pending.baseline.databaseID.Uint64(),
		Effects: len(prepared.pending.mutations),
	})
	if err != nil || len(encoded) > MaxPublicationBytes {
		return "", errors.New("pending publication envelope is invalid")
	}
	return string(encoded), nil
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
	hash := sha256.New()
	hash.Write([]byte(reviewID))
	hash.Write([]byte{0})
	hash.Write([]byte(strconv.FormatUint(uint64(mutation.localID), 10)))
	hash.Write([]byte{0})
	hash.Write([]byte(mutation.body))
	hash.Write([]byte{0})
	hash.Write([]byte(mutation.target.path.raw))
	sum := hash.Sum(nil)
	return draftCommentMarkerPrefix + fmt.Sprintf("%x", sum[:16]) + markerSuffix
}
