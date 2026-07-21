package app

import (
	"context"
	"errors"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

type SyntaxWork struct {
	ID       review.OperationID
	Document *source.Document
	Request  review.SyntaxRequest
}

type SyntaxOutcome struct {
	ID     review.OperationID
	Result review.SyntaxResult
	Err    error
}

type syntaxJob struct {
	work SyntaxWork
	ctx  context.Context
}

func (coordinator *Coordinator) SubmitSyntax(work SyntaxWork) error {
	if work.ID == 0 || work.Document == nil ||
		work.Request.Operation != work.ID ||
		work.Request.Source == 0 {
		return errors.New("invalid syntax work")
	}
	var requestBytes int64 = 256
	for _, demand := range work.Request.Demands {
		requestBytes += int64(len(demand.Lines) * 4)
		requestBytes += int64(len(demand.Identity.Selector()))
	}
	return coordinator.submit(
		work.ID,
		requestBytes,
		2*review.MaxBlobBytes+review.MaxSyntaxBytes,
		func(ctx context.Context) bool {
			select {
			case coordinator.syntaxJobs <- syntaxJob{work: work, ctx: ctx}:
				return true
			default:
				return false
			}
		},
	)
}

func (coordinator *Coordinator) runSyntaxWorker() {
	for job := range coordinator.syntaxJobs {
		coordinator.required.wait(job.work.ID)
		_ = coordinator.ReportProgress(Progress{
			ID:     job.work.ID,
			Total:  1,
			Detail: boundedOperationDetail("loading and preparing syntax"),
		})
		var result review.SyntaxResult
		var err error
		if !coordinator.acquireCPU(job.ctx) {
			err = context.Cause(job.ctx)
		} else {
			result, err = coordinator.service.LoadSyntax(
				job.ctx,
				job.work.Document,
				job.work.Request,
			)
			coordinator.releaseCPU()
		}
		coordinator.complete(SyntaxOutcome{
			ID:     job.work.ID,
			Result: result,
			Err:    err,
		})
	}
}
