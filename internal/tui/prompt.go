package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/tui/render"
)

const maxPromptBytes = 64 << 10

const promptInputLimitNotice = "prompt input limit reached: 65536 bytes"

const (
	draftConflictLocalValue  = "local"
	draftConflictRemoteValue = "remote"
	draftConflictBaseValue   = "base"
)

type commandKind uint8

const (
	commandQuit commandKind = iota + 1
	commandReload
	commandToggleDiff
	commandCopy
	commandExport
	commandJSON
	commandSubmit
	commandResolveDraft
	commandAcknowledgeSubmit
	commandHelp
	commandFilter
	commandSearchAll
	commandSearchCancel
	commandContext
	commandOldLine
	commandPath
	commandWrap
	commandNewLine
)

type commandIntent struct {
	text    string
	number  uint32
	kind    commandKind
	enabled bool
	toggle  bool
	whole   bool
	force   bool
}

func (model *Model) beginCommandPrompt() tea.Cmd {
	model.abandonChord()
	model.mode = ModeCommand
	model.commandInput.SetValue("")
	return model.commandInput.Focus()
}

func (model *Model) handlePromptKey(message tea.KeyPressMsg) tea.Cmd {
	stroke := keyStrokeFrom(message)
	if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
		if action == keymap.ActionNone {
			return command
		}
		if action == keymap.ActionCancel || action == keymap.ActionInterrupt {
			model.cancelPrompt()
			return nil
		}
		if action == keymap.ActionAccept {
			command := model.commandInput.Value()
			model.cancelPrompt()
			return model.executeCommand(command)
		}
		if action == keymap.ActionDeleteBackward {
			message = tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
		}
	}

	return model.updatePromptInput(message)
}

func (model *Model) updatePromptInput(message tea.Msg) tea.Cmd {
	input := &model.commandInput
	if paste, ok := message.(tea.PasteMsg); ok && len(paste.Content) > maxPromptBytes {
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: promptInputLimitNotice,
		}
		return nil
	}
	previous := input.Value()
	previousPosition := input.Position()
	updated, command := input.Update(message)
	*input = updated
	value := input.Value()
	if len(value) > maxPromptBytes {
		input.SetValue(previous)
		input.SetCursor(previousPosition)
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: promptInputLimitNotice,
		}
		return command
	}
	if render.ContainsBidiControl(value) {
		input.SetValue(previous)
		input.SetCursor(previousPosition)
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: "command input contains bidirectional control",
		}
		return command
	}
	return command
}

func (model *Model) cancelPrompt() {
	model.searchInput.Blur()
	model.commandInput.Blur()
	model.mode = ModeNormal
	model.abandonChord()
}

func (model *Model) resizePrompts() {
	width := max(min(model.size.Width, maxRenderWidth)-4, 1)
	if model.overlay == OverlayFind {
		renderWidth, renderHeight := model.renderSize()
		layout := newFindModalLayout(renderWidth, renderHeight)
		width = max(
			layout.dialog.inner.width-lipgloss.Width(model.searchInput.Prompt)-1,
			1,
		)
	}
	model.searchInput.SetWidth(width)
	commandWidth := min(model.size.Width, maxRenderWidth) -
		lipgloss.Width(model.commandInput.Prompt) - 1
	model.commandInput.SetWidth(max(commandWidth, 1))
}

func (model *Model) commandBarLine(width int, styles semanticStyles) string {
	commandInput := model.commandInput
	inputStyles := commandInput.Styles()
	inputStyles.Focused.Prompt = styles.heading.Inherit(styles.status)
	inputStyles.Focused.Text = styles.status
	inputStyles.Focused.Placeholder = styles.muted.Inherit(styles.status)
	inputStyles.Focused.Suggestion = styles.muted.Inherit(styles.status)
	inputStyles.Blurred = inputStyles.Focused
	inputStyles.Cursor.Color = styles.status.GetForeground()
	commandInput.SetStyles(inputStyles)
	return styles.status.Render(fitCellWidth(commandInput.View(), width))
}

