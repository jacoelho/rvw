package round

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jacoelho/rvw/internal/source"
)

const (
	// lockWait bounds waiting for the ledger lock (REQ-RND-003).
	lockWait = 2 * time.Second
	// lockPoll is the interval between attempts to take the lock.
	lockPoll = 20 * time.Millisecond
	// maxLedgerBytes bounds a ledger file in both directions: read rejects a larger file as
	// invalid and record refuses to write one, so every written ledger is readable. It is
	// MaxReviewed versions of a few hundred bytes plus the earlier comments' bodies.
	maxLedgerBytes = 64 << 20
	// tempPrefix starts the name of a ledger being written; ExpireLedgers removes leftovers.
	tempPrefix = ".ledger-"
	// discardedMarker is in the name of every moved-aside ledger, which no worktree's ledger name
	// contains (REQ-RND-007).
	discardedMarker = ".discarded-"
)

// LedgerDir returns ~/Library/Application Support/rvw/ledgers (REQ-RND-001).
func LedgerDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate ledger directory: %w", err)
	}
	return filepath.Join(base, "rvw", "ledgers"), nil
}

// Refs is the retention-ref access Record needs; *source.Repo implements it.
type Refs interface {
	KeepTrees(ctx context.Context, ref, base, head string, at time.Time) (source.RefUpdate, error)
	UndoKeep(ctx context.Context, u source.RefUpdate) error
	RetentionRefs(ctx context.Context) ([]source.RetentionRef, error)
	DeleteRefs(ctx context.Context, refs []source.RetentionRef) error
}

// Store is one worktree's ledger file. It holds no lock between calls: each Load and Record
// takes the lock for its own read or write (REQ-RND-003), so sessions of one worktree run at once.
type Store struct {
	path, lockPath string
	// refPrefix is this worktree's retention-ref namespace, ending in "/".
	refPrefix string
}

// OpenStore returns the store of the ledger for the repository whose common Git directory is
// commonDir and the worktree at worktree, both absolute, in dir (REQ-RND-001). It creates dir.
func OpenStore(dir, commonDir, worktree string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create ledger directory: %w", err)
	}
	var buf []byte
	// The length prefixes keep the parts unambiguous.
	for _, part := range []string{"rvw-ledger-v1", commonDir, worktree} {
		buf = fmt.Appendf(buf, "%d:%s\n", len(part), part)
	}
	sum := sha256.Sum256(buf)
	key := hex.EncodeToString(sum[:])
	return &Store{
		path: filepath.Join(dir, key+".json"), lockPath: filepath.Join(dir, key+".lock"),
		refPrefix: source.RetentionPrefix + WorktreeID(worktree) + "/",
	}, nil
}

// WorktreeID is the worktree's ID (Definitions): the first 16 hexadecimal digits of the SHA-256 of
// its absolute path.
func WorktreeID(worktree string) string {
	sum := sha256.Sum256([]byte(worktree))
	return hex.EncodeToString(sum[:8])
}

// Path is the ledger file's path.
func (s *Store) Path() string { return s.path }

// MovedAsideError reports an invalid ledger that Load moved aside (REQ-RND-007).
type MovedAsideError struct {
	Path   string // where the ledger now is
	Reason string
}

func (e *MovedAsideError) Error() string {
	return fmt.Sprintf("review ledger moved aside to %s: %s", e.Path, e.Reason)
}

// Load reads the ledger at session start and returns it as REQ-RND-004 keeps it as of now. It
// always returns a usable Ledger, empty on error, so the caller reports a non-nil error and
// continues (REQ-RND-006). It never rewrites the ledger file; an invalid ledger is moved aside and
// reported as *MovedAsideError. now is the caller's wall clock and is not checked against delivery
// times: a now earlier than recorded deliveries keeps everything.
func (s *Store) Load(now time.Time) (Ledger, error) {
	unlock, err := s.lock()
	if err != nil {
		return Ledger{}, err
	}
	l, err := s.read()
	if err == nil {
		l = l.bounded(now)
	}
	if invalid, ok := errors.AsType[*invalidLedgerError](err); ok {
		to, merr := s.moveAside()
		if merr != nil {
			err = errors.Join(fmt.Errorf("review ledger %s: %w", s.path, err), merr)
		} else {
			err = &MovedAsideError{Path: to, Reason: invalid.reason}
		}
	}
	return l, errors.Join(err, unlock())
}

