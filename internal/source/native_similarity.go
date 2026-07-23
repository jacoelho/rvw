package source

import (
	"bufio"
	"context"
	"errors"
	"io"
	"path/filepath"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/jacoelho/rvw/internal/review"
)

const (
	maxSimilaritySignatureSpans = 536_870_912
	maxSimilarityCandidatePairs = 1_000_000
	maxSimilarityMergeSteps     = 128_000_000
	maxSimilarityCacheBytes     = 4 << 20
)

type nativeSimilaritySource struct { //nolint:govet // Field order mirrors source-side scoring data.
	record   int
	oid      review.ObjectID
	path     string
	mode     review.FileMode
	deletion bool
}

type nativeSimilarityTarget struct { //nolint:govet // Field order mirrors target-side scoring data.
	record int
	oid    review.ObjectID
	path   string
	mode   review.FileMode
}

type nativeSpanCounts struct {
	counts map[string]uint32
	size   uint64
	spans  uint64
	bytes  uint64
}

type nativeSimilarityPair struct {
	old review.ObjectID
	new review.ObjectID
}

func nativeSimilarityCandidates(
	records []diffRecord,
) ([]nativeSimilaritySource, []nativeSimilarityTarget) {
	sources := make([]nativeSimilaritySource, 0)
	targets := make([]nativeSimilarityTarget, 0)
	for index, record := range records {
		if record.unmerged != nil {
			continue
		}
		if record.status != 'R' && record.status != 'C' && record.oldOID != nil &&
			objectKind(record.oldMode) == review.ObjectBlob &&
			record.oldMode.String() != "120000" {
			sources = append(sources, nativeSimilaritySource{
				record: index, oid: *record.oldOID, path: record.oldPath,
				mode: record.oldMode, deletion: record.status == 'D',
			})
		}
		if record.status == 'A' && record.newOID != nil &&
			objectKind(record.newMode) == review.ObjectBlob && record.newMode.String() != "120000" {
			targets = append(targets, nativeSimilarityTarget{
				record: index, oid: *record.newOID, path: record.newPath, mode: record.newMode,
			})
		}
	}
	return sources, targets
}

func nativeSimilarityCandidateCount(records []diffRecord) int {
	sources, targets := nativeSimilarityCandidates(records)
	return len(sources) + len(targets)
}

func nativeSimilarityBlobHashes(records []diffRecord) []plumbing.Hash {
	sources, targets := nativeSimilarityCandidates(records)
	hashes := make([]plumbing.Hash, 0, len(sources)+len(targets))
	seen := make(map[plumbing.Hash]struct{}, len(sources)+len(targets))
	for _, oid := range appendSimilarityOIDs(sources, targets) {
		hash := plumbing.NewHash(oid.String())
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}
		hashes = append(hashes, hash)
	}
	sort.Slice(hashes, func(left, right int) bool {
		return hashes[left].String() < hashes[right].String()
	})
	return hashes
}

func appendSimilarityOIDs(
	sources []nativeSimilaritySource,
	targets []nativeSimilarityTarget,
) []review.ObjectID {
	values := make([]review.ObjectID, 0, len(sources)+len(targets))
	for _, source := range sources {
		values = append(values, source.oid)
	}
	for _, target := range targets {
		values = append(values, target.oid)
	}
	return values
}

