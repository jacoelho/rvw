package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/keymap"
)

type exportDestination uint8

const (
	destinationClipboard exportDestination = iota + 1
	destinationStdout
)

func (model *Model) openExportPicker(selection exportSelection) {
	if !selection.options.Ready() {
		model.notice = &Notice{Kind: NoticeWarning, Text: "invalid export options"}
		return
	}
	if selection.destination != destinationClipboard &&
		selection.destination != destinationStdout {
		model.notice = &Notice{Kind: NoticeWarning, Text: "invalid export destination"}
		return
	}
	if selection.format != change.ExportMarkdown && selection.format != change.ExportJSON ||
		selection.format == change.ExportJSON &&
			selection.destination != destinationStdout {
		model.notice = &Notice{Kind: NoticeWarning, Text: "invalid export format and destination"}
		return
	}
	model.exportPicker = selection
	model.overlay = OverlayExport
}

func (model *Model) applyExportPickerAction(action keymap.Action) {
	switch action {
	case keymap.ActionCancel, keymap.ActionInterrupt, keymap.ActionQuit:
		model.overlay = OverlayNone
	case keymap.ActionToggleExportUnmap:
		model.toggleExportOption(change.ExportUnmappable)
	case keymap.ActionExportSelected:
		selection := model.exportPicker
		model.overlay = OverlayNone
		model.startExport(selection)
	default:
	}
}

func (model *Model) toggleExportOption(option change.ExportOption) {
	next, err := model.exportPicker.options.Toggle(option)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		return
	}
	model.exportPicker.options = next
}

func (model *Model) startExport(selection exportSelection) {
	operationID, err := change.NewOperationID(model.nextOperation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: operationIdentityExhausted}
		return
	}
	model.nextOperation++
	next, err := model.review.BeginOperation(operationID, change.OperationExport)
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
		status := change.OperationFailed
		if errors.Is(err, ErrCoordinatorSaturated) {
			status = change.OperationLimited
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
	if model.exportPicker.format == change.ExportJSON {
		format = "JSON"
	}
	destination := "clipboard"
	if model.exportPicker.destination == destinationStdout {
		destination = "stdout"
	}
	checked := func(option change.ExportOption) string {
		if model.exportPicker.options.Has(option) {
			return "[x]"
		}
		return "[ ]"
	}
	styles := newSemanticStyles(model.settings)
	preferred := dialogSize{width: 52, height: 9}
	spec := dialogSpec{
		preferredInner: preferred,
		borderMinimum:  dialogSize{width: 24, height: 10},
	}
	dialogLayout := newDialogLayout(width, height, spec)
	controls := model.modalBindingRows(
		keymap.ContextExportPicker,
		[]keymap.Action{keymap.ActionCancel, keymap.ActionExportSelected, keymap.ActionToggleExportUnmap},
		dialogLayout.inner.width,
		styles,
	)
	toggleChord := model.exportToggleChord()
	fullOptionLine, fullChordVisible := exportOptionLine(
		checked(change.ExportUnmappable),
		toggleChord,
		"include unmappable comments",
		dialogLayout.inner.width,
	)
	body := []string{
		modalTitle(styles, "Export selection"),
		"",
		"format       " + format,
		"destination  " + destination,
		"",
		fullOptionLine,
	}
	if len(body)+len(controls) > dialogLayout.inner.height || toggleChord != "" && !fullChordVisible {
		compactOptionLine, _ := exportOptionLine(
			checked(change.ExportUnmappable),
			toggleChord,
			"unmappable",
			dialogLayout.inner.width,
		)
		body = []string{
			modalTitle(styles, "Export selection"),
			format + " → " + destination,
			compactOptionLine,
		}
	}
	spec.preferredInner = contentSizedDialog(preferred, body, controls)
	workspace, _ := model.workspaceView(width, height, model.workspaceLayout(width, height))
	return standardDialogOverlay(workspace, body, controls, width, height, spec, styles)
}

