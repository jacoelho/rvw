package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/config"
	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/output"
	"github.com/jacoelho/rvw/internal/source"
	"github.com/jacoelho/rvw/internal/tui"
)

const (
	readinessBarrierTimeout = time.Second
	coordinatorJoinTimeout  = 10 * time.Second
)

type interactiveRequest struct {
	ctx         context.Context
	input       io.Reader
	output      io.Writer
	service     *source.Service
	opened      *source.Opened
	revision    *source.Revision
	termination *terminationOwner
	resolved    *config.Config
	environment []string
}

func runInteractive(request interactiveRequest) (returnErr error) {
	if request.input == nil || request.output == nil {
		return errors.New("interactive mode requires input and output")
	}
	ownsRevision := false
	defer func() {
		if !ownsRevision && request.revision != nil {
			returnErr = errors.Join(returnErr, request.revision.Retire().Close())
		}
	}()
	if request.opened == nil || request.resolved == nil {
		return errors.New("interactive mode requires opened source and configuration")
	}
	settings, err := interactiveSettings(*request.resolved)
	if err != nil {
		return err
	}

	workContext, cancelWork := context.WithCancel(request.ctx)
	defer cancelWork()
	programContext, cancelProgram := context.WithCancel(request.ctx)
	defer cancelProgram()
	receiverContext, cancelReceiver := context.WithCancel(context.WithoutCancel(request.ctx))
	defer cancelReceiver()

	deliveryManager := delivery.NewManager()
	defer func() {
		closeContext, cancel := context.WithTimeout(
			context.WithoutCancel(request.ctx),
			coordinatorJoinTimeout,
		)
		defer cancel()
		returnErr = errors.Join(returnErr, deliveryManager.Close(closeContext))
	}()
	coordinator, err := tui.NewCoordinator(workContext, request.service, deliveryManager)
	if err != nil {
		return err
	}
	unregisterEscalator := func() {}
	if request.termination != nil {
		unregisterEscalator = request.termination.RegisterEscalator(coordinator.Escalate)
	}
	defer unregisterEscalator()
	receiver, err := tui.NewReadyReceiver(receiverContext)
	if err != nil {
		shutdownContext, cancel := context.WithTimeout(
			context.WithoutCancel(request.ctx),
			coordinatorJoinTimeout,
		)
		defer cancel()
		return errors.Join(err, coordinator.Shutdown(shutdownContext))
	}
	model, err := tui.NewModel(
		workContext,
		*request.opened,
		request.revision,
		settings,
		coordinator,
		receiver,
	)
	if err != nil {
		shutdownContext, cancel := context.WithTimeout(
			context.WithoutCancel(request.ctx),
			coordinatorJoinTimeout,
		)
		defer cancel()
		return errors.Join(err, coordinator.Shutdown(shutdownContext))
	}
	ownsRevision = true

	program := tea.NewProgram(
		model,
		tea.WithContext(programContext),
		tea.WithoutSignalHandler(),
		tea.WithInput(request.input),
		tea.WithOutput(request.output),
		tea.WithEnvironment(interactiveProgramEnvironment(
			request.environment,
			request.resolved.UI().ExplicitTheme,
		)),
	)
	_, runErr := program.Run()

	if runErr != nil && !errors.Is(runErr, tea.ErrProgramKilled) {
		runErr = errors.Join(runErr, program.ReleaseTerminal())
	}
	return finishInteractive(request, model, coordinator, receiver, cancelReceiver, runErr)
}

func interactiveProgramEnvironment(environment []string, explicitTheme bool) []string {
	owned := slices.Clone(environment)
	if !explicitTheme {
		return owned
	}
	return slices.DeleteFunc(owned, func(value string) bool {
		name, _, _ := strings.Cut(value, "=")
		return name == "NO_COLOR"
	})
}

func finishInteractive(
	request interactiveRequest,
	model *tui.Model,
	coordinator *tui.Coordinator,
	receiver *tui.ReadyReceiver,
	cancelReceiver context.CancelFunc,
	runErr error,
) error {
	cancelReceiver()
	barrierContext, cancelBarrier := context.WithTimeout(
		context.WithoutCancel(request.ctx),
		readinessBarrierTimeout,
	)
	barrierErr := receiver.StopAndWait(barrierContext)
	cancelBarrier()

	shutdownContext, cancelShutdown := context.WithTimeout(
		context.WithoutCancel(request.ctx),
		coordinatorJoinTimeout,
	)
	settlement, shutdownErr := coordinator.ShutdownWithSettlement(shutdownContext)
	cancelShutdown()
	result := model.Result(settlement)

	var documentErr error
	if shutdownErr == nil {
		documentErr = model.CloseSourceDocument()
	}
	var stdoutErr error
	if result.HasStdout() {
		stdoutErr = output.WritePreparedExport(
			request.output,
			result.Stdout,
			output.Options{Terminal: true},
		)
	}
	if request.termination != nil &&
		request.termination.Received() &&
		errors.Is(runErr, tea.ErrProgramKilled) {
		runErr = nil
	}
	return errors.Join(
		runErr,
		barrierErr,
		shutdownErr,
		result.Err,
		documentErr,
		stdoutErr,
	)
}

