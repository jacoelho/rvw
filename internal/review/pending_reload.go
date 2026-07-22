package review

import (
	"slices"
	"strings"
)

func (preparation *replacementPreparation) mergePendingReview() error {
	status := preparation.snapshot.PendingReviewSummary().Status
	if preparation.original.pending == nil {
		if status != PendingReviewReady {
			preparation.next.pending = nil
			return nil
		}
		preparation.next.pending = nil
		merged, err := importPendingReview(preparation.next)
		if err != nil {
			return err
		}
		preparation.next = merged
		return nil
	}
	if status != PendingReviewReady {
		if status == PendingReviewAbsent {
			return preparation.mergeMissingPendingReview()
		}
		preparation.next.pending = preparation.original.pending.clone()
		return nil
	}
	remote, ok := preparation.snapshot.PendingReview()
	if !ok {
		return ErrInvalidPendingReview
	}
	if remote.nodeID != preparation.original.pending.nodeID ||
		remote.databaseID != preparation.original.pending.databaseID {
		return ErrInvalidPendingReview
	}
	return preparation.mergeReadyPendingReview(remote)
}

func (preparation *replacementPreparation) mergeMissingPendingReview() error {
	state := preparation.original.pending.clone()
	removed := make(map[CommentID]struct{})
	for id, binding := range state.bindings {
		comment, exists := preparation.next.commentByID(id)
		if !exists {
			continue
		}
		if binding.deleted {
			removed[id] = struct{}{}
			delete(state.bindings, id)
			delete(state.byNode, binding.remoteNode)
			continue
		}
		localChanged := binding.deleted || comment.body.String() != binding.baseline
		if !localChanged && binding.conflict == nil {
			removed[id] = struct{}{}
			delete(state.bindings, id)
			delete(state.byNode, binding.remoteNode)
			continue
		}
		binding.conflict = &pendingConflict{
			base:   binding.baseline,
			local:  comment.body.String(),
			remote: "",
		}
		state.bindings[id] = binding
	}
	removePendingComments(&preparation.next, removed)
	if len(state.bindings) == 0 {
		preparation.next.pending = nil
	} else {
		state.bodyID = pendingBodyID(state.bindings)
		preparation.next.pending = state
	}
	return nil
}

func (preparation *replacementPreparation) mergeReadyPendingReview(
	remote PendingReview,
) error {
	original := preparation.original.pending
	state := &pendingReviewState{
		bindings:   make(map[CommentID]pendingBinding, len(original.bindings)+remote.CommentCount()+1),
		byNode:     make(map[CommentNodeID]CommentID, remote.CommentCount()),
		nodeID:     remote.nodeID,
		databaseID: remote.databaseID,
		commit:     remote.commit,
		bodyUpdate: remote.viewerUpdate,
	}
	removed := make(map[CommentID]struct{})
	seenRemote := make(map[CommentNodeID]struct{}, remote.CommentCount())

	remoteBody, remoteBodyMarker := splitPendingMarker(remote.body, publicationMarkerPrefix)
	for id := range preparation.pendingSummaryCommentsApplied(remoteBody) {
		removed[id] = struct{}{}
	}
	if original.bodyID != nil {
		id := *original.bodyID
		binding := original.bindings[id]
		if len(removed) != 0 {
			if err := replacePendingCommentBody(&preparation.next, id, remoteBody); err != nil {
				return err
			}
		}
		if err := preparation.mergePendingBinding(
			state,
			id,
			binding,
			remoteBody,
			remoteBodyMarker,
			PendingCommentTarget{},
			remote.viewerUpdate,
			false,
			true,
			removed,
		); err != nil {
			return err
		}
	} else if strings.TrimSpace(remoteBody) != "" {
		id, err := appendPendingComment(
			&preparation.next,
			remoteBody,
			ReviewCommentTarget(),
			false,
			false,
		)
		if err != nil {
			return err
		}
		state.bodyID = new(id)
		state.bindings[id] = pendingBinding{
			kind: pendingBindingBody, baseline: strings.Clone(remoteBody),
			marker: strings.Clone(remoteBodyMarker), canUpdate: remote.viewerUpdate,
		}
	}
	state.bodyBase = strings.Clone(remoteBody)
	state.bodyMarker = strings.Clone(remoteBodyMarker)

	remoteByNode := make(map[CommentNodeID]PendingComment, remote.CommentCount())
	for _, comment := range remote.comments {
		remoteByNode[comment.nodeID] = comment
	}
	for node, id := range original.byNode {
		binding, exists := original.bindings[id]
		if !exists {
			return ErrInvalidPendingReview
		}
		remoteComment, remoteExists := remoteByNode[node]
		if !remoteExists {
			if err := preparation.mergeDeletedRemoteComment(state, id, binding, removed); err != nil {
				return err
			}
			continue
		}
		seenRemote[node] = struct{}{}
		body, marker := splitPendingMarker(remoteComment.body, draftCommentMarkerPrefix)
		if err := preparation.mergePendingBinding(
			state,
			id,
			binding,
			body,
			marker,
			remoteComment.target,
			remoteComment.viewerUpdate,
			remoteComment.viewerDelete,
			false,
			removed,
		); err != nil {
			return err
		}
	}
	if err := preparation.mergeNewPendingComments(
		state,
		original,
		remote,
		seenRemote,
		removed,
	); err != nil {
		return err
	}
	removePendingComments(&preparation.next, removed)
	state.bodyID = pendingBodyID(state.bindings)
	preparation.next.pending = state
	return nil
}

