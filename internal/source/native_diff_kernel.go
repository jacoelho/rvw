package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/jacoelho/rvw/internal/review"
)

type nativeLine struct {
	start      int
	end        int
	hash       uint64
	terminated bool
}

type nativeEditKind uint8

const (
	nativeEditEqual nativeEditKind = iota + 1
	nativeEditDelete
	nativeEditAdd
)

type nativeEdit struct {
	kind     nativeEditKind
	oldStart int
	newStart int
	count    int
}

type histogramTask struct {
	oldStart int
	oldEnd   int
	newStart int
	newEnd   int
	edit     nativeEdit
	fixed    bool
}

func populateNativeDiff(
	ctx context.Context,
	session *repositorySession,
	record *diffRecord,
) error {
	if record == nil {
		return errors.New("native diff record is nil")
	}
	if record.oldOID != nil && record.newOID != nil && *record.oldOID == *record.newOID {
		return nil
	}
	oldKind := objectKind(record.oldMode)
	newKind := objectKind(record.newMode)
	if oldKind == review.ObjectGitlink || newKind == review.ObjectGitlink {
		return nil
	}
	if err := pinNativeRecordSides(ctx, session, record); err != nil {
		return err
	}
	oldSize, newSize, err := nativeRecordContentSizes(session, record)
	if err != nil {
		return err
	}
	if oldSize+newSize > 1<<20 {
		return populateNativeDiskDiff(ctx, session, record, oldSize, newSize)
	}
	oldContents, err := readNativeRecordSide(ctx, session, record.oldOID)
	if err != nil {
		return fmt.Errorf("read old content for %q: %w", primaryPath(*record), err)
	}
	newContents, err := readNativeRecordSide(ctx, session, record.newOID)
	if err != nil {
		return fmt.Errorf("read new content for %q: %w", primaryPath(*record), err)
	}
	binary, err := classifyNativeBinary(ctx, session, primaryPath(*record), oldContents, newContents)
	if err != nil {
		return err
	}
	if binary {
		record.binary = true
		return nil
	}
	if bytes.Count(oldContents, []byte{'\n'})+bytes.Count(newContents, []byte{'\n'})+2 > 65_536 {
		return populateNativeDiskDiff(ctx, session, record, oldSize, newSize)
	}
	oldLines, err := tokenizeNativeLines(ctx, oldContents)
	if err != nil {
		return err
	}
	newLines, err := tokenizeNativeLines(ctx, newContents)
	if err != nil {
		return err
	}
	edits, err := histogramEdits(ctx, oldContents, newContents, oldLines, newLines)
	if err != nil {
		return err
	}
	var additions, deletions uint64
	for _, edit := range edits {
		switch edit.kind {
		case nativeEditAdd:
			additions += uint64(edit.count) //nolint:gosec // edit counts are validated non-negative line counts.
		case nativeEditDelete:
			deletions += uint64(edit.count) //nolint:gosec // edit counts are validated non-negative line counts.
		case nativeEditEqual:
		}
	}
	if additions > uint64(review.MaxAdditions) || deletions > uint64(review.MaxAdditions) {
		return &LimitError{Resource: LimitDiffLineTokens, Limit: uint64(review.MaxAdditions)}
	}
	record.additions = uint32(additions)
	record.deletions = uint32(deletions)
	return nil
}

func pinNativeRecordSides(
	ctx context.Context,
	session *repositorySession,
	record *diffRecord,
) error {
	for _, side := range []struct {
		oid  *review.ObjectID
		mode review.FileMode
	}{{record.oldOID, record.oldMode}, {record.newOID, record.newMode}} {
		if side.oid == nil || objectKind(side.mode) != review.ObjectBlob {
			continue
		}
		if err := session.pinObjectContent(ctx, plumbing.NewHash(side.oid.String())); err != nil {
			return err
		}
	}
	return nil
}

