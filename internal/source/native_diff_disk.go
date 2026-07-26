package source

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/jacoelho/rvw/internal/change"
)

const (
	diskLineRecordBytes      = 56
	diskOccurrenceBytes      = 40
	diskHistogramTaskBytes   = 64
	diskEditRecordBytes      = 32
	diskSortChunkBytes       = 8 << 20
	diskSortMergeFanIn       = 32
	diskSortMergeBufferBytes = 4 << 10
	maxDiskDiffLineTokens    = 64_000_000
)

type diskLineRecord struct {
	hash       [sha256.Size]byte
	offset     uint64
	length     uint64
	terminated bool
}

type diskDiffSide struct {
	content *os.File
	lines   *os.File
	path    string
	count   uint64
	size    uint64
}

type diskDiffKernel struct {
	ctx       context.Context
	old       *diskDiffSide
	new       *diskDiffSide
	workspace string
	temps     []string
	oldBuffer []byte
	newBuffer []byte
	probes    uint64
	tokens    uint64
}

type diskOccurrence struct {
	hash  [sha256.Size]byte
	index uint64
}

type diskOccurrenceRun struct {
	file    *os.File
	reader  *bufio.Reader
	path    string
	current diskOccurrence
	valid   bool
}

type diskOccurrenceHeap []*diskOccurrenceRun

func (values *diskOccurrenceHeap) Len() int { return len(*values) }

func (values *diskOccurrenceHeap) Less(left, right int) bool {
	comparison := bytes.Compare((*values)[left].current.hash[:], (*values)[right].current.hash[:])
	return comparison < 0 || comparison == 0 && (*values)[left].current.index < (*values)[right].current.index
}

func (values *diskOccurrenceHeap) Swap(left, right int) {
	(*values)[left], (*values)[right] = (*values)[right], (*values)[left]
}

func (values *diskOccurrenceHeap) Push(value any) {
	*values = append(*values, value.(*diskOccurrenceRun)) //nolint:errcheck,forcetypeassert // container/heap supplies only this private run type.
}

func (values *diskOccurrenceHeap) Pop() any {
	old := *values
	last := len(old) - 1
	value := old[last]
	old[last] = nil
	*values = old[:last]
	return value
}

type diskHistogramTask struct {
	oldStart uint64
	oldEnd   uint64
	newStart uint64
	newEnd   uint64
	edit     diskEdit
	fixed    bool
}

type diskEdit struct {
	kind     nativeEditKind
	oldStart uint64
	newStart uint64
	count    uint64
}

type diskDiffAnalysis struct {
	kernel    *diskDiffKernel
	edits     *os.File
	editPath  string
	editCount uint64
	additions uint64
	deletions uint64
}

func newDiskDiffKernel(
	ctx context.Context,
	workspace string,
	oldContent, newContent *os.File,
	oldSize, newSize uint64,
) (*diskDiffKernel, error) {
	kernel := &diskDiffKernel{
		ctx: ctx, workspace: workspace,
		oldBuffer: make([]byte, 16<<10),
		newBuffer: make([]byte, 16<<10),
		old:       &diskDiffSide{content: oldContent, size: oldSize},
		new:       &diskDiffSide{content: newContent, size: newSize},
	}
	if err := kernel.indexSide(kernel.old, "old-lines-"); err != nil {
		return nil, errors.Join(err, kernel.Close())
	}
	if err := kernel.indexSide(kernel.new, "new-lines-"); err != nil {
		return nil, errors.Join(err, kernel.Close())
	}
	return kernel, nil
}

func (kernel *diskDiffKernel) newTemp(pattern string) (*os.File, string, error) {
	file, err := os.CreateTemp(kernel.workspace, pattern)
	if err != nil {
		return nil, "", err
	}
	path := file.Name()
	kernel.temps = append(kernel.temps, path)
	return file, path, nil
}

