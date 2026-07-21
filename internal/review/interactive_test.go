package review

import (
	"errors"
	"strings"
	"testing"
)

func TestCommentIdentityDoesNotChangeAfterDeletion(t *testing.T) {
	review := testReview(t, testSnapshot(t, 1, "a.go"))

	var ids []CommentID
	for range 3 {
		target, _ := FileCommentTarget(0)
		var err error
		review, err = review.BeginDraft(target)
		if err != nil {
			t.Fatal(err)
		}
		review, err = review.UpdateDraft("body")
		if err != nil {
			t.Fatal(err)
		}
		var id CommentID
		review, id, err = review.SaveDraft()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	var err error
	review, err = review.DeleteComment(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	comments := review.Comments()
	if len(comments) != 2 || comments[0].ID() != ids[0] || comments[1].ID() != ids[2] {
		t.Fatalf("comment identities changed after deletion: %+v", comments)
	}
	if _, err := review.DeleteComment(ids[1]); !errors.Is(err, ErrInvalidReviewTransition) {
		t.Fatalf("deleting removed identity error = %v", err)
	}
}

func TestFileSelectionDoesNotActivateUntilExplicitlyRequested(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "a.go", "b.go"))
	next, err := current.SelectFile(1)
	if err != nil {
		t.Fatal(err)
	}
	if selected, _ := next.SelectedFile(); selected != 1 {
		t.Fatalf("selected file = %d, want 1", selected)
	}
	if active, _ := next.ActiveFile(); active != 0 {
		t.Fatalf("moving selection activated file %d", active)
	}
	next, err = next.ActivateSelectedFile()
	if err != nil {
		t.Fatal(err)
	}
	if active, _ := next.ActiveFile(); active != 1 {
		t.Fatalf("activated file = %d, want 1", active)
	}
}

func TestEditingCommentPreservesIdentity(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "a.go"))
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, "before")
	id := current.Comments()[0].ID()

	var err error
	current, err = current.BeginEditComment(id)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("after")
	if err != nil {
		t.Fatal(err)
	}
	var saved CommentID
	current, saved, err = current.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	comment := current.Comments()[0]
	if saved != id || comment.ID() != id || comment.Body().String() != "after" {
		t.Fatalf("edit did not preserve identity: saved=%d comment=%+v", saved, comment)
	}
}

func TestReplaceSnapshotReconcilesByRawPathIdentity(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "a.go", "b.go"))
	var err error
	current, err = current.ActivateFile(0)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.ToggleFileReviewed(0)
	if err != nil {
		t.Fatal(err)
	}

	fileTarget, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, fileTarget, "file")
	line, _ := NewLineNumber(7)
	lineTarget, _ := LineCommentTarget(0, LineNew, line, line)
	current = saveTestComment(t, current, lineTarget, "line")

	replacement := testSnapshot(t, 2, "c.go", "a.go")
	next, report, err := current.ReplaceSnapshot(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if next.WorkspaceRevision().Uint64() != current.WorkspaceRevision().Uint64()+1 {
		t.Fatalf("workspace revision did not advance")
	}
	if got, ok := next.ActiveFile(); !ok || got != 1 || !report.ActivePreserved {
		t.Fatalf("active file was not remapped: %d %v %+v", got, ok, report)
	}
	if next.FileReviewed(1) || next.FileReviewed(0) || report.FileMarksStale != 1 {
		t.Fatalf("unverifiable file review mark was preserved: %+v", report)
	}
	if report.FilesPreserved != 1 || report.FilesRemoved != 1 ||
		report.CommentsStale != 1 || report.CommentsUnmappable != 0 {
		t.Fatalf("unexpected reload report: %+v", report)
	}
	comments := next.Comments()
	if fileID, _ := comments[0].Target().FileID(); fileID != 1 || comments[0].Stale() {
		t.Fatalf("file comment was not remapped: %+v", comments[0])
	}
	if fileID, _ := comments[1].Target().FileID(); fileID != 1 || !comments[1].Stale() {
		t.Fatalf("line comment was not remapped stale: %+v", comments[1])
	}
	if next.Patch(1).Status != PatchUnavailable {
		t.Fatalf("replacement retained patch state")
	}
}