func nativeRecordContentSizes(
	session *repositorySession,
	record *diffRecord,
) (uint64, uint64, error) {
	size := func(oid *review.ObjectID) (uint64, error) {
		if oid == nil {
			return 0, nil
		}
		content, ok := session.contents[plumbing.NewHash(oid.String())]
		if !ok || content.size < 0 {
			return 0, errors.New("native diff content was not pinned")
		}
		return uint64(content.size), nil
	}
	oldSize, err := size(record.oldOID)
	if err != nil {
		return 0, 0, err
	}
	newSize, err := size(record.newOID)
	return oldSize, newSize, err
}

func populateNativeDiskDiff(
	ctx context.Context,
	session *repositorySession,
	record *diffRecord,
	oldSize, newSize uint64,
) (returnErr error) {
	oldContent, err := openPinnedDiskContent(ctx, session, record.oldOID)
	if err != nil {
		return err
	}
	newContent, err := openPinnedDiskContent(ctx, session, record.newOID)
	if err != nil {
		if oldContent != nil {
			return errors.Join(err, oldContent.Close())
		}
		return err
	}
	decided, binary, err := nativeBinaryAttribute(ctx, session, primaryPath(*record))
	if err != nil {
		return errors.Join(err, closeDiskContents(oldContent, newContent))
	}
	if !decided {
		oldBinary, oldErr := diskContentBinary(oldContent)
		newBinary, newErr := diskContentBinary(newContent)
		if binaryErr := errors.Join(oldErr, newErr); binaryErr != nil {
			return errors.Join(binaryErr, closeDiskContents(oldContent, newContent))
		}
		binary = oldBinary || newBinary
	}
	if binary {
		record.binary = true
		return closeDiskContents(oldContent, newContent)
	}
	workspace, err := session.ensureWorkspace()
	if err != nil {
		return errors.Join(err, closeDiskContents(oldContent, newContent))
	}
	analysis, err := analyzeDiskDiff(
		ctx, workspace, oldContent, newContent, oldSize, newSize,
	)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, analysis.Close()) }()
	record.additions = uint32(analysis.additions) //nolint:gosec // The edit writer enforces the review count limit.
	record.deletions = uint32(analysis.deletions) //nolint:gosec // The edit writer enforces the review count limit.
	return nil
}

