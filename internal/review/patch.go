package review

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// MaxPatchBytes defines the supported patch bytes limit.
const (
	MaxPatchBytes                  int64 = 64 << 20
	MaxPatchLines                        = 1_000_000
	preparedPatchMetadataBytes     int64 = 280
	preparedPatchLineMetadataBytes int64 = 25
	preparedPatchHunkMetadataBytes int64 = 12
	patchUint32StorageBytes        int64 = 4
	patchPollBytes                       = 16 << 10
	maxHunkHeaderBytes                   = 256
	maxPatchSparseRowGrowthBytes   int64 = MaxPatchLines / 2 * patchUint32StorageBytes
	maxPatchSparseHunkGrowthBytes  int64 = 3 * (MaxHunks / 2) * patchUint32StorageBytes
	// MaxPreparedPatchRetainedBytes bounds complete PreparedPatch ownership.
	MaxPreparedPatchRetainedBytes int64 = preparedPatchMetadataBytes + MaxPatchBytes +
		MaxPatchLines*preparedPatchLineMetadataBytes +
		MaxHunks*preparedPatchHunkMetadataBytes
	// MaxPreparedPatchConstructionBytes includes the largest old-plus-new
	// sparse-index growth while the maximum retained patch state is live.
	MaxPreparedPatchConstructionBytes int64 = MaxPreparedPatchRetainedBytes +
		max(maxPatchSparseRowGrowthBytes, maxPatchSparseHunkGrowthBytes)
)

// ErrInvalidPatchTransition identifies an invalid patch transition failure.
var ErrInvalidPatchTransition = errors.New("invalid patch transition")

// PatchStatus describes patch status.
type PatchStatus uint8

// PatchUnavailable identifies a supported patch unavailable value.
const (
	PatchUnavailable PatchStatus = iota + 1
	PatchPending
	PatchReady
	PatchFailed
	PatchCancelled
	PatchLimited
)

// ContextLines represents context lines.
type ContextLines uint16

// NewContextLines constructs and validates context lines.
func NewContextLines(value uint16) (ContextLines, error) {
	if value > 10_000 {
		return 0, fmt.Errorf("context lines %d exceed 10000", value)
	}
	return ContextLines(value), nil
}

// Uint16 returns the receiver's uint16 value.
func (lines ContextLines) Uint16() uint16 {
	return uint16(lines)
}

// PatchContext represents patch context.
type PatchContext struct {
	lines     ContextLines
	wholeFile bool
}

// ContextAround performs the context around operation.
func ContextAround(lines ContextLines) (PatchContext, error) {
	if _, err := NewContextLines(lines.Uint16()); err != nil {
		return PatchContext{}, err
	}
	return PatchContext{lines: lines}, nil
}

// WholeFileContext performs the whole file context operation.
func WholeFileContext() PatchContext {
	return PatchContext{wholeFile: true}
}

// WholeFile reports the receiver's whole file state.
func (context PatchContext) WholeFile() bool {
	return context.wholeFile
}

// Lines returns the receiver's lines value.
func (context PatchContext) Lines() (ContextLines, bool) {
	return context.lines, !context.wholeFile
}

// UnifiedLines returns the receiver's unified lines value.
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

// PatchRequest describes patch request.
type PatchRequest struct {
	Workspace WorkspaceRevision
	Handle    PatchHandle
	Context   PatchContext
}

// PatchResult describes patch result.
type PatchResult struct {
	Prepared  *PreparedPatch
	Detail    PatchDetail
	Handle    PatchHandle
	Workspace WorkspaceRevision
	Status    PatchStatus
}

// PatchState describes patch state.
type PatchState struct {
	prepared *PreparedPatch
	Detail   PatchDetail
	Hunks    uint32
	Lines    uint32
	Status   PatchStatus
}

// presentationPatch is the single patch retained for terminal presentation.
// Foreground serialization guarantees that only one patch can be pending or
// displayed at a time.
type presentationPatch struct {
	state         PatchState
	retainedBytes int64
	file          FileID
	set           bool
}

