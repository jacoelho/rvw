package review

import "testing"

func TestPendingTombstoneAnchorsSurviveFullRebuilds(t *testing.T) {
	tests := []struct {
		name    string
		rebuild func(*testing.T, Review) Review
	}{
		{
			name: "reload",
			rebuild: func(t *testing.T, current Review) Review {
				t.Helper()
				pending, _ := current.snapshot.PendingReview()
				next, _, err := current.ReplaceSnapshot(pendingAnchorSnapshot(t, 2, pending))
				if err != nil {
					t.Fatal(err)
				}
				return next
			},
		},
		{
			name: "pending rebase",
			rebuild: func(t *testing.T, current Review) Review {
				t.Helper()
				remote := *current.snapshot.pendingReview
				prepared, err := PreparePendingRebase(current.PendingRebaseSource(), remote)
				if err != nil {
					t.Fatal(err)
				}
				next, err := current.ApplyPendingRebase(prepared)
				if err != nil {
					t.Fatal(err)
				}
				return next
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current, pendingID, anchor := pendingReviewWithSharedAnchor(t)
			current, err := current.DeleteComment(pendingID)
			if err != nil {
				t.Fatal(err)
			}
			assertCommentAnchorCount(t, current, anchor, 1)

			next := test.rebuild(t, current)
			if state := next.PendingBindingState(pendingID); state != PendingBindingDeleted {
				t.Fatalf("binding state = %s, want %s", state, PendingBindingDeleted)
			}
			assertCommentAnchorCount(t, next, anchor, 1)
		})
	}
}

func TestPendingTombstoneEditDeleteAndRepeatKeepExactSharedAnchorCount(t *testing.T) {
	current, pendingID, anchor := pendingReviewWithSharedAnchor(t)
	current, err := current.DeleteComment(pendingID)
	if err != nil {
		t.Fatal(err)
	}
	assertCommentAnchorCount(t, current, anchor, 1)

	current, err = current.BeginEditComment(pendingID)
	if err != nil {
		t.Fatal(err)
	}
	assertCommentAnchorCount(t, current, anchor, 2)
	current, err = current.CancelDraft()
	if err != nil {
		t.Fatal(err)
	}

	current, err = current.DeleteComment(pendingID)
	if err != nil {
		t.Fatal(err)
	}
	assertCommentAnchorCount(t, current, anchor, 1)
	current, err = current.DeleteComment(pendingID)
	if err != nil {
		t.Fatal(err)
	}
	assertCommentAnchorCount(t, current, anchor, 1)
}

func TestPendingConflictResolutionMaintainsSharedAnchorOwnership(t *testing.T) {
	tests := []struct {
		name         string
		scenario     pendingAnchorConflictScenario
		resolution   PendingConflictResolution
		wantBody     string
		wantState    PendingBindingState
		wantExists   bool
		wantAnchored uint16
	}{
		{
			name: "deleted local versus live remote chooses local", scenario: anchorConflictDeletedRemoteLive,
			resolution: PendingConflictLocal, wantBody: "inline", wantState: PendingBindingDeleted,
			wantExists: true, wantAnchored: 1,
		},
		{
			name: "deleted local versus live remote chooses remote", scenario: anchorConflictDeletedRemoteLive,
			resolution: PendingConflictRemote, wantBody: "remote", wantState: PendingBindingClean,
			wantExists: true, wantAnchored: 2,
		},
		{
			name: "deleted local versus live remote chooses base", scenario: anchorConflictDeletedRemoteLive,
			resolution: PendingConflictBase, wantBody: "inline", wantState: PendingBindingModified,
			wantExists: true, wantAnchored: 2,
		},
		{
			name: "live local versus absent remote chooses local", scenario: anchorConflictLiveRemoteAbsent,
			resolution: PendingConflictLocal, wantBody: "local", wantState: PendingBindingNew,
			wantExists: true, wantAnchored: 2,
		},
		{
			name: "live local versus absent remote chooses remote", scenario: anchorConflictLiveRemoteAbsent,
			resolution: PendingConflictRemote, wantState: PendingBindingNew,
			wantExists: false, wantAnchored: 1,
		},
		{
			name: "live local versus absent remote chooses base", scenario: anchorConflictLiveRemoteAbsent,
			resolution: PendingConflictBase, wantBody: "inline", wantState: PendingBindingNew,
			wantExists: true, wantAnchored: 2,
		},
		{
			name: "live addition versus live remote chooses local", scenario: anchorConflictLiveBaseAbsent,
			resolution: PendingConflictLocal, wantBody: "local", wantState: PendingBindingModified,
			wantExists: true, wantAnchored: 2,
		},
		{
			name: "live addition versus live remote chooses remote", scenario: anchorConflictLiveBaseAbsent,
			resolution: PendingConflictRemote, wantBody: "remote", wantState: PendingBindingClean,
			wantExists: true, wantAnchored: 2,
		},
		{
			name: "live addition versus live remote chooses absent base", scenario: anchorConflictLiveBaseAbsent,
			resolution: PendingConflictBase, wantBody: "local", wantState: PendingBindingDeleted,
			wantExists: true, wantAnchored: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current, pendingID, anchor := pendingReviewWithSharedAnchor(t)
			current = installPendingAnchorConflict(t, current, pendingID, test.scenario)

			next, err := current.ResolvePendingConflict(pendingID, test.resolution)
			if err != nil {
				t.Fatal(err)
			}
			comment, exists := next.commentByID(pendingID)
			if exists != test.wantExists {
				t.Fatalf("comment exists = %t, want %t", exists, test.wantExists)
			}
			if exists && comment.Body().String() != test.wantBody {
				t.Fatalf("comment body = %q, want %q", comment.Body().String(), test.wantBody)
			}
			if state := next.PendingBindingState(pendingID); state != test.wantState {
				t.Fatalf("binding state = %s, want %s", state, test.wantState)
			}
			assertCommentAnchorCount(t, next, anchor, test.wantAnchored)
		})
	}
}

