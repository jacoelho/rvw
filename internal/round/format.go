package round

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// ledgerVersion is the persisted ledger format. A ledger of another version is moved aside, not
// migrated (REQ-RND-007).
const ledgerVersion = 1

// ledgerFile is the persisted ledger. Its types belong to this format alone; decode validates
// every value, because blob and tree IDs reach git commands.
type ledgerFile struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	Version        int            `json:"version"`
	LastAt         time.Time      `json:"last_at"`
	HideWhitespace bool           `json:"hide_whitespace"`
	Reviewed       []reviewedFile `json:"reviewed"`
	Comments       commentsFile   `json:"comments"`
}

type reviewedFile struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	OldPath    string    `json:"old_path"`
	NewPath    string    `json:"new_path"`
	OldMode    string    `json:"old_mode"`
	OldBlob    string    `json:"old_blob"`
	NewMode    string    `json:"new_mode"`
	NewBlob    string    `json:"new_blob"`
	Whitespace bool      `json:"whitespace"`
	At         time.Time `json:"at"`
	Base       string    `json:"base"`
	Head       string    `json:"head"`
}

type commentsFile struct {
	At    time.Time     `json:"at"`
	Base  string        `json:"base"`
	Head  string        `json:"head"`
	Items []commentFile `json:"items"`
}

// targetFile is an earlier comment's target, the ledger's own shape like the draft's, so a change
// to the agent-facing formats cannot change it.
type targetFile struct {
	Kind  string `json:"kind"`
	Path  string `json:"path,omitzero"`
	Side  string `json:"side,omitzero"`
	Start int    `json:"start,omitzero"`
	End   int    `json:"end,omitzero"`
}

func targetFileOf(t review.Target) targetFile {
	start, end := t.Lines()
	return targetFile{Kind: string(t.Kind()), Path: t.Path(), Side: string(t.Side()), Start: start, End: end}
}

func (f targetFile) target() (review.Target, error) {
	switch review.TargetKind(f.Kind) {
	case review.TargetReview:
		if f != (targetFile{Kind: f.Kind}) {
			return review.Target{}, errors.New("a review target takes no path, side or lines")
		}
		return review.ReviewTarget(), nil
	case review.TargetFile:
		if f.Side != "" || f.Start != 0 || f.End != 0 {
			return review.Target{}, errors.New("a file target takes no side or lines")
		}
		return review.FileTarget(f.Path)
	case review.TargetLine:
		if f.End != f.Start {
			return review.Target{}, errors.New("a line target ends where it starts")
		}
		return review.LineTarget(f.Path, review.Side(f.Side), f.Start)
	case review.TargetRange:
		return review.RangeTarget(f.Path, review.Side(f.Side), f.Start, f.End)
	default:
		return review.Target{}, fmt.Errorf("unknown target kind %q", f.Kind)
	}
}

type commentFile struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	ID      string     `json:"id"`
	Author  string     `json:"author"`
	Body    string     `json:"body"`
	Target  targetFile `json:"target"`
	OldPath string     `json:"old_path"`
	NewPath string     `json:"new_path"`
	Mode    string     `json:"mode"`
	Blob    string     `json:"blob"`
}

// encode returns l's persisted form, reviewed versions in file key order.
func (l Ledger) encode() ([]byte, error) {
	f := ledgerFile{
		Version: ledgerVersion, LastAt: l.lastAt, HideWhitespace: l.hideWhitespace,
		Reviewed: make([]reviewedFile, 0, len(l.reviewed)),
		Comments: commentsFile{
			At: l.comments.At, Base: l.comments.Snapshot.Base, Head: l.comments.Snapshot.Head,
			Items: make([]commentFile, 0, len(l.comments.Items)),
		},
	}
	for _, key := range sortedKeys(l.reviewed) {
		r := l.reviewed[key]
		f.Reviewed = append(f.Reviewed, reviewedFile{
			OldPath: key.OldPath, NewPath: key.NewPath,
			OldMode: r.Version.Old.Mode, OldBlob: r.Version.Old.ID,
			NewMode: r.Version.New.Mode, NewBlob: r.Version.New.ID,
			Whitespace: r.Whitespace, At: r.At, Base: r.Snapshot.Base, Head: r.Snapshot.Head,
		})
	}
	for _, c := range l.comments.Items {
		f.Comments.Items = append(f.Comments.Items, commentFile{
			ID: c.ID, Author: string(c.Author), Body: c.Body, Target: targetFileOf(c.Target),
			OldPath: c.Key.OldPath, NewPath: c.Key.NewPath, Mode: c.Blob.Mode, Blob: c.Blob.ID,
		})
	}
	data, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("encode ledger: %w", err)
	}
	return data, nil
}

