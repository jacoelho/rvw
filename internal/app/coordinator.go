package app

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/jacoelho/rvw/internal/delivery"
	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// MaxAcceptedOperations defines the supported accepted operations limit.
const (
	MaxAcceptedOperations       = 64
	MaxQueuedRequestBytes int64 = 32 << 20
	// MaxRetainedResultBytes allows one search to retain one maximum patch plus
	// bounded hits while a presentation patch may complete concurrently.
	MaxRetainedResultBytes    int64 = 256 << 20
	MaxIOWorkers                    = 8
	MaxCPUWorkers                   = 8
	MaxPresentationCacheBytes int64 = 128 << 20

	reservedPublicationRequestBytes int64 = review.MaxPublicationBytes
	reservedPublicationResultBytes  int64 = review.MaxPublicationBytes
	patchResultReservation          int64 = review.MaxPatchBytes +
		review.MaxPatchLines*25 +
		review.MaxHunks*12
	reloadResultReservation int64 = review.MaxSourcePaths + review.MaxFiles*256
)

// ErrCoordinatorClosed identifies an coordinator closed failure.
var (
	ErrCoordinatorClosed    = errors.New("work coordinator is closed")
	ErrCoordinatorSaturated = errors.New("work coordinator is saturated")
)

// PatchWork represents patch work.
type PatchWork struct {
	Document *source.Document
	Request  review.PatchRequest
	ID       review.OperationID
}

// PatchOutcome describes patch outcome.
type PatchOutcome struct {
	Err    error
	Result review.PatchResult
	ID     review.OperationID
}

// SearchWork represents search work.
type SearchWork struct {
	Document *source.Document
	Request  review.SearchRequest
	ID       review.OperationID
}

// SearchOutcome describes search outcome.
type SearchOutcome struct {
	Err    error
	Result review.SearchResult
	ID     review.OperationID
}

// PublicationCheckOutcome describes publication check outcome.
type PublicationCheckOutcome struct {
	Err   error
	Check review.PublicationCheckResult
	ID    review.OperationID
}

// PublicationCommitOutcome describes publication commit outcome.
type PublicationCommitOutcome struct {
	Publication review.PublicationResult
	ID          review.OperationID
}

// ReconciliationOutcome describes reconciliation outcome.
type ReconciliationOutcome struct {
	Reconciliation review.ReconciliationResult
	ID             review.OperationID
}

// ExportWork represents export work.
type ExportWork struct {
	Templates   review.ExportTemplates
	Input       review.ExportInput
	ID          review.OperationID
	Options     review.ExportOptions
	Format      review.ExportFormat
	Destination delivery.Destination
}

// ExportOutcome describes export outcome.
type ExportOutcome struct {
	Err         error
	Prepared    review.PreparedExport
	ID          review.OperationID
	Destination delivery.Destination
}

// DeliveryWork represents delivery work.
type DeliveryWork struct {
	Prepared    review.PreparedExport
	ID          review.OperationID
	Destination delivery.Destination
}

// DeliveryOutcome describes delivery outcome.
type DeliveryOutcome struct {
	Err     error
	Outcome delivery.Outcome
	ID      review.OperationID
}

// Outcome describes outcome.
type Outcome interface {
	coordinatorOutcome()
	operationID() review.OperationID
}

