package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/tui/render"
)

func (model *Model) openPublication() {
	pending := model.review.PendingReviewSummary()
	model.publicationDraftReady = pending.Status == change.PendingReviewReady &&
		pending.Dirty == 0 && pending.Conflicts == 0
	model.startPublicationCheck()
}

func (model *Model) startPublicationCheck() {
	model.publicationIntent = publicationOperationIntentNone
	summary := model.review.PublicationSummary()
	if summary.Status == change.PublicationUnknown {
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

func (model *Model) publicationOptions() (change.PublicationOptions, error) {
	policies, err := change.NewPublicationPolicies()
	if err != nil {
		return change.PublicationOptions{}, err
	}
	event := change.PublicationEventDraft
	if model.publicationDraftReady {
		event = model.publicationEvent
	}
	return change.NewPublicationOptions(event, policies)
}

func (model *Model) nextOperationID() (change.OperationID, error) {
	operationID, err := change.NewOperationID(model.nextOperation)
	if err != nil {
		return 0, errors.New(operationIdentityExhausted)
	}
	model.nextOperation++
	return operationID, nil
}

func failPublicationCheck(
	current change.Review,
	request change.PublicationCheckRequest,
	failure error,
) change.Review {
	detail := boundedOperationDetail(failure.Error())
	next, err := current.ApplyPublicationCheck(change.PublicationCheckResult{
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
		change.OperationFailed,
		detail,
	)
	if err != nil {
		return current
	}
	return next
}

func (model *Model) claimPublicationOutcome(id change.OperationID) bool {
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
		result = change.PublicationCheckResult{
			Operation: outcome.ID,
			Workspace: model.review.WorkspaceRevision(),
			Source:    model.review.Snapshot().SourceID(),
			Detail:    boundedOperationDetail(outcome.Err.Error()),
		}
	}
	next, err := model.review.ApplyPublicationCheck(result)
	if err != nil {
		result = change.PublicationCheckResult{
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
	status := change.OperationSucceeded
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
		result := change.PublicationResult{
			Operation:   operationID,
			Workspace:   request.Workspace,
			Source:      request.Source,
			Disposition: change.PublicationDispositionFailedBeforeCommit,
			Detail:      boundedOperationDetail(err.Error()),
		}
		if failedReview, applyErr := next.ApplyPublication(result); applyErr == nil {
			model.review = resolvePublicationOperation(
				failedReview,
				operationID,
				change.OperationFailed,
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
	status := change.OperationSucceeded
	switch result.Disposition() {
	case change.PublicationDispositionCommitted:
	case change.PublicationDispositionUnknown:
		status = change.OperationUnknown
	case change.PublicationDispositionFailedBeforeCommit,
		change.PublicationDispositionCancelledBeforeCommit:
		status = change.OperationFailed
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
	if result.Disposition() == change.PublicationDispositionUnknown {
		model.notice = &Notice{
			Kind: NoticeError,
			Text: "publication outcome is unknown; reconciling exact remote state",
		}
		model.startReconciliation()
		return
	}
	if result.Disposition() == change.PublicationDispositionCommitted {
		message := "GitHub review published"
		kind := NoticeSuccess
		if model.review.PublicationSummary().Event == change.PublicationEventDraft {
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
		if model.review.PublicationSummary().Event == change.PublicationEventDraft &&
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
	reconciliationStatus := change.OperationSucceeded
	if result.Disposition() == change.ReconciliationStillUnknown {
		reconciliationStatus = change.OperationFailed
	}
	next = resolvePublicationOperation(
		next,
		outcome.ID,
		reconciliationStatus,
		result.Detail(),
	)
	if hasAttempt &&
		result.Disposition() != change.ReconciliationStillUnknown {
		settledStatus := change.OperationSucceeded
		if result.Disposition() == change.ReconciliationNotCommitted {
			settledStatus = change.OperationFailed
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
	case change.ReconciliationCommitted:
		model.notice = &Notice{
			Kind: NoticeSuccess,
			Text: "GitHub publication confirmed from exact remote state",
		}
	case change.ReconciliationNotCommitted:
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: "GitHub review was not published; retry is safe after a new check",
		}
	case change.ReconciliationStillUnknown:
		model.notice = &Notice{
			Kind: NoticeError,
			Text: "publication remains unknown; retry is fenced",
		}
	}
}

func resolvePublicationOperation(
	current change.Review,
	id change.OperationID,
	status change.OperationStatus,
	detail change.OperationDetail,
) change.Review {
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
		case change.PublicationChecking, change.PublicationPublishing:
			model.publicationIntent = publicationOperationIntentCancel
			model.coordinator.Cancel(model.publicationOperation)
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "cancelling publication; a claimed remote commit will remain fenced",
			}
		case change.PublicationChecked:
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
		case change.PublicationChecking:
			model.publicationIntent = publicationOperationIntentSubmit
			model.notice = &Notice{
				Kind: NoticeInformation,
				Text: "submission requested; waiting for the GitHub review check",
			}
		case change.PublicationChecked:
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

var publicationEvents = [...]change.PublicationEvent{
	change.PublicationEventComment,
	change.PublicationEventApprove,
	change.PublicationEventRequestChanges,
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
	if summary := model.review.PublicationSummary(); summary.Status == change.PublicationChecked &&
		summary.Event != change.PublicationEventDraft {
		model.restartPublicationCheck()
	}
}

func publicationPrimaryAction(status change.PublicationStatus) keymap.Action {
	switch status {
	case change.PublicationChecking, change.PublicationChecked,
		change.PublicationIdle, change.PublicationFailedBeforeCommit,
		change.PublicationUnknown, change.PublicationConfirmedAbsent:
		return keymap.ActionSubmitReview
	default:
		return keymap.ActionNone
	}
}

func publicationStatusRequiresRecheck(status change.PublicationStatus) bool {
	switch status {
	case change.PublicationIdle, change.PublicationFailedBeforeCommit,
		change.PublicationUnknown, change.PublicationConfirmedAbsent:
		return true
	default:
		return false
	}
}

func (model *Model) recheckPublication(status change.PublicationStatus) {
	if model.publicationOperation != 0 || !publicationStatusRequiresRecheck(status) {
		return
	}
	if status == change.PublicationUnknown {
		model.startReconciliation()
		return
	}
	model.restartPublicationCheck()
}

func publicationStatusAllowsEventSelection(status change.PublicationStatus) bool {
	return status == change.PublicationIdle || status == change.PublicationChecked
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
	if model.review.PublicationSummary().Status == change.PublicationChecked {
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
		if summary.Status == change.PublicationPublishing {
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
		lines = append(lines, render.SafeText(pendingProblem))
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
	summary change.PublicationSummary,
	pending change.PendingReviewSummary,
	pendingProblem string,
	lockedChoices bool,
	styles semanticStyles,
) []string {
	var lines []string
	switch summary.Status {
	case change.PublicationChecking:
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
	case change.PublicationChecked:
		lines = append(
			lines,
			model.publicationCheckedLines(summary, pending, pendingProblem, styles)...,
		)
	case change.PublicationPublishing:
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
	case change.PublicationUnknown:
		if lockedChoices {
			lines = append(lines,
				render.SafeText("Target: "+summary.Target.Repository()+"#"+
					fmt.Sprintf("%d", summary.Target.PullRequest())),
				"Reconciliation is required before retrying.",
			)
			break
		}
		lines = append(lines,
			"",
			styles.heading.Render("Submission"),
			"Outcome unknown; retry is fenced.",
			render.SafeText("Target: "+summary.Target.Repository()+"#"+
				fmt.Sprintf("%d", summary.Target.PullRequest()),
			),
			"Recheck to verify exact remote state before retrying.",
		)
	case change.PublicationReconciling:
		if lockedChoices {
			break
		}
		lines = append(lines, "", styles.heading.Render("Submission"),
			"Checking exact GitHub publication state…")
	case change.PublicationCommitted:
		if lockedChoices {
			break
		}
		lines = append(lines, "", styles.heading.Render("Submission"),
			"Review published.")
	case change.PublicationConfirmedAbsent:
		if lockedChoices {
			break
		}
		lines = append(lines, "", styles.heading.Render("Submission"),
			"No matching GitHub review was found; a checked retry is safe.")
	case change.PublicationFailedBeforeCommit:
		if lockedChoices {
			if !summary.Detail.IsEmpty() {
				lines = append(lines, render.SafeText(summary.Detail.String()))
			}
			break
		}
		lines = append(lines, "", styles.heading.Render("Submission"),
			render.SafeText("Publication failed before commit: "+summary.Detail.String()))
	default:
		lines = append(lines, "", styles.heading.Render("Submission"),
			"No GitHub review check is active.")
	}
	return lines
}

func (model *Model) publicationCheckedLines(
	summary change.PublicationSummary,
	pending change.PendingReviewSummary,
	pendingProblem string,
	styles semanticStyles,
) []string {
	stage := "Submit pending draft as " + model.publicationEvent.String()
	if summary.Event == change.PublicationEventDraft {
		stage = "Synchronize comments to a pending GitHub draft"
	}
	lines := []string{
		"",
		styles.heading.Render("Submission"),
		render.SafeText("Target: " + summary.Target.Repository() + "#" +
			fmt.Sprintf("%d", summary.Target.PullRequest()),
		),
		"Next step: " + stage,
		fmt.Sprintf("Comments: %d included, %d excluded", summary.Included, summary.Excluded),
	}
	if pending.Status == change.PendingReviewReady {
		lines = append(lines, fmt.Sprintf(
			"Pending draft: %d changed · %d conflicts",
			pending.Dirty, pending.Conflicts,
		))
	} else if pendingProblem == "" {
		lines = append(lines, render.SafeText("Pending draft: "+pending.Status.String()))
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
	summary change.PublicationSummary,
	intent publicationOperationIntent,
	styles semanticStyles,
) []string {
	lines := []string{modalTitle(styles, "Publish GitHub review"), ""}
	var status string
	switch summary.Status {
	case change.PublicationChecking:
		status = "Checking GitHub…"
		if intent == publicationOperationIntentSubmit {
			status = "Submission queued"
		}
	case change.PublicationChecked:
		status = "Ready to publish"
		if summary.Event == change.PublicationEventDraft {
			status = "Ready to sync draft"
		}
	case change.PublicationPublishing:
		status = "Publishing review…"
		if summary.Event == change.PublicationEventDraft {
			status = "Syncing draft…"
		}
	case change.PublicationCommitted:
		status = "Review published"
		if summary.Event == change.PublicationEventDraft {
			status = "Draft synchronized"
		}
	case change.PublicationFailedBeforeCommit:
		status = "Failed before commit"
	case change.PublicationUnknown:
		status = "Outcome unknown"
	case change.PublicationReconciling:
		status = "Reconciling review…"
	case change.PublicationConfirmedAbsent:
		status = "Remote review absent"
	default:
		status = "No review check active"
	}
	return append(lines, styles.heading.Render(status))
}

func pendingReviewProblemLine(pending change.PendingReviewSummary) string {
	switch pending.Status {
	case change.PendingReviewUnavailable,
		change.PendingReviewLimited,
		change.PendingReviewAmbiguous:
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
	lines := []string{styles.heading.Render(render.SafeText(heading))}
	for _, event := range publicationEvents {
		if event == model.publicationEvent {
			lines = append(lines, styles.selectedControl.Render("(●) "+publicationEventLabel(event)))
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
	bindings := keymap.SelectBindings(
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
	bindings := keymap.SelectBindings(model.effectiveBindings(keymap.ContextSubmit), actions)
	var summary strings.Builder
	for index, binding := range bindings {
		keymap.AppendSummary(&summary, index, binding.Chord, binding.Action)
	}
	return summary.String()
}

func (model *Model) publicationBindingRows(width int, styles semanticStyles) []string {
	summary := model.review.PublicationSummary()
	bar := bindingBar{bindings: keymap.SelectBindings(
		model.effectiveBindings(keymap.ContextSubmit),
		model.publicationBindingActions(),
	)}
	switch {
	case publicationStatusRequiresRecheck(summary.Status):
		bar.labels = map[keymap.Action]string{
			keymap.ActionSubmitReview: keymap.ActionSubmitRecheck.Description(),
		}
	case summary.Event == change.PublicationEventDraft:
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

func publicationEventLabel(event change.PublicationEvent) string {
	switch event {
	case change.PublicationEventComment:
		return publicationEventCommentLabel
	case change.PublicationEventApprove:
		return publicationEventApproveLabel
	case change.PublicationEventRequestChanges:
		return publicationEventRequestChangesLabel
	default:
		return event.String()
	}
}

func boundedOperationDetail(value string) change.OperationDetail {
	return change.NewBoundedTrimmedOperationDetail(value)
}
