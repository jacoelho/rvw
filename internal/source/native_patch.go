package source

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jacoelho/rvw/internal/review"
)

type nativePatchInput struct { //nolint:govet // Field order follows old/new side ownership.
	repository *repositorySession
	oldPath    string
	newPath    string
	oldMode    review.FileMode
	newMode    review.FileMode
	oldOID     review.ObjectID
	newOID     review.ObjectID
	hasOldOID  bool
	hasNewOID  bool
	context    review.PatchContext
	kind       review.ChangeKind
	similarity uint16
	binary     bool
}

type nativePatchRow struct {
	kind      nativeEditKind
	oldIndex  int
	newIndex  int
	oldBefore int
	newBefore int
}

func acquireNativePatch(
	ctx context.Context,
	document *Document,
	file review.File,
	patchContext review.PatchContext,
) (*nativePatchInput, error) {
	oldPath := ""
	if path, ok := file.OldPath(); ok {
		oldPath = string(path.Raw())
	}
	newPath := ""
	if path, ok := file.NewPath(); ok {
		newPath = string(path.Raw())
	}
	if _, conflict := document.revision.unmerged[primaryAvailablePath(oldPath, newPath)]; conflict {
		return nil, errors.New("unmerged index path has no single prepared patch")
	}
	change := file.Change()
	if change.Kind() == review.ChangeSubmodule {
		return nil, errors.New("submodule changes do not have a text patch")
	}
	input := &nativePatchInput{
		repository: document.revision.repository,
		oldPath:    oldPath,
		newPath:    newPath,
		context:    patchContext,
		kind:       change.Kind(),
		binary:     change.Kind() == review.ChangeBinary,
	}
	if mode, ok := change.Mode(); ok {
		input.oldMode, input.newMode = mode.Old(), mode.New()
	} else {
		zeroMode, err := review.NewFileMode("000000")
		if err != nil {
			return nil, err
		}
		regularMode, err := review.NewFileMode("100644")
		if err != nil {
			return nil, err
		}
		input.oldMode = zeroMode
		input.newMode = input.oldMode
		if oldPath != "" {
			input.oldMode = regularMode
		}
		if newPath != "" {
			input.newMode = regularMode
		}
	}
	if oid, ok := file.Content(review.LineOld); ok {
		input.oldOID, input.hasOldOID = oid, true
		if err := document.revision.repository.pinObjectContent(ctx, reviewHash(oid)); err != nil {
			return nil, err
		}
	}
	if oid, ok := file.Content(review.LineNew); ok {
		input.newOID, input.hasNewOID = oid, true
		if err := document.revision.repository.pinObjectContent(ctx, reviewHash(oid)); err != nil {
			return nil, err
		}
	}
	if input.hasOldOID && input.hasNewOID {
		input.similarity = document.revision.similarities[nativeSimilarityPair{
			old: input.oldOID,
			new: input.newOID,
		}]
		if input.similarity == 0 && input.oldOID == input.newOID {
			input.similarity = 60_000
		}
	}
	return input, nil
}