func (PatchOutcome) coordinatorOutcome()                                 {}
func (SearchOutcome) coordinatorOutcome()                                {}
func (SyntaxOutcome) coordinatorOutcome()                                {}
func (PublicationCheckOutcome) coordinatorOutcome()                      {}
func (PublicationCommitOutcome) coordinatorOutcome()                     {}
func (ReconciliationOutcome) coordinatorOutcome()                        {}
func (ReloadAcquiredOutcome) coordinatorOutcome()                        {}
func (ReloadPreparedOutcome) coordinatorOutcome()                        {}
func (FileTreeOutcome) coordinatorOutcome()                              {}
func (WorkspaceIndexOutcome) coordinatorOutcome()                        {}
func (ExportOutcome) coordinatorOutcome()                                {}
func (DeliveryOutcome) coordinatorOutcome()                              {}
func (Progress) coordinatorOutcome()                                     {}
func (outcome PatchOutcome) operationID() review.OperationID             { return outcome.ID }
func (outcome SearchOutcome) operationID() review.OperationID            { return outcome.ID }
func (outcome SyntaxOutcome) operationID() review.OperationID            { return outcome.ID }
func (outcome PublicationCheckOutcome) operationID() review.OperationID  { return outcome.ID }
func (outcome PublicationCommitOutcome) operationID() review.OperationID { return outcome.ID }
func (outcome ReconciliationOutcome) operationID() review.OperationID    { return outcome.ID }
func (outcome ReloadAcquiredOutcome) operationID() review.OperationID    { return outcome.ID }
func (outcome ReloadPreparedOutcome) operationID() review.OperationID    { return outcome.ID }
func (outcome FileTreeOutcome) operationID() review.OperationID          { return outcome.ID }
func (outcome WorkspaceIndexOutcome) operationID() review.OperationID    { return outcome.ID }
func (outcome ExportOutcome) operationID() review.OperationID            { return outcome.ID }
func (outcome DeliveryOutcome) operationID() review.OperationID          { return outcome.ID }
func (outcome Progress) operationID() review.OperationID                 { return outcome.ID }

// ReadyState describes ready state.
type ReadyState uint8

// ReadyAvailable identifies a supported ready available value.
const (
	ReadyAvailable ReadyState = iota + 1
	ReadyClosed
	ReadyCancelled
)

type reservation struct {
	cancel          context.CancelFunc
	cancelOperation func()
	publication     *publicationReservation
	requestBytes    int64
	resultBytes     int64
	completed       bool
}

type publicationReservation struct {
	marker review.PublicationMarker
	target review.PublicationTarget
}

type patchJob struct {
	ctx  context.Context
	work PatchWork
}

type searchJob struct {
	ctx  context.Context
	work SearchWork
}

type reloadJob struct {
	ctx  context.Context
	work ReloadWork
}

type reloadPreparationJob struct {
	ctx  context.Context
	work ReloadPreparationWork
}

type fileTreeJob struct {
	ctx  context.Context
	work FileTreeWork
}

type workspaceIndexJob struct {
	ctx  context.Context
	work WorkspaceIndexWork
}

type exportJob struct {
	ctx  context.Context
	work ExportWork
}

type deliveryJob struct {
	ctx  context.Context
	work DeliveryWork
}

// Coordinator represents coordinator.
type Coordinator struct {
	stats                 atomic.Value
	workContext           context.Context
	workspaceIndexJobs    chan workspaceIndexJob
	progressStop          chan struct{}
	reloadPreparationJobs chan reloadPreparationJob
	done                  chan struct{}
	cpuPermits            chan struct{}
	accepted              map[review.OperationID]reservation
	retireDocuments       chan *source.Document
	service               *source.Service
	reloadJobs            chan reloadJob
	progress              map[review.OperationID]Progress
	delivery              *delivery.Manager
	publicationJobs       chan publicationJob
	fileTreeJobs          chan fileTreeJob
	required              *requiredScheduler
	syntaxJobs            chan syntaxJob
	exportJobs            chan exportJob
	deliveryJobs          chan deliveryJob
	ready                 chan struct{}
	patchJobs             chan patchJob
	searchJobs            chan searchJob
	cleanupErrors         []error
	progressOrder         []review.OperationID
	outcomes              []Outcome
	workers               sync.WaitGroup
	reservedResult        int64
	publicationActive     review.OperationID
	cleanupOmitted        uint64
	progressBytes         int64
	requestBytes          int64
	ioCount               int
	cpuCount              int
	mu                    sync.Mutex
	progressDirty         bool
	readyClosed           bool
	workersDone           bool
	closed                bool
}