func interactiveSettings(resolved config.Config) (tui.Settings, error) {
	diff := resolved.Diff()
	if diff.ContextLines < 0 || diff.ContextLines > math.MaxUint16 {
		return tui.Settings{}, errors.New("diff context lines cannot be represented as uint16")
	}
	contextLines, err := change.NewContextLines(uint16(diff.ContextLines))
	if err != nil {
		return tui.Settings{}, err
	}
	diffView := tui.DiffViewUnified
	if diff.View == config.DiffSideBySide {
		diffView = tui.DiffViewSideBySide
	}
	activeTheme := resolved.ActiveTheme()
	syntaxSelectorValue := activeTheme.String()
	if theme, ok := resolved.Theme(activeTheme); ok && theme.SyntaxTheme != "" {
		syntaxSelectorValue = theme.SyntaxTheme.String()
	}
	syntaxSelector, err := change.NewSyntaxSelector(syntaxSelectorValue)
	if err != nil {
		return tui.Settings{}, err
	}

	ui := resolved.UI()
	var settingFeatureValues []tui.SettingFeature
	if ui.Mouse {
		settingFeatureValues = append(settingFeatureValues, tui.FeatureMouse)
	}
	if ui.ColorEnabled {
		settingFeatureValues = append(settingFeatureValues, tui.FeatureColor)
	}
	if diff.Wrap {
		settingFeatureValues = append(settingFeatureValues, tui.FeatureWrap)
	}
	settingFeatures, err := tui.NewSettingFeatures(settingFeatureValues...)
	if err != nil {
		return tui.Settings{}, err
	}
	exportConfig := resolved.Export()
	exportTemplates, err := change.CompileExportTemplates(
		exportConfig.MarkdownTemplate.String(),
		exportConfig.CommentTemplate.String(),
	)
	if err != nil {
		return tui.Settings{}, err
	}
	githubConfig := resolved.GitHub()
	publicationEvent := change.PublicationEventComment
	switch githubConfig.SubmitDefault {
	case config.SubmitComment:
	case config.SubmitApprove:
		publicationEvent = change.PublicationEventApprove
	case config.SubmitRequestChanges:
		publicationEvent = change.PublicationEventRequestChanges
	}
	keybindings, err := interactiveKeybindings(ui.Keybindings)
	if err != nil {
		return tui.Settings{}, err
	}
	palette, err := interactivePalette(resolved, activeTheme)
	if err != nil {
		return tui.Settings{}, err
	}
	return tui.NewSettings(tui.SettingsRequest{
		Features:         settingFeatures,
		DiffView:         diffView,
		ContextLines:     contextLines,
		SyntaxSelector:   syntaxSelector,
		ExportTemplates:  exportTemplates,
		PublicationEvent: publicationEvent,
		Keybindings:      keybindings,
		Palette:          palette,
	})
}

func interactivePalette(resolved config.Config, activeTheme config.ThemeName) (tui.Palette, error) {
	theme, ok := resolved.Theme(activeTheme)
	if !ok {
		return tui.NewPalette(nil)
	}
	return tui.NewPalette(interactivePaletteValues(theme))
}

func interactivePaletteValues(theme config.Theme) map[tui.PaletteRole]string {
	paletteValues := make(map[tui.PaletteRole]string)
	roles := map[config.ThemeColor]tui.PaletteRole{
		"panel_bg":           tui.PalettePanelBackground,
		"fg_primary":         tui.PalettePrimary,
		"fg_secondary":       tui.PaletteSecondary,
		"fg_dim":             tui.PaletteDim,
		"diff_add":           tui.PaletteDiffAdd,
		"diff_del":           tui.PaletteDiffDelete,
		"diff_context":       tui.PaletteDiffContext,
		"pending":            tui.PalettePending,
		"border_focused":     tui.PaletteFocused,
		"status_bar_bg":      tui.PaletteStatusBackground,
		"cursor_line_bg":     tui.PaletteCursorBackground,
		"cursor_line_fg":     tui.PaletteCursorForeground,
		"diff_add_bg":        tui.PaletteDiffAddBackground,
		"diff_del_bg":        tui.PaletteDiffDeleteBackground,
		"diff_hunk_header":   tui.PaletteDiffHunk,
		"message_warning_fg": tui.PaletteWarningForeground,
		"message_warning_bg": tui.PaletteWarningBackground,
		"message_error_fg":   tui.PaletteErrorForeground,
		"message_error_bg":   tui.PaletteErrorBackground,
		"message_success_fg": tui.PaletteSuccessForeground,
		"message_success_bg": tui.PaletteSuccessBackground,
	}
	for name, role := range roles {
		if color := theme.Colors[name].String(); color != "" {
			paletteValues[role] = color
		}
	}
	syntaxRoles := map[config.SyntaxColor]tui.PaletteRole{
		config.SyntaxKeyword: tui.PaletteSyntaxKeyword,
		config.SyntaxString:  tui.PaletteSyntaxString,
		config.SyntaxComment: tui.PaletteSyntaxComment,
		config.SyntaxNumber:  tui.PaletteSyntaxNumber,
	}
	for name, role := range syntaxRoles {
		if color := theme.SyntaxColors[name].String(); color != "" {
			paletteValues[role] = color
		}
	}
	return paletteValues
}

func interactiveKeybindings(bindings []config.Keybinding) ([]keymap.Binding, error) {
	keybindings := make([]keymap.Binding, 0, len(bindings))
	for index, binding := range bindings {
		if err := keymap.ValidateBinding(binding); err != nil {
			return nil, fmt.Errorf("keybinding %d: %w", index+1, err)
		}
		keybindings = append(keybindings, binding)
	}
	return keybindings, nil
}
