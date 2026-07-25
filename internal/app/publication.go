package app

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/keymap"
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
	model.notice = &Notice{Kind: NoticeInformation, Text: "checking GitHub review"}
}

func (model *Model) publicationOptions() (review.PublicationOptions, error) {
	policies, err := review.NewPublicationPolicies()
	if err != nil {
		return review.PublicationOptions{}, err
	}
	event := review.PublicationEventDraft
	if model.publicationDraftReady {
		event = model.publicationEvent
	}
	return review.NewPublicationOptions(event, policies)
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
		model.notice = &Notice{Kind: NoticeInformation, Text: "GitHub review check cancelled"}
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
	model.notice = &Notice{Kind: NoticeInformation, Text: "GitHub review is ready"}
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
	selections := model.captureReviewSelections()
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
	model.reconcileReviewSelections(selections)
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
		kind := NoticeSuccess
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
	selections := model.captureReviewSelections()
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
	model.reconcileReviewSelections(selections)
	if refreshErr := result.LocalRefreshError(); refreshErr != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(refreshErr.Error())}
		return
	}
	switch result.Disposition() {
	case review.ReconciliationCommitted:
		model.notice = &Notice{
			Kind: NoticeSuccess,
			Text: "GitHub publication confirmed from exact remote state",
		}
	case review.ReconciliationNotCommitted:
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: "GitHub review was not published; retry is safe after a new check",
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

func (model *Model) applyPublicationAction(action keymap.Action) {
	summary := model.review.PublicationSummary()
	switch action {
	case keymap.ActionCancel, keymap.ActionInterrupt, keymap.ActionQuit:
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
	case keymap.ActionStatusDetails:
		model.statusReturn = OverlaySubmit
		model.overlay = OverlayStatus
	case keymap.ActionSubmitReview:
		if model.publicationIntent == publicationOperationIntentCancel {
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "publication cancellation is settling; submission remains disabled",
			}
			return
		}
		if model.publicationIntent == publicationOperationIntentSubmit {
			return
		}
		if publicationPrimaryAction(summary.Status) != keymap.ActionSubmitReview {
			return
		}
		switch summary.Status {
		case review.PublicationChecking:
			model.publicationIntent = publicationOperationIntentSubmit
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "submission requested; waiting for the GitHub review check",
			}
		case review.PublicationChecked:
			model.startPublication()
		default:
			model.recheckPublication(summary.Status)
		}
	case keymap.ActionSubmitRecheck:
		model.recheckPublication(summary.Status)
	case keymap.ActionPreviousSubmitEvent:
		model.movePublicationEvent(-1)
	case keymap.ActionNextSubmitEvent:
		model.movePublicationEvent(1)
	default:
	}
}

var publicationEvents = [...]review.PublicationEvent{
	review.PublicationEventComment,
	review.PublicationEventApprove,
	review.PublicationEventRequestChanges,
}

const (
	publicationEventCommentLabel        = "Comment"
	publicationEventApproveLabel        = "Approve"
	publicationEventRequestChangesLabel = "Request changes"
)

