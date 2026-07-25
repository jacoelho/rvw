package app

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/review"
)

func (model *Model) minimumView(width, height int) string {
	line := "rvw needs at least 20×6; current " +
		fmt.Sprintf("%d×%d", model.size.Width, model.size.Height)
	lines := make([]string, 0, 4)
	if model.overlay == OverlayConfirm {
		if height == 1 {
			lines = append(lines, "decision pending; resize to decide")
		} else {
			lines = append(lines, "decision pending", "resize to decide")
		}
	}
	lines = append(lines, line)
	controls := model.bindingSummary(KeyContextMinimumSize)
	lines = append(lines, controls)
	return constrainedLines(lines, width, height)
}

func (model *Model) helpModalView(
	width, height int,
	styles semanticStyles,
) (string, boundedPanelLayout) {
	layout := newBoundedPanelLayout(width, height, boundedPanelRequest{
		minimumWidth:  40,
		minimumHeight: 10,
		maximumWidth:  80,
		maximumHeight: 30,
		inset:         2,
	})
	rendered := model.help.View(effectiveHelpKeyMap{
		bindings: model.effectiveBindings(KeyContextNormal),
		maxItems: max(layout.innerHeight-4, 1),
		offset:   model.helpTop,
	})
	lines := append([]string{styles.heading.Render("Keybindings"), ""}, strings.Split(rendered, "\n")...)
	lines = append(lines, "", styles.muted.Render(model.bindingSummary(KeyContextHelp)))
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

func (model *Model) statusModalView(
	width, height int,
	styles semanticStyles,
) (string, boundedPanelLayout) {
	layout := newBoundedPanelLayout(width, height, boundedPanelRequest{
		minimumWidth:  48,
		minimumHeight: 12,
		maximumWidth:  80,
		maximumHeight: 30,
		inset:         2,
	})
	lines := model.statusFactLines()
	operationCount := model.review.OperationCount()
	if operationCount != 0 {
		lines = append(lines, "", "Operations (newest first)")
		available := max(layout.innerHeight-len(lines)-3, 1)
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
			lines = append(lines, "  "+line)
		}
		remaining := operationCount - offset - len(operations)
		if remaining != 0 {
			lines = append(lines, fmt.Sprintf("  %d older records retained", remaining))
		}
	}
	lines = append(lines, "", styles.muted.Render(model.bindingSummary(KeyContextStatusDetails)))
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

func (model *Model) statusFactLines() []string {
	stats := model.coordinatorStats
	lines := []string{
		"Status Details",
		"",
		"── Session",
		fmt.Sprintf("  Source       %d", model.review.Snapshot().SourceID().Uint64()),
		fmt.Sprintf("  Workspace    %d", model.review.WorkspaceRevision().Uint64()),
		"",
		"── Resources",
		fmt.Sprintf("  Operations   %d/%d", stats.Accepted, MaxAcceptedOperations),
		fmt.Sprintf("  Queue bytes  %d/%d", stats.RequestBytes, MaxQueuedRequestBytes),
		fmt.Sprintf("  Results      %d/%d", stats.ReservedResult, MaxRetainedResultBytes),
		fmt.Sprintf("  I/O workers  %d/%d", stats.IOWorkers, MaxIOWorkers),
		fmt.Sprintf("  CPU workers  %d/%d", stats.CPUWorkers, MaxCPUWorkers),
	}
	latest := model.notice
	if latest == nil {
		latest = model.lastNotice
	}
	if latest != nil {
		lines = append(lines, "", "── Latest Notice", "  "+noticePlainText(*latest))
	}
	publication := model.review.PublicationSummary()
	if publication.Status != review.PublicationIdle {
		lines = append(lines, "", "── Publication", "  Status   "+publication.Status.String())
		if !publication.Detail.IsEmpty() {
			lines = append(lines, "  Detail   "+publication.Detail.String())
		}
		if publication.Marker != "" {
			lines = append(lines, "  Marker   "+publication.Marker.String())
		}
	}
	remoteThreads := model.review.Snapshot().RemoteThreadSummary()
	if remoteThreads.Status != review.RemoteThreadsNotRequested {
		lines = append(lines, "", "── Remote Threads",
			fmt.Sprintf("  Status   %s (%d)", remoteThreads.Status, remoteThreads.Count),
		)
		if !remoteThreads.Detail.IsEmpty() {
			lines = append(lines, "  Detail   "+remoteThreads.Detail.String())
		}
	}
	return lines
}

func noticePlainText(notice Notice) string {
	prefix := "ℹ "
	switch notice.Kind {
	case NoticeInformation:
	case NoticeWarning:
		prefix = "⚠ "
	case NoticeError:
		prefix = "✗ "
	}
	return prefix + string(notice.Text)
}

func (model *Model) statusOperationRows(height int) int {
	return max(height-len(model.statusFactLines())-5, 0)
}
