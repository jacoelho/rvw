package change

import (
	"strings"
	"testing"
)

var commentSafetyReviewSink Review

func BenchmarkReviewUpdateDraftMaximumBody(b *testing.B) {
	current := scaleReview(b)
	current, err := current.BeginDraft(ReviewCommentTarget())
	if err != nil {
		b.Fatal(err)
	}
	body := strings.Repeat("x", int(MaxCommentBodyBytes))

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		updated, updateErr := current.UpdateDraft(body)
		if updateErr != nil {
			b.Fatal(updateErr)
		}
		commentSafetyReviewSink = updated
	}
	b.StopTimer()

	draft, ok := commentSafetyReviewSink.Draft()
	if !ok || len(draft.Body()) != len(body) || draft.Body() != body {
		b.Fatalf("updated draft bytes = %d, present = %t; want %d, true", len(draft.Body()), ok, len(body))
	}
}
