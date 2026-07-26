package tui

import (
	"context"
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/projection"
)

func (coordinator *Coordinator) runCPUWorker() {
	exportJobs := coordinator.exportJobs
	reloadJobs := coordinator.reloadPreparationJobs
	fileTreeJobs := coordinator.fileTreeJobs
	workspaceIndexJobs := coordinator.workspaceIndexJobs
	commentJobs := coordinator.commentMutationJobs
	for exportJobs != nil || reloadJobs != nil || fileTreeJobs != nil ||
		workspaceIndexJobs != nil || commentJobs != nil {
		select {
		case job, ok := <-exportJobs:
			if !ok {
				exportJobs = nil
				continue
			}
			coordinator.executeExportCPU(job)
		case job, ok := <-reloadJobs:
			if !ok {
				reloadJobs = nil
				continue
			}
			coordinator.executeReloadPreparationCPU(job)
		case job, ok := <-fileTreeJobs:
			if !ok {
				fileTreeJobs = nil
				continue
			}
			coordinator.executeFileTreeCPU(job)
		case job, ok := <-workspaceIndexJobs:
			if !ok {
				workspaceIndexJobs = nil
				continue
			}
			coordinator.executeWorkspaceIndexCPU(job)
		case job, ok := <-commentJobs:
			if !ok {
				commentJobs = nil
				continue
			}
			coordinator.executeCommentMutationCPU(job)
		}
	}
}

func (coordinator *Coordinator) executeExportCPU(job exportJob) {
	coordinator.required.Wait(job.work.ID)
	var prepared change.PreparedExport
	var err error
	coordinator.reportProgress(Progress{
		ID:     job.work.ID,
		Total:  1,
		Detail: boundedOperationDetail("preparing export"),
	})
	if !coordinator.acquireCPU(job.ctx) {
		err = context.Cause(job.ctx)
	} else {
		cancelled := func() error { return context.Cause(job.ctx) }
		var input change.ExportInput
		input, err = job.work.Source.ProjectCancellable(cancelled)
		job.work.Source = change.ExportSource{}
		if err == nil {
			switch job.work.Format {
			case change.ExportMarkdown:
				prepared, err = input.PrepareMarkdownCancellable(
					cancelled,
					job.work.Templates,
					job.work.Options,
				)
			case change.ExportJSON:
				prepared, err = input.PrepareJSONCancellable(
					cancelled,
					job.work.Options,
				)
			}
		}
		coordinator.releaseCPU()
	}
	coordinator.complete(ExportOutcome{
		ID:          job.work.ID,
		Prepared:    prepared,
		Destination: job.work.Destination,
		Err:         err,
	})
}

func (coordinator *Coordinator) executeReloadPreparationCPU(job reloadPreparationJob) {
	coordinator.required.Wait(job.work.ID)
	snapshot := job.work.Document.Snapshot()
	ledger, err := projection.NewLedger(MaxRetainedResultBytes, snapshot.RetainedBytes())
	var prepared change.PreparedReplacement
	if err == nil {
		prepared, err = coordinator.prepareReplacementCPU(job, ledger)
	}
	var index WorkspaceIndexOutcome
	if err == nil {
		index, err = coordinator.prepareReloadIndexesCPU(job, ledger)
	}
	if err != nil {
		prepared = change.PreparedReplacement{}
		index = WorkspaceIndexOutcome{}
	}
	outcome := ReloadPreparedOutcome{
		ID:                job.work.ID,
		Document:          job.work.Document,
		Replacement:       prepared,
		FileTree:          index.FileTree,
		VisibleProjection: index.VisibleProjection,
		Collapsed:         index.Collapsed,
		Index:             index.Index,
		Err:               err,
	}
	if outcome.Err == nil && outcome.RetainedBytes() > MaxRetainedResultBytes {
		outcome = ReloadPreparedOutcome{
			ID:       job.work.ID,
			Document: job.work.Document,
			Err: fmt.Errorf(
				"%w: reload preparation retains %d bytes; limit is %d",
				projection.ErrRetainedLimited,
				outcome.RetainedBytes(),
				MaxRetainedResultBytes,
			),
		}
	}
	coordinator.complete(outcome)
}

