package change

import "strings"

func (preparation *replacementPreparation) mergePendingReview() error {
	status := preparation.snapshot.PendingReviewSummary().Status
	if preparation.original.pending == nil {
		if status != PendingReviewReady {
			preparation.next.pending = nil
			return nil
		}
		preparation.next.pending = nil
		merged, err := importPendingReviewCancellable(preparation.next, preparation.cancelled)
		if err != nil {
			return err
		}
		preparation.next = merged
		indexes, err := indexPendingCommentsCancellable(
			preparation.cancelled,
			preparation.next.comments,
		)
		if err != nil {
			return err
		}
		preparation.commentIndexes = indexes
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
	state, err := preparePendingCommentStateCancellable(
		preparation.cancelled,
		preparation.original.pending,
		preparation.next.commentState,
		preparation.pendingFiles,
		remote,
	)
	if err != nil {
		return err
	}
	preparation.next.commentState = state
	indexes, err := indexPendingCommentsCancellable(preparation.cancelled, state.comments)
	if err != nil {
		return err
	}
	preparation.commentIndexes = indexes
	return nil
}

func (preparation *replacementPreparation) mergeMissingPendingReview() error {
	original := preparation.original.pending
	state := *original
	state.bindings = make(map[CommentID]pendingBinding)
	state.byNode = make(map[CommentNodeID]CommentID)
	removed := make(map[CommentID]struct{})
	count := 0
	for id, binding := range original.bindings {
		if count%256 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return err
			}
		}
		count++
		if binding.conflict != nil {
			conflict := *binding.conflict
			binding.conflict = new(conflict)
		}
		comment, exists := preparation.comment(id)
		if !exists {
			state.bindings[id] = binding
			if binding.kind == pendingBindingComment {
				state.byNode[binding.remoteNode] = id
			}
			continue
		}
		if binding.deleted {
			removed[id] = struct{}{}
			continue
		}
		local := commentContentFrom(comment)
		localChanged := binding.deleted || local != binding.baseline
		if !localChanged && binding.conflict == nil {
			removed[id] = struct{}{}
			continue
		}
		binding.conflict = &pendingConflict{
			base:   cloneCommentContent(binding.baseline),
			local:  cloneCommentContent(local),
			remote: commentContent{},
		}
		state.bindings[id] = binding
		if binding.kind == pendingBindingComment {
			state.byNode[binding.remoteNode] = id
		}
	}
	if err := preparation.removePendingComments(removed); err != nil {
		return err
	}
	if len(state.bindings) == 0 {
		preparation.next.pending = nil
	} else {
		state.bodyID = pendingBodyID(state.bindings)
		preparation.next.pending = &state
	}
	return nil
}

func (preparation *replacementPreparation) mergeReadyPendingReview(
	remote PendingReview,
) error {
	original := preparation.original.pending
	state := &pendingReviewState{
		bindings:   make(map[CommentID]pendingBinding),
		byNode:     make(map[CommentNodeID]CommentID),
		nodeID:     remote.nodeID,
		databaseID: remote.databaseID,
		commit:     remote.commit,
		bodyUpdate: remote.viewerUpdate,
	}
	removed := make(map[CommentID]struct{})
	seenRemote := make(map[CommentNodeID]struct{}, remote.CommentCount())

	remoteBody, remoteBodyMarker := splitPendingMarker(remote.body, publicationMarkerPrefix)
	remoteBodyContent := commentContent{body: remoteBody}
	if remoteBodyMarker != "" {
		remoteBodyContent = decodeOwnedCommentWire(remoteBody)
	}
	applied, err := preparation.pendingSummaryCommentsApplied(remoteBody)
	if err != nil {
		return err
	}
	count := 0
	for id := range applied {
		if count%256 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return err
			}
		}
		count++
		removed[id] = struct{}{}
	}
	if original.bodyID != nil {
		id := *original.bodyID
		binding := original.bindings[id]
		if len(removed) != 0 {
			if err := preparation.replacePendingComment(id, remoteBodyContent); err != nil {
				return err
			}
		}
		if err := preparation.mergePendingBody(
			state,
			id,
			binding,
			remoteBodyContent,
			remoteBody,
			remoteBodyMarker,
			removed,
		); err != nil {
			return err
		}
	} else if strings.TrimSpace(remoteBody) != "" {
		id, err := preparation.appendPendingComment(
			remoteBodyContent,
			ReviewCommentTarget(),
			commentFlags{},
		)
		if err != nil {
			return err
		}
		state.bodyID = new(id)
		state.bindings[id] = pendingBinding{
			kind: pendingBindingBody, baseline: cloneCommentContent(remoteBodyContent),
			wireBaseline: strings.Clone(remoteBody), marker: strings.Clone(remoteBodyMarker),
		}
	}
	state.bodyBase = strings.Clone(remoteBody)
	state.bodyMarker = strings.Clone(remoteBodyMarker)

	remoteByNode, mapErr := preparation.pendingCommentsByNode(remote)
	if mapErr != nil {
		return mapErr
	}
	if err := preparation.mergeExistingPendingComments(
		state,
		original,
		remoteByNode,
		seenRemote,
		removed,
	); err != nil {
		return err
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
	if err := preparation.removePendingComments(removed); err != nil {
		return err
	}
	state.bodyID = pendingBodyID(state.bindings)
	preparation.next.pending = state
	return nil
}

