package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jacoelho/rvw/internal/source"
)

// draftTTL is how long an unused draft is kept.
const draftTTL = 30 * 24 * time.Hour

// tempDraftPrefix starts the name of a draft being written. A process killed between creating
// such a file and renaming it leaves the file behind; ExpireDrafts removes it.
const tempDraftPrefix = ".draft-"

// maxDraftBytes bounds a draft file read: the comment limits plus summary and viewed marks.
const maxDraftBytes = 64 << 20

// DraftDir returns ~/Library/Application Support/rvw/drafts.
func DraftDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate draft directory: %w", err)
	}
	return filepath.Join(base, "rvw", "drafts"), nil
}

// DraftKey identifies a snapshot's draft by its repository, source kind and trees (REQ-DRF-002).
// A branch source's base ref is left out: the base tree already identifies the base, so every
// spelling of one base, the origin/HEAD default included, shares one draft (Q-38). The key does
// not encode the draft format, so a draft written by an older format is found and moved aside
// rather than ignored.
func DraftKey(snap *source.Snapshot) string {
	src := snap.Source()
	var buf []byte
	// The length prefixes keep the parts unambiguous.
	for _, part := range []string{
		"rvw-draft-v1", snap.CommonDir(), snap.TopLevel(), string(src.Kind()),
		fmt.Sprint(src.IncludesUncommitted()), snap.BaseTree(), snap.HeadTree(),
	} {
		buf = fmt.Appendf(buf, "%d:%s\n", len(part), part)
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// LockedError reports a draft held by another session. URL is empty while that session is still
// starting.
type LockedError struct{ URL string }

func (e *LockedError) Error() string {
	if e.URL == "" {
		return "another rvw session for this snapshot is starting"
	}
	return "another rvw session is reviewing this snapshot: " + e.URL
}

// Store is one draft file and the lock that makes its session the only writer.
type Store struct {
	lock      *os.File
	draftPath string
	key       string
}

// OpenStore takes the exclusive lock for key in dir, creating dir if needed.
func OpenStore(dir, key string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create draft directory: %w", err)
	}
	lockPath := filepath.Join(dir, key+".lock")
	lock, err := lockFile(lockPath)
	if err != nil {
		return nil, err
	}
	return &Store{draftPath: filepath.Join(dir, key+".json"), lock: lock, key: key}, nil
}

// lockFile opens and flocks path. Expiry may unlink a lock file, so after locking it checks that
// the locked inode is still the one at path and retries otherwise.
func lockFile(path string) (*os.File, error) {
	for range 10 {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // path built from a hex key
		if err != nil {
			return nil, fmt.Errorf("open draft lock: %w", err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			if !errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, errors.Join(fmt.Errorf("lock draft: %w", err), f.Close())
			}
			url := lockedURL(f)
			if err := f.Close(); err != nil {
				return nil, fmt.Errorf("close draft lock: %w", err)
			}
			return nil, &LockedError{URL: url}
		}
		held, err1 := f.Stat()
		current, err2 := os.Stat(path)
		if err1 == nil && err2 == nil && os.SameFile(held, current) {
			// A killed session leaves its URL behind; clear it so others report "starting".
			if err := f.Truncate(0); err != nil {
				return nil, errors.Join(fmt.Errorf("clear draft lock: %w", err), f.Close())
			}
			return f, nil
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("close draft lock: %w", err)
		}
	}
	return nil, errors.New("lock draft: lock file keeps changing")
}

// lockedURL reads the URL another session recorded in its lock file. An unreadable URL reads as
// empty, which reports that session as still starting.
func lockedURL(f *os.File) string {
	data, err := io.ReadAll(io.LimitReader(f, 4<<10))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SetURL records the session URL in the lock file for a second instance to print.
func (s *Store) SetURL(url string) error {
	if err := s.lock.Truncate(0); err != nil {
		return fmt.Errorf("record session URL: %w", err)
	}
	if _, err := s.lock.WriteAt([]byte(url+"\n"), 0); err != nil {
		return fmt.Errorf("record session URL: %w", err)
	}
	return nil
}

// Close clears the recorded URL and releases the lock.
func (s *Store) Close() error {
	var errs []error
	if err := s.lock.Truncate(0); err != nil {
		errs = append(errs, fmt.Errorf("clear session URL: %w", err))
	}
	if err := s.lock.Close(); err != nil {
		errs = append(errs, fmt.Errorf("release draft lock: %w", err))
	}
	return errors.Join(errs...)
}

// Path is the draft file's path.
func (s *Store) Path() string { return s.draftPath }

// errNoDraft is returned by load when there is no draft.
var errNoDraft = errors.New("no saved draft")

// unusableDraftError reports a draft file that cannot be read as a draft of this format.
type unusableDraftError struct{ reason string }

func (e *unusableDraftError) Error() string { return e.reason }

// load reads the draft file. It returns errNoDraft when there is none, an *unusableDraftError
// when the file is not a draft of this format, and any other error when reading fails.
func (s *Store) load() (draftFile, error) {
	data, err := readLimited(s.draftPath, maxDraftBytes+1)
	if errors.Is(err, os.ErrNotExist) {
		return draftFile{}, errNoDraft
	}
	if err != nil {
		return draftFile{}, fmt.Errorf("read draft: %w", err)
	}
	if len(data) > maxDraftBytes {
		return draftFile{}, &unusableDraftError{fmt.Sprintf("it exceeds %d bytes", maxDraftBytes)}
	}
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return draftFile{}, &unusableDraftError{"it is corrupt: " + err.Error()}
	}
	if probe.Version != draftVersion {
		return draftFile{}, &unusableDraftError{fmt.Sprintf("it has unsupported version %d", probe.Version)}
	}
	var d draftFile
	if err := decodeStrict(data, &d); err != nil {
		return draftFile{}, &unusableDraftError{"it is corrupt: " + err.Error()}
	}
	// Touch so expiry measures time since last use.
	now := time.Now()
	if err := os.Chtimes(s.draftPath, now, now); err != nil {
		return draftFile{}, fmt.Errorf("touch draft: %w", err)
	}
	return d, nil
}

// discardedMarker is in the name of every moved-aside draft; ExpireDrafts deletes them.
const discardedMarker = ".discarded-"

// moveAside renames the draft to a unique sibling and returns its path. The caller holds the
// lock, so no other session reads or writes the draft meanwhile. The new name's mtime is reset so
// it is kept for draftTTL from now.
func (s *Store) moveAside() (string, error) {
	base := strings.TrimSuffix(s.draftPath, ".json")
	to := fmt.Sprintf("%s%s%d.json", base, discardedMarker, time.Now().UnixNano())
	if err := os.Rename(s.draftPath, to); err != nil {
		return "", fmt.Errorf("move draft aside: %w", err)
	}
	now := time.Now()
	if err := os.Chtimes(to, now, now); err != nil {
		return "", fmt.Errorf("move draft aside: %w", err)
	}
	if err := syncDir(filepath.Dir(to)); err != nil {
		return "", err
	}
	return to, nil
}

// moveAsideIfAny moves the draft aside as moveAside does and returns its new path, or "" when there
// is no draft.
func (s *Store) moveAsideIfAny() (string, error) {
	if _, err := os.Lstat(s.draftPath); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return s.moveAside()
}

// readLimited reads at most limit bytes of the file at path.
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: path is the draft path built from a hex key
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	return data, errors.Join(err, f.Close())
}

