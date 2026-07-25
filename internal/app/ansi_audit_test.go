package app

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/jacoelho/rvw/internal/config"
	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/review"
)

const ansiAuditMinimumTextContrast = 4.5

type ansiAuditSize struct {
	width  int
	height int
}

type ansiAuditTheme struct {
	name         string
	palette      Palette
	defaultFG    string
	defaultBG    string
	colorEnabled bool
}

type ansiAuditOccupancyClass uint8

const (
	ansiAuditOccupancyNone ansiAuditOccupancyClass = iota
	ansiAuditOccupancyGeneral
	ansiAuditOccupancyDialog
	ansiAuditOccupancyReferenceDialog
	ansiAuditOccupancyBlockingSurface
	ansiAuditOccupancyCompactSurface
)

type ansiAuditScenario struct {
	name           string
	focus          FocusOwner
	overlay        bool
	setup          func(*Model)
	visibleCue     string
	selectedCue    string
	controlContext keymap.Context
	primaryAction  keymap.Action
	requiresCancel bool
	occupancyClass ansiAuditOccupancyClass
}

type ansiAuditModelState struct {
	filteredFileTree      *fileTree
	notice                *Notice
	lastNotice            *Notice
	visibleFileTree       fileTree
	allFileTree           fileTree
	fileProjection        fileProjectionState
	review                review.Review
	settings              Settings
	coordinatorStats      CoordinatorStats
	size                  TerminalSize
	confirmation          confirmation
	commentPaneVisibility paneVisibility
	focus                 FocusOwner
	singlePaneOverride    FocusOwner
	overlay               Overlay
	mode                  InteractionMode
	fileTreeVisible       bool
	diffCursor            uint32
	nextOperation         uint64
	publicationOperation  review.OperationID
	commentSelection      commentRowIdentity
	commentSelectionKnown bool
	exportPicker          exportSelection
	find                  findPalette
	searchInputValue      string
	publicationDraftReady bool
	publicationIntent     publicationOperationIntent
	publicationEvent      review.PublicationEvent
	searchInputFocused    bool
}

func TestDeterministicANSIRenderAudit(t *testing.T) {
	model, coordinator := newANSIAuditModel(t)
	settleModelWork(t, model, coordinator)
	model.settings.keymap = ansiAuditKeymap(t)
	baseline := captureANSIAuditModelState(model)
	themes := ansiAuditThemes(t)
	auditProfileMatrixIsMaterial(t, themes)
	sizes := []ansiAuditSize{
		{width: 40, height: 12},
		{width: 60, height: 20},
		{width: 80, height: 24},
		{width: 100, height: 30},
		{width: 120, height: 40},
		{width: 160, height: 50},
	}
	profiles := []colorprofile.Profile{
		colorprofile.ANSI,
		colorprofile.ANSI256,
		colorprofile.TrueColor,
	}

	t.Run("state completeness", func(t *testing.T) {
		auditStateCompleteness(t, model, baseline, themes[0])
	})
	t.Run("profile size state matrix", func(t *testing.T) {
		for _, theme := range themes {
			for _, size := range sizes {
				for _, scenario := range ansiAuditScenarios(t) {
					name := fmt.Sprintf(
						"%s/%dx%d/%s",
						theme.name,
						size.width,
						size.height,
						scenario.name,
					)
					t.Run(name, func(t *testing.T) {
						baseline.restore(model)
						model.settings.palette = theme.palette
						model.settings.colorEnabled = theme.colorEnabled
						model.size = TerminalSize{
							Width: size.width, Height: size.height, Known: true,
						}
						scenario.setup(model)
						content := model.View().Content
						auditRawANSIFrame(t, content, size, scenario.overlay)
						plain := ansi.Strip(content)
						if scenario.visibleCue != "" &&
							!strings.Contains(plain, scenario.visibleCue) {
							t.Fatalf("frame omitted state cue %q:\n%s", scenario.visibleCue, plain)
						}
						if scenario.overlay {
							if count := strings.Count(plain, "▌ "); count != 1 {
								t.Fatalf("modal focus markers = %d, want exactly 1:\n%s", count, plain)
							}
							if maximum, bounded := ansiAuditBoundedDialogMaximum(scenario.name); bounded {
								layout, observed := ansiAuditObservedBoundedDialogLayout(
									plain, size, scenario.name,
								)
								if !observed {
									t.Fatalf("bounded dialog geometry was not observable:\n%s", plain)
								}
								if err := ansiAuditCenteredBoundedDialog(layout, size, maximum); err != nil {
									t.Fatal(err)
								}
							}
							controls := strings.ToLower(ansiAuditModalControlText(
								plain, size, scenario.name,
							))
							if scenario.requiresCancel && !ansiAuditControlHasBinding(
								controls,
								ansiAuditBindingText(t, model, scenario.controlContext, keymap.ActionCancel),
							) {
								t.Fatalf("modal omitted visible cancel action:\n%s", plain)
							}
							if scenario.primaryAction != "" {
								primary := ansiAuditBindingText(
									t, model, scenario.controlContext, scenario.primaryAction,
								)
								if !ansiAuditControlHasBinding(controls, primary) {
									t.Fatalf("modal omitted primary action %q:\n%s", primary, plain)
								}
							}
						}
						if !scenario.overlay {
							auditWorkspaceGeometry(t, model, plain, size, scenario.focus)
						}
						for _, profile := range profiles {
							t.Run(profile.String(), func(t *testing.T) {
								auditProfileFrame(
									t,
									content,
									size,
									profile,
									theme,
									scenario,
								)
							})
						}
					})
				}
			}
		}
	})
	t.Run("layout stability", func(t *testing.T) {
		auditLayoutStability(t, model, baseline, themes[0], sizes)
	})
	t.Run("publication primary action is live", func(t *testing.T) {
		baseline.restore(model)
		setupANSIAuditCheckingPublication(t, model)
		if summary := model.review.PublicationSummary(); summary.Status != review.PublicationChecking {
			t.Fatalf("publication scenario status = %s, want checking", summary.Status)
		}
		model.applyPublicationAction(keymap.ActionSubmitReview)
		if model.publicationIntent != publicationOperationIntentSubmit {
			t.Fatalf("publication primary action intent = %d, want submit", model.publicationIntent)
		}
	})
	baseline.restore(model)
}