func (preparation *replacementPreparation) mergeExistingPendingComments(
	state *pendingReviewState,
	original *pendingReviewState,
	remoteByNode map[CommentNodeID]PendingComment,
	seenRemote map[CommentNodeID]struct{},
	removed map[CommentID]struct{},
) error {
	count := 0
	for node, id := range original.byNode {
		if count%256 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return err
			}
		}
		count++
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
		content := commentContent{body: body}
		if marker != "" {
			content = decodeOwnedCommentWire(body)
		}
		if err := preparation.mergePendingComment(
			state,
			id,
			binding,
			remoteComment,
			content,
			body,
			marker,
			removed,
		); err != nil {
			return err
		}
	}
	return nil
}

func (preparation *replacementPreparation) pendingCommentsByNode(
	remote PendingReview,
) (map[CommentNodeID]PendingComment, error) {
	comments := make(map[CommentNodeID]PendingComment, remote.CommentCount())
	for index, comment := range remote.comments {
		if index%256 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return nil, err
			}
		}
		comments[comment.nodeID] = comment
	}
	return comments, nil
}

func (preparation *replacementPreparation) mergeNewPendingComments(
	state *pendingReviewState,
	original *pendingReviewState,
	remote PendingReview,
	seenRemote map[CommentNodeID]struct{},
	removed map[CommentID]struct{},
) error {
	localMarkers, err := preparation.pendingLocalCommentMarkers(remote.nodeID, original, removed)
	if err != nil {
		return err
	}
	for index, remoteComment := range remote.comments {
		if index%256 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return err
			}
		}
		if _, seen := seenRemote[remoteComment.nodeID]; seen {
			continue
		}
		body, marker := splitPendingMarker(remoteComment.body, draftCommentMarkerPrefix)
		content := commentContent{body: body}
		if marker != "" {
			content = decodeOwnedCommentWire(body)
		}
		if localID, matched := localMarkers[marker]; matched && marker != "" {
			local, exists := preparation.comment(localID)
			if !exists {
				return ErrInvalidPendingReview
			}
			binding := pendingBinding{
				kind:         pendingBindingComment,
				remoteNode:   remoteComment.nodeID,
				remoteID:     remoteComment.databaseID,
				target:       remoteComment.target,
				baseline:     cloneCommentContent(content),
				wireBaseline: strings.Clone(body),
				marker:       strings.Clone(marker),
				capabilities: remoteComment.capabilities,
			}
			if commentContentFrom(local) != content {
				binding.conflict = &pendingConflict{
					base:   commentContent{},
					local:  cloneCommentContent(commentContentFrom(local)),
					remote: cloneCommentContent(content),
				}
			} else {
				target, flags := preparation.projectPendingTarget(remoteComment.target)
				if err := preparation.replacePendingCommentTarget(localID, target, flags); err != nil {
					return err
				}
			}
			state.bindings[localID] = binding
			state.byNode[remoteComment.nodeID] = localID
			continue
		}
		target, flags := preparation.projectPendingTarget(remoteComment.target)
		id, err := preparation.appendPendingComment(content, target, flags)
		if err != nil {
			return err
		}
		state.bindings[id] = pendingBinding{
			kind:         pendingBindingComment,
			remoteNode:   remoteComment.nodeID,
			remoteID:     remoteComment.databaseID,
			target:       remoteComment.target,
			baseline:     cloneCommentContent(content),
			wireBaseline: strings.Clone(body),
			marker:       strings.Clone(marker),
			capabilities: remoteComment.capabilities,
		}
		state.byNode[remoteComment.nodeID] = id
	}
	return nil
}

