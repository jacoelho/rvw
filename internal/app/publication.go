package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/review"
)

func (model *Model) startPublicationCheck() {
	summary := model.review.PublicationSummary()
	if summary.Status == review.PublicationUnknown {
		model.startReconciliation()
		return
	}
	if _, ok := model.document.PullRequest(); !ok {
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: "GitHub publication is available only for a pull-request source",
		}
		return
	}
	if model.publicationOperation != 0 {
		model.notice = &Notice{Kind: NoticeInformation, Text: "publication work is already active"}
		model.overlay = OverlaySubmit
		return
	}
	options, err := model.publicationOptions()
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	operationID, err := model.nextOperationID()
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	current := model.review
	next, request, err := current.StartPublicationCheck(operationID, options)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	if err := model.coordinator.SubmitPublicationCheck(PublicationCheckWork{
		ID:       operationID,
		Document: model.document,
		Review:   current,
		Request:  request,
	}); err != nil {
		model.review = failPublicationCheck(next, request, err)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.publicationOperation = operationID
	model.publicationCancel = false
	model.overlay = OverlaySubmit
	model.notice = &Notice{Kind: NoticeInformation, Text: "checking publication preflight"}
}

func (model *Model) publicationOptions() (review.PublicationOptions, error) {
	var policies []review.PublicationPolicy
	configured := model.settings.publication.Policies()
	if configured.Has(review.PublicationPrefixKinds) {
		policies = append(policies, review.PublicationPrefixKinds)
	}
	if configured.Has(review.PublicationMoveUnmappableToSummary) ||
		model.publicationMoveSummary {
		policies = append(policies, review.PublicationMoveUnmappableToSummary)
	}
	if configured.Has(review.PublicationAllowStaleHead) ||
		model.publicationAllowStale {
		policies = append(policies, review.PublicationAllowStaleHead)
	}
	typedPolicies, err := review.NewPublicationPolicies(policies...)
	if err != nil {
		return review.PublicationOptions{}, err
	}
	options, err := review.NewPublicationOptions(
		model.settings.publication.Event(),
		typedPolicies,
	)
	if err != nil {
		return review.PublicationOptions{}, err
	}
	excluded := make([]review.CommentID, 0, len(model.publicationExcluded))
	for id := range model.publicationExcluded {
		excluded = append(excluded, id)
	}
	slices.Sort(excluded)
	return options.WithExcludedComments(excluded)
}

func (model *Model) nextOperationID() (review.OperationID, error) {
	operationID, err := review.NewOperationID(model.nextOperation)
	if err != nil {
		return 0, errors.New("operation identity exhausted")
	}
	model.nextOperation++
	return operationID, nil
}

func failPublicationCheck(
	current review.Review,
	request review.PublicationCheckRequest,
	failure error,
) review.Review {
	detail := boundedOperationDetail(failure.Error())
	next, err := current.ApplyPublicationCheck(review.PublicationCheckResult{
		Operation: request.Operation,
		Workspace: request.Workspace,
		Source:    request.Source,
		Detail:    detail,
	})
	if err != nil {
		return current
	}
	next, err = next.ResolveOperation(
		request.Operation,
		review.OperationFailed,
		detail,
	)
	if err != nil {
		return current
	}
	return next
}

func (model *Model) applyPublicationOutcome(outcome PublicationOutcome) {
	if outcome.ID == 0 || outcome.ID != model.publicationOperation {
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale publication outcome"}
		return
	}
	model.publicationOperation = 0
	switch outcome.Stage {
	case PublicationCheckCompleted:
		model.applyPublicationCheckOutcome(outcome)
	case PublicationCommitCompleted:
		model.applyPublicationCommitOutcome(outcome)
	case PublicationReconciliationCompleted:
		model.applyReconciliationOutcome(outcome)
	default:
		model.notice = &Notice{Kind: NoticeError, Text: "invalid publication outcome stage"}
	}
}

