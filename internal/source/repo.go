// Package source resolves a review source into an immutable Git snapshot. Every Git fact comes
// from the git executable.
package source

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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
	// baseCommit is what a branch source's base ref resolved to; "" for other sources.
	baseCommit string
	// index is the private index the uncommitted tree was written from, kept for Changed; nil
	// unless the head is the uncommitted tree.
	index *privateIndex
	files []ChangedFile
	// records[i] is files[i]'s raw record from the listing. Patches and counts are computed from
	// these records, so the listing alone decides how a file's sides are paired.
	records []string
	// sizes is the size of every non-submodule blob of files, -1 when absent locally.
	sizes map[string]int64
	// missing lists, sorted, the blobs of files absent locally when they were listed: the ones
	// Changed asks about.
	missing  []string
	vendored []string // vendored directory per file, see VendoredDir
	renames  RenameDetection
}

// SameFiles reports whether s and o list the same changed files in the same order, paired the same
// way (the raw records that decide how patches pair the sides) and with the same blobs present
// locally (ChangedFile.Missing), so a file index names the same file, with the same readable
// content, in both. It compares only the listing; trees and commits are not compared (DraftKey
// covers them). It works after Close, since files and records are retained. In a partial clone,
// a blob arriving that leaves a file with none missing makes it false, even when no rename
// pairing changes.
func (s *Snapshot) SameFiles(o *Snapshot) bool {
	return s.renames == o.renames && slices.Equal(s.files, o.files) && slices.Equal(s.records, o.records)
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

// Changed reports whether resolving the snapshot's source now could give other trees or another
// file list (REQ-SES-010): HEAD resolves to another commit, including an unborn HEAD gaining one; a
// branch source's base ref resolves to another commit or to none; when the head is the uncommitted
// tree, a worktree file differs from the snapshot's private index or a file that is neither in it
// nor ignored appears; or, in a partial clone, a blob absent when the files were listed is now
// present, which changes the listing (SameFiles) though not the trees. A true answer is a reason
// to resolve again, not a promise that anything differs: a file edited and changed back, or a
// commit of the same tree, reads as changed.
//
// It runs git but writes no object, fetches none, and touches neither the real index nor the
// worktree: the worktree is compared with the private index, whose stat data it refreshes, and the
// absent blobs are asked about in one `cat-file --batch-check` run. Index-only changes, such as
// untracking a file, and ignore rules outside the worktree changing are not seen. It fails after
// Close of an uncommitted snapshot.
func (s *Snapshot) Changed(ctx context.Context) (bool, error) {
	head, err := s.repo.headCommit(ctx)
	if err != nil {
		return false, err
	}
	if head != s.headCommit {
		return true, nil
	}
	if s.source.kind == KindBranch {
		base, err := s.repo.commitOf(ctx, s.source.baseRef)
		if err != nil {
			return false, err
		}
		if base != s.baseCommit {
			return true, nil
		}
	}
	if s.index != nil {
		if changed, err := s.index.changed(ctx, s.repo.run); err != nil || changed {
			return changed, err
		}
	}
	return s.blobsArrived(ctx)
}

// blobsArrived reports whether a file listed with a blob absent locally (ChangedFile.Missing) now
// has every blob present, which changes the listing: one blob of two arriving changes nothing.
func (s *Snapshot) blobsArrived(ctx context.Context) (bool, error) {
	if len(s.missing) == 0 {
		return false, nil
	}
	now := make(map[string]int64, len(s.missing))
	if err := s.repo.sizeBlobs(ctx, s.missing, now); err != nil {
		return false, err
	}
	present := func(id string) bool {
		n, listed := s.sizes[id]
		_, arrived := now[id]
		return !listed || n >= 0 || arrived
	}
	for _, f := range s.files {
		if f.Missing && present(f.OldBlob) && present(f.NewBlob) {
			return true, nil
		}
	}
	return false, nil
}

// Close deletes the snapshot's private index, after which Changed fails. Every other method still
// works: the trees and blobs are in the object database. It is idempotent, and Repo.Close does it
// for every snapshot still open.
func (s *Snapshot) Close() error {
	if s.index == nil {
		return nil
	}
	err := s.index.close()
	s.repo.forget(s.index)
	return err
}

// Repo is one repository's git boundary. It owns a long-lived object reader and the private
// indexes of its open snapshots; Close stops the reader and deletes the indexes for good, so a
// blob read after Close fails.
type Repo struct {
	objects   *objectReader
	indexes   map[*privateIndex]struct{} // the open snapshots' private indexes
	topLevel  string
	commonDir string
	emptyTree string
	run       runner
	objMu     sync.Mutex // guards objects and closed
	indexMu   sync.Mutex // guards indexes
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

// Close stops the object reader and deletes the private index of every snapshot still open. Later
// blob reads fail with ErrClosed.
func (r *Repo) Close() error {
	// closed is set under indexMu, which keep holds while it checks closed, so no index is kept
	// after the swap below.
	r.indexMu.Lock()
	r.objMu.Lock()
	r.closed = true
	r.objMu.Unlock()
	indexes := r.indexes
	r.indexes = nil
	r.indexMu.Unlock()
	var errs []error
	for x := range indexes {
		errs = append(errs, x.close())
	}
	r.objMu.Lock()
	defer r.objMu.Unlock()
	if r.objects != nil {
		errs = append(errs, r.objects.close())
		r.objects = nil
	}
	return errors.Join(errs...)
}

// keep records x as an open snapshot's index, for Close to delete. It fails after Close.
func (r *Repo) keep(x *privateIndex) error {
	r.indexMu.Lock()
	defer r.indexMu.Unlock()
	r.objMu.Lock()
	closed := r.closed
	r.objMu.Unlock()
	if closed {
		return ErrClosed
	}
	if r.indexes == nil {
		r.indexes = map[*privateIndex]struct{}{}
	}
	r.indexes[x] = struct{}{}
	return nil
}

func (r *Repo) forget(x *privateIndex) {
	r.indexMu.Lock()
	defer r.indexMu.Unlock()
	delete(r.indexes, x)
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

// Resolve builds the snapshot for src. progress receives what Resolve is doing while it runs.
// err is named so the deferred cleanup below can add a failed index deletion to it.
func (r *Repo) Resolve(ctx context.Context, src Source, progress *slog.Logger) (_ *Snapshot, err error) {
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
	if snap.baseCommit, snap.baseTree, err = r.base(ctx, src, head); err != nil {
		return nil, err
	}
	if src.uncommitted {
		progress.Info("snapshotting uncommitted changes", "how", "git add -A into a private index")
		snap.headTree, snap.index, err = r.uncommittedTree(ctx)
	} else {
		snap.headTree, err = r.treeOf(ctx, head)
	}
	if err != nil {
		return nil, err
	}
	// A snapshot that is not returned deletes its private index here.
	ok := false
	defer func() {
		if !ok {
			err = errors.Join(err, snap.Close())
		}
	}()
	list, err := r.changedFiles(ctx, snap.baseTree, snap.headTree)
	if err != nil {
		return nil, err
	}
	snap.files, snap.records, snap.sizes, snap.renames = list.files, list.records, list.sizes, list.renames
	for id, n := range snap.sizes {
		if n < 0 {
			snap.missing = append(snap.missing, id)
		}
	}
	slices.Sort(snap.missing)
	if len(snap.files) == 0 {
		return nil, errors.New("nothing to review: " + src.String() + " has no changes")
	}
	if snap.vendored, err = snap.vendoredDirs(ctx); err != nil {
		return nil, err
	}
	ok = true
	return snap, nil
}

// base returns the base tree of src, and for a branch source the commit its base ref resolves to.
func (r *Repo) base(ctx context.Context, src Source, head string) (baseCommit, tree string, err error) {
	switch src.kind {
	case KindLast:
		parent, err := r.run.output(ctx, 4<<10, nil, "rev-parse", "--verify", "--quiet", head+"^1^{commit}")
		if err != nil {
			var ge *gitError
			if errors.As(err, &ge) && ge.exitCode() == 1 {
				return "", r.emptyTree, nil // root commit
			}
			return "", "", err
		}
		tree, err := r.treeOf(ctx, strings.TrimSpace(string(parent)))
		return "", tree, err
	case KindUncommitted:
		if head == "" {
			return "", r.emptyTree, nil
		}
		tree, err := r.treeOf(ctx, head)
		return "", tree, err
	case KindBranch:
		base, err := r.commitOf(ctx, src.baseRef)
		if err != nil {
			return "", "", err
		}
		if base == "" {
			return "", "", fmt.Errorf("base ref %q does not resolve to a commit", src.baseRef)
		}
		mb, err := r.run.line(ctx, "merge-base", base, head)
		if err != nil {
			var ge *gitError
			if errors.As(err, &ge) && ge.exitCode() == 1 {
				return "", "", fmt.Errorf("no merge base between %q and HEAD", src.baseRef)
			}
			return "", "", err
		}
		tree, err := r.treeOf(ctx, mb)
		return base, tree, err
	default:
		return "", "", fmt.Errorf("unknown source kind %q", src.kind)
	}
}

// commitOf returns the commit ref resolves to, or "" when it resolves to none.
func (r *Repo) commitOf(ctx context.Context, ref string) (string, error) {
	c, err := r.run.line(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		// --verify --quiet exits 1, silently, exactly when the name does not resolve.
		if ge, ok := errors.AsType[*gitError](err); ok && ge.exitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("resolve base ref %q: %w", ref, err)
	}
	return c, nil
}

func (r *Repo) treeOf(ctx context.Context, commit string) (string, error) {
	return r.run.line(ctx, "rev-parse", "--verify", commit+"^{tree}")
}

// uncommittedTree writes the uncommitted tree from a private copy of the index, so the real index
// is never written. The private index is returned for Changed; the caller owns it.
func (r *Repo) uncommittedTree(ctx context.Context) (tree string, _ *privateIndex, err error) {
	realIndex, err := r.run.line(ctx, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "rvw-index-")
	if err != nil {
		return "", nil, fmt.Errorf("private index: %w", err)
	}
	x := &privateIndex{dir: dir, path: filepath.Join(dir, "index")}
	defer func() {
		if err != nil {
			err = errors.Join(err, x.close())
		}
	}()
	if within(dir, r.topLevel) {
		return "", nil, fmt.Errorf("temporary directory %s is inside the worktree; set TMPDIR elsewhere", dir)
	}
	// A repository without an index (nothing staged yet) starts from an empty private index.
	if err = copyFile(realIndex, x.path); err != nil && !errors.Is(err, errNoSource) {
		return "", nil, fmt.Errorf("copy index: %w", err)
	}
	env := x.env()
	if _, err = r.run.output(ctx, 1<<20, env, "-c", "core.splitIndex=false", "add", "-A"); err != nil {
		return "", nil, err
	}
	out, err := r.run.output(ctx, 4<<10, env, "-c", "core.splitIndex=false", "write-tree")
	if err != nil {
		return "", nil, err
	}
	if err = r.keep(x); err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(string(out)), x, nil
}

// privateIndex is a private index file outside the worktree holding the uncommitted tree's entries
// and their stat data, as git add -A left them (REQ-SRC-005).
type privateIndex struct {
	dir, path string
	mu        sync.Mutex // serialises checks and close
	closed    bool
}

func (x *privateIndex) env() []string { return []string{"GIT_INDEX_FILE=" + x.path} }

// errFound stops a listing at its first byte: a check needs only to know that there is output.
var errFound = errors.New("found")

// changed reports whether the worktree differs from x: a tracked file's content or mode differs or
// it is gone, or a file that is not ignored is not in x. Refreshing x's stat data first keeps a
// file touched but unchanged from counting; refreshing hashes such files without writing objects.
// Dirty submodule worktrees are ignored, as git add -A records only a submodule's commit.
func (x *privateIndex) changed(ctx context.Context, run runner) (bool, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return false, ErrClosed
	}
	env := x.env()
	// -q: files that need an update are reported by diff-files, not here.
	if _, err := run.output(ctx, 1<<20, env, "-c", "core.splitIndex=false", "update-index", "-q", "--refresh"); err != nil {
		return false, err
	}
	if _, err := run.output(ctx, 4<<10, env, "diff-files", "--quiet", "--ignore-submodules=dirty"); err != nil {
		if ge, ok := errors.AsType[*gitError](err); ok && ge.exitCode() == 1 {
			return true, nil
		}
		return false, err
	}
	_, err := run.stream(ctx, 1, env, nil, func(br *bufio.Reader) error {
		if _, err := br.ReadByte(); err == nil {
			return errFound
		}
		return nil
	}, "ls-files", "-z", "--others", "--exclude-standard", "--directory", "--no-empty-directory")
	if errors.Is(err, errFound) {
		return true, nil
	}
	return false, err
}

// close deletes x's directory; it is idempotent.
func (x *privateIndex) close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return nil
	}
	x.closed = true
	if err := os.RemoveAll(x.dir); err != nil {
		return fmt.Errorf("remove private index: %w", err)
	}
	return nil
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