func parseCommand(value string) (commandIntent, error) { //nolint:funlen // One closed switch owns the complete command grammar.
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), ":"))
	if value == "" {
		return commandIntent{}, errors.New("command is empty")
	}
	name, arguments, _ := strings.Cut(value, " ")
	arguments = strings.TrimSpace(arguments)
	noArguments := func(kind commandKind) (commandIntent, error) {
		if arguments != "" {
			return commandIntent{}, fmt.Errorf("%s does not accept arguments", name)
		}
		return commandIntent{kind: kind}, nil
	}
	switch name {
	case "q":
		return noArguments(commandQuit)
	case "q!":
		intent, err := noArguments(commandQuit)
		intent.force = true
		return intent, err
	case reloadLabel:
		return noArguments(commandReload)
	case "diff":
		return noArguments(commandToggleDiff)
	case "copy":
		return noArguments(commandCopy)
	case exportLabel:
		return noArguments(commandExport)
	case "json":
		return noArguments(commandJSON)
	case "submit":
		return noArguments(commandSubmit)
	case "resolve-draft":
		return parseResolveDraftCommand(arguments)
	case "ack-submit":
		return noArguments(commandAcknowledgeSubmit)
	case helpLabel:
		return noArguments(commandHelp)
	case "filter":
		return commandIntent{kind: commandFilter, text: arguments}, nil
	case "search-all":
		if arguments == "" {
			return commandIntent{}, errors.New("search-all requires text")
		}
		return commandIntent{kind: commandSearchAll, text: arguments}, nil
	case "search-cancel":
		return noArguments(commandSearchCancel)
	case "context":
		if arguments == "" {
			return commandIntent{kind: commandContext, whole: true}, nil
		}
		number, err := strconv.ParseUint(arguments, 10, 16)
		if err != nil {
			return commandIntent{}, errors.New("context requires non-negative lines")
		}
		return commandIntent{kind: commandContext, number: uint32(number)}, nil
	case "old-line":
		number, err := parsePositiveLine(arguments)
		return commandIntent{kind: commandOldLine, number: number}, err
	case "path":
		if arguments == "" {
			return commandIntent{}, errors.New("path requires an exact path")
		}
		return commandIntent{kind: commandPath, text: arguments}, nil
	case "set":
		switch arguments {
		case "wrap":
			return commandIntent{kind: commandWrap, enabled: true}, nil
		case "nowrap":
			return commandIntent{kind: commandWrap}, nil
		case "wrap!":
			return commandIntent{kind: commandWrap, toggle: true}, nil
		default:
			return commandIntent{}, errors.New("set accepts wrap, nowrap, or wrap")
		}
	default:
		number, err := parsePositiveLine(value)
		if err == nil {
			return commandIntent{kind: commandNewLine, number: number}, nil
		}
		return commandIntent{}, fmt.Errorf("unknown command %q", name)
	}
}

func parseResolveDraftCommand(arguments string) (commandIntent, error) {
	switch arguments {
	case draftConflictLocalValue, draftConflictRemoteValue, draftConflictBaseValue:
		return commandIntent{kind: commandResolveDraft, text: arguments}, nil
	default:
		return commandIntent{}, errors.New("resolve-draft requires local, remote, or base")
	}
}

func parsePositiveLine(value string) (uint32, error) {
	number, err := strconv.ParseUint(value, 10, 32)
	if err != nil || number == 0 {
		return 0, errors.New("line must be a positive integer")
	}
	return uint32(number), nil
}