func (model *Model) applyPublicationCheckOutcome(outcome PublicationOutcome) {
	result := outcome.Check
	if outcome.Err != nil {
		result = review.PublicationCheckResult{
			Operation: outcome.ID,
			Workspace: model.review.WorkspaceRevision(),
			Source:    model.review.Snapshot().SourceID(),
			Detail:    boundedOperationDetail(outcome.Err.Error()),
		}
	}
	next, err := model.review.ApplyPublicationCheck(result)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	status := review.OperationSucceeded
	if result.Checked == nil {
		status = review.OperationFailed
	}
	next, err = next.ResolveOperation(outcome.ID, status, result.Detail)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	if model.publicationCancel {
		model.publicationCancel = false
		if result.Checked != nil {
			model.review, _ = model.review.CancelPublicationCheck()
		}
		model.overlay = OverlayNone
		model.notice = &Notice{Kind: NoticeInformation, Text: "publication preflight cancelled"}
		return
	}
	if result.Checked == nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(result.Detail)}
		return
	}
	model.notice = &Notice{Kind: NoticeInformation, Text: "publication preflight is ready"}
}

func (model *Model) startPublication() {
	operationID, err := model.nextOperationID()
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	next, request, err := model.review.StartPublication(operationID)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	if err := model.coordinator.SubmitPublication(PublicationWork{
		ID:      operationID,
		Request: request,
	}); err != nil {
		result := review.PublicationResult{
			Operation:   operationID,
			Workspace:   request.Workspace,
			Source:      request.Source,
			Disposition: review.PublicationDispositionFailedBeforeCommit,
			Detail:      boundedOperationDetail(err.Error()),
		}
		if next, applyErr := next.ApplyPublication(result); applyErr == nil {
			model.review = resolvePublicationOperation(
				next,
				operationID,
				review.OperationFailed,
				result.Detail,
			)
		}
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.publicationOperation = operationID
	model.publicationCancel = false
	model.notice = &Notice{Kind: NoticeInformation, Text: "publishing GitHub review"}
}

func (model *Model) applyPublicationCommitOutcome(outcome PublicationOutcome) {
	next, err := model.review.ApplyPublication(outcome.Publication)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	status := review.OperationSucceeded
	switch outcome.Publication.Disposition {
	case review.PublicationDispositionUnknown:
		status = review.OperationUnknown
	case review.PublicationDispositionFailedBeforeCommit,
		review.PublicationDispositionCancelledBeforeCommit:
		status = review.OperationFailed
	}
	next = resolvePublicationOperation(
		next,
		outcome.ID,
		status,
		outcome.Publication.Detail,
	)
	model.review = next
	model.publicationCancel = false
	if outcome.Publication.Disposition == review.PublicationDispositionUnknown {
		model.notice = &Notice{
			Kind: NoticeError,
			Text: "publication outcome is unknown; reconciling exact remote marker",
		}
		model.startReconciliation()
		return
	}
	if outcome.Publication.Disposition == review.PublicationDispositionCommitted {
		model.notice = &Notice{Kind: NoticeInformation, Text: "GitHub review published"}
	} else {
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText(outcome.Publication.Detail),
		}
	}
}

func (model *Model) startReconciliation() {
	if model.publicationOperation != 0 {
		model.notice = &Notice{Kind: NoticeInformation, Text: "publication work is already active"}
		return
	}
	operationID, err := model.nextOperationID()
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	next, request, err := model.review.StartReconciliation(operationID)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	if err := model.coordinator.SubmitReconciliation(ReconciliationWork{
		ID:      operationID,
		Request: request,
	}); err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.publicationOperation = operationID
	model.overlay = OverlaySubmit
	model.notice = &Notice{Kind: NoticeInformation, Text: "reconciling GitHub publication"}
}

func (model *Model) applyReconciliationOutcome(outcome PublicationOutcome) {
	attempt, hasAttempt := model.review.UnknownPublicationOperation()
	next, err := model.review.ApplyReconciliation(outcome.Reconciliation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	reconciliationStatus := review.OperationSucceeded
	if outcome.Reconciliation.Disposition == review.ReconciliationStillUnknown {
		reconciliationStatus = review.OperationFailed
	}
	next = resolvePublicationOperation(
		next,
		outcome.ID,
		reconciliationStatus,
		outcome.Reconciliation.Detail,
	)
	if hasAttempt &&
		outcome.Reconciliation.Disposition != review.ReconciliationStillUnknown {
		settledStatus := review.OperationSucceeded
		if outcome.Reconciliation.Disposition == review.ReconciliationNotCommitted {
			settledStatus = review.OperationFailed
		}
		if settled, settleErr := next.SettleUnknownOperation(
			attempt,
			settledStatus,
			outcome.Reconciliation.Detail,
		); settleErr == nil {
			next = settled
		}
	}
	model.review = next
	switch outcome.Reconciliation.Disposition {
	case review.ReconciliationCommitted:
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: "GitHub review commit confirmed by publication marker",
		}
	case review.ReconciliationNotCommitted:
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: "publication marker confirmed absent; retry is safe after a new preflight",
		}
	case review.ReconciliationStillUnknown:
		model.notice = &Notice{
			Kind: NoticeError,
			Text: "publication remains unknown; retry is fenced",
		}
	}
}

