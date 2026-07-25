package app

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/review"
)

func (model *Model) openPublication() {
	pending := model.review.PendingReviewSummary()
	model.publicationDraftReady = pending.Status == review.PendingReviewReady &&
		pending.Dirty == 0 && pending.Conflicts == 0
	model.startPublicationCheck()
}

func (model *Model) startPublicationCheck() {
	model.publicationIntent = publicationOperationIntentNone
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
	target, ok := model.document.PublicationTarget()
	if !ok {
		model.notice = &Notice{Kind: NoticeWarning, Text: "GitHub publication target is unavailable"}
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
		ID:      operationID,
		Target:  target,
		Source:  current.PublicationSource(),
		Request: request,
	}); err != nil {
		model.review = failPublicationCheck(next, request, err)
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.publicationOperation = operationID
	model.overlay = OverlaySubmit
	model.notice = &Notice{Kind: NoticeInformation, Text: "checking publication preflight"}
}

func (model *Model) publicationOptions() (review.PublicationOptions, error) {
	var policies []review.PublicationPolicy
	configured := model.settings.publication.Policies()
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
	event := review.PublicationEventDraft
	if model.publicationDraftReady {
		event = model.publicationEvent
	}
	options, err := review.NewPublicationOptions(event, typedPolicies)
	if err != nil {
		return review.PublicationOptions{}, err
	}
	return options.WithExclusions(model.publicationExcluded)
}

func (model *Model) nextOperationID() (review.OperationID, error) {
	operationID, err := review.NewOperationID(model.nextOperation)
	if err != nil {
		return 0, errors.New(operationIdentityExhausted)
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

func (model *Model) claimPublicationOutcome(id review.OperationID) bool {
	if id == 0 || id != model.publicationOperation {
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale publication outcome"}
		return false
	}
	model.publicationOperation = 0
	return true
}

func (model *Model) applyPublicationCheckOutcome(outcome PublicationCheckOutcome) {
	if !model.claimPublicationOutcome(outcome.ID) {
		return
	}
	intent := model.publicationIntent
	model.publicationIntent = publicationOperationIntentNone
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
		result = review.PublicationCheckResult{
			Operation: outcome.ID,
			Workspace: model.review.WorkspaceRevision(),
			Source:    model.review.Snapshot().SourceID(),
			Detail:    boundedOperationDetail(err.Error()),
		}
		next, err = model.review.ApplyPublicationCheck(result)
		if err != nil {
			model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
			return
		}
	}
	status := review.OperationSucceeded
	if result.Checked == nil {
		status = operationStatusForWorkError(outcome.Err)
	}
	next, err = next.ResolveOperation(outcome.ID, status, result.Detail)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	if intent == publicationOperationIntentCancel {
		if result.Checked != nil {
			if !model.cancelPublicationCheck() {
				return
			}
		}
		model.overlay = OverlayNone
		model.notice = &Notice{Kind: NoticeInformation, Text: "publication preflight cancelled"}
		return
	}
	if result.Checked == nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(result.Detail.String())}
		return
	}
	if intent == publicationOperationIntentSubmit {
		model.startPublication()
		return
	}
	model.notice = &Notice{Kind: NoticeInformation, Text: "publication preflight is ready"}
}

