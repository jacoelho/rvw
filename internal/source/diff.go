package source

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Large-file limits: a text file with a side over either is large.
const (
	LargeBytes = 1 << 20
	LargeLines = 20_000
)

// maxPatch bounds one file's patch output held in memory.
const maxPatch = 64 << 20

// FileKind says how a changed file can be presented. Its values carry no wire spelling: a
// boundary that shows one maps it explicitly.
type FileKind uint8

// File kinds. Only KindText has a line diff.
const (
	KindText FileKind = iota + 1
	KindBinary
	KindSubmodule
	KindModeOnly
	KindUnavailable
)

// LineOp is a patch line's operation.
type LineOp byte

// Patch line operations.
const (
	OpContext LineOp = ' '
	OpAdd     LineOp = '+'
	OpDelete  LineOp = '-'
)

// Line is one patch line without its operation prefix. Old and New are its line numbers on each
// side, numbered from the hunk header; a side the line is not on is 0.
type Line struct {
	Text     string
	Old, New int
	Op       LineOp
	NoEOL    bool // followed by "\ No newline at end of file"
}

// HunkRange is a hunk header's numbers.
type HunkRange struct {
	OldStart, OldCount int
	NewStart, NewCount int
}

// Hunk is one patch hunk.
type Hunk struct {
	Section string // text after the second @@, if any
	Lines   []Line
	HunkRange
}

// SideFacts describes one side of a changed file.
type SideFacts struct {
	Size int64
	// Lines is the side's line count, numbered as a patch numbers lines. It is 0 when the side is
	// absent, or larger than LargeBytes, in which case it is not counted.
	Lines   int
	Present bool
}

// FileFacts is what rvw knows about one changed file without keeping its content.
type FileFacts struct {
	// Hunks are the patch hunks' header numbers in patch order; nil unless Kind is KindText.
	Hunks    []HunkRange
	Old, New SideFacts
	Kind     FileKind
	// Large reports a text file either of whose sides exceeds LargeBytes or LargeLines. Only a
	// KindText file can be large.
	Large bool
}

// FileDiff is one changed file's presentation data, read on request and not retained.
type FileDiff struct {
	// Hunks[k].HunkRange equals Facts.Hunks[k].
	Hunks []Hunk
	// Old and New hold the sides' content: nil when the side is absent, or the file is large or
	// not text.
	Old, New []byte
	Facts    FileFacts
}

// Facts reads changed file i's facts: one patch run, plus a line count of each present side no
// larger than LargeBytes. It retains nothing.
func (s *Snapshot) Facts(ctx context.Context, i int) (FileFacts, error) {
	d, err := s.read(ctx, i, false)
	return d.Facts, err
}

// Diff reads changed file i: its facts, its hunks and, unless large, both sides' content.
func (s *Snapshot) Diff(ctx context.Context, i int) (FileDiff, error) {
	return s.read(ctx, i, true)
}

