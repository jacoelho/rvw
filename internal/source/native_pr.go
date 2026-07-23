package source

import (
	"context"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/jacoelho/rvw/internal/review"
)

const maxRetainedPullRequestRecords = 3_000

func (service *Service) prepareNativePullRequest(
	ctx context.Context,
	runCPU CPUAdmission,
	repository *repositorySession,
	details PullRequestDetails,
	options OpenOptions,
) (review.Snapshot, revision, error) {
	acquisition, err := newSmartGitAcquisition(ctx, service.github, details.Repository, repository)
	if err != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("advertise pull-request repository: %w", err)
	}
	baseCommitHash := reviewHash(details.BaseOID)
	headCommitHash := reviewHash(details.HeadOID)
	if fetchErr := acquisition.fetchCommitAncestry(
		ctx, baseCommitHash, headCommitHash, details.BaseRef, details.Number,
	); fetchErr != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("fetch pull-request ancestry: %w", fetchErr)
	}
	mergeBase, err := nativeMergeBase(ctx, repository, baseCommitHash, headCommitHash)
	if err != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("resolve pull-request merge base: %w", err)
	}
	baseCommit, err := object.GetCommit(repository.objects, mergeBase)
	if err != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("read pull-request merge-base commit: %w", err)
	}
	headCommit, err := object.GetCommit(repository.objects, headCommitHash)
	if err != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("read pull-request head commit: %w", err)
	}
	if fetchErr := acquisition.fetchTrees(ctx, baseCommit.TreeHash, headCommit.TreeHash); fetchErr != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("fetch pull-request trees: %w", fetchErr)
	}
	options.Gitignore = false
	options.RVWIgnore = false
	records, err := nativeFilteredPullRequestRecords(
		ctx,
		repository,
		baseCommit.TreeHash,
		headCommit.TreeHash,
		options,
	)
	if err != nil {
		return review.Snapshot{}, revision{}, err
	}
	if minimumPullRequestRecords(records) > maxRetainedPullRequestRecords {
		return review.Snapshot{}, revision{}, &LimitError{
			Resource: LimitPullRequestRecords,
			Limit:    maxRetainedPullRequestRecords,
		}
	}
	records = enrichExactNativeRenames(records)
	if nativeSimilarityCandidateCount(records) <= 1_000 {
		if fetchErr := acquisition.fetchBlobs(ctx, nativeSimilarityBlobHashes(records)); fetchErr != nil {
			return review.Snapshot{}, revision{}, fmt.Errorf("fetch rename-candidate blobs: %w", fetchErr)
		}
		records, err = enrichInexactNativeRenames(ctx, repository, records)
		if err != nil {
			return review.Snapshot{}, revision{}, fmt.Errorf("analyze rename candidates: %w", err)
		}
	}
	if len(records) > maxRetainedPullRequestRecords {
		return review.Snapshot{}, revision{}, &LimitError{
			Resource: LimitPullRequestRecords,
			Limit:    maxRetainedPullRequestRecords,
		}
	}
	if fetchErr := acquisition.fetchBlobs(ctx, nativeRecordBlobHashes(records)); fetchErr != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("fetch selected pull-request blobs: %w", fetchErr)
	}
	if analysisErr := populateNativeRecords(ctx, repository, records); analysisErr != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("analyze pull-request changes: %w", analysisErr)
	}
	fresh, err := service.github.pullRequest(ctx, details.Repository, details.Number)
	if err != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("refresh pull-request metadata: %w", err)
	}
	freshDetails, err := validatePullRequestPayload(details.Repository, details.Number, fresh)
	if err != nil {
		return review.Snapshot{}, revision{}, err
	}
	if freshDetails.BaseOID != details.BaseOID || freshDetails.HeadOID != details.HeadOID {
		return review.Snapshot{}, revision{}, fmt.Errorf(
			"%w: pull-request base or head changed while opening; retry explicitly",
			ErrSourceStale,
		)
	}
	sourceID, err := service.newSourceID()
	if err != nil {
		return review.Snapshot{}, revision{}, err
	}
	snapshot, err := service.snapshot(
		ctx, runCPU, sourceID, repository, records, options,
	)
	if err != nil {
		return review.Snapshot{}, revision{}, err
	}
	return snapshot, revision{
		similarities: nativeSimilarityScores(records),
	}, nil
}