func prepareNativePatch(
	ctx context.Context,
	input *nativePatchInput,
) (*review.PreparedPatch, error) {
	if input == nil {
		return nil, errors.New("native patch input is nil")
	}
	lines := nativePatchMetadata(input)
	if input.binary {
		oldName := nativePatchSideName("a", input.oldPath)
		newName := nativePatchSideName("b", input.newPath)
		lines = append(lines, structuredMetadata(
			[]byte("Binary files "+oldName+" and "+newName+" differ"),
		))
		return review.PrepareStructuredPatchCancellable(
			func() error { return context.Cause(ctx) }, lines,
		)
	}
	oldSize, newSize, err := nativePatchContentSizes(input)
	if err != nil {
		return nil, err
	}
	if oldSize+newSize > 1<<20 {
		return prepareNativeDiskPatch(ctx, input, lines, oldSize, newSize)
	}
	oldContents, err := readNativePatchSide(ctx, input, review.LineOld)
	if err != nil {
		return nil, err
	}
	newContents, err := readNativePatchSide(ctx, input, review.LineNew)
	if err != nil {
		return nil, err
	}
	oldLines, err := tokenizeNativeLines(ctx, oldContents)
	if err != nil {
		return nil, err
	}
	newLines, err := tokenizeNativeLines(ctx, newContents)
	if err != nil {
		return nil, err
	}
	edits, err := histogramEdits(
		ctx, oldContents, newContents, oldLines, newLines,
	)
	if err != nil {
		return nil, err
	}
	rows, changed, err := expandNativePatchRows(ctx, edits)
	if err != nil {
		return nil, err
	}
	if len(changed) == 0 {
		return review.PrepareStructuredPatchCancellable(
			func() error { return context.Cause(ctx) }, lines,
		)
	}
	lines = append(lines,
		structuredMetadata([]byte("--- "+nativePatchSideName("a", input.oldPath))),
		structuredMetadata([]byte("+++ "+nativePatchSideName("b", input.newPath))),
	)
	contextLines := int(input.context.UnifiedLines())
	if input.context.WholeFile() {
		contextLines = max(len(rows), len(oldLines), len(newLines))
	}
	windows := nativePatchWindows(changed, len(rows), contextLines)
	for hunkIndex, window := range windows {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		oldCount, newCount := 0, 0
		for _, row := range rows[window[0]:window[1]] {
			if row.kind != nativeEditAdd {
				oldCount++
			}
			if row.kind != nativeEditDelete {
				newCount++
			}
		}
		first := rows[window[0]]
		oldStart := first.oldBefore
		if oldCount != 0 {
			oldStart++
		}
		newStart := first.newBefore
		if newCount != 0 {
			newStart++
		}
		header := []byte("@@ -" + patchRange(oldStart, oldCount) +
			" +" + patchRange(newStart, newCount) + " @@")
		lines = append(lines, review.StructuredPatchLine{
			Text: header, Kind: review.PatchLineHunkHeader,
			Hunk: uint32(hunkIndex), HasHunk: true,
		})
		for _, row := range rows[window[0]:window[1]] {
			structured, marker := nativeStructuredRow(
				oldContents, newContents, oldLines, newLines, row, uint32(hunkIndex),
			)
			lines = append(lines, structured)
			if marker != nil {
				lines = append(lines, *marker)
			}
		}
	}
	return review.PrepareStructuredPatchCancellable(
		func() error { return context.Cause(ctx) }, lines,
	)
}

func nativePatchContentSizes(input *nativePatchInput) (uint64, uint64, error) {
	if input == nil || input.repository == nil {
		return 0, 0, errors.New("native patch repository is unavailable")
	}
	size := func(oid review.ObjectID, present bool) (uint64, error) {
		if !present {
			return 0, nil
		}
		content, ok := input.repository.contents[reviewHash(oid)]
		if !ok || content.size < 0 {
			return 0, errors.New("native patch content was not pinned")
		}
		return uint64(content.size), nil
	}
	oldSize, err := size(input.oldOID, input.hasOldOID)
	if err != nil {
		return 0, 0, err
	}
	newSize, err := size(input.newOID, input.hasNewOID)
	return oldSize, newSize, err
}

func readNativePatchSide(
	ctx context.Context,
	input *nativePatchInput,
	side review.LineSide,
) ([]byte, error) {
	switch side {
	case review.LineOld:
		if !input.hasOldOID {
			return nil, nil
		}
		return readNativeRecordSide(ctx, input.repository, &input.oldOID)
	case review.LineNew:
		if !input.hasNewOID {
			return nil, nil
		}
		return readNativeRecordSide(ctx, input.repository, &input.newOID)
	default:
		return nil, errors.New("native patch side is invalid")
	}
}

