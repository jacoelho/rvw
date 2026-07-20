package review

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
)

const (
	MaxPatchBytes int64 = 64 << 20
	MaxPatchLines       = 1_000_000
)

var ErrInvalidPatchTransition = errors.New("invalid patch transition")

type PatchStatus uint8

const (
	PatchUnavailable PatchStatus = iota + 1
	PatchPending
	PatchReady
	PatchFailed
	PatchCancelled
	PatchLimited
)

type ContextLines uint16

func NewContextLines(value uint16) (ContextLines, error) {
	if value > 10_000 {
		return 0, fmt.Errorf("context lines %d exceed 10000", value)
	}
	return ContextLines(value), nil
}

func (lines ContextLines) Uint16() uint16 {
	return uint16(lines)
}

type PatchContext struct {
	lines     ContextLines
	wholeFile bool
}

func ContextAround(lines ContextLines) (PatchContext, error) {
	if _, err := NewContextLines(lines.Uint16()); err != nil {
		return PatchContext{}, err
	}
	return PatchContext{lines: lines}, nil
}

func WholeFileContext() PatchContext {
	return PatchContext{wholeFile: true}
}

func (context PatchContext) WholeFile() bool {
	return context.wholeFile
}

func (context PatchContext) Lines() (ContextLines, bool) {
	return context.lines, !context.wholeFile
}

func (context PatchContext) UnifiedLines() uint32 {
	if context.wholeFile {
		return MaxPatchLines
	}
	return uint32(context.lines.Uint16())
}

func (context PatchContext) valid() bool {
	if context.wholeFile {
		return context.lines == 0
	}
	_, err := NewContextLines(context.lines.Uint16())
	return err == nil
}

type PatchRequest struct {
	Workspace WorkspaceRevision
	Handle    PatchHandle
	Context   PatchContext
}

type PatchResult struct {
	Workspace WorkspaceRevision
	Handle    PatchHandle
	Status    PatchStatus
	Prepared  *PreparedPatch
	Detail    PatchDetail
}

type PatchState struct {
	Status   PatchStatus
	Hunks    uint32
	Lines    uint32
	Detail   PatchDetail
	prepared *PreparedPatch
}

func (state PatchState) RetainedBytes() int64 {
	return state.prepared.RetainedBytes()
}

type PatchText string

func (text PatchText) String() string {
	return string(text)
}

type PatchDetail string

func (detail PatchDetail) String() string {
	return string(detail)
}

type PatchOffset uint32

func (offset PatchOffset) Uint32() uint32 {
	return uint32(offset)
}

type PatchLine struct {
	Number    uint32
	Text      PatchText
	Truncated bool
	kind      PatchLineKind
	oldLine   uint32
	newLine   uint32
	hunk      uint32
}

type PatchLineKind uint8

const (
	PatchLineMetadata PatchLineKind = iota + 1
	PatchLineHunkHeader
	PatchLineContext
	PatchLineAddition
	PatchLineDeletion
	PatchLineNoNewline
)

const noPatchHunk = ^uint32(0)

func (line PatchLine) Kind() PatchLineKind {
	return line.kind
}

func (line PatchLine) OldLine() (LineNumber, bool) {
	if line.oldLine == 0 {
		return 0, false
	}
	return LineNumber(line.oldLine), true
}

func (line PatchLine) NewLine() (LineNumber, bool) {
	if line.newLine == 0 {
		return 0, false
	}
	return LineNumber(line.newLine), true
}

func (line PatchLine) Hunk(fileID FileID) (HunkID, bool) {
	if line.hunk == noPatchHunk {
		return HunkID{}, false
	}
	index, err := NewHunkIndex(line.hunk)
	if err != nil {
		return HunkID{}, false
	}
	id, err := NewHunkID(fileID, index)
	return id, err == nil
}

type PreparedPatch struct {
	text        []byte
	hunks       uint32
	lineOffsets []PatchOffset
	lineKinds   []PatchLineKind
	oldLines    []uint32
	newLines    []uint32
	oldLineRows []uint32
	newLineRows []uint32
	lineHunks   []uint32
	hunkLines   []uint32
	hunkFirst   []uint32
	hunkLast    []uint32
}