func exportOptionLine(check, chord, label string, width int) (string, bool) {
	withoutChord := check + " " + label
	if chord == "" {
		return withoutChord, false
	}
	withChord := check + " " + chord + " " + label
	if lipgloss.Width(withChord) <= width {
		return withChord, true
	}
	return withoutChord, false
}

func (model *Model) exportToggleChord() string {
	bindings := model.settings.keymap.BindingsFor(
		keymap.Scope{Context: keymap.ContextExportPicker},
		[]keymap.Action{keymap.ActionToggleExportUnmap},
	)
	if len(bindings) == 0 {
		return ""
	}
	return bindings[0].Chord.Display()
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
			change.OperationFailed,
			boundedOperationDetail(outcome.Err.Error()),
		); err != nil {
			model.notice = operationTransitionNotice(err)
		}
		return nil
	}
	if outcome.Prepared.Format() != request.format ||
		outcome.Prepared.Bytes() > change.MaxPreparedExportBytes {
		err := errors.New("export result is invalid")
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(err.Error())}
		if resolveErr := model.settleOperation(
			outcome.ID,
			change.OperationFailed,
			boundedOperationDetail(err.Error()),
		); resolveErr != nil {
			model.notice = operationTransitionNotice(resolveErr)
		}
		return nil
	}
	if err := model.settleOperation(
		outcome.ID,
		change.OperationSucceeded,
		change.EmptyOperationDetail(),
	); err != nil {
		model.notice = operationTransitionNotice(err)
		return nil
	}
	if request.destination == destinationStdout {
		prepared := outcome.Prepared
		model.pendingStdout = new(prepared)
		model.quitting = true
		model.coordinator.StopAdmissionAndCancel()
		return tea.Quit
	}
	model.startDelivery(outcome.Prepared)
	return nil
}

func (model *Model) startDelivery(
	prepared change.PreparedExport,
) {
	operationID, err := change.NewOperationID(model.nextOperation)
	if err != nil {
		model.notice = &Notice{Kind: NoticeError, Text: operationIdentityExhausted}
		return
	}
	model.nextOperation++
	next, err := model.review.BeginOperation(operationID, change.OperationDelivery)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return
	}
	if err := model.coordinator.SubmitDelivery(DeliveryWork{
		ID:       operationID,
		Prepared: prepared,
	}); err != nil {
		status := change.OperationFailed
		if errors.Is(err, ErrCoordinatorSaturated) {
			status = change.OperationLimited
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
	model.deliveryOperations[operationID] = struct{}{}
	model.notice = &Notice{Kind: NoticeInformation, Text: "delivering export"}
}

func (model *Model) applyDeliveryOutcome(outcome DeliveryOutcome) {
	if _, tracked := model.deliveryOperations[outcome.ID]; !tracked {
		model.notice = &Notice{Kind: NoticeWarning, Text: "stale delivery outcome"}
		return
	}
	delete(model.deliveryOperations, outcome.ID)
	if outcome.Err != nil {
		status := change.OperationFailed
		notice := outcome.Err.Error()
		if outcome.Outcome.State == delivery.CommitUnknown {
			status = change.OperationUnknown
			notice = "delivery outcome fenced; " + notice
		}
		model.notice = &Notice{Kind: NoticeError, Text: NoticeText(notice)}
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
		change.OperationSucceeded,
		change.EmptyOperationDetail(),
	); err != nil {
		model.notice = operationTransitionNotice(err)
		return
	}
	model.notice = &Notice{Kind: NoticeSuccess, Text: "export delivered"}
}

func (model *Model) settleOperation(
	id change.OperationID,
	status change.OperationStatus,
	detail change.OperationDetail,
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
func (model *Model) PendingStdout() (change.PreparedExport, bool) {
	if model.pendingStdout == nil || !model.quitting {
		return change.PreparedExport{}, false
	}
	return *model.pendingStdout, true
}
