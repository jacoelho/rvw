package review

import (
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestRemoteThreadMappingOwnsMarkersAndNavigationIdentity(t *testing.T) {
	line, _ := NewLineNumber(9)
	thread := testRemoteThread(t, "thread", "before.go", LineOld, &line, remoteThreadOptions{
		resolved: true,
	})
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
	ambiguous := testRemoteThread(t, "ambiguous", "same.go", LineNew, &line, remoteThreadOptions{})
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

	outdated := testRemoteThread(t, "outdated", "one.go", LineNew, &line, remoteThreadOptions{
		outdated: true,
	})
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

func TestRemoteThreadFailureRetainsLastAppliedProjection(t *testing.T) {
	line, _ := NewLineNumber(3)
	thread := testRemoteThread(t, "thread", "one.go", LineNew, &line, remoteThreadOptions{})
	snapshot, err := testSnapshot(t, 1, "one.go").WithRemoteThreads([]RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := NewOperationDetail("remote threads failed")
	snapshot, err = snapshot.WithRemoteThreadFailure(RemoteThreadsUnavailable, detail)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RemoteThreadCount() != 1 ||
		!testReview(t, snapshot).HasCommentAnchor(0, LineNew, line) {
		t.Fatal("remote thread failure discarded the last applied projection")
	}
}

func TestRemoteThreadReturnsBoundedPointAccess(t *testing.T) {
	snapshot := testSnapshotWithRemoteComment(t)
	for _, test := range []struct {
		name  string
		index int
		ok    bool
	}{
		{name: "negative", index: -1},
		{name: "present", index: 0, ok: true},
		{name: "exact end", index: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			thread, ok := snapshot.RemoteThread(test.index)
			if ok != test.ok {
				t.Fatalf("RemoteThread(%d) present = %t, want %t", test.index, ok, test.ok)
			}
			if ok && thread.ID() != "thread" {
				t.Fatalf("RemoteThread(%d) ID = %q, want thread", test.index, thread.ID())
			}
		})
	}
}

func TestRemoteThreadPointAccessPreservesImmutableOwnership(t *testing.T) {
	snapshot := testSnapshotWithRemoteComment(t)
	thread, ok := snapshot.RemoteThread(0)
	if !ok || thread.ID() != "thread" {
		t.Fatal("remote thread is unavailable")
	}

	threads := snapshot.RemoteThreads(0, 1)
	threads[0] = RemoteThread{}
	thread, _ = snapshot.RemoteThread(0)
	if thread.ID() != "thread" {
		t.Fatal("mutating the ranged thread copy changed snapshot ownership")
	}
	comments := thread.Comments(0, 1)
	comments[0] = RemoteComment{}
	comment, ok := thread.Comment(0)
	if !ok || comment.ID() != "comment" {
		t.Fatal("mutating the ranged comment copy changed thread ownership")
	}
}

func TestRemoteThreadPointAccessAllocatesNothing(t *testing.T) {
	snapshot := testSnapshotWithRemoteComment(t)
	var (
		thread  RemoteThread
		comment RemoteComment
	)
	allocations := testing.AllocsPerRun(1_000, func() {
		var threadOK, commentOK bool
		thread, threadOK = snapshot.RemoteThread(0)
		comment, commentOK = thread.Comment(0)
		if !threadOK || !commentOK {
			panic("remote point access is unavailable")
		}
	})
	if allocations != 0 {
		t.Fatalf("remote thread plus comment point access allocations = %g, want 0", allocations)
	}
	if thread.ID() != "thread" || comment.ID() != "comment" {
		t.Fatalf("point access = thread %q comment %q", thread.ID(), comment.ID())
	}
}

func TestRemoteThreadIndexTracksStableIdentityWithoutAllocation(t *testing.T) {
	line, _ := NewLineNumber(1)
	threads := []RemoteThread{
		testRemoteThread(t, "thread-z", "one.go", LineNew, &line, remoteThreadOptions{}),
		testRemoteThread(t, "thread-a", "one.go", LineNew, &line, remoteThreadOptions{}),
		testRemoteThread(t, "thread-m", "one.go", LineNew, &line, remoteThreadOptions{}),
	}
	snapshot, err := testSnapshot(t, 1, "one.go").WithRemoteThreads(threads)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{
		"thread-z": 0,
		"thread-a": 1,
		"thread-m": 2,
	} {
		if got, ok := snapshot.RemoteThreadIndex(id); !ok || got != want {
			t.Fatalf("RemoteThreadIndex(%q) = (%d, %t), want (%d, true)", id, got, ok, want)
		}
	}
	if got, ok := snapshot.RemoteThreadIndex("missing"); ok || got != 0 {
		t.Fatalf("missing RemoteThreadIndex = (%d, %t), want (0, false)", got, ok)
	}
	if allocations := testing.AllocsPerRun(1_000, func() {
		if index, ok := snapshot.RemoteThreadIndex("thread-m"); !ok || index != 2 {
			panic("stable remote thread index is unavailable")
		}
	}); allocations != 0 {
		t.Fatalf("RemoteThreadIndex allocations = %g, want 0", allocations)
	}
}

func TestRemoteThreadFailureRetainsStableIdentityIndex(t *testing.T) {
	snapshot := testSnapshotWithRemoteComment(t)
	detail, _ := NewOperationDetail("remote threads failed")
	failed, err := snapshot.WithRemoteThreadFailure(RemoteThreadsUnavailable, detail)
	if err != nil {
		t.Fatal(err)
	}
	if index, ok := failed.RemoteThreadIndex("thread"); !ok || index != 0 {
		t.Fatalf("failed RemoteThreadIndex = (%d, %t), want retained index 0", index, ok)
	}
}

func TestReplacementRetainsRemoteProjectionWhenRefreshFails(t *testing.T) {
	line, _ := NewLineNumber(3)
	thread := testRemoteThread(t, "thread", "one.go", LineNew, &line, remoteThreadOptions{})
	previous, err := testSnapshot(t, 1, "one.go").WithRemoteThreads([]RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	replacement := testSnapshot(t, 2, "one.go")
	detail, _ := NewOperationDetail("GitHub refresh failed")
	replacement, err = replacement.WithRemoteThreadFailure(RemoteThreadsUnavailable, detail)
	if err != nil {
		t.Fatal(err)
	}

	current := testReview(t, previous)
	prepared, err := PrepareReplacement(current, replacement)
	if err != nil {
		t.Fatal(err)
	}
	wantRetained := prepared.next.retainedBytesWithoutSnapshot() +
		prepared.next.snapshot.RetainedBytes() - replacement.RetainedBytes()
	if prepared.RetainedBytes() != wantRetained {
		t.Fatalf("prepared retained bytes = %d, want %d", prepared.RetainedBytes(), wantRetained)
	}
	if construction := ReplacementConstructionBytes(current, replacement); construction < prepared.RetainedBytes() {
		t.Fatalf("construction bound = %d, retained result = %d", construction, prepared.RetainedBytes())
	}
	next, _, err := current.CommitReplacement(prepared)
	if err != nil {
		t.Fatal(err)
	}
	summary := next.Snapshot().RemoteThreadSummary()
	if summary.Status != RemoteThreadsUnavailable || summary.Count != 1 ||
		summary.Detail != detail {
		t.Fatalf("remote summary = %#v, want unavailable stale projection", summary)
	}
	if index, ok := next.Snapshot().RemoteThreadIndex("thread"); !ok || index != 0 {
		t.Fatalf("retained RemoteThreadIndex = (%d, %t), want (0, true)", index, ok)
	}
}

func TestStaleRemoteProjectionExcludesNewlyAcquiredPendingComments(t *testing.T) {
	path, _ := NewPath([]byte("one.go"))
	comment, err := NewRemoteComment(RemoteCommentDescriptor{
		ID: "PRRC_node", Body: "pending copy",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := NewRemoteThread(RemoteThreadDescriptor{
		ID: "thread", Path: path, Side: LineNew, Comments: []RemoteComment{comment},
	})
	if err != nil {
		t.Fatal(err)
	}
	previous, err := testSnapshot(t, 1, "one.go").WithRemoteThreads([]RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := NewOperationDetail("GitHub refresh failed")
	replacement, err := testSnapshot(t, 2, "one.go").WithRemoteThreadFailure(
		RemoteThreadsUnavailable,
		detail,
	)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err = replacement.WithPendingReview(newRetainedTestPendingReview(t))
	if err != nil {
		t.Fatal(err)
	}

	current := testReview(t, previous)
	prepared, err := PrepareReplacement(current, replacement)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := current.CommitReplacement(prepared)
	if err != nil {
		t.Fatal(err)
	}
	summary := next.Snapshot().RemoteThreadSummary()
	if summary.Status != RemoteThreadsUnavailable || summary.Count != 0 {
		t.Fatalf("filtered stale remote summary = %#v", summary)
	}
}

func TestLaterPendingCaptureFiltersFailedStaleRemoteProjection(t *testing.T) {
	path, _ := NewPath([]byte("one.go"))
	comment, err := NewRemoteComment(RemoteCommentDescriptor{
		ID: "PRRC_node", Body: "pending copy",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := NewRemoteThread(RemoteThreadDescriptor{
		ID: "thread", Path: path, Side: LineNew, Comments: []RemoteComment{comment},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := testSnapshot(t, 1, "one.go").WithRemoteThreads([]RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := NewOperationDetail("GitHub refresh failed")
	snapshot, err = snapshot.WithRemoteThreadFailure(RemoteThreadsUnavailable, detail)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = snapshot.WithPendingReview(newRetainedTestPendingReview(t))
	if err != nil {
		t.Fatal(err)
	}
	summary := snapshot.RemoteThreadSummary()
	if summary.Status != RemoteThreadsUnavailable || summary.Count != 0 ||
		summary.Detail != detail {
		t.Fatalf("filtered failed remote summary = %#v", summary)
	}
}

func TestOptionalStaleProjectionDoesNotBlockValidFailureSnapshot(t *testing.T) {
	previous := testSnapshotWithRemoteComment(t)
	detail, _ := NewOperationDetail(strings.Repeat("failure detail ", 16))
	replacement, err := testSnapshot(t, 2, "one.go").WithRemoteThreadFailure(
		RemoteThreadsUnavailable,
		detail,
	)
	if err != nil {
		t.Fatal(err)
	}
	threadDelta := previous.remoteRetainedBytes - detail.Bytes()
	replacement.retainedBytes = MaxSnapshotRetainedBytes - threadDelta - detail.Bytes()/2

	retained, err := retainRemoteThreadsAfterFailure(nil, previous, replacement)
	if err != nil {
		t.Fatal(err)
	}
	summary := retained.RemoteThreadSummary()
	if summary.Status != RemoteThreadsUnavailable || summary.Count != 0 ||
		summary.Detail != detail {
		t.Fatalf("fallback summary = %#v, want original failure-only candidate", summary)
	}
}

func TestRemoteThreadPositionSortHonorsCancellation(t *testing.T) {
	positions := make([]remoteThreadPosition, 4_096)
	for index := range positions {
		positions[index] = remoteThreadPosition{
			id: "thread-" + strings.Repeat("z", index%31), index: index,
		}
	}
	want := errors.New("cancel remote thread position sort")
	polls := 0
	err := sortRemoteThreadPositionsCancellable(func() error {
		polls++
		if polls == 2 {
			return want
		}
		return nil
	}, positions)
	if !errors.Is(err, want) {
		t.Fatalf("position sort error = %v, want %v", err, want)
	}
}

func testSnapshotWithRemoteComment(t *testing.T) Snapshot {
	t.Helper()
	comment, err := NewRemoteComment(RemoteCommentDescriptor{
		ID: "comment", Author: "reviewer", Body: "body",
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := NewPath([]byte("one.go"))
	if err != nil {
		t.Fatal(err)
	}
	thread, err := NewRemoteThread(RemoteThreadDescriptor{
		ID: "thread", Path: path, Side: LineNew, Comments: []RemoteComment{comment},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := testSnapshot(t, 1, "one.go").WithRemoteThreads([]RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

type remoteThreadOptions struct {
	resolved bool
	outdated bool
}

func testRemoteThread(
	t *testing.T,
	id, pathValue string,
	side LineSide,
	line *LineNumber,
	options remoteThreadOptions,
) RemoteThread {
	t.Helper()
	path, err := NewPath([]byte(pathValue))
	if err != nil {
		t.Fatal(err)
	}
	thread, err := NewRemoteThread(RemoteThreadDescriptor{
		ID: id, Resolved: options.resolved, Outdated: options.outdated,
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

func TestTakeRemoteThreadCancellableStopsNearEndAndTransfersOnlyOnSuccess(t *testing.T) {
	path, err := NewPath([]byte("one.go"))
	if err != nil {
		t.Fatal(err)
	}
	comments := testRemoteComments(t, 3)
	descriptor := RemoteThreadDescriptor{
		ID:       "thread",
		Path:     path,
		Side:     LineNew,
		Comments: comments,
	}
	cause := errors.New("stop remote thread construction")
	for _, cancelAt := range []int{4, 5} {
		checks := 0
		thread, cancelErr := TakeRemoteThreadCancellable(func() error {
			checks++
			if checks == cancelAt {
				return cause
			}
			return nil
		}, descriptor)
		if !errors.Is(cancelErr, cause) {
			t.Fatalf("cancel at %d error = %v", cancelAt, cancelErr)
		}
		if checks != cancelAt || thread.CommentCount() != 0 {
			t.Fatalf(
				"cancel at %d: checks=%d comments=%d",
				cancelAt,
				checks,
				thread.CommentCount(),
			)
		}
	}
	if descriptor.Comments[2].ID() != "comment-2" {
		t.Fatal("cancelled take mutated caller-owned comments")
	}

	thread, err := TakeRemoteThread(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.SliceData(thread.comments) != unsafe.SliceData(comments) {
		t.Fatal("successful synchronous take did not consume comment backing")
	}
}

func TestTakeRemoteThreadsCancellableChecksFinalCommentValidationBeforeMapping(t *testing.T) {
	path, err := NewPath([]byte("one.go"))
	if err != nil {
		t.Fatal(err)
	}
	thread, err := TakeRemoteThread(RemoteThreadDescriptor{
		ID:       "thread",
		Path:     path,
		Side:     LineNew,
		Comments: testRemoteComments(t, 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	threads := []RemoteThread{thread}
	cause := errors.New("stop final remote comment validation")
	checks := 0
	next, err := testSnapshot(t, 1, "one.go").TakeRemoteThreadsCancellable(func() error {
		checks++
		if checks == 6 {
			return cause
		}
		return nil
	}, threads)
	if !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
	if checks != 6 || next.RemoteThreadCount() != 0 {
		t.Fatalf("checks=%d threads=%d, want 6 and 0", checks, next.RemoteThreadCount())
	}
	if threads[0].mapping != 0 {
		t.Fatal("cancelled content validation partially installed path mapping")
	}
}

func testRemoteComments(t *testing.T, count int) []RemoteComment {
	t.Helper()
	comments := make([]RemoteComment, count)
	for index := range comments {
		comment, err := TakeRemoteComment(RemoteCommentDescriptor{
			ID:        "comment-" + string(rune('0'+index)),
			Body:      "body",
			CreatedAt: "created",
			URL:       "https://github.com/comment",
		})
		if err != nil {
			t.Fatal(err)
		}
		comments[index] = comment
	}
	return comments
}
