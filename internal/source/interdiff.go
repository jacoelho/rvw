package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Blob is one side of a file version: its Git mode and blob ID. The zero Blob is an absent side.
type Blob struct{ Mode, ID string }

// Side returns f's new or old side; the zero Blob when f lacks it.
func (f ChangedFile) Side(newSide bool) Blob {
	b := Blob{Mode: f.OldMode, ID: f.OldBlob}
	if newSide {
		b = Blob{Mode: f.NewMode, ID: f.NewBlob}
	}
	if b.ID == "" {
		return Blob{}
	}
	return b
}

// Submodule reports whether b is a gitlink, whose ID names a commit.
func (b Blob) Submodule() bool { return b.Mode == modeSubmodule }

// modeSymlink is a symbolic link's mode.
const modeSymlink = "120000"

// Validate reports whether b names a present side as the changed-file list does: a regular,
// executable, symlink or gitlink mode and a full hexadecimal SHA-1 or SHA-256 object ID. It is the
// check for a side read from outside git, such as a stored reviewed version, before it reaches a
// git command.
func (b Blob) Validate() error {
	switch b.Mode {
	case modeRegular, modeExecutable, modeSymlink, modeSubmodule:
	default:
		return fmt.Errorf("invalid file mode %q", b.Mode)
	}
	if !ValidObjectID(b.ID) {
		return fmt.Errorf("invalid blob id %q", b.ID)
	}
	return nil
}

// ValidObjectID reports whether id is a full lowercase hexadecimal SHA-1 or SHA-256 object ID other
// than the zero ID.
func ValidObjectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, c := range []byte(id) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return !isZeroID(id)
}

// symlink reports whether mode is a symbolic link's; regular and executable files compare as one
// kind, so a diff between them is a mode change and not a type change.
func symlink(mode string) bool { return mode == modeSymlink }

// ErrNotText reports two versions that git does not compare as text: one is binary, a gitlink or
// over a large-file limit, or one is a symlink and the other a file.
var ErrNotText = errors.New("the versions cannot be compared as text")

// textSides is two blobs read for a text comparison, with their facts.
type textSides struct {
	a, b           []byte
	aFacts, bFacts SideFacts
}

// comparableKinds reports whether a and b are of kinds git compares as text: neither a gitlink, and
// both or neither symlinks.
func comparableKinds(a, b Blob) bool {
	return !a.Submodule() && !b.Submodule() && symlink(a.Mode) == symlink(b.Mode)
}

// readTextSides reads a then b for a text comparison. It fails with ErrNotText when
// !comparableKinds(a, b) or either side is large (REQ-PERF-002: repo.side returns nil data), and
// with ErrMissing when either blob is absent.
func (r *Repo) readTextSides(ctx context.Context, a, b Blob) (textSides, error) {
	if !comparableKinds(a, b) {
		return textSides{}, ErrNotText
	}
	var t textSides
	var err error
	if t.a, t.aFacts, err = r.side(ctx, a.ID); err != nil {
		return textSides{}, err
	}
	if t.b, t.bFacts, err = r.side(ctx, b.ID); err != nil {
		return textSides{}, err
	}
	if t.a == nil || t.b == nil {
		return textSides{}, ErrNotText
	}
	return t, nil
}

// Interdiff returns the diff from reviewed, a new side the human reviewed earlier, to changed file
// i's new side, under the file's new path (Definitions: interdiff). Its Old is reviewed's content
// and its New the snapshot's, so line numbers on the new side are snapshot lines and Context
// expands the interdiff's gaps.
//
// It fails with ErrMissing when reviewed's blob is absent locally, and with ErrNotText when the
// sides are not comparable as text or either is large; any other error is a failure to run git.
func (s *Snapshot) Interdiff(ctx context.Context, i int, reviewed Blob) (FileDiff, error) {
	if i < 0 || i >= s.FileCount() {
		return FileDiff{}, fmt.Errorf("file index %d out of range", i)
	}
	if err := reviewed.Validate(); err != nil {
		return FileDiff{}, err
	}
	f := s.File(i)
	if f.NewBlob == "" || f.Missing {
		return FileDiff{}, fmt.Errorf("%s: %w", f.Path(), ErrNotText)
	}
	sides, err := s.repo.readTextSides(ctx, reviewed, f.Side(true))
	if err != nil {
		return FileDiff{}, fmt.Errorf("%s: %w", f.Path(), err)
	}
	record := pairRecord(reviewed, f.Side(true), f.NewPath)
	binary, hunks, err := s.repo.recordHunks(ctx, record, "-p")
	if err != nil {
		return FileDiff{}, fmt.Errorf("%s: %w", f.Path(), err)
	}
	if !binary && len(hunks) == 0 {
		// Equal content prints no "Binary files" line, so ask git's binary detection directly.
		if binary, err = s.repo.recordBinary(ctx, record); err != nil {
			return FileDiff{}, fmt.Errorf("%s: %w", f.Path(), err)
		}
	}
	if binary {
		return FileDiff{}, fmt.Errorf("%s: %w", f.Path(), ErrNotText)
	}
	d := FileDiff{
		Hunks: hunks, Old: sides.a, New: sides.b, record: record,
		Facts: FileFacts{Kind: KindText, Old: sides.aFacts, New: sides.bFacts, Hunks: make([]HunkRange, len(hunks))},
	}
	for k, h := range hunks {
		d.Facts.Hunks[k] = h.HunkRange
	}
	return d, nil
}

