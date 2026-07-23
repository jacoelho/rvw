package app

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jacoelho/rvw/internal/review"
)

func TestCommentKindPresentationEscapesOnlyVisibleBidirectionalControls(t *testing.T) {
	styles := semanticStyles{
		commentNote:    lipgloss.NewStyle().Bold(true),
		commentSuggest: lipgloss.NewStyle().Underline(true),
	}
	for _, test := range []struct {
		label      string
		want       string
		wantStyled bool
	}{
		{label: "note", want: "[note] ", wantStyled: true},
		{label: "suggestion", want: "[suggestion] ", wantStyled: true},
		{label: "kind\u202e", want: `[kind\u{202E}] `},
		{label: "kind\u2066", want: `[kind\u{2066}] `},
		{label: "kind\ufffd", want: "[kind\ufffd] "},
		{label: `kind\u{202E}`, want: `[kind\u{202E}] `},
	} {
		t.Run(test.label, func(t *testing.T) {
			kinds, err := review.NewCommentKinds(test.label)
			if err != nil {
				t.Fatal(err)
			}
			kind, _ := kinds.At(0)
			if kind.String() != test.label {
				t.Fatalf("semantic label = %q, want %q", kind.String(), test.label)
			}
			rendered := commentKindText(kind, styles)
			if plain := ansi.Strip(rendered); plain != test.want {
				t.Fatalf("visible kind = %q, want %q", plain, test.want)
			}
			if styled := strings.Contains(rendered, "\x1b["); styled != test.wantStyled {
				t.Fatalf("styled = %t, want %t: %q", styled, test.wantStyled, rendered)
			}
			if strings.ContainsAny(rendered, "\u202e\u2066") {
				t.Fatalf("rendered kind contains raw bidirectional control: %q", rendered)
			}
		})
	}
}

func TestCommentRowsNeverExposeRawConfiguredBidirectionalLabels(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	for _, label := range []string{"kind\u202e", "kind\u2066", "kind\ufffd", `kind\u{202E}`} {
		t.Run(label, func(t *testing.T) {
			kinds, err := review.NewCommentKinds(label)
			if err != nil {
				t.Fatal(err)
			}
			policies, err := review.NewCommentPolicies(review.CommentTypePrefixForGitHub)
			if err != nil {
				t.Fatal(err)
			}
			current, err := review.NewReview(review.ReviewRequest{
				Snapshot: model.review.Snapshot(), CommentKinds: kinds, CommentPolicies: policies,
			})
			if err != nil {
				t.Fatal(err)
			}
			current, err = current.BeginDraft(review.ReviewCommentTarget())
			if err != nil {
				t.Fatal(err)
			}
			current, err = current.UpdateDraft("body")
			if err != nil {
				t.Fatal(err)
			}
			current, _, err = current.SaveDraft()
			if err != nil {
				t.Fatal(err)
			}
			model.review = current

			lines, _ := model.commentLines(80, 3, newSemanticStyles(model.settings))
			if len(lines) != 1 {
				t.Fatalf("comment lines = %d, want 1", len(lines))
			}
			plain := ansi.Strip(lines[0])
			if strings.ContainsAny(plain, "\u202e\u2066") {
				t.Fatalf("comment row contains raw bidirectional control: %q", plain)
			}
			if !strings.Contains(plain, safeText(label)) || !strings.Contains(plain, "body") {
				t.Fatalf("comment row = %q", plain)
			}
		})
	}
}
