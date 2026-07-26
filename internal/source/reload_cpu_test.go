package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReloadUsesSynchronousCPUAdmission(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\n")
	runGitTest(t, root, "add", "tracked.txt")
	runGitTest(t, root, "commit", "-m", "first")
	writeFile(t, filepath.Join(root, "tracked.txt"), "two\n")

	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, documentRevision, err := service.Open(t.Context(), OpenRequest{Target: WorktreeTarget(), Options: OpenOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRevision(t, documentRevision)

	active := false
	calls := 0
	_, reloadedRevision, err := service.Reload(
		t.Context(), document, documentRevision,

		document.Options(),
		func(ctx context.Context, work func() error) error {
			if active {
				t.Fatal("CPU admission re-entered synchronously")
			}
			if err := context.Cause(ctx); err != nil {
				return err
			}
			active = true
			calls++
			err := work()
			active = false
			return err
		})

	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRevision(t, reloadedRevision)
	if calls < 3 {
		t.Fatalf("CPU admissions = %d, want snapshot accounting, filter, and construction", calls)
	}
}

func TestReloadCPUAdmissionFailureCleansPrivateWorkspace(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\n")
	runGitTest(t, root, "add", "tracked.txt")
	runGitTest(t, root, "commit", "-m", "first")
	writeFile(t, filepath.Join(root, "tracked.txt"), "two\n")

	workspaceRoot := t.TempDir()
	t.Setenv("TMPDIR", workspaceRoot)
	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, documentRevision, err := service.Open(t.Context(), OpenRequest{Target: WorktreeTarget(), Options: OpenOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRevision(t, documentRevision)
	before := directoryNames(t, workspaceRoot)

	cause := errors.New("reject reload CPU admission")
	calls := 0
	reloaded, _, err := service.Reload(
		t.Context(), document, documentRevision,

		document.Options(),
		func(context.Context, func() error) error {
			calls++
			return cause
		})

	if reloaded.Snapshot().SourceID() != 0 || !errors.Is(err, cause) {
		t.Fatalf("document=%v error=%v", reloaded, err)
	}
	if calls != 1 {
		t.Fatalf("CPU admission calls = %d, want 1", calls)
	}
	after := directoryNames(t, workspaceRoot)
	if len(after) != len(before) {
		t.Fatalf("private workspaces before=%v after=%v", before, after)
	}
	for name := range before {
		if _, exists := after[name]; !exists {
			t.Fatalf("original private workspace %q was removed", name)
		}
	}
}

func directoryNames(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		names[entry.Name()] = struct{}{}
	}
	return names
}
