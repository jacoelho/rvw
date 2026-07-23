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
	// MaxRetainedResultBytes bounds retained and in-construction operation results.
	MaxRetainedResultBytes    int64 = 128 << 20
	MaxIOWorkers                    = 8
	MaxCPUWorkers                   = 8
	MaxPresentationCacheBytes int64 = 128 << 20

	// Reconciliation can retain the captured response, one maximum decoded
	// token, a response-implied duplicate-ID set, and bounded command error.
	publicationResultReservation int64 = 4*review.MaxPublicationBytes +
		source.MaxCommandErrorConstructionBytes
	patchResultReservation             int64 = source.MaxPatchResultConstructionBytes
	reloadResultReservation            int64 = MaxRetainedResultBytes
	reloadAcquisitionResultReservation int64 = MaxRetainedResultBytes
	fileTreeResultReservation          int64 = 16 << 20
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

// PublicationCheckOutcome describes publication check outcome.
type PublicationCheckOutcome struct {
	Err   error
	Check review.PublicationCheckResult
	ID    review.OperationID
}

// PublicationCommitOutcome describes publication commit outcome.
type PublicationCommitOutcome struct {
	Err         error
	Detail      review.OperationDetail
	Publication review.PreparedPublicationResult
	ID          review.OperationID
	Disposition review.PublicationDisposition
}

// ReconciliationOutcome describes reconciliation outcome.
type ReconciliationOutcome struct {
	Err            error
	Detail         review.OperationDetail
	Reconciliation review.PreparedReconciliationResult
	ID             review.OperationID
	Disposition    review.ReconciliationDisposition
}