func (review Review) patchState(fileID FileID) PatchState {
	if !review.patch.set || review.patch.file != fileID {
		return PatchState{Status: PatchUnavailable}
	}
	return review.patch.state
}

// MaxLineNumber returns the largest source line represented on side by this patch.
func (state PatchState) MaxLineNumber(side LineSide) (LineNumber, bool) {
	if state.prepared == nil {
		return 0, false
	}
	var value uint32
	switch side {
	case LineOld:
		value = state.prepared.maxOldLine
	case LineNew:
		value = state.prepared.maxNewLine
	default:
		return 0, false
	}
	if value == 0 {
		return 0, false
	}
	return LineNumber(value), true
}

// RetainedBytes returns the receiver's retained bytes value.
func (state PatchState) RetainedBytes() int64 {
	return state.prepared.RetainedBytes()
}

// PatchText represents patch text.
type PatchText string

// String returns the receiver's string value.
func (text PatchText) String() string {
	return string(text)
}

// PatchDetail represents patch detail.
type PatchDetail string

// String returns the receiver's string value.
func (detail PatchDetail) String() string {
	return string(detail)
}

// PatchOffset represents patch offset.
type PatchOffset uint32

// Uint32 returns the receiver's uint32 value.
func (offset PatchOffset) Uint32() uint32 {
	return uint32(offset)
}

// PatchLine represents patch line.
type PatchLine struct {
	Text       PatchText
	Number     uint32
	oldLine    uint32
	newLine    uint32
	hunk       uint32
	Truncated  bool
	descriptor patchLineDescriptor
}

// PatchLineKind describes patch line kind.
type PatchLineKind uint8

// PatchLineMetadata identifies a supported patch line metadata value.
const (
	PatchLineMetadata PatchLineKind = iota + 1
	PatchLineHunkHeader
	PatchLineContext
	PatchLineAddition
	PatchLineDeletion
	PatchLineNoNewline
)

const (
	noPatchHunk                                 = ^uint32(0)
	noNewlineMarkerText                         = `\ No newline at end of file`
	patchLineKindBits                           = 3
	patchLineDescriptorMask patchLineDescriptor = 1<<patchLineKindBits - 1
)

type patchLineDescriptor uint8

type patchLineSides uint8

const (
	patchLineOldSide patchLineSides = 1 << iota
	patchLineNewSide
	patchLineBothSides = patchLineOldSide | patchLineNewSide
)

func plainPatchLineDescriptor(kind PatchLineKind) patchLineDescriptor {
	return patchLineDescriptor(kind)
}

func noNewlinePatchLineDescriptor(sides patchLineSides) (patchLineDescriptor, bool) {
	if !sides.valid() {
		return 0, false
	}
	return plainPatchLineDescriptor(PatchLineNoNewline) |
		patchLineDescriptor(sides)<<patchLineKindBits, true
}

func (descriptor patchLineDescriptor) kind() PatchLineKind {
	return PatchLineKind(descriptor & patchLineDescriptorMask)
}

func (descriptor patchLineDescriptor) appliesToSide(side LineSide) bool {
	if descriptor.kind() != PatchLineNoNewline {
		return false
	}
	sides := patchLineSides(descriptor >> patchLineKindBits)
	if !sides.valid() {
		return false
	}
	return sides.has(side)
}

func (sides patchLineSides) valid() bool {
	return sides != 0 && sides&^patchLineBothSides == 0
}

func (sides patchLineSides) has(side LineSide) bool {
	switch side {
	case LineOld:
		return sides&patchLineOldSide != 0
	case LineNew:
		return sides&patchLineNewSide != 0
	default:
		return false
	}
}

func noNewlineSidesAfter(kind PatchLineKind) patchLineSides {
	switch kind {
	case PatchLineContext:
		return patchLineBothSides
	case PatchLineAddition:
		return patchLineNewSide
	case PatchLineDeletion:
		return patchLineOldSide
	default:
		return 0
	}
}

// Kind returns the receiver's kind value.
func (line PatchLine) Kind() PatchLineKind {
	return line.descriptor.kind()
}

