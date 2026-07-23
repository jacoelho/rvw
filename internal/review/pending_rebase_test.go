package review

import (
	"errors"
	"testing"
	"unsafe"
)

func TestPendingRebaseSourceIsNarrowConstantTimeAndFullyCharged(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}

	var source PendingRebaseSource
	if allocations := testing.AllocsPerRun(100, func() {
		source = current.PendingRebaseSource()
	}); allocations != 0 {
		t.Fatalf("PendingRebaseSource allocations = %f, want 0", allocations)
	}
	wantRetained := current.retainedBytes +
		current.snapshot.remoteRetainedBytes +
		current.snapshot.pendingReviewDetail.Bytes() +
		current.snapshot.pathProjection.retainedBytes() +
		pendingRebaseSourceMetadataBytes
	if !source.Ready() || source.RetainedBytes() != wantRetained {
		t.Fatalf(
			"source = ready %t retained %d, want true and %d",
			source.Ready(),
			source.RetainedBytes(),
			wantRetained,
		)
	}
	if source.Workspace() != current.workspace ||
		source.Source() != current.snapshot.SourceID() ||
		source.CommentRevision() != current.revision {
		t.Fatalf(
			"source fence = workspace %d source %d comments %d",
			source.Workspace(),
			source.Source(),
			source.CommentRevision(),
		)
	}
	if pendingRebaseSourceMetadataBytes < int64(unsafe.Sizeof(PendingRebaseSource{})) {
		t.Fatalf(
			"source metadata charge = %d, layout = %d",
			pendingRebaseSourceMetadataBytes,
			unsafe.Sizeof(PendingRebaseSource{}),
		)
	}
}

func TestPreparePendingRebaseBuildsDeltaAndApplyIsAtomicConstantTime(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	remote := *current.snapshot.pendingReview
	remote.body = "remote summary"
	remote.comments = append([]PendingComment(nil), remote.comments...)
	remote.comments[0].body = "remote inline"

	source := current.PendingRebaseSource()
	prepared, err := PreparePendingRebase(source, remote)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.RetainedBytes() == 0 ||
		prepared.RetainedBytes() > source.ConstructionBytes(remote) {
		t.Fatalf(
			"prepared retained = %d, construction = %d",
			prepared.RetainedBytes(),
			source.ConstructionBytes(remote),
		)
	}
	if preparedPendingRebaseMetadataBytes < int64(unsafe.Sizeof(PreparedPendingRebase{})) {
		t.Fatalf(
			"prepared metadata charge = %d, layout = %d",
			preparedPendingRebaseMetadataBytes,
			unsafe.Sizeof(PreparedPendingRebase{}),
		)
	}

	files := unsafe.SliceData(current.snapshot.files)
	aliases := unsafe.SliceData(current.snapshot.pathProjection.aliases)
	var next Review
	var applyErr error
	if allocations := testing.AllocsPerRun(100, func() {
		next, applyErr = current.ApplyPendingRebase(prepared)
	}); allocations != 0 {
		t.Fatalf("ApplyPendingRebase allocations = %f, want 0", allocations)
	}
	if applyErr != nil {
		t.Fatal(applyErr)
	}
	if unsafe.SliceData(next.snapshot.files) != files ||
		unsafe.SliceData(next.snapshot.pathProjection.aliases) != aliases {
		t.Fatal("pending delta replaced base file or path-projection ownership")
	}
	if next.snapshot.SourceID() != current.snapshot.SourceID() ||
		next.workspace != current.workspace ||
		next.revision != current.revision+1 ||
		next.state != current.state+1 {
		t.Fatalf(
			"applied fences = workspace %d source %d comments %d state %d",
			next.workspace,
			next.snapshot.SourceID(),
			next.revision,
			next.state,
		)
	}
	if summary := next.PendingReviewSummary(); summary.Status != PendingReviewReady || summary.Dirty != 0 || summary.Conflicts != 0 {
		t.Fatalf("pending summary = %#v", summary)
	}
	if summary, ok := next.CommentAt(0); !ok || summary.Body().String() != "remote summary" {
		t.Fatalf("summary comment = %#v, exists %t", summary, ok)
	}
	if inline, ok := next.CommentAt(1); !ok || inline.Body().String() != "remote inline" {
		t.Fatalf("inline comment = %#v, exists %t", inline, ok)
	}
	if next.publication.pendingRefresh {
		t.Fatal("successful pending rebase retained a stale-refresh requirement")
	}
	assertSnapshotRetainedCache(t, next.snapshot)
}