func TestANSIAuditMetricsRejectMalformedFrames(t *testing.T) {
	t.Run("misaligned pane boundary", func(t *testing.T) {
		frame := "FILES│DIFF\nrow   │row\nrow  │ row\nReady"
		if err := ansiAuditAlignedSeparators(frame); err == nil {
			t.Fatal("misaligned rendered separator passed the audit")
		}
	})
	t.Run("collapsed action spacing", func(t *testing.T) {
		count, ratio, compliance := ansiAuditFooterMetrics(
			"Ready  Down next row c comment  Tab next pane",
		)
		if count >= 3 && ratio >= 2 && compliance >= 0.95 {
			t.Fatalf("collapsed footer passed: count=%d ratio=%.2f compliance=%.2f", count, ratio, compliance)
		}
	})
	t.Run("stable content displacement", func(t *testing.T) {
		before := []string{"FILES│DIFF", "alpha│one", "beta │two", readyStatusText}
		after := []string{"FILES│DIFF", "beta │two", "alpha│one", readyStatusText}
		if changed := ansiAuditChangedRows(before, after); len(changed) == 0 {
			t.Fatal("displaced stable content passed the audit")
		}
	})
	t.Run("non-diff semantic anchor displacement", func(t *testing.T) {
		before := "▌ FILES│  DIFF\n  row  │  diff\nReady"
		after := " ▌ FILES│ DIFF\n   row  │ diff\nReady"
		score, _, err := ansiAuditLayoutShiftScore(
			before, after, ansiAuditSize{width: 16, height: 3},
		)
		if err != nil {
			t.Fatal(err)
		}
		if score == 0 {
			t.Fatal("moved non-DIFF semantic anchors produced zero layout shift")
		}
	})
	t.Run("overlapping regions", func(t *testing.T) {
		regions := []ansiAuditRegion{
			{x: 0, y: 0, width: 10, height: 5},
			{x: 9, y: 0, width: 10, height: 5},
		}
		if err := ansiAuditNonOverlappingRegions(regions); err == nil {
			t.Fatal("overlapping regions passed the audit")
		}
	})
	t.Run("rendered pane overlap erases boundary", func(t *testing.T) {
		frame := "FILES│DIFF\nleft row overwrote right\nReady     "
		if _, err := ansiAuditObservedWorkspaceRegions(
			frame, ansiAuditSize{width: 10, height: 3},
		); err == nil {
			t.Fatal("rendered pane overlap passed the production boundary audit")
		}
	})
	t.Run("body text cannot impersonate modal controls", func(t *testing.T) {
		frame := "▌ Confirmation     \nbody                  \noperation cancelled   \ny confirm operation   "
		controls := ansiAuditModalControlText(
			frame, ansiAuditSize{width: 20, height: 4}, "confirm-modal",
		)
		for _, binding := range []string{"esc cancel", "y yes"} {
			if ansiAuditControlHasBinding(controls, binding) {
				t.Fatalf("body text was parsed as modal binding %q: %q", binding, controls)
			}
		}
		if !strings.Contains(controls, "cancelled") {
			t.Fatalf("negative fixture omitted bottom-row impersonator: %q", controls)
		}
	})
	t.Run("hidden active selection", func(t *testing.T) {
		rows := []string{"  00_active_long.go", "  01_component_file.go"}
		if err := ansiAuditExpectedSelection(rows, nil, "00_active_long.go"); err == nil {
			t.Fatal("hidden active selection passed the audit")
		}
	})
	t.Run("sparse fixed modal region", func(t *testing.T) {
		occupancy, ok := ansiAuditRegionOccupancy(
			ansiAuditRegion{width: 40, height: 10}, 40,
		)
		minimum, _, _ := ansiAuditOccupancyRange(ansiAuditOccupancyDialog)
		if !ok || occupancy >= minimum {
			t.Fatalf("sparse fixed modal passed: occupancy=%.3f available=%t", occupancy, ok)
		}
	})
	t.Run("off-center bounded dialog", func(t *testing.T) {
		frame := "▌ Confirmation                         \nbody                                    \nEsc cancel                               \ny yes  n no                              "
		size := ansiAuditSize{width: 60, height: 10}
		layout, ok := ansiAuditObservedBoundedDialogLayout(frame, size, "confirm-modal")
		if !ok {
			t.Fatal("off-center fixture was not observable")
		}
		maximum, _ := ansiAuditBoundedDialogMaximum("confirm-modal")
		if err := ansiAuditCenteredBoundedDialog(layout, size, maximum); err == nil {
			t.Fatal("off-center bounded dialog passed the audit")
		}
	})
}

func auditProfileMatrixIsMaterial(t *testing.T, themes []ansiAuditTheme) {
	t.Helper()
	for _, theme := range themes {
		if !theme.colorEnabled {
			continue
		}
		for _, role := range []PaletteRole{
			PalettePrimary, PaletteFocused, PaletteCursorBackground, PaletteDiffAdd,
		} {
			value := theme.palette.color(role, "")
			if value == "" {
				continue
			}
			trueColor := colorprofile.TrueColor.Convert(lipgloss.Color(value))
			ansi16 := colorprofile.ANSI.Convert(lipgloss.Color(value))
			if !ansiAuditSameColor(trueColor, ansi16) {
				return
			}
		}
	}
	t.Fatal("color profile matrix has no RGB color that changes under ANSI16 conversion")
}

func newANSIAuditModel(t *testing.T) (*Model, *Coordinator) {
	t.Helper()
	changed := make(map[string]string, 24)
	var active strings.Builder
	for index := range 120 {
		if index%2 == 0 {
			fmt.Fprintf(&active, "value_%03d = %s\n", index, strings.Repeat("x", 72))
		} else {
			fmt.Fprintf(&active, "x_%03d = %s\n", index, strings.Repeat("y", 30))
		}
	}
	changed["00_active_long.go"] = active.String()
	for index := 1; index < 24; index++ {
		changed[fmt.Sprintf("%02d_component_descriptive.go", index)] =
			fmt.Sprintf("component_%02d = changed\n", index)
	}
	model, coordinator := newTestModel(t, changed)
	appendTestReviewComments(t, model,
		"check ownership", "verify bounds", "keep identity stable", "handle cancellation",
		"review terminal width", "confirm failure state", "preserve selection", "check contrast",
		"avoid hidden work", "retain stale data", "make focus visible", "validate input",
		"keep footer stable", "show remote failure", "bound memory", "avoid overlap",
		"align columns", "keep commands visible", "show success", "show loading",
		"show empty state", "show filtered empty", "show stale failure", "finish review",
	)
	return model, coordinator
}