func TestReplaceSnapshotPreservesOnlyProvablyIdenticalHunkMarks(t *testing.T) {
	oldObject, _ := NewObjectID(strings.Repeat("1", 40))
	newObject, _ := NewObjectID(strings.Repeat("2", 40))
	current := testReview(
		t,
		testContentSnapshot(t, 1, "a.go", oldObject, newObject),
	)
	contextLines, _ := NewContextLines(3)
	patchContext, _ := ContextAround(contextLines)
	pending, request, err := current.RequestPatch(0, patchContext)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := TakePatch([]byte("@@ -1 +1 @@\n-old\n+new\n"))
	if err != nil {
		t.Fatal(err)
	}
	current, err = pending.ApplyPatch(PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
		Status:    PatchReady,
		Prepared:  prepared,
	})
	if err != nil {
		t.Fatal(err)
	}
	index, _ := NewHunkIndex(0)
	hunk, _ := NewHunkID(0, index)
	current, err = current.ToggleHunkReviewed(hunk)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.ToggleFileReviewed(0)
	if err != nil {
		t.Fatal(err)
	}

	identical, report, err := current.ReplaceSnapshot(
		testContentSnapshot(t, 2, "a.go", oldObject, newObject),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !identical.HunkReviewed(hunk) || !identical.FileReviewed(0) ||
		report.HunkMarksStale != 0 || report.FileMarksStale != 0 {
		t.Fatalf("identical replacement lost hunk mark: %+v", report)
	}

	changedObject, _ := NewObjectID(strings.Repeat("3", 40))
	changed, report, err := current.ReplaceSnapshot(
		testContentSnapshot(t, 3, "a.go", oldObject, changedObject),
	)
	if err != nil {
		t.Fatal(err)
	}
	if changed.HunkReviewed(hunk) || changed.FileReviewed(0) ||
		report.HunkMarksStale != 1 || report.FileMarksStale != 1 {
		t.Fatalf("changed replacement preserved stale hunk mark: %+v", report)
	}
}

func TestReplaceSnapshotPreservesUnmappableCommentPathAndBlocksDraftLoss(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "gone.go"))
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, "comment")

	replacement := testSnapshot(t, 2, "new.go")
	next, report, err := current.ReplaceSnapshot(replacement)
	if err != nil {
		t.Fatal(err)
	}
	comment := next.Comments()[0]
	path, ok := comment.Target().AnchoredPath()
	if !comment.Unmappable() || !ok || path.Display() != "gone.go" ||
		report.CommentsUnmappable != 1 {
		t.Fatalf("unmappable comment lost its anchor: %+v %+v", comment, report)
	}
	if fileID, mapped := comment.Target().FileID(); mapped {
		t.Fatalf("unmappable comment retained colliding file ID %d", fileID)
	}

	current, err = current.BeginDraft(target)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("unsaved")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := current.ReplaceSnapshot(replacement); !errors.Is(err, ErrInvalidReload) {
		t.Fatalf("replacement with unmappable draft error = %v", err)
	}
	if draft, ok := current.Draft(); !ok || draft.Body() != "unsaved" {
		t.Fatalf("failed replacement mutated draft: %+v %v", draft, ok)
	}
}