type diskPatchWindow struct {
	rowStart  uint64
	rowEnd    uint64
	oldBefore uint64
	newBefore uint64
	oldCount  uint64
	newCount  uint64
	located   bool
}

func prepareNativeDiskPatch(
	ctx context.Context,
	input *nativePatchInput,
	metadata []review.StructuredPatchLine,
	oldSize, newSize uint64,
) (_ *review.PreparedPatch, returnErr error) {
	oldOID, newOID := nativePatchOIDs(input)
	oldContent, err := openPinnedDiskContent(ctx, input.repository, oldOID)
	if err != nil {
		return nil, err
	}
	newContent, err := openPinnedDiskContent(ctx, input.repository, newOID)
	if err != nil {
		return nil, errors.Join(err, closeDiskContents(oldContent))
	}
	workspace, err := input.repository.ensureWorkspace()
	if err != nil {
		return nil, errors.Join(err, closeDiskContents(oldContent, newContent))
	}
	analysis, err := analyzeDiskDiff(ctx, workspace, oldContent, newContent, oldSize, newSize)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, analysis.Close()) }()
	windows, err := diskPatchWindows(ctx, analysis, input.context)
	if err != nil {
		return nil, err
	}
	if len(windows) == 0 {
		return review.PrepareStructuredPatchCancellable(
			func() error { return context.Cause(ctx) }, metadata,
		)
	}
	metadata = append(metadata,
		structuredMetadata([]byte("--- "+nativePatchSideName("a", input.oldPath))),
		structuredMetadata([]byte("+++ "+nativePatchSideName("b", input.newPath))),
	)
	return review.PrepareGeneratedPatchCancellable(
		func() error { return context.Cause(ctx) },
		func(yield func(review.StructuredPatchLine) error) error {
			return generateDiskPatch(ctx, analysis, windows, metadata, yield)
		},
	)
}

func nativePatchOIDs(input *nativePatchInput) (*review.ObjectID, *review.ObjectID) {
	var oldOID, newOID *review.ObjectID
	if input.hasOldOID {
		oldOID = &input.oldOID
	}
	if input.hasNewOID {
		newOID = &input.newOID
	}
	return oldOID, newOID
}

func diskPatchWindows(
	ctx context.Context,
	analysis *diskDiffAnalysis,
	patchContext review.PatchContext,
) ([]diskPatchWindow, error) {
	if analysis == nil || analysis.edits == nil || analysis.additions+analysis.deletions == 0 {
		return nil, nil
	}
	contextLines := uint64(patchContext.UnifiedLines())
	windows := make([]diskPatchWindow, 0, min(analysis.editCount, uint64(review.MaxHunks)))
	row := uint64(0)
	for index := range analysis.editCount {
		if index%4_096 == 0 {
			if err := context.Cause(ctx); err != nil {
				return nil, err
			}
		}
		edit, err := readDiskEditAt(analysis.edits, index)
		if err != nil {
			return nil, err
		}
		if edit.kind != nativeEditEqual {
			start := row - min(row, contextLines)
			end := row + edit.count
			if contextLines <= ^uint64(0)-end {
				end += contextLines
			} else {
				end = ^uint64(0)
			}
			if len(windows) != 0 && start <= windows[len(windows)-1].rowEnd {
				windows[len(windows)-1].rowEnd = max(windows[len(windows)-1].rowEnd, end)
			} else {
				if len(windows) == review.MaxHunks {
					return nil, fmt.Errorf("patch hunks exceed %d", review.MaxHunks)
				}
				windows = append(windows, diskPatchWindow{rowStart: start, rowEnd: end})
			}
		}
		row += edit.count
	}
	for index := range windows {
		windows[index].rowEnd = min(windows[index].rowEnd, row)
	}
	if patchContext.WholeFile() {
		windows = []diskPatchWindow{{rowEnd: row}}
	}
	if err := locateDiskPatchWindows(ctx, analysis, windows); err != nil {
		return nil, err
	}
	return windows, nil
}