// read is Diff, keeping the hunk lines and side content only when keep is set.
func (s *Snapshot) read(ctx context.Context, i int, keep bool) (FileDiff, error) {
	if i < 0 || i >= s.FileCount() {
		return FileDiff{}, fmt.Errorf("file index %d out of range", i)
	}
	f := s.File(i)
	unavailable := FileDiff{Facts: FileFacts{Kind: KindUnavailable}}
	switch {
	case f.Missing:
		return unavailable, nil
	case f.Submodule():
		return FileDiff{Facts: FileFacts{Kind: KindSubmodule}}, nil
	}
	var d FileDiff
	oldData, oldFacts, errOld := s.repo.side(ctx, f.OldBlob)
	var newData []byte
	var newFacts SideFacts
	var errNew error
	if errOld == nil {
		newData, newFacts, errNew = s.repo.side(ctx, f.NewBlob)
	}
	if err := errors.Join(errOld, errNew); err != nil {
		if errors.Is(err, errMissing) {
			return unavailable, nil
		}
		return FileDiff{}, err
	}
	d.Facts.Old, d.Facts.New = oldFacts, newFacts
	d.Facts.Large = oldFacts.Present && oldData == nil || newFacts.Present && newData == nil
	patch, err := s.patch(ctx, i)
	if err != nil {
		return FileDiff{}, err
	}
	var binary bool
	var hunks []Hunk
	for _, section := range patch {
		b, h, parseErr := parsePatch(section)
		if parseErr != nil {
			return FileDiff{}, fmt.Errorf("%s: %w", f.Path(), parseErr)
		}
		binary = binary || b
		hunks = append(hunks, h...)
	}
	if !binary && len(hunks) == 0 && oldFacts.Present && newFacts.Present && f.OldMode == f.NewMode {
		// A pure rename has no content diff, so git prints no "Binary files" line for it; ask
		// git's binary detection directly so a renamed binary file is not shown as text.
		if binary, err = s.numstatBinary(ctx, i); err != nil {
			return FileDiff{}, fmt.Errorf("%s: %w", f.Path(), err)
		}
	}
	switch {
	case binary:
		return FileDiff{Facts: FileFacts{
			Kind: KindBinary,
			Old:  SideFacts{Present: oldFacts.Present, Size: oldFacts.Size},
			New:  SideFacts{Present: newFacts.Present, Size: newFacts.Size},
		}}, nil
	case len(hunks) == 0 && f.OldMode != f.NewMode && f.OldPath != "" && f.NewPath != "":
		d.Facts.Kind = KindModeOnly
		d.Facts.Large = false
		return d, nil
	}
	d.Facts.Kind = KindText
	d.Facts.Hunks = make([]HunkRange, len(hunks))
	for k, h := range hunks {
		d.Facts.Hunks[k] = h.HunkRange
	}
	if keep {
		d.Hunks = hunks
		if !d.Facts.Large {
			d.Old, d.New = oldData, newData
		}
	}
	return d, nil
}

// side reads a blob unless it exceeds LargeBytes; data is nil when the side is absent or large.
func (r *Repo) side(ctx context.Context, id string) (data []byte, facts SideFacts, err error) {
	if id == "" {
		return nil, SideFacts{}, nil
	}
	info, err := r.objectInfo(ctx, id)
	if err != nil {
		return nil, SideFacts{}, err
	}
	if info.missing {
		return nil, SideFacts{}, errMissing
	}
	facts = SideFacts{Present: true, Size: info.size}
	if info.size > LargeBytes {
		return nil, facts, nil
	}
	err = r.readBlob(ctx, id, func(size int64, body io.Reader) error {
		data = make([]byte, size)
		_, readErr := io.ReadFull(body, data)
		return readErr
	})
	if err != nil {
		return nil, SideFacts{}, err
	}
	facts.Lines = countLines(data)
	if facts.Lines > LargeLines {
		data = nil
	}
	return data, facts, nil
}

// countLines counts lines the way a patch numbers them: a final line without a newline counts.
func countLines(data []byte) int {
	n := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func sectionsOf(f ChangedFile) int {
	if f.Status == StatusType {
		return 2
	}
	return 1
}

// patch returns file i's patch sections, from diff-pairs fed the file's record from the listing.
func (s *Snapshot) patch(ctx context.Context, i int) ([][]byte, error) {
	f := s.files[i]
	out, err := s.repo.run.outputStdin(ctx, maxPatch, nil, strings.NewReader(s.records[i]), diffPairs("-p")...)
	if err != nil {
		return nil, err
	}
	// git splits a type change (for example, file to symlink) into a deletion section and an
	// addition section; every other record has one section.
	sections := splitSections(out)
	if len(sections) != sectionsOf(f) {
		return nil, fmt.Errorf("%s: %d patch sections, want %d", f.Path(), len(sections), sectionsOf(f))
	}
	return sections, nil
}

// numstatBinary reports whether git's numstat marks file i as binary.
func (s *Snapshot) numstatBinary(ctx context.Context, i int) (bool, error) {
	var count LineCount
	n := 0
	err := s.numStat(ctx, []int{i}, func(_ int, c LineCount) { count, n = c, n+1 })
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, fmt.Errorf("numstat: %d counts for one file", n)
	}
	return count.Binary, nil
}