// ExportWork represents export work.
type ExportWork struct {
	Templates   review.ExportTemplates
	Source      review.ExportSource
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
	commentMutationJobs   chan commentMutationJob
	progressStop          chan struct{}
	reloadPreparationJobs chan reloadPreparationJob
	done                  chan struct{}
	cpuPermits            chan struct{}
	accepted              map[review.OperationID]reservation
	retireCleanups        chan *source.Cleanup
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
	collapseCapture       review.OperationID
	cleanupOmitted        uint64
	progressBytes         int64
	collapseCaptureBytes  int64
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
		commentMutationJobs:   make(chan commentMutationJob, 1),
		exportJobs:            make(chan exportJob, MaxAcceptedOperations),
		deliveryJobs:          make(chan deliveryJob, 1),
		retireCleanups:        make(chan *source.Cleanup, MaxAcceptedOperations),
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
		work.Options.RetainedBytes(),
		reloadAcquisitionResultReservation,
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
	collapseBytes, err := work.Collapse.requestBytes()
	if err != nil {
		return err
	}
	requestBytes := work.Review.RetainedCommentBytes() + collapseBytes +
		collapseRequestStateCharge
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
		len(work.Tree.nodes) == 0 || work.Tree.projection != nil ||
		len(work.Files) > review.MaxFiles {
		return errors.New("invalid file tree work")
	}
	requestBytes := int64(cap(work.Files))*4 + 128
	return coordinator.submit(
		work.ID,
		requestBytes,
		fileTreeResultReservation,
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
	resultBytes, err := workspaceIndexAllowance(work.Snapshot)
	if err != nil {
		return err
	}
	collapseBytes, err := work.Collapse.requestBytes()
	if err != nil {
		return err
	}
	return coordinator.submit(
		work.ID,
		collapseBytes+collapseRequestStateCharge,
		resultBytes,
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

func (coordinator *Coordinator) reserveCollapseCapture(
	id review.OperationID,
	requestBytes int64,
) error {
	if id == 0 || requestBytes < 0 || requestBytes > MaxQueuedRequestBytes {
		return errors.New("invalid collapse capture reservation")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return ErrCoordinatorClosed
	}
	if coordinator.collapseCapture != 0 && coordinator.collapseCapture != id {
		return ErrCoordinatorSaturated
	}
	if _, exists := coordinator.accepted[id]; exists {
		return fmt.Errorf("operation %d is already accepted", id)
	}
	previous := coordinator.collapseCaptureBytes
	if coordinator.collapseCapture == 0 && len(coordinator.accepted) >= MaxAcceptedOperations-1 ||
		coordinator.requestBytes-previous+requestBytes > MaxQueuedRequestBytes {
		return ErrCoordinatorSaturated
	}
	coordinator.collapseCapture = id
	coordinator.collapseCaptureBytes = requestBytes
	coordinator.requestBytes += requestBytes - previous
	coordinator.updateStatsLocked()
	return nil
}

func (coordinator *Coordinator) releaseCollapseCapture(id review.OperationID) {
	if id == 0 {
		return
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.collapseCapture != id {
		return
	}
	coordinator.requestBytes -= coordinator.collapseCaptureBytes
	coordinator.collapseCapture = 0
	coordinator.collapseCaptureBytes = 0
	coordinator.updateStatsLocked()
}

// ReleaseCleanup admits detached source cleanup without retaining a document graph.
func (coordinator *Coordinator) ReleaseCleanup(cleanup *source.Cleanup) error {
	if cleanup == nil {
		return errors.New("cannot release nil source cleanup")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return ErrCoordinatorClosed
	}
	select {
	case coordinator.retireCleanups <- cleanup:
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

// SubmitExport performs the submit export operation.
func (coordinator *Coordinator) SubmitExport(work ExportWork) error {
	if work.ID == 0 || work.Destination == 0 ||
		!work.Source.Ready() ||
		work.Format != review.ExportMarkdown && work.Format != review.ExportJSON ||
		work.Format == review.ExportMarkdown && !work.Templates.Ready() {
		return errors.New("invalid export work")
	}
	requestBytes := work.Source.RetainedBytes()
	if work.Format == review.ExportMarkdown {
		requestBytes += work.Templates.RetainedBytes()
	}
	return coordinator.submit(
		work.ID,
		requestBytes,
		work.Source.ConstructionBytes(),
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
		work.Prepared.Bytes()+delivery.MaxClipboardStdoutBytes+
			delivery.MaxClipboardErrorConstructionBytes,
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
	captured := coordinator.collapseCapture == id
	if captured && coordinator.collapseCaptureBytes != requestBytes {
		return errors.New("collapse capture reservation does not match submitted work")
	}
	active := len(coordinator.accepted)
	if coordinator.collapseCapture != 0 {
		active++
	}
	requestIncrement := requestBytes
	if captured {
		requestIncrement = 0
	}
	// One slot remains reserved for publication or reconciliation.
	if active >= MaxAcceptedOperations || !captured && active >= MaxAcceptedOperations-1 ||
		coordinator.requestBytes+requestIncrement > MaxQueuedRequestBytes ||
		coordinator.reservedResult+resultBytes > MaxRetainedResultBytes {
		if captured {
			coordinator.requestBytes -= coordinator.collapseCaptureBytes
			coordinator.collapseCapture = 0
			coordinator.collapseCaptureBytes = 0
			coordinator.updateStatsLocked()
		}
		return ErrCoordinatorSaturated
	}
	if captured {
		coordinator.collapseCapture = 0
		coordinator.collapseCaptureBytes = 0
	}
	operationContext, cancel := context.WithCancel(coordinator.workContext)
	coordinator.accepted[id] = reservation{
		requestBytes: requestBytes,
		resultBytes:  resultBytes,
		cancel:       cancel,
	}
	coordinator.requestBytes += requestIncrement
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

func (coordinator *Coordinator) growResultReservation(
	id review.OperationID,
	resultBytes int64,
) error {
	if id == 0 || resultBytes < 0 || resultBytes > MaxRetainedResultBytes {
		return ErrCoordinatorSaturated
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	accepted, exists := coordinator.accepted[id]
	if !exists || accepted.completed {
		return ErrCoordinatorClosed
	}
	if resultBytes <= accepted.resultBytes {
		return nil
	}
	increment := resultBytes - accepted.resultBytes
	if increment > MaxRetainedResultBytes-coordinator.reservedResult {
		return ErrCoordinatorSaturated
	}
	accepted.resultBytes = resultBytes
	coordinator.accepted[id] = accepted
	coordinator.reservedResult += increment
	coordinator.updateStatsLocked()
	return nil
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
		acquisition, err := coordinator.service.AcquirePatch(
			job.ctx,
			job.work.Document,
			job.work.Request,
		)
		var result review.PatchResult
		if err == nil {
			result, err = acquisition.Finish(job.ctx, coordinator.runCPU)
		}
		acquisition.Close()
		coordinator.complete(PatchOutcome{
			ID:     job.work.ID,
			Result: result,
			Err:    err,
		})
	}
}

func (coordinator *Coordinator) runReloadWorker() {
	reloadJobs := coordinator.reloadJobs
	retireCleanups := coordinator.retireCleanups
	for reloadJobs != nil || retireCleanups != nil {
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
				coordinator.runCPU,
			)
			coordinator.complete(ReloadAcquiredOutcome{
				ID:       job.work.ID,
				Document: document,
				Err:      err,
			})
		case cleanup, ok := <-retireCleanups:
			if !ok {
				retireCleanups = nil
				continue
			}
			if err := cleanup.Close(); err != nil {
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