func (preparation *replacementPreparation) pendingSummaryCommentsApplied(
	remoteBody string,
) (map[CommentID]struct{}, error) {
	consumed := make([]CommentID, 0)
	offset := 0
	wrote := false
	matches := true
	match := func(body string) {
		if wrote {
			matches = matches && len(remoteBody)-offset >= 2 && remoteBody[offset:offset+2] == "\n\n"
			offset += min(2, len(remoteBody)-offset)
		}
		matches = matches && len(remoteBody)-offset >= len(body) &&
			remoteBody[offset:offset+min(len(body), len(remoteBody)-offset)] == body
		offset += min(len(body), len(remoteBody)-offset)
		wrote = true
	}
	if preparation.original.pending.bodyID != nil {
		if comment, ok := preparation.comment(*preparation.original.pending.bodyID); ok {
			binding := preparation.original.pending.bindings[comment.id]
			if !binding.deleted {
				wire := binding.wireBaseline
				if commentContentFrom(comment) != binding.baseline {
					var err error
					wire, err = encodePublicationSummaryWire(
						commentContentFrom(comment), comment.target,
					)
					if err != nil {
						return nil, err
					}
				}
				match(wire)
			}
		}
	}
	for index, comment := range preparation.next.comments {
		if index%256 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return nil, err
			}
		}
		if _, bound := preparation.original.pending.bindings[comment.id]; bound ||
			comment.target.Kind() != TargetReview {
			continue
		}
		wire, err := encodePublicationSummaryWire(
			commentContentFrom(comment), comment.target,
		)
		if err != nil {
			return nil, err
		}
		match(wire)
		consumed = append(consumed, comment.id)
	}
	if len(consumed) == 0 || !matches || offset != len(remoteBody) {
		return map[CommentID]struct{}{}, nil
	}
	applied := make(map[CommentID]struct{})
	for _, id := range consumed {
		applied[id] = struct{}{}
	}
	return applied, nil
}

func (preparation *replacementPreparation) pendingLocalCommentMarkers(
	reviewID ReviewNodeID,
	original *pendingReviewState,
	removed map[CommentID]struct{},
) (map[string]CommentID, error) {
	markers := make(map[string]CommentID)
	duplicates := make(map[string]struct{})
	options := PublicationOptions{event: PublicationEventDraft}
	for index, comment := range preparation.next.comments {
		if index%256 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return nil, err
			}
		}
		if _, bound := original.bindings[comment.id]; bound {
			continue
		}
		if _, remove := removed[comment.id]; remove {
			continue
		}
		mutation, decision, summary, prepareErr := prepareNewPendingComment(
			comment,
			options,
			original.commit,
		)
		if prepareErr != nil {
			return nil, prepareErr
		}
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
	return markers, nil
}

type pendingTextMergeDisposition uint8

const (
	pendingTextProjectRemote pendingTextMergeDisposition = iota + 1
	pendingTextPreserveLocal
	pendingTextRemoved
)

type pendingTextMerge struct {
	binding     pendingBinding
	disposition pendingTextMergeDisposition
}

