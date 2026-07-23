package app

import (
	"context"
	"errors"
	"unsafe"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// SyntaxWork represents syntax work.
type SyntaxWork struct {
	Document *source.Document
	Request  review.SyntaxRequest
	ID       review.OperationID
}

// SyntaxOutcome describes syntax outcome.
type SyntaxOutcome struct {
	Err    error
	Result review.SyntaxResult
	ID     review.OperationID
}

type syntaxJob struct {
	ctx  context.Context
	work SyntaxWork
}

const syntaxResultConstructionBytes = review.MaxBlobBytes +
	3*review.MaxSyntaxBytes + source.MaxCommandErrorConstructionBytes

func (work SyntaxWork) requestBytes() int64 {
	requestBytes := work.Request.RetainedBytes()
	if requestBytes == 0 {
		return 0
	}
	return int64(unsafe.Sizeof(work)-unsafe.Sizeof(work.Request)) + requestBytes
}

// SubmitSyntax performs the submit syntax operation.
func (coordinator *Coordinator) SubmitSyntax(work SyntaxWork) error {
	if work.ID == 0 || work.Document == nil ||
		work.Request.Operation != work.ID ||
		work.Request.Source == 0 {
		return errors.New("invalid syntax work")
	}
	requestBytes := work.requestBytes()
	if requestBytes == 0 {
		return errors.New("invalid syntax work")
	}
	return coordinator.submit(
		work.ID,
		requestBytes,
		// Demands are loaded sequentially. The peak is the first terminal
		// result, one blob, and the old-plus-new growth peak of the second.
		syntaxResultConstructionBytes,
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
		coordinator.reportProgress(Progress{
			ID:     job.work.ID,
			Total:  1,
			Detail: boundedOperationDetail("loading and preparing syntax"),
		})
		result, err := coordinator.service.LoadSyntaxWithCPUAdmission(
			job.ctx,
			job.work.Document,
			job.work.Request,
			coordinator.runCPU,
		)
		coordinator.complete(SyntaxOutcome{
			ID:     job.work.ID,
			Result: result,
			Err:    err,
		})
	}
}
