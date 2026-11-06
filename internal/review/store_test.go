package review_test

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// TestDraftRestoresTheReview checks that a restarted session restores comments, summary and viewed
// marks, ignores the seed, and keeps creation order.
func TestDraftRestoresTheReview(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	dir := t.TempDir()
	s, st := fx.start(t, dir, review.NewSeed(review.SeedComment{Target: line(pathA, review.New, 2), Body: "seeded"}))
	for id, target := range map[string]review.Target{
		"review":  reviewTarget,
		"file":    file(pathDeleted),
		"renamed": line(pathRenOld, review.Old, 3),
		"range":   span(pathA, review.New, 1, 4),
	} {
		if _, err := s.CreateComment(t.Context(), id, on(t, s, target), "body of "+id); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{pathA, pathRenNew, pathDeleted} {
		if err := s.SetViewed([]int{fx.fileIndex(t, path)}, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetViewed([]int{fx.fileIndex(t, pathA)}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSummary("summary\n"); err != nil {
		t.Fatal(err)
	}
	want := s.Draft()
	if len(want.Viewed) != 2 || len(want.Comments) != 5 {
		t.Fatalf("draft before restart = %+v", want)
	}
	s.End()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st = openStore(t, dir, fx.key(fx.last))
	s, o, err := review.Start(t.Context(), st, fx.last, review.Fresh{Seed: review.NewSeed(review.SeedComment{Target: reviewTarget, Body: "second seed"})})
	if err != nil {
		t.Fatal(err)
	}
	if _, moved, _ := o.MovedAside(); moved || !o.Resumed() {
		t.Fatalf("Start: resumed = %v, moved aside = %v; want the draft resumed", o.Resumed(), moved)
	}
	if got := withoutVersions(s.Draft()); !reflect.DeepEqual(got, withoutVersions(want)) {
		t.Fatalf("restored =\n%+v\nwant\n%+v", got, want)
	}
	for _, c := range want.Comments {
		assertFileIndex(t, fx, c)
	}

	// A comment created after the restart sorts after a restored one on the same line.
	if _, err := s.CreateComment(t.Context(), "after", on(t, s, line(pathA, review.New, 2)), "after restart"); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range s.Draft().Comments {
		if c.Target == line(pathA, review.New, 2) {
			bodies = append(bodies, c.Body)
		}
	}
	if !reflect.DeepEqual(bodies, []string{"seeded", "after restart"}) {
		t.Fatalf("comments on %s:2 = %q; want the restored one first", pathA, bodies)
	}
}

// TestDraftPersistedBeforeApplying checks that a change that cannot be persisted is not applied.
func TestDraftPersistedBeforeApplying(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	fx := newFixture(t)
	dir := t.TempDir()
	s, st := fx.start(t, dir, review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "c1", on(t, s, line(pathA, review.New, 1)), "persisted"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetViewed([]int{0}, true); err != nil {
		t.Fatal(err)
	}
	want := s.Draft()
	c1 := versionOf(t, s, "c1")

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	failures := map[string]error{
		"new comment":     second(s.CreateComment(t.Context(), "c2", on(t, s, reviewTarget), "x")),
		"edited comment":  second(s.EditComment("c1", c1, "edited")),
		"deleted comment": s.DeleteComment("c1", c1),
		"viewed":          s.SetViewed([]int{0}, false),
		"summary":         s.SetSummary("x"),
	}
	for name, err := range failures {
		if err == nil {
			t.Errorf("%s: saved to a read-only draft directory", name)
		}
		if _, ok := errors.AsType[*review.InvalidError](err); ok {
			t.Errorf("%s: a storage failure reported as invalid input: %v", name, err)
		}
		if errors.Is(err, review.ErrStale) {
			t.Errorf("%s: a storage failure reported as stale: %v", name, err)
		}
	}
	if got := s.Draft(); !reflect.DeepEqual(got, want) {
		t.Fatalf("draft after failed saves =\n%+v\nwant\n%+v", got, want)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s.End()
	assertDraftRestores(t, fx, st, want)
}

// TestDraftKeyIdentity checks that the key separates snapshots, sources, worktrees and
// repositories, that spellings of one base share a key, and that it is stable (REQ-DRF-002).
func TestDraftKeyIdentity(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	// A linked worktree shares the repository's common directory; a clone has its own. Both
	// resolve the same trees as the fixture. Neither checks out: APFS refuses the non-UTF-8 path.
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, fx.dir, "worktree", "add", "-q", "--no-checkout", "--detach", linked, "HEAD")
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, fx.dir, "clone", "-q", "--no-checkout", fx.dir, clone)
	// Two more names for fx.branch's base, the first commit: the same trees, other spellings.
	git(t, fx.dir, "tag", "t1", "HEAD~1")
	git(t, fx.dir, "tag", "t2", "HEAD~1")
	keys := map[string]string{
		"last":                 review.DraftKey(fx.last),
		"branch":               review.DraftKey(fx.branch),
		"branch --uncommitted": review.DraftKey(resolve(t, fx.repo, branchSource(t, "t1", true))),
		"uncommitted":          review.DraftKey(fx.uncommitted),
		"other worktree":       review.DraftKey(lastIn(t, linked)),
		"other clone":          review.DraftKey(lastIn(t, clone)),
	}
	seen := map[string]string{}
	for name, k := range keys {
		if other, dup := seen[k]; dup {
			t.Errorf("%s and %s share draft key %s", name, other, k)
		}
		seen[k] = name
	}
	for _, ref := range []string{"t1", "t2"} {
		if k := review.DraftKey(resolve(t, fx.repo, branchSource(t, ref, false))); k != keys["branch"] {
			t.Errorf("branch %s and branch <first commit ID> name the same base trees; want one draft key", ref)
		}
	}
	if again := review.DraftKey(resolve(t, fx.repo, source.Last())); again != keys["last"] {
		t.Errorf("the same snapshot has keys %s and %s", keys["last"], again)
	}
	if again := review.DraftKey(resolve(t, fx.repo, branchSource(t, "t1", false))); again != keys["branch"] {
		t.Errorf("resolving t1 twice gives keys %s and %s", keys["branch"], again)
	}

	// A session for another snapshot does not load this one's draft.
	dir := t.TempDir()
	s, _ := fx.start(t, dir, review.NewSeed())
	if err := s.SetSummary("last"); err != nil {
		t.Fatal(err)
	}
	st := openStore(t, dir, keys["uncommitted"])
	other, o, err := review.Start(t.Context(), st, fx.uncommitted, review.Fresh{Seed: review.NewSeed()})
	if err != nil {
		t.Fatal(err)
	}
	if _, moved, _ := o.MovedAside(); o.Resumed() || moved || other.Draft().Summary != "" {
		t.Fatal("a different snapshot loaded the draft")
	}
}

// lastIn resolves "rvw last" in the worktree at dir.
func lastIn(t *testing.T, dir string) *source.Snapshot {
	t.Helper()
	repo, err := source.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return resolve(t, repo, source.Last())
}

// TestExpireDrafts checks that drafts unused for 30 days expire unless a session holds them.
//
// Not parallel: a git child forked by a parallel test briefly shares a closed store's lock (see
// openStore), and expiry would then rightly skip the draft as held.
func TestExpireDrafts(t *testing.T) {
	fx := newFixture(t)
	dir := t.TempDir()
	s, st := fx.start(t, dir, review.NewSeed())
	if err := s.SetSummary("old work"); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour

	// Held by a running session: kept, and not an error.
	if err := review.ExpireDrafts(dir, time.Now().Add(31*day)); err != nil {
		t.Fatalf("ExpireDrafts with a held draft: %v", err)
	}
	if !fileExists(t, st.Path()) {
		t.Fatal("a held draft expired")
	}
	s.End()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if err := review.ExpireDrafts(dir, time.Now().Add(29*day)); err != nil {
		t.Fatal(err)
	}
	if !fileExists(t, st.Path()) {
		t.Fatal("a draft unused for 29 days expired")
	}
	if err := review.ExpireDrafts(dir, time.Now().Add(31*day)); err != nil {
		t.Fatal(err)
	}
	if fileExists(t, st.Path()) {
		t.Fatal("a draft unused for 31 days was kept")
	}

	st = openStore(t, dir, fx.key(fx.last))
	_, o, err := review.Start(t.Context(), st, fx.last, review.Fresh{Seed: review.NewSeed()})
	if _, moved, _ := o.MovedAside(); err != nil || o.Resumed() || moved {
		t.Fatalf("Start after expiry: resumed = %v, moved aside = %v, err = %v; want a new draft", o.Resumed(), moved, err)
	}
}

// TestExpireDraftsRemovesStaleTemporaryFiles: a temporary draft file left by a process killed
// before its rename is removed once older than 30 days, and a live session's draft and lock are
// untouched. Not parallel, like TestExpireDrafts: it holds a session's lock.
func TestExpireDraftsRemovesStaleTemporaryFiles(t *testing.T) {
	fx := newFixture(t)
	dir := t.TempDir()
	s, st := fx.start(t, dir, review.NewSeed())
	if err := s.SetSummary("live work"); err != nil {
		t.Fatal(err)
	}
	lock := strings.TrimSuffix(st.Path(), ".json") + ".lock"
	if !fileExists(t, lock) {
		t.Fatalf("no lock file %s beside the draft", lock)
	}
	young, old := filepath.Join(dir, ".draft-y"), filepath.Join(dir, ".draft-x")
	for _, p := range []string{young, old} {
		if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour

	if err := review.ExpireDrafts(dir, time.Now().Add(29*day)); err != nil {
		t.Fatal(err)
	}
	if !fileExists(t, young) {
		t.Error("a temporary draft file 29 days old was removed")
	}
	if err := review.ExpireDrafts(dir, time.Now().Add(31*day)); err != nil {
		t.Fatal(err)
	}
	if fileExists(t, old) {
		t.Error("a temporary draft file 31 days old was kept")
	}
	if !fileExists(t, st.Path()) || !fileExists(t, lock) {
		t.Error("expiring temporary files removed a live session's draft or lock")
	}
}

func TestExpireDraftsWithoutDirectory(t *testing.T) {
	t.Parallel()
	if err := review.ExpireDrafts(t.TempDir()+"/missing", time.Now()); err != nil {
		t.Fatalf("ExpireDrafts on a missing directory: %v", err)
	}
}

// TestStoreLock checks that only one session at a time holds a draft.
func TestStoreLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := openStore(t, dir, "k")

	_, err := review.OpenStore(dir, "k")
	if le, ok := errors.AsType[*review.LockedError](err); !ok || le.URL != "" {
		t.Fatalf("second OpenStore while starting: err = %v; want a LockedError without URL", err)
	}
	const url = "http://127.0.0.1:41234/#token"
	if err := first.SetURL(url); err != nil {
		t.Fatal(err)
	}
	_, err = review.OpenStore(dir, "k")
	if le, ok := errors.AsType[*review.LockedError](err); !ok || le.URL != url {
		t.Fatalf("second OpenStore while serving: err = %v; want a LockedError with %s", err, url)
	}
	openStore(t, dir, "other") // another snapshot is independent

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	openStore(t, dir, "k")
}

const holdLockEnv = "RVW_REVIEW_TEST_HOLD_LOCK"

// TestHoldLockHelper is the child process of TestKilledSessionLeavesNoStaleURL.
func TestHoldLockHelper(t *testing.T) {
	dir := os.Getenv(holdLockEnv)
	if dir == "" {
		return
	}
	st, err := review.OpenStore(dir, "k")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetURL("http://127.0.0.1:1/#stale"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stdout.WriteString("ready\n"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin) // hold the lock until killed
}

// TestKilledSessionLeavesNoStaleURL checks that a killed session's URL is not reported by later
// sessions.
func TestKilledSessionLeavesNoStaleURL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHoldLockHelper$")
	cmd.Env = append(os.Environ(), holdLockEnv+"="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if got, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || got != "ready\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("helper: %q, %v", got, err)
	}
	_, err = review.OpenStore(dir, "k")
	if le, ok := errors.AsType[*review.LockedError](err); !ok || le.URL != "http://127.0.0.1:1/#stale" {
		t.Errorf("OpenStore while the helper serves: err = %v; want its URL", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	openStore(t, dir, "k")
	_, err = review.OpenStore(dir, "k")
	if le, ok := errors.AsType[*review.LockedError](err); !ok || le.URL != "" {
		t.Fatalf("OpenStore after a killed session: err = %v; want a LockedError without URL", err)
	}
}
