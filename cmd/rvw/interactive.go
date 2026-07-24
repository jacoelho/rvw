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

	"github.com/jacoelho/rvw/internal/app"
	"github.com/jacoelho/rvw/internal/config"
	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/output"
	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
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
	document    *source.Document
	termination *terminationOwner
	environment []string
	resolved    config.Config
}

func runInteractive(request interactiveRequest) (returnErr error) {
	if request.input == nil || request.output == nil {
		return errors.New("interactive mode requires input and output")
	}
	ownsDocument := false
	defer func() {
		if !ownsDocument && request.document != nil {
			returnErr = errors.Join(returnErr, request.document.Close())
		}
	}()
	settings, err := interactiveSettings(request.resolved)
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
	coordinator, err := app.NewCoordinator(workContext, request.service, deliveryManager)
	if err != nil {
		return err
	}
	unregisterEscalator := func() {}
	if request.termination != nil {
		unregisterEscalator = request.termination.RegisterEscalator(coordinator.Escalate)
	}
	defer unregisterEscalator()
	receiver, err := app.NewReadyReceiver(receiverContext)
	if err != nil {
		shutdownContext, cancel := context.WithTimeout(
			context.WithoutCancel(request.ctx),
			coordinatorJoinTimeout,
		)
		defer cancel()
		return errors.Join(err, coordinator.Shutdown(shutdownContext))
	}
	model, err := app.NewModel(
		workContext,
		request.document,
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
	ownsDocument = true

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
	model *app.Model,
	coordinator *app.Coordinator,
	receiver *app.ReadyReceiver,
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
	modelSettlement := model.PublicationShutdownSettlement()
	settlement, shutdownErr := coordinator.ShutdownWithSettlement(shutdownContext)
	cancelShutdown()

	var documentErr error
	if shutdownErr == nil {
		documentErr = model.CloseSourceDocument()
	}
	var stdoutErr error
	if pending, ok := model.PendingStdout(); ok {
		stdoutErr = output.WritePreparedExport(
			request.output,
			pending,
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
		publicationShutdownError(
			mergePublicationSettlements(modelSettlement, settlement),
		),
		documentErr,
		stdoutErr,
	)
}

func mergePublicationSettlements(
	settlements ...app.ShutdownSettlement,
) app.ShutdownSettlement {
	merged := app.ShutdownSettlement{}
	indexes := make(map[string]int)
	for _, settlement := range settlements {
		for _, fact := range settlement.Publications {
			key := fmt.Sprintf(
				"%s#%d/%s",
				fact.Target.Repository(),
				fact.Target.PullRequest(),
				fact.Marker,
			)
			if index, exists := indexes[key]; exists {
				if publicationFactRank(fact) >
					publicationFactRank(merged.Publications[index]) {
					merged.Publications[index] = fact
				}
				continue
			}
			indexes[key] = len(merged.Publications)
			merged.Publications = append(merged.Publications, fact)
		}
	}
	return merged
}

func publicationFactRank(fact app.PublicationShutdownFact) int {
	if fact.Disposition == review.PublicationDispositionCommitted ||
		fact.Disposition == review.PublicationDispositionFailedBeforeCommit ||
		fact.Disposition == review.PublicationDispositionCancelledBeforeCommit ||
		fact.Reconciliation == review.ReconciliationCommitted ||
		fact.Reconciliation == review.ReconciliationNotCommitted {
		return 2
	}
	return 1
}

func publicationShutdownError(settlement app.ShutdownSettlement) error {
	var facts []error
	for _, fact := range settlement.Publications {
		target := fmt.Sprintf(
			"%s#%d",
			fact.Target.Repository(),
			fact.Target.PullRequest(),
		)
		switch {
		case fact.Disposition == review.PublicationDispositionUnknown ||
			fact.Reconciliation == review.ReconciliationStillUnknown:
			facts = append(facts, fmt.Errorf(
				"GitHub publication outcome is unknown for %s marker %s; retry risks a duplicate review",
				target,
				fact.Marker,
			))
		case fact.Disposition == review.PublicationDispositionCommitted ||
			fact.Reconciliation == review.ReconciliationCommitted:
			facts = append(facts, fmt.Errorf(
				"GitHub publication committed during shutdown for %s marker %s",
				target,
				fact.Marker,
			))
		}
	}
	return errors.Join(facts...)
}

func interactiveSettings(resolved config.Config) (app.Settings, error) {
	diff := resolved.Diff()
	if diff.ContextLines < 0 || diff.ContextLines > math.MaxUint16 {
		return app.Settings{}, errors.New("diff context lines cannot be represented as uint16")
	}
	contextLines, err := review.NewContextLines(uint16(diff.ContextLines))
	if err != nil {
		return app.Settings{}, err
	}
	diffView := app.DiffViewUnified
	if diff.View == config.DiffSideBySide {
		diffView = app.DiffViewSideBySide
	}
	activeTheme := resolved.ActiveTheme()
	syntaxSelectorValue := activeTheme.String()
	if theme, ok := resolved.Theme(activeTheme); ok && theme.SyntaxTheme != "" {
		syntaxSelectorValue = theme.SyntaxTheme.String()
	}
	syntaxSelector, err := review.NewSyntaxSelector(syntaxSelectorValue)
	if err != nil {
		return app.Settings{}, err
	}

	ui := resolved.UI()
	var settingFeatureValues []app.SettingFeature
	if ui.Mouse {
		settingFeatureValues = append(settingFeatureValues, app.FeatureMouse)
	}
	if ui.ColorEnabled {
		settingFeatureValues = append(settingFeatureValues, app.FeatureColor)
	}
	if diff.Wrap {
		settingFeatureValues = append(settingFeatureValues, app.FeatureWrap)
	}
	settingFeatures, err := app.NewSettingFeatures(settingFeatureValues...)
	if err != nil {
		return app.Settings{}, err
	}
	exportConfig := resolved.Export()
	exportTemplates, err := review.CompileExportTemplates(
		exportConfig.MarkdownTemplate.String(),
		exportConfig.CommentTemplate.String(),
	)
	if err != nil {
		return app.Settings{}, err
	}
	githubConfig := resolved.GitHub()
	publicationEvent := review.PublicationEventComment
	switch githubConfig.SubmitDefault {
	case config.SubmitComment:
	case config.SubmitApprove:
		publicationEvent = review.PublicationEventApprove
	case config.SubmitRequestChanges:
		publicationEvent = review.PublicationEventRequestChanges
	}
	publicationPolicies, err := review.NewPublicationPolicies()
	if err != nil {
		return app.Settings{}, err
	}
	publicationOptions, err := review.NewPublicationOptions(
		publicationEvent,
		publicationPolicies,
	)
	if err != nil {
		return app.Settings{}, err
	}
	keybindings, err := interactiveKeybindings(ui.Keybindings)
	if err != nil {
		return app.Settings{}, err
	}
	palette, err := interactivePalette(resolved, activeTheme)
	if err != nil {
		return app.Settings{}, err
	}
	return app.NewSettings(app.SettingsRequest{
		Features:        settingFeatures,
		DiffView:        diffView,
		ContextLines:    contextLines,
		SyntaxSelector:  syntaxSelector,
		ExportTemplates: exportTemplates,
		Publication:     publicationOptions,
		Keybindings:     keybindings,
		Palette:         palette,
	})
}

func interactivePalette(resolved config.Config, activeTheme config.ThemeName) (app.Palette, error) {
	theme, ok := resolved.Theme(activeTheme)
	if !ok {
		return app.NewPalette(nil)
	}
	return app.NewPalette(interactivePaletteValues(theme))
}

func interactivePaletteValues(theme config.Theme) map[app.PaletteRole]string {
	paletteValues := make(map[app.PaletteRole]string)
	roles := map[config.ThemeColor]app.PaletteRole{
		"panel_bg":           app.PalettePanelBackground,
		"fg_primary":         app.PalettePrimary,
		"fg_secondary":       app.PaletteSecondary,
		"fg_dim":             app.PaletteDim,
		"diff_add":           app.PaletteDiffAdd,
		"diff_del":           app.PaletteDiffDelete,
		"diff_context":       app.PaletteDiffContext,
		"pending":            app.PalettePending,
		"border_focused":     app.PaletteFocused,
		"status_bar_bg":      app.PaletteStatusBackground,
		"cursor_line_bg":     app.PaletteCursorBackground,
		"diff_add_bg":        app.PaletteDiffAddBackground,
		"diff_del_bg":        app.PaletteDiffDeleteBackground,
		"diff_hunk_header":   app.PaletteDiffHunk,
		"message_warning_fg": app.PaletteWarningForeground,
		"message_warning_bg": app.PaletteWarningBackground,
		"message_error_fg":   app.PaletteErrorForeground,
		"message_error_bg":   app.PaletteErrorBackground,
		"message_success_fg": app.PaletteSuccessForeground,
		"message_success_bg": app.PaletteSuccessBackground,
	}
	for name, role := range roles {
		if color := theme.Colors[name].String(); color != "" {
			paletteValues[role] = color
		}
	}
	syntaxRoles := map[config.SyntaxColor]app.PaletteRole{
		config.SyntaxKeyword: app.PaletteSyntaxKeyword,
		config.SyntaxString:  app.PaletteSyntaxString,
		config.SyntaxComment: app.PaletteSyntaxComment,
		config.SyntaxNumber:  app.PaletteSyntaxNumber,
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