// NewCoordinator constructs and validates coordinator.
func NewCoordinator(
	ctx context.Context,
	service *source.Service,
	deliveryManager *delivery.Manager,
) (*Coordinator, error) {
	if ctx == nil || service == nil || deliveryManager == nil {
		return nil, errors.New("work coordinator requires context, source service, and delivery manager")
	}
	ioWorkers := MaxIOWorkers
	cpuWorkers := min(max(runtime.GOMAXPROCS(0), 1), MaxCPUWorkers)
	coordinator := &Coordinator{
		workContext:           ctx,
		service:               service,
		delivery:              deliveryManager,
		accepted:              make(map[review.OperationID]reservation),
		progress:              make(map[review.OperationID]Progress),
		required:              newRequiredScheduler(),
		ready:                 make(chan struct{}, 1),
		patchJobs:             make(chan patchJob, MaxAcceptedOperations),
		searchJobs:            make(chan searchJob, 1),
		syntaxJobs:            make(chan syntaxJob, 8),
		publicationJobs:       make(chan publicationJob, 1),
		reloadJobs:            make(chan reloadJob, 1),
		reloadPreparationJobs: make(chan reloadPreparationJob, 1),
		fileTreeJobs:          make(chan fileTreeJob, 1),
		workspaceIndexJobs:    make(chan workspaceIndexJob, 1),
		exportJobs:            make(chan exportJob, MaxAcceptedOperations),
		deliveryJobs:          make(chan deliveryJob, 1),
		retireDocuments:       make(chan *source.Document, MaxAcceptedOperations),
		progressStop:          make(chan struct{}),
		cpuPermits:            make(chan struct{}, cpuWorkers),
		done:                  make(chan struct{}),
		ioCount:               ioWorkers,
		cpuCount:              cpuWorkers,
	}
	coordinator.stats.Store(CoordinatorStats{IOWorkers: ioWorkers, CPUWorkers: cpuWorkers})
	for range ioWorkers - 5 {
		coordinator.workers.Go(coordinator.runPatchWorker)
	}
	coordinator.workers.Go(coordinator.runSearchWorker)
	coordinator.workers.Go(coordinator.runSyntaxWorker)
	coordinator.workers.Go(coordinator.runPublicationWorker)
	coordinator.workers.Go(coordinator.runDeliveryWorker)
	coordinator.workers.Go(coordinator.runReloadWorker)
	coordinator.workers.Go(coordinator.runProgressNotifier)
	for range cpuWorkers {
		coordinator.workers.Go(coordinator.runCPUWorker)
	}
	go coordinator.monitorWorkers()
	return coordinator, nil
}

// SubmitReload performs the submit reload operation.
func (coordinator *Coordinator) SubmitReload(work ReloadWork) error {
	if work.ID == 0 || work.Document == nil {
		return errors.New("invalid reload work")
	}
	return coordinator.submit(
		work.ID,
		128,
		reloadResultReservation,
		func(ctx context.Context) bool {
			select {
			case coordinator.reloadJobs <- reloadJob{work: work, ctx: ctx}:
				return true
			default:
				return false
			}
		},
	)
}

// SubmitReloadPreparation performs the submit reload preparation operation.
func (coordinator *Coordinator) SubmitReloadPreparation(work ReloadPreparationWork) error {
	if work.ID == 0 || work.Document == nil ||
		work.Review.Snapshot().SourceID() == 0 {
		return errors.New("invalid reload preparation work")
	}
	requestBytes := work.Review.RetainedCommentBytes() + 128
	return coordinator.submit(
		work.ID,
		requestBytes,
		reloadResultReservation,
		func(ctx context.Context) bool {
			select {
			case coordinator.reloadPreparationJobs <- reloadPreparationJob{
				work: work,
				ctx:  ctx,
			}:
				return true
			default:
				return false
			}
		},
	)
}

