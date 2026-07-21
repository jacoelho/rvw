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
			coordinator.required.wait(job.work.ID)
			var prepared review.PreparedExport
			var err error
			_ = coordinator.ReportProgress(Progress{
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
		case job, ok := <-reloadJobs:
			if !ok {
				reloadJobs = nil
				continue
			}
			coordinator.required.wait(job.work.ID)
			var prepared review.PreparedReplacement
			var err error
			_ = coordinator.ReportProgress(Progress{
				ID:     job.work.ID,
				Total:  2,
				Detail: boundedOperationDetail("reconciling review state"),
			})
			if !coordinator.acquireCPU(job.ctx) {
				err = context.Cause(job.ctx)
			} else {
				prepared, err = review.PrepareReplacementCancellable(
					func() error { return context.Cause(job.ctx) },
					job.work.Review,
					job.work.Document.Snapshot(),
				)
				coordinator.releaseCPU()
			}
			var (
				allFiles, visibleFiles       []review.FileID
				allFileTree, visibleFileTree fileTree
				pathFiles                    map[string]review.FileID
				ambiguousPaths               map[string]struct{}
			)
			if err == nil {
				_ = coordinator.ReportProgress(Progress{
					ID:        job.work.ID,
					Completed: 1,
					Total:     2,
					Detail:    boundedOperationDetail("building file indexes"),
				})
				if !coordinator.acquireCPU(job.ctx) {
					err = context.Cause(job.ctx)
				} else {
					allFiles, visibleFiles, pathFiles, ambiguousPaths, err =
						buildFileIndexesContext(
							job.ctx,
							job.work.Document.Snapshot(),
						)
					if err == nil {
						allFileTree, err = buildFileTreeContext(
							job.ctx,
							job.work.Document.Snapshot(),
							allFiles,
						)
					}
					if err == nil {
						if len(visibleFiles) == len(allFiles) {
							visibleFileTree = allFileTree
						} else {
							visibleFileTree, err = buildFileTreeContext(
								job.ctx,
								job.work.Document.Snapshot(),
								visibleFiles,
							)
						}
					}
					coordinator.releaseCPU()
				}
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
		case job, ok := <-fileTreeJobs:
			if !ok {
				fileTreeJobs = nil
				continue
			}
			coordinator.required.wait(job.work.ID)
			var tree fileTree
			var err error
			if !coordinator.acquireCPU(job.ctx) {
				err = context.Cause(job.ctx)
			} else {
				tree, err = buildFileTreeContext(
					job.ctx,
					job.work.Snapshot,
					job.work.Files,
				)
				coordinator.releaseCPU()
			}
			coordinator.complete(FileTreeOutcome{
				ID:         job.work.ID,
				Generation: job.work.Generation,
				Files:      job.work.Files,
				Tree:       tree,
				Err:        err,
			})
		case job, ok := <-workspaceIndexJobs:
			if !ok {
				workspaceIndexJobs = nil
				continue
			}
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
	}
}