func closeDiskContents(contents ...*os.File) error {
	var result error
	for _, file := range contents {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	return result
}

func classifyNativeBinary(
	ctx context.Context,
	session *repositorySession,
	path string,
	oldContents, newContents []byte,
) (bool, error) {
	decided, binary, err := nativeBinaryAttribute(ctx, session, path)
	if err != nil || decided {
		return binary, err
	}
	return nativeBinary(oldContents) || nativeBinary(newContents), nil
}

func nativeBinaryAttribute(
	ctx context.Context,
	session *repositorySession,
	path string,
) (decided, binary bool, returnErr error) {
	attributes, err := session.attributesForPath(ctx, path)
	if err != nil {
		return false, false, err
	}
	if attribute, ok := attributes["diff"]; ok {
		switch {
		case attribute.IsUnset():
			return true, true, nil
		case attribute.IsSet():
			return true, false, nil
		case attribute.IsValueSet():
			if binary, configured := session.config.value("diff", attribute.Value(), "binary"); configured {
				value, err := parseGitBoolean(binary)
				if err != nil {
					return false, false, fmt.Errorf("diff.%s.binary: %w", attribute.Value(), err)
				}
				return true, value, nil
			}
		}
	}
	return false, false, nil
}

func readNativeRecordSide(
	ctx context.Context,
	session *repositorySession,
	oid *review.ObjectID,
) ([]byte, error) {
	if oid == nil {
		return nil, nil
	}
	hash := plumbing.NewHash(oid.String())
	if hash.IsZero() {
		return nil, errors.New("content identity is zero")
	}
	return session.readContent(ctx, hash, maxWorktreeFileBytes)
}

func nativeBinary(contents []byte) bool {
	return bytes.IndexByte(contents[:min(len(contents), 8_000)], 0) >= 0
}

func tokenizeNativeLines(ctx context.Context, contents []byte) ([]nativeLine, error) {
	if len(contents) == 0 {
		return nil, nil
	}
	lines := make([]nativeLine, 0, bytes.Count(contents, []byte{'\n'})+1)
	start := 0
	for start < len(contents) {
		if len(lines)%4_096 == 0 {
			if err := context.Cause(ctx); err != nil {
				return nil, err
			}
		}
		relativeEnd := bytes.IndexByte(contents[start:], '\n')
		end := len(contents)
		terminated := false
		if relativeEnd >= 0 {
			end = start + relativeEnd
			terminated = true
		}
		hasher := fnv.New64a()
		_, _ = hasher.Write(contents[start:end])
		if terminated {
			_, _ = hasher.Write([]byte{'\n'})
		}
		lines = append(lines, nativeLine{
			start: start, end: end, hash: hasher.Sum64(), terminated: terminated,
		})
		if !terminated {
			break
		}
		start = end + 1
	}
	return lines, nil
}

func histogramEdits(
	ctx context.Context,
	oldContents, newContents []byte,
	oldLines, newLines []nativeLine,
) ([]nativeEdit, error) {
	tasks := []histogramTask{{oldEnd: len(oldLines), newEnd: len(newLines)}}
	edits := make([]nativeEdit, 0, 32)
	var probes uint64
	for len(tasks) != 0 {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		last := len(tasks) - 1
		task := tasks[last]
		tasks = tasks[:last]
		if task.fixed {
			appendNativeEdit(&edits, task.edit)
			continue
		}
		oldStart, newStart := task.oldStart, task.newStart
		for oldStart < task.oldEnd && newStart < task.newEnd &&
			nativeLinesEqual(oldContents, newContents, oldLines[oldStart], newLines[newStart]) {
			oldStart++
			newStart++
			probes++
		}
		prefix := oldStart - task.oldStart
		if prefix > 0 {
			appendNativeEdit(&edits, nativeEdit{
				kind: nativeEditEqual, oldStart: task.oldStart, newStart: task.newStart, count: prefix,
			})
		}
		oldEnd, newEnd := task.oldEnd, task.newEnd
		for oldEnd > oldStart && newEnd > newStart &&
			nativeLinesEqual(oldContents, newContents, oldLines[oldEnd-1], newLines[newEnd-1]) {
			oldEnd--
			newEnd--
			probes++
		}
		suffix := task.oldEnd - oldEnd
		if oldStart == oldEnd || newStart == newEnd {
			if newStart < newEnd {
				appendNativeEdit(&edits, nativeEdit{
					kind: nativeEditAdd, oldStart: oldStart, newStart: newStart, count: newEnd - newStart,
				})
			}
			if oldStart < oldEnd {
				appendNativeEdit(&edits, nativeEdit{
					kind: nativeEditDelete, oldStart: oldStart, newStart: newStart, count: oldEnd - oldStart,
				})
			}
			if suffix > 0 {
				appendNativeEdit(&edits, nativeEdit{
					kind: nativeEditEqual, oldStart: oldEnd, newStart: newEnd, count: suffix,
				})
			}
			continue
		}
		oldAnchor, newAnchor, run, occurrence, anchorProbes := nativeHistogramAnchor(
			oldContents, newContents, oldLines, newLines, oldStart, oldEnd, newStart, newEnd,
		)
		probes += anchorProbes
		if probes > 512_000_000 {
			return nil, &LimitError{Resource: LimitDiffHistogramProbes, Limit: 512_000_000}
		}
		if occurrence == 0 {
			appendNativeEdit(&edits, nativeEdit{
				kind: nativeEditDelete, oldStart: oldStart, newStart: newStart, count: oldEnd - oldStart,
			})
			appendNativeEdit(&edits, nativeEdit{
				kind: nativeEditAdd, oldStart: oldEnd, newStart: newStart, count: newEnd - newStart,
			})
			if suffix > 0 {
				appendNativeEdit(&edits, nativeEdit{
					kind: nativeEditEqual, oldStart: oldEnd, newStart: newEnd, count: suffix,
				})
			}
			continue
		}
		if suffix > 0 {
			tasks = append(tasks, histogramTask{fixed: true, edit: nativeEdit{
				kind: nativeEditEqual, oldStart: oldEnd, newStart: newEnd, count: suffix,
			}})
		}
		tasks = append(tasks,
			histogramTask{oldStart: oldAnchor + run, oldEnd: oldEnd, newStart: newAnchor + run, newEnd: newEnd},
			histogramTask{fixed: true, edit: nativeEdit{
				kind: nativeEditEqual, oldStart: oldAnchor, newStart: newAnchor, count: run,
			}},
			histogramTask{oldStart: oldStart, oldEnd: oldAnchor, newStart: newStart, newEnd: newAnchor},
		)
	}
	return edits, nil
}

func nativeHistogramAnchor(
	oldContents, newContents []byte,
	oldLines, newLines []nativeLine,
	oldStart, oldEnd, newStart, newEnd int,
) (bestOld, bestNew, bestRun, bestOccurrence int, probes uint64) {
	occurrences := make(map[uint64][]int, min(oldEnd-oldStart, 4_096))
	for index := oldStart; index < oldEnd; index++ {
		positions := occurrences[oldLines[index].hash]
		if len(positions) <= 64 {
			occurrences[oldLines[index].hash] = append(positions, index)
		}
	}
	for newIndex := newStart; newIndex < newEnd; newIndex++ {
		positions := occurrences[newLines[newIndex].hash]
		if len(positions) == 0 || len(positions) > 64 {
			continue
		}
		matches := make([]int, 0, len(positions))
		for _, oldIndex := range positions {
			probes++
			if nativeLinesEqual(oldContents, newContents, oldLines[oldIndex], newLines[newIndex]) {
				matches = append(matches, oldIndex)
			}
		}
		occurrence := len(matches)
		if occurrence == 0 || occurrence > 64 {
			continue
		}
		for _, matchedOld := range matches {
			anchorOld, anchorNew := matchedOld, newIndex
			for anchorOld > oldStart && anchorNew > newStart &&
				nativeLinesEqual(
					oldContents, newContents, oldLines[anchorOld-1], newLines[anchorNew-1],
				) {
				anchorOld--
				anchorNew--
				probes++
			}
			run := 1 + matchedOld - anchorOld
			for anchorOld+run < oldEnd && anchorNew+run < newEnd &&
				nativeLinesEqual(
					oldContents, newContents, oldLines[anchorOld+run], newLines[anchorNew+run],
				) {
				run++
				probes++
			}
			if bestOccurrence == 0 || occurrence < bestOccurrence ||
				occurrence == bestOccurrence && (run > bestRun ||
					run == bestRun && (anchorOld < bestOld ||
						anchorOld == bestOld && anchorNew < bestNew)) {
				bestOld, bestNew, bestRun, bestOccurrence =
					anchorOld, anchorNew, run, occurrence
			}
		}
	}
	return bestOld, bestNew, bestRun, bestOccurrence, probes
}

func nativeLinesEqual(
	oldContents, newContents []byte,
	oldLine, newLine nativeLine,
) bool {
	return oldLine.hash == newLine.hash && oldLine.terminated == newLine.terminated &&
		bytes.Equal(oldContents[oldLine.start:oldLine.end], newContents[newLine.start:newLine.end])
}

func appendNativeEdit(edits *[]nativeEdit, edit nativeEdit) {
	if edit.count == 0 {
		return
	}
	if len(*edits) != 0 {
		last := &(*edits)[len(*edits)-1]
		contiguous := last.kind == edit.kind
		switch edit.kind {
		case nativeEditEqual:
			contiguous = contiguous && last.oldStart+last.count == edit.oldStart &&
				last.newStart+last.count == edit.newStart
		case nativeEditDelete:
			contiguous = contiguous && last.oldStart+last.count == edit.oldStart &&
				last.newStart == edit.newStart
		case nativeEditAdd:
			contiguous = contiguous && last.oldStart == edit.oldStart &&
				last.newStart+last.count == edit.newStart
		}
		if contiguous {
			last.count += edit.count
			return
		}
	}
	*edits = append(*edits, edit)
}
