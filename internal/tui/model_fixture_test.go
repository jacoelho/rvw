package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/change"
	workcoord "github.com/jacoelho/rvw/internal/coordinator"
	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/keymap"
	"github.com/jacoelho/rvw/internal/source"
)

func newTestModel(tb testing.TB, changed map[string]string) (*Model, *Coordinator) {
	tb.Helper()
	t := tb
	fixture := newSourceFixture(t, changed)
	workContext, cancelWork := context.WithCancel(t.Context())
	deliveryManager := newTestDeliveryManager(t)
	coordinator, err := NewCoordinator(workContext, fixture.service, deliveryManager)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	receiver, err := NewReadyReceiver(t.Context())
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	contextLines, err := change.NewContextLines(3)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	exportTemplates, err := change.CompileExportTemplates(
		"{{comments}}",
		"{{index}} {{comment}}",
	)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	features, err := NewSettingFeatures(FeatureMouse, FeatureWrap)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	syntaxSelector, err := change.NewSyntaxSelector("test")
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	settings, err := NewSettings(SettingsRequest{
		Features:         features,
		DiffView:         DiffViewUnified,
		ContextLines:     contextLines,
		SyntaxSelector:   syntaxSelector,
		ExportTemplates:  exportTemplates,
		PublicationEvent: change.PublicationEventComment,
		Keybindings: []keymap.Binding{
			{Context: keymap.ContextNormal, Chord: "q", Action: keymap.ActionQuit},
			{Context: keymap.ContextNormal, Chord: "j", Action: keymap.ActionMoveDown},
			{Context: keymap.ContextNormal, Chord: "enter", Action: keymap.ActionActivate},
			{Context: keymap.ContextNormal, Chord: "c", Action: keymap.ActionCommentAtCursor},
			{Context: keymap.ContextNormal, Chord: "M", Action: keymap.ActionExportMarkdown},
			{Context: keymap.ContextNormal, Chord: "J", Action: keymap.ActionExportJSON},
			{Context: keymap.ContextNormal, Chord: "?", Action: keymap.ActionHelp},
			{Context: keymap.ContextCommentEdit, Chord: "ctrl-s", Action: keymap.ActionSaveComment},
			{Context: keymap.ContextCommentEdit, Chord: "esc", Action: keymap.ActionCancel},
			{Context: keymap.ContextHelp, Chord: "q", Action: keymap.ActionCancel},
			{Context: keymap.ContextExportPicker, Chord: "enter", Action: keymap.ActionExportSelected},
			{Context: keymap.ContextExportPicker, Chord: "esc", Action: keymap.ActionCancel},
			{Context: keymap.ContextMinimumSize, Chord: "q", Action: keymap.ActionQuit},
		},
	})
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	opened, revision, active := fixture.document.Materialization()
	if !active {
		cancelWork()
		t.Fatal("source fixture revision is unavailable")
	}
	review, err := change.NewReview(change.ReviewRequest{Snapshot: opened.Snapshot()})
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	model, err := NewModel(
		workContext,
		opened,
		revision,
		review,
		settings,
		coordinator,
		receiver,
	)
	if err != nil {
		cancelWork()
		t.Fatal(err)
	}
	if got := coordinator.Stats().Accepted; got != 0 {
		cancelWork()
		t.Fatalf("model construction accepted %d operations, want 0", got)
	}
	updated, command := model.Update(initializeMsg{})
	if updated != model || command != nil {
		cancelWork()
		t.Fatalf("initialize update = (%T, %v), want same model and no command", updated, command)
	}
	indexContext, cancelIndex := context.WithTimeout(t.Context(), 5*time.Second)
	if state := coordinator.WaitReady(indexContext); state != ReadyAvailable {
		cancelIndex()
		cancelWork()
		t.Fatalf("workspace index readiness = %v, want available", state)
	}
	cancelIndex()
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	t.Cleanup(func() {
		receiverContext, cancelReceiver := context.WithTimeout(context.Background(), time.Second)
		defer cancelReceiver()
		if err := receiver.StopAndWait(receiverContext); err != nil {
			t.Error(err)
		}
		coordinator.StopAdmissionAndCancel()
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := coordinator.Shutdown(shutdownContext); err != nil {
			t.Error(err)
		}
		cancelWork()
	})
	return model, coordinator
}

func testReviewRequest(snapshot change.Snapshot) change.ReviewRequest {
	return change.ReviewRequest{Snapshot: snapshot}
}

func newTestDeliveryManager(tb testing.TB) *delivery.Manager {
	tb.Helper()
	t := tb
	manager := delivery.NewManager()
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Close(closeContext); err != nil {
			t.Error(err)
		}
	})
	return manager
}

type sourceFixture struct {
	service  *source.Service
	document *workcoord.SourceRevision
}

func newSourceFixture(tb testing.TB, changed map[string]string) sourceFixture {
	tb.Helper()
	t := tb
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "rvw@example.test")
	runGit(t, root, "config", "user.name", "rvw test")
	for name := range changed {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "initial")
	for name, contents := range changed {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workingDirectory, err := source.NewWorkingDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := source.NewService(workingDirectory, source.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opened, revision, err := service.Open(t.Context(), source.OpenRequest{
		Target: source.WorktreeTarget(),
		Options: source.OpenOptions{
			IncludeUntracked: true,
			BuiltinDefaults:  true,
			Gitignore:        true,
			RVWIgnore:        true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	document := workcoord.NewSourceRevision(opened, revision)
	t.Cleanup(func() {
		if err := document.Retire().Close(); err != nil {
			t.Error(err)
		}
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := service.Close(shutdownContext); err != nil {
			t.Error(err)
		}
	})
	return sourceFixture{service: service, document: document}
}

func runGit(tb testing.TB, directory string, arguments ...string) {
	tb.Helper()
	t := tb
	command := exec.CommandContext(tb.Context(), "git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func keyMessage(value string) tea.KeyPressMsg {
	code := []rune(value)[0]
	if value == "enter" {
		code = tea.KeyEnter
	}
	return tea.KeyPressMsg(tea.Key{Text: value, Code: code})
}
