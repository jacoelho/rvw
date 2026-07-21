package review

import (
	"strings"
	"testing"
	"unsafe"
)

func TestRemoteReviewStringsOwnValidatedBacking(t *testing.T) {
	id := oversizedParentSuffix("comment-id")
	author := oversizedParentSuffix("author")
	body := oversizedParentSuffix("body")
	createdAt := oversizedParentSuffix("created-at")
	url := oversizedParentSuffix("https://example.test/comment")
	comment, err := NewRemoteComment(RemoteCommentDescriptor{
		ID: id, Author: author, Body: body, CreatedAt: createdAt, URL: url,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertOwnedRemoteString(t, "comment ID", comment.ID(), id)
	assertOwnedRemoteString(t, "comment author", comment.Author(), author)
	assertOwnedRemoteString(t, "comment body", comment.Body(), body)
	assertOwnedRemoteString(t, "comment creation time", comment.CreatedAt(), createdAt)
	assertOwnedRemoteString(t, "comment URL", comment.URL(), url)

	threadID := oversizedParentSuffix("thread-id")
	path, err := NewPath([]byte("one.go"))
	if err != nil {
		t.Fatal(err)
	}
	thread, err := NewRemoteThread(RemoteThreadDescriptor{
		ID: threadID, Path: path, Side: LineNew, Comments: []RemoteComment{comment},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertOwnedRemoteString(t, "thread ID", thread.ID(), threadID)

	detail := oversizedParentSuffix("remote threads unavailable")
	ownedDetail, err := NewOperationDetail(detail)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := testSnapshot(t, 1, "one.go").WithRemoteThreadFailure(
		RemoteThreadsUnavailable,
		ownedDetail,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertOwnedRemoteString(
		t,
		"remote failure detail",
		snapshot.RemoteThreadSummary().Detail.String(),
		detail,
	)
}

func oversizedParentSuffix(value string) string {
	parent := strings.Repeat("x", 1<<20) + value
	return parent[len(parent)-len(value):]
}

func assertOwnedRemoteString(t *testing.T, name, got, source string) {
	t.Helper()
	if unsafe.StringData(got) == unsafe.StringData(source) {
		t.Fatalf("%s retained caller backing", name)
	}
}