func (preparation *replacementPreparation) mergePendingBody(
	state *pendingReviewState,
	id CommentID,
	binding pendingBinding,
	remoteContent commentContent,
	remoteWire, remoteMarker string,
	removed map[CommentID]struct{},
) error {
	merged, err := preparation.mergePendingText(id, binding, remoteContent, remoteWire, removed)
	if err != nil || merged.disposition == pendingTextRemoved {
		return err
	}
	binding = merged.binding
	binding.marker = strings.Clone(remoteMarker)
	binding.kind = pendingBindingBody
	state.bodyID = new(id)
	state.bindings[id] = binding
	return nil
}

func (preparation *replacementPreparation) mergePendingComment(
	state *pendingReviewState,
	id CommentID,
	binding pendingBinding,
	remote PendingComment,
	remoteContent commentContent,
	remoteWire, remoteMarker string,
	removed map[CommentID]struct{},
) error {
	merged, err := preparation.mergePendingText(id, binding, remoteContent, remoteWire, removed)
	if err != nil || merged.disposition == pendingTextRemoved {
		return err
	}
	binding = merged.binding
	binding.marker = strings.Clone(remoteMarker)
	binding.kind = pendingBindingComment
	binding.target = remote.target
	binding.capabilities = remote.capabilities
	state.byNode[binding.remoteNode] = id
	if merged.disposition == pendingTextProjectRemote {
		target, flags := preparation.projectPendingTarget(remote.target)
		if err := preparation.replacePendingCommentTarget(id, target, flags); err != nil {
			return err
		}
	}
	state.bindings[id] = binding
	return nil
}

func (preparation *replacementPreparation) mergePendingText(
	id CommentID,
	binding pendingBinding,
	remoteContent commentContent,
	remoteWire string,
	removed map[CommentID]struct{},
) (pendingTextMerge, error) {
	comment, exists := preparation.comment(id)
	if !exists {
		return pendingTextMerge{}, ErrInvalidPendingReview
	}
	localContent := commentContentFrom(comment)
	localChanged := binding.deleted || localContent != binding.baseline || binding.conflict != nil
	remoteChanged := remoteContent != binding.baseline
	if !localChanged && remoteChanged {
		if strings.TrimSpace(remoteContent.body) == "" {
			removed[id] = struct{}{}
			return pendingTextMerge{binding: binding, disposition: pendingTextRemoved}, nil
		}
		if err := preparation.replacePendingComment(id, remoteContent); err != nil {
			return pendingTextMerge{}, err
		}
		localContent = remoteContent
	}
	if localChanged && remoteChanged {
		if !binding.deleted && localContent == remoteContent {
			binding.conflict = nil
		} else {
			binding.conflict = &pendingConflict{
				base:         cloneCommentContent(binding.baseline),
				local:        cloneCommentContent(localContent),
				remote:       cloneCommentContent(remoteContent),
				localDeleted: binding.deleted,
			}
		}
	}
	preserveConflict := !remoteChanged && binding.conflict != nil &&
		binding.conflict.remote == remoteContent
	if !preserveConflict && (!localChanged || localContent == remoteContent) {
		binding.conflict = nil
	}
	binding.baseline = cloneCommentContent(remoteContent)
	binding.wireBaseline = strings.Clone(remoteWire)
	disposition := pendingTextProjectRemote
	if localChanged {
		disposition = pendingTextPreserveLocal
	}
	return pendingTextMerge{binding: binding, disposition: disposition}, nil
}

func (preparation *replacementPreparation) mergeDeletedRemoteComment(
	state *pendingReviewState,
	id CommentID,
	binding pendingBinding,
	removed map[CommentID]struct{},
) error {
	comment, exists := preparation.comment(id)
	if !exists {
		return ErrInvalidPendingReview
	}
	local := commentContentFrom(comment)
	if binding.deleted || local == binding.baseline && binding.conflict == nil {
		removed[id] = struct{}{}
		return nil
	}
	binding.conflict = &pendingConflict{
		base:   cloneCommentContent(binding.baseline),
		local:  cloneCommentContent(local),
		remote: commentContent{},
	}
	state.bindings[id] = binding
	return nil
}