// SubmitFileTree performs the submit file tree operation.
func (coordinator *Coordinator) SubmitFileTree(work FileTreeWork) error {
	if work.ID == 0 || work.Generation == 0 ||
		work.Snapshot.SourceID() == 0 ||
		len(work.Files) > review.MaxFiles {
		return errors.New("invalid file tree work")
	}
	requestBytes := int64(len(work.Files))*4 + 128
	return coordinator.submit(
		work.ID,
		requestBytes,
		reloadResultReservation,
		func(ctx context.Context) bool {
			select {
			case coordinator.fileTreeJobs <- fileTreeJob{work: work, ctx: ctx}:
				return true
			default:
				return false
			}
		},
	)
}

// SubmitWorkspaceIndex performs the submit workspace index operation.
func (coordinator *Coordinator) SubmitWorkspaceIndex(work WorkspaceIndexWork) error {
	if work.ID == 0 || work.Snapshot.SourceID() == 0 {
		return errors.New("invalid workspace index work")
	}
	return coordinator.submit(
		work.ID,
		128,
		reloadResultReservation,
		func(ctx context.Context) bool {
			select {
			case coordinator.workspaceIndexJobs <- workspaceIndexJob{work: work, ctx: ctx}:
				return true
			default:
				return false
			}
		},
	)
}

// ReleaseDocument performs the release document operation.
func (coordinator *Coordinator) ReleaseDocument(document *source.Document) error {
	if document == nil {
		return errors.New("cannot release a nil source document")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return ErrCoordinatorClosed
	}
	select {
	case coordinator.retireDocuments <- document:
		return nil
	default:
		return ErrCoordinatorSaturated
	}
}

// SubmitPatch performs the submit patch operation.
func (coordinator *Coordinator) SubmitPatch(work PatchWork) error {
	if work.ID == 0 || work.Document == nil || work.Request.Handle.ID() == 0 {
		return errors.New("invalid patch work")
	}
	const requestBytes int64 = 64
	return coordinator.submit(
		work.ID,
		requestBytes,
		patchResultReservation,
		func(ctx context.Context) bool {
			select {
			case coordinator.patchJobs <- patchJob{work: work, ctx: ctx}:
				return true
			default:
				return false
			}
		},
	)
}

