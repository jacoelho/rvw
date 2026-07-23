package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jacoelho/rvw/internal/review"
)

func TestCommentEditorRejectsUnsafeInputBeforeWidgetAndView(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "escape", value: "unsafe-before\x1bunsafe-after"},
		{name: "tab", value: "unsafe-before\tunsafe-after"},
		{name: "carriage return", value: "unsafe-before\runsafe-after"},
		{name: "delete", value: "unsafe-before\x7funsafe-after"},
		{name: "c1", value: "unsafe-before\u0085unsafe-after"},
		{name: "bidi", value: "unsafe-before\u202eunsafe-after"},
		{name: "replacement", value: "unsafe-before\ufffdunsafe-after"},
		{name: "invalid utf-8", value: string([]byte("unsafe-before\xffunsafe-after"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, coordinator := newTestModel(t, map[string]string{
				"alpha.txt": "original\nadded\n",
			})
			_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
			active, ok := model.review.ActiveFile()
			if !ok {
				t.Fatal("missing active file")
			}
			waitForPatch(t, model, coordinator, active)
			beginTestLineComment(t, model)
			_, _ = model.Update(tea.PasteMsg{Content: "alpha\nomega"})
			model.commentEditor.CursorUp()
			model.commentEditor.SetCursorColumn(2)
			wantBody := model.commentEditor.Value()
			wantLine := model.commentEditor.Line()
			wantColumn := model.commentEditor.Column()

			_, _ = model.Update(tea.PasteMsg{Content: test.value})
			draft, ok := model.review.Draft()
			if !ok || model.commentEditor.Value() != wantBody || draft.Body() != wantBody {
				t.Fatalf("rejected paste changed editor %q, draft %q, present %t", model.commentEditor.Value(), draft.Body(), ok)
			}
			if model.commentEditor.Line() != wantLine || model.commentEditor.Column() != wantColumn {
				t.Fatalf(
					"rejected paste cursor %d:%d, want %d:%d",
					model.commentEditor.Line(), model.commentEditor.Column(), wantLine, wantColumn,
				)
			}
			if model.notice == nil || model.notice.Kind != NoticeWarning ||
				model.notice.Text != NoticeText(review.ErrInvalidReviewTransition.Error()) {
				t.Fatalf("notice = %+v", model.notice)
			}
			if view := model.View().Content; strings.Contains(view, test.value) {
				t.Fatalf("view contains rejected raw paste %q", test.value)
			}

			model.notice = nil
			_ = model.updateCommentEditor(tea.KeyPressMsg(tea.Key{Code: 'x', Text: test.value}))
			draft, ok = model.review.Draft()
			if !ok || model.commentEditor.Value() != wantBody || draft.Body() != wantBody {
				t.Fatalf(
					"rejected key text changed editor %q, draft %q, present %t",
					model.commentEditor.Value(),
					draft.Body(),
					ok,
				)
			}
			if model.commentEditor.Line() != wantLine || model.commentEditor.Column() != wantColumn {
				t.Fatalf(
					"rejected key-text cursor %d:%d, want %d:%d",
					model.commentEditor.Line(),
					model.commentEditor.Column(),
					wantLine,
					wantColumn,
				)
			}
			if model.notice == nil || model.notice.Kind != NoticeWarning ||
				model.notice.Text != NoticeText(review.ErrInvalidReviewTransition.Error()) {
				t.Fatalf("key-text notice = %+v", model.notice)
			}
		})
	}
}

func TestCommentEditorChecksExactPasteCandidateSizeBeforeScalarSafety(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "original\nadded\n",
	})
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	active, ok := model.review.ActiveFile()
	if !ok {
		t.Fatal("missing active file")
	}
	waitForPatch(t, model, coordinator, active)
	beginTestLineComment(t, model)
	_, _ = model.Update(tea.PasteMsg{Content: "kept"})
	model.commentEditor.SetCursorColumn(2)
	wantLine := model.commentEditor.Line()
	wantColumn := model.commentEditor.Column()

	_, _ = model.Update(tea.PasteMsg{Content: strings.Repeat("\x1b", int(review.MaxCommentBodyBytes))})
	draft, ok := model.review.Draft()
	if !ok || draft.Body() != "kept" || model.commentEditor.Value() != "kept" {
		t.Fatalf("rejected paste changed editor %q, draft %q", model.commentEditor.Value(), draft.Body())
	}
	if model.commentEditor.Line() != wantLine || model.commentEditor.Column() != wantColumn {
		t.Fatalf("cursor %d:%d, want %d:%d", model.commentEditor.Line(), model.commentEditor.Column(), wantLine, wantColumn)
	}
	if model.notice == nil || !strings.Contains(string(model.notice.Text), "comment body limit reached") {
		t.Fatalf("notice = %+v", model.notice)
	}
}

func TestModelRemainsUsableWithUnavailableUnsafePendingCapture(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	detail, err := review.NewOperationDetail("GitHub pending review body was unsafe")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := model.review.Snapshot().WithPendingReviewStatus(
		review.PendingReviewUnavailable,
		detail,
	)
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = review.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = model.Update(tea.WindowSizeMsg{Width: wideWidth, Height: 30})
	if summary := model.review.PendingReviewSummary(); summary.Status != review.PendingReviewUnavailable {
		t.Fatalf("pending status = %s", summary.Status)
	}
	model.overlay = OverlaySubmit
	if view := model.View().Content; view == "" || strings.Contains(view, "\u202e") {
		t.Fatalf("unavailable model view was empty or unsafe: %q", view)
	} else if !strings.Contains(view, detail.String()) {
		t.Fatalf("submit view omitted pending-review failure detail: %q", view)
	}
	setModelKeybindings(t, model, Keybinding{
		Context: KeyContextSubmit,
		Chord:   "F2",
		Action:  KeyActionStatusDetails,
	})
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF2}))
	if model.overlay != OverlayStatus {
		t.Fatalf("F2 left overlay = %d, want status details", model.overlay)
	}
}