func (model *Model) startPublication() {
	model.publicationIntent = publicationOperationIntentNone
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
		Rebase:  model.review.PublicationRebaseSource(),
	}); err != nil {
		result := review.PublicationResult{
			Operation:   operationID,
			Workspace:   request.Workspace,
			Source:      request.Source,
			Disposition: review.PublicationDispositionFailedBeforeCommit,
			Detail:      boundedOperationDetail(err.Error()),
		}
		if failedReview, applyErr := next.ApplyPublication(result); applyErr == nil {
			model.review = resolvePublicationOperation(
				failedReview,
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
	model.notice = &Notice{Kind: NoticeInformation, Text: "publishing GitHub review"}
}

func (model *Model) applyPublicationCommitOutcome(outcome PublicationCommitOutcome) {
	if !model.claimPublicationOutcome(outcome.ID) {
		return
	}
	result := outcome.Publication
	next, err := model.review.ApplyPreparedPublication(result)
	if err != nil {
		if outcome.Err != nil {
			err = errors.Join(outcome.Err, err)
		}
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	status := review.OperationSucceeded
	switch result.Disposition() {
	case review.PublicationDispositionCommitted:
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
		result.Detail(),
	)
	model.review = next
	model.publicationIntent = publicationOperationIntentNone
	if result.Disposition() == review.PublicationDispositionUnknown {
		model.notice = &Notice{
			Kind: NoticeError,
			Text: "publication outcome is unknown; reconciling exact remote state",
		}
		model.startReconciliation()
		return
	}
	if result.Disposition() == review.PublicationDispositionCommitted {
		message := "GitHub review published"
		kind := NoticeInformation
		if model.review.PublicationSummary().Event == review.PublicationEventDraft {
			message = "GitHub draft review synchronized"
			if !result.Detail().IsEmpty() {
				message = result.Detail().String()
				kind = NoticeWarning
			}
		}
		if refreshErr := result.LocalRefreshError(); refreshErr != nil {
			message = refreshErr.Error()
			kind = NoticeWarning
		}
		model.notice = &Notice{Kind: kind, Text: NoticeText(message)}
		if model.review.PublicationSummary().Event == review.PublicationEventDraft &&
			result.LocalRefreshError() == nil {
			model.publicationDraftReady = true
			model.startPublicationCheck()
		}
	} else {
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: NoticeText(result.Detail().String()),
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
		Rebase:  model.review.PendingRebaseSource(),
	}); err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.publicationOperation = operationID
	model.overlay = OverlaySubmit
	model.notice = &Notice{Kind: NoticeInformation, Text: "reconciling GitHub publication"}
}

func (model *Model) applyReconciliationOutcome(outcome ReconciliationOutcome) {
	if !model.claimPublicationOutcome(outcome.ID) {
		return
	}
	attempt, hasAttempt := model.review.UnknownPublicationOperation()
	result := outcome.Reconciliation
	next, err := model.review.ApplyPreparedReconciliation(result)
	if err != nil {
		if outcome.Err != nil {
			err = errors.Join(outcome.Err, err)
		}
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	reconciliationStatus := review.OperationSucceeded
	if result.Disposition() == review.ReconciliationStillUnknown {
		reconciliationStatus = review.OperationFailed
	}
	next = resolvePublicationOperation(
		next,
		outcome.ID,
		reconciliationStatus,
		result.Detail(),
	)
	if hasAttempt &&
		result.Disposition() != review.ReconciliationStillUnknown {
		settledStatus := review.OperationSucceeded
		if result.Disposition() == review.ReconciliationNotCommitted {
			settledStatus = review.OperationFailed
		}
		if settled, settleErr := next.SettleUnknownOperation(
			attempt,
			settledStatus,
			result.Detail(),
		); settleErr == nil {
			next = settled
		}
	}
	model.review = next
	if refreshErr := result.LocalRefreshError(); refreshErr != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(refreshErr.Error())}
		return
	}
	switch result.Disposition() {
	case review.ReconciliationCommitted:
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: "GitHub publication confirmed from exact remote state",
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

func (model *Model) applyPublicationAction(action KeyAction) {
	summary := model.review.PublicationSummary()
	switch action {
	case KeyActionCancel, KeyActionInterrupt, KeyActionQuit:
		switch summary.Status {
		case review.PublicationChecking, review.PublicationPublishing:
			model.publicationIntent = publicationOperationIntentCancel
			model.coordinator.Cancel(model.publicationOperation)
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "cancelling publication; a claimed remote commit will remain fenced",
			}
		case review.PublicationChecked:
			if !model.cancelPublicationCheck() {
				return
			}
			model.overlay = OverlayNone
		default:
			model.overlay = OverlayNone
		}
	case KeyActionStatusDetails:
		model.overlay = OverlayStatus
	case KeyActionSubmitReview:
		switch summary.Status {
		case review.PublicationChecking:
			model.publicationIntent = publicationOperationIntentSubmit
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "submission requested; waiting for publication preflight",
			}
		case review.PublicationChecked:
			model.startPublication()
		default:
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
	case KeyActionPreviousSubmitEvent:
		model.movePublicationEvent(-1)
	case KeyActionNextSubmitEvent:
		model.movePublicationEvent(1)
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
	default:
	}
}

var publicationEvents = [...]review.PublicationEvent{
	review.PublicationEventComment,
	review.PublicationEventApprove,
	review.PublicationEventRequestChanges,
}

func (model *Model) movePublicationEvent(delta int) {
	if delta == 0 || model.publicationOperation != 0 {
		return
	}
	index := 0
	for candidate, event := range publicationEvents {
		if event == model.publicationEvent {
			index = candidate
			break
		}
	}
	index = (index + delta) % len(publicationEvents)
	if index < 0 {
		index += len(publicationEvents)
	}
	model.publicationEvent = publicationEvents[index]
	if summary := model.review.PublicationSummary(); summary.Status == review.PublicationChecked &&
		summary.Event != review.PublicationEventDraft {
		model.restartPublicationCheck()
	}
}

func (model *Model) restartPublicationCheck() {
	if model.review.PublicationSummary().Status == review.PublicationChecked {
		if !model.cancelPublicationCheck() {
			return
		}
	}
	model.startPublicationCheck()
}

func (model *Model) cancelPublicationCheck() bool {
	next, err := model.review.CancelPublicationCheck()
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return false
	}
	model.review = next
	return true
}

func (model *Model) togglePublicationExclusion() {
	if model.publicationCursor == 0 {
		return
	}
	comment, ok := model.review.CommentAt(model.publicationCursor - 1)
	if !ok {
		return
	}
	if model.review.PendingReviewSummary().Status == review.PendingReviewReady &&
		model.review.PendingBindingState(comment.ID()) != review.PendingBindingNew {
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: "existing GitHub drafts can be removed with delete, not excluded from their review",
		}
		return
	}
	exclusions, err := model.publicationExcluded.Toggle(comment.ID())
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.publicationExcluded = exclusions
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

func (model *Model) publicationModalView(
	width, height int,
	styles semanticStyles,
) (string, boundedPanelLayout) {
	layout := newBoundedPanelLayout(width, height, boundedPanelRequest{
		minimumWidth:  60,
		minimumHeight: 16,
		maximumWidth:  90,
		maximumHeight: 32,
		inset:         2,
	})
	summary := model.review.PublicationSummary()
	pending := model.review.PendingReviewSummary()
	pendingProblem := pendingReviewProblemLine(pending)
	lines := []string{styles.heading.Render("GitHub Review"), "", model.publicationEventSelector()}
	if pendingProblem != "" {
		lines = append(lines, styles.warning.Render(pendingProblem))
	}
	switch summary.Status {
	case review.PublicationChecking:
		status := "◐ Checking immutable repository, pull request, and head…"
		if model.publicationIntent == publicationOperationIntentSubmit {
			status = "◐ Checking; submission is queued…"
		}
		lines = append(lines, "", status)
	case review.PublicationChecked:
		unmappablePolicy := "exclude"
		if model.publicationMoveSummary {
			unmappablePolicy = "move to summary"
		}
		headStatus := "current head"
		if summary.StaleHead {
			headStatus = styles.warning.Render("stale (override active)")
		}
		stage := "Submit as " + model.publicationEvent.String()
		if summary.Event == review.PublicationEventDraft {
			stage = "Synchronize to pending draft"
		}
		lines = append(lines, "",
			fmt.Sprintf("  Target       %s#%d", summary.Target.Repository(), summary.Target.PullRequest()),
			fmt.Sprintf("  Comments     %d included, %d excluded", summary.Included, summary.Excluded),
			fmt.Sprintf("  Unmappable   %s", unmappablePolicy),
			"  Head         "+headStatus,
		)
		if pending.Status == review.PendingReviewReady {
			lines = append(lines, fmt.Sprintf(
				"  Pending      continue #%d (%d changed, %d conflicts)",
				pending.ReviewID.Uint64(), pending.Dirty, pending.Conflicts,
			))
		} else if pendingProblem == "" {
			lines = append(lines, "  Pending      "+pending.Status.String())
		}
		lines = append(lines, "", "  "+stage)
		chooserHeight := min(max(layout.innerHeight-len(lines)-5, 1), 6)
		lines = append(lines, "", styles.muted.Render("── Comments"))
		lines = append(lines, model.publicationCommentLines(chooserHeight)...)
		detailHeight := max(layout.innerHeight-len(lines)-2, 0)
		lines = append(
			lines,
			model.publicationDetailLines(layout.innerWidth, detailHeight)...,
		)
	case review.PublicationPublishing:
		lines = append(lines, "", "◐ Publishing — cancellation may leave unknown state.")
	case review.PublicationUnknown:
		lines = append(lines, "",
			styles.warning.Render("⚠ Outcome unknown; retry is fenced."),
			"",
			fmt.Sprintf("  Target   %s#%d", summary.Target.Repository(), summary.Target.PullRequest()),
			"  Marker   "+summary.Marker.String(),
			"",
			"Use submit-recheck to verify exact remote state.",
		)
	case review.PublicationReconciling:
		lines = append(lines, "", "◐ Checking exact GitHub publication state…")
	case review.PublicationCommitted:
		lines = append(lines, "", styles.reviewed.Render("✓ Publication committed."))
	case review.PublicationConfirmedAbsent:
		lines = append(lines, "", "Marker confirmed absent; safe to submit a new attempt.")
	case review.PublicationFailedBeforeCommit:
		lines = append(lines, "", styles.failure.Render("✗ Publication failed: "+summary.Detail.String()))
	default:
		lines = append(lines, "", "No publication preflight is active.")
	}
	lines = append(lines, "", styles.muted.Render(model.bindingSummary(KeyContextSubmit)))
	content := constrainedStyledLines(lines, layout.innerWidth, layout.innerHeight)
	panelStyle := styles.panel.Width(layout.width).Height(layout.height)
	if layout.bordered {
		panelStyle = panelStyle.Border(lipgloss.RoundedBorder())
		if foreground := styles.borderFocused.GetForeground(); foreground != nil {
			panelStyle = panelStyle.BorderForeground(foreground)
		}
	}
	return panelStyle.Render(content), layout
}

func pendingReviewProblemLine(pending review.PendingReviewSummary) string {
	switch pending.Status {
	case review.PendingReviewUnavailable,
		review.PendingReviewLimited,
		review.PendingReviewAmbiguous:
	default:
		return ""
	}
	line := "Pending review: " + pending.Status.String()
	if !pending.Detail.IsEmpty() {
		line += " — " + pending.Detail.String()
	}
	return line
}

func (model *Model) publicationEventSelector() string {
	var selector strings.Builder
	selector.WriteString("Submit as (h/l): ")
	for index, event := range publicationEvents {
		if index != 0 {
			selector.WriteString("  ")
		}
		label := publicationEventLabel(event)
		if event == model.publicationEvent {
			selector.WriteByte('[')
			selector.WriteString(label)
			selector.WriteByte(']')
		} else {
			selector.WriteString(label)
		}
	}
	return selector.String()
}

func publicationEventLabel(event review.PublicationEvent) string {
	switch event {
	case review.PublicationEventComment:
		return "Comment"
	case review.PublicationEventApprove:
		return "Approve"
	case review.PublicationEventRequestChanges:
		return "Request changes"
	default:
		return event.String()
	}
}

func (model *Model) publicationCommentLines(limit int) []string {
	if limit <= 0 {
		return nil
	}
	start := max(model.publicationCursor-limit/2, 0)
	count := model.review.CommentCount() + 1
	start = min(start, max(count-limit, 0))
	lines := make([]string, 0, min(limit, count-start))
	pendingReady := model.review.Snapshot().PendingReviewSummary().Status ==
		review.PendingReviewReady
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
		label := state
		if pendingReady {
			binding := model.review.PendingBindingState(comment.ID()).String()
			label = binding + " · " + state
		}
		lines = append(lines, cursor+"["+label+"] "+safeAndClip(comment.Body().String(), 80))
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
	return review.NewBoundedTrimmedOperationDetail(value)
}