func TestReplaceSnapshotPreservesLineCommentWhenItsSideIsIdentical(t *testing.T) {
	oldObject, _ := NewObjectID(strings.Repeat("1", 40))
	newObject, _ := NewObjectID(strings.Repeat("2", 40))
	current := testReview(
		t,
		testContentSnapshot(t, 1, "a.go", oldObject, newObject),
	)
	line, _ := NewLineNumber(7)
	target, _ := LineCommentTarget(0, LineNew, line, line)
	current = saveTestComment(t, current, target, "line")

	identical, report, err := current.ReplaceSnapshot(
		testContentSnapshot(t, 2, "a.go", oldObject, newObject),
	)
	if err != nil {
		t.Fatal(err)
	}
	if identical.Comments()[0].Stale() || report.CommentsStale != 0 {
		t.Fatalf("identical line side became stale: %+v", report)
	}
	if !identical.HasCommentAnchor(0, LineNew, line) {
		t.Fatal("identical replacement lost its comment anchor")
	}

	changedObject, _ := NewObjectID(strings.Repeat("3", 40))
	changed, report, err := current.ReplaceSnapshot(
		testContentSnapshot(t, 3, "a.go", oldObject, changedObject),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !changed.Comments()[0].Stale() || report.CommentsStale != 1 {
		t.Fatalf("changed line side remained current: %+v", report)
	}
	if changed.HasCommentAnchor(0, LineNew, line) {
		t.Fatal("stale replacement retained its comment anchor")
	}
}

func TestReplaceSnapshotRequiresIdenticalLineDraftContentAndSupersedesCPUWork(t *testing.T) {
	oldObject, _ := NewObjectID(strings.Repeat("1", 40))
	newObject, _ := NewObjectID(strings.Repeat("2", 40))
	current := testReview(
		t,
		testContentSnapshot(t, 1, "a.go", oldObject, newObject),
	)
	line, _ := NewLineNumber(7)
	target, _ := LineCommentTarget(0, LineNew, line, line)
	var err error
	current, err = current.BeginDraft(target)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft("unsaved")
	if err != nil {
		t.Fatal(err)
	}
	operationID, _ := NewOperationID(1)
	current, err = current.BeginOperation(operationID, OperationSyntax)
	if err != nil {
		t.Fatal(err)
	}

	identical, report, err := current.ReplaceSnapshot(
		testContentSnapshot(t, 2, "a.go", oldObject, newObject),
	)
	if err != nil {
		t.Fatal(err)
	}
	draft, ok := identical.Draft()
	if !ok || draft.Body() != "unsaved" || !report.DraftPreserved {
		t.Fatalf("identical replacement lost draft: %+v %v %+v", draft, ok, report)
	}
	operation := identical.Operations()[operationID]
	if operation.Status != OperationSuperseded ||
		report.OperationsSuperseded != 1 {
		t.Fatalf("syntax operation was not superseded: %+v %+v", operation, report)
	}

	changedObject, _ := NewObjectID(strings.Repeat("3", 40))
	if _, _, err := current.ReplaceSnapshot(
		testContentSnapshot(t, 3, "a.go", oldObject, changedObject),
	); !errors.Is(err, ErrInvalidReload) {
		t.Fatalf("changed line draft replacement error = %v, want ErrInvalidReload", err)
	}
}

func TestReplaceSnapshotBlocksUnresolvedPublication(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "a.go"))
	operationID, _ := NewOperationID(1)
	var err error
	current, err = current.BeginOperation(operationID, OperationPublication)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := current.ReplaceSnapshot(testSnapshot(t, 2, "a.go")); !errors.Is(err, ErrInvalidReload) {
		t.Fatalf("replacement error = %v, want ErrInvalidReload", err)
	}
}

