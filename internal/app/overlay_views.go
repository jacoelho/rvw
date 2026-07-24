package app

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"

	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
)

func (model *Model) minimumView(width, height int) string {
	styles := newSemanticStyles(model.settings)
	lines := make([]string, 0, 4)
	if model.overlay == OverlayConfirm {
		controls := model.modalBindingRows(
			keymap.ContextConfirm,
			[]keymap.Action{keymap.ActionCancel, keymap.ActionConfirmNo, keymap.ActionInterrupt},
			width,
			styles,
		)
		return blockingSurface([]string{
			modalTitle(styles, "Decision pending"),
			"Disabled: resize to decide.",
			fmt.Sprintf("Resize to %d×%d", minimumWidth, minimumHeight),
			fmt.Sprintf("Current %d×%d", model.size.Width, model.size.Height),
		}, controls, width, height, styles)
	}
	lines = append(lines,
		fmt.Sprintf("Resize to %d×%d", minimumWidth, minimumHeight),
		fmt.Sprintf("Current %d×%d", model.size.Width, model.size.Height),
	)
	bindings := bindingsForActions(
		model.effectiveBindings(keymap.ContextMinimumSize),
		[]keymap.Action{keymap.ActionQuit, keymap.ActionInterrupt},
	)
	lines = append(lines, bindingBar{bindings: bindings}.Render(width, styles))
	return constrainedStyledLines(lines, width, height)
}

func (model *Model) helpView(width, height int) string {
	styles := newSemanticStyles(model.settings)
	controls := model.modalBindingRows(
		keymap.ContextHelp,
		[]keymap.Action{keymap.ActionCancel, keymap.ActionNextHelpPage, keymap.ActionPreviousHelpPage},
		width,
		styles,
	)
	helpModel := model.help
	helpModel.Styles = help.Styles{
		Ellipsis:       styles.muted,
		ShortKey:       styles.heading,
		ShortDesc:      styles.muted,
		ShortSeparator: styles.muted,
		FullKey:        styles.heading,
		FullDesc:       styles.muted,
		FullSeparator:  styles.muted,
	}
	rendered := helpModel.View(effectiveHelpKeyMap{
		bindings: model.effectiveBindings(keymap.ContextNormal),
		maxItems: max(height-len(controls)-2, 1),
		offset:   model.helpTop,
	})
	lines := append(
		[]string{modalTitle(styles, "Keybindings · "+model.configFocus().String()), ""},
		strings.Split(rendered, "\n")...,
	)
	return blockingSurface(lines, controls, width, height, styles)
}

func (model *Model) statusView(width, height int) string {
	styles := newSemanticStyles(model.settings)
	controls := model.modalBindingRows(
		keymap.ContextStatusDetails,
		[]keymap.Action{keymap.ActionCancel, keymap.ActionMoveDown, keymap.ActionPageDown},
		width,
		styles,
	)
	lines := model.statusFactLines()
	if len(lines) != 0 {
		lines[0] = modalTitle(styles, lines[0])
	}
	operationCount := model.review.OperationCount()
	if operationCount != 0 {
		lines = append(lines, "", styles.heading.Render("Operations (newest first)"))
		available := model.statusOperationRows(max(height-len(controls), 0))
		offset := min(model.statusTop, max(operationCount-available, 0))
		operations := model.review.NewestOperations(offset, available)
		for _, operation := range operations {
			line := fmt.Sprintf(
				"#%d %-14s %s",
				operation.ID.Uint64(),
				operation.Kind.String(),
				operation.Status.String(),
			)
			if !operation.Detail.IsEmpty() {
				line += " — " + operation.Detail.String()
			} else if progress, ok := model.operationProgress[operation.ID]; ok {
				if progress.Total != 0 {
					line += fmt.Sprintf(
						" — %d/%d",
						progress.Completed,
						progress.Total,
					)
				}
				if !progress.Detail.IsEmpty() {
					line += " " + progress.Detail.String()
				}
			}
			lines = append(lines, safeText(line))
		}
		remaining := operationCount - offset - len(operations)
		if remaining != 0 && len(lines) < height-1 {
			lines = append(lines, fmt.Sprintf("%d older records retained", remaining))
		}
	}
	return blockingSurface(lines, controls, width, height, styles)
}

func modalTitle(styles semanticStyles, title string) string {
	return styles.focused.Render("▌ " + safeText(title))
}