func locateDiskPatchWindows(
	ctx context.Context,
	analysis *diskDiffAnalysis,
	windows []diskPatchWindow,
) error {
	windowIndex, row := 0, uint64(0)
	for editIndex := uint64(0); editIndex < analysis.editCount && windowIndex < len(windows); editIndex++ {
		if editIndex%4_096 == 0 {
			if err := context.Cause(ctx); err != nil {
				return err
			}
		}
		edit, err := readDiskEditAt(analysis.edits, editIndex)
		if err != nil {
			return err
		}
		editEnd := row + edit.count
		for windowIndex < len(windows) && windows[windowIndex].rowStart < editEnd {
			window := &windows[windowIndex]
			overlapStart := max(row, window.rowStart)
			overlapEnd := min(editEnd, window.rowEnd)
			if overlapStart < overlapEnd {
				delta := overlapStart - row
				if !window.located {
					window.oldBefore = edit.oldStart + diskEditOldAdvance(edit.kind, delta)
					window.newBefore = edit.newStart + diskEditNewAdvance(edit.kind, delta)
					window.located = true
				}
				count := overlapEnd - overlapStart
				window.oldCount += diskEditOldAdvance(edit.kind, count)
				window.newCount += diskEditNewAdvance(edit.kind, count)
			}
			if window.rowEnd <= editEnd {
				windowIndex++
				continue
			}
			break
		}
		row = editEnd
	}
	for _, window := range windows {
		if !window.located {
			return errors.New("disk patch window is outside the edit stream")
		}
	}
	return nil
}

func diskEditOldAdvance(kind nativeEditKind, count uint64) uint64 {
	if kind == nativeEditEqual || kind == nativeEditDelete {
		return count
	}
	return 0
}

func diskEditNewAdvance(kind nativeEditKind, count uint64) uint64 {
	if kind == nativeEditEqual || kind == nativeEditAdd {
		return count
	}
	return 0
}

func generateDiskPatch(
	ctx context.Context,
	analysis *diskDiffAnalysis,
	windows []diskPatchWindow,
	metadata []review.StructuredPatchLine,
	yield func(review.StructuredPatchLine) error,
) error {
	for _, line := range metadata {
		if err := yield(line); err != nil {
			return err
		}
	}
	windowIndex, row := 0, uint64(0)
	buffer := make([]byte, 0, 64<<10)
	for editIndex := uint64(0); editIndex < analysis.editCount && windowIndex < len(windows); editIndex++ {
		if editIndex%4_096 == 0 {
			if err := context.Cause(ctx); err != nil {
				return err
			}
		}
		edit, err := readDiskEditAt(analysis.edits, editIndex)
		if err != nil {
			return err
		}
		editEnd := row + edit.count
		for windowIndex < len(windows) && windows[windowIndex].rowStart < editEnd {
			window := windows[windowIndex]
			if row <= window.rowStart {
				header := diskPatchHunkHeader(window)
				if err := yield(review.StructuredPatchLine{
					Text: header, Kind: review.PatchLineHunkHeader,
					Hunk: uint32(windowIndex), HasHunk: true,
				}); err != nil {
					return err
				}
			}
			overlapStart := max(row, window.rowStart)
			overlapEnd := min(editEnd, window.rowEnd)
			for rowIndex := overlapStart; rowIndex < overlapEnd; rowIndex++ {
				delta := rowIndex - row
				line, marker, nextBuffer, err := diskStructuredPatchLine(
					analysis.kernel, edit, delta, uint32(windowIndex), buffer,
				)
				if err != nil {
					return err
				}
				buffer = nextBuffer
				if err := yield(line); err != nil {
					return err
				}
				if marker != nil {
					if err := yield(*marker); err != nil {
						return err
					}
				}
			}
			if window.rowEnd <= editEnd {
				windowIndex++
				continue
			}
			break
		}
		row = editEnd
	}
	if windowIndex != len(windows) {
		return errors.New("disk patch did not consume every window")
	}
	return nil
}