// SubmitSearch performs the submit search operation.
func (coordinator *Coordinator) SubmitSearch(work SearchWork) error {
	if work.ID == 0 || work.Document == nil ||
		work.Request.Operation != work.ID ||
		work.Request.Source == 0 ||
		work.Request.Query == "" {
		return errors.New("invalid search work")
	}
	if work.Request.Target.FileCount() == 0 {
		return errors.New("invalid search work")
	}
	requestBytes := int64(len(work.Request.Query)) + work.Request.Target.RetainedBytes() + 128
	return coordinator.submit(
		work.ID,
		requestBytes,
		patchResultReservation+review.MaxSearchHitBytes,
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

// SubmitExport performs the submit export operation.
func (coordinator *Coordinator) SubmitExport(work ExportWork) error {
	if work.ID == 0 || work.Destination == 0 ||
		!work.Input.Ready() ||
		work.Format != review.ExportMarkdown && work.Format != review.ExportJSON ||
		work.Format == review.ExportMarkdown && !work.Templates.Ready() {
		return errors.New("invalid export work")
	}
	requestBytes := work.Input.RetainedBytes()
	if work.Format == review.ExportMarkdown {
		requestBytes += work.Templates.RetainedBytes()
	}
	return coordinator.submit(
		work.ID,
		requestBytes,
		review.MaxPreparedExportBytes,
		func(ctx context.Context) bool {
			select {
			case coordinator.exportJobs <- exportJob{work: work, ctx: ctx}:
				return true
			default:
				return false
			}
		},
	)
}

// SubmitDelivery performs the submit delivery operation.
func (coordinator *Coordinator) SubmitDelivery(work DeliveryWork) error {
	if work.ID == 0 || work.Destination != delivery.DestinationClipboard ||
		work.Prepared.Format() == 0 {
		return errors.New("invalid delivery work")
	}
	return coordinator.submit(
		work.ID,
		work.Prepared.Bytes(),
		64<<10,
		func(ctx context.Context) bool {
			select {
			case coordinator.deliveryJobs <- deliveryJob{work: work, ctx: ctx}:
				return true
			default:
				return false
			}
		},
	)
}

func (coordinator *Coordinator) submit(
	id review.OperationID,
	requestBytes, resultBytes int64,
	enqueue func(context.Context) bool,
) error {
	if requestBytes < 0 || resultBytes < 0 {
		return errors.New("invalid work reservation")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return ErrCoordinatorClosed
	}
	if _, exists := coordinator.accepted[id]; exists {
		return fmt.Errorf("operation %d is already accepted", id)
	}
	// One slot remains reserved for publication or reconciliation.
	if len(coordinator.accepted) >= MaxAcceptedOperations-1 ||
		coordinator.requestBytes+requestBytes >
			MaxQueuedRequestBytes-reservedPublicationRequestBytes ||
		coordinator.reservedResult+resultBytes >
			MaxRetainedResultBytes-reservedPublicationResultBytes {
		return ErrCoordinatorSaturated
	}
	operationContext, cancel := context.WithCancel(coordinator.workContext)
	coordinator.accepted[id] = reservation{
		requestBytes: requestBytes,
		resultBytes:  resultBytes,
		cancel:       cancel,
	}
	coordinator.requestBytes += requestBytes
	coordinator.reservedResult += resultBytes
	coordinator.required.register(id)
	if enqueue(operationContext) {
		coordinator.updateStatsLocked()
		return nil
	}
	delete(coordinator.accepted, id)
	coordinator.requestBytes -= requestBytes
	coordinator.reservedResult -= resultBytes
	coordinator.required.unregister(id)
	cancel()
	coordinator.updateStatsLocked()
	return ErrCoordinatorSaturated
}

// Cancel performs the cancel operation.
func (coordinator *Coordinator) Cancel(id review.OperationID) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	acceptedReservation, exists := coordinator.accepted[id]
	if !exists {
		return false
	}
	if acceptedReservation.cancelOperation != nil {
		acceptedReservation.cancelOperation()
	}
	acceptedReservation.cancel()
	return true
}

// WaitReady performs the wait ready operation.
func (coordinator *Coordinator) WaitReady(ctx context.Context) ReadyState {
	select {
	case _, ok := <-coordinator.ready:
		if !ok {
			return ReadyClosed
		}
		return ReadyAvailable
	case <-ctx.Done():
		return ReadyCancelled
	}
}

// ClaimNext returns the receiver's claim next value.
func (coordinator *Coordinator) ClaimNext() (Outcome, bool) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.outcomes) == 0 {
		if len(coordinator.progressOrder) != 0 {
			id := coordinator.progressOrder[0]
			copy(coordinator.progressOrder, coordinator.progressOrder[1:])
			coordinator.progressOrder =
				coordinator.progressOrder[:len(coordinator.progressOrder)-1]
			progress := coordinator.progress[id]
			delete(coordinator.progress, id)
			coordinator.progressBytes -= progressRetainedBytes(progress)
			if len(coordinator.progressOrder) != 0 {
				coordinator.progressDirty = true
			}
			return progress, true
		}
		coordinator.closeReadyIfFinishedLocked()
		return nil, false
	}
	outcome := coordinator.outcomes[0]
	coordinator.outcomes = slices.Delete(coordinator.outcomes, 0, 1)
	id := outcome.operationID()
	if acceptedReservation, exists := coordinator.accepted[id]; exists {
		if id == coordinator.publicationActive {
			coordinator.publicationActive = 0
		}
		acceptedReservation.cancel()
		coordinator.requestBytes -= acceptedReservation.requestBytes
		coordinator.reservedResult -= acceptedReservation.resultBytes
		delete(coordinator.accepted, id)
	}
	if len(coordinator.outcomes) != 0 || len(coordinator.progressOrder) != 0 {
		coordinator.signalReadyLocked()
	}
	coordinator.updateStatsLocked()
	coordinator.closeReadyIfFinishedLocked()
	return outcome, true
}

