package app

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jacoelho/rvw/internal/review"
)

func TestMinimumSizeOwnsInput(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	model.mode = ModeCommentEdit
	_ = model.commentEditor.Focus()
	_, _ = model.Update(tea.WindowSizeMsg{Width: minimumWidth - 1, Height: minimumHeight})
	if cursor := model.View().Cursor; cursor != nil {
		t.Fatalf("minimum-size view exposed hidden editor cursor: %+v", cursor)
	}
	model.mode = ModeNormal
	before, _ := model.review.SelectedFile()

	updated, command := model.Update(keyMessage("j"))
	if updated != model || command != nil {
		t.Fatal("minimum-size ignored action changed owner or returned work")
	}
	after, _ := model.review.SelectedFile()
	if after != before {
		t.Fatalf("minimum-size move changed selection from %d to %d", before, after)
	}

	_, command = model.Update(keyMessage("q"))
	if command == nil {
		t.Fatal("minimum-size quit did not return tea.Quit")
	}
	if message := command(); message != (tea.QuitMsg{}) {
		t.Fatalf("quit command returned %T, want tea.QuitMsg", message)
	}
}

func TestWrappedDiffRetainsLongLineTailInUnifiedAndSideBySideViews(t *testing.T) {
	const tail = "TAIL-MARKER"
	longLine := strings.Repeat("segment ", 30) + tail + "\n"
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": longLine})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})

	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 24})
	if content := model.View().Content; !strings.Contains(content, tail) {
		t.Fatalf("wrapped unified view omitted long-line tail:\n%s", content)
	}
	model.settings.wrap = false
	if content := model.View().Content; strings.Contains(content, tail) {
		t.Fatalf("clipped unified view unexpectedly retained offscreen tail:\n%s", content)
	}

	model.settings.wrap = true
	model.settings.diffView = DiffViewSideBySide
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	if content := model.View().Content; !strings.Contains(content, tail) {
		t.Fatalf("wrapped side-by-side view omitted long-line tail:\n%s", content)
	}
}

func TestDiffRenderingPreservesUnicodeAndExpandsTabs(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "12\tcafé 界 e\u0301 👩‍💻\n",
	})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})
	_, _ = model.Update(tea.WindowSizeMsg{Width: compactWidth, Height: 24})

	content := model.View().Content
	if strings.Contains(content, `\u{0009}`) {
		t.Fatalf("diff rendered a tab as an escaped control:\n%s", content)
	}
	if !strings.Contains(content, "12  café 界 e\u0301 👩‍💻") {
		t.Fatalf("diff did not preserve printable Unicode or expand its tab:\n%s", content)
	}
}

func TestDiffRenderingEscapesHostileTextInEveryLayout(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "safe\x1b\u0085\u202e" + string([]byte{0xff}) + "\n",
	})
	waitContext, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if state := coordinator.WaitReady(waitContext); state != ReadyAvailable {
		t.Fatalf("patch ready state = %d", state)
	}
	_, _ = model.Update(outcomeReadyMsg{state: ReadyAvailable})

	for _, test := range []struct {
		name     string
		view     DiffView
		wrap     bool
		width    int
		expected string
	}{
		{"unified wrapped", DiffViewUnified, true, compactWidth, `\x1B\u{0085}\u{202E}\xFF`},
		{"unified clipped", DiffViewUnified, false, compactWidth, `\x1B\u{0085}\u{202E}\xFF`},
		{"side-by-side wrapped", DiffViewSideBySide, true, wideWidth, `\x1B\u{0085}\u{202E}\xFF`},
		{"side-by-side clipped", DiffViewSideBySide, false, wideWidth, `\x1B\u{0085}\u{202E}\xFF`},
	} {
		t.Run(test.name, func(t *testing.T) {
			model.settings.diffView = test.view
			model.settings.wrap = test.wrap
			_, _ = model.Update(tea.WindowSizeMsg{Width: test.width, Height: 24})
			content := ansi.Strip(model.View().Content)
			if !utf8.ValidString(content) {
				t.Fatal("view contains invalid UTF-8")
			}
			for _, unsafe := range []string{"\x1b", "\u0085", "\u202e", string([]byte{0xff})} {
				if strings.Contains(content, unsafe) {
					t.Fatalf("view emitted unsafe text %q:\n%s", unsafe, content)
				}
			}
			if !strings.Contains(content, test.expected) {
				t.Fatalf("view omitted visible escapes %q:\n%s", test.expected, content)
			}
		})
	}
}

func TestSyntaxRenderingDoesNotSplitGraphemeForStyle(t *testing.T) {
	model := Model{}
	raw := "1️⃣\tvalue\x1b"
	spans := func(yield func(review.SyntaxSpan) bool) {
		yield(review.SyntaxSpan{Start: 0, End: 1, Kind: review.SyntaxNumber})
	}
	rendered := model.renderSyntaxContent(syntaxContent{
		raw:    raw,
		spans:  spans,
		width:  80,
		wrap:   true,
		styles: semanticStyles{number: lipgloss.NewStyle().Bold(true)},
	})
	want := lipgloss.NewStyle().Bold(true).Render("1️⃣") + `  value\x1B`
	if rendered != want {
		t.Fatalf("syntax render = %q, want exact grapheme styling %q", rendered, want)
	}
}

func TestPublicationPreflightRendersCanonicalPayloadAndDecision(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	kind := model.settings.commentKinds[0]
	target, _ := review.FileCommentTarget(0)
	current, err := model.review.BeginDraft(target, kind)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("full publication comment")
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = current.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	policies, _ := review.NewPublicationPolicies()
	options, _ := review.NewPublicationOptions(review.PublicationEventComment, policies)
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := review.NewObjectID(strings.Repeat("1", 40))
	head, _ := review.NewObjectID(strings.Repeat("2", 40))
	publicationTarget, err := review.NewPublicationTarget(
		review.PublicationTargetDescriptor{
			Repository:  "owner/repository",
			PullRequest: 7,
			Base:        base,
			Head:        head,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	marker, _ := review.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	checked, err := review.TakeCheckedPublication(
		review.CheckedPublicationDescriptor{
			Target: publicationTarget,
			Marker: marker,
		},
		prepared,
	)
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := review.NewOperationID(99)
	current, request, err := current.StartPublicationCheck(operation, options)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.ApplyPublicationCheck(review.PublicationCheckResult{
		Operation: request.Operation,
		Workspace: request.Workspace,
		Source:    request.Source,
		Checked:   checked,
	})
	if err != nil {
		t.Fatal(err)
	}
	model.review = current
	model.overlay = OverlaySubmit
	_, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 25})
	payloadView := model.View().Content
	if !strings.Contains(payloadView, "[payload] canonical GitHub request") ||
		!strings.Contains(payloadView, `"commit_id":"`+head.String()+`"`) {
		t.Fatalf("preflight omitted canonical payload:\n%s", payloadView)
	}

	model.publicationCursor = 1
	commentView := model.View().Content
	if !strings.Contains(commentView, "[summary] issue full publication comment") ||
		!strings.Contains(commentView, "Full comment #0") {
		t.Fatalf("preflight omitted comment decision/detail:\n%s", commentView)
	}
}