func ansiAuditKeymap(t *testing.T) *compiledKeybindings {
	t.Helper()
	compiled, err := compileKeybindings(keymap.DefaultBindings())
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func ansiAuditThemes(t *testing.T) []ansiAuditTheme {
	t.Helper()
	return []ansiAuditTheme{
		ansiAuditProductionTheme(t, "dark", config.ThemeTokyoNightStorm),
		ansiAuditProductionTheme(t, "light", config.ThemeTokyoNightDay),
		ansiAuditProductionTheme(t, "high-contrast", config.ThemeANSI16Dark),
		{name: "monochrome", colorEnabled: false, defaultFG: "15", defaultBG: "0"},
	}
}

func ansiAuditProductionTheme(
	t *testing.T,
	name string,
	themeName config.ThemeName,
) ansiAuditTheme {
	t.Helper()
	root := t.TempDir()
	configuration := "theme = " + fmt.Sprintf("%q", themeName.String()) + "\n"
	if err := os.WriteFile(filepath.Join(root, ".rvw.toml"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Load(config.LoadRequest{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	theme, ok := resolved.Theme(resolved.ActiveTheme())
	if !ok {
		t.Fatalf("production theme %q was not resolved", themeName)
	}
	values := ansiAuditPaletteValues(theme)
	palette, err := NewPalette(values)
	if err != nil {
		t.Fatal(err)
	}
	return ansiAuditTheme{
		name:         name,
		palette:      palette,
		defaultFG:    theme.Colors["fg_primary"].String(),
		defaultBG:    theme.Colors["panel_bg"].String(),
		colorEnabled: true,
	}
}

func ansiAuditPaletteValues(theme config.Theme) map[PaletteRole]string {
	values := make(map[PaletteRole]string)
	roles := map[config.ThemeColor]PaletteRole{
		"panel_bg": PalettePanelBackground, "fg_primary": PalettePrimary,
		"fg_secondary": PaletteSecondary, "fg_dim": PaletteDim,
		"diff_add": PaletteDiffAdd, "diff_del": PaletteDiffDelete,
		"diff_context": PaletteDiffContext, "pending": PalettePending,
		"border_focused": PaletteFocused, "status_bar_bg": PaletteStatusBackground,
		"cursor_line_bg": PaletteCursorBackground, "diff_add_bg": PaletteDiffAddBackground,
		"diff_del_bg": PaletteDiffDeleteBackground, "diff_hunk_header": PaletteDiffHunk,
		"message_warning_fg": PaletteWarningForeground,
		"message_warning_bg": PaletteWarningBackground,
		"message_error_fg":   PaletteErrorForeground,
		"message_error_bg":   PaletteErrorBackground,
		"message_success_fg": PaletteSuccessForeground,
		"message_success_bg": PaletteSuccessBackground,
	}
	for name, role := range roles {
		if value := theme.Colors[name].String(); value != "" {
			values[role] = value
		}
	}
	syntaxRoles := map[config.SyntaxColor]PaletteRole{
		config.SyntaxKeyword: PaletteSyntaxKeyword,
		config.SyntaxString:  PaletteSyntaxString,
		config.SyntaxComment: PaletteSyntaxComment,
		config.SyntaxNumber:  PaletteSyntaxNumber,
	}
	for name, role := range syntaxRoles {
		if value := theme.SyntaxColors[name].String(); value != "" {
			values[role] = value
		}
	}
	return values
}

func ansiAuditScenarios(t *testing.T) []ansiAuditScenario {
	t.Helper()
	return []ansiAuditScenario{
		{
			name: "populated", focus: FocusDiff, visibleCue: "DIFF", selectedCue: "diff --git",
			occupancyClass: ansiAuditOccupancyGeneral,
			setup:          func(model *Model) { model.focus = FocusDiff },
		},
		{
			name: "long", focus: FocusDiff, visibleCue: "Warning:", selectedCue: "diff --git",
			setup: func(model *Model) {
				model.focus = FocusDiff
				model.notice = &Notice{
					Kind: NoticeWarning,
					Text: "a deliberately long warning validates clipping without moving footer actions or pane boundaries",
				}
			},
		},
		{
			name: "loading", focus: FocusDiff, visibleCue: "Loading files",
			setup: func(model *Model) {
				model.focus = FocusDiff
				model.fileProjection.workspace = projectionLoading
			},
		},
		{
			name: "empty", focus: FocusFileTree, visibleCue: "No changed files",
			setup: func(model *Model) {
				model.focus = FocusFileTree
				model.singlePaneOverride = FocusFileTree
				model.visibleFileTree = fileTree{}
				model.allFileTree = fileTree{}
				model.fileProjection = fileProjectionState{
					workspace: projectionReady, filter: projectionReady,
				}
			},
		},
		{
			name: "error", focus: FocusFileTree, visibleCue: "Error:",
			setup: func(model *Model) {
				model.focus = FocusFileTree
				model.singlePaneOverride = FocusFileTree
				model.visibleFileTree = fileTree{}
				model.allFileTree = fileTree{}
				model.fileProjection = fileProjectionState{
					workspace:       projectionFailed,
					filter:          projectionReady,
					workspaceDetail: "workspace index unavailable",
				}
			},
		},
		{
			name: "status-modal", overlay: true, visibleCue: "Status details",
			controlContext: keymap.ContextStatusDetails,
			primaryAction:  keymap.ActionMoveDown, requiresCancel: true,
			occupancyClass: ansiAuditOccupancyBlockingSurface,
			setup:          func(model *Model) { model.overlay = OverlayStatus },
		},
		{
			name: "help-modal", overlay: true, visibleCue: "Keybindings",
			controlContext: keymap.ContextHelp,
			primaryAction:  keymap.ActionNextHelpPage, requiresCancel: true,
			occupancyClass: ansiAuditOccupancyReferenceDialog,
			setup:          func(model *Model) { model.overlay = OverlayHelp },
		},
		{
			name: "find-modal", overlay: true, visibleCue: "Find",
			controlContext: keymap.ContextSearch,
			primaryAction:  keymap.ActionNextSearchScope, requiresCancel: true,
			occupancyClass: ansiAuditOccupancyDialog,
			setup: func(model *Model) {
				model.overlay = OverlayFind
				results := make([]fileFindResult, len(model.fileOrder))
				for index, file := range model.fileOrder {
					results[index] = fileFindResult{file: file, order: index}
				}
				model.find = findPalette{
					scope: findFiles, phase: findReady, fileResults: results,
					totalMatches: len(results),
				}
				model.searchInput.SetValue("component")
				model.searchInput.Focus()
			},
		},
		{
			name: "export-modal", overlay: true, visibleCue: "Export selection",
			controlContext: keymap.ContextExportPicker,
			primaryAction:  keymap.ActionExportSelected, requiresCancel: true,
			occupancyClass: ansiAuditOccupancyDialog,
			setup: func(model *Model) {
				model.openExportPicker(exportSelection{
					format:      review.ExportMarkdown,
					destination: delivery.DestinationClipboard,
					options:     review.DefaultExportOptions(),
				})
			},
		},
		{
			name: "confirm-quit-modal", overlay: true, visibleCue: "Confirmation",
			controlContext: keymap.ContextConfirm,
			primaryAction:  keymap.ActionConfirmYes, requiresCancel: true,
			occupancyClass: ansiAuditOccupancyDialog,
			setup: func(model *Model) {
				model.overlay = OverlayConfirm
				model.confirmation = confirmation{kind: confirmationQuit}
			},
		},
		{
			name: "confirm-delete-modal", overlay: true, visibleCue: "Confirmation",
			controlContext: keymap.ContextConfirm,
			primaryAction:  keymap.ActionConfirmYes, requiresCancel: true,
			occupancyClass: ansiAuditOccupancyDialog,
			setup: func(model *Model) {
				comment, ok := model.review.CommentAt(0)
				if !ok {
					t.Fatal("ANSI audit confirmation has no comment")
				}
				model.overlay = OverlayConfirm
				model.confirmation = confirmation{
					kind: confirmationDeleteComment, comment: comment.ID(),
				}
			},
		},
		{
			name: "confirm-reload-modal", overlay: true, visibleCue: "Confirmation",
			controlContext: keymap.ContextConfirm,
			primaryAction:  keymap.ActionConfirmYes, requiresCancel: true,
			occupancyClass: ansiAuditOccupancyDialog,
			setup: func(model *Model) {
				model.overlay = OverlayConfirm
				model.confirmation = confirmation{kind: confirmationReload}
			},
		},
		{
			name: "publication-modal", overlay: true, visibleCue: "Publish GitHub review",
			controlContext: keymap.ContextSubmit,
			primaryAction:  keymap.ActionSubmitReview, requiresCancel: true,
			occupancyClass: ansiAuditOccupancyCompactSurface,
			setup: func(model *Model) {
				setupANSIAuditCheckingPublication(t, model)
			},
		},
		{
			name: "focus-tree", focus: FocusFileTree, visibleCue: "FILES",
			selectedCue: "M 00_active",
			setup: func(model *Model) {
				model.focus = FocusFileTree
				model.singlePaneOverride = FocusFileTree
				selectANSIAuditTreeCursor(t, model)
			},
		},
		{
			name: "focus-diff", focus: FocusDiff, visibleCue: "DIFF", selectedCue: "diff --git",
			setup: func(model *Model) { model.focus = FocusDiff },
		},
		{
			name: "focus-comments", focus: FocusComments, visibleCue: "COMMENTS",
			selectedCue: "check ownership",
			setup: func(model *Model) {
				model.focus = FocusComments
				model.commentPaneVisibility = paneVisibilityShown
				model.singlePaneOverride = FocusComments
			},
		},
	}
}

func setupANSIAuditCheckingPublication(t *testing.T, model *Model) {
	t.Helper()
	model.overlay = OverlaySubmit
	model.publicationDraftReady = true
	model.publicationEvent = review.PublicationEventComment
	_, _ = beginTestPublicationCheck(t, model)
}

func selectANSIAuditTreeCursor(t *testing.T, model *Model) {
	t.Helper()
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("ANSI audit model has no active file")
	}
	identity, ok := model.currentFileTree().identityForFile(active)
	if !ok {
		t.Fatal("ANSI audit active file is absent from the tree")
	}
	model.treeCursor = identity
}

func captureANSIAuditModelState(model *Model) ansiAuditModelState {
	return ansiAuditModelState{
		filteredFileTree:      model.filteredFileTree,
		notice:                model.notice,
		lastNotice:            model.lastNotice,
		visibleFileTree:       model.visibleFileTree,
		allFileTree:           model.allFileTree,
		fileProjection:        model.fileProjection,
		review:                model.review,
		settings:              model.settings,
		coordinatorStats:      model.coordinatorStats,
		size:                  model.size,
		confirmation:          model.confirmation,
		commentPaneVisibility: model.commentPaneVisibility,
		focus:                 model.focus,
		singlePaneOverride:    model.singlePaneOverride,
		overlay:               model.overlay,
		mode:                  model.mode,
		fileTreeVisible:       model.fileTreeVisible,
		diffCursor:            model.diffCursor,
		nextOperation:         model.nextOperation,
		publicationOperation:  model.publicationOperation,
		commentSelection:      model.commentSelection,
		commentSelectionKnown: model.commentSelectionKnown,
		exportPicker:          model.exportPicker,
		find:                  model.find,
		searchInputValue:      model.searchInput.Value(),
		publicationDraftReady: model.publicationDraftReady,
		publicationIntent:     model.publicationIntent,
		publicationEvent:      model.publicationEvent,
		searchInputFocused:    model.searchInput.Focused(),
	}
}

func (state ansiAuditModelState) restore(model *Model) {
	model.filteredFileTree = state.filteredFileTree
	model.notice = state.notice
	model.lastNotice = state.lastNotice
	model.visibleFileTree = state.visibleFileTree
	model.allFileTree = state.allFileTree
	model.fileProjection = state.fileProjection
	model.review = state.review
	model.settings = state.settings
	model.coordinatorStats = state.coordinatorStats
	model.size = state.size
	model.confirmation = state.confirmation
	model.commentPaneVisibility = state.commentPaneVisibility
	model.focus = state.focus
	model.singlePaneOverride = state.singlePaneOverride
	model.overlay = state.overlay
	model.mode = state.mode
	model.fileTreeVisible = state.fileTreeVisible
	model.diffCursor = state.diffCursor
	model.nextOperation = state.nextOperation
	model.publicationOperation = state.publicationOperation
	model.commentSelection = state.commentSelection
	model.commentSelectionKnown = state.commentSelectionKnown
	model.exportPicker = state.exportPicker
	model.find = state.find
	model.searchInput.SetValue(state.searchInputValue)
	if state.searchInputFocused {
		model.searchInput.Focus()
	} else {
		model.searchInput.Blur()
	}
	model.publicationDraftReady = state.publicationDraftReady
	model.publicationIntent = state.publicationIntent
	model.publicationEvent = state.publicationEvent
}

func auditRawANSIFrame(t *testing.T, content string, size ansiAuditSize, overlay bool) {
	t.Helper()
	if strings.ContainsRune(content, '\t') {
		t.Fatal("frame contains a tab instead of explicit spacing")
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) > size.height {
		t.Fatalf("frame rows = %d, exceeds terminal height %d", len(lines), size.height)
	}
	fullWidth := 0
	for row, line := range lines {
		width := lipgloss.Width(line)
		if width > size.width {
			t.Fatalf("row %d width = %d, exceeds terminal width %d", row, width, size.width)
		}
		if width == size.width {
			fullWidth++
		}
	}
	if !overlay && len(lines) != 0 && float64(fullWidth)/float64(len(lines)) < 0.95 {
		t.Fatalf(
			"aligned full-width rows = %d/%d (%.1f%%), want at least 95%%",
			fullWidth,
			len(lines),
			100*float64(fullWidth)/float64(len(lines)),
		)
	}
}

func ansiAuditModalControlText(plain string, size ansiAuditSize, scenario string) string {
	lines := strings.Split(plain, "\n")
	start := max(size.height-2, 0)
	end := size.height
	switch scenario {
	case "find-modal":
		layout := newFindModalLayout(size.width, size.height)
		start = max(layout.y+layout.height-2, 0)
		end = layout.y + layout.height
	default:
		if layout, ok := ansiAuditObservedBoundedDialogLayout(plain, size, scenario); ok {
			start = max(layout.y+layout.height-2, 0)
			end = layout.y + layout.height
		}
	}
	controls := make([]string, 0, 2)
	for row := start; row < end; row++ {
		controls = append(controls, ansiAuditLine(lines, row))
	}
	return strings.Join(controls, "\n")
}

func ansiAuditControlHasBinding(controls, expected string) bool {
	for line := range strings.SplitSeq(controls, "\n") {
		for _, span := range ansiAuditSeparatedSpans(line) {
			if strings.EqualFold(span.text, expected) {
				return true
			}
		}
	}
	return false
}

func ansiAuditBindingText(
	t *testing.T,
	model *Model,
	contextName keymap.Context,
	action keymap.Action,
) string {
	t.Helper()
	bindings := bindingsForActions(model.effectiveBindings(contextName), []keymap.Action{action})
	if len(bindings) != 1 {
		t.Fatalf("%s binding count for %s = %d, want 1", contextName, action, len(bindings))
	}
	return strings.ToLower(bindings[0].Chord.Display() + " " + action.Description())
}

type ansiAuditRegion struct {
	x, y          int
	width, height int
}

func ansiAuditNonOverlappingRegions(regions []ansiAuditRegion) error {
	for index, region := range regions {
		if region.width < 0 || region.height < 0 {
			return fmt.Errorf("region %d has negative geometry: %+v", index, region)
		}
		for otherIndex := index + 1; otherIndex < len(regions); otherIndex++ {
			other := regions[otherIndex]
			overlaps := region.x < other.x+other.width &&
				other.x < region.x+region.width &&
				region.y < other.y+other.height &&
				other.y < region.y+region.height
			if overlaps {
				return fmt.Errorf("regions %d and %d overlap", index, otherIndex)
			}
		}
	}
	return nil
}

func ansiAuditObservedWorkspaceRegions(
	plain string,
	size ansiAuditSize,
) ([]ansiAuditRegion, error) {
	lines := strings.Split(plain, "\n")
	if len(lines) < 2 {
		return nil, errors.New("workspace frame has no body")
	}
	separators := ansiAuditSeparatorColumns(lines[0])
	for row, line := range lines[1 : len(lines)-1] {
		if observed := ansiAuditSeparatorColumns(line); !slices.Equal(observed, separators) {
			return nil, fmt.Errorf(
				"workspace row %d pane boundaries = %v, want %v",
				row+1, observed, separators,
			)
		}
	}
	regions := make([]ansiAuditRegion, 0, len(separators)+1)
	start := 0
	for _, separator := range append(separators, size.width) {
		if separator < start || separator > size.width {
			return nil, fmt.Errorf("workspace pane boundary %d is outside %d columns", separator, size.width)
		}
		regions = append(regions, ansiAuditRegion{
			x: start, y: 0, width: separator - start, height: max(size.height-1, 0),
		})
		start = separator + 1
	}
	return regions, nil
}

func auditWorkspaceGeometry(
	t *testing.T,
	model *Model,
	plain string,
	size ansiAuditSize,
	focus FocusOwner,
) {
	t.Helper()
	layout := model.workspaceLayout(size.width, size.height)
	used := layout.treeWidth + layout.diffWidth + layout.commentsWidth +
		max(layout.paneCount()-1, 0)
	if used != size.width {
		t.Fatalf("workspace regions consume %d columns, want exactly %d", used, size.width)
	}
	if layout.treeWidth < 0 || layout.diffWidth < 0 || layout.commentsWidth < 0 {
		t.Fatalf("workspace has negative region width: %+v", layout)
	}
	regions, err := ansiAuditObservedWorkspaceRegions(plain, size)
	if err != nil {
		t.Fatal(err)
	}
	expectedWidths := make([]int, 0, layout.paneCount())
	for _, width := range []int{layout.treeWidth, layout.diffWidth, layout.commentsWidth} {
		if width != 0 {
			expectedWidths = append(expectedWidths, width)
		}
	}
	observedWidths := make([]int, len(regions))
	for index, region := range regions {
		observedWidths[index] = region.width
	}
	if !slices.Equal(observedWidths, expectedWidths) {
		t.Fatalf("rendered pane widths = %v, want %v", observedWidths, expectedWidths)
	}
	if err := ansiAuditNonOverlappingRegions(regions); err != nil {
		t.Fatal(err)
	}
	firstLine, _, _ := strings.Cut(plain, "\n")
	if count := strings.Count(firstLine, "▌ "); count != 1 {
		t.Fatalf("visible focus markers = %d, want exactly 1 in %q", count, firstLine)
	}
	wantTitle := map[FocusOwner]string{
		FocusFileTree: "FILES",
		FocusDiff:     "DIFF",
		FocusComments: "COMMENTS",
	}[focus]
	if !strings.Contains(firstLine, wantTitle) {
		t.Fatalf("focused pane title %q is not visible in %q", wantTitle, firstLine)
	}
	if err := ansiAuditAlignedSeparators(plain); err != nil {
		t.Fatal(err)
	}
	if model.overlay == OverlayNone && model.mode == ModeNormal {
		lines := strings.Split(plain, "\n")
		footer := lines[len(lines)-1]
		count, ratio, compliance := ansiAuditFooterMetrics(footer)
		if model.notice != nil {
			if count != 0 {
				t.Fatalf("active notice footer exposed %d actions", count)
			}
			return
		}
		minimum := 1
		if size.width >= compactWidth {
			minimum = 3
		}
		if count < minimum || count > 6 {
			t.Fatalf("visible footer actions = %d, want %d..6", count, minimum)
		}
		if count > 1 && ratio < 2 {
			t.Fatalf("rendered footer gap-size ratio = %.2f, want >= 2", ratio)
		}
		if count > 1 && compliance < 0.95 {
			t.Fatalf("rendered footer spacing compliance = %.3f, want >= 0.95", compliance)
		}
	}
}

type ansiAuditSpan struct {
	text       string
	start, end int
}

func ansiAuditFooterMetrics(footer string) (int, float64, float64) {
	actions := ansiAuditFooterActionSpans(footer)
	if len(actions) < 2 {
		return len(actions), 0, 1
	}
	minimumInter := math.MaxInt
	maximumIntra := 0
	for _, action := range actions {
		run := 0
		for _, character := range action.text {
			if character == ' ' {
				run++
				maximumIntra = max(maximumIntra, run)
				continue
			}
			run = 0
		}
	}
	compliant := 0
	for index := 1; index < len(actions); index++ {
		gap := actions[index].start - actions[index-1].end
		minimumInter = min(minimumInter, gap)
		if gap >= 2 {
			compliant++
		}
	}
	if maximumIntra == 0 {
		return len(actions), 0, 0
	}
	return len(actions), float64(minimumInter) / float64(maximumIntra),
		float64(compliant) / float64(len(actions)-1)
}

func ansiAuditFooterActionSpans(footer string) []ansiAuditSpan {
	var actions []ansiAuditSpan
	for _, span := range ansiAuditSeparatedSpans(footer) {
		text := strings.ToLower(span.text)
		for _, label := range []string{
			nextRowLabel, "previous row", "comment", "file comment", "next pane",
			"help", reloadLabel, "next", "prev", "activate", "status details",
		} {
			if strings.HasSuffix(text, " "+label) {
				actions = append(actions, span)
				break
			}
		}
	}
	return actions
}

func ansiAuditSeparatedSpans(line string) []ansiAuditSpan {
	var spans []ansiAuditSpan
	var current strings.Builder
	start := 0
	end := 0
	pendingSpaces := 0
	column := 0
	for _, character := range line {
		width := lipgloss.Width(string(character))
		if character == ' ' {
			pendingSpaces += width
		} else {
			switch {
			case current.Len() == 0:
				start = column
			case pendingSpaces >= 2:
				spans = append(spans, ansiAuditSpan{
					text: current.String(), start: start, end: end,
				})
				current.Reset()
				start = column
			case pendingSpaces != 0:
				current.WriteString(strings.Repeat(" ", pendingSpaces))
			}
			current.WriteRune(character)
			end = column + width
			pendingSpaces = 0
		}
		column += width
	}
	if current.Len() != 0 {
		spans = append(spans, ansiAuditSpan{
			text: current.String(), start: start, end: end,
		})
	}
	return spans
}

func ansiAuditAlignedSeparators(plain string) error {
	lines := strings.Split(plain, "\n")
	if len(lines) < 2 {
		return nil
	}
	want := ansiAuditSeparatorColumns(lines[0])
	if len(want) == 0 {
		return nil
	}
	aligned := 0
	total := max(len(lines)-1, 1)
	for _, line := range lines[1 : len(lines)-1] {
		got := ansiAuditSeparatorColumns(line)
		if slices.Equal(got, want) {
			aligned++
		}
	}
	if float64(aligned)/float64(total-1) < 0.95 {
		return fmt.Errorf("rendered pane-column alignment = %d/%d, want >=95%%", aligned, total-1)
	}
	return nil
}

func ansiAuditSeparatorColumns(line string) []int {
	var columns []int
	column := 0
	for _, character := range line {
		if character == '│' {
			columns = append(columns, column)
		}
		column += lipgloss.Width(string(character))
	}
	return columns
}

func auditProfileFrame(
	t *testing.T,
	content string,
	size ansiAuditSize,
	profile colorprofile.Profile,
	theme ansiAuditTheme,
	scenario ansiAuditScenario,
) {
	t.Helper()
	var converted bytes.Buffer
	writer := colorprofile.Writer{Forward: &converted, Profile: profile}
	stream := strings.ReplaceAll(content, "\n", "\r\n")
	if _, err := writer.WriteString(stream); err != nil {
		t.Fatal(err)
	}
	terminal := vt.NewEmulator(size.width, size.height)
	t.Cleanup(func() { _ = terminal.Close() })
	if _, err := terminal.Write(converted.Bytes()); err != nil {
		t.Fatal(err)
	}
	defaultForeground := profile.Convert(lipgloss.Color(theme.defaultFG))
	defaultBackground := profile.Convert(lipgloss.Color(theme.defaultBG))
	occupied := 0
	borders := 0
	selectedCells := make([][]bool, size.height)
	plainCells := make([][]string, size.height)
	modalRegion := ansiAuditModalRegion(scenario, size, ansi.Strip(content))
	regionOccupied := 0
	for row := range size.height {
		plainCells[row] = make([]string, size.width)
		selectedCells[row] = make([]bool, size.width)
		for column := range size.width {
			plainCells[row][column] = " "
		}
	}
	var selectedBackground color.Color
	if theme.colorEnabled {
		selectedBackground = profile.Convert(lipgloss.Color(
			theme.palette.color(PaletteCursorBackground, ""),
		))
	}
	for y := range size.height {
		for x := range size.width {
			cell := terminal.CellAt(x, y)
			if cell == nil || cell.Width == 0 {
				continue
			}
			plainCells[y][x] = cell.Content
			if strings.TrimSpace(cell.Content) == "" {
				continue
			}
			occupied++
			if ansiAuditRegionContains(modalRegion, x, y) {
				regionOccupied++
			}
			if strings.ContainsAny(cell.Content, "│─┌┐└┘╭╮╰╯▌") {
				borders++
			}
			const (
				attributeFaint   = uint8(1 << 1)
				attributeReverse = uint8(1 << 5)
			)
			if cell.Style.Attrs&attributeFaint != 0 {
				t.Fatalf("occupied cell %d,%d uses faint", x, y)
			}
			foreground := cell.Style.Fg
			background := cell.Style.Bg
			if foreground == nil {
				foreground = defaultForeground
			}
			if background == nil {
				background = defaultBackground
			}
			if cell.Style.Attrs&attributeReverse != 0 {
				foreground, background = background, foreground
				selectedCells[y][x] = true
			} else if theme.colorEnabled && ansiAuditSameColor(background, selectedBackground) {
				selectedCells[y][x] = true
			}
			if ratio := ansiAuditContrastRatio(foreground, background); ratio < ansiAuditMinimumTextContrast {
				t.Fatalf(
					"cell %d,%d %q contrast = %.2f:1 in %s, want >= %.1f:1",
					x,
					y,
					cell.Content,
					ratio,
					profile,
					ansiAuditMinimumTextContrast,
				)
			}
		}
	}
	area := size.width * size.height
	if area == 0 {
		t.Fatal("audit terminal has zero area")
	}
	if occupied != 0 {
		if density := float64(borders) / float64(occupied); density >= 0.15 {
			t.Fatalf("visual-noise ratio = %.3f, want < 0.15", density)
		}
	}
	plainRows := make([]string, size.height)
	for row := range size.height {
		plainRows[row] = strings.TrimRight(strings.Join(plainCells[row], ""), " ")
	}
	if scenario.selectedCue != "" {
		if err := ansiAuditExpectedSelection(plainRows, selectedCells, scenario.selectedCue); err != nil {
			t.Fatal(err)
		}
	}
	if scenario.occupancyClass != ansiAuditOccupancyNone {
		occupancy, ok := ansiAuditRegionOccupancy(modalRegion, regionOccupied)
		if !ok {
			t.Fatal("audited surface has no occupied content region")
		}
		minimum, maximum, className := ansiAuditOccupancyRange(scenario.occupancyClass)
		if occupancy < minimum || occupancy > maximum {
			t.Fatalf(
				"%s occupancy = %.3f, want %.3f..%.2f",
				className, occupancy, minimum, maximum,
			)
		}
	}
}

func ansiAuditModalRegion(
	scenario ansiAuditScenario,
	size ansiAuditSize,
	plain string,
) ansiAuditRegion {
	if scenario.name == "find-modal" {
		layout := newFindModalLayout(size.width, size.height)
		return ansiAuditRegion{
			x: layout.x, y: layout.y, width: layout.width, height: layout.height,
		}
	}
	if layout, ok := ansiAuditObservedBoundedDialogLayout(plain, size, scenario.name); ok {
		return ansiAuditRegion(layout)
	}
	return ansiAuditRegion{width: size.width, height: size.height}
}

func ansiAuditBoundedDialogMaximum(scenario string) (blockingDialogSize, bool) {
	switch {
	case strings.HasPrefix(scenario, "confirm-"):
		return blockingDialogSize{width: 40, height: 7}, true
	case scenario == "export-modal":
		return blockingDialogSize{width: 52, height: 9}, true
	case scenario == "help-modal":
		return blockingDialogSize{width: helpDialogWidth, height: helpDialogHeight}, true
	default:
		return blockingDialogSize{}, false
	}
}

func ansiAuditObservedBoundedDialogLayout(
	plain string,
	size ansiAuditSize,
	scenario string,
) (blockingDialogLayout, bool) {
	maximum, ok := ansiAuditBoundedDialogMaximum(scenario)
	if !ok {
		return blockingDialogLayout{}, false
	}
	lines := strings.Split(plain, "\n")
	if scenario == "help-modal" {
		layout := newHelpDialogLayout(size.width, size.height)
		line := ansiAuditLine(lines, layout.y)
		prefix, _, found := strings.Cut(line, "▌ Keybindings")
		if !found {
			return blockingDialogLayout{}, false
		}
		layout.x = lipgloss.Width(prefix)
		return layout, true
	}
	for row, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		last := row
		for candidate := row; candidate < len(lines); candidate++ {
			if strings.TrimSpace(lines[candidate]) != "" {
				last = candidate
			}
		}
		return blockingDialogLayout{
			x: lipgloss.Width(line) - lipgloss.Width(strings.TrimLeft(line, " ")), y: row,
			width: min(size.width, maximum.width), height: last - row + 1,
		}, true
	}
	return blockingDialogLayout{}, false
}

func ansiAuditCenteredBoundedDialog(
	layout blockingDialogLayout,
	size ansiAuditSize,
	maximum blockingDialogSize,
) error {
	expectedWidth := min(size.width, maximum.width)
	if layout.width != expectedWidth {
		return fmt.Errorf("bounded dialog width = %d, want %d", layout.width, expectedWidth)
	}
	if layout.height < 1 || layout.height > min(size.height, maximum.height) {
		return fmt.Errorf(
			"bounded dialog height = %d, want 1..%d",
			layout.height,
			min(size.height, maximum.height),
		)
	}
	expectedX := max((size.width-layout.width)/2, 0)
	expectedY := max((size.height-layout.height)/2, 0)
	if layout.x != expectedX || layout.y != expectedY {
		return fmt.Errorf(
			"bounded dialog origin = (%d,%d), want centered (%d,%d)",
			layout.x,
			layout.y,
			expectedX,
			expectedY,
		)
	}
	return nil
}

func ansiAuditRegionContains(region ansiAuditRegion, x, y int) bool {
	return x >= region.x && x < region.x+region.width &&
		y >= region.y && y < region.y+region.height
}

func ansiAuditExpectedSelection(rows []string, selectedCells [][]bool, cue string) error {
	for row, line := range rows {
		before, _, found := strings.Cut(line, cue)
		if found {
			start := lipgloss.Width(before)
			end := start + lipgloss.Width(cue)
			if row < len(selectedCells) {
				for column := start; column < end && column < len(selectedCells[row]); column++ {
					if selectedCells[row][column] {
						return nil
					}
				}
			}
			return fmt.Errorf("active selection %q is visible without a selection cue", cue)
		}
	}
	return fmt.Errorf("active selection %q is outside the viewport", cue)
}

func ansiAuditRegionOccupancy(region ansiAuditRegion, occupied int) (float64, bool) {
	area := region.width * region.height
	if occupied == 0 || area == 0 {
		return 0, false
	}
	return float64(occupied) / float64(area), true
}

func ansiAuditOccupancyRange(class ansiAuditOccupancyClass) (float64, float64, string) {
	switch class {
	case ansiAuditOccupancyGeneral:
		return 0.35, 0.65, "general application"
	case ansiAuditOccupancyDialog:
		return 0.25, 0.60, "modal dialog"
	case ansiAuditOccupancyReferenceDialog:
		return 0.15, 0.60, "reference dialog"
	case ansiAuditOccupancyBlockingSurface:
		return 0.02, 0.65, "full-screen operational surface"
	case ansiAuditOccupancyCompactSurface:
		return 0.01, 0.65, "compact full-screen operational surface"
	default:
		return 0, 1, "unclassified"
	}
}

func ansiAuditSameColor(first, second color.Color) bool {
	if first == nil || second == nil {
		return false
	}
	firstRed, firstGreen, firstBlue, firstAlpha := first.RGBA()
	secondRed, secondGreen, secondBlue, secondAlpha := second.RGBA()
	return firstRed == secondRed && firstGreen == secondGreen && firstBlue == secondBlue &&
		firstAlpha == secondAlpha
}

func ansiAuditContrastRatio(first, second color.Color) float64 {
	firstLuminance := ansiAuditRelativeLuminance(first)
	secondLuminance := ansiAuditRelativeLuminance(second)
	if firstLuminance < secondLuminance {
		firstLuminance, secondLuminance = secondLuminance, firstLuminance
	}
	return (firstLuminance + 0.05) / (secondLuminance + 0.05)
}

func ansiAuditRelativeLuminance(value color.Color) float64 {
	red, green, blue, _ := value.RGBA()
	channel := func(component uint32) float64 {
		normalized := float64(component) / 65535
		if normalized <= 0.04045 {
			return normalized / 12.92
		}
		return math.Pow((normalized+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(red) + 0.7152*channel(green) + 0.0722*channel(blue)
}

func auditStateCompleteness(
	t *testing.T,
	model *Model,
	baseline ansiAuditModelState,
	theme ansiAuditTheme,
) {
	t.Helper()
	type stateCase struct {
		name  string
		cues  []string
		setup func(*Model)
	}
	emptyTree := func(model *Model) {
		model.visibleFileTree = fileTree{}
		model.allFileTree = fileTree{}
		model.focus = FocusFileTree
		model.singlePaneOverride = FocusFileTree
	}
	states := []stateCase{
		{name: "initial", cues: []string{applicationName, "waiting for terminal size"}, setup: func(model *Model) {
			model.size = TerminalSize{}
		}},
		{name: "loading no data", cues: []string{"LOADING", "Loading changed files"}, setup: func(model *Model) {
			emptyTree(model)
			model.fileProjection = fileProjectionState{workspace: projectionLoading, filter: projectionReady}
		}},
		{name: "loading existing", cues: []string{"LOADING", "Loading files"}, setup: func(model *Model) {
			model.fileProjection.workspace = projectionLoading
		}},
		{name: "loaded", cues: []string{"FILES", "DIFF"}, setup: func(*Model) {}},
		{name: "empty", cues: []string{"FILES", "No changed files"}, setup: func(model *Model) {
			emptyTree(model)
			model.fileProjection = fileProjectionState{workspace: projectionReady, filter: projectionReady}
		}},
		{name: "filtered empty", cues: []string{"FILES", "No files match the filter"}, setup: func(model *Model) {
			empty := fileTree{}
			model.filteredFileTree = &empty
			model.focus = FocusFileTree
			model.singlePaneOverride = FocusFileTree
			model.fileProjection = fileProjectionState{
				workspace: projectionReady, filter: projectionReady,
				requestedQuery: "missing", appliedQuery: "missing",
			}
		}},
		{name: "error no data", cues: []string{"! ERROR", "Error: changed files unavailable"}, setup: func(model *Model) {
			emptyTree(model)
			model.fileProjection = fileProjectionState{
				workspace: projectionFailed, filter: projectionReady,
				workspaceDetail: "index unavailable",
			}
		}},
		{name: "error stale", cues: []string{"! ERROR", "Error: file index unavailable"}, setup: func(model *Model) {
			model.fileProjection = fileProjectionState{
				workspace: projectionFailed, filter: projectionReady,
				workspaceDetail: "refresh failed; prior results retained",
			}
		}},
		{name: "disabled", cues: []string{"Decision pending", "Disabled:"}, setup: func(model *Model) {
			model.size = TerminalSize{Width: 19, Height: 5, Known: true}
			model.overlay = OverlayConfirm
			model.confirmation = confirmation{kind: confirmationReload}
		}},
		{name: "focused", cues: []string{"▌ DIFF"}, setup: func(model *Model) {
			model.focus = FocusDiff
		}},
		{name: "selected", cues: []string{"▸", "00_active_long.go"}, setup: func(model *Model) {
			model.focus = FocusFileTree
			model.singlePaneOverride = FocusFileTree
			selectANSIAuditTreeCursor(t, model)
		}},
		{name: "pending mutation", cues: []string{commentChangePreparing}, setup: func(model *Model) {
			model.notice = &Notice{Kind: NoticeInformation, Text: commentChangePreparing}
			model.coordinatorStats.Accepted = 1
		}},
		{name: "success", cues: []string{"Success:", commentSaved}, setup: func(model *Model) {
			model.notice = &Notice{Kind: NoticeSuccess, Text: commentSaved}
		}},
	}
	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			baseline.restore(model)
			model.settings.palette = theme.palette
			model.settings.colorEnabled = theme.colorEnabled
			model.size = TerminalSize{Width: 160, Height: 50, Known: true}
			state.setup(model)
			plain := ansi.Strip(model.View().Content)
			for _, cue := range state.cues {
				if !strings.Contains(plain, cue) {
					t.Fatalf("state omitted cue %q:\n%s", cue, plain)
				}
			}
		})
	}
}

func auditLayoutStability(
	t *testing.T,
	model *Model,
	baseline ansiAuditModelState,
	theme ansiAuditTheme,
	sizes []ansiAuditSize,
) {
	t.Helper()
	for _, size := range sizes {
		baseline.restore(model)
		model.settings.palette = theme.palette
		model.settings.colorEnabled = theme.colorEnabled
		model.size = TerminalSize{Width: size.width, Height: size.height, Known: true}
		before := strings.Split(model.View().Content, "\n")
		model.notice = &Notice{Kind: NoticeSuccess, Text: "refresh complete"}
		afterStatus := strings.Split(model.View().Content, "\n")
		if len(before) != len(afterStatus) ||
			!slices.Equal(before[:max(len(before)-1, 0)], afterStatus[:max(len(afterStatus)-1, 0)]) {
			t.Fatalf("%dx%d status update moved stable non-footer cells", size.width, size.height)
		}
		if footer := ansi.Strip(ansiAuditLine(afterStatus, len(afterStatus)-1)); !strings.Contains(footer, "Success: refresh complete") ||
			len(ansiAuditFooterActionSpans(footer)) != 0 {
			t.Fatalf("%dx%d active notice did not own footer: %q", size.width, size.height, footer)
		}
		model.acknowledgeNotice()
		afterAcknowledgement := strings.Split(model.View().Content, "\n")
		if ansi.Strip(ansiAuditLine(before, len(before)-1)) !=
			ansi.Strip(ansiAuditLine(afterAcknowledgement, len(afterAcknowledgement)-1)) {
			t.Fatalf("%dx%d acknowledged notice did not restore footer actions", size.width, size.height)
		}
		model.diffCursor++
		afterSelection := strings.Split(model.View().Content, "\n")
		changedRows := ansiAuditChangedRows(before, afterSelection)
		if len(changedRows) == 0 {
			t.Fatalf("%dx%d selection has no visible state change", size.width, size.height)
		}
		beforePlain := ansiAuditWithoutSelectionMarkers(ansi.Strip(strings.Join(before, "\n")))
		afterPlain := ansiAuditWithoutSelectionMarkers(ansi.Strip(strings.Join(afterSelection, "\n")))
		if beforePlain != afterPlain {
			t.Fatalf("%dx%d selection changed stable content or geometry", size.width, size.height)
		}
		if score, anchors, err := ansiAuditLayoutShiftScore(
			strings.Join(before, "\n"), strings.Join(afterSelection, "\n"), size,
		); err != nil || anchors < 3 || score != 0 {
			t.Fatalf(
				"%dx%d selection LSS = %.4f across %d anchors: %v; want 0 across at least 3",
				size.width, size.height, score, anchors, err,
			)
		}

		model.diffCursor--
		beforeItem := model.View().Content
		appendTestReviewComments(t, model, "new stable-layout audit item")
		afterItem := model.View().Content
		if score, anchors, err := ansiAuditLayoutShiftScore(beforeItem, afterItem, size); err != nil ||
			anchors < 3 || score >= 0.01 {
			t.Fatalf(
				"%dx%d item-add LSS = %.4f across %d anchors: %v; want <0.01 across at least 3",
				size.width, size.height, score, anchors, err,
			)
		}
		if ratio := ansiAuditPlainCellChangeRatio(beforeItem, afterItem, size); ratio >= 0.01 {
			t.Fatalf("%dx%d item-add changed-cell ratio = %.4f, want <0.01", size.width, size.height, ratio)
		}
	}
}

type ansiAuditAnchor struct {
	x, y int
}

func ansiAuditLayoutShiftScore(
	before, after string,
	size ansiAuditSize,
) (float64, int, error) {
	beforeAnchors := ansiAuditSemanticAnchors(before)
	afterAnchors := ansiAuditSemanticAnchors(after)
	if len(beforeAnchors) == 0 || size.width+size.height == 0 {
		return 0, 0, errors.New("layout-shift frame has no semantic anchors or area")
	}
	displacement := 0.0
	for name, beforeAnchor := range beforeAnchors {
		afterAnchor, ok := afterAnchors[name]
		if !ok {
			return 0, len(beforeAnchors), fmt.Errorf("stable anchor %q disappeared", name)
		}
		distance := ansiAuditAbsolute(afterAnchor.x-beforeAnchor.x) +
			ansiAuditAbsolute(afterAnchor.y-beforeAnchor.y)
		displacement += float64(distance) / float64(size.width+size.height)
	}
	return displacement / float64(len(beforeAnchors)), len(beforeAnchors), nil
}

func ansiAuditAbsolute(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func ansiAuditSemanticAnchors(frame string) map[string]ansiAuditAnchor {
	lines := strings.Split(ansi.Strip(frame), "\n")
	anchors := make(map[string]ansiAuditAnchor, 12)
	if len(lines) == 0 {
		return anchors
	}
	for _, title := range []string{"FILES", "DIFF", "COMMENTS"} {
		if before, _, ok := strings.Cut(lines[0], title); ok {
			anchors["title:"+title] = ansiAuditAnchor{x: lipgloss.Width(before)}
		}
	}
	for index, column := range ansiAuditSeparatorColumns(lines[0]) {
		anchors[fmt.Sprintf("separator:%d", index)] = ansiAuditAnchor{x: column}
	}
	for _, cue := range []string{"diff --git", "M 00_active", "check ownership"} {
		if row, column, ok := ansiAuditTextAnchor(lines, cue); ok {
			anchors["content:"+cue] = ansiAuditAnchor{x: column, y: row}
		}
	}
	footerRow := len(lines) - 1
	for _, action := range ansiAuditFooterActionSpans(lines[footerRow]) {
		anchors["action:"+strings.ToLower(action.text)] = ansiAuditAnchor{
			x: action.start, y: footerRow,
		}
	}
	return anchors
}

func ansiAuditTextAnchor(lines []string, cue string) (int, int, bool) {
	for row, line := range lines {
		if before, _, ok := strings.Cut(line, cue); ok {
			return row, lipgloss.Width(before), true
		}
	}
	return 0, 0, false
}

func ansiAuditWithoutSelectionMarkers(content string) string {
	return strings.NewReplacer("▸", " ", "›", " ").Replace(content)
}

func ansiAuditChangedRows(before, after []string) []int {
	rows := max(len(before), len(after))
	changed := make([]int, 0, 2)
	for row := range rows {
		if row >= len(before) || row >= len(after) || before[row] != after[row] {
			changed = append(changed, row)
		}
	}
	return changed
}

func ansiAuditPlainCellChangeRatio(before, after string, size ansiAuditSize) float64 {
	beforeLines := strings.Split(ansi.Strip(before), "\n")
	afterLines := strings.Split(ansi.Strip(after), "\n")
	changed := 0
	for row := range size.height {
		beforeCells := ansiAuditPlainCells(ansiAuditLine(beforeLines, row), size.width)
		afterCells := ansiAuditPlainCells(ansiAuditLine(afterLines, row), size.width)
		for column := range size.width {
			if beforeCells[column] != afterCells[column] {
				changed++
			}
		}
	}
	return float64(changed) / float64(size.width*size.height)
}

func ansiAuditLine(lines []string, row int) string {
	if row < 0 || row >= len(lines) {
		return ""
	}
	return lines[row]
}

func ansiAuditPlainCells(line string, width int) []string {
	cells := make([]string, width)
	column := 0
	for _, character := range line {
		cellWidth := lipgloss.Width(string(character))
		if column >= width {
			break
		}
		cells[column] = string(character)
		column += max(cellWidth, 1)
	}
	return cells
}
