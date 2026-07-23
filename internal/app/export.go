package app

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/review"
)

func (model *Model) openExportPicker(selection exportSelection) {
	if !selection.options.Ready() {
		model.notice = &Notice{Kind: NoticeWarning, Text: "invalid export options"}
		return
	}
	if selection.destination != delivery.DestinationClipboard &&
		selection.destination != delivery.DestinationStdout {
		model.notice = &Notice{Kind: NoticeWarning, Text: "invalid export destination"}
		return
	}
	if selection.format != review.ExportMarkdown && selection.format != review.ExportJSON ||
		selection.format == review.ExportJSON &&
			selection.destination != delivery.DestinationStdout {
		model.notice = &Notice{Kind: NoticeWarning, Text: "invalid export format and destination"}
		return
	}
	model.exportPicker = selection
	model.overlay = OverlayExport
}

func (model *Model) applyExportPickerAction(action KeyAction) {
	switch action {
	case KeyActionCancel, KeyActionInterrupt, KeyActionQuit:
		model.overlay = OverlayNone
	case KeyActionToggleExportUnmap:
		model.toggleExportOption(review.ExportUnmappable)
	case KeyActionExportSelected:
		selection := model.exportPicker
		model.overlay = OverlayNone
		model.startExport(selection)
	default:
	}
}

func (model *Model) toggleExportOption(option review.ExportOption) {
	next, err := model.exportPicker.options.Toggle(option)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	model.exportPicker.options = next
}

func (model *Model) startExport(selection exportSelection) {
	operationID, err := review.NewOperationID(model.nextOperation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: operationIdentityExhausted}
		return
	}
	model.nextOperation++
	next, err := model.review.BeginOperation(operationID, review.OperationExport)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	if err := model.coordinator.SubmitExport(ExportWork{
		ID:          operationID,
		Source:      model.review.ExportSource(),
		Templates:   model.settings.exportTemplates,
		Options:     selection.options,
		Format:      selection.format,
		Destination: selection.destination,
	}); err != nil {
		status := review.OperationFailed
		if errors.Is(err, ErrCoordinatorSaturated) {
			status = review.OperationLimited
		}
		if resolved, resolveErr := next.ResolveOperation(
			operationID,
			status,
			boundedOperationDetail(err.Error()),
		); resolveErr == nil {
			model.review = resolved
		}
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.exportOperations[operationID] = exportRequest{
		format:      selection.format,
		destination: selection.destination,
	}
	model.notice = &Notice{Kind: NoticeInformation, Text: "preparing export"}
}

func (model *Model) exportPickerView(width, height int) string {
	format := "Markdown"
	if model.exportPicker.format == review.ExportJSON {
		format = "JSON"
	}
	destination := "clipboard"
	if model.exportPicker.destination == delivery.DestinationStdout {
		destination = "stdout"
	}
	checked := func(option review.ExportOption) string {
		if model.exportPicker.options.Has(option) {
			return "[x]"
		}
		return "[ ]"
	}
	return constrainedLines([]string{
		"Export selection",
		"",
		"format       " + format,
		"destination  " + destination,
		"",
		checked(review.ExportUnmappable) + " include unmappable comments",
		"",
		model.bindingSummary(KeyContextExportPicker),
	}, width, height)
}

func (model *Model) applyExportOutcome(outcome ExportOutcome) tea.Cmd {
	request, tracked := model.exportOperations[outcome.ID]
	if !tracked || request.destination != outcome.Destination {
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale export outcome"}
		return nil
	}
	delete(model.exportOperations, outcome.ID)
	if outcome.Err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(outcome.Err.Error())}
		if err := model.settleOperation(
			outcome.ID,
			review.OperationFailed,
			boundedOperationDetail(outcome.Err.Error()),
		); err != nil {
			model.notice = operationTransitionNotice(err)
		}
		return nil
	}
	if outcome.Prepared.Format() != request.format ||
		outcome.Prepared.Bytes() > review.MaxPreparedExportBytes {
		err := errors.New("export result is invalid")
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		if resolveErr := model.settleOperation(
			outcome.ID,
			review.OperationFailed,
			boundedOperationDetail(err.Error()),
		); resolveErr != nil {
			model.notice = operationTransitionNotice(resolveErr)
		}
		return nil
	}
	if err := model.settleOperation(
		outcome.ID,
		review.OperationSucceeded,
		review.EmptyOperationDetail(),
	); err != nil {
		model.notice = operationTransitionNotice(err)
		return nil
	}
	if request.destination == delivery.DestinationStdout {
		prepared := outcome.Prepared
		model.pendingStdout = new(prepared)
		model.quitting = true
		model.coordinator.StopAdmissionAndCancel()
		return tea.Quit
	}
	model.startDelivery(outcome.Prepared, request.destination)
	return nil
}

func (model *Model) startDelivery(
	prepared review.PreparedExport,
	destination delivery.Destination,
) {
	operationID, err := review.NewOperationID(model.nextOperation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: operationIdentityExhausted}
		return
	}
	model.nextOperation++
	next, err := model.review.BeginOperation(operationID, review.OperationDelivery)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	if err := model.coordinator.SubmitDelivery(DeliveryWork{
		ID:          operationID,
		Prepared:    prepared,
		Destination: destination,
	}); err != nil {
		status := review.OperationFailed
		if errors.Is(err, ErrCoordinatorSaturated) {
			status = review.OperationLimited
		}
		if resolved, resolveErr := next.ResolveOperation(
			operationID,
			status,
			boundedOperationDetail(err.Error()),
		); resolveErr == nil {
			model.review = resolved
		}
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	model.review = next
	model.deliveryOperations[operationID] = destination
	model.notice = &Notice{Kind: NoticeInformation, Text: "delivering export"}
}

func (model *Model) applyDeliveryOutcome(outcome DeliveryOutcome) {
	destination, tracked := model.deliveryOperations[outcome.ID]
	if !tracked || destination != outcome.Outcome.Destination {
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale delivery outcome"}
		return
	}
	delete(model.deliveryOperations, outcome.ID)
	if outcome.Err != nil {
		status := review.OperationFailed
		if outcome.Outcome.State == delivery.CommitUnknown {
			status = review.OperationUnknown
		}
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(outcome.Err.Error())}
		if err := model.settleOperation(
			outcome.ID,
			status,
			boundedOperationDetail(outcome.Err.Error()),
		); err != nil {
			model.notice = operationTransitionNotice(err)
		}
		return
	}
	if err := model.settleOperation(
		outcome.ID,
		review.OperationSucceeded,
		review.EmptyOperationDetail(),
	); err != nil {
		model.notice = operationTransitionNotice(err)
		return
	}
	model.notice = &Notice{Kind: NoticeInformation, Text: "export delivered"}
}

func (model *Model) settleOperation(
	id review.OperationID,
	status review.OperationStatus,
	detail review.OperationDetail,
) error {
	next, err := model.review.ResolveOperation(id, status, detail)
	if err != nil {
		return err
	}
	model.review = next
	return nil
}

func operationTransitionNotice(err error) *Notice {
	return &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
}

// PendingStdout returns the receiver's pending stdout value.
func (model *Model) PendingStdout() (review.PreparedExport, bool) {
	if model.pendingStdout == nil || !model.quitting {
		return review.PreparedExport{}, false
	}
	return *model.pendingStdout, true
}
