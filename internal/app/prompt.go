package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/review"
)

const maxPromptBytes = 64 << 10

type commandKind uint8

const (
	commandQuit commandKind = iota + 1
	commandReload
	commandToggleDiff
	commandCopy
	commandExport
	commandJSON
	commandSubmit
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
	kind    commandKind
	text    string
	number  uint32
	enabled bool
	toggle  bool
	whole   bool
	force   bool
}

func (model *Model) beginPrompt(mode InteractionMode) tea.Cmd {
	model.abandonChord()
	switch mode {
	case ModeSearch:
		model.mode = mode
		model.searchInput.SetValue("")
		return model.searchInput.Focus()
	case ModeCommand:
		model.mode = mode
		model.commandInput.SetValue("")
		return model.commandInput.Focus()
	default:
		return nil
	}
}

func (model *Model) handlePromptKey(message tea.KeyPressMsg) tea.Cmd {
	stroke := normalizeStroke(message.String())
	if action, consumed, command := model.resolveKeyStroke(stroke); consumed {
		if action == KeyActionNone {
			return command
		}
		switch action {
		case KeyActionCancel, KeyActionInterrupt:
			model.cancelPrompt()
			return nil
		case KeyActionAccept:
			if model.mode == ModeSearch {
				query := model.searchInput.Value()
				model.cancelPrompt()
				model.startSearch(query, review.SearchCurrentFile)
				return nil
			}
			command := model.commandInput.Value()
			model.cancelPrompt()
			return model.executeCommand(command)
		case KeyActionDeleteBackward:
			message = tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
		}
	}

	input := &model.commandInput
	if model.mode == ModeSearch {
		input = &model.searchInput
	}
	previous := input.Value()
	updated, command := input.Update(message)
	*input = updated
	if len(input.Value()) > maxPromptBytes {
		input.SetValue(previous)
		model.notice = &Notice{
			Kind: NoticeWarning,
			Text: "prompt input limit reached: 65536 bytes",
		}
		return nil
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
	model.searchInput.SetWidth(width)
	model.commandInput.SetWidth(width)
}

func (model *Model) promptView(width, height int) string {
	contextName := KeyContextCommand
	title := "Command"
	input := model.commandInput.View()
	if model.mode == ModeSearch {
		contextName = KeyContextSearch
		title = "Search changed patches"
		input = model.searchInput.View()
	}
	return constrainedLines(
		[]string{title, model.bindingSummary(contextName), input},
		width,
		height,
	)
}

func parseCommand(value string) (commandIntent, error) {
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
	case "reload":
		return noArguments(commandReload)
	case "diff":
		return noArguments(commandToggleDiff)
	case "copy":
		return noArguments(commandCopy)
	case "export":
		return noArguments(commandExport)
	case "json":
		return noArguments(commandJSON)
	case "submit":
		return noArguments(commandSubmit)
	case "ack-submit":
		return noArguments(commandAcknowledgeSubmit)
	case "help":
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
			return commandIntent{}, errors.New("set accepts wrap, nowrap, or wrap!")
		}
	default:
		number, err := parsePositiveLine(value)
		if err == nil {
			return commandIntent{kind: commandNewLine, number: number}, nil
		}
		return commandIntent{}, fmt.Errorf("unknown command %q", name)
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
		model.applyAction(KeyActionToggleView, 1)
	case commandCopy:
		model.openExportPicker(exportSelection{
			format:      review.ExportMarkdown,
			destination: delivery.DestinationClipboard,
		})
	case commandExport:
		model.openExportPicker(exportSelection{
			format:      review.ExportMarkdown,
			destination: delivery.DestinationStdout,
		})
	case commandJSON:
		model.openExportPicker(exportSelection{
			format:      review.ExportJSON,
			destination: delivery.DestinationStdout,
		})
	case commandSubmit:
		model.applyAction(KeyActionOpenSubmit, 1)
	case commandAcknowledgeSubmit:
		model.acknowledgePublication()
	case commandHelp:
		model.overlay = OverlayHelp
	case commandFilter:
		return model.startFileFilter(intent.text)
	case commandSearchAll:
		model.startSearch(intent.text, review.SearchAllFiles)
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
		model.selectSourceLine(review.LineOld, intent.number)
	case commandPath:
		model.activatePath(intent.text)
	case commandWrap:
		if intent.toggle {
			model.settings.wrap = !model.settings.wrap
		} else {
			model.settings.wrap = intent.enabled
		}
	case commandNewLine:
		model.selectSourceLine(review.LineNew, intent.number)
	}
	return nil
}

func patchContextForCommand(intent commandIntent) (review.PatchContext, error) {
	if intent.whole {
		return review.WholeFileContext(), nil
	}
	if intent.number > 10_000 {
		return review.PatchContext{}, fmt.Errorf(
			"context lines %d exceed 10000",
			intent.number,
		)
	}
	contextLines, err := review.NewContextLines(uint16(intent.number))
	if err != nil {
		return review.PatchContext{}, err
	}
	return review.ContextAround(contextLines)
}

func (model *Model) setPatchContext(context review.PatchContext) {
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

func (model *Model) applyPatchContext(context review.PatchContext) {
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