func splitSections(patch []byte) [][]byte {
	marker := []byte("diff --git ")
	var out [][]byte
	start := -1
	for pos := 0; pos < len(patch); {
		end := bytes.IndexByte(patch[pos:], '\n')
		lineEnd := len(patch)
		if end >= 0 {
			lineEnd = pos + end + 1
		}
		if bytes.HasPrefix(patch[pos:], marker) {
			if start >= 0 {
				out = append(out, patch[start:pos])
			}
			start = pos
		}
		pos = lineEnd
	}
	if start >= 0 {
		out = append(out, patch[start:])
	}
	return out
}

// parsePatch parses one file section of the snapshot diff command's patch output.
func parsePatch(section []byte) (binary bool, hunks []Hunk, err error) {
	sc := bufio.NewScanner(bytes.NewReader(section))
	sc.Buffer(make([]byte, 64<<10), maxPatch)
	// Split on LF only: a CR before it belongs to the line's text.
	sc.Split(scanLF)
	inHunks := false
	var cur *Hunk
	var oldN, newN int // the next line number on each side
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "@@ "):
			h, err := parseHunkHeader(line)
			if err != nil {
				return false, nil, err
			}
			hunks = append(hunks, h)
			cur = &hunks[len(hunks)-1]
			oldN, newN = h.First()
			inHunks = true
		case !inHunks:
			if strings.HasPrefix(line, "Binary files ") {
				binary = true
			}
		case line == "":
			return false, nil, errors.New("patch: empty line inside a hunk")
		case line[0] == '\\':
			if n := len(cur.Lines); n > 0 {
				cur.Lines[n-1].NoEOL = true
			}
		case line[0] == ' ' || line[0] == '+' || line[0] == '-':
			l := Line{Op: LineOp(line[0]), Text: line[1:]}
			if l.Op != OpAdd {
				l.Old = oldN
				oldN++
			}
			if l.Op != OpDelete {
				l.New = newN
				newN++
			}
			cur.Lines = append(cur.Lines, l)
		default:
			return false, nil, fmt.Errorf("patch: unexpected line %q", truncate([]byte(line), 40))
		}
	}
	if err := sc.Err(); err != nil {
		return false, nil, fmt.Errorf("patch: %w", err)
	}
	return binary, hunks, nil
}

func scanLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if k := bytes.IndexByte(data, '\n'); k >= 0 {
		return k + 1, data[:k], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func parseHunkHeader(line string) (Hunk, error) {
	rest := strings.TrimPrefix(line, "@@ ")
	ranges, section, ok := strings.Cut(rest, " @@")
	if !ok {
		return Hunk{}, fmt.Errorf("patch: bad hunk header %q", line)
	}
	oldR, newR, ok := strings.Cut(ranges, " ")
	if !ok || !strings.HasPrefix(oldR, "-") || !strings.HasPrefix(newR, "+") {
		return Hunk{}, fmt.Errorf("patch: bad hunk header %q", line)
	}
	var h Hunk
	var err1, err2 error
	h.OldStart, h.OldCount, err1 = parseRange(oldR[1:])
	h.NewStart, h.NewCount, err2 = parseRange(newR[1:])
	if err1 != nil || err2 != nil {
		return Hunk{}, fmt.Errorf("patch: bad hunk header %q", line)
	}
	h.Section = strings.TrimPrefix(section, " ")
	return h, nil
}

func parseRange(s string) (start, count int, err error) {
	a, b, hasCount := strings.Cut(s, ",")
	if start, err = strconv.Atoi(a); err != nil {
		return 0, 0, err
	}
	count = 1
	if hasCount {
		count, err = strconv.Atoi(b)
	}
	return start, count, err
}

// SideLine is one line of a file side.
type SideLine struct {
	// Text is the line's first bytes, at most the keep given to Lines; it may end mid-rune.
	Text string
	// Bytes is the whole line's length without its LF.
	Bytes int
}

// LineRange is an inclusive range of 1-based line numbers; valid ranges have 1 <= Start <= End.
type LineRange struct{ Start, End int }

// Lines returns the lines of file i's old or new side for each of ranges: len(out) == len(ranges)
// and out[k] holds ranges[k]'s lines in order. Ranges may be unsorted, overlap or repeat. A range
// extending past the side's last line gets only the lines that exist, possibly none; that is not
// an error. Each SideLine keeps at most min(keep, LargeBytes) bytes of its line, none when
// keep <= 0, and reports the whole line's length.
//
// Lines streams the side's blob once per call and retains only lines inside some range, so its
// memory is bounded by the ranges' total length whatever the blob holds; with no ranges it reads
// no blob. It returns an error and no lines when i is out of range, the side has no content, a
// range is invalid, or reading the blob fails.
func (s *Snapshot) Lines(ctx context.Context, i int, newSide bool, ranges []LineRange, keep int) ([][]SideLine, error) {
	if i < 0 || i >= s.FileCount() {
		return nil, fmt.Errorf("file index %d out of range", i)
	}
	f := s.File(i)
	id := f.OldBlob
	if newSide {
		id = f.NewBlob
	}
	if id == "" || f.Submodule() {
		return nil, fmt.Errorf("%s has no content on that side", f.Path())
	}
	last := 0
	for _, r := range ranges {
		if r.Start < 1 || r.End < r.Start {
			return nil, fmt.Errorf("invalid line range %d-%d", r.Start, r.End)
		}
		last = max(last, r.End)
	}
	out := make([][]SideLine, len(ranges))
	if len(ranges) == 0 {
		return out, nil
	}
	keep = min(max(keep, 0), LargeBytes)
	// byStart orders the ranges by first line; active holds those covering the current line.
	byStart := make([]int, len(ranges))
	for k := range byStart {
		byStart[k] = k
	}
	slices.SortFunc(byStart, func(a, b int) int { return cmp.Compare(ranges[a].Start, ranges[b].Start) })
	var active []int
	err := s.repo.readBlob(ctx, id, func(_ int64, body io.Reader) error {
		br := bufio.NewReaderSize(body, 64<<10)
		next := 0
		for n := 1; n <= last; n++ {
			for next < len(byStart) && ranges[byStart[next]].Start == n {
				active = append(active, byStart[next])
				next++
			}
			active = slices.DeleteFunc(active, func(k int) bool { return ranges[k].End < n })
			limit := 0
			if len(active) > 0 {
				limit = keep
			}
			text, size, ok, err := readLine(br, limit)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			for _, k := range active {
				out[k] = append(out[k], SideLine{Text: text, Bytes: size})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// readLine reads one line without its LF, keeping at most limit bytes of it and discarding the
// rest; size is the whole line's length. ok is false at the end of the input.
func readLine(br *bufio.Reader, limit int) (text string, size int, ok bool, err error) {
	var kept []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			ok = true
		}
		body := bytes.TrimSuffix(chunk, []byte{'\n'})
		size += len(body)
		if room := limit - len(kept); room > 0 {
			kept = append(kept, body[:min(room, len(body))]...)
		}
		switch {
		case err == nil:
			return string(kept), size, true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return string(kept), size, ok, nil
		default:
			return "", 0, false, err
		}
	}
}

// LineCount is a changed file's added and deleted line counts. Binary is set when git reports
// no line counts.
type LineCount struct {
	Added, Deleted int
	Binary         bool
}

// NumStat reports line counts for the changed files, calling yield with each counted file's
// index in list order as soon as git has counted it. A file with a missing blob gets no count,
// because counting reads both blobs; every other file is counted. A yielded count is final and
// correct, but an error can follow earlier yields. ctx is checked between files.
func (s *Snapshot) NumStat(ctx context.Context, yield func(i int, c LineCount)) error {
	var present []int
	for i, f := range s.files {
		if !f.Missing {
			present = append(present, i)
		}
	}
	if len(present) == 0 {
		return nil
	}
	return s.numStat(ctx, present, yield)
}

// maxNumStatOutput bounds one numstat command's output: two counts, the record's paths and a
// delimiter per file, so never more than the listing it was fed.
const maxNumStatOutput = maxRawOutput

// numStat runs one numstat command over the records of files, which are in list order, and
// yields each file's count as it arrives. Each record is followed by a lone NUL, which makes
// diff-pairs flush its output for the records before it and echo the NUL, so git writes one
// count per batch while stdin is still open and counts stream instead of arriving at the end.
// diff-pairs prints one record per input record in input order; each is checked against the file
// it is taken for before it is yielded.
func (s *Snapshot) numStat(ctx context.Context, files []int, yield func(i int, c LineCount)) error {
	in := make([]io.Reader, 0, 2*len(files))
	for _, i := range files {
		in = append(in, strings.NewReader(s.records[i]), strings.NewReader("\x00"))
	}
	_, err := s.repo.run.stream(ctx, maxNumStatOutput, nil, io.MultiReader(in...), func(out *bufio.Reader) error {
		for k, i := range files {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("numstat: %w", err)
			}
			c, oldPath, newPath, err := readNumStat(out)
			if err != nil {
				return fmt.Errorf("numstat: record %d: %w", k+1, err)
			}
			f := s.files[i]
			single := newPath == "" && f.Status != StatusRenamed && oldPath == f.Path()
			if !single && (oldPath != f.OldPath || newPath != f.NewPath) {
				return fmt.Errorf("numstat: record for %q -> %q where %q -> %q was expected", oldPath, newPath, f.OldPath, f.NewPath)
			}
			if b, err := out.ReadByte(); err != nil || b != 0 {
				return fmt.Errorf("numstat: no batch delimiter after record %d", k+1)
			}
			yield(i, c)
		}
		if _, err := out.ReadByte(); err == nil {
			return fmt.Errorf("numstat: unexpected output after %d records", len(files))
		} else if !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	}, diffPairs("--numstat")...)
	return err
}

// readNumStat reads one `--numstat -z` record. An unrenamed record carries one path, returned as
// oldPath with newPath "".
func readNumStat(out *bufio.Reader) (c LineCount, oldPath, newPath string, err error) {
	field := func(what string) (string, error) {
		f, readErr := out.ReadString(0)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return "", fmt.Errorf("missing %s", what)
			}
			return "", readErr
		}
		return f[:len(f)-1], nil
	}
	rec, err := field("record")
	if err != nil {
		return LineCount{}, "", "", err
	}
	fields := strings.SplitN(rec, "\t", 3)
	if len(fields) != 3 {
		return LineCount{}, "", "", fmt.Errorf("malformed record %q", rec)
	}
	if fields[0] == "-" && fields[1] == "-" {
		c.Binary = true
	} else {
		a, err1 := strconv.Atoi(fields[0])
		d, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			return LineCount{}, "", "", fmt.Errorf("malformed record %q", rec)
		}
		c.Added, c.Deleted = a, d
	}
	if fields[2] != "" {
		return c, fields[2], "", nil
	}
	if oldPath, err = field("rename source"); err != nil {
		return LineCount{}, "", "", err
	}
	if newPath, err = field("rename target"); err != nil {
		return LineCount{}, "", "", err
	}
	return c, oldPath, newPath, nil
}

