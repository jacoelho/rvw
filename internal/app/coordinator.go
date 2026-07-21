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

const (
	MaxAcceptedOperations       = 64
	MaxQueuedRequestBytes int64 = 32 << 20
	// One search retains one maximum patch plus bounded hits while a
	// presentation patch may complete concurrently.
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

var (
	ErrCoordinatorClosed    = errors.New("work coordinator is closed")
	ErrCoordinatorSaturated = errors.New("work coordinator is saturated")
)

type PatchWork struct {
	ID       review.OperationID
	Document *source.Document
	Request  review.PatchRequest
}

type PatchOutcome struct {
	ID     review.OperationID
	Result review.PatchResult
	Err    error
}

type SearchWork struct {
	ID       review.OperationID
	Document *source.Document
	Request  review.SearchRequest
}

type SearchOutcome struct {
	ID     review.OperationID
	Result review.SearchResult
	Err    error
}

type PublicationCheckOutcome struct {
	ID    review.OperationID
	Check review.PublicationCheckResult
	Err   error
}

type PublicationCommitOutcome struct {
	ID          review.OperationID
	Publication review.PublicationResult
}

type ReconciliationOutcome struct {
	ID             review.OperationID
	Reconciliation review.ReconciliationResult
}

type ExportWork struct {
	ID          review.OperationID
	Input       review.ExportInput
	Templates   review.ExportTemplates
	Options     review.ExportOptions
	Format      review.ExportFormat
	Destination delivery.Destination
}

type ExportOutcome struct {
	ID          review.OperationID
	Prepared    review.PreparedExport
	Destination delivery.Destination
	Err         error
}

type DeliveryWork struct {
	ID          review.OperationID
	Prepared    review.PreparedExport
	Destination delivery.Destination
}

type DeliveryOutcome struct {
	ID      review.OperationID
	Outcome delivery.Outcome
	Err     error
}

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

type ReadyState uint8

const (
	ReadyAvailable ReadyState = iota + 1
	ReadyClosed
	ReadyCancelled
)

type reservation struct {
	requestBytes    int64
	resultBytes     int64
	cancel          context.CancelFunc
	cancelOperation func()
	completed       bool
	publication     *publicationReservation
}

type publicationReservation struct {
	target review.PublicationTarget
	marker review.PublicationMarker
}

type patchJob struct {
	work PatchWork
	ctx  context.Context
}

type searchJob struct {
	work SearchWork
	ctx  context.Context
}

type reloadJob struct {
	work ReloadWork
	ctx  context.Context
}

type reloadPreparationJob struct {
	work ReloadPreparationWork
	ctx  context.Context
}

type fileTreeJob struct {
	work FileTreeWork
	ctx  context.Context
}

type workspaceIndexJob struct {
	work WorkspaceIndexWork
	ctx  context.Context
}

type exportJob struct {
	work ExportWork
	ctx  context.Context
}

type deliveryJob struct {
	work DeliveryWork
	ctx  context.Context
}

type Coordinator struct {
	workContext context.Context
	service     *source.Service
	delivery    *delivery.Manager

	mu                sync.Mutex
	closed            bool
	workersDone       bool
	readyClosed       bool
	accepted          map[review.OperationID]reservation
	requestBytes      int64
	reservedResult    int64
	outcomes          []Outcome
	progress          map[review.OperationID]Progress
	progressOrder     []review.OperationID
	progressBytes     int64
	progressDirty     bool
	required          *requiredScheduler
	cleanupErrors     []error
	cleanupOmitted    uint64
	publicationActive review.OperationID

	ready                 chan struct{}
	patchJobs             chan patchJob
	searchJobs            chan searchJob
	syntaxJobs            chan syntaxJob
	publicationJobs       chan publicationJob
	reloadJobs            chan reloadJob
	reloadPreparationJobs chan reloadPreparationJob
	fileTreeJobs          chan fileTreeJob
	workspaceIndexJobs    chan workspaceIndexJob
	exportJobs            chan exportJob
	deliveryJobs          chan deliveryJob
	retireDocuments       chan *source.Document
	progressStop          chan struct{}
	cpuPermits            chan struct{}
	done                  chan struct{}

	workers  sync.WaitGroup
	ioCount  int
	cpuCount int
	stats    atomic.Value
}

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

func (coordinator *Coordinator) SubmitSearch(work SearchWork) error {
	if work.ID == 0 || work.Document == nil ||
		work.Request.Operation != work.ID ||
		work.Request.Source == 0 ||
		work.Request.Query == "" {
		return errors.New("invalid search work")
	}
	requestBytes := int64(len(work.Request.Query)) + 128
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

func (coordinator *Coordinator) Cancel(id review.OperationID) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	reservation, exists := coordinator.accepted[id]
	if !exists {
		return false
	}
	if reservation.cancelOperation != nil {
		reservation.cancelOperation()
	}
	reservation.cancel()
	return true
}

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
	if reservation, exists := coordinator.accepted[id]; exists {
		if id == coordinator.publicationActive {
			coordinator.publicationActive = 0
		}
		reservation.cancel()
		coordinator.requestBytes -= reservation.requestBytes
		coordinator.reservedResult -= reservation.resultBytes
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
	files := snapshot.AllFiles()
	total := snapshot.FileCount()
	if request.Scope == review.SearchCurrentFile {
		file, ok := snapshot.File(request.File)
		if !ok {
			return review.SearchResult{}, errors.New("search file is unavailable")
		}
		files = func(yield func(review.File) bool) {
			yield(file)
		}
		total = 1
	}
	fileIndex := 0
	for file := range files {
		currentIndex := fileIndex
		fileIndex++
		_ = coordinator.ReportProgress(Progress{
			ID:        job.work.ID,
			Completed: uint64(currentIndex),
			Total:     uint64(total),
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
			if len(hits)+len(batch.Hits) > review.MaxSearchHits {
				status = review.SearchLimited
				break
			}
			hits = append(hits, batch.Hits...)
			_ = coordinator.ReportProgress(Progress{
				ID:        job.work.ID,
				Completed: uint64(currentIndex),
				Total:     uint64(total),
				Detail:    boundedOperationDetail(fmt.Sprintf("%d matches retained", len(hits))),
			})
			if batch.Done {
				break
			}
		}
		if status == review.SearchCancelled || status == review.SearchLimited {
			break
		}
	}
	_ = coordinator.ReportProgress(Progress{
		ID:        job.work.ID,
		Completed: uint64(total),
		Total:     uint64(total),
		Detail:    boundedOperationDetail("search scan complete"),
	})
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
			_ = coordinator.ReportProgress(Progress{
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
	reservation, exists := coordinator.accepted[id]
	if !exists || reservation.completed {
		return
	}
	reservation.completed = true
	coordinator.accepted[id] = reservation
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
