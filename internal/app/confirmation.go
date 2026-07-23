package app

import (
	tea "charm.land/bubbletea/v2"

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

func (model *Model) confirmationView(width, height int) string {
	prompt := "Confirm action?"
	switch model.confirmation.kind {
	case confirmationNone:
	case confirmationQuit:
		prompt = "Discard local review changes and quit?"
	case confirmationDeleteComment:
		prompt = "Delete the selected comment?"
		if model.review.PendingBindingState(model.confirmation.comment) != review.PendingBindingNew {
			prompt = "Delete this GitHub draft on the next save?"
		}
	case confirmationReload:
		prompt = "Reload and discard review state that cannot be reconciled?"
	}
	return constrainedLines(
		[]string{"Confirmation", "", prompt, "", model.bindingSummary(KeyContextConfirm)},
		width,
		height,
	)
}
