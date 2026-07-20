package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTakeCheckedPublicationOwnsAndValidatesPayload(t *testing.T) {
	base, _ := NewObjectID(strings.Repeat("1", 40))
	head, _ := NewObjectID(strings.Repeat("2", 40))
	target, err := NewPublicationTarget(PublicationTargetDescriptor{
		Repository: "owner/repository", PullRequest: 7, Base: base, Head: head,
	})
	if err != nil {
		t.Fatal(err)
	}
	marker, _ := NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventApprove, policies)
	prepared := PreparedPublication{
		workspace: 1,
		source:    1,
		options:   options,
		summary:   "summary",
		comments: []PublicationComment{{
			path:      "file.go",
			body:      "comment",
			side:      LineNew,
			startLine: 2,
			endLine:   3,
		}},
	}
	checked, err := TakeCheckedPublication(
		CheckedPublicationDescriptor{Target: target, Marker: marker},
		prepared,
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload publicationPayload
	if err := json.Unmarshal([]byte(checked.Payload()), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CommitID != head.String() ||
		payload.Event != "APPROVE" ||
		payload.Body != "summary\n\n"+marker.String() ||
		len(payload.Comments) != 1 ||
		payload.Comments[0].Path != "file.go" ||
		payload.Comments[0].Side != "RIGHT" ||
		payload.Comments[0].StartLine != 2 ||
		payload.Comments[0].Line != 3 {
		t.Fatalf("canonical payload = %#v", payload)
	}
	if _, err := TakeCheckedPublication(
		CheckedPublicationDescriptor{
			Target: target,
			Marker: PublicationMarker("marker"),
		},
		prepared,
	); err == nil {
		t.Fatal("invalid marker accepted")
	}
}

func TestPreparePublicationRecordsEveryCommentDecision(t *testing.T) {
	issue, _ := NewCommentKind("issue")
	current, err := NewReview(testSnapshot(t, 1, "file.go"), []CommentKind{issue})
	if err != nil {
		t.Fatal(err)
	}
	line, _ := NewLineNumber(2)
	inlineTarget, _ := LineCommentTarget(0, LineNew, line, line)
	fileTarget, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, inlineTarget, issue, "inline")
	current = saveTestComment(t, current, fileTarget, issue, "summary")
	current = saveTestComment(t, current, ReviewCommentTarget(), issue, "excluded")
	excluded := current.Comments()[2].ID()

	policies, _ := NewPublicationPolicies(PublicationPrefixKinds)
	options, _ := NewPublicationOptions(PublicationEventComment, policies)
	options, err = options.WithExcludedComments([]CommentID{excluded})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	want := []PublicationDecision{
		PublicationIncludedInline,
		PublicationIncludedSummary,
		PublicationExcludedManual,
	}
	for index, expected := range want {
		decision, ok := prepared.DecisionAt(index)
		if !ok || decision.CommentID() != current.Comments()[index].ID() ||
			decision.Decision() != expected {
			t.Fatalf(
				"decision %d = (%v, %v), want comment %d and %v",
				index,
				decision.CommentID(),
				decision.Decision(),
				current.Comments()[index].ID(),
				expected,
			)
		}
	}
	if prepared.IncludedCount() != 2 || prepared.ExcludedCount() != 1 ||
		prepared.CommentCount() != 1 ||
		!strings.Contains(prepared.SummaryBody(), "[issue] summary") {
		t.Fatalf(
			"prepared publication = included %d excluded %d inline %d summary %q",
			prepared.IncludedCount(),
			prepared.ExcludedCount(),
			prepared.CommentCount(),
			prepared.SummaryBody(),
		)
	}
}