func resolvePublicationOperation(
	current review.Review,
	id review.OperationID,
	status review.OperationStatus,
	detail review.OperationDetail,
) review.Review {
	next, err := current.ResolveOperation(id, status, detail)
	if err != nil {
		return current
	}
	return next
}

func (model *Model) applyPublicationAction(action KeyAction) tea.Cmd {
	summary := model.review.PublicationSummary()
	switch action {
	case KeyActionCancel, KeyActionInterrupt, KeyActionQuit:
		switch summary.Status {
		case review.PublicationChecking, review.PublicationPublishing:
			model.publicationCancel = true
			model.coordinator.Cancel(model.publicationOperation)
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "cancelling publication; a claimed remote commit will remain fenced",
			}
		case review.PublicationChecked:
			model.review, _ = model.review.CancelPublicationCheck()
			model.overlay = OverlayNone
		default:
			model.overlay = OverlayNone
		}
	case KeyActionSubmitReview:
		if summary.Status == review.PublicationChecked {
			model.startPublication()
		}
	case KeyActionSubmitRecheck:
		if summary.Status == review.PublicationUnknown {
			model.startReconciliation()
		} else if model.publicationOperation == 0 {
			model.restartPublicationCheck()
		}
	case KeyActionToggleStaleOverride:
		if model.publicationOperation == 0 {
			model.publicationAllowStale = !model.publicationAllowStale
			model.restartPublicationCheck()
		}
	case KeyActionToggleSubmitSummary:
		if model.publicationOperation == 0 {
			model.publicationMoveSummary = !model.publicationMoveSummary
			model.restartPublicationCheck()
		}
	case KeyActionToggleSubmitExclude:
		if model.publicationOperation == 0 {
			model.togglePublicationExclusion()
			model.restartPublicationCheck()
		}
	case KeyActionMoveUp:
		model.publicationCursor = max(model.publicationCursor-1, 0)
		model.publicationDetailTop = 0
	case KeyActionMoveDown:
		model.publicationCursor = min(
			model.publicationCursor+1,
			model.review.CommentCount(),
		)
		model.publicationDetailTop = 0
	case KeyActionPageUp:
		model.publicationDetailTop = max(
			model.publicationDetailTop-model.publicationDetailStep(),
			0,
		)
	case KeyActionPageDown:
		model.publicationDetailTop = min(
			model.publicationDetailTop+model.publicationDetailStep(),
			review.MaxPublicationBytes,
		)
	}
	return nil
}

func (model *Model) restartPublicationCheck() {
	if model.review.PublicationSummary().Status == review.PublicationChecked {
		model.review, _ = model.review.CancelPublicationCheck()
	}
	model.startPublicationCheck()
}

func (model *Model) togglePublicationExclusion() {
	if model.publicationCursor == 0 {
		return
	}
	comment, ok := model.review.CommentAt(model.publicationCursor - 1)
	if !ok {
		return
	}
	if _, excluded := model.publicationExcluded[comment.ID()]; excluded {
		delete(model.publicationExcluded, comment.ID())
	} else {
		model.publicationExcluded[comment.ID()] = struct{}{}
	}
}

func (model *Model) acknowledgePublication() {
	next, err := model.review.AcknowledgePublication()
	if err != nil {
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: "an unknown publication cannot be acknowledged away; reconciliation is required",
		}
		return
	}
	model.review = next
	model.notice = &Notice{Kind: NoticeInformation, Text: "publication fact acknowledged"}
}