func (kernel *diskDiffKernel) indexSide(
	side *diskDiffSide,
	pattern string,
) error {
	lines, path, err := kernel.newTemp(pattern)
	if err != nil {
		return err
	}
	side.lines, side.path = lines, path
	if side.content == nil || side.size == 0 {
		return nil
	}
	if _, err := side.content.Seek(0, io.SeekStart); err != nil {
		return err
	}
	writer := bufio.NewWriterSize(lines, 64<<10)
	buffer := make([]byte, 64<<10)
	hasher := sha256.New()
	var lineOffset, position uint64
	for {
		count, readErr := side.content.Read(buffer)
		chunk := buffer[:count]
		for len(chunk) != 0 {
			newline := bytes.IndexByte(chunk, '\n')
			if newline < 0 {
				_, _ = hasher.Write(chunk)
				position += uint64(len(chunk))
				chunk = nil
				continue
			}
			segment := chunk[:newline+1]
			_, _ = hasher.Write(segment)
			position += uint64(len(segment))
			var digest [sha256.Size]byte
			copy(digest[:], hasher.Sum(nil))
			record := diskLineRecord{
				hash: digest, offset: lineOffset,
				length: position - lineOffset - 1, terminated: true,
			}
			if err := writeDiskLineRecord(writer, record); err != nil {
				return err
			}
			side.count++
			kernel.tokens++
			if kernel.tokens > maxDiskDiffLineTokens {
				return &LimitError{Resource: LimitDiffLineTokens, Limit: maxDiskDiffLineTokens}
			}
			if side.count%diffCancellationStride == 0 {
				if err := context.Cause(kernel.ctx); err != nil {
					return err
				}
			}
			hasher.Reset()
			lineOffset = position
			chunk = chunk[newline+1:]
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return readErr
			}
			break
		}
		if count == 0 {
			return io.ErrNoProgress
		}
	}
	if position != lineOffset {
		var digest [sha256.Size]byte
		copy(digest[:], hasher.Sum(nil))
		if err := writeDiskLineRecord(writer, diskLineRecord{
			hash: digest, offset: lineOffset, length: position - lineOffset,
		}); err != nil {
			return err
		}
		side.count++
		kernel.tokens++
		if kernel.tokens > maxDiskDiffLineTokens {
			return &LimitError{Resource: LimitDiffLineTokens, Limit: maxDiskDiffLineTokens}
		}
	}
	if position != side.size {
		return fmt.Errorf("content size changed while indexing lines: read %d, want %d", position, side.size)
	}
	return writer.Flush()
}

func writeDiskLineRecord(writer io.Writer, record diskLineRecord) error {
	var encoded [diskLineRecordBytes]byte
	binary.BigEndian.PutUint64(encoded[0:8], record.offset)
	binary.BigEndian.PutUint64(encoded[8:16], record.length)
	copy(encoded[16:48], record.hash[:])
	if record.terminated {
		encoded[48] = 1
	}
	_, err := writer.Write(encoded[:])
	return err
}

func (side *diskDiffSide) line(index uint64) (diskLineRecord, error) {
	if side == nil || index >= side.count {
		return diskLineRecord{}, errors.New("disk diff line index is out of range")
	}
	var encoded [diskLineRecordBytes]byte
	if _, err := side.lines.ReadAt(encoded[:], int64(index*diskLineRecordBytes)); err != nil { //nolint:gosec // Token limits keep the offset below int64.
		return diskLineRecord{}, err
	}
	var hash [sha256.Size]byte
	copy(hash[:], encoded[16:48])
	return diskLineRecord{
		offset: binary.BigEndian.Uint64(encoded[0:8]),
		length: binary.BigEndian.Uint64(encoded[8:16]),
		hash:   hash, terminated: encoded[48] == 1,
	}, nil
}