func nativeFilteredPullRequestRecords(
	ctx context.Context,
	repository *repositorySession,
	oldTree, newTree plumbing.Hash,
	options OpenOptions,
) ([]diffRecord, error) {
	ledger, err := newSourceLedger(options.RetainedBytes() + 64<<20)
	if err != nil {
		return nil, err
	}
	matcher, err := newCompiledFilterSet(ctx, ledger, options.Include, options.Exclude, nil)
	if err != nil {
		return nil, err
	}
	defer matcher.Close()
	zeroMode, err := review.NewFileMode("000000")
	if err != nil {
		return nil, err
	}
	records := make([]diffRecord, 0, 128)
	additions, deletions, other := 0, 0, 0
	err = walkNativeTreeDiff(
		ctx,
		repository,
		oldTree,
		newTree,
		review.MaxFiles,
		func(record diffRecord) error {
			filtered, keep, filterErr := filterNativePullRequestRecord(
				record,
				options,
				matcher,
				zeroMode,
			)
			if filterErr != nil || !keep {
				return filterErr
			}
			switch filtered.status {
			case 'A':
				additions++
			case 'D':
				deletions++
			default:
				other++
			}
			if other+max(additions, deletions) > maxRetainedPullRequestRecords {
				return &LimitError{
					Resource: LimitPullRequestRecords,
					Limit:    maxRetainedPullRequestRecords,
				}
			}
			records = append(records, filtered)
			return nil
		},
	)
	return records, err
}

func filterNativePullRequestRecord(
	record diffRecord,
	options OpenOptions,
	matcher *compiledFilterSet,
	zeroMode review.FileMode,
) (diffRecord, bool, error) {
	oldRetained, err := nativePullRequestPathRetained(record.oldPath, options, matcher)
	if err != nil {
		return diffRecord{}, false, err
	}
	newRetained, err := nativePullRequestPathRetained(record.newPath, options, matcher)
	if err != nil {
		return diffRecord{}, false, err
	}
	switch {
	case oldRetained && newRetained:
		return record, true, nil
	case oldRetained:
		record.newOID = nil
		record.newMode = zeroMode
		record.newPath = ""
		record.status = 'D'
		return record, true, nil
	case newRetained:
		record.oldOID = nil
		record.oldMode = zeroMode
		record.oldPath = ""
		record.status = 'A'
		return record, true, nil
	default:
		return diffRecord{}, false, nil
	}
}

func nativePullRequestPathRetained(
	path string,
	options OpenOptions,
	matcher *compiledFilterSet,
) (bool, error) {
	if path == "" {
		return false, nil
	}
	encoding := filterPathInvalidUTF8
	if utf8.ValidString(path) {
		encoding = filterPathValidUTF8
	}
	match, err := matcher.matchPrepared(path, encoding)
	if err != nil {
		return false, err
	}
	flags, err := filterFlags(
		path,
		filterPathFacts{encoding: encoding, gitignore: filterNotGitignored},
		options,
		match,
	)
	return err == nil && !flags.Hidden(), err
}

func minimumPullRequestRecords(records []diffRecord) int {
	additions, deletions, other := 0, 0, 0
	for _, record := range records {
		switch record.status {
		case 'A':
			additions++
		case 'D':
			deletions++
		default:
			other++
		}
	}
	return other + max(additions, deletions)
}

func nativeRecordBlobHashes(records []diffRecord) []plumbing.Hash {
	hashes := make([]plumbing.Hash, 0, len(records)*2)
	seen := make(map[plumbing.Hash]struct{}, len(records)*2)
	for _, record := range records {
		for _, side := range []struct {
			oid  *review.ObjectID
			mode review.FileMode
		}{{record.oldOID, record.oldMode}, {record.newOID, record.newMode}} {
			if side.oid == nil || objectKind(side.mode) != review.ObjectBlob {
				continue
			}
			hash := plumbing.NewHash(side.oid.String())
			if _, exists := seen[hash]; exists {
				continue
			}
			seen[hash] = struct{}{}
			hashes = append(hashes, hash)
		}
	}
	sort.Slice(hashes, func(left, right int) bool {
		return hashes[left].String() < hashes[right].String()
	})
	return hashes
}
