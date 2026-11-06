// Package source resolves a review source into an immutable Git snapshot. Every Git fact comes
// from the git executable.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// waitDelay bounds how long a cancelled git command may keep its pipes open.
const waitDelay = 2 * time.Second

// Kind names a review source.
type Kind string

// The review sources.
const (
	KindLast        Kind = "last"
	KindBranch      Kind = "branch"
	KindUncommitted Kind = "uncommitted"
)

// Source is a validated review source request. Construct it with Last, Branch or Uncommitted.
type Source struct {
	kind        Kind
	baseRef     string
	uncommitted bool
}

// Last reviews HEAD against its first parent.
func Last() Source { return Source{kind: KindLast} }

// Uncommitted reviews the uncommitted tree against HEAD.
func Uncommitted() Source { return Source{kind: KindUncommitted, uncommitted: true} }

// Branch reviews HEAD, or the uncommitted tree when uncommitted is set, against the merge base of
// baseRef and HEAD. The base ref must not be empty.
func Branch(baseRef string, uncommitted bool) (Source, error) {
	if baseRef == "" {
		return Source{}, errors.New("branch needs a base ref")
	}
	return Source{kind: KindBranch, baseRef: baseRef, uncommitted: uncommitted}, nil
}

// Kind returns the source kind.
func (s Source) Kind() Kind { return s.kind }

// BaseRef returns the branch source's base ref, or "".
func (s Source) BaseRef() string { return s.baseRef }

// IncludesUncommitted reports whether the head is the uncommitted tree.
func (s Source) IncludesUncommitted() bool { return s.uncommitted }

// String describes the source for people, in messages and logs.
func (s Source) String() string {
	switch s.kind {
	case KindLast:
		return "last commit"
	case KindUncommitted:
		return "uncommitted changes"
	case KindBranch:
		if s.uncommitted {
			return "branch " + s.baseRef + "..HEAD plus uncommitted changes"
		}
		return "branch " + s.baseRef + "..HEAD"
	default:
		return string(s.kind)
	}
}

// Snapshot is the immutable pair of trees under review and its changed files, bound to the Repo
// that resolved it. Its methods read that repository lazily.
type Snapshot struct {
	repo       *Repo
	source     Source
	baseTree   string
	headTree   string
	headCommit string
	files      []ChangedFile
	// records[i] is files[i]'s raw record from the listing. Patches and counts are computed from
	// these records, so the listing alone decides how a file's sides are paired.
	records  []string
	vendored []string // vendored directory per file, see VendoredDir
	renames  RenameDetection
}

// Source returns the source that produced the snapshot.
func (s *Snapshot) Source() Source { return s.source }

// BaseTree returns the base tree ID.
func (s *Snapshot) BaseTree() string { return s.baseTree }

// HeadTree returns the head tree ID.
func (s *Snapshot) HeadTree() string { return s.headTree }

// HeadCommit is the commit HEAD resolved to at start, or "" when HEAD was unborn.
func (s *Snapshot) HeadCommit() string { return s.headCommit }

// FileCount returns the number of changed files.
func (s *Snapshot) FileCount() int { return len(s.files) }

// File returns the changed file at index i, in snapshot diff order. Like a slice index, it
// panics unless 0 <= i < FileCount().
func (s *Snapshot) File(i int) ChangedFile { return s.files[i] }

// RenameDetection reports how far rename detection ran for the changed-file list.
func (s *Snapshot) RenameDetection() RenameDetection { return s.renames }

// CommonDir is the common Git directory of the repository the snapshot was resolved in.
func (s *Snapshot) CommonDir() string { return s.repo.commonDir }

// TopLevel is the top-level directory of the worktree the snapshot was resolved in.
func (s *Snapshot) TopLevel() string { return s.repo.topLevel }

// HeadMoved reports whether HEAD now resolves to a commit other than HeadCommit, including an
// unborn HEAD gaining a commit.
func (s *Snapshot) HeadMoved(ctx context.Context) (bool, error) {
	head, err := s.repo.headCommit(ctx)
	if err != nil {
		return false, err
	}
	return head != s.headCommit, nil
}