// AppliesToSide reports whether side owns this no-newline marker.
func (line PatchLine) AppliesToSide(side LineSide) bool {
	return line.descriptor.appliesToSide(side)
}

// OldLine returns the receiver's old line value.
func (line PatchLine) OldLine() (LineNumber, bool) {
	if line.oldLine == 0 {
		return 0, false
	}
	return LineNumber(line.oldLine), true
}

// NewLine constructs and validates line.
func (line PatchLine) NewLine() (LineNumber, bool) {
	if line.newLine == 0 {
		return 0, false
	}
	return LineNumber(line.newLine), true
}

// Hunk performs the hunk operation.
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

// PreparedPatch represents prepared patch.
type PreparedPatch struct {
	oldLineRows []uint32
	hunkFirst   []uint32
	lineOffsets []PatchOffset
	lineKinds   []patchLineDescriptor
	oldLines    []uint32
	newLines    []uint32
	lineHunks   []uint32
	newLineRows []uint32
	hunkLast    []uint32
	hunkLines   []uint32
	text        []byte
	hunks       uint32
	maxOldLine  uint32
	maxNewLine  uint32
}

// StructuredPatchLine is one already-classified line used to construct a
// PreparedPatch without reparsing a textual diff protocol.
type StructuredPatchLine struct {
	Text      []byte
	OldLine   uint32
	NewLine   uint32
	Hunk      uint32
	Kind      PatchLineKind
	HasHunk   bool
	OldMarker bool
	NewMarker bool
}

// StructuredPatchGenerator emits one complete patch in display order. The
// generator must be repeatable: preparation invokes it once to admit exact
// retained sizes and once to publish the immutable PreparedPatch.
type StructuredPatchGenerator func(yield func(StructuredPatchLine) error) error

type patchParseState struct {
	activeHunk     uint32
	oldLine        uint32
	newLine        uint32
	oldRemaining   uint32
	newRemaining   uint32
	noNewlineSides patchLineSides
}

type parsedPatchLine struct {
	descriptor patchLineDescriptor
	oldLine    uint32
	newLine    uint32
}

// TakePatch takes ownership of patch.
func TakePatch(text []byte) (*PreparedPatch, error) {
	return TakePatchCancellable(nil, text)
}

// TakePatchCancellable takes ownership of patch while bounding cancellation latency.
func TakePatchCancellable(cancelled func() error, text []byte) (*PreparedPatch, error) {
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	if int64(len(text)) > MaxPatchBytes {
		return nil, fmt.Errorf("patch exceeds %d bytes", MaxPatchBytes)
	}
	if int64(cap(text)) > MaxPatchBytes {
		return nil, fmt.Errorf("patch retained capacity exceeds %d bytes", MaxPatchBytes)
	}
	capacity, err := patchLineCapacity(cancelled, text)
	if err != nil {
		return nil, err
	}
	patch := newPreparedPatch(text, capacity)
	offsets := patch.lineOffsets
	remaining := text
	var offset uint32
	state := patchParseState{activeHunk: noPatchHunk}
	for len(remaining) > 0 {
		offsets, err = appendPatchOffsetCancellable(cancelled, offsets, offset)
		if err != nil {
			return nil, err
		}
		line, next, advance, err := nextPatchLineCancellable(cancelled, remaining)
		if err != nil {
			return nil, err
		}
		remaining = next
		offset += advance
		row := uint32FromInt(len(offsets) - 1)
		parsed, parseErr := state.parseLine(patch, line, row)
		if parseErr != nil {
			return nil, parseErr
		}
		patch.appendParsedLine(row, state.activeHunk, parsed)
	}
	if err := completeHunk(state.oldRemaining, state.newRemaining); err != nil {
		return nil, err
	}
	patch.lineOffsets = offsets
	return patch, nil
}

// PrepareStructuredPatchCancellable constructs a patch from validated line
// semantics while retaining the exact display text owned by the result.
func PrepareStructuredPatchCancellable(
	cancelled func() error,
	lines []StructuredPatchLine,
) (*PreparedPatch, error) {
	return PrepareGeneratedPatchCancellable(cancelled, func(yield func(StructuredPatchLine) error) error {
		for _, line := range lines {
			if err := yield(line); err != nil {
				return err
			}
		}
		return nil
	})
}