func (coordinator *Coordinator) runPatchWorker() {
	for job := range coordinator.patchJobs {
		coordinator.required.wait(job.work.ID)
		result, err := coordinator.service.LoadPatch(job.ctx, job.work.Document, job.work.Request)
		coordinator.complete(PatchOutcome{
			ID:     job.work.ID,
			Result: result,
			Err:    err,
		})
	}
}

func (coordinator *Coordinator) runSearchWorker() {
	for job := range coordinator.searchJobs {
		coordinator.required.wait(job.work.ID)
		result, err := coordinator.executeSearch(job)
		coordinator.complete(SearchOutcome{
			ID:     job.work.ID,
			Result: result,
			Err:    err,
		})
	}
}

func (coordinator *Coordinator) executeSearch(job searchJob) (review.SearchResult, error) {
	request := job.work.Request
	snapshot := job.work.Document.Snapshot()
	if snapshot.SourceID() != request.Source {
		return review.SearchResult{}, errors.New("search source revision is stale")
	}
	query := review.PrepareSearchQuery(request.Query)
	hits := make([]review.SearchHit, 0, 256)
	var unavailable uint32
	status := review.SearchComplete
	total := request.Target.FileCount()
	retainedBytes := int64(0)
	fileIndex := 0
	for fileID := range request.Target.Files() {
		file, ok := snapshot.File(fileID)
		if !ok {
			return review.SearchResult{}, errors.New("search file is unavailable")
		}
		currentIndex := fileIndex
		fileIndex++
		coordinator.reportProgress(Progress{
			ID:        job.work.ID,
			Completed: uint64FromInt(currentIndex),
			Total:     uint64FromInt(total),
			Detail:    boundedOperationDetail("searching changed files"),
		})
		if err := context.Cause(job.ctx); err != nil {
			status = review.SearchCancelled
			hits = nil
			break
		}
		patchResult, err := coordinator.service.LoadPatch(job.ctx, job.work.Document, review.PatchRequest{
			Workspace: request.Workspace,
			Handle:    file.PatchHandle(),
			Context:   request.Context,
		})
		if err != nil {
			if context.Cause(job.ctx) != nil {
				status = review.SearchCancelled
				hits = nil
				break
			}
			unavailable++
			continue
		}
		if patchResult.Status != review.PatchReady || patchResult.Prepared == nil {
			if patchResult.Status == review.PatchCancelled {
				status = review.SearchCancelled
				hits = nil
				break
			}
			unavailable++
			continue
		}
		var cursor review.SearchCursor
		for {
			if err := context.Cause(job.ctx); err != nil {
				status = review.SearchCancelled
				hits = nil
				break
			}
			if !coordinator.acquireCPU(job.ctx) {
				status = review.SearchCancelled
				hits = nil
				break
			}
			batch := review.ScanSearchBatch(
				patchResult.Prepared,
				file.ID(),
				query,
				cursor,
			)
			coordinator.releaseCPU()
			cursor = batch.Cursor
			for _, hit := range batch.Hits {
				hitBytes := hit.RetainedBytes()
				if len(hits) == review.MaxSearchHits ||
					retainedBytes+hitBytes > review.MaxSearchHitBytes {
					status = review.SearchLimited
					break
				}
				hits = append(hits, hit)
				retainedBytes += hitBytes
			}
			coordinator.reportProgress(Progress{
				ID:        job.work.ID,
				Completed: uint64FromInt(currentIndex),
				Total:     uint64FromInt(total),
				Detail:    boundedOperationDetail(fmt.Sprintf("%d matches retained", len(hits))),
			})
			if batch.Done {
				break
			}
			if status == review.SearchLimited {
				break
			}
		}
		if status == review.SearchCancelled || status == review.SearchLimited {
			break
		}
	}
	coordinator.reportProgress(Progress{
		ID:        job.work.ID,
		Completed: uint64FromInt(total),
		Total:     uint64FromInt(total),
		Detail:    boundedOperationDetail("search scan complete"),
	})
	return finishSearchResult(request, status, unavailable, hits)
}