// Record records d in the ledger and keeps the delivered snapshot's trees (REQ-RND-002,
// REQ-RND-005), all under the lock: it merges d into the ledger read now, points the retention ref
// at the delivered snapshot, writes the ledger, and then deletes this worktree's retention refs the
// ledger no longer names and every retention ref older than TTL. A failure to lock, to read or
// validate the ledger, or to write the ref changes nothing (REQ-RND-006). The retention ref is
// written only when the merged ledger names the delivered snapshot; one it does not name would be
// deleted again in the same call.
func (s *Store) Record(ctx context.Context, refs Refs, d Delivery) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	return errors.Join(s.record(ctx, refs, d), unlock())
}

func (s *Store) record(ctx context.Context, refs Refs, d Delivery) error {
	l, err := s.read()
	if err != nil {
		return fmt.Errorf("record review round: %w", err)
	}
	next := l.merge(d)
	named := next.snapshots()
	// The ref is dated by the newest delivery naming its snapshot, which an older d does not
	// change, so pruning by age never drops a ref the ledger still keeps.
	var kept *source.RefUpdate
	if at, ok := named[d.Snapshot]; ok {
		u, keepErr := refs.KeepTrees(ctx, s.refName(d.Snapshot), d.Snapshot.Base, d.Snapshot.Head, at)
		if keepErr != nil {
			return fmt.Errorf("record review round: %w", keepErr)
		}
		kept = &u
	}
	data, err := next.encode()
	if err == nil && len(data) > maxLedgerBytes {
		err = fmt.Errorf("record review round: the ledger would be %d bytes, over %d", len(data), maxLedgerBytes)
	}
	if err == nil {
		var replaced bool
		if replaced, err = s.write(data); replaced {
			// The new ledger is in place; only its directory entry may not be durable yet.
			return errors.Join(err, s.prune(ctx, refs, named, d.At))
		}
	}
	if kept != nil {
		// The ledger is unchanged, so the ref returns to exactly what it held (REQ-RND-006).
		if undoErr := refs.UndoKeep(ctx, *kept); undoErr != nil {
			err = errors.Join(err, fmt.Errorf("restore retention ref: %w", undoErr))
		}
	}
	return err
}

// refName is snap's retention ref in this worktree's namespace.
func (s *Store) refName(snap Snapshot) string { return s.refPrefix + snap.Base + "-" + snap.Head }

// pruneAttempts bounds prune's listings: a delete fails as a whole when another session moved or
// deleted a listed ref since the listing, which a fresh listing resolves.
const pruneAttempts = 2

// prune deletes this worktree's retention refs not in named and every retention ref committed more
// than TTL before now. A ref deletes only while it holds the commit listed, so a ref another
// session re-dated after the listing is kept.
func (s *Store) prune(ctx context.Context, refs Refs, named map[Snapshot]time.Time, now time.Time) error {
	var err error
	for range pruneAttempts {
		if err = s.pruneOnce(ctx, refs, named, now); err == nil {
			return nil
		}
	}
	return err
}

func (s *Store) pruneOnce(ctx context.Context, refs Refs, named map[Snapshot]time.Time, now time.Time) error {
	all, err := refs.RetentionRefs(ctx)
	if err != nil {
		return fmt.Errorf("prune retention refs: %w", err)
	}
	keep := map[string]bool{}
	for snap := range named {
		keep[s.refName(snap)] = true
	}
	cutoff := now.Add(-TTL)
	var stale []source.RetentionRef
	for _, r := range all {
		mine := strings.HasPrefix(r.Name, s.refPrefix)
		// A commit date is whole seconds, the delivery time it records truncated: the ref is old
		// only once all of its second is, so it never goes while a ledger still keeps its snapshot.
		old := !r.Committed.Add(time.Second).After(cutoff)
		if mine && !keep[r.Name] || old {
			stale = append(stale, r)
		}
	}
	if err := refs.DeleteRefs(ctx, stale); err != nil {
		return fmt.Errorf("prune retention refs: %w", err)
	}
	return nil
}