// PrepareGeneratedPatchCancellable constructs a patch without requiring the
// caller to retain all structured lines. Exact byte and line admission occurs
// before the result allocation is published.
//
//nolint:gocognit,cyclop,funlen,maintidx // Validation and direct publication intentionally share one bounded construction pass.
func PrepareGeneratedPatchCancellable(
	cancelled func() error,
	generate StructuredPatchGenerator,
) (*PreparedPatch, error) {
	if generate == nil {
		return nil, errors.New("structured patch generator is nil")
	}
	lineCount, total := 0, 0
	err := generate(func(line StructuredPatchLine) error {
		if lineCount%4_096 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return err
			}
		}
		if lineCount == MaxPatchLines {
			return fmt.Errorf("patch lines exceed %d", MaxPatchLines)
		}
		if bytes.IndexByte(line.Text, '\n') >= 0 {
			return errors.New("structured patch line contains a newline")
		}
		separator := 0
		if lineCount != 0 {
			separator = 1
		}
		if len(line.Text) > int(MaxPatchBytes)-total-separator {
			return fmt.Errorf("patch exceeds %d bytes", MaxPatchBytes)
		}
		total += separator + len(line.Text)
		lineCount++
		return nil
	})
	if err != nil {
		return nil, err
	}
	patch := newPreparedPatch(make([]byte, total), lineCount)
	activeHunk := noPatchHunk
	var displayOffset uint32
	row, offset := 0, 0
	err = generate(func(line StructuredPatchLine) error {
		if row%4_096 == 0 {
			if cancelErr := cancellationError(cancelled); cancelErr != nil {
				return cancelErr
			}
		}
		if row >= lineCount || bytes.IndexByte(line.Text, '\n') >= 0 {
			return errors.New("structured patch generator changed between passes")
		}
		if row != 0 {
			patch.text[offset] = '\n'
			offset++
		}
		if len(line.Text) > len(patch.text)-offset {
			return errors.New("structured patch generator changed between passes")
		}
		copy(patch.text[offset:], line.Text)
		offset += len(line.Text)
		patch.lineOffsets = append(patch.lineOffsets, PatchOffset(displayOffset))
		displayOffset += uint32(len(line.Text)) //nolint:gosec // aggregate text is capped at 64 MiB above.
		if row+1 < lineCount {
			displayOffset++
		}
		descriptor := plainPatchLineDescriptor(line.Kind)
		switch line.Kind {
		case PatchLineMetadata:
			if line.HasHunk || line.OldLine != 0 || line.NewLine != 0 || line.OldMarker || line.NewMarker {
				return errors.New("structured metadata line has source semantics")
			}
			activeHunk = noPatchHunk
		case PatchLineHunkHeader:
			if !line.HasHunk || line.Hunk != patch.hunks || line.OldLine != 0 || line.NewLine != 0 ||
				line.OldMarker || line.NewMarker {
				return errors.New("structured hunk header is invalid")
			}
			if patch.hunks == MaxHunks {
				return fmt.Errorf("patch hunks exceed %d", MaxHunks)
			}
			activeHunk = line.Hunk
			patch.hunks++
			patch.appendHunk(uint32(row)) //nolint:gosec // lineCount is admitted below one million.
		case PatchLineContext:
			if !line.HasHunk || line.Hunk != activeHunk || line.OldLine == 0 || line.NewLine == 0 ||
				line.OldMarker || line.NewMarker {
				return errors.New("structured context line is invalid")
			}
		case PatchLineAddition:
			if !line.HasHunk || line.Hunk != activeHunk || line.OldLine != 0 || line.NewLine == 0 ||
				line.OldMarker || line.NewMarker {
				return errors.New("structured addition line is invalid")
			}
		case PatchLineDeletion:
			if !line.HasHunk || line.Hunk != activeHunk || line.OldLine == 0 || line.NewLine != 0 ||
				line.OldMarker || line.NewMarker {
				return errors.New("structured deletion line is invalid")
			}
		case PatchLineNoNewline:
			if !line.HasHunk || line.Hunk != activeHunk || line.OldLine != 0 || line.NewLine != 0 {
				return errors.New("structured no-newline line is invalid")
			}
			sides := patchLineSides(0)
			if line.OldMarker {
				sides |= patchLineOldSide
			}
			if line.NewMarker {
				sides |= patchLineNewSide
			}
			var valid bool
			descriptor, valid = noNewlinePatchLineDescriptor(sides)
			if !valid || string(line.Text) != noNewlineMarkerText {
				return errors.New("structured no-newline marker is invalid")
			}
		default:
			return errors.New("structured patch line kind is invalid")
		}
		patch.appendParsedLine(uint32(row), activeHunk, parsedPatchLine{ //nolint:gosec // lineCount is admitted below one million.
			descriptor: descriptor,
			oldLine:    line.OldLine,
			newLine:    line.NewLine,
		})
		row++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if row != lineCount || offset != total {
		return nil, errors.New("structured patch generator changed between passes")
	}
	return patch, nil
}

func (state *patchParseState) parseLine(
	patch *PreparedPatch,
	line []byte,
	row uint32,
) (parsedPatchLine, error) {
	markerSides := state.noNewlineSides
	state.noNewlineSides = 0

	var parsed parsedPatchLine
	switch {
	case bytes.Equal(line, []byte(noNewlineMarkerText)):
		if state.activeHunk == noPatchHunk || !markerSides.valid() {
			return parsed, errors.New("patch no-newline marker does not follow a material hunk line")
		}
		descriptor, valid := noNewlinePatchLineDescriptor(markerSides)
		if !valid {
			return parsed, errors.New("patch no-newline marker has invalid side membership")
		}
		parsed.descriptor = descriptor
		return parsed, nil
	case bytes.HasPrefix(line, []byte(noNewlineMarkerText)):
		return parsed, errors.New("patch no-newline marker has trailing content")
	case bytes.HasPrefix(line, []byte("@@ ")):
		return state.startHunk(patch, line, row)
	case state.activeHunk != noPatchHunk:
		material, err := state.parseHunkMaterial(line)
		if err != nil {
			return parsedPatchLine{}, err
		}
		state.noNewlineSides = noNewlineSidesAfter(material.descriptor.kind())
		return material, nil
	default:
		parsed.descriptor = plainPatchLineDescriptor(PatchLineMetadata)
		return parsed, nil
	}
}

func (state *patchParseState) startHunk(
	patch *PreparedPatch,
	line []byte,
	row uint32,
) (parsedPatchLine, error) {
	if err := completeHunk(state.oldRemaining, state.newRemaining); err != nil {
		return parsedPatchLine{}, err
	}
	if patch.hunks == MaxHunks {
		return parsedPatchLine{}, fmt.Errorf("patch hunks exceed %d", MaxHunks)
	}
	oldLine, oldRemaining, newLine, newRemaining, err := parseHunkHeader(line)
	if err != nil {
		return parsedPatchLine{}, err
	}
	state.activeHunk = patch.hunks
	state.oldLine, state.oldRemaining = oldLine, oldRemaining
	state.newLine, state.newRemaining = newLine, newRemaining
	patch.hunks++
	patch.appendHunk(row)
	return parsedPatchLine{
		descriptor: plainPatchLineDescriptor(PatchLineHunkHeader),
	}, nil
}

func (state *patchParseState) parseHunkMaterial(line []byte) (parsedPatchLine, error) {
	if state.oldRemaining == 0 && state.newRemaining == 0 {
		state.activeHunk = noPatchHunk
		return parsedPatchLine{
			descriptor: plainPatchLineDescriptor(PatchLineMetadata),
		}, nil
	}
	if len(line) == 0 {
		return parsedPatchLine{}, errors.New("patch hunk contains an empty unprefixed line")
	}
	switch line[0] {
	case ' ':
		if state.oldRemaining == 0 || state.newRemaining == 0 {
			return parsedPatchLine{}, errors.New("patch hunk context exceeds its declared range")
		}
		parsed := parsedPatchLine{
			descriptor: plainPatchLineDescriptor(PatchLineContext),
			oldLine:    state.oldLine,
			newLine:    state.newLine,
		}
		state.oldLine++
		state.newLine++
		state.oldRemaining--
		state.newRemaining--
		return parsed, nil
	case '+':
		if state.newRemaining == 0 {
			return parsedPatchLine{}, errors.New("patch hunk additions exceed their declared range")
		}
		parsed := parsedPatchLine{
			descriptor: plainPatchLineDescriptor(PatchLineAddition),
			newLine:    state.newLine,
		}
		state.newLine++
		state.newRemaining--
		return parsed, nil
	case '-':
		if state.oldRemaining == 0 {
			return parsedPatchLine{}, errors.New("patch hunk deletions exceed their declared range")
		}
		parsed := parsedPatchLine{
			descriptor: plainPatchLineDescriptor(PatchLineDeletion),
			oldLine:    state.oldLine,
		}
		state.oldLine++
		state.oldRemaining--
		return parsed, nil
	default:
		return parsedPatchLine{}, fmt.Errorf(
			"patch hunk contains unsupported line prefix %q",
			line[0],
		)
	}
}

func (patch *PreparedPatch) appendParsedLine(
	row uint32,
	activeHunk uint32,
	parsed parsedPatchLine,
) {
	kind := parsed.descriptor.kind()
	if activeHunk != noPatchHunk && noNewlineSidesAfter(kind).valid() {
		if patch.hunkFirst[activeHunk] == noPatchHunk {
			patch.hunkFirst[activeHunk] = row
		}
		patch.hunkLast[activeHunk] = row
	}
	patch.lineKinds = append(patch.lineKinds, parsed.descriptor)
	patch.oldLines = append(patch.oldLines, parsed.oldLine)
	patch.newLines = append(patch.newLines, parsed.newLine)
	patch.maxOldLine = max(patch.maxOldLine, parsed.oldLine)
	patch.maxNewLine = max(patch.maxNewLine, parsed.newLine)
	if parsed.oldLine != 0 {
		patch.oldLineRows = appendBoundedPatchUint32(
			patch.oldLineRows, row, MaxPatchLines,
		)
	}
	if parsed.newLine != 0 {
		patch.newLineRows = appendBoundedPatchUint32(
			patch.newLineRows, row, MaxPatchLines,
		)
	}
	patch.lineHunks = append(patch.lineHunks, activeHunk)
}

func newPreparedPatch(text []byte, lineCapacity int) *PreparedPatch {
	return &PreparedPatch{
		text:        text,
		lineOffsets: make([]PatchOffset, 0, lineCapacity),
		lineKinds:   make([]patchLineDescriptor, 0, lineCapacity),
		oldLines:    make([]uint32, 0, lineCapacity),
		newLines:    make([]uint32, 0, lineCapacity),
		lineHunks:   make([]uint32, 0, lineCapacity),
	}
}

func (patch *PreparedPatch) appendHunk(line uint32) {
	if len(patch.hunkLines) == cap(patch.hunkLines) {
		capacity := nextBoundedPatchCapacity(len(patch.hunkLines)+1, cap(patch.hunkLines), MaxHunks)
		lines := make([]uint32, len(patch.hunkLines), capacity)
		first := make([]uint32, len(patch.hunkFirst), capacity)
		last := make([]uint32, len(patch.hunkLast), capacity)
		copy(lines, patch.hunkLines)
		copy(first, patch.hunkFirst)
		copy(last, patch.hunkLast)
		patch.hunkLines = lines
		patch.hunkFirst = first
		patch.hunkLast = last
	}
	patch.hunkLines = append(patch.hunkLines, line)
	patch.hunkFirst = append(patch.hunkFirst, noPatchHunk)
	patch.hunkLast = append(patch.hunkLast, noPatchHunk)
}

func appendBoundedPatchUint32(values []uint32, value uint32, limit int) []uint32 {
	if len(values) == cap(values) {
		capacity := nextBoundedPatchCapacity(len(values)+1, cap(values), limit)
		next := make([]uint32, len(values), capacity)
		copy(next, values)
		values = next
	}
	return append(values, value)
}

func nextBoundedPatchCapacity(required, current, limit int) int {
	half := limit / 2
	if required > half {
		return limit
	}
	return min(half, max(required, max(1, current*2)))
}

func appendPatchOffsetCancellable(
	cancelled func() error,
	offsets []PatchOffset,
	offset uint32,
) ([]PatchOffset, error) {
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	if len(offsets) == MaxPatchLines {
		return nil, fmt.Errorf("patch lines exceed %d", MaxPatchLines)
	}
	return append(offsets, PatchOffset(offset)), nil
}

func nextPatchLineCancellable(
	cancelled func() error,
	remaining []byte,
) ([]byte, []byte, uint32, error) {
	index, err := indexPatchByteCancellable(cancelled, remaining, '\n')
	if err != nil {
		return nil, nil, 0, err
	}
	if index < 0 {
		return remaining, nil, 0, nil
	}
	return remaining[:index], remaining[index+1:], uint32FromInt(index + 1), nil
}

func patchLineCapacity(cancelled func() error, text []byte) (int, error) {
	lines := 1
	for start := 0; start < len(text); start += patchPollBytes {
		if err := cancellationError(cancelled); err != nil {
			return 0, err
		}
		end := min(start+patchPollBytes, len(text))
		lines += bytes.Count(text[start:end], []byte{'\n'})
		if lines >= MaxPatchLines {
			return MaxPatchLines, nil
		}
	}
	if err := cancellationError(cancelled); err != nil {
		return 0, err
	}
	return lines, nil
}

func indexPatchByteCancellable(
	cancelled func() error,
	value []byte,
	target byte,
) (int, error) {
	for start := 0; start < len(value); start += patchPollBytes {
		if err := cancellationError(cancelled); err != nil {
			return -1, err
		}
		end := min(start+patchPollBytes, len(value))
		if index := bytes.IndexByte(value[start:end], target); index >= 0 {
			return start + index, nil
		}
	}
	return -1, cancellationError(cancelled)
}

func parseHunkHeader(line []byte) (uint32, uint32, uint32, uint32, error) {
	searchEnd := min(len(line), maxHunkHeaderBytes)
	closeIndex := bytes.Index(line[3:searchEnd], []byte(" @@"))
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

// Hunks returns the receiver's hunks value.
func (patch *PreparedPatch) Hunks() uint32 {
	if patch == nil {
		return 0
	}
	return patch.hunks
}

// Lines returns the receiver's lines value.
func (patch *PreparedPatch) Lines() uint32 {
	if patch == nil {
		return 0
	}
	return uint32FromInt(len(patch.lineOffsets))
}

// RetainedBytes returns the receiver's retained bytes value.
func (patch *PreparedPatch) RetainedBytes() int64 {
	if patch == nil {
		return 0
	}
	return preparedPatchMetadataBytes + int64(cap(patch.text)) +
		int64(cap(patch.oldLineRows))*patchUint32StorageBytes +
		int64(cap(patch.hunkFirst))*patchUint32StorageBytes +
		int64(cap(patch.lineOffsets))*patchUint32StorageBytes +
		int64(cap(patch.lineKinds)) +
		int64(cap(patch.oldLines))*patchUint32StorageBytes +
		int64(cap(patch.newLines))*patchUint32StorageBytes +
		int64(cap(patch.lineHunks))*patchUint32StorageBytes +
		int64(cap(patch.newLineRows))*patchUint32StorageBytes +
		int64(cap(patch.hunkLast))*patchUint32StorageBytes +
		int64(cap(patch.hunkLines))*patchUint32StorageBytes
}

// PatchLineForSource performs the patch line for source operation.
func (review Review) PatchLineForSource(
	fileID FileID,
	side LineSide,
	line LineNumber,
) (uint32, bool) {
	state := review.patchState(fileID)
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

// RequestPatch performs the request patch operation.
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
	current := review.patchState(fileID)
	if current.Status == PatchPending {
		return review, PatchRequest{}, fmt.Errorf("%w: patch is already pending", ErrInvalidPatchTransition)
	}
	next := review.clone()
	next.patch = presentationPatch{
		file:  fileID,
		state: PatchState{Status: PatchPending},
		set:   true,
	}
	return next, PatchRequest{
		Workspace: review.workspace,
		Handle:    file.PatchHandle(),
		Context:   context,
	}, nil
}

// ApplyPatch performs the apply patch operation.
func (review Review) ApplyPatch(result PatchResult) (Review, error) {
	if result.Workspace != review.workspace ||
		result.Handle.SourceID() != review.snapshot.SourceID() {
		return review, fmt.Errorf("%w: stale workspace or source", ErrInvalidPatchTransition)
	}
	fileID := result.Handle.FileID()
	file, ok := review.file(fileID)
	if !ok || file.PatchHandle() != result.Handle ||
		review.patchState(fileID).Status != PatchPending {
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
	state := PatchState{
		Status:   result.Status,
		Hunks:    result.Prepared.Hunks(),
		Lines:    result.Prepared.Lines(),
		Detail:   result.Detail,
		prepared: result.Prepared,
	}
	if result.Status != PatchReady {
		state = PatchState{Status: result.Status, Detail: result.Detail}
	}
	next.patch = presentationPatch{
		file:          fileID,
		state:         state,
		retainedBytes: state.RetainedBytes(),
		set:           true,
	}
	return next, nil
}

// PatchLines performs the patch lines operation.
func (review Review) PatchLines(fileID FileID, start, count uint32, maxLineBytes int64) []PatchLine {
	state := review.patchState(fileID)
	if state.Status != PatchReady || count == 0 || start >= state.Lines || maxLineBytes <= 0 {
		return nil
	}
	end := start + min(count, state.Lines-start)
	lines := make([]PatchLine, 0, end-start)
	for line := start; line < end; line++ {
		begin := state.prepared.lineOffsets[line].Uint32()
		finish := uint32FromInt(len(state.prepared.text))
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
			finish = begin + uint32FromInt64(retained)
		}
		lines = append(lines, PatchLine{
			Number:     line,
			Text:       PatchText(state.prepared.text[begin:finish]),
			Truncated:  truncated,
			descriptor: state.prepared.lineKinds[line],
			oldLine:    state.prepared.oldLines[line],
			newLine:    state.prepared.newLines[line],
			hunk:       state.prepared.lineHunks[line],
		})
	}
	return lines
}

// DropPatch performs the drop patch operation.
func (review Review) DropPatch(fileID FileID) Review {
	if !review.patch.set || review.patch.file != fileID {
		return review
	}
	next := review.clone()
	next.patch = presentationPatch{}
	next.hunkMarks = review.hunkMarks.dropFile(fileID)
	return next
}

// AdjacentHunkLine performs the adjacent hunk line operation.
func (review Review) AdjacentHunkLine(
	fileID FileID,
	current uint32,
	direction NavigationDirection,
	count uint16,
) (uint32, bool) {
	state := review.patchState(fileID)
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

// HunkRows performs the hunk rows operation.
func (review Review) HunkRows(id HunkID) (uint32, uint32, bool) {
	state := review.patchState(id.FileID())
	index := id.Index().Uint32()
	if state.Status != PatchReady || state.prepared == nil ||
		index >= uint32FromInt(len(state.prepared.hunkFirst)) {
		return 0, 0, false
	}
	first := state.prepared.hunkFirst[index]
	last := state.prepared.hunkLast[index]
	return first, last, first != noPatchHunk && last != noPatchHunk
}

// Patch performs the patch operation.
func (review Review) Patch(fileID FileID) PatchState {
	return review.patchState(fileID)
}

// RetainedPatchBytes returns the receiver's retained patch bytes value.
func (review Review) RetainedPatchBytes() int64 {
	return review.patch.retainedBytes
}
