package tui

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	"github.com/jacoelho/rvw/internal/change"
	workcoord "github.com/jacoelho/rvw/internal/coordinator"
)

type searchCandidates struct {
	files     []change.FileID
	workspace change.WorkspaceRevision
	source    change.SourceID
	count     int
}

const searchAccumulatorGrowthSeed = 8

type searchAccumulator struct {
	hits          []change.SearchHit
	retainedBytes int64
}

func (accumulator *searchAccumulator) append(hit change.SearchHit) bool {
	if accumulator == nil || len(accumulator.hits) >= change.MaxSearchHits {
		return false
	}
	excerptBytes := hit.RetainedBytes() - change.SearchHitStorageBytes
	if excerptBytes < 0 ||
		accumulator.retainedBytes > change.MaxSearchHitBytes-excerptBytes {
		return false
	}
	if len(accumulator.hits) < cap(accumulator.hits) {
		accumulator.hits = append(accumulator.hits, hit)
		accumulator.retainedBytes += excerptBytes
		return true
	}
	required := len(accumulator.hits) + 1
	capacity, ok := nextSearchAccumulatorCapacity(
		cap(accumulator.hits),
		required,
		accumulator.retainedBytes,
		excerptBytes,
	)
	if !ok {
		return false
	}
	hits := make([]change.SearchHit, len(accumulator.hits), capacity)
	copy(hits, accumulator.hits)
	oldCapacity := cap(accumulator.hits)
	hits = append(hits, hit)
	accumulator.hits = hits
	accumulator.retainedBytes +=
		int64(cap(accumulator.hits)-oldCapacity)*change.SearchHitStorageBytes + excerptBytes
	return true
}

func nextSearchAccumulatorCapacity(
	current, required int,
	retainedBytes, excerptBytes int64,
) (int, bool) {
	if current < 0 || current > change.MaxSearchHits || required <= current ||
		required > change.MaxSearchHits || retainedBytes < 0 || excerptBytes < 0 ||
		retainedBytes > change.MaxSearchHitBytes-excerptBytes {
		return 0, false
	}
	candidate := searchAccumulatorGrowthSeed
	if current != 0 {
		if current > change.MaxSearchHits/2 {
			candidate = change.MaxSearchHits
		} else {
			candidate = current * 2
		}
	}
	remainingBytes := change.MaxSearchHitBytes - retainedBytes - excerptBytes
	byteCapacity := current + int(remainingBytes/change.SearchHitStorageBytes)
	candidate = min(
		max(candidate, searchAccumulatorGrowthSeed),
		change.MaxSearchHits,
		byteCapacity,
	)
	return candidate, candidate >= required
}

func newSearchCandidates(current change.Review, files []change.FileID) (*searchCandidates, error) {
	candidates := &searchCandidates{
		files:     files,
		workspace: current.WorkspaceRevision(),
		source:    current.Snapshot().SourceID(),
		count:     len(files),
	}
	if !candidates.valid() {
		return nil, change.ErrInvalidSearch
	}
	return candidates, nil
}

func (candidates *searchCandidates) valid() bool {
	return candidates != nil && candidates.workspace != 0 && candidates.source != 0 &&
		candidates.count > 0 && candidates.count <= change.MaxFiles &&
		candidates.count == len(candidates.files)
}

func (candidates *searchCandidates) validFor(admission change.SearchAdmission) bool {
	return candidates.valid() && candidates.workspace == admission.Workspace() &&
		candidates.source == admission.Source()
}

func (candidates *searchCandidates) retainedBytes() (int64, error) {
	if !candidates.valid() {
		return 0, change.ErrInvalidSearch
	}
	backing, err := change.SearchCandidateRetainedBytes(cap(candidates.files))
	if err != nil {
		return 0, err
	}
	return int64(unsafe.Sizeof(*candidates)) + backing, nil
}

// SearchWork represents admitted search work.
type SearchWork struct {
	Document   *workcoord.SourceRevision
	Candidates *searchCandidates
	Admission  change.SearchAdmission
}

// SearchOutcome describes a terminal search outcome.
type SearchOutcome struct {
	Err    error
	Result change.SearchResult
	ID     change.OperationID
}

func (SearchOutcome) coordinatorOutcome() {}

func (outcome SearchOutcome) operationID() change.OperationID { return outcome.ID }

type searchJob struct {
	ctx  context.Context
	work SearchWork
}

