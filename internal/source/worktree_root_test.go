package source

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestRawWorktreeAncestorSymlinkCannotHashOutsideBlob(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "--quiet")
	if err := os.Mkdir(filepath.Join(repository, "ancestor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideContents := []byte("outside-blob-must-not-be-written\n")
	if err := os.WriteFile(filepath.Join(outside, "path"), outsideContents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "ancestor", "path"), []byte("inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree := openTestRootedRepository(t, repository)
	service := newTestService(t, repository)
	defer closeTestService(t, service)
	workspace := t.TempDir()
	entries := []rawWorktreeEntry{{path: "ancestor/path", mode: review.FileMode("100644")}}
	_, err := service.hashRawWorktreeEntriesWith(
		t.Context(),
		worktree,
		nil,
		workspace,
		entries,
		func(root *os.Root, relative string, mode review.FileMode) (rawWorktreeCapture, error) {
			if err := os.Rename(
				filepath.Join(repository, "ancestor"),
				filepath.Join(repository, "ancestor-original"),
			); err != nil {
				return rawWorktreeCapture{}, err
			}
			if err := os.Symlink(outside, filepath.Join(repository, "ancestor")); err != nil {
				return rawWorktreeCapture{}, err
			}
			return openRawWorktreePath(root, relative, mode)
		},
	)
	if err == nil {
		t.Fatal("outside-root ancestor symlink was accepted")
	}
	staged, readErr := os.ReadDir(filepath.Join(workspace, "raw-blobs"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(staged) != 0 {
		t.Fatalf("raw staging retained %d files after rejected ancestor swap", len(staged))
	}
	oid := gitObjectID(t, repository, outsideContents)
	command := exec.CommandContext(t.Context(), "git", "cat-file", "-e", oid)
	command.Dir = repository
	if command.Run() == nil {
		t.Fatalf("outside blob %s was written to repository object storage", oid)
	}
}

func TestRawWorktreeAncestorSwapAfterLstatCannotOpenOutsideFile(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(repository, "ancestor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "ancestor", "path"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "path"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(repository)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	_, err = openRawWorktreePathWith(
		root,
		"ancestor/path",
		review.FileMode("100644"),
		func(root *os.Root, relative string) (*os.File, error) {
			if err := os.Rename(
				filepath.Join(repository, "ancestor"),
				filepath.Join(repository, "ancestor-original"),
			); err != nil {
				return nil, err
			}
			if err := os.Symlink(outside, filepath.Join(repository, "ancestor")); err != nil {
				return nil, err
			}
			return openRawWorktreeRegular(root, relative)
		},
	)
	if err == nil {
		t.Fatal("outside-root ancestor swap after lstat was accepted")
	}
}

func TestWorktreeGitlinkAncestorSymlinkCannotResolveOutsideOID(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	insideParent := filepath.Join(repository, "ancestor")
	inside := filepath.Join(insideParent, "gitlink")
	outsideParent := filepath.Join(parent, "outside")
	outside := filepath.Join(outsideParent, "gitlink")
	initializeCommittedRepositoryAt(t, repository, "root")
	initializeCommittedRepositoryAt(t, inside, "inside")
	initializeCommittedRepositoryAt(t, outside, "outside")
	outsideOID := strings.TrimSpace(runGitTest(t, outside, "rev-parse", gitHeadReference))
	worktree := openTestRootedRepository(t, repository)
	if err := os.Rename(insideParent, filepath.Join(repository, "ancestor-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideParent, insideParent); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, repository)
	defer closeTestService(t, service)
	oid, err := service.worktreeGitlinkHead(t.Context(), worktree, nil, "ancestor/gitlink")
	if err == nil {
		t.Fatalf("outside-root gitlink resolved as %s", oid)
	}
	if oid.String() == outsideOID {
		t.Fatalf("outside gitlink OID %s was accepted", outsideOID)
	}
}

func TestRootedWorktreeCommandsIgnoreRepositoryPathReplacement(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	moved := filepath.Join(parent, "repository-original")
	initializeCommittedRepositoryAt(t, repository, "original")
	originalOID := strings.TrimSpace(runGitTest(t, repository, "rev-parse", gitHeadReference))
	worktree := openTestRootedRepository(t, repository)
	if err := os.Rename(repository, moved); err != nil {
		t.Fatal(err)
	}
	initializeCommittedRepositoryAt(t, repository, "replacement")
	replacementOID := strings.TrimSpace(runGitTest(t, repository, "rev-parse", gitHeadReference))
	if replacementOID == originalOID {
		t.Fatal("test repositories unexpectedly have the same HEAD")
	}
	service := newTestService(t, repository)
	defer closeTestService(t, service)
	result, err := service.runRootedGit(
		t.Context(), worktree, nil, []string{"rev-parse", gitHeadReference}, nil, 128,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(result.Stdout)); got != originalOID {
		t.Fatalf("rooted command HEAD = %s, want original %s; replacement is %s", got, originalOID, replacementOID)
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
	if err := os.WriteFile(filepath.Join(path, "file"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, path, "add", "file")
	runGitTest(t, path, "commit", "--quiet", "-m", "commit")
}

func gitObjectID(t *testing.T, directory string, contents []byte) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", "hash-object", "--stdin")
	command.Dir = directory
	command.Stdin = strings.NewReader(string(contents))
	output, err := command.Output()
	if err != nil {
		t.Fatalf("compute test blob identity: %v", err)
	}
	return strings.TrimSpace(string(output))
}

func newOpenTestRoot(t *testing.T) (string, *os.Root) {
	t.Helper()
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return directory, root
}

func openTestRootedRepository(t *testing.T, path string) *rootedRepository {
	t.Helper()
	repository, err := openRootedRepository(RepositoryPath(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Error(err)
		}
	})
	return repository
}