func (preparation *replacementPreparation) mergeNewPendingComments(
	state *pendingReviewState,
	original *pendingReviewState,
	remote PendingReview,
	seenRemote map[CommentNodeID]struct{},
	removed map[CommentID]struct{},
) error {
	localMarkers := preparation.pendingLocalCommentMarkers(remote.nodeID, original, removed)
	for _, remoteComment := range remote.comments {
		if _, seen := seenRemote[remoteComment.nodeID]; seen {
			continue
		}
		body, marker := splitPendingMarker(remoteComment.body, draftCommentMarkerPrefix)
		if localID, matched := localMarkers[marker]; matched && marker != "" {
			local, exists := preparation.next.commentByID(localID)
			if !exists {
				return ErrInvalidPendingReview
			}
			binding := pendingBinding{
				kind:       pendingBindingComment,
				remoteNode: remoteComment.nodeID,
				remoteID:   remoteComment.databaseID,
				target:     remoteComment.target,
				baseline:   strings.Clone(body),
				marker:     strings.Clone(marker),
				canUpdate:  remoteComment.viewerUpdate,
				canDelete:  remoteComment.viewerDelete,
			}
			if local.body.String() != body {
				binding.conflict = &pendingConflict{
					base: "", local: local.body.String(), remote: body,
				}
			} else {
				target, stale, unmappable := projectPendingTarget(preparation.snapshot, remoteComment.target)
				if err := replacePendingCommentTarget(
					&preparation.next,
					localID,
					target,
					stale,
					unmappable,
				); err != nil {
					return err
				}
			}
			state.bindings[localID] = binding
			state.byNode[remoteComment.nodeID] = localID
			continue
		}
		target, stale, unmappable := projectPendingTarget(preparation.snapshot, remoteComment.target)
		id, err := appendPendingComment(&preparation.next, body, target, stale, unmappable)
		if err != nil {
			return err
		}
		state.bindings[id] = pendingBinding{
			kind:       pendingBindingComment,
			remoteNode: remoteComment.nodeID,
			remoteID:   remoteComment.databaseID,
			target:     remoteComment.target,
			baseline:   strings.Clone(body),
			marker:     strings.Clone(marker),
			canUpdate:  remoteComment.viewerUpdate,
			canDelete:  remoteComment.viewerDelete,
		}
		state.byNode[remoteComment.nodeID] = id
	}
	return nil
}

func (preparation *replacementPreparation) pendingSummaryCommentsApplied(
	remoteBody string,
) map[CommentID]struct{} {
	consumed := make(map[CommentID]struct{})
	desired := ""
	if preparation.original.pending.bodyID != nil {
		if body, ok := preparation.next.commentByID(*preparation.original.pending.bodyID); ok &&
			!preparation.original.pending.bindings[body.id].deleted {
			desired = body.body.String()
		}
	}
	for _, comment := range preparation.next.comments {
		if _, bound := preparation.original.pending.bindings[comment.id]; bound ||
			comment.target.Kind() != TargetReview {
			continue
		}
		if desired != "" {
			desired += "\n\n"
		}
		desired += comment.body.String()
		consumed[comment.id] = struct{}{}
	}
	if len(consumed) == 0 || desired != remoteBody {
		return nil
	}
	return consumed
}

func (preparation *replacementPreparation) pendingLocalCommentMarkers(
	reviewID ReviewNodeID,
	original *pendingReviewState,
	removed map[CommentID]struct{},
) map[string]CommentID {
	markers := make(map[string]CommentID)
	duplicates := make(map[string]struct{})
	options := PublicationOptions{event: PublicationEventDraft}
	for _, comment := range preparation.next.comments {
		if _, bound := original.bindings[comment.id]; bound {
			continue
		}
		if _, remove := removed[comment.id]; remove {
			continue
		}
		mutation, decision, summary := prepareNewPendingComment(
			comment,
			options,
			original.commit,
		)
		if summary != "" || decision != PublicationIncludedInline {
			continue
		}
		marker := stablePendingCommentMarker(reviewID, mutation)
		if _, exists := markers[marker]; exists {
			duplicates[marker] = struct{}{}
		}
		markers[marker] = comment.id
	}
	for marker := range duplicates {
		delete(markers, marker)
	}
	return markers
}

