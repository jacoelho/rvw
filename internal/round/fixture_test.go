package round_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/round"
	"github.com/jacoelho/rvw/internal/source"
)

// cleanEnv is the environment of the fixture's own git commands: an allowlist, so variables a test
// sets with t.Setenv to attack the code under test never reach the oracle.
var cleanEnv []string

// TestMain isolates every git command, including those the code under test runs, from the user's
// configuration.
func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	home, err := os.MkdirTemp("", "rvw-round-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(home)
	vars := map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     home,
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_COUNT":    "2",
		"GIT_CONFIG_KEY_0":    "maintenance.auto",
		"GIT_CONFIG_VALUE_0":  "false",
		"GIT_CONFIG_KEY_1":    "gc.auto",
		"GIT_CONFIG_VALUE_1":  "0",
		"GIT_AUTHOR_NAME":     "Test Author",
		"GIT_AUTHOR_EMAIL":    "author@example.com",
		"GIT_AUTHOR_DATE":     "2026-01-02T03:04:05Z",
		"GIT_COMMITTER_NAME":  "Test Committer",
		"GIT_COMMITTER_EMAIL": "committer@example.com",
		"GIT_COMMITTER_DATE":  "2026-01-02T03:04:05Z",
	}
	cleanEnv = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir()}
	for k, v := range vars {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cleanEnv = append(cleanEnv, k+"="+v)
	}
	return m.Run()
}

// repo is a fixture repository driven by the git executable.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &repo{t: t, dir: dir}
	r.git("init", "-q", "-b", "main")
	return r
}

// at returns a fixture for another worktree of the same repository.
func (r *repo) at(dir string) *repo { return &repo{t: r.t, dir: dir} }

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.CommandContext(context.WithoutCancel(r.t.Context()), "git", args...)
	cmd.Dir = r.dir
	cmd.Env = slices.Clone(cleanEnv)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %s in %s: %v: %s", strings.Join(args, " "), r.dir, err, stderr.String())
	}
	return strings.TrimSuffix(stdout.String(), "\n")
}

// gitOK reports whether git succeeds.
func (r *repo) gitOK(args ...string) bool {
	cmd := exec.CommandContext(context.WithoutCancel(r.t.Context()), "git", args...)
	cmd.Dir = r.dir
	cmd.Env = slices.Clone(cleanEnv)
	return cmd.Run() == nil
}

func (r *repo) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "--no-verify", "-m", msg)
}

// open opens the repository with the code under test and closes it at cleanup.
func (r *repo) open() *source.Repo {
	r.t.Helper()
	sr, err := source.Open(r.t.Context(), r.dir)
	if err != nil {
		r.t.Fatalf("Open(%s): %v", r.dir, err)
	}
	r.t.Cleanup(func() {
		if err := sr.Close(); err != nil {
			r.t.Errorf("Close: %v", err)
		}
	})
	return sr
}

var quiet = slog.New(slog.DiscardHandler)

// uncommitted resolves the worktree's uncommitted changes.
func (r *repo) uncommitted(sr *source.Repo) *source.Snapshot {
	r.t.Helper()
	snap, err := sr.Resolve(r.t.Context(), source.Uncommitted(), quiet)
	if err != nil {
		r.t.Fatalf("Resolve uncommitted in %s: %v", r.dir, err)
	}
	return snap
}

// fileIndex returns the index of the changed file shown as path.
func fileIndex(t *testing.T, snap *source.Snapshot, path string) int {
	t.Helper()
	for i := range snap.FileCount() {
		if snap.File(i).Path() == path {
			return i
		}
	}
	t.Fatalf("no changed file %q", path)
	return -1
}

// lines returns "<prefix>1\n" .. "<prefix>n\n".
func lines(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s%d\n", prefix, i)
	}
	return b.String()
}

// Object IDs for ledgers that never reach git: distinct, valid, and recognisable in failures.
const (
	tree1 = "1111111111111111111111111111111111111111"
	tree2 = "2222222222222222222222222222222222222222"
	tree3 = "3333333333333333333333333333333333333333"
	blobA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	blobB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	blobC = "cccccccccccccccccccccccccccccccccccccccc"
)

// objectID returns a valid object ID made from n, distinct for distinct n.
func objectID(n int) string { return fmt.Sprintf("%040x", n+1) }

// fakeRefs is a recording Refs whose refs carry commit dates truncated to whole seconds, as git
// stores them, and a fresh commit ID per write. Like git, it writes or deletes a ref only while it
// holds the expected commit. It is safe for concurrent use.
type fakeRefs struct {
	// onKeep and onDelete, when set, run at the start of each call; their error is the call's.
	onKeep   func() error
	onDelete func() error
	refs     map[string]source.RetentionRef
	keeps    []string
	deletes  [][]string
	// history is every ref value written, so UndoKeep can restore one.
	history []source.RetentionRef
	commits int
	mu      sync.Mutex
}