func (model *Model) movePublicationEvent(delta int) {
	if delta == 0 || !model.publicationEventEditable() {
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

func publicationPrimaryAction(status review.PublicationStatus) keymap.Action {
	switch status {
	case review.PublicationChecking, review.PublicationChecked,
		review.PublicationIdle, review.PublicationFailedBeforeCommit,
		review.PublicationUnknown, review.PublicationConfirmedAbsent:
		return keymap.ActionSubmitReview
	default:
		return keymap.ActionNone
	}
}

func publicationStatusRequiresRecheck(status review.PublicationStatus) bool {
	switch status {
	case review.PublicationIdle, review.PublicationFailedBeforeCommit,
		review.PublicationUnknown, review.PublicationConfirmedAbsent:
		return true
	default:
		return false
	}
}

func (model *Model) recheckPublication(status review.PublicationStatus) {
	if model.publicationOperation != 0 || !publicationStatusRequiresRecheck(status) {
		return
	}
	if status == review.PublicationUnknown {
		model.startReconciliation()
		return
	}
	model.restartPublicationCheck()
}

func publicationStatusAllowsEventSelection(status review.PublicationStatus) bool {
	return status == review.PublicationIdle || status == review.PublicationChecked
}

func (model *Model) publicationEventEditable() bool {
	return model.publicationDraftReady && model.publicationOptionsEditable()
}

func (model *Model) publicationOptionsEditable() bool {
	return model.publicationOperation == 0 &&
		model.publicationIntent != publicationOperationIntentCancel &&
		publicationStatusAllowsEventSelection(model.review.PublicationSummary().Status)
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
	pending := model.review.PendingReviewSummary()
	pendingProblem := pendingReviewProblemLine(pending)
	styles := newSemanticStyles(model.settings)
	controls := model.publicationBindingRows(width, styles)
	if model.publicationIntent == publicationOperationIntentCancel {
		status := "Cancelling publication work…"
		if summary.Status == review.PublicationPublishing {
			status = "Cancelling publication; any claimed remote commit remains fenced."
		}
		return blockingSurface([]string{
			modalTitle(styles, "Publish GitHub review"),
			"",
			styles.heading.Render("Cancelling"),
			status,
		}, controls, width, height, styles)
	}
	if height <= minimumHeight {
		body := publicationCompactStateLines(summary, model.publicationIntent, styles)
		if model.publicationDraftReady {
			state := body[len(body)-1]
			choices := model.publicationEventLines(width, styles)
			body = append([]string{state}, choices[1:]...)
			body[0] = styles.focused.Render("▌ ") + body[0]
		}
		return blockingSurface(body, controls, width, height, styles)
	}
	lines := []string{modalTitle(styles, "Publish GitHub review")}
	lockedChoices := model.publicationDraftReady && !model.publicationEventEditable()
	if model.publicationDraftReady {
		choices := model.publicationEventLines(width, styles)
		if lockedChoices {
			state := publicationCompactStateLines(summary, model.publicationIntent, styles)
			lines = append(lines, state[len(state)-1])
			lines = append(lines, choices[1:]...)
		} else {
			lines = append(lines, choices...)
		}
	}
	if pendingProblem != "" {
		lines = append(lines, safeText(pendingProblem))
	}
	lines = append(lines, model.publicationStateLines(
		summary,
		pending,
		pendingProblem,
		lockedChoices,
		styles,
	)...)
	return blockingSurface(lines, controls, width, height, styles)
}

func (model *Model) publicationStateLines(
	summary review.PublicationSummary,
	pending review.PendingReviewSummary,
	pendingProblem string,
	lockedChoices bool,
	styles semanticStyles,
) []string {
	var lines []string
	switch summary.Status {
	case review.PublicationChecking:
		if lockedChoices {
			break
		}
		status := "Checking immutable repository, pull request, and head…"
		switch model.publicationIntent {
		case publicationOperationIntentSubmit:
			status = "Submission queued; checking immutable repository, pull request, and head…"
		case publicationOperationIntentCancel:
			status = "Cancelling GitHub review check…"
		case publicationOperationIntentNone:
		}
		lines = append(lines, "", styles.heading.Render("Submission"), status)
	case review.PublicationChecked:
		lines = append(
			lines,
			model.publicationCheckedLines(summary, pending, pendingProblem, styles)...,
		)
	case review.PublicationPublishing:
		if lockedChoices {
			break
		}
		status := "Publishing; cancellation after the commit claim produces an unknown result."
		if model.publicationIntent == publicationOperationIntentCancel {
			status = "Cancelling publication; a claimed remote commit remains fenced until settlement."
		}
		lines = append(lines,
			"",
			styles.heading.Render("Submission"),
			status,
		)
	case review.PublicationUnknown:
		if lockedChoices {
			lines = append(lines,
				safeText("Target: "+summary.Target.Repository()+"#"+
					fmt.Sprintf("%d", summary.Target.PullRequest())),
				"Reconciliation is required before retrying.",
			)
			break
		}
		lines = append(lines,
			"",
			styles.heading.Render("Submission"),
			"Outcome unknown; retry is fenced.",
			safeText("Target: "+summary.Target.Repository()+"#"+
				fmt.Sprintf("%d", summary.Target.PullRequest()),
			),
			"Recheck to verify exact remote state before retrying.",
		)
	case review.PublicationReconciling:
		if lockedChoices {
			break
		}
		lines = append(lines, "", styles.heading.Render("Submission"),
			"Checking exact GitHub publication state…")
	case review.PublicationCommitted:
		if lockedChoices {
			break
		}
		lines = append(lines, "", styles.heading.Render("Submission"),
			"Review published.")
	case review.PublicationConfirmedAbsent:
		if lockedChoices {
			break
		}
		lines = append(lines, "", styles.heading.Render("Submission"),
			"No matching GitHub review was found; a checked retry is safe.")
	case review.PublicationFailedBeforeCommit:
		if lockedChoices {
			if !summary.Detail.IsEmpty() {
				lines = append(lines, safeText(summary.Detail.String()))
			}
			break
		}
		lines = append(lines, "", styles.heading.Render("Submission"),
			safeText("Publication failed before commit: "+summary.Detail.String()))
	default:
		lines = append(lines, "", styles.heading.Render("Submission"),
			"No GitHub review check is active.")
	}
	return lines
}

func (model *Model) publicationCheckedLines(
	summary review.PublicationSummary,
	pending review.PendingReviewSummary,
	pendingProblem string,
	styles semanticStyles,
) []string {
	stage := "Submit pending draft as " + model.publicationEvent.String()
	if summary.Event == review.PublicationEventDraft {
		stage = "Synchronize comments to a pending GitHub draft"
	}
	lines := []string{
		"",
		styles.heading.Render("Submission"),
		safeText("Target: " + summary.Target.Repository() + "#" +
			fmt.Sprintf("%d", summary.Target.PullRequest()),
		),
		"Next step: " + stage,
		fmt.Sprintf("Comments: %d included, %d excluded", summary.Included, summary.Excluded),
	}
	if pending.Status == review.PendingReviewReady {
		lines = append(lines, fmt.Sprintf(
			"Pending draft: %d changed · %d conflicts",
			pending.Dirty, pending.Conflicts,
		))
	} else if pendingProblem == "" {
		lines = append(lines, safeText("Pending draft: "+pending.Status.String()))
	}
	if summary.Excluded != 0 {
		lines = append(lines, styles.warning.Render(
			fmt.Sprintf("Warning: %d comments cannot be safely mapped and will be excluded", summary.Excluded),
		))
	}
	if pending.Conflicts != 0 {
		lines = append(lines, styles.warning.Render(
			fmt.Sprintf("Warning: %d pending draft conflicts must be resolved", pending.Conflicts),
		))
	}
	if summary.StaleHead {
		lines = append(lines, styles.warning.Render(
			"Warning: the pull request head changed; submission is blocked",
		))
	}
	return lines
}

func publicationCompactStateLines(
	summary review.PublicationSummary,
	intent publicationOperationIntent,
	styles semanticStyles,
) []string {
	lines := []string{modalTitle(styles, "Publish GitHub review"), ""}
	var status string
	switch summary.Status {
	case review.PublicationChecking:
		status = "Checking GitHub…"
		if intent == publicationOperationIntentSubmit {
			status = "Submission queued"
		}
	case review.PublicationChecked:
		status = "Ready to publish"
		if summary.Event == review.PublicationEventDraft {
			status = "Ready to sync draft"
		}
	case review.PublicationPublishing:
		status = "Publishing review…"
		if summary.Event == review.PublicationEventDraft {
			status = "Syncing draft…"
		}
	case review.PublicationCommitted:
		status = "Review published"
		if summary.Event == review.PublicationEventDraft {
			status = "Draft synchronized"
		}
	case review.PublicationFailedBeforeCommit:
		status = "Failed before commit"
	case review.PublicationUnknown:
		status = "Outcome unknown"
	case review.PublicationReconciling:
		status = "Reconciling review…"
	case review.PublicationConfirmedAbsent:
		status = "Remote review absent"
	default:
		status = "No review check active"
	}
	return append(lines, styles.heading.Render(status))
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

func (model *Model) publicationEventLines(width int, styles semanticStyles) []string {
	headingWidth := max(width-lipgloss.Width("▌ "), 1)
	heading := "Review action · " + model.publicationEventNavigationHint(width, false)
	if lipgloss.Width(heading) > headingWidth {
		heading = "Action"
		available := max(headingWidth-lipgloss.Width("Action · "), 0)
		if hint := model.publicationEventNavigationHint(available, true); hint != "" {
			heading += " · " + hint
		}
	}
	lines := []string{styles.heading.Render(safeText(heading))}
	for _, event := range publicationEvents {
		if event == model.publicationEvent {
			lines = append(lines, styles.selected.Render("(●) "+publicationEventLabel(event)))
			continue
		}
		lines = append(lines, styles.muted.Render("( ) "+publicationEventLabel(event)))
	}
	return lines
}

func (model *Model) publicationEventNavigationHint(width int, compact bool) string {
	if !model.publicationEventEditable() {
		if model.publicationOperation != 0 {
			return "selection locked while publication work is active"
		}
		return "selection unavailable in this state"
	}
	bindings := bindingsForActions(
		model.effectiveBindings(keymap.ContextSubmit),
		[]keymap.Action{
			keymap.ActionPreviousSubmitEvent,
			keymap.ActionNextSubmitEvent,
		},
	)
	if len(bindings) == 2 {
		if compact {
			previous := bindings[0].Chord.Display()
			next := bindings[1].Chord.Display()
			pair := previous + "/" + next
			switch {
			case lipgloss.Width(pair) <= width:
				return pair
			case lipgloss.Width(previous) <= width:
				return previous
			case lipgloss.Width(next) <= width:
				return next
			default:
				return ""
			}
		}
		return bindings[0].Chord.Display() + " or " + bindings[1].Chord.Display() + " to change"
	}
	if len(bindings) == 1 {
		if compact {
			chord := bindings[0].Chord.Display()
			if lipgloss.Width(chord) <= width {
				return chord
			}
			return ""
		}
		return bindings[0].Chord.Display() + " to change"
	}
	return "selection unavailable"
}

func (model *Model) publicationBindingSummary() string {
	actions := model.publicationBindingActions()
	bindings := bindingsForActions(model.effectiveBindings(keymap.ContextSubmit), actions)
	var summary strings.Builder
	for index, binding := range bindings {
		writeBindingSummary(&summary, index, binding.Chord, binding.Action)
	}
	return summary.String()
}

func (model *Model) publicationBindingRows(width int, styles semanticStyles) []string {
	summary := model.review.PublicationSummary()
	bar := bindingBar{bindings: bindingsForActions(
		model.effectiveBindings(keymap.ContextSubmit),
		model.publicationBindingActions(),
	)}
	switch {
	case publicationStatusRequiresRecheck(summary.Status):
		bar.labels = map[keymap.Action]string{
			keymap.ActionSubmitReview: keymap.ActionSubmitRecheck.Description(),
		}
	case summary.Event == review.PublicationEventDraft:
		bar.labels = map[keymap.Action]string{
			keymap.ActionSubmitReview: "sync draft",
		}
	}
	return bar.RenderRows(width, 2, styles, styles.panel)
}

func (model *Model) publicationBindingActions() []keymap.Action {
	actions := []keymap.Action{keymap.ActionCancel}
	if model.publicationIntent == publicationOperationIntentCancel ||
		model.publicationIntent == publicationOperationIntentSubmit {
		return actions
	}
	if primary := publicationPrimaryAction(model.review.PublicationSummary().Status); primary != keymap.ActionNone {
		actions = append(actions, primary)
	}
	if model.publicationEventEditable() {
		actions = append(actions,
			keymap.ActionPreviousSubmitEvent,
			keymap.ActionNextSubmitEvent,
		)
	}
	return actions
}

func publicationEventLabel(event review.PublicationEvent) string {
	switch event {
	case review.PublicationEventComment:
		return publicationEventCommentLabel
	case review.PublicationEventApprove:
		return publicationEventApproveLabel
	case review.PublicationEventRequestChanges:
		return publicationEventRequestChangesLabel
	default:
		return event.String()
	}
}

func boundedOperationDetail(value string) review.OperationDetail {
	return review.NewBoundedTrimmedOperationDetail(value)
}