// ChangesFrom returns the hunk ranges of the diff, without context lines, from blob from to
// changed file i's new (newSide) or old side, as `git diff-pairs` reports them for a raw record
// pairing the two under the side's mode: each range's old lines are exactly the lines deleted and
// its new lines exactly the lines added. Equal blob IDs give no ranges without running git.
//
// It fails with ErrMissing when from's blob is absent locally and ErrNotText when either blob is a
// gitlink or over a large-file limit, one is a symlink and the other a file, or git compares them
// as binary; the file must have the side.
func (s *Snapshot) ChangesFrom(ctx context.Context, i int, newSide bool, from Blob) ([]HunkRange, error) {
	if i < 0 || i >= s.FileCount() {
		return nil, fmt.Errorf("file index %d out of range", i)
	}
	if err := from.Validate(); err != nil {
		return nil, err
	}
	f := s.File(i)
	cur := f.Side(newSide)
	if cur.ID == "" {
		return nil, fmt.Errorf("%s has no content on that side", f.Path())
	}
	// Only the two blobs diffed matter: a gitlink on the file's other side does not stop it.
	if !comparableKinds(from, cur) {
		return nil, fmt.Errorf("%s: %w", f.Path(), ErrNotText)
	}
	if cur.ID == from.ID {
		return nil, nil
	}
	if f.Missing {
		return nil, ErrMissing
	}
	// The sides are read, not only sized, because LargeLines needs a line count and nothing cheaper
	// provides one; at most LargeBytes per side, instead of a patch of up to 64 MiB.
	if _, err := s.repo.readTextSides(ctx, from, cur); err != nil {
		return nil, fmt.Errorf("%s: %w", f.Path(), err)
	}
	path := f.NewPath
	if !newSide {
		path = f.OldPath
	}
	// Both sides take the current mode, so a mode change never splits the patch into a deletion
	// and an addition: only the lines matter.
	binary, hunks, err := s.repo.recordHunks(ctx, pairRecord(Blob{Mode: cur.Mode, ID: from.ID}, cur, path), "-p", "-U0")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Path(), err)
	}
	if binary {
		return nil, fmt.Errorf("%s: %w", f.Path(), ErrNotText)
	}
	out := make([]HunkRange, len(hunks))
	for k, h := range hunks {
		out[k] = h.HunkRange
	}
	return out, nil
}

// pairRecord is a raw record pairing two sides of one path as a modification.
func pairRecord(from, to Blob, path string) string {
	return fmt.Sprintf(":%s %s %s %s M\x00%s\x00", from.Mode, to.Mode, from.ID, to.ID, path)
}

// recordHunks runs diff-pairs with format on one record whose sides are of one kind, so its patch
// has at most one section, and parses it. Identical sides print no section.
func (r *Repo) recordHunks(ctx context.Context, record string, format ...string) (binary bool, hunks []Hunk, err error) {
	out, err := r.run.outputStdin(ctx, maxPatch, nil, strings.NewReader(record), diffPairs(format...)...)
	if err != nil {
		return false, nil, err
	}
	sections := splitSections(out)
	switch len(sections) {
	case 0:
		return false, nil, nil
	case 1:
		return parsePatch(sections[0])
	default:
		return false, nil, fmt.Errorf("%d patch sections, want 1", len(sections))
	}
}

// recordBinary reports whether git's numstat marks the record's sides as binary.
func (r *Repo) recordBinary(ctx context.Context, record string) (bool, error) {
	out, err := r.run.outputStdin(ctx, maxReplyLine+int64(len(record)), nil, strings.NewReader(record), diffPairs("--numstat")...)
	if err != nil {
		return false, err
	}
	if len(out) == 0 {
		return false, nil
	}
	c, _, _, err := readNumStat(bufioReader(out)) //nolint:dogsled // only the count matters for one record
	if err != nil {
		return false, fmt.Errorf("numstat: %w", err)
	}
	return c.Binary, nil
}

// BlobLines is Lines for a blob named by ID rather than by a changed file's side, such as an
// earlier comment's blob. It fails with ErrMissing when the blob is absent locally.
func (s *Snapshot) BlobLines(ctx context.Context, b Blob, ranges []LineRange, keep int) ([][]SideLine, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return s.repo.blobLines(ctx, b.ID, ranges, keep)
}

// MapLines maps lines start to end of a side through hunks, the zero-context diff from that side
// to another as ChangesFrom returns it. It fails when a hunk deletes one of the lines or adds a
// line between two of them; otherwise the lines move by the line count the hunks before them
// change.
func MapLines(hunks []HunkRange, start, end int) (newStart, newEnd int, ok bool) {
	shift := 0
	for _, h := range hunks {
		if h.OldCount == 0 {
			// A pure addition after old line OldStart.
			switch {
			case h.OldStart < start:
				shift += h.NewCount
			case h.OldStart < end:
				return 0, 0, false
			}
			continue
		}
		last := h.OldStart + h.OldCount - 1
		switch {
		case last < start:
			shift += h.NewCount - h.OldCount
		case h.OldStart <= end:
			return 0, 0, false
		}
	}
	return start + shift, end + shift, true
}
