package review_test

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// Fixture paths. The last commit modifies a.go and b.go, deletes del.go, renames old.go to
// ren.go, and adds add.go, long.go, bin.dat, a path with a tab and a path that is not UTF-8.
const (
	pathA       = "a.go"
	pathB       = "b.go"
	pathAdded   = "add.go"
	pathDeleted = "del.go"
	pathRenOld  = "old.go"
	pathRenNew  = "ren.go"
	pathLong    = "long.go"
	pathBinary  = "bin.dat"
	pathTab     = "tab\tname.go"
	pathNonUTF8 = "\xff.go"
)

func numbered(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s %d", prefix, i+1)
	}
	return out
}

var (
	aOld   = numbered("a", 10)
	aNew   = slices.Concat(aOld[:1], []string{"a 2 changed"}, aOld[2:])
	bOld   = numbered("b", 10)
	bNew   = slices.Concat(bOld[:3], []string{"b 4 changed", "b 5 changed"}, bOld[5:])
	delOld = numbered("deleted", 3)
	renOld = numbered("renamed file line", 10)
	// added holds the quoting cases: é straddles byte 500 of line 2, and lines 3 and 4 sit at and
	// past the quote line limit.
	added = []string{
		"short", strings.Repeat("a", 499) + "é" + "tail", strings.Repeat("b", 500), strings.Repeat("c", 501),
	}
	longNew = numbered("long", 30)
)

// fixture is a repository whose last commit holds every kind of changed file the review package
// distinguishes, and an uncommitted edit on top.
type fixture struct {
	repo        *source.Repo
	last        *source.Snapshot // rvw last
	branch      *source.Snapshot // rvw branch <first commit>: the same trees as last
	uncommitted *source.Snapshot // rvw uncommitted
	dir         string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, pathA, aOld)
	write(t, dir, pathB, bOld)
	write(t, dir, pathDeleted, delOld)
	write(t, dir, pathRenOld, renOld)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "one")
	first := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))

	write(t, dir, pathA, aNew)
	write(t, dir, pathB, bNew)
	write(t, dir, pathAdded, added)
	write(t, dir, pathLong, longNew)
	write(t, dir, pathTab, []string{"tab"})
	if err := os.WriteFile(filepath.Join(dir, pathBinary), []byte{0, 1, 2, 0, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, pathDeleted)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, pathRenOld), filepath.Join(dir, pathRenNew)); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	// APFS refuses a non-UTF-8 file name, so the path goes into the index directly.
	blob := strings.TrimSpace(gitIn(t, dir, "x\n", "hash-object", "-w", "--stdin"))
	git(t, dir, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+pathNonUTF8)
	git(t, dir, "commit", "-q", "-m", "two")
	write(t, dir, pathA, slices.Concat(aNew, []string{"a uncommitted"}))

	repo, err := source.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	fx := &fixture{repo: repo, dir: dir}
	fx.last = resolve(t, repo, source.Last())
	fx.branch = resolve(t, repo, branchSource(t, first, false))
	fx.uncommitted = resolve(t, repo, source.Uncommitted())
	return fx
}

// key is the draft key of snap in the fixture repository.
func (fx *fixture) key(snap *source.Snapshot) string {
	return review.DraftKey(snap)
}

