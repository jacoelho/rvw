package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestUniqueWorktreeRepositoryRootsUsesComponentPrefixes(t *testing.T) {
	values := []string{
		"a/b",
		"a-b",
		"a",
		"z/nested",
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := sortWorktreeStrings(t.Context(), ledger, values); err != nil {
		t.Fatal(err)
	}
	unique, roots := uniqueSortedWorktreeRepositoryRoots(values)
	if got := unique; !slices.Equal(got, []string{"a", "a-b", "z/nested"}) {
		t.Fatalf("unique repository roots = %q", got)
	}
	for path, want := range map[string]string{
		"a/b/file":       "a",
		"a-b/file":       "a-b",
		"z/nested/child": "z/nested",
	} {
		root, ok := containingWorktreeRepository(path, roots)
		if !ok || root != want {
			t.Fatalf("containing root for %q = %q, %v; want %q", path, root, ok, want)
		}
	}
}

func TestWorktreeStringOrderingUsesSharedLedgerAndCancellation(t *testing.T) {
	values := []string{"b", "a"}
	exact := int64(len(values)) * (worktreeStringSlotCharge + worktreeRadixStackCharge)
	ledger, err := newSourceLedgerWithLimit(exact, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := sortWorktreeStrings(t.Context(), ledger, values); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(values, []string{"a", "b"}) || ledger.used != 0 {
		t.Fatalf("ordered values=%q used=%d", values, ledger.used)
	}

	ledger, err = newSourceLedgerWithLimit(exact-1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := sortWorktreeStrings(
		t.Context(),
		ledger,
		[]string{"b", "a"},
	); !errors.Is(err, ErrSourceLimited) {
		t.Fatalf("one-byte-over ordering error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ledger, err = newSourceLedgerWithLimit(exact, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := sortWorktreeStrings(
		ctx,
		ledger,
		[]string{"b", "a"},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ordering error = %v", err)
	}
}

func TestWorktreeRadixOrderingMatchesByteLexicalOrder(t *testing.T) {
	values := []string{
		"a/b",
		"a",
		"",
		string([]byte{'a', 0xff}),
		string([]byte{0x80}),
		"a/b/c",
		"a",
		"z",
	}
	want := slices.Clone(values)
	slices.Sort(want)
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := sortWorktreeStrings(t.Context(), ledger, values); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(values, want) {
		t.Fatalf("radix order = %q, want %q", values, want)
	}
}

func TestWorktreeUpdateOrderingRemovesBeforeAdding(t *testing.T) {
	updates := []indexUpdate{
		{path: "z"},
		{path: "b", remove: true},
		{path: "a"},
		{path: "a/b", remove: true},
		{path: "a", remove: true},
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := sortWorktreeUpdates(t.Context(), ledger, updates); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		path   string
		remove bool
	}{
		{path: "a", remove: true},
		{path: "a/b", remove: true},
		{path: "b", remove: true},
		{path: "a"},
		{path: "z"},
	}
	for index, expected := range want {
		if updates[index].path != expected.path || updates[index].remove != expected.remove {
			t.Fatalf("update %d = %+v, want %+v", index, updates[index], expected)
		}
	}
}

func TestWorktreeRadixOrderingCancelsWithinLongCommonPrefix(t *testing.T) {
	prefix := strings.Repeat("x", int(review.MaxPathBytes)-1)
	values := []string{prefix + "b", prefix + "a"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := sortWorktreeStrings(ctx, ledger, values); !errors.Is(err, context.Canceled) {
		t.Fatalf("long-prefix cancellation = %v", err)
	}
}

func TestEmbeddedWorktreePrefixLedgerAcceptsExactLimitAndRejectsOneOver(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootPath, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "a", "b", "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree := openTestRootedRepository(t, rootPath)

	const retained = 3 * embeddedWorktreeStateBytes
	for _, test := range []struct {
		name    string
		limit   int64
		wantErr bool
	}{
		{name: "exact", limit: retained},
		{name: "one over", limit: retained - 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger, err := newSourceLedgerWithLimit(test.limit, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = discoverEmbeddedWorktreeRepositories(
				context.Background(),
				[]string{"a/b/file"},
				ledger,
				directCPUAdmission,
				func(ctx context.Context, path string) (embeddedWorktreeState, error) {
					return (&Service{}).probeEmbeddedWorktreeRepository(
						ctx, worktree, nil, path,
					)
				},
			)
			if test.wantErr != (err != nil) {
				t.Fatalf("error = %v, want error %v", err, test.wantErr)
			}
			if !test.wantErr && ledger.used != retained {
				t.Fatalf("ledger used = %d, want %d", ledger.used, retained)
			}
		})
	}
}