// invalidLedgerError reports a ledger file that is not a valid ledger of this format.
type invalidLedgerError struct{ reason string }

func (e *invalidLedgerError) Error() string { return e.reason }

func invalidf(format string, args ...any) error {
	return &invalidLedgerError{fmt.Sprintf(format, args...)}
}

// decode parses and validates a persisted ledger. Every failure is an *invalidLedgerError.
func decode(data []byte) (Ledger, error) {
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return Ledger{}, invalidf("it is corrupt: %v", err)
	}
	if probe.Version != ledgerVersion {
		return Ledger{}, invalidf("it has unsupported version %d", probe.Version)
	}
	var f ledgerFile
	if err := json.Unmarshal(data, &f, json.RejectUnknownMembers(true)); err != nil {
		return Ledger{}, invalidf("it is corrupt: %v", err)
	}
	l := Ledger{reviewed: make(map[FileKey]Reviewed, len(f.Reviewed)), lastAt: f.LastAt, hideWhitespace: f.HideWhitespace}
	if len(f.Reviewed) > MaxReviewed {
		return Ledger{}, invalidf("it holds %d reviewed versions, over %d", len(f.Reviewed), MaxReviewed)
	}
	for k, r := range f.Reviewed {
		key := FileKey{OldPath: r.OldPath, NewPath: r.NewPath}
		v := Version{Old: source.Blob{Mode: r.OldMode, ID: r.OldBlob}, New: source.Blob{Mode: r.NewMode, ID: r.NewBlob}}
		snap := Snapshot{Base: r.Base, Head: r.Head}
		err := errors.Join(checkKeyed(key, v), checkSnapshot(snap), checkAt(r.At, f.LastAt))
		if err == nil {
			if _, dup := l.reviewed[key]; dup {
				err = errors.New("the file key is repeated")
			}
		}
		if err != nil {
			return Ledger{}, invalidf("reviewed version %d: %v", k, err)
		}
		l.reviewed[key] = Reviewed{At: r.At, Snapshot: snap, Version: v, Whitespace: r.Whitespace}
	}
	comments, err := decodeComments(f.Comments, f.LastAt)
	if err != nil {
		return Ledger{}, err
	}
	l.comments = comments
	if l.lastAt.IsZero() && (len(l.reviewed) > 0 || len(l.comments.Items) > 0) {
		return Ledger{}, invalidf("it records rounds but no last round time")
	}
	return l, nil
}

// earlierID is an earlier comment ID: 16 lowercase hexadecimal digits (Delivered).
var earlierID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func decodeComments(f commentsFile, lastAt time.Time) (Comments, error) {
	// A delivery with no comments still records its time and snapshot: an older delivery merged
	// later must not replace its empty list (merge).
	if len(f.Items) == 0 && f.At.IsZero() && f.Base == "" && f.Head == "" {
		return Comments{}, nil
	}
	snap := Snapshot{Base: f.Base, Head: f.Head}
	if err := errors.Join(checkSnapshot(snap), checkAt(f.At, lastAt)); err != nil {
		return Comments{}, invalidf("comments: %v", err)
	}
	if len(f.Items) > review.MaxComments {
		return Comments{}, invalidf("comments: %d comments, over %d", len(f.Items), review.MaxComments)
	}
	out := Comments{At: f.At, Snapshot: snap, Items: make([]Earlier, 0, len(f.Items))}
	ids := map[string]bool{}
	total := 0
	for k, c := range f.Items {
		e, err := decodeComment(c)
		if err == nil && ids[e.ID] {
			err = errors.New("the id is repeated")
		}
		if err != nil {
			return Comments{}, invalidf("comment %d: %v", k, err)
		}
		ids[e.ID] = true
		if total += len(e.Body); total > review.MaxTotalBytes {
			return Comments{}, invalidf("comments: bodies exceed %d bytes", review.MaxTotalBytes)
		}
		out.Items = append(out.Items, e)
	}
	return out, nil
}