func (work SearchWork) requestBytes() (int64, error) {
	if work.Document == nil || work.Admission.Operation() == 0 ||
		!work.Candidates.validFor(work.Admission) || work.Admission.Query() == "" {
		return 0, errors.New("invalid search work")
	}
	candidates, err := work.Candidates.retainedBytes()
	if err != nil {
		return 0, err
	}
	return int64(unsafe.Sizeof(work)) + candidates + int64(len(work.Admission.Query())), nil
}

func (work SearchWork) resultBytes() (int64, error) {
	if !work.Candidates.validFor(work.Admission) {
		return 0, errors.New("invalid search work")
	}
	request, err := change.SearchRequestConstructionBytes(
		work.Admission.Query(),
		work.Candidates.count,
	)
	if err != nil {
		return 0, err
	}
	target, err := change.SearchTargetConstructionBytes(work.Candidates.count)
	if err != nil {
		return 0, err
	}
	searchPhase := max(
		change.MaxSearchHitBytes+change.MaxSearchBatchConstructionBytes,
		change.MaxSearchAggregateConstructionBytes+change.MaxSearchBatchRetainedBytes,
	)
	scan := request + patchResultReservation + searchPhase
	return max(target, scan), nil
}

// SubmitSearch admits search work without scanning candidate files.
func (coordinator *Coordinator) SubmitSearch(work SearchWork) error {
	requestBytes, err := work.requestBytes()
	if err != nil {
		return err
	}
	resultBytes, err := work.resultBytes()
	if err != nil {
		return err
	}
	return coordinator.submit(
		work.Admission.Operation(),
		requestBytes,
		resultBytes,
		func(ctx context.Context) bool {
			select {
			case coordinator.searchJobs <- searchJob{work: work, ctx: ctx}:
				return true
			default:
				return false
			}
		},
	)
}

func (coordinator *Coordinator) runSearchWorker() {
	for job := range coordinator.searchJobs {
		operation := job.work.Admission.Operation()
		coordinator.required.Wait(operation)
		result, err := coordinator.executeSearch(job, operation)
		coordinator.complete(SearchOutcome{
			ID:     operation,
			Result: result,
			Err:    err,
		})
	}
}

func (coordinator *Coordinator) executeSearch(
	job searchJob,
	operation change.OperationID,
) (change.SearchResult, error) {
	request, err := coordinator.prepareSearchRequest(&job)
	if err != nil {
		return change.SearchResult{}, err
	}
	snapshot := job.work.Document.Snapshot()
	opened, revision, available := job.work.Document.Materialization()
	if !available {
		return change.SearchResult{}, errors.New("search source revision is unavailable")
	}
	var accumulator searchAccumulator
	var unavailable uint32
	status := change.SearchComplete
	target := request.Target()
	total := target.FileCount()
	fileIndex := 0
	for fileID := range target.Files() {
		file, ok := snapshot.File(fileID)
		if !ok {
			return change.SearchResult{}, errors.New("search file is unavailable")
		}
		currentIndex := fileIndex
		fileIndex++
		coordinator.reportProgress(Progress{
			ID:        operation,
			Completed: uint64FromInt(currentIndex),
			Total:     uint64FromInt(total),
			Detail:    boundedOperationDetail("searching changed files"),
		})
		if context.Cause(job.ctx) != nil {
			status = change.SearchCancelled
			accumulator = searchAccumulator{}
			break
		}
		acquisition, loadErr := coordinator.service.AcquirePatch(
			job.ctx,
			opened,
			revision,
			change.PatchRequest{
				Workspace: request.Workspace(),
				Handle:    file.PatchHandle(),
				Context:   request.Context(),
			},
		)
		var patchResult change.PatchResult
		if loadErr == nil {
			patchResult, loadErr = acquisition.Finish(job.ctx, coordinator.runCPU)
		}
		acquisition.Close()
		if loadErr != nil {
			if context.Cause(job.ctx) != nil {
				status = change.SearchCancelled
				accumulator = searchAccumulator{}
				break
			}
			unavailable++
			continue
		}
		if patchResult.Status != change.PatchReady || patchResult.Prepared == nil {
			if patchResult.Status == change.PatchCancelled {
				status = change.SearchCancelled
				accumulator = searchAccumulator{}
				break
			}
			unavailable++
			continue
		}
		status, accumulator = coordinator.scanSearchPatch(
			job,
			operation,
			request,
			patchResult.Prepared,
			file.ID(),
			currentIndex,
			total,
			status,
			accumulator,
		)
		if status == change.SearchCancelled || status == change.SearchLimited {
			break
		}
	}
	coordinator.reportProgress(Progress{
		ID:        operation,
		Completed: uint64FromInt(total),
		Total:     uint64FromInt(total),
		Detail:    boundedOperationDetail("search scan complete"),
	})
	if !coordinator.acquireCPU(job.ctx) {
		return change.SearchResult{}, searchContextError(job.ctx)
	}
	result, resultErr := finishSearchResult(
		func() error { return context.Cause(job.ctx) },
		request,
		status,
		unavailable,
		accumulator,
	)
	coordinator.releaseCPU()
	return result, resultErr
}