func TestPreparedReplacementIsRevisionFenced(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "a.go"))
	prepared, err := PrepareReplacement(current, testSnapshot(t, 2, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.ToggleFileReviewed(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := current.CommitReplacement(prepared); !errors.Is(
		err,
		ErrStaleReloadPreparation,
	) {
		t.Fatalf("stale commit error = %v", err)
	}
}

func TestReplaceSnapshotReconcilesUnambiguousRenameAliases(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "before.go"))
	target, _ := FileCommentTarget(0)
	current = saveTestComment(t, current, target, "rename")
	current, err := current.ToggleFileReviewed(0)
	if err != nil {
		t.Fatal(err)
	}

	next, report, err := current.ReplaceSnapshot(
		testRenamedSnapshot(t, 2, "before.go", "after.go"),
	)
	if err != nil {
		t.Fatal(err)
	}
	comment := next.Comments()[0]
	path, ok := comment.Target().AnchoredPath()
	if !ok || path.Display() != "after.go" || comment.Unmappable() {
		t.Fatalf("renamed comment = %+v path=%q", comment, path.Display())
	}
	if next.FileReviewed(0) || report.FilesPreserved != 1 || report.FileMarksStale != 1 {
		t.Fatalf("unverifiable renamed review mark/report = %v %+v", next.FileReviewed(0), report)
	}
}

func TestOperationsAreCapacityAndDetailBounded(t *testing.T) {
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	done := mustOperationDetail("done")
	for index := uint64(1); index <= MaxStatusRecords+32; index++ {
		id, _ := NewOperationID(index)
		var err error
		current, err = current.BeginOperation(id, OperationPatch)
		if err != nil {
			t.Fatalf("begin operation %d: %v", index, err)
		}
		current, err = current.ResolveOperation(id, OperationSucceeded, done)
		if err != nil {
			t.Fatalf("resolve operation %d: %v", index, err)
		}
	}
	if count := len(current.Operations()); count != MaxStatusRecords {
		t.Fatalf("retained statuses = %d, want %d", count, MaxStatusRecords)
	}
	oldest, _ := NewOperationID(1)
	if _, exists := current.Operations()[oldest]; exists {
		t.Fatal("oldest settled status was not retired")
	}

}

func saveTestComment(
	t *testing.T,
	current Review,
	target CommentTarget,
	body string,
) Review {
	t.Helper()
	next, err := current.BeginDraft(target)
	if err != nil {
		t.Fatal(err)
	}
	next, err = next.UpdateDraft(body)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err = next.SaveDraft()
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func testReview(t *testing.T, snapshot Snapshot) Review {
	t.Helper()
	current, err := NewReview(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func testSnapshot(t *testing.T, source uint64, paths ...string) Snapshot {
	t.Helper()
	sourceID, err := NewSourceID(source)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewSnapshotBuilder(sourceID, len(paths))
	if err != nil {
		t.Fatal(err)
	}
	flags, _ := NewFlags(FileTraits{}, HiddenReasons{})
	for _, value := range paths {
		path, err := NewPath([]byte(value))
		if err != nil {
			t.Fatal(err)
		}
		change, _ := NewUnclassifiedChange(ChangeAdded, 1, 0)
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

func testRenamedSnapshot(t *testing.T, source uint64, oldName, newName string) Snapshot {
	t.Helper()
	sourceID, err := NewSourceID(source)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewSnapshotBuilder(sourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	oldPath, _ := NewPath([]byte(oldName))
	newPath, _ := NewPath([]byte(newName))
	change, err := NewUnclassifiedChange(ChangeRenamed, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	flags, _ := NewFlags(FileTraits{}, HiddenReasons{})
	if _, err := builder.AddFile(&oldPath, &newPath, change, flags); err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func testContentSnapshot(
	t *testing.T,
	source uint64,
	pathValue string,
	oldObject ObjectID,
	newObject ObjectID,
) Snapshot {
	t.Helper()
	sourceID, err := NewSourceID(source)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewSnapshotBuilder(sourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := NewPath([]byte(pathValue))
	change, _ := NewUnclassifiedChange(ChangeModified, 1, 1)
	flags, _ := NewFlags(FileTraits{}, HiddenReasons{})
	contents, err := NewFileContents(
		OldFileContent(oldObject),
		NewFileContent(newObject),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.AddFileWithContents(
		&path,
		&path,
		change,
		flags,
		contents,
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
