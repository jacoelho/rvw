package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

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

func TestDocumentDetachCleanupReleasesGraphAndTransfersWorkspace(t *testing.T) {
	document := &Document{
		local: new(LocalDetails),
	}

	cleanup := document.DetachCleanup()
	if cleanup == nil || document.local != nil || document.revision.repository != nil || !document.closed {
		t.Fatalf("detached document retained source ownership: %+v", document)
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
	document, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()

	current, err := review.NewReview(testReviewRequest(document.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	contextLines, _ := review.NewContextLines(3)
	patchContext, _ := review.ContextAround(contextLines)
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

	patch, err := service.LoadPatch(t.Context(), document, patchRequest)
	if err != nil {
		t.Fatal(err)
	}
	if patch.Status != review.PatchReady {
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

	line, _ := review.NewLineNumber(1)
	selector, _ := review.NewSyntaxSelector("text")
	operation, _ := review.NewOperationID(2)
	_, syntaxRequest, err := current.RequestSyntax(
		operation,
		0,
		selector,
		[]review.LineNumber{line},
		[]review.LineNumber{line},
	)
	if err != nil {
		t.Fatal(err)
	}
	syntax, err := service.LoadSyntax(t.Context(), document, syntaxRequest)
	if err != nil {
		t.Fatal(err)
	}
	if syntax.Status != review.SyntaxReady {
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
	document, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()

	objects := filepath.Join(main, ".git", "objects")
	moved := filepath.Join(main, ".git", "objects-pinned")
	if err := os.Rename(objects, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(objects, 0o700); err != nil {
		t.Fatal(err)
	}

	current, err := review.NewReview(testReviewRequest(document.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	line, _ := review.NewLineNumber(1)
	selector, _ := review.NewSyntaxSelector("text")
	operation, _ := review.NewOperationID(1)
	_, request, err := current.RequestSyntax(
		operation,
		0,
		selector,
		[]review.LineNumber{line},
		[]review.LineNumber{line},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.LoadSyntax(t.Context(), document, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != review.SyntaxReady {
		t.Fatalf("linked-worktree syntax after object replacement = %+v", result)
	}
}