// Gap is a run of unchanged lines outside every hunk: new-side lines From..To, which are old-side
// lines OldFrom..OldFrom+(To-From). The zero Gap holds no lines; any other has 1 <= From <= To.
type Gap struct{ From, To, OldFrom int }

// Gaps returns f's expandable context, or nil when f has none: context needs a text file, not
// large, with both sides present. A non-nil result has exactly len(f.Hunks)+1 entries: entry
// k < len(f.Hunks) is the gap before hunk k and the last is the gap after the last hunk, the
// zero Gap where no line lies between. A file with no hunks, such as a
// pure rename, has one gap covering the whole file.
func (f FileFacts) Gaps() []Gap {
	if f.Kind != KindText || f.Large || !f.Old.Present || !f.New.Present {
		return nil
	}
	out := make([]Gap, 0, len(f.Hunks)+1)
	gap := func(from, to, oldFrom int) Gap {
		if from > to {
			return Gap{}
		}
		return Gap{From: from, To: to, OldFrom: oldFrom}
	}
	nextOld, nextNew := 1, 1
	for _, h := range f.Hunks {
		o, n := h.First()
		out = append(out, gap(nextNew, n-1, nextOld))
		nextOld, nextNew = o+h.OldCount, n+h.NewCount
	}
	return append(out, gap(nextNew, f.New.Lines, nextOld))
}

