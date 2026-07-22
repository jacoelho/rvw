package review

import (
	"strings"
	"testing"
	"unsafe"
)

func TestSavedCommentAnchorsAreImmutableAndCounted(t *testing.T) {
	base := testReview(t, testSnapshot(t, 1, "one.go"))
	start, _ := NewLineNumber(3)
	end, _ := NewLineNumber(7)
	target, _ := LineCommentTarget(0, LineNew, start, end)

	first := saveTestComment(t, base, target, "first")
	if base.HasCommentAnchor(0, LineNew, end) {
		t.Fatal("saving a comment mutated the prior review value")
	}
	if !first.HasCommentAnchor(0, LineNew, end) ||
		first.HasCommentAnchor(0, LineNew, start) {
		t.Fatal("range comment was not anchored exclusively at its end line")
	}

	second := saveTestComment(t, first, target, "second")
	firstID := second.Comments()[0].ID()
	afterFirstDelete, err := second.DeleteComment(firstID)
	if err != nil {
		t.Fatal(err)
	}
	if !afterFirstDelete.HasCommentAnchor(0, LineNew, end) {
		t.Fatal("deleting one of two comments removed their shared marker")
	}
	secondID := afterFirstDelete.Comments()[0].ID()
	afterSecondDelete, err := afterFirstDelete.DeleteComment(secondID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSecondDelete.HasCommentAnchor(0, LineNew, end) {
		t.Fatal("deleting the final comment retained its marker")
	}
	if !second.HasCommentAnchor(0, LineNew, end) {
		t.Fatal("deleting comments mutated a prior review value")
	}
}

func TestDraftAndNonLineCommentsDoNotCreateAnchors(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	line, _ := NewLineNumber(4)
	target, _ := LineCommentTarget(0, LineOld, line, line)
	draft, err := current.BeginDraft(target)
	if err != nil {
		t.Fatal(err)
	}
	if draft.HasCommentAnchor(0, LineOld, line) {
		t.Fatal("unsaved draft created a comment marker")
	}
	if fileComment := saveTestComment(t, current, mustFileTarget(t), "file"); fileComment.HasCommentAnchor(0, LineOld, line) {
		t.Fatal("file comment created a line marker")
	}
	if reviewComment := saveTestComment(t, current, ReviewCommentTarget(), "review"); reviewComment.HasCommentAnchor(0, LineOld, line) {
		t.Fatal("review comment created a line marker")
	}
}

func mustFileTarget(t *testing.T) CommentTarget {
	t.Helper()
	target, err := FileCommentTarget(0)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestValidatedCommentStringsOwnTheirBacking(t *testing.T) {
	bodyParent := strings.Repeat("y", 1<<20) + "body"
	bodySource := bodyParent[len(bodyParent)-4:]
	body, err := NewCommentBody(bodySource)
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.StringData(body.String()) == unsafe.StringData(bodySource) {
		t.Fatal("comment body retained caller backing")
	}
}

func TestDraftBodyOwnsValidatedBacking(t *testing.T) {
	current, err := NewReview(testSnapshot(t, 1, "one.go"))
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.BeginDraft(ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	parent := strings.Repeat("z", 1<<20) + "draft"
	source := parent[len(parent)-5:]
	current, err = current.UpdateDraft(source)
	if err != nil {
		t.Fatal(err)
	}
	draft, ok := current.Draft()
	if !ok {
		t.Fatal("updated draft is unavailable")
	}
	if unsafe.StringData(draft.Body()) == unsafe.StringData(source) {
		t.Fatal("draft body retained caller backing")
	}
}
