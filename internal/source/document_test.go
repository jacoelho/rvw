package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func closeTestRevision(tb testing.TB, revision *Revision) {
	tb.Helper()
	if err := revision.Retire().Close(); err != nil {
		tb.Error(err)
	}
}

func initializeCommittedRepositoryAt(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, path, "init", "--quiet")
	runGitTest(t, path, "config", "user.email", "test@example.com")
	runGitTest(t, path, "config", "user.name", "Test")
	writeFile(t, filepath.Join(path, "file"), contents)
	runGitTest(t, path, "add", "file")
	runGitTest(t, path, "commit", "--quiet", "-m", "commit")
}

func TestRevisionRetireTransfersResourcesWithoutInvalidatingOpened(t *testing.T) {
	opened := Opened{local: new(LocalDetails)}
	revision := &Revision{}

	cleanup := revision.Retire()
	if cleanup == nil || !revision.retired {
		t.Fatalf("revision did not transfer resource ownership: %+v", revision)
	}
	if _, ok := opened.Local(); !ok {
		t.Fatal("retiring revision invalidated opened facts")
	}
	if err := cleanup.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cleanup.Close(); err != nil {
		t.Fatalf("repeated detached cleanup: %v", err)
	}
}

func TestDocumentMaterializationUsesPinnedRepositoryAfterPathReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repository")
	initializeCommittedRepositoryAt(t, root, "one")
	writeFile(t, filepath.Join(root, "file"), "one\ntwo\n")
	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, documentRevision, err := service.Open(t.Context(), OpenRequest{Target: WorktreeTarget(), Options: OpenOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRevision(t, documentRevision)

	current, err := change.NewReview(testReviewRequest(document.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	contextLines, _ := change.NewContextLines(3)
	patchContext, _ := change.ContextAround(contextLines)
	current, patchRequest, err := current.RequestPatch(0, patchContext)
	if err != nil {
		t.Fatal(err)
	}

	moved := filepath.Join(parent, "repository-pinned")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	initializeCommittedRepositoryAt(t, root, "replacement")
	writeFile(t, filepath.Join(root, "file"), "replacement\n")

	patch, err := service.LoadPatch(t.Context(), document, documentRevision, patchRequest)
	if err != nil {
		t.Fatal(err)
	}
	if patch.Status != change.PatchReady {
		t.Fatalf("patch after replacement = %+v", patch)
	}
	current, err = current.ApplyPatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	text := patchLinesText(current.PatchLines(0, 0, 100, 4_096))
	if !strings.Contains(text, "+two") || strings.Contains(text, "replacement") {
		t.Fatalf("patch followed replacement repository:\n%s", text)
	}

	line, _ := change.NewLineNumber(1)
	selector, _ := change.NewSyntaxSelector("text")
	operation, _ := change.NewOperationID(2)
	_, syntaxRequest, err := current.RequestSyntax(
		operation,
		0,
		selector,
		[]change.LineNumber{line},
		[]change.LineNumber{line},
	)
	if err != nil {
		t.Fatal(err)
	}
	syntax, err := service.LoadSyntax(t.Context(), document, documentRevision, syntaxRequest)
	if err != nil {
		t.Fatal(err)
	}
	if syntax.Status != change.SyntaxReady {
		t.Fatalf("syntax after replacement = %+v", syntax)
	}
}

func TestLinkedWorktreeMaterializationUsesPinnedCommonObjects(t *testing.T) {
	parent := t.TempDir()
	main := filepath.Join(parent, "main")
	linked := filepath.Join(parent, "linked")
	initializeCommittedRepositoryAt(t, main, "one")
	runGitTest(t, main, "worktree", "add", "-b", "linked-test", linked, "HEAD")
	writeFile(t, filepath.Join(linked, "file"), "one\ntwo\n")
	service := newTestService(t, linked)
	defer closeTestService(t, service)
	document, documentRevision, err := service.Open(t.Context(), OpenRequest{Target: WorktreeTarget(), Options: OpenOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRevision(t, documentRevision)

	objects := filepath.Join(main, ".git", "objects")
	moved := filepath.Join(main, ".git", "objects-pinned")
	if err := os.Rename(objects, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(objects, 0o700); err != nil {
		t.Fatal(err)
	}

	current, err := change.NewReview(testReviewRequest(document.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	line, _ := change.NewLineNumber(1)
	selector, _ := change.NewSyntaxSelector("text")
	operation, _ := change.NewOperationID(1)
	_, request, err := current.RequestSyntax(
		operation,
		0,
		selector,
		[]change.LineNumber{line},
		[]change.LineNumber{line},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.LoadSyntax(t.Context(), document, documentRevision, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != change.SyntaxReady {
		t.Fatalf("linked-worktree syntax after object replacement = %+v", result)
	}
}