// ContextLine is one unchanged line outside every hunk, numbered on both sides.
type ContextLine struct {
	Text     string
	Old, New int
}

// Context errors.
var (
	// ErrNoContext reports a file without expandable context; see FileFacts.Gaps.
	ErrNoContext = errors.New("this file has no context lines")
	// ErrOutsideGap reports a line range that is not inside one gap.
	ErrOutsideGap = errors.New("the lines are not inside one gap")
)

// Context returns the new side's lines from..to, which must lie inside one gap, numbered on both
// sides. Lines split on LF as countLines counts them: a lone CR stays in the text.
func (d FileDiff) Context(from, to int) ([]ContextLine, error) {
	gs := d.Facts.Gaps()
	if gs == nil {
		return nil, ErrNoContext
	}
	// Gaps holds a zero Gap wherever no line lies between, and 0..0 would match it, so line numbers
	// below 1 are rejected here rather than by the gap test.
	k := -1
	if from >= 1 {
		k = slices.IndexFunc(gs, func(g Gap) bool { return g.From <= from && from <= to && to <= g.To })
	}
	if k < 0 {
		return nil, fmt.Errorf("%w: %d-%d", ErrOutsideGap, from, to)
	}
	shift := gs[k].OldFrom - gs[k].From
	out := make([]ContextLine, 0, to-from+1)
	rest := d.New
	for n := 1; n <= to; n++ {
		line, after, _ := bytes.Cut(rest, []byte{'\n'})
		rest = after
		if n >= from {
			out = append(out, ContextLine{Text: string(line), Old: n + shift, New: n})
		}
	}
	return out, nil
}

// First returns the numbers of the hunk's first old and new lines. A side with no lines names
// the line before the hunk, so its first line is the one after.
func (h HunkRange) First() (oldLine, newLine int) {
	firstLine := func(start, count int) int {
		if count == 0 {
			return start + 1
		}
		return start
	}
	return firstLine(h.OldStart, h.OldCount), firstLine(h.NewStart, h.NewCount)
}

// Showable reports whether every line of r on the new (newSide) or old side lies inside a hunk
// or a gap, so the file view can show it: it is the rule for which lines a comment may target.
// A range may span a hunk and the context beside it. A large file has no gaps, so only its hunk
// lines are showable. An invalid r reports false.
func (f FileFacts) Showable(newSide bool, r LineRange) bool {
	if f.Kind != KindText || r.Start < 1 || r.End < r.Start {
		return false
	}
	// Hunks and gaps are in ascending order and do not overlap on either side; next is the first
	// line of r not yet covered.
	next := r.Start
	cover := func(first, count int) bool {
		if count > 0 && first <= next && next <= first+count-1 {
			next = first + count
		}
		return next > r.End
	}
	gaps := f.Gaps()
	for k, h := range f.Hunks {
		if gaps != nil {
			if g := gaps[k]; g != (Gap{}) && cover(gapStart(g, newSide), g.To-g.From+1) {
				return true
			}
		}
		o, n := h.First()
		first, count := o, h.OldCount
		if newSide {
			first, count = n, h.NewCount
		}
		if cover(first, count) {
			return true
		}
	}
	if gaps != nil {
		if g := gaps[len(f.Hunks)]; g != (Gap{}) && cover(gapStart(g, newSide), g.To-g.From+1) {
			return true
		}
	}
	return false
}

func gapStart(g Gap, newSide bool) int {
	if newSide {
		return g.From
	}
	return g.OldFrom
}