func TestPendingTombstoneRemoteDeletionDoesNotDecrementLiveSibling(t *testing.T) {
	current, pendingID, anchor := pendingReviewWithSharedAnchor(t)
	current, err := current.DeleteComment(pendingID)
	if err != nil {
		t.Fatal(err)
	}
	assertCommentAnchorCount(t, current, anchor, 1)

	pending, _ := current.snapshot.PendingReview()
	remote, err := NewPendingReview(PendingReviewDescriptor{
		NodeID: pending.NodeID(), DatabaseID: pending.DatabaseID(), Commit: pending.Commit(),
		Body: pending.Body(), ViewerUpdate: pending.ViewerCanUpdate(),
	})
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := current.ReplaceSnapshot(pendingAnchorSnapshot(t, 2, remote))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := next.commentByID(pendingID); exists {
		t.Fatal("remotely deleted tombstone was retained")
	}
	assertCommentAnchorCount(t, next, anchor, 1)
}

type pendingAnchorConflictScenario uint8

const (
	anchorConflictDeletedRemoteLive pendingAnchorConflictScenario = iota + 1
	anchorConflictLiveRemoteAbsent
	anchorConflictLiveBaseAbsent
)

func installPendingAnchorConflict(
	t *testing.T,
	current Review,
	pendingID CommentID,
	scenario pendingAnchorConflictScenario,
) Review {
	t.Helper()
	comment, exists := current.commentByID(pendingID)
	if !exists {
		t.Fatal("pending comment is unavailable")
	}
	kind := comment.kind
	base := commentContent{kind: kind, body: "inline"}
	local := commentContent{kind: kind, body: "local"}
	remote := commentContent{kind: kind, body: "remote"}

	var err error
	switch scenario {
	case anchorConflictDeletedRemoteLive:
		current, err = current.DeleteComment(pendingID)
	case anchorConflictLiveRemoteAbsent, anchorConflictLiveBaseAbsent:
		current = editTestComment(t, current, pendingID, local.body)
	default:
		t.Fatalf("unsupported conflict scenario %d", scenario)
	}
	if err != nil {
		t.Fatal(err)
	}

	binding := current.pending.bindings[pendingID]
	switch scenario {
	case anchorConflictDeletedRemoteLive:
		binding.baseline = cloneCommentContent(remote)
		binding.conflict = &pendingConflict{
			base: base, local: base, remote: remote, localDeleted: true,
		}
	case anchorConflictLiveRemoteAbsent:
		binding.baseline = cloneCommentContent(base)
		binding.conflict = &pendingConflict{base: base, local: local, remote: commentContent{kind: kind}}
	case anchorConflictLiveBaseAbsent:
		binding.baseline = cloneCommentContent(remote)
		binding.conflict = &pendingConflict{
			base: commentContent{kind: kind}, local: local, remote: remote,
		}
	}
	current.pending.bindings[pendingID] = binding
	current.refresh()
	return current
}

func pendingReviewWithSharedAnchor(t *testing.T) (Review, CommentID, commentAnchor) {
	t.Helper()
	pendingSnapshot := pendingTestSnapshot(t)
	remoteReview, _ := pendingSnapshot.PendingReview()
	current, err := NewReview(testReviewRequest(pendingAnchorSnapshot(t, 1, remoteReview)))
	if err != nil {
		t.Fatal(err)
	}
	pending, exists := current.CommentAt(1)
	if !exists {
		t.Fatal("pending line comment is unavailable")
	}
	current = saveTestComment(t, current, pending.Target(), "live sibling")
	anchor, anchored := targetCommentAnchor(pending.Target())
	if !anchored {
		t.Fatal("pending line comment has no anchor")
	}
	assertCommentAnchorCount(t, current, anchor, 2)
	return current, pending.ID(), anchor
}

func pendingAnchorSnapshot(t *testing.T, source uint64, pending PendingReview) Snapshot {
	t.Helper()
	sourceID, err := NewSourceID(source)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewSnapshotBuilder(sourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := NewPath([]byte("one.go"))
	change, _ := NewUnclassifiedChange(ChangeModified, 1, 1)
	flags, _ := NewFlags(FileTraits{}, HiddenReasons{})
	contents, err := NewFileContents(
		OldFileContent(pending.Commit()),
		NewFileContent(pending.Commit()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.AddFileWithContents(&path, &path, change, flags, contents); err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = snapshot.WithPendingReview(pending)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertCommentAnchorCount(
	t *testing.T,
	current Review,
	anchor commentAnchor,
	want uint16,
) {
	t.Helper()
	if got := current.commentAnchors[anchor]; got != want {
		t.Fatalf("anchor count = %d, want %d", got, want)
	}
	if got := current.HasCommentAnchor(anchor.file, anchor.side, anchor.line); got != (want != 0) {
		t.Fatalf("anchor presence = %t, want %t", got, want != 0)
	}
}
