package review

import (
	"strings"
	"testing"
	"unsafe"
)

func TestRemoteThreadMappingOwnsMarkersAndNavigationIdentity(t *testing.T) {
	line, _ := NewLineNumber(9)
	thread := testRemoteThread(t, "thread", "before.go", LineOld, &line, true, false)
	snapshot, err := testRenamedSnapshot(t, 1, "before.go", "after.go").WithRemoteThreads(
		[]RemoteThread{thread},
	)
	if err != nil {
		t.Fatal(err)
	}
	mapped := snapshot.RemoteThreads(0, 1)[0]
	file, status := mapped.FileMapping()
	if status != RemoteMappingMapped || file != 0 {
		t.Fatalf("mapping = (%d, %d), want file 0 mapped", file, status)
	}
	current := testReview(t, snapshot)
	if !current.HasCommentAnchor(0, LineOld, line) {
		t.Fatal("resolved remote thread did not create an old-side marker")
	}
}

func TestRemoteThreadMappingRejectsAmbiguityAndExcludesOutdatedLines(t *testing.T) {
	line, _ := NewLineNumber(5)
	ambiguous := testRemoteThread(t, "ambiguous", "same.go", LineNew, &line, false, false)
	snapshot, err := testSnapshot(t, 1, "same.go", "same.go").WithRemoteThreads(
		[]RemoteThread{ambiguous},
	)
	if err != nil {
		t.Fatal(err)
	}
	thread := snapshot.RemoteThreads(0, 1)[0]
	if _, status := thread.FileMapping(); status != RemoteMappingAmbiguous {
		t.Fatalf("ambiguous mapping status = %d", status)
	}
	if testReview(t, snapshot).HasCommentAnchor(0, LineNew, line) ||
		testReview(t, snapshot).HasCommentAnchor(1, LineNew, line) {
		t.Fatal("ambiguous remote thread created a marker")
	}

	outdated := testRemoteThread(t, "outdated", "one.go", LineNew, &line, false, true)
	snapshot, err = testSnapshot(t, 2, "one.go").WithRemoteThreads([]RemoteThread{outdated})
	if err != nil {
		t.Fatal(err)
	}
	thread = snapshot.RemoteThreads(0, 1)[0]
	if file, status := thread.FileMapping(); status != RemoteMappingMapped || file != 0 {
		t.Fatalf("outdated mapping = (%d, %d), want file 0 mapped", file, status)
	}
	if testReview(t, snapshot).HasCommentAnchor(0, LineNew, line) {
		t.Fatal("outdated remote thread created a marker")
	}
}

func TestRemoteThreadFailureClearsDerivedMappingsAndMarkers(t *testing.T) {
	line, _ := NewLineNumber(3)
	thread := testRemoteThread(t, "thread", "one.go", LineNew, &line, false, false)
	snapshot, err := testSnapshot(t, 1, "one.go").WithRemoteThreads([]RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := NewOperationDetail("remote threads failed")
	snapshot, err = snapshot.WithRemoteThreadFailure(RemoteThreadsUnavailable, detail)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RemoteThreadCount() != 0 ||
		testReview(t, snapshot).HasCommentAnchor(0, LineNew, line) {
		t.Fatal("remote thread failure retained derived state")
	}
}

func testRemoteThread(
	t *testing.T,
	id, pathValue string,
	side LineSide,
	line *LineNumber,
	resolved, outdated bool,
) RemoteThread {
	t.Helper()
	path, err := NewPath([]byte(pathValue))
	if err != nil {
		t.Fatal(err)
	}
	thread, err := NewRemoteThread(RemoteThreadDescriptor{
		ID: id, Resolved: resolved, Outdated: outdated,
		Path: path, Line: line, Side: side,
	})
	if err != nil {
		t.Fatal(err)
	}
	return thread
}

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