// hideBlob moves the loose object rev names out of the object database, so reading it fails,
// and returns a func that puts it back. The fixture's objects are loose: gc is off.
func (fx *fixture) hideBlob(t *testing.T, rev string) (restore func()) {
	t.Helper()
	id := strings.TrimSpace(git(t, fx.dir, "rev-parse", rev))
	obj := strings.TrimSpace(git(t, fx.dir, "rev-parse", "--path-format=absolute", "--git-path", "objects/"+id[:2]+"/"+id[2:]))
	aside := filepath.Join(t.TempDir(), id)
	if err := os.Rename(obj, aside); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore = func() {
		if restored {
			return
		}
		restored = true
		if err := os.Rename(aside, obj); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(restore)
	return restore
}

// fileIndex returns the index of the changed file whose display path is path.
func (fx *fixture) fileIndex(t *testing.T, path string) int {
	t.Helper()
	for i := range fx.last.FileCount() {
		if f := fx.last.File(i); f.Path() == path {
			return i
		}
	}
	t.Fatalf("%q is not a changed file", path)
	return -1
}

func resolve(t *testing.T, repo *source.Repo, src source.Source) *source.Snapshot {
	t.Helper()
	snap, err := repo.Resolve(t.Context(), src, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("resolve %s: %v", src, err)
	}
	return snap
}

func write(t *testing.T, dir, name string, lines []string) {
	t.Helper()
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitIn(t, dir, "", args...)
}

func gitIn(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func gitEnv() []string {
	env := []string{
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		// No detached auto-maintenance writing into fixtures after a test returns.
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=gc.auto", "GIT_CONFIG_VALUE_1=0",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}

// openStore opens the draft store for key in dir and closes it at cleanup unless the test has.
//
// A lock released by Store.Close in this process can stay held for a moment: flock belongs to the
// open file description, and a git child forked by a parallel test shares it until its exec closes
// it. So a LockedError is retried until a deadline. Separate rvw processes do not share
// descriptions, so this is a property of the test process only.
func openStore(t *testing.T, dir, key string) *review.Store {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := review.OpenStore(dir, key)
		if _, locked := errors.AsType[*review.LockedError](err); locked && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
}

// start opens a store in dir for the fixture's last snapshot and starts a session.
func (fx *fixture) start(t *testing.T, dir string, seed review.Seed) (*review.Session, *review.Store) {
	t.Helper()
	st := openStore(t, dir, fx.key(fx.last))
	s, o, err := review.Start(t.Context(), st, fx.last, seed)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if o.Resumed() {
		t.Fatal("start resumed a draft; want a new one")
	}
	if path, moved, reason := o.MovedAside(); moved {
		t.Fatalf("start moved a draft aside to %s: %v", path, reason)
	}
	return s, st
}

func parseSeed(t *testing.T, doc string) review.Seed {
	t.Helper()
	seed, err := review.ParseSeed(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	return seed
}

// mustTarget panics on a target the test built wrong; tests build only well-formed targets with
// line, span and file, and check malformed ones through the constructors directly.
func mustTarget(target review.Target, err error) review.Target {
	if err != nil {
		panic(fmt.Sprintf("build target: %v", err))
	}
	return target
}

func line(path string, side review.Side, n int) review.Target {
	return mustTarget(review.LineTarget(path, side, n))
}

func span(path string, side review.Side, start, end int) review.Target {
	return mustTarget(review.RangeTarget(path, side, start, end))
}

func file(path string) review.Target {
	return mustTarget(review.FileTarget(path))
}

var reviewTarget = review.ReviewTarget()

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return false
}

// on places a comment where target points in s's snapshot, so tests can name places by path. A
// line or range on a side the file lacks is placed on the file whose display path is target's.
func on(tb testing.TB, s *review.Session, target review.Target) review.Placement {
	tb.Helper()
	if target.Kind() == review.TargetReview {
		return review.OnReview()
	}
	snap := s.Snapshot()
	i := -1
	for k := range snap.FileCount() {
		f := snap.File(k)
		path := f.NewPath
		switch {
		case target.Kind() == review.TargetFile:
			path = f.Path()
		case target.Side() == review.Old:
			path = f.OldPath
		}
		if path == target.Path() {
			i = k
			break
		}
		if i < 0 && f.Path() == target.Path() {
			i = k
		}
	}
	if i < 0 {
		tb.Fatalf("no changed file %q", target.Path())
	}
	start, end := target.Lines()
	switch target.Kind() {
	case review.TargetFile:
		return review.OnFile(i)
	case review.TargetLine:
		return review.OnLine(i, target.Side(), start)
	case review.TargetRange:
		return review.OnRange(i, target.Side(), start, end)
	default:
		return review.OnReview()
	}
}

// versionOf is comment id's current version, as an editor that just loaded it holds.
func versionOf(tb testing.TB, s *review.Session, id string) int64 {
	tb.Helper()
	for _, c := range s.View().Comments {
		if c.ID == id {
			return c.Version
		}
	}
	return 0 // no such comment; EditComment reports ErrNoComment
}

// withoutVersions clears the versions, which a restore does not keep.
func withoutVersions(v review.View) review.View {
	v.Comments = slices.Clone(v.Comments)
	for i := range v.Comments {
		v.Comments[i].Version = 0
	}
	return v
}

// branchSource is source.Branch for a base ref the test knows is valid.
func branchSource(tb testing.TB, baseRef string, uncommitted bool) source.Source {
	tb.Helper()
	src, err := source.Branch(baseRef, uncommitted)
	if err != nil {
		tb.Fatal(err)
	}
	return src
}
