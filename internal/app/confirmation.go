package app

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/review"
)

type confirmationKind uint8

const (
	confirmationNone confirmationKind = iota
	confirmationQuit
	confirmationDeleteComment
	confirmationReload
)

type confirmation struct {
	kind    confirmationKind
	comment review.CommentID
}

func (model *Model) requestQuit() tea.Cmd {
	if model.review.HasLocalChanges() {
		model.confirmation = confirmation{kind: confirmationQuit}
		model.overlay = OverlayConfirm
		model.abandonChord()
		return nil
	}
	return model.quit()
}

func (model *Model) forceQuit() tea.Cmd {
	return model.quit()
}

func (model *Model) quit() tea.Cmd {
	model.quitting = true
	model.abandonCollapseCapture()
	model.coordinator.StopAdmissionAndCancel()
	return tea.Quit
}

func (model *Model) requestCommentDelete() {
	count := model.review.CommentCount()
	if count == 0 || model.commentCursor >= count {
		if model.commentCursor >= count && model.review.Snapshot().RemoteThreadCount() != 0 {
			model.notice = &Notice{Kind: NoticeInformation, Text: "remote comments are read-only"}
		}
		return
	}
	model.commentCursor = min(max(model.commentCursor, 0), count-1)
	comment, ok := model.review.CommentAt(model.commentCursor)
	if !ok {
		return
	}
	model.confirmation = confirmation{
		kind:    confirmationDeleteComment,
		comment: comment.ID(),
	}
	model.overlay = OverlayConfirm
	model.abandonChord()
}

func (model *Model) requestReload() {
	if model.review.HasLocalChanges() {
		model.confirmation = confirmation{kind: confirmationReload}
		model.overlay = OverlayConfirm
		model.abandonChord()
		return
	}
	model.startReload()
}

func (model *Model) applyConfirmationAction(action KeyAction) tea.Cmd {
	switch action {
	case KeyActionCancel, KeyActionConfirmNo, KeyActionInterrupt:
		model.confirmation = confirmation{}
		model.overlay = OverlayNone
	case KeyActionConfirmYes:
		pending := model.confirmation
		if pending.kind == confirmationQuit {
			return model.quit()
		}
		model.confirmation = confirmation{}
		model.overlay = OverlayNone
		switch pending.kind {
		case confirmationNone, confirmationQuit:
		case confirmationDeleteComment:
			model.deleteComment(pending.comment)
		case confirmationReload:
			model.startReload()
		}
	default:
	}
	return nil
}

func (model *Model) confirmationModalView(
	width, height int,
	styles semanticStyles,
) (string, boundedPanelLayout) {
	prompt := "Confirm action?"
	isDestructive := false
	switch model.confirmation.kind {
	case confirmationNone:
	case confirmationQuit:
		prompt = "Discard local review changes and quit?"
		isDestructive = true
	case confirmationDeleteComment:
		prompt = "Delete the selected comment?"
		isDestructive = true
		if model.review.PendingBindingState(model.confirmation.comment) != review.PendingBindingNew {
			prompt = "Delete this GitHub draft on the next save?"
		}
	case confirmationReload:
		prompt = "Reload and discard review state that cannot be reconciled?"
		isDestructive = true
	}
	layout := newBoundedPanelLayout(width, height, boundedPanelRequest{
		minimumWidth:  32,
		minimumHeight: 8,
		maximumWidth:  72,
		maximumHeight: 10,
		inset:         2,
	})
	actions := model.actionHintSummary(
		KeyContextConfirm,
		actionHint{action: KeyActionConfirmYes, label: "Yes"},
		actionHint{action: KeyActionConfirmNo, label: "No"},
		actionHint{action: KeyActionCancel, label: "Cancel"},
	)
	title := "Confirmation"
	if isDestructive {
		title = "⚠ " + title
	}
	lines := []string{
		styles.warning.Render(title),
		"",
		safeAndClip(prompt, layout.innerWidth),
		"",
		styles.muted.Render(safeAndClip(actions, layout.innerWidth)),
	}
	content := constrainedStyledLines(lines, layout.innerWidth, layout.innerHeight)
	panelStyle := styles.panel.Width(layout.width).Height(layout.height)
	if layout.bordered {
		borderStyle := lipgloss.RoundedBorder()
		panelStyle = panelStyle.Border(borderStyle)
		if isDestructive {
			if foreground := styles.warning.GetForeground(); foreground != nil {
				panelStyle = panelStyle.BorderForeground(foreground)
			}
		} else if foreground := styles.borderFocused.GetForeground(); foreground != nil {
			panelStyle = panelStyle.BorderForeground(foreground)
		}
	}
	return panelStyle.Render(content), layout
}