func (model *Model) publicationView(width, height int) string {
	summary := model.review.PublicationSummary()
	lines := []string{"GitHub submission preflight"}
	switch summary.Status {
	case review.PublicationChecking:
		lines = append(lines, "Checking immutable repository, pull request, and head…")
	case review.PublicationChecked:
		unmappablePolicy := "exclude"
		if model.publicationMoveSummary {
			unmappablePolicy = "move to summary"
		}
		headStatus := "checked at current head"
		if summary.StaleHead {
			headStatus = "stale head explicitly overridden"
		}
		lines = append(lines,
			"Target: "+summary.Target.Repository()+"#"+
				fmt.Sprintf("%d", summary.Target.PullRequest()),
			"Event: "+summary.Event.String(),
			fmt.Sprintf("Comments: %d included, %d excluded", summary.Included, summary.Excluded),
			"Unmappable policy: "+unmappablePolicy,
			"Source/head: "+headStatus,
		)
		chooserHeight := min(max(height-len(lines)-5, 1), 6)
		lines = append(lines, model.publicationCommentLines(chooserHeight)...)
		detailHeight := max(height-len(lines)-2, 0)
		lines = append(
			lines,
			model.publicationDetailLines(width, detailHeight)...,
		)
	case review.PublicationPublishing:
		lines = append(lines, "Publishing; cancellation after the commit claim produces an unknown result.")
	case review.PublicationUnknown:
		lines = append(lines,
			"Outcome unknown; retry is fenced.",
			"Target: "+summary.Target.Repository()+"#"+
				fmt.Sprintf("%d", summary.Target.PullRequest()),
			"Marker: "+summary.Marker.String(),
			"Use submit-recheck to enumerate every remote review.",
		)
	case review.PublicationReconciling:
		lines = append(lines, "Enumerating every remote review for the exact publication marker…")
	case review.PublicationCommitted:
		lines = append(lines, "Publication committed.")
	case review.PublicationConfirmedAbsent:
		lines = append(lines, "Marker confirmed absent; a new checked attempt may be submitted safely.")
	case review.PublicationFailedBeforeCommit:
		lines = append(lines, "Publication failed before commit: "+summary.Detail.String())
	default:
		lines = append(lines, "No publication preflight is active.")
	}
	lines = append(lines, model.bindingSummary(KeyContextSubmit))
	return constrainedLines(lines, width, height)
}

func (model *Model) publicationCommentLines(limit int) []string {
	if limit <= 0 {
		return nil
	}
	start := max(model.publicationCursor-limit/2, 0)
	count := model.review.CommentCount() + 1
	start = min(start, max(count-limit, 0))
	lines := make([]string, 0, min(limit, count-start))
	for index := start; index < min(start+limit, count); index++ {
		cursor := "  "
		if index == model.publicationCursor {
			cursor = "▸ "
		}
		if index == 0 {
			lines = append(lines, cursor+"[payload] canonical GitHub request")
			continue
		}
		comment, ok := model.review.CommentAt(index - 1)
		if !ok {
			continue
		}
		state := "unchecked"
		if decision, ok := model.review.PublicationDecisionAt(index - 1); ok &&
			decision.CommentID() == comment.ID() {
			state = decision.Decision().String()
		}
		lines = append(lines, cursor+"["+state+"] "+comment.Kind().String()+" "+
			safeAndClip(comment.Body().String(), 80))
	}
	return lines
}

func (model *Model) publicationDetailLines(width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	label := "Canonical payload"
	value := model.review.PublicationPreflightPayload()
	if model.publicationCursor != 0 {
		if comment, ok := model.review.CommentAt(model.publicationCursor - 1); ok {
			label = "Full comment #" + fmt.Sprint(comment.ID().Uint32())
			value = comment.Body().String()
		}
	}
	top := min(max(model.publicationDetailTop, 0), len(value))
	for top < len(value) && !utf8.RuneStart(value[top]) {
		top++
	}
	maxBytes := max(width*max(height-1, 1)*4, 256)
	end := min(top+maxBytes, len(value))
	for end > top && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	header := fmt.Sprintf("%s · bytes %d–%d/%d", label, top, end, len(value))
	if value == "" {
		return []string{header, "(empty)"}
	}
	wrapped := lipgloss.Wrap(safeText(value[top:end]), max(width, 1), "")
	rows := strings.Split(wrapped, "\n")
	return append([]string{header}, rows[:min(len(rows), max(height-1, 0))]...)
}

func (model *Model) publicationDetailStep() int {
	width, height := model.renderSize()
	return max(width*max(height/2, 1)*2, 256)
}

func boundedOperationDetail(value string) review.OperationDetail {
	if len(value) > int(review.MaxOperationDetail) {
		value = value[:review.MaxOperationDetail]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return review.OperationDetail(strings.TrimSpace(value))
}
