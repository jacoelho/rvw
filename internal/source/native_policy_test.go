package source

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitignoreRulesShareOrderedFilterAutomaton(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, ".gitignore"), "ignored/*\n!ignored/keep.txt\n/root.txt\n")
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "nested", ".gitignore"), "*.log\n!important.log\n")
	session, err := openRepositorySession(RepositoryPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()
	records := make([]diffRecord, 0, 6)
	for _, path := range []string{
		"ignored/drop.txt", "ignored/keep.txt", "root.txt", "deep/root.txt",
		"nested/deep/value.log", "nested/deep/important.log",
	} {
		records = append(records, diffRecord{newPath: path})
	}
	rules, err := nativeGitignoreRules(t.Context(), session, records)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newCompiledFilterSet(t.Context(), ledger, nil, nil, rules)
	if err != nil {
		t.Fatal(err)
	}
	defer matcher.Close()
	wants := map[string]bool{
		"ignored/drop.txt":          true,
		"ignored/keep.txt":          false,
		"root.txt":                  true,
		"deep/root.txt":             false,
		"nested/deep/value.log":     true,
		"nested/deep/important.log": false,
	}
	for path, want := range wants {
		match, err := matcher.match(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := match.Has(filterMatchGitignore); got != want {
			t.Errorf("path %q gitignored=%v, want %v", path, got, want)
		}
	}
}