// save writes the draft atomically: temp file, fsync, rename, then fsync the directory.
func (s *Store) save(d draftFile) error {
	data, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encode draft: %w", err)
	}
	dir := filepath.Dir(s.draftPath)
	tmp, err := os.CreateTemp(dir, tempDraftPrefix+"*")
	if err != nil {
		return fmt.Errorf("write draft: %w", err)
	}
	if err := writeSynced(tmp, data); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), s.draftPath); err != nil {
		return errors.Join(fmt.Errorf("replace draft: %w", err), os.Remove(tmp.Name()))
	}
	return syncDir(dir)
}

// writeSynced writes data to f, fsyncs and closes it.
func writeSynced(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write draft: %w", err), f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync draft: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write draft: %w", err)
	}
	return nil
}

// syncDir fsyncs dir so a rename inside it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: dir is the draft directory, not external input
	if err != nil {
		return fmt.Errorf("sync draft directory: %w", err)
	}
	if err := d.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync draft directory: %w", err), d.Close())
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("sync draft directory: %w", err)
	}
	return nil
}

func (s *Store) delete() error {
	if err := os.Remove(s.draftPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete draft: %w", err)
	}
	return nil
}

// ExpireDrafts deletes drafts in dir unused for 30 days whose lock is free, drafts moved aside
// more than 30 days ago, and temporary draft files older than 30 days. Expiry is housekeeping: the
// caller reports the returned error and continues the review.
func ExpireDrafts(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("expire drafts: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempDraftPrefix) {
			// A live session's temporary file exists for milliseconds, so an old one is a leftover
			// and needs no lock.
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) >= draftTTL {
				if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
					errs = append(errs, fmt.Errorf("expire temporary draft: %w", err))
				}
			}
			continue
		}
		if strings.Contains(e.Name(), discardedMarker) {
			// A moved-aside draft is never reopened, so it needs no lock.
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) >= draftTTL {
				if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
					errs = append(errs, fmt.Errorf("expire discarded draft: %w", err))
				}
			}
			continue
		}
		key, ok := strings.CutSuffix(e.Name(), ".lock")
		if !ok {
			continue
		}
		draft := filepath.Join(dir, key+".json")
		lockInfo, err := e.Info()
		if err != nil {
			continue
		}
		lastUse := lockInfo.ModTime()
		if info, statErr := os.Stat(draft); statErr == nil && info.ModTime().After(lastUse) {
			lastUse = info.ModTime()
		}
		if now.Sub(lastUse) < draftTTL {
			continue
		}
		lock, err := lockFile(filepath.Join(dir, e.Name()))
		if err != nil {
			// A held lock is a live session; anything else is reported.
			if _, held := errors.AsType[*LockedError](err); !held {
				errs = append(errs, err)
			}
			continue
		}
		if err := os.Remove(draft); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("expire draft: %w", err))
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("expire draft lock: %w", err))
		}
		if err := lock.Close(); err != nil {
			errs = append(errs, fmt.Errorf("expire draft lock: %w", err))
		}
	}
	return errors.Join(errs...)
}