func (model *Model) modalBindingRows(
	contextName keymap.Context,
	actions []keymap.Action,
	width int,
	styles semanticStyles,
) []string {
	return bindingBar{
		bindings: bindingsForActions(model.effectiveBindings(contextName), actions),
	}.RenderRows(width, 2, styles, styles.panel)
}

func (model *Model) statusFactLines() []string {
	stats := model.coordinatorStats
	lines := []string{
		"Status details",
		"",
		fmt.Sprintf("source revision      %d", model.review.Snapshot().SourceID().Uint64()),
		fmt.Sprintf("workspace revision   %d", model.review.WorkspaceRevision().Uint64()),
		fmt.Sprintf("accepted operations  %d/%d", stats.Accepted, MaxAcceptedOperations),
		fmt.Sprintf("request bytes        %d/%d", stats.RequestBytes, MaxQueuedRequestBytes),
		fmt.Sprintf("reserved results     %d/%d", stats.ReservedResult, MaxRetainedResultBytes),
		fmt.Sprintf("I/O workers          %d/%d", stats.IOWorkers, MaxIOWorkers),
		fmt.Sprintf("CPU workers          %d/%d", stats.CPUWorkers, MaxCPUWorkers),
	}
	if model.notice != nil {
		label := "latest"
		switch model.notice.Kind {
		case NoticeSuccess:
			label = "latest success"
		case NoticeWarning:
			label = "latest warning"
		case NoticeError:
			label = "latest error"
		case NoticeInformation:
		}
		lines = append(lines, "", label+": "+string(model.notice.Text))
	}
	switch model.fileProjection.workspace {
	case projectionInitial, projectionLoading:
		lines = append(lines, "", "file workspace       loading")
	case projectionFailed:
		lines = append(lines,
			"",
			"file workspace       error",
			"workspace detail     "+string(model.fileProjection.workspaceDetail),
		)
	case projectionReady:
	}
	if model.fileProjection.requestedQuery != "" ||
		model.fileProjection.appliedQuery != "" ||
		model.fileProjection.filter == projectionFailed {
		lines = append(lines, "", "file filter requested "+model.fileProjection.requestedQuery)
		lines = append(lines, "file filter applied   "+model.fileProjection.appliedQuery)
		switch model.fileProjection.filter {
		case projectionLoading:
			lines = append(lines, "file filter state     loading; prior results retained")
		case projectionFailed:
			lines = append(lines,
				"file filter state     error; prior results retained",
				"file filter detail    "+string(model.fileProjection.filterDetail),
			)
		case projectionInitial, projectionReady:
		}
	}
	publication := model.review.PublicationSummary()
	if publication.Status != review.PublicationIdle {
		lines = append(lines, "", "publication          "+publication.Status.String())
		if !publication.Detail.IsEmpty() {
			lines = append(lines, "publication detail   "+publication.Detail.String())
		}
		if publication.Marker != "" {
			lines = append(lines, "publication marker   "+publication.Marker.String())
		}
	}
	remoteThreads := model.review.Snapshot().RemoteThreadSummary()
	if remoteThreads.Status != review.RemoteThreadsNotRequested {
		status := remoteThreads.Status.String()
		if model.foreground.reloadActive() {
			status = "loading"
		}
		if remoteThreads.Count != 0 &&
			(model.foreground.reloadActive() ||
				remoteThreads.Status == review.RemoteThreadsUnavailable ||
				remoteThreads.Status == review.RemoteThreadsLimited) {
			status += "; prior results retained"
		}
		lines = append(
			lines,
			"",
			fmt.Sprintf(
				"remote threads       %s (%d)",
				status,
				remoteThreads.Count,
			),
		)
		if !remoteThreads.Detail.IsEmpty() {
			lines = append(lines, "remote thread detail "+remoteThreads.Detail.String())
		}
	}
	pending := model.review.PendingReviewSummary()
	switch pending.Status {
	case review.PendingReviewUnavailable, review.PendingReviewLimited,
		review.PendingReviewAmbiguous:
		lines = append(lines, "", "pending review       "+pending.Status.String())
		if !pending.Detail.IsEmpty() {
			lines = append(lines, "pending review detail "+pending.Detail.String())
		}
	case review.PendingReviewNotRequested, review.PendingReviewAbsent,
		review.PendingReviewReady:
	}
	for index := range lines {
		lines[index] = safeText(lines[index])
	}
	return lines
}

func (model *Model) statusOperationRows(height int) int {
	return max(height-len(model.statusFactLines())-5, 0)
}
