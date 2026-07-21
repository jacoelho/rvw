package app

import (
	"fmt"
	"strings"

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

func (model *Model) helpView(width, height int) string {
	rendered := model.help.View(effectiveHelpKeyMap{
		bindings: model.effectiveBindings(KeyContextNormal),
		maxItems: max(height-4, 1),
		offset:   model.helpTop,
	})
	lines := append([]string{"Effective help", ""}, strings.Split(rendered, "\n")...)
	lines = append(lines, "", model.bindingSummary(KeyContextHelp))
	return constrainedLines(lines, width, height)
}

func (model *Model) statusView(width, height int) string {
	lines := model.statusFactLines()
	operationCount := model.review.OperationCount()
	if operationCount != 0 {
		lines = append(lines, "", "Operations (newest first)")
		available := model.statusOperationRows(height)
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
			lines = append(lines, line)
		}
		remaining := operationCount - offset - len(operations)
		if remaining != 0 && len(lines) < height-1 {
			lines = append(lines, fmt.Sprintf("%d older records retained", remaining))
		}
	}
	lines = append(lines, "", model.bindingSummary(KeyContextStatusDetails))
	return constrainedLines(lines, width, height)
}

func (model *Model) statusFactLines() []string {
	stats := model.coordinatorStats
	lines := []string{
		"Status details",
		"",
		fmt.Sprintf("accepted operations  %d/%d", stats.Accepted, MaxAcceptedOperations),
		fmt.Sprintf("request bytes        %d/%d", stats.RequestBytes, MaxQueuedRequestBytes),
		fmt.Sprintf("reserved results     %d/%d", stats.ReservedResult, MaxRetainedResultBytes),
		fmt.Sprintf("I/O workers          %d/%d", stats.IOWorkers, MaxIOWorkers),
		fmt.Sprintf("CPU workers          %d/%d", stats.CPUWorkers, MaxCPUWorkers),
	}
	if model.notice != nil {
		lines = append(lines, "", "latest: "+string(model.notice.Text))
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
		lines = append(
			lines,
			"",
			fmt.Sprintf(
				"remote threads       %s (%d)",
				remoteThreads.Status,
				remoteThreads.Count,
			),
		)
		if !remoteThreads.Detail.IsEmpty() {
			lines = append(lines, "remote thread detail "+remoteThreads.Detail.String())
		}
	}
	return lines
}

func (model *Model) statusOperationRows(height int) int {
	return max(height-len(model.statusFactLines())-5, 0)
}