func (preparation *replacementPreparation) mergePendingBinding(
	state *pendingReviewState,
	id CommentID,
	binding pendingBinding,
	remoteBody, remoteMarker string,
	remoteTarget PendingCommentTarget,
	canUpdate, canDelete, bodyBinding bool,
	removed map[CommentID]struct{},
) error {
	comment, exists := preparation.next.commentByID(id)
	if !exists {
		return ErrInvalidPendingReview
	}
	localBody := comment.body.String()
	localChanged := binding.deleted || localBody != binding.baseline || binding.conflict != nil
	remoteChanged := remoteBody != binding.baseline
	if !localChanged && remoteChanged {
		if strings.TrimSpace(remoteBody) == "" {
			removed[id] = struct{}{}
			return nil
		}
		if err := replacePendingCommentBody(&preparation.next, id, remoteBody); err != nil {
			return err
		}
		localBody = remoteBody
	}
	if localChanged && remoteChanged {
		if !binding.deleted && localBody == remoteBody {
			binding.conflict = nil
			binding.deleted = false
		} else {
			binding.conflict = &pendingConflict{
				base:         binding.baseline,
				local:        localBody,
				remote:       remoteBody,
				localDeleted: binding.deleted,
			}
		}
	}
	preserveConflict := !remoteChanged && binding.conflict != nil &&
		binding.conflict.remote == remoteBody
	if !preserveConflict && (!localChanged || localBody == remoteBody) {
		binding.conflict = nil
	}
	binding.baseline = strings.Clone(remoteBody)
	binding.marker = strings.Clone(remoteMarker)
	binding.canUpdate = canUpdate
	binding.canDelete = canDelete
	if bodyBinding {
		binding.kind = pendingBindingBody
		state.bodyID = new(id)
	} else {
		binding.kind = pendingBindingComment
		binding.target = remoteTarget
		state.byNode[binding.remoteNode] = id
		if !localChanged {
			target, stale, unmappable := projectPendingTarget(preparation.snapshot, remoteTarget)
			if err := replacePendingCommentTarget(&preparation.next, id, target, stale, unmappable); err != nil {
				return err
			}
		}
	}
	state.bindings[id] = binding
	return nil
}

func (preparation *replacementPreparation) mergeDeletedRemoteComment(
	state *pendingReviewState,
	id CommentID,
	binding pendingBinding,
	removed map[CommentID]struct{},
) error {
	comment, exists := preparation.next.commentByID(id)
	if !exists {
		return ErrInvalidPendingReview
	}
	if binding.deleted || comment.body.String() == binding.baseline && binding.conflict == nil {
		removed[id] = struct{}{}
		return nil
	}
	binding.conflict = &pendingConflict{
		base:   binding.baseline,
		local:  comment.body.String(),
		remote: "",
	}
	state.bindings[id] = binding
	return nil
}

func replacePendingCommentBody(current *Review, id CommentID, body string) error {
	validated, err := NewCommentBody(body)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(current.comments, func(comment Comment) bool { return comment.id == id })
	if index < 0 || current.commentBody-int64(len(current.comments[index].body))+int64(len(body)) >
		MaxCommentBodyTotal {
		return ErrInvalidPendingReview
	}
	current.comments = slices.Clone(current.comments)
	current.commentBody -= int64(len(current.comments[index].body))
	current.comments[index].body = validated
	current.commentBody += int64(len(validated))
	return nil
}

func replacePendingCommentTarget(
	current *Review,
	id CommentID,
	target CommentTarget,
	stale, unmappable bool,
) error {
	index := slices.IndexFunc(current.comments, func(comment Comment) bool { return comment.id == id })
	if index < 0 {
		return ErrInvalidPendingReview
	}
	current.comments = slices.Clone(current.comments)
	current.comments[index].target = target
	current.comments[index].stale = stale
	current.comments[index].unmappable = unmappable
	return nil
}

func removePendingComments(current *Review, removed map[CommentID]struct{}) {
	if len(removed) == 0 {
		current.commentAnchors = indexCommentAnchors(current.comments)
		return
	}
	retained := make([]Comment, 0, len(current.comments)-len(removed))
	current.commentBody = 0
	for _, comment := range current.comments {
		if _, remove := removed[comment.id]; remove {
			continue
		}
		retained = append(retained, comment)
		current.commentBody += int64(len(comment.body))
	}
	current.comments = retained
	current.commentAnchors = indexCommentAnchors(current.comments)
}

func pendingBodyID(bindings map[CommentID]pendingBinding) *CommentID {
	for id, binding := range bindings {
		if binding.kind == pendingBindingBody {
			return new(id)
		}
	}
	return nil
}

func rebasePendingCapture(current Review, remote PendingReview) (Review, error) {
	snapshot, err := current.snapshot.WithPendingReview(remote)
	if err != nil {
		return current, err
	}
	preparation := replacementPreparation{
		original: current,
		next:     current.clone(),
		snapshot: snapshot,
	}
	preparation.next.snapshot = snapshot
	if err := preparation.mergeReadyPendingReview(remote); err != nil {
		return current, err
	}
	return preparation.next, nil
}