// lock takes the exclusive ledger lock, waiting at most lockWait. The lock file is never renamed
// or deleted, so a lock always guards the file other sessions lock. The wait also absorbs a
// transient hold no session owns: a git child forked by this process shares the lock's open file
// until its exec closes it, so a non-blocking attempt right after an unlock can fail.
func (s *Store) lock() (unlock func() error, err error) {
	f, err := os.OpenFile(s.lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open ledger lock: %w", err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			return f.Close, nil // closing releases the flock
		}
		if !errors.Is(lockErr, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			if errors.Is(lockErr, syscall.EWOULDBLOCK) {
				lockErr = fmt.Errorf("another rvw session held it for %v", lockWait)
			}
			return nil, errors.Join(fmt.Errorf("lock review ledger: %w", lockErr), f.Close())
		}
		time.Sleep(lockPoll)
	}
}

// read returns the ledger, empty when there is none. A file that is not a valid ledger of this
// format gives an *invalidLedgerError; any other error is a failure to read.
func (s *Store) read() (Ledger, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Ledger{}, nil
	}
	if err != nil {
		return Ledger{}, fmt.Errorf("read review ledger: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxLedgerBytes+1))
	if err := errors.Join(readErr, f.Close()); err != nil {
		return Ledger{}, fmt.Errorf("read review ledger: %w", err)
	}
	if len(data) > maxLedgerBytes {
		return Ledger{}, invalidf("it exceeds %d bytes", maxLedgerBytes)
	}
	return decode(data)
}

// write replaces the ledger atomically: temp file, fsync, rename, then fsync the directory.
// write replaces the ledger with data atomically. replaced reports whether the new file is in
// place, so a failure syncing its directory afterwards is told apart from one that left the old.
func (s *Store) write(data []byte) (replaced bool, err error) {
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return false, fmt.Errorf("write review ledger: %w", err)
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	err = errors.Join(err, tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), s.path)
	}
	if err != nil {
		return false, errors.Join(fmt.Errorf("write review ledger: %w", err), os.Remove(tmp.Name()))
	}
	return true, syncDir(dir)
}

// moveAside renames the ledger to a sibling whose name holds discardedMarker and returns its path.
// Its mtime is reset so ExpireLedgers keeps it for TTL from now.
func (s *Store) moveAside() (string, error) {
	to := fmt.Sprintf("%s%s%d.json", strings.TrimSuffix(s.path, ".json"), discardedMarker, time.Now().UnixNano())
	if err := os.Rename(s.path, to); err != nil {
		return "", fmt.Errorf("move review ledger aside: %w", err)
	}
	now := time.Now()
	if err := os.Chtimes(to, now, now); err != nil {
		return "", fmt.Errorf("move review ledger aside: %w", err)
	}
	return to, syncDir(filepath.Dir(to))
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: dir is the ledger directory
	if err != nil {
		return fmt.Errorf("sync ledger directory: %w", err)
	}
	if err := errors.Join(d.Sync(), d.Close()); err != nil {
		return fmt.Errorf("sync ledger directory: %w", err)
	}
	return nil
}

// ExpireLedgers deletes ledgers moved aside and temporary ledger files in dir older than TTL
// (REQ-RND-007). Ledgers themselves are never deleted: once nothing is left in TTL, a ledger names
// no snapshot and keeps only the last round's Hide whitespace setting and time. Expiry is
// housekeeping: the caller reports the error and continues.
func ExpireLedgers(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("expire review ledgers: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), tempPrefix) && !strings.Contains(e.Name(), discardedMarker) {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) >= TTL {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("expire review ledger: %w", err))
			}
		}
	}
	return errors.Join(errs...)
}

// sortedKeys returns m's keys by old path, then new path.
func sortedKeys[V any](m map[FileKey]V) []FileKey {
	return slices.SortedFunc(maps.Keys(m), func(a, b FileKey) int {
		if a.OldPath != b.OldPath {
			return strings.Compare(a.OldPath, b.OldPath)
		}
		return strings.Compare(a.NewPath, b.NewPath)
	})
}