func TakePatch(text []byte) (*PreparedPatch, error) {
	if int64(len(text)) > MaxPatchBytes {
		return nil, fmt.Errorf("patch exceeds %d bytes", MaxPatchBytes)
	}
	capacity := min(bytes.Count(text, []byte{'\n'})+1, MaxPatchLines)
	patch := &PreparedPatch{
		text:        text,
		lineOffsets: make([]PatchOffset, 0, capacity),
		lineKinds:   make([]PatchLineKind, 0, capacity),
		oldLines:    make([]uint32, 0, capacity),
		newLines:    make([]uint32, 0, capacity),
		lineHunks:   make([]uint32, 0, capacity),
	}
	offsets := patch.lineOffsets
	remaining := text
	var offset uint32
	activeHunk := noPatchHunk
	var oldLine, newLine, oldRemaining, newRemaining uint32
	for len(remaining) > 0 {
		if len(offsets) == MaxPatchLines {
			return nil, fmt.Errorf("patch lines exceed %d", MaxPatchLines)
		}
		offsets = append(offsets, PatchOffset(offset))
		line := remaining
		if index := bytes.IndexByte(remaining, '\n'); index >= 0 {
			line = remaining[:index]
			remaining = remaining[index+1:]
			offset += uint32(index + 1)
		} else {
			remaining = nil
		}
		kind := PatchLineMetadata
		rowOld, rowNew := uint32(0), uint32(0)
		if bytes.HasPrefix(line, []byte("@@ ")) {
			if err := completeHunk(oldRemaining, newRemaining); err != nil {
				return nil, err
			}
			if patch.hunks == MaxHunks {
				return nil, fmt.Errorf("patch hunks exceed %d", MaxHunks)
			}
			var err error
			oldLine, oldRemaining, newLine, newRemaining, err = parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			activeHunk = patch.hunks
			patch.hunks++
			patch.hunkLines = append(patch.hunkLines, uint32(len(offsets)-1))
			patch.hunkFirst = append(patch.hunkFirst, noPatchHunk)
			patch.hunkLast = append(patch.hunkLast, noPatchHunk)
			kind = PatchLineHunkHeader
		} else if activeHunk != noPatchHunk {
			switch {
			case bytes.HasPrefix(line, []byte(`\ No newline at end of file`)):
				kind = PatchLineNoNewline
			case oldRemaining == 0 && newRemaining == 0:
				activeHunk = noPatchHunk
			case len(line) == 0:
				return nil, errors.New("patch hunk contains an empty unprefixed line")
			case line[0] == ' ':
				if oldRemaining == 0 || newRemaining == 0 {
					return nil, errors.New("patch hunk context exceeds its declared range")
				}
				kind = PatchLineContext
				rowOld, rowNew = oldLine, newLine
				oldLine++
				newLine++
				oldRemaining--
				newRemaining--
			case line[0] == '+':
				if newRemaining == 0 {
					return nil, errors.New("patch hunk additions exceed their declared range")
				}
				kind = PatchLineAddition
				rowNew = newLine
				newLine++
				newRemaining--
			case line[0] == '-':
				if oldRemaining == 0 {
					return nil, errors.New("patch hunk deletions exceed their declared range")
				}
				kind = PatchLineDeletion
				rowOld = oldLine
				oldLine++
				oldRemaining--
			default:
				return nil, fmt.Errorf("patch hunk contains unsupported line prefix %q", line[0])
			}
		}
		if activeHunk != noPatchHunk &&
			(kind == PatchLineContext || kind == PatchLineAddition || kind == PatchLineDeletion) {
			row := uint32(len(offsets) - 1)
			if patch.hunkFirst[activeHunk] == noPatchHunk {
				patch.hunkFirst[activeHunk] = row
			}
			patch.hunkLast[activeHunk] = row
		}
		patch.lineKinds = append(patch.lineKinds, kind)
		patch.oldLines = append(patch.oldLines, rowOld)
		patch.newLines = append(patch.newLines, rowNew)
		row := uint32(len(offsets) - 1)
		if rowOld != 0 {
			patch.oldLineRows = append(patch.oldLineRows, row)
		}
		if rowNew != 0 {
			patch.newLineRows = append(patch.newLineRows, row)
		}
		patch.lineHunks = append(patch.lineHunks, activeHunk)
	}
	if err := completeHunk(oldRemaining, newRemaining); err != nil {
		return nil, err
	}
	patch.lineOffsets = offsets
	return patch, nil
}