func decodeComment(c commentFile) (Earlier, error) {
	if !earlierID.MatchString(c.ID) {
		return Earlier{}, fmt.Errorf("invalid id %q", c.ID)
	}
	author := review.Author(c.Author)
	if author != review.Human && author != review.Agent {
		return Earlier{}, fmt.Errorf("unknown author %q", c.Author)
	}
	if c.Body == "" || len(c.Body) > review.MaxBodyBytes {
		return Earlier{}, errors.New("the body is empty or too long")
	}
	t, err := c.Target.target()
	if err != nil {
		return Earlier{}, err
	}
	e := Earlier{
		ID: c.ID, Author: author, Body: c.Body, Target: t,
		Key: FileKey{OldPath: c.OldPath, NewPath: c.NewPath}, Blob: source.Blob{Mode: c.Mode, ID: c.Blob},
	}
	switch t.Kind() {
	case review.TargetReview:
		if e.Key != (FileKey{}) || e.Blob != (source.Blob{}) {
			return Earlier{}, errors.New("a review comment has a file")
		}
	case review.TargetFile:
		err = errors.Join(checkKey(e.Key, t.Path()), checkNoBlob(e.Blob))
	case review.TargetLine, review.TargetRange:
		err = errors.Join(checkKey(e.Key, t.Path()), e.Blob.Validate())
		if err == nil && e.Blob.Submodule() {
			err = errors.New("a line comment is on a gitlink")
		}
		if err == nil && (t.Side() == review.Old && e.Key.OldPath == "" || t.Side() == review.New && e.Key.NewPath == "") {
			err = errors.New("the commented side is absent")
		}
	}
	if err != nil {
		return Earlier{}, err
	}
	return e, nil
}

// checkKey checks a commented file's key against its target's displayed path: the new path, or the
// old path of a deleted file.
func checkKey(k FileKey, path string) error {
	if k == (FileKey{}) {
		return errors.New("the file key is empty")
	}
	shown := k.NewPath
	if shown == "" {
		shown = k.OldPath
	}
	if shown != path {
		return fmt.Errorf("the target path %q is not the file's %q", path, shown)
	}
	return nil
}

func checkNoBlob(b source.Blob) error {
	if b != (source.Blob{}) {
		return errors.New("a file comment has a blob")
	}
	return nil
}

// checkKeyed checks that a version has exactly the sides its key has.
func checkKeyed(k FileKey, v Version) error {
	if k == (FileKey{}) {
		return errors.New("the file key is empty")
	}
	side := func(path string, b source.Blob) error {
		if path == "" {
			return checkAbsent(b)
		}
		return b.Validate()
	}
	return errors.Join(side(k.OldPath, v.Old), side(k.NewPath, v.New))
}

func checkAbsent(b source.Blob) error {
	if b != (source.Blob{}) {
		return errors.New("an absent side has a blob")
	}
	return nil
}

func checkSnapshot(s Snapshot) error {
	if !source.ValidObjectID(s.Base) || !source.ValidObjectID(s.Head) {
		return fmt.Errorf("invalid tree ids %q, %q", s.Base, s.Head)
	}
	return nil
}

func checkAt(at, lastAt time.Time) error {
	if at.IsZero() || at.After(lastAt) {
		return fmt.Errorf("delivery time %v is zero or after the last round", at)
	}
	return nil
}