func diskPatchHunkHeader(window diskPatchWindow) []byte {
	oldStart := window.oldBefore
	if window.oldCount != 0 {
		oldStart++
	}
	newStart := window.newBefore
	if window.newCount != 0 {
		newStart++
	}
	return []byte("@@ -" + patchRange64(oldStart, window.oldCount) +
		" +" + patchRange64(newStart, window.newCount) + " @@")
}

func patchRange64(start, count uint64) string {
	if count == 1 {
		return strconv.FormatUint(start, 10)
	}
	return strconv.FormatUint(start, 10) + "," + strconv.FormatUint(count, 10)
}

func diskStructuredPatchLine(
	kernel *diskDiffKernel,
	edit diskEdit,
	delta uint64,
	hunk uint32,
	buffer []byte,
) (review.StructuredPatchLine, *review.StructuredPatchLine, []byte, error) {
	var side *diskDiffSide
	var index uint64
	line := review.StructuredPatchLine{Hunk: hunk, HasHunk: true}
	switch edit.kind {
	case nativeEditEqual:
		side, index = kernel.old, edit.oldStart+delta
		line.Kind = review.PatchLineContext
		line.OldLine = uint32(index + 1)                 //nolint:gosec // Disk token admission is below uint32.
		line.NewLine = uint32(edit.newStart + delta + 1) //nolint:gosec // Disk token admission is below uint32.
	case nativeEditDelete:
		side, index = kernel.old, edit.oldStart+delta
		line.Kind = review.PatchLineDeletion
		line.OldLine = uint32(index + 1) //nolint:gosec // Disk token admission is below uint32.
	case nativeEditAdd:
		side, index = kernel.new, edit.newStart+delta
		line.Kind = review.PatchLineAddition
		line.NewLine = uint32(index + 1) //nolint:gosec // Disk token admission is below uint32.
	default:
		return review.StructuredPatchLine{}, nil, buffer, errors.New("disk patch edit kind is invalid")
	}
	record, err := side.line(index)
	if err != nil {
		return review.StructuredPatchLine{}, nil, buffer, err
	}
	if record.length >= uint64(review.MaxPatchBytes) {
		return review.StructuredPatchLine{}, nil, buffer,
			fmt.Errorf("patch exceeds %d bytes", review.MaxPatchBytes)
	}
	required := int(record.length) + 1
	if cap(buffer) < required {
		buffer = make([]byte, required)
	} else {
		buffer = buffer[:required]
	}
	switch edit.kind {
	case nativeEditDelete:
		buffer[0] = '-'
	case nativeEditAdd:
		buffer[0] = '+'
	case nativeEditEqual:
		buffer[0] = ' '
	}
	if record.length != 0 {
		if _, err := side.content.ReadAt(buffer[1:], int64(record.offset)); err != nil { //nolint:gosec // Content limits bound the offset.
			return review.StructuredPatchLine{}, nil, buffer, err
		}
	}
	line.Text = buffer
	if record.terminated {
		return line, nil, buffer, nil
	}
	marker := &review.StructuredPatchLine{
		Text: []byte(`\ No newline at end of file`),
		Kind: review.PatchLineNoNewline, Hunk: hunk, HasHunk: true,
		OldMarker: edit.kind != nativeEditAdd,
		NewMarker: edit.kind != nativeEditDelete,
	}
	return line, marker, buffer, nil
}