func TestPreparePendingRebaseRebuildsCommentAnchorsAfterRemoteMerge(t *testing.T) {
	lineTwo, _ := NewLineNumber(2)
	lineThree, _ := NewLineNumber(3)
	path, _ := NewPath([]byte("one.go"))
	commit, _ := NewObjectID("1111111111111111111111111111111111111111")
	targetThree, err := NewPendingCommentTarget(PendingCommentTargetDescriptor{
		Path: path, Commit: commit, OriginalCommit: commit, Line: new(lineThree),
		Subject: PendingSubjectLine, Side: LineNew,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name               string
		modify             func(*testing.T, *PendingReview)
		wantTwo, wantThree uint16
	}{
		{
			name: "add",
			modify: func(t *testing.T, remote *PendingReview) {
				t.Helper()
				node, _ := NewCommentNodeID("added_node")
				database, _ := NewCommentDatabaseID(1<<40 + 2)
				added, err := NewPendingComment(PendingCommentDescriptor{
					NodeID: node, DatabaseID: database, Body: "added", Target: targetThree,
					Capabilities: editablePendingCapabilities(t),
				})
				if err != nil {
					t.Fatal(err)
				}
				remote.comments = append(remote.comments, added)
			},
			wantTwo: 1, wantThree: 1,
		},
		{
			name: "remove",
			modify: func(_ *testing.T, remote *PendingReview) {
				remote.comments = nil
			},
		},
		{
			name: "retarget",
			modify: func(_ *testing.T, remote *PendingReview) {
				remote.comments[0].target = targetThree
			},
			wantThree: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
			if err != nil {
				t.Fatal(err)
			}
			remote := *current.snapshot.pendingReview
			remote.comments = append([]PendingComment(nil), remote.comments...)
			test.modify(t, &remote)
			prepared, err := PreparePendingRebase(current.PendingRebaseSource(), remote)
			if err != nil {
				t.Fatal(err)
			}
			next, err := current.ApplyPendingRebase(prepared)
			if err != nil {
				t.Fatal(err)
			}
			two := commentAnchor{file: 0, side: LineNew, line: lineTwo}
			three := commentAnchor{file: 0, side: LineNew, line: lineThree}
			if next.commentAnchors[two] != test.wantTwo ||
				next.commentAnchors[three] != test.wantThree ||
				next.HasCommentAnchor(0, LineNew, lineTwo) != (test.wantTwo != 0) ||
				next.HasCommentAnchor(0, LineNew, lineThree) != (test.wantThree != 0) {
				t.Fatalf(
					"anchor counts = line 2: %d, line 3: %d; want %d, %d",
					next.commentAnchors[two], next.commentAnchors[three],
					test.wantTwo, test.wantThree,
				)
			}
		})
	}
}

func TestApplyPendingRebaseRejectsEveryStaleFenceAndActiveMutation(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	remote := *current.snapshot.pendingReview
	prepared, err := PreparePendingRebase(current.PendingRebaseSource(), remote)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(Review) Review
	}{
		{name: "workspace", mutate: func(review Review) Review {
			review.workspace++
			return review
		}},
		{name: "source", mutate: func(review Review) Review {
			review.snapshot.sourceID++
			return review
		}},
		{name: "comment revision", mutate: func(review Review) Review {
			review.revision++
			return review
		}},
		{name: "active mutation", mutate: func(review Review) Review {
			review.activeMutation = 9
			review.mutationKind = CommentMutationDelete
			return review
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stale := test.mutate(current)
			next, applyErr := stale.ApplyPendingRebase(prepared)
			if !errors.Is(applyErr, ErrInvalidPendingReview) {
				t.Fatalf("apply error = %v, want %v", applyErr, ErrInvalidPendingReview)
			}
			if next.state != stale.state || next.revision != stale.revision {
				t.Fatal("stale apply changed the review")
			}
		})
	}
}

func TestPreparePendingRebaseCancellableStopsBeforeConstruction(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	remote := *current.snapshot.pendingReview
	cause := errors.New("cancel pending rebase")
	checks := 0
	_, err = PreparePendingRebaseCancellable(func() error {
		checks++
		return cause
	}, current.PendingRebaseSource(), remote)
	if !errors.Is(err, cause) {
		t.Fatalf("preparation error = %v, want %v", err, cause)
	}
	if checks != 1 {
		t.Fatalf("cancellation checks = %d, want 1", checks)
	}
}

func TestPendingCommentIndexCancellationReturnsMidIndexCause(t *testing.T) {
	comments := make([]Comment, 300)
	for index := range comments {
		comments[index].id = CommentID(index)
	}
	cause := errors.New("cancel pending comment index")
	checks := 0
	_, err := indexPendingCommentsCancellable(func() error {
		checks++
		if checks == 3 {
			return cause
		}
		return nil
	}, comments)
	if !errors.Is(err, cause) {
		t.Fatalf("index cancellation = %v, want %v", err, cause)
	}
	if checks != 3 {
		t.Fatalf("index cancellation checks = %d, want 3", checks)
	}
}

func TestPendingRebaseSourceRejectsActiveMutation(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	current.activeMutation = 3
	current.mutationKind = CommentMutationSave
	if source := current.PendingRebaseSource(); source.Ready() || source.RetainedBytes() != 0 {
		t.Fatalf("active-mutation source = ready %t retained %d", source.Ready(), source.RetainedBytes())
	}
}