func (kernel *diskDiffKernel) linesEqual(
	oldIndex, newIndex uint64,
) (bool, error) {
	oldLine, err := kernel.old.line(oldIndex)
	if err != nil {
		return false, err
	}
	newLine, err := kernel.new.line(newIndex)
	if err != nil {
		return false, err
	}
	if oldLine.hash != newLine.hash || oldLine.length != newLine.length ||
		oldLine.terminated != newLine.terminated {
		return false, nil
	}
	remaining := oldLine.length
	oldOffset, newOffset := oldLine.offset, newLine.offset
	for remaining != 0 {
		chunk := min(remaining, uint64(len(kernel.oldBuffer)))
		oldBytes := kernel.oldBuffer[:chunk]
		newBytes := kernel.newBuffer[:chunk]
		if _, err := kernel.old.content.ReadAt(oldBytes, int64(oldOffset)); err != nil { //nolint:gosec // File size bounds offsets below int64.
			return false, err
		}
		if _, err := kernel.new.content.ReadAt(newBytes, int64(newOffset)); err != nil { //nolint:gosec // File size bounds offsets below int64.
			return false, err
		}
		if !bytes.Equal(oldBytes, newBytes) {
			return false, nil
		}
		remaining -= chunk
		oldOffset += chunk
		newOffset += chunk
	}
	return true, nil
}

func (kernel *diskDiffKernel) chargeProbe() error {
	kernel.probes++
	if kernel.probes > maxDiffHistogramProbes {
		return &LimitError{Resource: LimitDiffHistogramProbes, Limit: maxDiffHistogramProbes}
	}
	if kernel.probes%diffCancellationStride == 0 {
		return context.Cause(kernel.ctx)
	}
	return nil
}

func (kernel *diskDiffKernel) Close() error {
	if kernel == nil {
		return nil
	}
	var result error
	for _, side := range []*diskDiffSide{kernel.old, kernel.new} {
		if side == nil {
			continue
		}
		if side.lines != nil {
			result = errors.Join(result, side.lines.Close())
		}
		if side.content != nil {
			result = errors.Join(result, side.content.Close())
		}
	}
	for _, path := range kernel.temps {
		if path != "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
	}
	kernel.old, kernel.new = nil, nil
	kernel.temps = nil
	return result
}

func (analysis *diskDiffAnalysis) Close() error {
	if analysis == nil {
		return nil
	}
	if analysis.edits != nil {
		if err := analysis.edits.Close(); err != nil {
			return errors.Join(err, analysis.kernel.Close())
		}
	}
	err := analysis.kernel.Close()
	*analysis = diskDiffAnalysis{}
	return err
}

func openPinnedDiskContent(
	ctx context.Context,
	session *repositorySession,
	oid *change.ObjectID,
) (*os.File, error) {
	if oid == nil {
		return nil, nil //nolint:nilnil // An absent diff side intentionally has no content descriptor.
	}
	hash := reviewHash(*oid)
	if err := session.pinObjectContent(ctx, hash); err != nil {
		return nil, err
	}
	content, ok := session.contents[hash]
	if !ok || content.size < 0 {
		return nil, errors.New("pinned diff content is unavailable")
	}
	return os.Open(content.path)
}