// Repo is one repository's git boundary. It owns a long-lived object reader; Close stops it for
// good, so a blob read after Close fails.
type Repo struct {
	objects   *objectReader
	topLevel  string
	commonDir string
	emptyTree string
	run       runner
	objMu     sync.Mutex // guards objects and closed
	closed    bool
}

// Open finds the worktree containing dir and checks the git version.
func Open(ctx context.Context, dir string) (*Repo, error) {
	probe := newRunner(dir)
	version, err := probe.line(ctx, "version")
	if err != nil {
		return nil, fmt.Errorf("run git: %w", err)
	}
	if err = checkVersion(version); err != nil {
		return nil, err
	}
	out, err := probe.line(ctx, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	if err != nil {
		// Only git's own "not a git repository" is a user error; a refusal such as dubious
		// ownership, or a cancellation, is reported as it is.
		if ge, ok := errors.AsType[*gitError](err); ok && ge.exitCode() == 128 && strings.Contains(ge.Stderr, "not a git repository") {
			return nil, errors.New("not inside a Git worktree")
		}
		return nil, fmt.Errorf("find the Git worktree: %w", err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 || lines[0] == "" {
		return nil, errors.New("not inside a Git worktree")
	}
	r := &Repo{run: newRunner(lines[0]), topLevel: lines[0], commonDir: lines[1]}
	r.emptyTree, err = r.run.line(ctx, "hash-object", "-t", "tree", "/dev/null")
	if err != nil {
		return nil, fmt.Errorf("empty tree id: %w", err)
	}
	return r, nil
}

// Close stops the object reader. Later blob reads fail with ErrClosed.
func (r *Repo) Close() error {
	r.objMu.Lock()
	defer r.objMu.Unlock()
	r.closed = true
	if r.objects == nil {
		return nil
	}
	err := r.objects.close()
	r.objects = nil
	return err
}

// headCommit returns the commit HEAD resolves to, or "" when HEAD is unborn.
func (r *Repo) headCommit(ctx context.Context) (string, error) {
	out, err := r.run.output(ctx, 4<<10, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		var ge *gitError
		if errors.As(err, &ge) && ge.exitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ErrOriginHeadUnset reports that refs/remotes/origin/HEAD does not exist or is not a symbolic ref.
var ErrOriginHeadUnset = errors.New("origin/HEAD is not set")

// OriginHead returns the ref origin/HEAD points to, as git symbolic-ref --short names it: usually
// "origin/main", or "remotes/origin/main" when a local branch shares the short name. It does not
// check that the target resolves; Resolve does. It fails with ErrOriginHeadUnset when origin/HEAD
// is missing or not symbolic.
func (r *Repo) OriginHead(ctx context.Context) (string, error) {
	ref, err := r.run.line(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		// --quiet exits 1, silently, exactly when the ref is missing or not symbolic.
		if ge, ok := errors.AsType[*gitError](err); ok && ge.exitCode() == 1 {
			return "", ErrOriginHeadUnset
		}
		return "", fmt.Errorf("find origin/HEAD: %w", err)
	}
	return ref, nil
}

// Resolve builds the snapshot for src. progress receives human-readable progress lines.
func (r *Repo) Resolve(ctx context.Context, src Source, progress *log.Logger) (*Snapshot, error) {
	if src.kind == "" {
		return nil, errors.New("source not set")
	}
	head, err := r.headCommit(ctx)
	if err != nil {
		return nil, err
	}
	if head == "" && src.kind != KindUncommitted {
		return nil, errors.New("HEAD is unborn; " + src.String() + " needs a commit")
	}
	snap := &Snapshot{repo: r, source: src, headCommit: head}
	if snap.baseTree, err = r.baseTree(ctx, src, head); err != nil {
		return nil, err
	}
	if src.uncommitted {
		progress.Println("rvw: snapshotting uncommitted changes (git add -A into a private index)")
		snap.headTree, err = r.uncommittedTree(ctx)
	} else {
		snap.headTree, err = r.treeOf(ctx, head)
	}
	if err != nil {
		return nil, err
	}
	if snap.files, snap.records, snap.renames, err = r.changedFiles(ctx, snap.baseTree, snap.headTree); err != nil {
		return nil, err
	}
	if len(snap.files) == 0 {
		return nil, errors.New("nothing to review: " + src.String() + " has no changes")
	}
	if snap.vendored, err = snap.vendoredDirs(ctx); err != nil {
		return nil, err
	}
	return snap, nil
}

func (r *Repo) baseTree(ctx context.Context, src Source, head string) (string, error) {
	switch src.kind {
	case KindLast:
		parent, err := r.run.output(ctx, 4<<10, nil, "rev-parse", "--verify", "--quiet", head+"^1^{commit}")
		if err != nil {
			var ge *gitError
			if errors.As(err, &ge) && ge.exitCode() == 1 {
				return r.emptyTree, nil // root commit
			}
			return "", err
		}
		return r.treeOf(ctx, strings.TrimSpace(string(parent)))
	case KindUncommitted:
		if head == "" {
			return r.emptyTree, nil
		}
		return r.treeOf(ctx, head)
	case KindBranch:
		base, err := r.run.line(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", src.baseRef+"^{commit}")
		if err != nil {
			// --verify --quiet exits 1, silently, exactly when the name does not resolve.
			if ge, ok := errors.AsType[*gitError](err); ok && ge.exitCode() == 1 {
				return "", fmt.Errorf("base ref %q does not resolve to a commit", src.baseRef)
			}
			return "", fmt.Errorf("resolve base ref %q: %w", src.baseRef, err)
		}
		mb, err := r.run.line(ctx, "merge-base", base, head)
		if err != nil {
			var ge *gitError
			if errors.As(err, &ge) && ge.exitCode() == 1 {
				return "", fmt.Errorf("no merge base between %q and HEAD", src.baseRef)
			}
			return "", err
		}
		return r.treeOf(ctx, mb)
	default:
		return "", fmt.Errorf("unknown source kind %q", src.kind)
	}
}

func (r *Repo) treeOf(ctx context.Context, commit string) (string, error) {
	return r.run.line(ctx, "rev-parse", "--verify", commit+"^{tree}")
}

// uncommittedTree writes the uncommitted tree from a private copy of the index, so the real index
// is never written.
func (r *Repo) uncommittedTree(ctx context.Context) (tree string, err error) {
	realIndex, err := r.run.line(ctx, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "rvw-index-")
	if err != nil {
		return "", fmt.Errorf("private index: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("remove private index: %w", rmErr))
		}
	}()
	if within(dir, r.topLevel) {
		return "", fmt.Errorf("temporary directory %s is inside the worktree; set TMPDIR elsewhere", dir)
	}
	private := filepath.Join(dir, "index")
	// A repository without an index (nothing staged yet) starts from an empty private index.
	if err = copyFile(realIndex, private); err != nil && !errors.Is(err, errNoSource) {
		return "", fmt.Errorf("copy index: %w", err)
	}
	env := []string{"GIT_INDEX_FILE=" + private}
	if _, err = r.run.output(ctx, 1<<20, env, "-c", "core.splitIndex=false", "add", "-A"); err != nil {
		return "", err
	}
	out, err := r.run.output(ctx, 4<<10, env, "-c", "core.splitIndex=false", "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func within(path, dir string) bool {
	rp, err1 := filepath.EvalSymlinks(path)
	rd, err2 := filepath.EvalSymlinks(dir)
	if err1 != nil || err2 != nil {
		rp, rd = path, dir
	}
	rel, err := filepath.Rel(rd, rp)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// errNoSource reports that copyFile's source does not exist. Only that is distinguished: a missing
// destination directory or any later failure is an ordinary error.
var errNoSource = errors.New("source file does not exist")

func copyFile(from, to string) (err error) {
	src, err := os.Open(from) //nolint:gosec // path comes from git rev-parse --git-path
	if errors.Is(err, fs.ErrNotExist) {
		return errNoSource
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, src.Close()) }()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // private temp path
	if err != nil {
		return err
	}
	if _, err = io.Copy(dst, src); err != nil {
		return errors.Join(err, dst.Close())
	}
	if err = dst.Close(); err != nil {
		return err
	}
	// git trusts an entry's cached stat data only for files older than the index; a fresh mtime
	// would make a same-size edit from the index's last second look unchanged.
	return os.Chtimes(to, info.ModTime(), info.ModTime())
}