func (preparation *replacementPreparation) comment(id CommentID) (Comment, bool) {
	index, exists := preparation.commentIndexes[id]
	if !exists || index < 0 || index >= len(preparation.next.comments) ||
		preparation.next.comments[index].id != id {
		return Comment{}, false
	}
	return preparation.next.comments[index], true
}

func (preparation *replacementPreparation) replacePendingComment(
	id CommentID,
	content commentContent,
) error {
	validated, err := NewCommentBody(content.body)
	if err != nil {
		return err
	}
	index, exists := preparation.commentIndexes[id]
	if !exists || index < 0 || index >= len(preparation.next.comments) ||
		preparation.next.comments[index].id != id ||
		preparation.next.commentBody-int64(len(preparation.next.comments[index].body))+
			int64(len(validated)) > MaxCommentBodyTotal {
		return ErrInvalidPendingReview
	}
	preparation.next.commentBody -= int64(len(preparation.next.comments[index].body))
	preparation.next.comments[index].body = validated
	preparation.next.commentBody += int64(len(validated))
	return nil
}

func (preparation *replacementPreparation) replacePendingCommentTarget(
	id CommentID,
	target CommentTarget,
	flags commentFlags,
) error {
	index, exists := preparation.commentIndexes[id]
	if !exists || index < 0 || index >= len(preparation.next.comments) ||
		preparation.next.comments[index].id != id || !flags.valid() {
		return ErrInvalidPendingReview
	}
	preparation.next.comments[index].target = target
	preparation.next.comments[index].flags = flags
	return nil
}

func (preparation *replacementPreparation) appendPendingComment(
	content commentContent,
	target CommentTarget,
	flags commentFlags,
) (CommentID, error) {
	validated, err := NewCommentBody(content.body)
	if err != nil || len(preparation.next.comments) >= MaxComments ||
		preparation.next.nextComment >= MaxComments ||
		preparation.next.commentBody+int64(len(validated)) > MaxCommentBodyTotal ||
		!flags.valid() {
		return 0, ErrInvalidPendingReview
	}
	id := CommentID(preparation.next.nextComment)
	preparation.commentIndexes[id] = len(preparation.next.comments)
	preparation.next.comments = append(preparation.next.comments, Comment{
		id: id, target: target, body: validated,
		flags: flags,
	})
	preparation.next.commentBody += int64(len(validated))
	preparation.next.nextComment++
	return id, nil
}

func (preparation *replacementPreparation) removePendingComments(
	removed map[CommentID]struct{},
) error {
	if len(removed) == 0 {
		return nil
	}
	retained := preparation.next.comments[:0]
	preparation.next.commentBody = 0
	count := 0
	for _, comment := range preparation.next.comments {
		if count%256 == 0 {
			if err := cancellationError(preparation.cancelled); err != nil {
				return err
			}
		}
		count++
		if _, remove := removed[comment.id]; remove {
			continue
		}
		retained = append(retained, comment)
		preparation.next.commentBody += int64(len(comment.body))
	}
	clear(preparation.next.comments[len(retained):])
	preparation.next.comments = retained
	indexes, err := indexPendingCommentsCancellable(preparation.cancelled, retained)
	if err != nil {
		return err
	}
	preparation.commentIndexes = indexes
	return nil
}

func (preparation *replacementPreparation) projectPendingTarget(
	target PendingCommentTarget,
) (CommentTarget, commentFlags) {
	return projectPendingTargetWithMappings(preparation.pendingFiles, target)
}

func pendingBodyID(bindings map[CommentID]pendingBinding) *CommentID {
	for id, binding := range bindings {
		if binding.kind == pendingBindingBody {
			return new(id)
		}
	}
	return nil
}

func indexPendingCommentsCancellable(
	cancelled func() error,
	comments []Comment,
) (map[CommentID]int, error) {
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	indexes := make(map[CommentID]int, len(comments))
	for index, comment := range comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return nil, err
			}
		}
		indexes[comment.id] = index
	}
	return indexes, nil
}
