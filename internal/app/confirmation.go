package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/keymap"
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
	if count == 0 || model.commentCursor >= count ||
		!model.commentRowSelected(model.commentCursor) {
		if model.commentCursor >= count && model.review.Snapshot().RemoteThreadCount() != 0 {
			model.notice = &Notice{Kind: NoticeInformation, Text: remoteCommentsReadOnly}
		}
		return
	}
	model.selectCommentIndex(model.commentCursor)
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

func (model *Model) applyConfirmationAction(action keymap.Action) tea.Cmd {
	switch action {
	case keymap.ActionCancel, keymap.ActionConfirmNo, keymap.ActionInterrupt:
		model.confirmation = confirmation{}
		model.overlay = OverlayNone
	case keymap.ActionConfirmYes:
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
	styles := newSemanticStyles(model.settings)
	preferred := dialogSize{width: 40, height: 7}
	spec := dialogSpec{
		preferredInner: preferred,
		borderMinimum:  dialogSize{width: 24, height: 10},
	}
	dialogLayout := newDialogLayout(width, height, spec)
	controls := model.modalBindingRows(
		keymap.ContextConfirm,
		[]keymap.Action{keymap.ActionCancel, keymap.ActionConfirmYes, keymap.ActionConfirmNo},
		dialogLayout.inner.width,
		styles,
	)
	promptLines := strings.Split(
		lipgloss.Wrap(safeText(prompt), max(dialogLayout.inner.width, 1), ""),
		"\n",
	)
	body := []string{modalTitle(styles, "Confirmation")}
	if dialogLayout.inner.height > minimumHeight {
		body = append(body, "")
	}
	body = append(body, promptLines...)
	if len(body)+len(controls) > dialogLayout.inner.height {
		body = strings.Split(
			lipgloss.Wrap(
				modalTitle(styles, prompt),
				max(dialogLayout.inner.width, 1),
				"",
			),
			"\n",
		)
	}
	spec.preferredInner = contentSizedDialog(preferred, body, controls)
	workspace, _ := model.workspaceView(width, height, model.workspaceLayout(width, height))
	return standardDialogOverlay(
		workspace,
		body,
		controls,
		width,
		height,
		spec,
		styles,
	)
}