//nolint:gocognit // Candidate admission, bounded cache reuse, scoring, and deterministic selection form one algorithm.
func enrichInexactNativeRenames(
	ctx context.Context,
	session *repositorySession,
	records []diffRecord,
) ([]diffRecord, error) {
	sources, targets := nativeSimilarityCandidates(records)
	if len(sources)+len(targets) > 1_000 || len(sources)*len(targets) > maxSimilarityCandidatePairs {
		return records, nil
	}
	removed := make([]bool, len(records))
	consumed := make([]bool, len(sources))
	cache := make(map[review.ObjectID]nativeSpanCounts)
	var spans, cacheBytes, mergeSteps uint64
	loadCounts := func(oid review.ObjectID) (nativeSpanCounts, bool) {
		if counts, ok := cache[oid]; ok {
			return counts, true
		}
		counts, err := nativeContentSpanCounts(ctx, session, oid)
		if err != nil || counts.bytes > maxSimilarityCacheBytes-cacheBytes {
			return nativeSpanCounts{}, false
		}
		if counts.spans > maxSimilaritySignatureSpans-spans {
			return nativeSpanCounts{}, false
		}
		cache[oid] = counts
		cacheBytes += counts.bytes
		spans += counts.spans
		return counts, true
	}
	for _, target := range targets {
		if context.Cause(ctx) != nil {
			return nil, context.Cause(ctx)
		}
		targetCounts, ok := loadCounts(target.oid)
		if !ok {
			return compactSimilarityRecords(records, removed), nil
		}
		bestSource, bestScore, bestBasename := -1, uint64(0), false
		for sourceIndex, source := range sources {
			if source.deletion && consumed[sourceIndex] || source.oid == target.oid {
				continue
			}
			sourceCounts, ok := loadCounts(source.oid)
			if !ok {
				return compactSimilarityRecords(records, removed), nil
			}
			copied := uint64(0)
			for span, sourceCount := range sourceCounts.counts {
				mergeSteps++
				if mergeSteps > maxSimilarityMergeSteps {
					return compactSimilarityRecords(records, removed), nil
				}
				count := min(sourceCount, targetCounts.counts[span])
				copied += uint64(count) * uint64(len(span))
			}
			maximum := max(sourceCounts.size, targetCounts.size)
			if maximum == 0 {
				continue
			}
			score := copied * 60_000 / maximum
			if score < 30_000 {
				continue
			}
			basename := filepath.Base(source.path) == filepath.Base(target.path)
			if bestSource < 0 || score > bestScore ||
				score == bestScore && basename && !bestBasename ||
				score == bestScore && basename == bestBasename && source.path < sources[bestSource].path {
				bestSource, bestScore, bestBasename = sourceIndex, score, basename
			}
		}
		if bestSource < 0 {
			continue
		}
		source := sources[bestSource]
		result := &records[target.record]
		result.oldOID = new(source.oid)
		result.oldMode = source.mode
		result.oldPath = source.path
		result.similarity = uint16(bestScore)
		if source.deletion {
			result.status = 'R'
			removed[source.record] = true
			consumed[bestSource] = true
		} else {
			result.status = 'C'
		}
		if err := populateNativeDiff(ctx, session, result); err != nil {
			return nil, err
		}
	}
	return compactSimilarityRecords(records, removed), nil
}

func nativeSimilarityScores(records []diffRecord) map[nativeSimilarityPair]uint16 {
	var scores map[nativeSimilarityPair]uint16
	for _, record := range records {
		if record.unmerged != nil {
			continue
		}
		if record.similarity == 0 || record.oldOID == nil || record.newOID == nil {
			continue
		}
		if scores == nil {
			scores = make(map[nativeSimilarityPair]uint16)
		}
		scores[nativeSimilarityPair{old: *record.oldOID, new: *record.newOID}] = record.similarity
	}
	return scores
}

func nativeContentSpanCounts(
	ctx context.Context,
	session *repositorySession,
	oid review.ObjectID,
) (nativeSpanCounts, error) {
	reader, size, err := session.openContent(plumbing.NewHash(oid.String()))
	if err != nil {
		return nativeSpanCounts{}, err
	}
	defer reader.Close() //nolint:errcheck // The immutable content reader has no commit-on-close behavior.
	if size < 0 {
		return nativeSpanCounts{}, errors.New("similarity content has an invalid size")
	}
	result := nativeSpanCounts{counts: make(map[string]uint32), size: uint64(size)}
	buffered := bufio.NewReaderSize(reader, 4<<10)
	span := make([]byte, 0, 64)
	for {
		if err := context.Cause(ctx); err != nil {
			return nativeSpanCounts{}, err
		}
		value, readErr := buffered.ReadByte()
		if readErr == nil {
			span = append(span, value)
		}
		if len(span) == 64 || value == '\n' || readErr != nil && len(span) != 0 {
			key := string(span)
			result.counts[key]++
			result.spans++
			result.bytes += uint64(len(key) + 24)
			span = span[:0]
			if result.bytes > maxSimilarityCacheBytes {
				return result, nil
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return result, nil
			}
			return nativeSpanCounts{}, readErr
		}
	}
}

func compactSimilarityRecords(records []diffRecord, removed []bool) []diffRecord {
	result := records[:0]
	for index, record := range records {
		if !removed[index] {
			result = append(result, record)
		}
	}
	return result
}