func (model *Model) executeCommand(value string) tea.Cmd {
	intent, err := parseCommand(value)
	if err != nil {
		model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
		return nil
	}
	switch intent.kind {
	case commandQuit:
		if intent.force {
			return model.forceQuit()
		}
		return model.requestQuit()
	case commandReload:
		model.requestReload()
	case commandToggleDiff:
		model.applyAction(keymap.ActionToggleView, 1)
	case commandCopy:
		model.openExportPicker(exportSelection{
			format:      change.ExportMarkdown,
			destination: destinationClipboard,
			options:     change.DefaultExportOptions(),
		})
	case commandExport:
		model.openExportPicker(exportSelection{
			format:      change.ExportMarkdown,
			destination: destinationStdout,
			options:     change.DefaultExportOptions(),
		})
	case commandJSON:
		model.openExportPicker(exportSelection{
			format:      change.ExportJSON,
			destination: destinationStdout,
			options:     change.DefaultExportOptions(),
		})
	case commandSubmit:
		model.applyAction(keymap.ActionOpenSubmit, 1)
	case commandResolveDraft:
		model.resolvePendingConflict(intent.text)
	case commandAcknowledgeSubmit:
		model.acknowledgePublication()
	case commandHelp:
		model.overlay = OverlayHelp
	case commandFilter:
		return model.startFileFilter(intent.text)
	case commandSearchAll:
		command := model.openFind(findReview)
		model.searchInput.SetValue(intent.text)
		model.submitFindText()
		return command
	case commandSearchCancel:
		model.cancelSearch()
	case commandContext:
		context, err := patchContextForCommand(intent)
		if err != nil {
			model.notice = &Notice{Kind: NoticeWarning, Text: NoticeText(err.Error())}
			return nil
		}
		model.setPatchContext(context)
	case commandOldLine:
		model.selectSourceLine(change.LineOld, intent.number)
	case commandPath:
		model.activatePath(intent.text)
	case commandWrap:
		if intent.toggle {
			model.settings.wrap = !model.settings.wrap
		} else {
			model.settings.wrap = intent.enabled
		}
	case commandNewLine:
		model.selectSourceLine(change.LineNew, intent.number)
	}
	return nil
}

func (model *Model) resolvePendingConflict(value string) {
	comment, ok := model.review.CommentAt(model.commentCursor)
	if !ok || model.review.PendingBindingState(comment.ID()) != change.PendingBindingConflict {
		model.notice = &Notice{Kind: NoticeWarning, Text: "select a conflicting GitHub draft"}
		return
	}
	resolution := change.PendingConflictLocal
	switch value {
	case draftConflictRemoteValue:
		resolution = change.PendingConflictRemote
	case draftConflictBaseValue:
		resolution = change.PendingConflictBase
	}
	model.startCommentMutation(
		func(operation change.OperationID) (change.Review, change.CommentMutation, error) {
			return model.review.StartResolvePendingConflict(operation, comment.ID(), resolution)
		},
	)
}

func patchContextForCommand(intent commandIntent) (change.PatchContext, error) {
	if intent.whole {
		return change.WholeFileContext(), nil
	}
	if intent.number > 10_000 {
		return change.PatchContext{}, fmt.Errorf(
			"context lines %d exceed 10000",
			intent.number,
		)
	}
	contextLines, err := change.NewContextLines(uint16(intent.number))
	if err != nil {
		return change.PatchContext{}, err
	}
	return change.ContextAround(contextLines)
}

func (model *Model) setPatchContext(context change.PatchContext) {
	if operationID, active := model.foreground.requestPatchContext(context); active {
		model.coordinator.Cancel(operationID)
		model.notice = &Notice{
			Kind: NoticeInformation,
			Text: "waiting for active search cancellation before changing context",
		}
		return
	}
	model.applyPatchContext(context)
}

func (model *Model) applyPatchContext(context change.PatchContext) {
	model.foreground.clearPendingDiffTarget()
	model.fullFileOverrides.clear()
	model.settings.patchContext = context
	model.requestActivePatch()
	if context.WholeFile() {
		model.notice = &Notice{Kind: NoticeInformation, Text: "loading whole-file patch context"}
		return
	}
	lines, _ := context.Lines()
	model.notice = &Notice{
		Kind: NoticeInformation,
		Text: NoticeText(fmt.Sprintf("loading patch with %d context lines", lines.Uint16())),
	}
}
