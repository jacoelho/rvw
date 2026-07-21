package review

import (
	"math/bits"
	"slices"
)

const hunkMarkFileChunk = 256

type fileMarkSet struct {
	words []uint64
	count uint32
}

func (set fileMarkSet) has(id FileID) bool {
	word := int(id.Uint32() / 64)
	return word < len(set.words) && set.words[word]&(uint64(1)<<(id.Uint32()%64)) != 0
}

func (set fileMarkSet) toggle(id FileID) fileMarkSet {
	next := fileMarkSet{words: slices.Clone(set.words), count: set.count}
	word := int(id.Uint32() / 64)
	if word >= len(next.words) {
		next.words = append(next.words, make([]uint64, word-len(next.words)+1)...)
	}
	mask := uint64(1) << (id.Uint32() % 64)
	if next.words[word]&mask != 0 {
		next.words[word] &^= mask
		next.count--
	} else {
		next.words[word] |= mask
		next.count++
	}
	return next
}

func addFileMark(set *fileMarkSet, id FileID) {
	word := int(id.Uint32() / 64)
	if word >= len(set.words) {
		set.words = append(set.words, make([]uint64, word-len(set.words)+1)...)
	}
	mask := uint64(1) << (id.Uint32() % 64)
	if set.words[word]&mask == 0 {
		set.words[word] |= mask
		set.count++
	}
}

func (set fileMarkSet) each(yield func(FileID) bool) {
	for wordIndex, word := range set.words {
		for word != 0 {
			bit := bits.TrailingZeros64(word)
			if !yield(FileID(uint32FromInt(wordIndex*64 + bit))) {
				return
			}
			word &^= uint64(1) << bit
		}
	}
}

type hunkMarkFile struct {
	words []uint64
	count uint32
}

type hunkMarkChunk [hunkMarkFileChunk]hunkMarkFile

type hunkMarkSet struct {
	chunks []*hunkMarkChunk
	count  uint32
}

func (set hunkMarkSet) file(id FileID) hunkMarkFile {
	chunkIndex := int(id.Uint32()) / hunkMarkFileChunk
	if chunkIndex >= len(set.chunks) || set.chunks[chunkIndex] == nil {
		return hunkMarkFile{}
	}
	return set.chunks[chunkIndex][int(id.Uint32())%hunkMarkFileChunk]
}

func (set hunkMarkSet) has(id HunkID) bool {
	file := set.file(id.FileID())
	word := int(id.Index().Uint32() / 64)
	return word < len(file.words) &&
		file.words[word]&(uint64(1)<<(id.Index().Uint32()%64)) != 0
}

func (set hunkMarkSet) toggle(id HunkID) hunkMarkSet {
	next := hunkMarkSet{chunks: slices.Clone(set.chunks), count: set.count}
	chunkIndex := int(id.FileID().Uint32()) / hunkMarkFileChunk
	if chunkIndex >= len(next.chunks) {
		next.chunks = append(
			next.chunks,
			make([]*hunkMarkChunk, chunkIndex-len(next.chunks)+1)...,
		)
	}
	chunk := new(hunkMarkChunk)
	if next.chunks[chunkIndex] != nil {
		*chunk = *next.chunks[chunkIndex]
	}
	next.chunks[chunkIndex] = chunk
	fileIndex := int(id.FileID().Uint32()) % hunkMarkFileChunk
	file := chunk[fileIndex]
	file.words = slices.Clone(file.words)
	word := int(id.Index().Uint32() / 64)
	if word >= len(file.words) {
		file.words = append(file.words, make([]uint64, word-len(file.words)+1)...)
	}
	mask := uint64(1) << (id.Index().Uint32() % 64)
	if file.words[word]&mask != 0 {
		file.words[word] &^= mask
		file.count--
		next.count--
	} else {
		file.words[word] |= mask
		file.count++
		next.count++
	}
	chunk[fileIndex] = file
	return next
}

func addHunkMark(set *hunkMarkSet, id HunkID) {
	chunkIndex := int(id.FileID().Uint32()) / hunkMarkFileChunk
	if chunkIndex >= len(set.chunks) {
		set.chunks = append(
			set.chunks,
			make([]*hunkMarkChunk, chunkIndex-len(set.chunks)+1)...,
		)
	}
	if set.chunks[chunkIndex] == nil {
		set.chunks[chunkIndex] = new(hunkMarkChunk)
	}
	file := &set.chunks[chunkIndex][int(id.FileID().Uint32())%hunkMarkFileChunk]
	word := int(id.Index().Uint32() / 64)
	if word >= len(file.words) {
		file.words = append(file.words, make([]uint64, word-len(file.words)+1)...)
	}
	mask := uint64(1) << (id.Index().Uint32() % 64)
	if file.words[word]&mask == 0 {
		file.words[word] |= mask
		file.count++
		set.count++
	}
}

func (set hunkMarkSet) dropFile(id FileID) hunkMarkSet {
	current := set.file(id)
	if current.count == 0 {
		return set
	}
	next := hunkMarkSet{chunks: slices.Clone(set.chunks), count: set.count - current.count}
	chunkIndex := int(id.Uint32()) / hunkMarkFileChunk
	chunk := new(hunkMarkChunk)
	*chunk = *next.chunks[chunkIndex]
	chunk[int(id.Uint32())%hunkMarkFileChunk] = hunkMarkFile{}
	next.chunks[chunkIndex] = chunk
	return next
}

func (set hunkMarkSet) each(yield func(HunkID) bool) {
	for chunkIndex, chunk := range set.chunks {
		if chunk == nil {
			continue
		}
		for fileIndex, file := range chunk {
			fileID := FileID(chunkIndex*hunkMarkFileChunk + fileIndex)
			for wordIndex, word := range file.words {
				for word != 0 {
					bit := bits.TrailingZeros64(word)
					index := HunkIndex(uint32FromInt(wordIndex*64 + bit))
					if !yield(HunkID{file: fileID, index: index}) {
						return
					}
					word &^= uint64(1) << bit
				}
			}
		}
	}
}
