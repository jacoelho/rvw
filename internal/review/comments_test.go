package review

import (
	"strings"
	"testing"
	"unsafe"
)

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