func parseHunkHeader(line []byte) (uint32, uint32, uint32, uint32, error) {
	closeIndex := bytes.Index(line[3:], []byte(" @@"))
	if closeIndex < 0 {
		return 0, 0, 0, 0, errors.New("patch hunk header is unterminated")
	}
	fields := bytes.Fields(line[3 : 3+closeIndex])
	if len(fields) != 2 || len(fields[0]) < 2 || fields[0][0] != '-' ||
		len(fields[1]) < 2 || fields[1][0] != '+' {
		return 0, 0, 0, 0, errors.New("patch hunk header has invalid ranges")
	}
	oldStart, oldCount, err := parseHunkRange(fields[0][1:])
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("patch old hunk range: %w", err)
	}
	newStart, newCount, err := parseHunkRange(fields[1][1:])
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("patch new hunk range: %w", err)
	}
	return oldStart, oldCount, newStart, newCount, nil
}

func parseHunkRange(value []byte) (uint32, uint32, error) {
	startText, countText, hasCount := bytes.Cut(value, []byte{','})
	if len(startText) == 0 || hasCount && len(countText) == 0 {
		return 0, 0, errors.New("range is empty")
	}
	start, err := strconv.ParseUint(string(startText), 10, 32)
	if err != nil {
		return 0, 0, errors.New("range start is invalid")
	}
	count := uint64(1)
	if hasCount {
		count, err = strconv.ParseUint(string(countText), 10, 32)
		if err != nil {
			return 0, 0, errors.New("range count is invalid")
		}
	}
	if count != 0 && start == 0 || count != 0 && start+count-1 > uint64(^uint32(0)) {
		return 0, 0, errors.New("range exceeds line-number bounds")
	}
	return uint32(start), uint32(count), nil
}

func completeHunk(oldRemaining, newRemaining uint32) error {
	if oldRemaining != 0 || newRemaining != 0 {
		return fmt.Errorf(
			"patch hunk ended before its declared ranges: old=%d new=%d",
			oldRemaining,
			newRemaining,
		)
	}
	return nil
}

func (patch *PreparedPatch) Hunks() uint32 {
	if patch == nil {
		return 0
	}
	return patch.hunks
}

func (patch *PreparedPatch) Lines() uint32 {
	if patch == nil {
		return 0
	}
	return uint32(len(patch.lineOffsets))
}

func (patch *PreparedPatch) RetainedBytes() int64 {
	if patch == nil {
		return 0
	}
	const retainedMetadataBytesPerLine int64 = 25
	const retainedMetadataBytesPerHunk int64 = 12
	return int64(len(patch.text)) +
		int64(len(patch.lineOffsets))*retainedMetadataBytesPerLine +
		int64(len(patch.hunkLines))*retainedMetadataBytesPerHunk
}

func (review Review) PatchLineForSource(
	fileID FileID,
	side LineSide,
	line LineNumber,
) (uint32, bool) {
	state := review.patches[fileID]
	if state.Status != PatchReady || state.prepared == nil || line == 0 {
		return 0, false
	}
	patch := state.prepared
	rows := patch.newLineRows
	lines := patch.newLines
	if side == LineOld {
		rows = patch.oldLineRows
		lines = patch.oldLines
	} else if side != LineNew {
		return 0, false
	}
	target := line.Uint32()
	position := sort.Search(len(rows), func(index int) bool {
		return lines[rows[index]] >= target
	})
	if position == len(rows) || lines[rows[position]] != target {
		return 0, false
	}
	return rows[position], true
}

func (review Review) RequestPatch(fileID FileID, context PatchContext) (Review, PatchRequest, error) {
	if !context.valid() {
		return review, PatchRequest{}, fmt.Errorf(
			"%w: invalid patch context",
			ErrInvalidPatchTransition,
		)
	}
	file, ok := review.file(fileID)
	if !ok {
		return review, PatchRequest{}, ErrInvalidPatchTransition
	}
	current := review.patches[fileID]
	if current.Status == PatchPending {
		return review, PatchRequest{}, fmt.Errorf("%w: patch is already pending", ErrInvalidPatchTransition)
	}
	next := review.clone()
	next.patches = maps.Clone(review.patches)
	next.patches[fileID] = PatchState{Status: PatchPending}
	return next, PatchRequest{
		Workspace: review.workspace,
		Handle:    file.PatchHandle(),
		Context:   context,
	}, nil
}