func nativePatchMetadata(input *nativePatchInput) []review.StructuredPatchLine {
	oldName := nativePatchDiffName("a", input.oldPath, input.newPath)
	newName := nativePatchDiffName("b", input.newPath, input.oldPath)
	lines := []review.StructuredPatchLine{structuredMetadata(
		[]byte("diff --git " + oldName + " " + newName),
	)}
	switch input.kind {
	case review.ChangeAdded:
		lines = append(lines, structuredMetadata([]byte("new file mode "+input.newMode.String())))
	case review.ChangeDeleted:
		lines = append(lines, structuredMetadata([]byte("deleted file mode "+input.oldMode.String())))
	case review.ChangeRenamed, review.ChangeRenameOnly:
		lines = appendSimilarityMetadata(lines, input.similarity)
		lines = append(lines,
			structuredMetadata([]byte("rename from "+quoteGitPath(input.oldPath))),
			structuredMetadata([]byte("rename to "+quoteGitPath(input.newPath))),
		)
	case review.ChangeCopied:
		lines = appendSimilarityMetadata(lines, input.similarity)
		lines = append(lines,
			structuredMetadata([]byte("copy from "+quoteGitPath(input.oldPath))),
			structuredMetadata([]byte("copy to "+quoteGitPath(input.newPath))),
		)
	case review.ChangeModified, review.ChangeBinary, review.ChangeModeOnly,
		review.ChangeSubmodule, review.ChangeObjectTypeChanged:
	}
	if input.oldPath != "" && input.newPath != "" && input.oldMode != input.newMode {
		lines = append(lines,
			structuredMetadata([]byte("old mode "+input.oldMode.String())),
			structuredMetadata([]byte("new mode "+input.newMode.String())),
		)
	}
	if input.hasOldOID || input.hasNewOID {
		oldOID, newOID := "0000000", "0000000"
		if input.hasOldOID {
			oldOID = input.oldOID.String()[:7]
		}
		if input.hasNewOID {
			newOID = input.newOID.String()[:7]
		}
		index := "index " + oldOID + ".." + newOID
		if input.oldPath != "" && input.newPath != "" && input.oldMode == input.newMode {
			index += " " + input.newMode.String()
		}
		lines = append(lines, structuredMetadata([]byte(index)))
	}
	return lines
}

func appendSimilarityMetadata(
	lines []review.StructuredPatchLine,
	score uint16,
) []review.StructuredPatchLine {
	if score == 0 {
		return lines
	}
	percent := uint64(score) * 100 / 60_000
	return append(lines, structuredMetadata([]byte(
		"similarity index "+strconv.FormatUint(percent, 10)+"%",
	)))
}

func structuredMetadata(text []byte) review.StructuredPatchLine {
	return review.StructuredPatchLine{Text: text, Kind: review.PatchLineMetadata}
}

func expandNativePatchRows(
	ctx context.Context,
	edits []nativeEdit,
) ([]nativePatchRow, []int, error) {
	rows := make([]nativePatchRow, 0)
	changed := make([]int, 0)
	oldPosition, newPosition := 0, 0
	for editIndex, edit := range edits {
		if editIndex%4_096 == 0 {
			if err := context.Cause(ctx); err != nil {
				return nil, nil, err
			}
		}
		for range edit.count {
			if len(rows) == review.MaxPatchLines {
				return nil, nil, &LimitError{Resource: LimitDiffLineTokens, Limit: review.MaxPatchLines}
			}
			row := nativePatchRow{
				kind: edit.kind, oldIndex: -1, newIndex: -1,
				oldBefore: oldPosition, newBefore: newPosition,
			}
			switch edit.kind {
			case nativeEditEqual:
				row.oldIndex, row.newIndex = oldPosition, newPosition
				oldPosition++
				newPosition++
			case nativeEditDelete:
				row.oldIndex = oldPosition
				oldPosition++
				changed = append(changed, len(rows))
			case nativeEditAdd:
				row.newIndex = newPosition
				newPosition++
				changed = append(changed, len(rows))
			default:
				return nil, nil, errors.New("native patch edit kind is invalid")
			}
			rows = append(rows, row)
		}
	}
	return rows, changed, nil
}