func finishSearchResult(
	request review.SearchRequest,
	status review.SearchStatus,
	unavailable uint32,
	hits []review.SearchHit,
) (review.SearchResult, error) {
	var detail review.OperationDetail
	switch {
	case status == review.SearchCancelled:
		detail = boundedOperationDetail("search cancelled")
	case status == review.SearchLimited:
		detail = boundedOperationDetail("search result capacity reached")
	case unavailable != 0:
		status = review.SearchPartial
		detail = boundedOperationDetail(fmt.Sprintf("%d files unavailable", unavailable))
	}
	return review.TakeSearchResult(review.SearchResultDescriptor{
		Operation:        request.Operation,
		Workspace:        request.Workspace,
		Source:           request.Source,
		Status:           status,
		UnavailableFiles: unavailable,
		Detail:           detail,
	}, hits)
}

func (coordinator *Coordinator) runReloadWorker() {
	reloadJobs := coordinator.reloadJobs
	retireDocuments := coordinator.retireDocuments
	for reloadJobs != nil || retireDocuments != nil {
		select {
		case job, ok := <-reloadJobs:
			if !ok {
				reloadJobs = nil
				continue
			}
			coordinator.required.wait(job.work.ID)
			coordinator.reportProgress(Progress{
				ID:     job.work.ID,
				Total:  2,
				Detail: boundedOperationDetail("acquiring source revision"),
			})
			document, err := coordinator.service.Reload(
				job.ctx,
				job.work.Document,
				job.work.Options,
			)
			coordinator.complete(ReloadAcquiredOutcome{
				ID:       job.work.ID,
				Document: document,
				Err:      err,
			})
		case document, ok := <-retireDocuments:
			if !ok {
				retireDocuments = nil
				continue
			}
			if err := document.Close(); err != nil {
				coordinator.mu.Lock()
				if len(coordinator.cleanupErrors) < MaxAcceptedOperations {
					coordinator.cleanupErrors = append(
						coordinator.cleanupErrors,
						fmt.Errorf("release source document: %w", err),
					)
				} else {
					coordinator.cleanupOmitted++
				}
				coordinator.mu.Unlock()
			}
		}
	}
}

func (coordinator *Coordinator) complete(outcome Outcome) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	id := outcome.operationID()
	acceptedReservation, exists := coordinator.accepted[id]
	if !exists || acceptedReservation.completed {
		return
	}
	acceptedReservation.completed = true
	coordinator.accepted[id] = acceptedReservation
	if progress, exists := coordinator.progress[id]; exists {
		delete(coordinator.progress, id)
		coordinator.progressBytes -= progressRetainedBytes(progress)
		index := slices.Index(coordinator.progressOrder, id)
		if index >= 0 {
			coordinator.progressOrder = slices.Delete(coordinator.progressOrder, index, index+1)
		}
	}
	coordinator.outcomes = append(coordinator.outcomes, outcome)
	coordinator.updateStatsLocked()
	coordinator.signalReadyLocked()
}

func (coordinator *Coordinator) monitorWorkers() {
	coordinator.workers.Wait()
	coordinator.mu.Lock()
	coordinator.workersDone = true
	coordinator.closeReadyIfFinishedLocked()
	coordinator.mu.Unlock()
	close(coordinator.done)
}

func (coordinator *Coordinator) signalReadyLocked() {
	select {
	case coordinator.ready <- struct{}{}:
	default:
	}
}

func (coordinator *Coordinator) closeReadyIfFinishedLocked() {
	if coordinator.closed && coordinator.workersDone && len(coordinator.outcomes) == 0 {
		coordinator.closeReadyLocked()
	}
}

func (coordinator *Coordinator) closeReadyLocked() {
	if coordinator.readyClosed {
		return
	}
	coordinator.readyClosed = true
	close(coordinator.ready)
}