func (coordinator *Coordinator) prepareReplacementCPU(
	job reloadPreparationJob,
	ledger *projection.Ledger,
) (change.PreparedReplacement, error) {
	coordinator.reportProgress(Progress{
		ID:     job.work.ID,
		Total:  2,
		Detail: boundedOperationDetail("reconciling review state"),
	})
	if !coordinator.acquireCPU(job.ctx) {
		return change.PreparedReplacement{}, context.Cause(job.ctx)
	}
	constructionBytes := change.ReplacementConstructionBytes(
		job.work.Review,
		job.work.Document.Snapshot(),
	)
	if err := ledger.Reserve(constructionBytes); err != nil {
		coordinator.releaseCPU()
		return change.PreparedReplacement{}, err
	}
	prepared, err := change.PrepareReplacementCancellable(
		func() error { return context.Cause(job.ctx) },
		job.work.Review,
		job.work.Document.Snapshot(),
	)
	coordinator.releaseCPU()
	if err != nil {
		ledger.Release(constructionBytes)
		return change.PreparedReplacement{}, err
	}
	retainedBytes := prepared.RetainedBytes()
	if retainedBytes > constructionBytes {
		ledger.Release(constructionBytes)
		return change.PreparedReplacement{}, errors.New("reload replacement construction accounting is incomplete")
	}
	ledger.Release(constructionBytes - retainedBytes)
	return prepared, err
}

func (coordinator *Coordinator) prepareReloadIndexesCPU(
	job reloadPreparationJob,
	ledger *projection.Ledger,
) (WorkspaceIndexOutcome, error) {
	coordinator.reportProgress(Progress{
		ID:        job.work.ID,
		Completed: 1,
		Total:     2,
		Detail:    boundedOperationDetail("building file indexes"),
	})
	if !coordinator.acquireCPU(job.ctx) {
		return WorkspaceIndexOutcome{}, context.Cause(job.ctx)
	}
	defer coordinator.releaseCPU()
	snapshot := job.work.Document.Snapshot()
	return buildWorkspaceIndexWithLedger(job.ctx, snapshot, job.work.Collapse, ledger)
}

func (coordinator *Coordinator) executeFileTreeCPU(job fileTreeJob) {
	coordinator.required.Wait(job.work.ID)
	var projection treeProjection
	var err error
	if !coordinator.acquireCPU(job.ctx) {
		err = context.Cause(job.ctx)
	} else {
		projection, err = job.work.Tree.project(job.ctx, job.work.Files)
		coordinator.releaseCPU()
	}
	coordinator.complete(FileTreeOutcome{
		ID:         job.work.ID,
		Generation: job.work.Generation,
		Files:      job.work.Files,
		Projection: projection,
		Err:        err,
	})
}

func (coordinator *Coordinator) executeWorkspaceIndexCPU(job workspaceIndexJob) {
	coordinator.required.Wait(job.work.ID)
	outcome := WorkspaceIndexOutcome{ID: job.work.ID}
	if !coordinator.acquireCPU(job.ctx) {
		outcome.Err = context.Cause(job.ctx)
	} else {
		ledger, ledgerErr := projection.NewLedger(
			MaxRetainedResultBytes,
			job.work.Snapshot.RetainedBytes(),
		)
		var prepared WorkspaceIndexOutcome
		var err error
		if ledgerErr != nil {
			err = ledgerErr
		} else {
			prepared, err = buildWorkspaceIndexWithLedger(
				job.ctx,
				job.work.Snapshot,
				job.work.Collapse,
				ledger,
			)
		}
		coordinator.releaseCPU()
		outcome = prepared
		outcome.ID = job.work.ID
		outcome.Err = err
	}
	coordinator.complete(outcome)
}
