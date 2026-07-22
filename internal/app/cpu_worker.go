package app

import (
	"context"

	"github.com/jacoelho/rvw/internal/review"
)

func (coordinator *Coordinator) runCPUWorker() {
	exportJobs := coordinator.exportJobs
	reloadJobs := coordinator.reloadPreparationJobs
	fileTreeJobs := coordinator.fileTreeJobs
	workspaceIndexJobs := coordinator.workspaceIndexJobs
	for exportJobs != nil || reloadJobs != nil || fileTreeJobs != nil ||
		workspaceIndexJobs != nil {
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
		}
	}
}

func (coordinator *Coordinator) executeExportCPU(job exportJob) {
	coordinator.required.wait(job.work.ID)
	var prepared review.PreparedExport
	var err error
	coordinator.reportProgress(Progress{
		ID:     job.work.ID,
		Total:  1,
		Detail: boundedOperationDetail("preparing export"),
	})
	if !coordinator.acquireCPU(job.ctx) {
		err = context.Cause(job.ctx)
	} else {
		switch job.work.Format {
		case review.ExportMarkdown:
			prepared, err = job.work.Input.PrepareMarkdownCancellable(
				func() error { return context.Cause(job.ctx) },
				job.work.Templates,
				job.work.Options,
			)
		case review.ExportJSON:
			prepared, err = job.work.Input.PrepareJSONCancellable(
				func() error { return context.Cause(job.ctx) },
				job.work.Options,
			)
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
	coordinator.required.wait(job.work.ID)
	prepared, err := coordinator.prepareReplacementCPU(job)
	var (
		allFiles, visibleFiles       []review.FileID
		allFileTree, visibleFileTree fileTree
		pathFiles                    map[string]review.FileID
		ambiguousPaths               map[string]struct{}
	)
	if err == nil {
		allFiles, visibleFiles, allFileTree, visibleFileTree,
			pathFiles, ambiguousPaths, err = coordinator.prepareReloadIndexesCPU(job)
	}
	coordinator.complete(ReloadPreparedOutcome{
		ID:              job.work.ID,
		Document:        job.work.Document,
		Replacement:     prepared,
		AllFiles:        allFiles,
		VisibleFiles:    visibleFiles,
		AllFileTree:     allFileTree,
		VisibleFileTree: visibleFileTree,
		PathFiles:       pathFiles,
		AmbiguousPaths:  ambiguousPaths,
		Err:             err,
	})
}

func (coordinator *Coordinator) prepareReplacementCPU(
	job reloadPreparationJob,
) (review.PreparedReplacement, error) {
	coordinator.reportProgress(Progress{
		ID:     job.work.ID,
		Total:  2,
		Detail: boundedOperationDetail("reconciling review state"),
	})
	if !coordinator.acquireCPU(job.ctx) {
		return review.PreparedReplacement{}, context.Cause(job.ctx)
	}
	prepared, err := review.PrepareReplacementCancellable(
		func() error { return context.Cause(job.ctx) },
		job.work.Review,
		job.work.Document.Snapshot(),
	)
	coordinator.releaseCPU()
	return prepared, err
}

func (coordinator *Coordinator) prepareReloadIndexesCPU(
	job reloadPreparationJob,
) (
	[]review.FileID,
	[]review.FileID,
	fileTree,
	fileTree,
	map[string]review.FileID,
	map[string]struct{},
	error,
) {
	coordinator.reportProgress(Progress{
		ID:        job.work.ID,
		Completed: 1,
		Total:     2,
		Detail:    boundedOperationDetail("building file indexes"),
	})
	if !coordinator.acquireCPU(job.ctx) {
		return nil, nil, fileTree{}, fileTree{}, nil, nil, context.Cause(job.ctx)
	}
	defer coordinator.releaseCPU()
	snapshot := job.work.Document.Snapshot()
	allFiles, visibleFiles, pathFiles, ambiguousPaths, err :=
		buildFileIndexesContext(job.ctx, snapshot)
	if err != nil {
		return nil, nil, fileTree{}, fileTree{}, nil, nil, err
	}
	allTree, err := buildFileTreeContext(job.ctx, snapshot, allFiles)
	if err != nil {
		return nil, nil, fileTree{}, fileTree{}, nil, nil, err
	}
	visibleTree := allTree
	if len(visibleFiles) != len(allFiles) {
		visibleTree, err = buildFileTreeContext(job.ctx, snapshot, visibleFiles)
	}
	return allFiles, visibleFiles, allTree, visibleTree, pathFiles, ambiguousPaths, err
}

func (coordinator *Coordinator) executeFileTreeCPU(job fileTreeJob) {
	coordinator.required.wait(job.work.ID)
	var tree fileTree
	var err error
	if !coordinator.acquireCPU(job.ctx) {
		err = context.Cause(job.ctx)
	} else {
		tree, err = buildFileTreeContext(job.ctx, job.work.Snapshot, job.work.Files)
		coordinator.releaseCPU()
	}
	coordinator.complete(FileTreeOutcome{
		ID:         job.work.ID,
		Generation: job.work.Generation,
		Files:      job.work.Files,
		Tree:       tree,
		Err:        err,
	})
}

func (coordinator *Coordinator) executeWorkspaceIndexCPU(job workspaceIndexJob) {
	coordinator.required.wait(job.work.ID)
	outcome := WorkspaceIndexOutcome{ID: job.work.ID}
	if !coordinator.acquireCPU(job.ctx) {
		outcome.Err = context.Cause(job.ctx)
	} else {
		prepared, err := buildWorkspaceIndex(job.ctx, job.work.Snapshot)
		coordinator.releaseCPU()
		outcome = prepared
		outcome.ID = job.work.ID
		outcome.Err = err
	}
	coordinator.complete(outcome)
}