func nativePatchWindows(changed []int, rows, contextLines int) [][2]int {
	windows := make([][2]int, 0, len(changed))
	for _, row := range changed {
		start := max(0, row-contextLines)
		end := min(rows, row+contextLines+1)
		if len(windows) != 0 && start <= windows[len(windows)-1][1] {
			windows[len(windows)-1][1] = max(windows[len(windows)-1][1], end)
			continue
		}
		windows = append(windows, [2]int{start, end})
	}
	return windows
}

func nativeStructuredRow(
	oldContents, newContents []byte,
	oldLines, newLines []nativeLine,
	row nativePatchRow,
	hunk uint32,
) (review.StructuredPatchLine, *review.StructuredPatchLine) {
	structured := review.StructuredPatchLine{Hunk: hunk, HasHunk: true}
	var contents []byte
	marker := review.StructuredPatchLine{
		Text: []byte(`\ No newline at end of file`),
		Kind: review.PatchLineNoNewline, Hunk: hunk, HasHunk: true,
	}
	switch row.kind {
	case nativeEditEqual:
		line := oldLines[row.oldIndex]
		contents = oldContents[line.start:line.end]
		structured.Kind = review.PatchLineContext
		structured.OldLine = uint32(row.oldIndex + 1) //nolint:gosec // patch admission bounds rows below uint32.
		structured.NewLine = uint32(row.newIndex + 1) //nolint:gosec // patch admission bounds rows below uint32.
		if !line.terminated {
			marker.OldMarker, marker.NewMarker = true, true
		}
	case nativeEditDelete:
		line := oldLines[row.oldIndex]
		contents = oldContents[line.start:line.end]
		structured.Kind = review.PatchLineDeletion
		structured.OldLine = uint32(row.oldIndex + 1) //nolint:gosec // patch admission bounds rows below uint32.
		if !line.terminated {
			marker.OldMarker = true
		}
	case nativeEditAdd:
		line := newLines[row.newIndex]
		contents = newContents[line.start:line.end]
		structured.Kind = review.PatchLineAddition
		structured.NewLine = uint32(row.newIndex + 1) //nolint:gosec // patch admission bounds rows below uint32.
		if !line.terminated {
			marker.NewMarker = true
		}
	}
	prefix := byte(' ')
	switch row.kind {
	case nativeEditDelete:
		prefix = '-'
	case nativeEditAdd:
		prefix = '+'
	case nativeEditEqual:
	}
	structured.Text = make([]byte, len(contents)+1)
	structured.Text[0] = prefix
	copy(structured.Text[1:], contents)
	if !marker.OldMarker && !marker.NewMarker {
		return structured, nil
	}
	return structured, &marker
}

func patchRange(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(count)
}

func nativePatchSideName(prefix, path string) string {
	if path == "" {
		return "/dev/null"
	}
	return quoteGitPath(prefix + "/" + path)
}

func nativePatchDiffName(prefix, path, fallback string) string {
	if path == "" {
		path = fallback
	}
	return quoteGitPath(prefix + "/" + path)
}

func quoteGitPath(path string) string {
	safe := path != ""
	for index := range len(path) {
		value := path[index]
		if value < 0x20 || value >= 0x7f || value == '"' || value == '\\' || value == '\t' {
			safe = false
			break
		}
	}
	if safe && !strings.HasPrefix(path, "-") {
		return path
	}
	var builder strings.Builder
	builder.WriteByte('"')
	for index := range len(path) {
		value := path[index]
		switch value {
		case '"', '\\':
			builder.WriteByte('\\')
			builder.WriteByte(value)
		case '\t':
			builder.WriteString(`\t`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		default:
			if value < 0x20 || value >= 0x7f {
				builder.WriteByte('\\')
				builder.WriteByte('0' + (value >> 6))
				builder.WriteByte('0' + ((value >> 3) & 7))
				builder.WriteByte('0' + (value & 7))
			} else {
				builder.WriteByte(value)
			}
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func primaryAvailablePath(oldPath, newPath string) string {
	if newPath != "" {
		return newPath
	}
	return oldPath
}