func (coordinator *Coordinator) prepareSearchRequest(
	job *searchJob,
) (change.SearchRequest, error) {
	snapshot := job.work.Document.Snapshot()
	admission := job.work.Admission
	candidates := job.work.Candidates
	job.work.Candidates = nil
	job.work.Admission = change.SearchAdmission{}
	if snapshot.SourceID() != admission.Source() || !candidates.validFor(admission) {
		return change.SearchRequest{}, errors.New("search source revision is stale")
	}
	if !coordinator.acquireCPU(job.ctx) {
		return change.SearchRequest{}, searchContextError(job.ctx)
	}
	target, err := change.NewSearchTargetCancellable(
		func() error { return context.Cause(job.ctx) },
		snapshot,
		candidates.files,
	)
	coordinator.releaseCPU()
	if err != nil {
		return change.SearchRequest{}, err
	}
	if !coordinator.acquireCPU(job.ctx) {
		return change.SearchRequest{}, searchContextError(job.ctx)
	}
	request, err := change.PrepareSearchRequestCancellable(
		func() error { return context.Cause(job.ctx) },
		admission,
		target,
	)
	coordinator.releaseCPU()
	return request, err
}

func (coordinator *Coordinator) scanSearchPatch(
	job searchJob,
	operation change.OperationID,
	request change.SearchRequest,
	patch *change.PreparedPatch,
	fileID change.FileID,
	fileIndex, total int,
	status change.SearchStatus,
	accumulator searchAccumulator,
) (change.SearchStatus, searchAccumulator) {
	var cursor change.SearchCursor
	for {
		if context.Cause(job.ctx) != nil || !coordinator.acquireCPU(job.ctx) {
			return change.SearchCancelled, searchAccumulator{}
		}
		batch := change.ScanSearchBatch(patch, fileID, request.Query(), cursor)
		coordinator.releaseCPU()
		cursor = batch.Cursor()
		for index := range batch.HitCount() {
			hit, ok := batch.Hit(index)
			if !ok || !accumulator.append(hit) {
				status = change.SearchLimited
				break
			}
		}
		coordinator.reportProgress(Progress{
			ID:        operation,
			Completed: uint64FromInt(fileIndex),
			Total:     uint64FromInt(total),
			Detail: boundedOperationDetail(
				fmt.Sprintf("%d matches retained", len(accumulator.hits)),
			),
		})
		if batch.Done() || status == change.SearchLimited {
			return status, accumulator
		}
	}
}

func finishSearchResult(
	cancelled func() error,
	request change.SearchRequest,
	status change.SearchStatus,
	unavailable uint32,
	accumulator searchAccumulator,
) (change.SearchResult, error) {
	var detail change.OperationDetail
	switch {
	case status == change.SearchCancelled:
		detail = boundedOperationDetail("search cancelled")
	case status == change.SearchLimited:
		detail = boundedOperationDetail("search result capacity reached")
	case unavailable != 0:
		status = change.SearchPartial
		detail = boundedOperationDetail(fmt.Sprintf("%d files unavailable", unavailable))
	}
	result, err := change.TakeSearchResultCancellable(cancelled, change.SearchResultDescriptor{
		Operation:        request.Operation(),
		Workspace:        request.Workspace(),
		Source:           request.Source(),
		Status:           status,
		UnavailableFiles: unavailable,
		Detail:           detail,
	}, accumulator.hits)
	if err != nil {
		return change.SearchResult{}, err
	}
	wantBytes := accumulator.retainedBytes + result.Detail().Bytes()
	if result.RetainedBytes() != wantBytes {
		return change.SearchResult{}, fmt.Errorf(
			"search result retained bytes = %d, want %d",
			result.RetainedBytes(),
			wantBytes,
		)
	}
	return result, nil
}

func searchContextError(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return context.Canceled
}