func (review Review) ApplyPatch(result PatchResult) (Review, error) {
	if result.Workspace != review.workspace ||
		result.Handle.SourceID() != review.snapshot.SourceID() {
		return review, fmt.Errorf("%w: stale workspace or source", ErrInvalidPatchTransition)
	}
	fileID := result.Handle.FileID()
	file, ok := review.file(fileID)
	if !ok || file.PatchHandle() != result.Handle ||
		review.patches[fileID].Status != PatchPending {
		return review, ErrInvalidPatchTransition
	}
	if result.Status != PatchReady && result.Status != PatchFailed &&
		result.Status != PatchCancelled && result.Status != PatchLimited &&
		result.Status != PatchUnavailable {
		return review, ErrInvalidPatchTransition
	}
	if result.Status == PatchReady {
		if result.Prepared == nil {
			return review, fmt.Errorf("%w: ready patch is not prepared", ErrInvalidPatchTransition)
		}
	} else if result.Prepared != nil {
		return review, fmt.Errorf("%w: terminal failure carries ready-only patch data", ErrInvalidPatchTransition)
	}

	next := review.clone()
	next.patches = maps.Clone(review.patches)
	next.patches[fileID] = PatchState{
		Status:   result.Status,
		Hunks:    result.Prepared.Hunks(),
		Lines:    result.Prepared.Lines(),
		Detail:   result.Detail,
		prepared: result.Prepared,
	}
	if result.Status != PatchReady {
		next.patches[fileID] = PatchState{Status: result.Status, Detail: result.Detail}
	}
	return next, nil
}

func (review Review) PatchLines(fileID FileID, start, count uint32, maxLineBytes int64) []PatchLine {
	state := review.patches[fileID]
	if state.Status != PatchReady || count == 0 || start >= state.Lines || maxLineBytes <= 0 {
		return nil
	}
	end := start + min(count, state.Lines-start)
	lines := make([]PatchLine, 0, end-start)
	for line := start; line < end; line++ {
		begin := state.prepared.lineOffsets[line].Uint32()
		finish := uint32(len(state.prepared.text))
		if line+1 < state.Lines {
			finish = state.prepared.lineOffsets[line+1].Uint32()
		}
		for finish > begin &&
			(state.prepared.text[finish-1] == '\n' || state.prepared.text[finish-1] == '\r') {
			finish--
		}
		truncated := int64(finish-begin) > maxLineBytes
		if truncated {
			retained := min(maxLineBytes, int64(finish-begin))
			finish = begin + uint32(retained)
		}
		lines = append(lines, PatchLine{
			Number:    line,
			Text:      PatchText(state.prepared.text[begin:finish]),
			Truncated: truncated,
			kind:      state.prepared.lineKinds[line],
			oldLine:   state.prepared.oldLines[line],
			newLine:   state.prepared.newLines[line],
			hunk:      state.prepared.lineHunks[line],
		})
	}
	return lines
}

func (review Review) DropPatch(fileID FileID) Review {
	if _, exists := review.patches[fileID]; !exists {
		return review
	}
	next := review.clone()
	next.patches = maps.Clone(review.patches)
	delete(next.patches, fileID)
	next.hunkMarks = review.hunkMarks.dropFile(fileID)
	return next
}

func (review Review) AdjacentHunkLine(
	fileID FileID,
	current uint32,
	direction NavigationDirection,
	count uint16,
) (uint32, bool) {
	state := review.patches[fileID]
	if state.Status != PatchReady || state.prepared == nil ||
		len(state.prepared.hunkLines) == 0 ||
		direction != NavigatePrevious && direction != NavigateNext ||
		count == 0 {
		return 0, false
	}
	lines := state.prepared.hunkLines
	if direction == NavigateNext {
		index := sort.Search(len(lines), func(index int) bool {
			return lines[index] > current
		})
		if index == len(lines) {
			return 0, false
		}
		index += min(int(count)-1, len(lines)-index-1)
		return lines[index], true
	}
	index := sort.Search(len(lines), func(index int) bool {
		return lines[index] >= current
	})
	if index == 0 {
		return 0, false
	}
	index--
	index -= min(int(count)-1, index)
	return lines[index], true
}

func (review Review) HunkRows(id HunkID) (uint32, uint32, bool) {
	state := review.patches[id.FileID()]
	index := id.Index().Uint32()
	if state.Status != PatchReady || state.prepared == nil ||
		index >= uint32(len(state.prepared.hunkFirst)) {
		return 0, 0, false
	}
	first := state.prepared.hunkFirst[index]
	last := state.prepared.hunkLast[index]
	return first, last, first != noPatchHunk && last != noPatchHunk
}

func (review Review) Patch(fileID FileID) PatchState {
	state, ok := review.patches[fileID]
	if !ok {
		return PatchState{Status: PatchUnavailable}
	}
	return state
}

func (review Review) RetainedPatchBytes() int64 {
	var total int64
	for _, state := range review.patches {
		total += state.RetainedBytes()
	}
	return total
}