func diskContentBinary(file *os.File) (bool, error) {
	if file == nil {
		return false, nil
	}
	var probe [8_000]byte
	count, err := file.ReadAt(probe[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return bytes.IndexByte(probe[:count], 0) >= 0, nil
}

func removeKernelTemp(kernel *diskDiffKernel, path string) error {
	for index := range kernel.temps {
		if kernel.temps[index] == path {
			kernel.temps[index] = ""
			break
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func writeDiskOccurrence(writer io.Writer, occurrence diskOccurrence) error {
	var encoded [diskOccurrenceBytes]byte
	copy(encoded[:sha256.Size], occurrence.hash[:])
	binary.BigEndian.PutUint64(encoded[sha256.Size:], occurrence.index)
	_, err := writer.Write(encoded[:])
	return err
}

func readDiskOccurrence(reader io.Reader) (diskOccurrence, error) {
	var encoded [diskOccurrenceBytes]byte
	if _, err := io.ReadFull(reader, encoded[:]); err != nil {
		return diskOccurrence{}, err
	}
	var hash [sha256.Size]byte
	copy(hash[:], encoded[:sha256.Size])
	return diskOccurrence{
		hash:  hash,
		index: binary.BigEndian.Uint64(encoded[sha256.Size:]),
	}, nil
}

func readDiskOccurrenceAt(file *os.File, index uint64) (diskOccurrence, error) {
	var encoded [diskOccurrenceBytes]byte
	if _, err := file.ReadAt(encoded[:], int64(index*diskOccurrenceBytes)); err != nil { //nolint:gosec // Line-token limits bound the offset.
		return diskOccurrence{}, err
	}
	var hash [sha256.Size]byte
	copy(hash[:], encoded[:sha256.Size])
	return diskOccurrence{
		hash:  hash,
		index: binary.BigEndian.Uint64(encoded[sha256.Size:]),
	}, nil
}

func (kernel *diskDiffKernel) occurrenceRuns(
	oldStart, oldEnd uint64,
) ([]string, error) {
	const chunkCount = diskSortChunkBytes / diskOccurrenceBytes
	chunk := make([]diskOccurrence, 0, chunkCount)
	runs := make([]string, 0, int((oldEnd-oldStart+chunkCount-1)/chunkCount)) //nolint:gosec // Token admission bounds the run count below int.
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		sort.Slice(chunk, func(left, right int) bool {
			comparison := bytes.Compare(chunk[left].hash[:], chunk[right].hash[:])
			return comparison < 0 || comparison == 0 && chunk[left].index < chunk[right].index
		})
		file, path, err := kernel.newTemp("histogram-run-")
		if err != nil {
			return err
		}
		writer := bufio.NewWriterSize(file, diskSortMergeBufferBytes)
		for _, occurrence := range chunk {
			if err := writeDiskOccurrence(writer, occurrence); err != nil {
				return errors.Join(err, file.Close())
			}
		}
		if err := errors.Join(writer.Flush(), file.Close()); err != nil {
			return err
		}
		runs = append(runs, path)
		chunk = chunk[:0]
		return nil
	}
	for index := oldStart; index < oldEnd; index++ {
		if index%diffCancellationStride == 0 {
			if err := context.Cause(kernel.ctx); err != nil {
				return nil, err
			}
		}
		line, err := kernel.old.line(index)
		if err != nil {
			return nil, err
		}
		chunk = append(chunk, diskOccurrence{hash: line.hash, index: index})
		if len(chunk) == cap(chunk) {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (kernel *diskDiffKernel) mergeOccurrenceRuns(paths []string) (string, error) {
	for len(paths) > 1 {
		next := make([]string, 0, (len(paths)+diskSortMergeFanIn-1)/diskSortMergeFanIn)
		for start := 0; start < len(paths); start += diskSortMergeFanIn {
			end := min(start+diskSortMergeFanIn, len(paths))
			if end-start == 1 {
				next = append(next, paths[start])
				continue
			}
			merged, err := kernel.mergeOccurrenceRunGroup(paths[start:end])
			if err != nil {
				return "", err
			}
			next = append(next, merged)
		}
		paths = next
	}
	if len(paths) == 0 {
		file, path, err := kernel.newTemp("histogram-empty-")
		if err != nil {
			return "", err
		}
		return path, file.Close()
	}
	return paths[0], nil
}

func (kernel *diskDiffKernel) mergeOccurrenceRunGroup(paths []string) (_ string, returnErr error) {
	runs := make([]*diskOccurrenceRun, 0, len(paths))
	defer func() {
		for _, run := range runs {
			returnErr = errors.Join(returnErr, run.file.Close())
		}
	}()
	queue := make(diskOccurrenceHeap, 0, len(paths))
	for _, path := range paths {
		file, err := os.Open(path) //nolint:gosec // path is an owner-created private sort run.
		if err != nil {
			return "", err
		}
		run := &diskOccurrenceRun{
			file: file, reader: bufio.NewReaderSize(file, diskSortMergeBufferBytes), path: path,
		}
		run.current, err = readDiskOccurrence(run.reader)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if err == nil {
			run.valid = true
			queue = append(queue, run)
		}
		runs = append(runs, run)
	}
	heap.Init(&queue)
	output, outputPath, err := kernel.newTemp("histogram-merge-")
	if err != nil {
		return "", err
	}
	writer := bufio.NewWriterSize(output, diskSortMergeBufferBytes)
	var merged uint64
	for queue.Len() != 0 {
		run := heap.Pop(&queue).(*diskOccurrenceRun) //nolint:errcheck,forcetypeassert,revive // Private heap contains only runs.
		if writeErr := writeDiskOccurrence(writer, run.current); writeErr != nil {
			return "", errors.Join(writeErr, output.Close())
		}
		merged++
		if merged%diffCancellationStride == 0 {
			if cancelErr := context.Cause(kernel.ctx); cancelErr != nil {
				return "", errors.Join(cancelErr, output.Close())
			}
		}
		run.current, err = readDiskOccurrence(run.reader)
		if err == nil {
			heap.Push(&queue, run)
		} else if !errors.Is(err, io.EOF) {
			return "", errors.Join(err, output.Close())
		}
	}
	if err := errors.Join(writer.Flush(), output.Close()); err != nil {
		return "", err
	}
	for _, path := range paths {
		if err := removeKernelTemp(kernel, path); err != nil {
			return "", err
		}
	}
	return outputPath, nil
}

func (kernel *diskDiffKernel) firstOccurrence(
	file *os.File,
	count uint64,
	hash [sha256.Size]byte,
) (uint64, error) {
	left, right := uint64(0), count
	for left < right {
		middle := left + (right-left)/2
		value, err := readDiskOccurrenceAt(file, middle)
		if err != nil {
			return 0, err
		}
		if bytes.Compare(value.hash[:], hash[:]) < 0 {
			left = middle + 1
		} else {
			right = middle
		}
	}
	return left, nil
}

//nolint:gocognit,cyclop // Exact collision checks, occurrence admission, run extension, and tie-breaking are one selection rule.
func (kernel *diskDiffKernel) histogramAnchor(
	oldStart, oldEnd, newStart, newEnd uint64,
) (bestOld, bestNew, bestRun, bestOccurrence uint64, returnErr error) {
	runs, err := kernel.occurrenceRuns(oldStart, oldEnd)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	path, err := kernel.mergeOccurrenceRuns(runs)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	file, err := os.Open(path) //nolint:gosec // path is an owner-created private sorted occurrence file.
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close(), removeKernelTemp(kernel, path))
	}()
	count := oldEnd - oldStart
	for newIndex := newStart; newIndex < newEnd; newIndex++ {
		if newIndex%diffCancellationStride == 0 {
			if err := context.Cause(kernel.ctx); err != nil {
				return 0, 0, 0, 0, err
			}
		}
		newLine, err := kernel.new.line(newIndex)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		first, err := kernel.firstOccurrence(file, count, newLine.hash)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		var matches [65]uint64
		matched := 0
		for position := first; position < count; position++ {
			occurrence, err := readDiskOccurrenceAt(file, position)
			if err != nil {
				return 0, 0, 0, 0, err
			}
			if occurrence.hash != newLine.hash {
				break
			}
			if probeErr := kernel.chargeProbe(); probeErr != nil {
				return 0, 0, 0, 0, probeErr
			}
			equal, err := kernel.linesEqual(occurrence.index, newIndex)
			if err != nil {
				return 0, 0, 0, 0, err
			}
			if !equal {
				continue
			}
			matches[matched] = occurrence.index
			matched++
			if matched == len(matches) {
				break
			}
		}
		if matched == 0 || matched > 64 {
			continue
		}
		occurrenceCount := uint64(matched)
		for _, matchedOld := range matches[:matched] {
			anchorOld, anchorNew := matchedOld, newIndex
			for anchorOld > oldStart && anchorNew > newStart {
				if err := kernel.chargeProbe(); err != nil {
					return 0, 0, 0, 0, err
				}
				equal, err := kernel.linesEqual(anchorOld-1, anchorNew-1)
				if err != nil {
					return 0, 0, 0, 0, err
				}
				if !equal {
					break
				}
				anchorOld--
				anchorNew--
			}
			run := uint64(1) + matchedOld - anchorOld
			for anchorOld+run < oldEnd && anchorNew+run < newEnd {
				if err := kernel.chargeProbe(); err != nil {
					return 0, 0, 0, 0, err
				}
				equal, err := kernel.linesEqual(anchorOld+run, anchorNew+run)
				if err != nil {
					return 0, 0, 0, 0, err
				}
				if !equal {
					break
				}
				run++
			}
			if bestOccurrence == 0 || occurrenceCount < bestOccurrence ||
				occurrenceCount == bestOccurrence && (run > bestRun ||
					run == bestRun && (anchorOld < bestOld ||
						anchorOld == bestOld && anchorNew < bestNew)) {
				bestOld, bestNew, bestRun, bestOccurrence =
					anchorOld, anchorNew, run, occurrenceCount
			}
		}
	}
	return bestOld, bestNew, bestRun, bestOccurrence, nil
}

type diskTaskStack struct {
	file  *os.File
	count uint64
}

func (stack *diskTaskStack) push(task diskHistogramTask) error {
	var encoded [diskHistogramTaskBytes]byte
	binary.BigEndian.PutUint64(encoded[0:8], task.oldStart)
	binary.BigEndian.PutUint64(encoded[8:16], task.oldEnd)
	binary.BigEndian.PutUint64(encoded[16:24], task.newStart)
	binary.BigEndian.PutUint64(encoded[24:32], task.newEnd)
	binary.BigEndian.PutUint64(encoded[32:40], task.edit.oldStart)
	binary.BigEndian.PutUint64(encoded[40:48], task.edit.newStart)
	binary.BigEndian.PutUint64(encoded[48:56], task.edit.count)
	encoded[56] = byte(task.edit.kind)
	if task.fixed {
		encoded[57] = 1
	}
	if _, err := stack.file.WriteAt(encoded[:], int64(stack.count*diskHistogramTaskBytes)); err != nil { //nolint:gosec // Token bounds keep the offset below int64.
		return err
	}
	stack.count++
	return nil
}

func (stack *diskTaskStack) pop() (diskHistogramTask, bool, error) {
	if stack.count == 0 {
		return diskHistogramTask{}, false, nil
	}
	stack.count--
	var encoded [diskHistogramTaskBytes]byte
	if _, err := stack.file.ReadAt(encoded[:], int64(stack.count*diskHistogramTaskBytes)); err != nil { //nolint:gosec // Token bounds keep the offset below int64.
		return diskHistogramTask{}, false, err
	}
	return diskHistogramTask{
		oldStart: binary.BigEndian.Uint64(encoded[0:8]),
		oldEnd:   binary.BigEndian.Uint64(encoded[8:16]),
		newStart: binary.BigEndian.Uint64(encoded[16:24]),
		newEnd:   binary.BigEndian.Uint64(encoded[24:32]),
		edit: diskEdit{
			oldStart: binary.BigEndian.Uint64(encoded[32:40]),
			newStart: binary.BigEndian.Uint64(encoded[40:48]),
			count:    binary.BigEndian.Uint64(encoded[48:56]),
			kind:     nativeEditKind(encoded[56]),
		},
		fixed: encoded[57] == 1,
	}, true, nil
}

type diskEditWriter struct {
	file      *os.File
	last      diskEdit
	count     uint64
	additions uint64
	deletions uint64
	hasLast   bool
}

func (writer *diskEditWriter) append(edit diskEdit) error {
	if edit.count == 0 {
		return nil
	}
	switch edit.kind {
	case nativeEditAdd:
		if edit.count > uint64(change.MaxAdditions)-writer.additions {
			return &LimitError{Resource: LimitDiffLineTokens, Limit: uint64(change.MaxAdditions)}
		}
		writer.additions += edit.count
	case nativeEditDelete:
		if edit.count > uint64(change.MaxDeletions)-writer.deletions {
			return &LimitError{Resource: LimitDiffLineTokens, Limit: uint64(change.MaxDeletions)}
		}
		writer.deletions += edit.count
	case nativeEditEqual:
	default:
		return errors.New("disk diff edit kind is invalid")
	}
	if writer.hasLast && diskEditsContiguous(writer.last, edit) {
		writer.last.count += edit.count
		return nil
	}
	if err := writer.flushLast(); err != nil {
		return err
	}
	writer.last = edit
	writer.hasLast = true
	return nil
}

func diskEditsContiguous(left, right diskEdit) bool {
	if left.kind != right.kind {
		return false
	}
	switch right.kind {
	case nativeEditEqual:
		return left.oldStart+left.count == right.oldStart &&
			left.newStart+left.count == right.newStart
	case nativeEditDelete:
		return left.oldStart+left.count == right.oldStart && left.newStart == right.newStart
	case nativeEditAdd:
		return left.oldStart == right.oldStart && left.newStart+left.count == right.newStart
	default:
		return false
	}
}

func (writer *diskEditWriter) flushLast() error {
	if !writer.hasLast {
		return nil
	}
	var encoded [diskEditRecordBytes]byte
	binary.BigEndian.PutUint64(encoded[0:8], writer.last.oldStart)
	binary.BigEndian.PutUint64(encoded[8:16], writer.last.newStart)
	binary.BigEndian.PutUint64(encoded[16:24], writer.last.count)
	encoded[24] = byte(writer.last.kind)
	if _, err := writer.file.WriteAt(encoded[:], int64(writer.count*diskEditRecordBytes)); err != nil { //nolint:gosec // Token bounds keep the offset below int64.
		return err
	}
	writer.count++
	writer.hasLast = false
	return nil
}

func readDiskEditAt(file *os.File, index uint64) (diskEdit, error) {
	var encoded [diskEditRecordBytes]byte
	if _, err := file.ReadAt(encoded[:], int64(index*diskEditRecordBytes)); err != nil { //nolint:gosec // Token bounds keep the offset below int64.
		return diskEdit{}, err
	}
	return diskEdit{
		oldStart: binary.BigEndian.Uint64(encoded[0:8]),
		newStart: binary.BigEndian.Uint64(encoded[8:16]),
		count:    binary.BigEndian.Uint64(encoded[16:24]),
		kind:     nativeEditKind(encoded[24]),
	}, nil
}

//nolint:gocognit,cyclop,funlen,maintidx // Disk task ownership and the selected recursive histogram order form one kernel.
func analyzeDiskDiff(
	ctx context.Context,
	workspace string,
	oldContent, newContent *os.File,
	oldSize, newSize uint64,
) (_ *diskDiffAnalysis, returnErr error) {
	kernel, err := newDiskDiffKernel(ctx, workspace, oldContent, newContent, oldSize, newSize)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			returnErr = errors.Join(returnErr, kernel.Close())
		}
	}()
	taskFile, _, err := kernel.newTemp("histogram-tasks-")
	if err != nil {
		return nil, err
	}
	defer taskFile.Close() //nolint:errcheck // Kernel cleanup reports removal; writes are synchronous.
	stack := &diskTaskStack{file: taskFile}
	if pushErr := stack.push(diskHistogramTask{
		oldEnd: kernel.old.count,
		newEnd: kernel.new.count,
	}); pushErr != nil {
		return nil, pushErr
	}
	editFile, editPath, err := kernel.newTemp("histogram-edits-")
	if err != nil {
		return nil, err
	}
	writer := &diskEditWriter{file: editFile}
	for {
		task, present, popErr := stack.pop()
		if popErr != nil {
			return nil, popErr
		}
		if !present {
			break
		}
		if cancelErr := context.Cause(ctx); cancelErr != nil {
			return nil, cancelErr
		}
		if task.fixed {
			if appendErr := writer.append(task.edit); appendErr != nil {
				return nil, appendErr
			}
			continue
		}
		oldStart, newStart := task.oldStart, task.newStart
		for oldStart < task.oldEnd && newStart < task.newEnd {
			if probeErr := kernel.chargeProbe(); probeErr != nil {
				return nil, probeErr
			}
			equal, err := kernel.linesEqual(oldStart, newStart)
			if err != nil {
				return nil, err
			}
			if !equal {
				break
			}
			oldStart++
			newStart++
		}
		prefix := oldStart - task.oldStart
		if prefix != 0 {
			if err := writer.append(diskEdit{
				kind: nativeEditEqual, oldStart: task.oldStart,
				newStart: task.newStart, count: prefix,
			}); err != nil {
				return nil, err
			}
		}
		oldEnd, newEnd := task.oldEnd, task.newEnd
		for oldEnd > oldStart && newEnd > newStart {
			if err := kernel.chargeProbe(); err != nil {
				return nil, err
			}
			equal, err := kernel.linesEqual(oldEnd-1, newEnd-1)
			if err != nil {
				return nil, err
			}
			if !equal {
				break
			}
			oldEnd--
			newEnd--
		}
		suffix := task.oldEnd - oldEnd
		if oldStart == oldEnd || newStart == newEnd {
			if newStart < newEnd {
				if err := writer.append(diskEdit{
					kind: nativeEditAdd, oldStart: oldStart,
					newStart: newStart, count: newEnd - newStart,
				}); err != nil {
					return nil, err
				}
			}
			if oldStart < oldEnd {
				if err := writer.append(diskEdit{
					kind: nativeEditDelete, oldStart: oldStart,
					newStart: newStart, count: oldEnd - oldStart,
				}); err != nil {
					return nil, err
				}
			}
			if suffix != 0 {
				if err := writer.append(diskEdit{
					kind: nativeEditEqual, oldStart: oldEnd,
					newStart: newEnd, count: suffix,
				}); err != nil {
					return nil, err
				}
			}
			continue
		}
		oldAnchor, newAnchor, run, occurrence, err := kernel.histogramAnchor(
			oldStart, oldEnd, newStart, newEnd,
		)
		if err != nil {
			return nil, err
		}
		if occurrence == 0 {
			if err := writer.append(diskEdit{
				kind: nativeEditDelete, oldStart: oldStart,
				newStart: newStart, count: oldEnd - oldStart,
			}); err != nil {
				return nil, err
			}
			if err := writer.append(diskEdit{
				kind: nativeEditAdd, oldStart: oldEnd,
				newStart: newStart, count: newEnd - newStart,
			}); err != nil {
				return nil, err
			}
			if suffix != 0 {
				if err := writer.append(diskEdit{
					kind: nativeEditEqual, oldStart: oldEnd,
					newStart: newEnd, count: suffix,
				}); err != nil {
					return nil, err
				}
			}
			continue
		}
		if suffix != 0 {
			if err := stack.push(diskHistogramTask{fixed: true, edit: diskEdit{
				kind: nativeEditEqual, oldStart: oldEnd,
				newStart: newEnd, count: suffix,
			}}); err != nil {
				return nil, err
			}
		}
		for _, next := range []diskHistogramTask{
			{oldStart: oldAnchor + run, oldEnd: oldEnd, newStart: newAnchor + run, newEnd: newEnd},
			{fixed: true, edit: diskEdit{
				kind: nativeEditEqual, oldStart: oldAnchor, newStart: newAnchor, count: run,
			}},
			{oldStart: oldStart, oldEnd: oldAnchor, newStart: newStart, newEnd: newAnchor},
		} {
			if err := stack.push(next); err != nil {
				return nil, err
			}
		}
	}
	if err := writer.flushLast(); err != nil {
		return nil, err
	}
	analysis := &diskDiffAnalysis{
		kernel: kernel, edits: editFile, editPath: editPath,
		editCount: writer.count, additions: writer.additions, deletions: writer.deletions,
	}
	keep = true
	return analysis, nil
}

var _ heap.Interface = (*diskOccurrenceHeap)(nil)