func newFakeRefs() *fakeRefs { return &fakeRefs{refs: map[string]source.RetentionRef{}} }

func (f *fakeRefs) KeepTrees(_ context.Context, ref, base, head string, at time.Time) (source.RefUpdate, error) {
	if f.onKeep != nil {
		if err := f.onKeep(); err != nil {
			return source.RefUpdate{}, err
		}
	}
	if !strings.HasSuffix(ref, "/"+base+"-"+head) {
		return source.RefUpdate{}, fmt.Errorf("fake: ref %q does not name trees %s-%s", ref, base, head)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	u := source.RefUpdate{Name: ref, Old: f.refs[ref].Commit, New: fmt.Sprintf("%040x", 1<<32+f.commits)}
	f.keeps = append(f.keeps, ref)
	f.refs[ref] = source.RetentionRef{Name: ref, Commit: u.New, Committed: at.Truncate(time.Second)}
	f.history = append(f.history, f.refs[ref])
	return u, nil
}

func (f *fakeRefs) UndoKeep(_ context.Context, u source.RefUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refs[u.Name].Commit != u.New {
		return fmt.Errorf("fake: %s no longer holds %s", u.Name, u.New)
	}
	if u.Old == "" {
		delete(f.refs, u.Name)
		return nil
	}
	k := slices.IndexFunc(f.history, func(r source.RetentionRef) bool { return r.Name == u.Name && r.Commit == u.Old })
	if k < 0 {
		return fmt.Errorf("fake: %s never held %s", u.Name, u.Old)
	}
	f.refs[u.Name] = f.history[k]
	return nil
}

func (f *fakeRefs) RetentionRefs(context.Context) ([]source.RetentionRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Collect(maps.Values(f.refs)), nil
}

func (f *fakeRefs) DeleteRefs(_ context.Context, refs []source.RetentionRef) error {
	if f.onDelete != nil {
		if err := f.onDelete(); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, len(refs))
	for k, r := range refs {
		if f.refs[r.Name].Commit != r.Commit {
			return fmt.Errorf("fake: %s does not hold %s", r.Name, r.Commit)
		}
		names[k] = r.Name
	}
	f.deletes = append(f.deletes, names)
	for _, n := range names {
		delete(f.refs, n)
	}
	return nil
}

// set points ref at a commit dated at, as another session would.
func (f *fakeRefs) set(ref string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	r := source.RetentionRef{Name: ref, Commit: fmt.Sprintf("%040x", 1<<32+f.commits), Committed: at.Truncate(time.Second)}
	f.history = append(f.history, r)
	f.refs[ref] = r
}

// names returns the refs held, sorted.
func (f *fakeRefs) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.refs))
	for n := range f.refs {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func (f *fakeRefs) keepCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keeps)
}

// openStore opens a store for a fixed fictitious worktree in a fresh directory.
func openStore(t *testing.T) *round.Store {
	t.Helper()
	return openStoreIn(t, t.TempDir(), "/repo/.git", "/repo")
}

func openStoreIn(t *testing.T, dir, commonDir, worktree string) *round.Store {
	t.Helper()
	s, err := round.OpenStore(dir, commonDir, worktree)
	if err != nil {
		t.Fatalf("OpenStore(%s, %s, %s): %v", dir, commonDir, worktree, err)
	}
	return s
}

// load loads the ledger as of now and fails on any error.
func load(t *testing.T, s *round.Store, now time.Time) round.Ledger {
	t.Helper()
	l, err := s.Load(now)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return l
}

func record(t *testing.T, s *round.Store, refs round.Refs, d round.Delivery) {
	t.Helper()
	if err := s.Record(t.Context(), refs, d); err != nil {
		t.Fatalf("Record(at %v): %v", d.At, err)
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// dirNames lists dir's entries.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// t0 is a whole-second time the bound tests count from.
var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

// regular returns a regular file's side.
func regular(id string) source.Blob { return source.Blob{Mode: "100644", ID: id} }

// marked returns a delivery, at the given time, of snapshot snap marking each key with its version.
func marked(at time.Time, snap round.Snapshot, versions map[round.FileKey]round.Version) round.Delivery {
	d := round.Delivery{At: at, Snapshot: snap, Reviewed: map[round.FileKey]round.Reviewed{}}
	for k, v := range versions {
		d.Reviewed[k] = round.Reviewed{At: at, Snapshot: snap, Version: v}
	}
	return d
}

func modified(path string) round.FileKey { return round.FileKey{OldPath: path, NewPath: path} }

func modVersion(oldID, newID string) round.Version {
	return round.Version{Old: regular(oldID), New: regular(newID)}
}
