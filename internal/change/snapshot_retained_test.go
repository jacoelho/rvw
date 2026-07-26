package change

import (
	"errors"
	"strings"
	"testing"
)

func TestSnapshotRetainedCacheTracksEveryTransition(t *testing.T) {
	base := newRetainedTestSnapshot(t, 1, 1)
	callerFiles := make([]File, len(base.files), 64)
	copy(callerFiles, base.files)
	var err error
	base, err = NewSnapshot(base.sourceID, callerFiles)
	if err != nil {
		t.Fatal(err)
	}
	if cap(base.files) != len(base.files) {
		t.Fatalf("NewSnapshot owned capacity = %d, want %d", cap(base.files), len(base.files))
	}
	assertSnapshotRetainedCache(t, base)

	detail, err := NewOperationDetail("acquisition unavailable")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := base.WithRemoteThreadFailure(RemoteThreadsUnavailable, detail)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotRetainedCache(t, failed)

	path, err := NewPath([]byte("one.go"))
	if err != nil {
		t.Fatal(err)
	}
	line, err := NewLineNumber(2)
	if err != nil {
		t.Fatal(err)
	}
	pendingComment, err := NewRemoteComment(RemoteCommentDescriptor{
		ID: "PRRC_node", Body: pendingValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishedComment, err := NewRemoteComment(RemoteCommentDescriptor{
		ID: "published", Body: "published",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := NewRemoteThread(RemoteThreadDescriptor{
		ID: "thread", Path: path, Line: new(line), Side: LineNew,
		Comments: []RemoteComment{pendingComment, publishedComment},
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := failed.WithRemoteThreads([]RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotRetainedCache(t, ready)

	pendingUnavailable, err := ready.WithPendingReviewStatus(PendingReviewUnavailable, detail)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotRetainedCache(t, pendingUnavailable)

	pending := newRetainedTestPendingReview(t)
	captured, err := pendingUnavailable.WithPendingReview(pending)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotRetainedCache(t, captured)
	threads := captured.RemoteThreads(0, 1)
	if len(threads) != 1 || threads[0].CommentCount() != 1 {
		t.Fatalf("filtered remote threads = %#v", threads)
	}
	comment, ok := threads[0].Comment(0)
	if !ok || comment != publishedComment {
		t.Fatalf("filtered remote comment = (%#v, %t)", comment, ok)
	}

	failed, err = captured.WithRemoteThreadFailure(RemoteThreadsLimited, detail)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotRetainedCache(t, failed)
	cleared, err := failed.WithPendingReviewStatus(
		PendingReviewAbsent,
		EmptyOperationDetail(),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotRetainedCache(t, cleared)

	ownedComments := make([]RemoteComment, 2, 8)
	copy(ownedComments, thread.comments)
	thread.comments = ownedComments
	ownedThreads := make([]RemoteThread, 1, 8)
	ownedThreads[0] = thread
	taken, err := base.TakeRemoteThreads(ownedThreads)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotRetainedCache(t, taken)

	ownedPendingComments := make([]PendingComment, len(pending.comments), 8)
	copy(ownedPendingComments, pending.comments)
	pending.comments = ownedPendingComments
	taken, err = taken.TakePendingReview(pending)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotRetainedCache(t, taken)
}

func TestSnapshotBuilderFinalCacheIncludesTransferredCapacity(t *testing.T) {
	snapshot := newRetainedTestSnapshot(t, 64, 1)
	path, err := NewPath([]byte("one.go"))
	if err != nil {
		t.Fatal(err)
	}
	if cap(snapshot.files) != 64 {
		t.Fatalf("transferred file capacity = %d, want 64", cap(snapshot.files))
	}
	assertSnapshotRetainedCache(t, snapshot)
	lenBased := int64(len(snapshot.files)*256 + len(path.raw))
	if snapshot.RetainedBytes() <= lenBased {
		t.Fatalf(
			"retained bytes = %d, want transferred capacity above %d",
			snapshot.RetainedBytes(),
			lenBased,
		)
	}
}

func TestSnapshotRetainedCacheAtMaximumFiles(t *testing.T) {
	snapshot := newRetainedTestSnapshot(t, MaxFiles, MaxFiles)
	maximumDetail, err := NewOperationDetail(strings.Repeat("x", int(MaxOperationDetail)))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = snapshot.WithRemoteThreadFailure(RemoteThreadsUnavailable, maximumDetail)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = snapshot.WithPendingReviewStatus(PendingReviewUnavailable, maximumDetail)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotRetainedCache(t, snapshot)

	var retained int64
	allocations := testing.AllocsPerRun(1_000, func() {
		retained = snapshot.RetainedBytes()
	})
	if allocations != 0 {
		t.Fatalf("RetainedBytes allocations = %v, want 0", allocations)
	}
	if retained != snapshotRetainedBytesForTest(snapshot) {
		t.Fatalf("final retained bytes = %d, want %d", retained, snapshotRetainedBytesForTest(snapshot))
	}
}

func TestSnapshotRetainedComponentCacheHonorsMaximum(t *testing.T) {
	cause := errors.New("test snapshot cache")
	const previous int64 = 64
	total := MaxSnapshotRetainedBytes - 1
	exact, err := replaceSnapshotRetainedComponent(total, previous, previous+1, cause)
	if err != nil {
		t.Fatalf("exact maximum rejected: %v", err)
	}
	if exact != MaxSnapshotRetainedBytes {
		t.Fatalf("exact maximum = %d, want %d", exact, MaxSnapshotRetainedBytes)
	}
	if _, err := (Snapshot{retainedBytes: exact}).validateRetainedBytes(cause); err != nil {
		t.Fatalf("exact maximum validation: %v", err)
	}
	over, err := replaceSnapshotRetainedComponent(
		total,
		previous,
		previous+2,
		cause,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Snapshot{retainedBytes: over}).validateRetainedBytes(cause); !errors.Is(err, cause) {
		t.Fatalf("one-byte-over error = %v", err)
	}
}

func newRetainedTestSnapshot(t *testing.T, capacity, count int) Snapshot {
	t.Helper()
	sourceID, err := NewSourceID(1)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewSnapshotBuilder(sourceID, capacity)
	if err != nil {
		t.Fatal(err)
	}
	path, err := NewPath([]byte("one.go"))
	if err != nil {
		t.Fatal(err)
	}
	change, err := NewUnclassifiedChange(ChangeAdded, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := NewFlags(FileTraits{}, HiddenReasons{})
	if err != nil {
		t.Fatal(err)
	}
	for range count {
		if _, err := builder.AddFile(nil, &path, change, flags); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func newRetainedTestPendingReview(t *testing.T) PendingReview {
	t.Helper()
	path, err := NewPath([]byte("one.go"))
	if err != nil {
		t.Fatal(err)
	}
	commit, err := NewObjectID(strings.Repeat("1", 40))
	if err != nil {
		t.Fatal(err)
	}
	line, err := NewLineNumber(2)
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewPendingCommentTarget(PendingCommentTargetDescriptor{
		Path: path, Commit: commit, OriginalCommit: commit, Line: new(line),
		Subject: PendingSubjectLine, Side: LineNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	commentNode, err := NewCommentNodeID("PRRC_node")
	if err != nil {
		t.Fatal(err)
	}
	commentDatabase, err := NewCommentDatabaseID(2)
	if err != nil {
		t.Fatal(err)
	}
	comment, err := NewPendingComment(PendingCommentDescriptor{
		NodeID: commentNode, DatabaseID: commentDatabase, Body: pendingValue, Target: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewNode, err := NewReviewNodeID("PRR_node")
	if err != nil {
		t.Fatal(err)
	}
	reviewDatabase, err := NewReviewDatabaseID(1)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: reviewNode, DatabaseID: reviewDatabase, Commit: commit,
		Body: summaryValue, Comments: []PendingComment{comment},
	})
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

func assertSnapshotRetainedCache(t *testing.T, snapshot Snapshot) {
	t.Helper()
	total, remote, pending := snapshotRetainedComponentsForTest(snapshot)
	if snapshot.RetainedBytes() != total {
		t.Fatalf("snapshot retained cache = %d, recomputed %d", snapshot.RetainedBytes(), total)
	}
	if snapshot.remoteRetainedBytes != remote {
		t.Fatalf("remote retained cache = %d, recomputed %d", snapshot.remoteRetainedBytes, remote)
	}
	if snapshot.pendingRetainedBytes != pending {
		t.Fatalf("pending retained cache = %d, recomputed %d", snapshot.pendingRetainedBytes, pending)
	}
}

func snapshotRetainedBytesForTest(snapshot Snapshot) int64 {
	total, _, _ := snapshotRetainedComponentsForTest(snapshot)
	return total
}

func snapshotRetainedComponentsForTest(snapshot Snapshot) (int64, int64, int64) {
	const (
		fileSlotBytes             int64 = 256
		remoteThreadSlotBytes     int64 = 96
		remoteThreadPositionBytes int64 = 24
		remoteCommentSlotBytes    int64 = 96
		remoteAnchorEntryBytes    int64 = 32
		pendingReviewHeaderBytes  int64 = 64
		pendingCommentSlotBytes   int64 = 256
	)
	base := int64(cap(snapshot.files))*fileSlotBytes + snapshot.pathProjection.metadataBytes
	for _, file := range snapshot.files {
		if file.oldPath != nil {
			base += int64(len(file.oldPath.raw))
		}
		if file.newPath != nil {
			base += int64(len(file.newPath.raw))
		}
	}

	remote := int64(cap(snapshot.remoteThreads))*remoteThreadSlotBytes +
		int64(cap(snapshot.remoteThreadPositions))*remoteThreadPositionBytes +
		int64(len(snapshot.remoteCommentAnchors))*remoteAnchorEntryBytes +
		snapshot.remoteThreadDetail.Bytes()
	for _, thread := range snapshot.remoteThreads {
		remote += int64(len(thread.id)+len(thread.path.raw)) +
			int64(cap(thread.comments))*remoteCommentSlotBytes
		for _, comment := range thread.comments {
			remote += int64(
				len(comment.id) + len(comment.author) + len(comment.body) +
					len(comment.createdAt) + len(comment.url),
			)
		}
	}

	pending := snapshot.pendingReviewDetail.Bytes()
	if snapshot.pendingReview != nil {
		review := snapshot.pendingReview
		pending += int64(len(review.nodeID)+len(review.commit)+len(review.body)) +
			pendingReviewHeaderBytes + int64(cap(review.comments))*pendingCommentSlotBytes
		for _, comment := range review.comments {
			pending += int64(
				len(comment.body) + len(comment.nodeID) + len(comment.target.path.raw) +
					len(comment.target.commit) + len(comment.target.originalCommit) +
					len(comment.target.replyTo),
			)
		}
	}
	return base + remote + pending, remote, pending
}
