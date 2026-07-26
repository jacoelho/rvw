package tui

import (
	"context"
	"errors"
	"unsafe"

	"github.com/jacoelho/rvw/internal/change"
	workcoord "github.com/jacoelho/rvw/internal/coordinator"
	"github.com/jacoelho/rvw/internal/source"
)

// SyntaxWork represents syntax work.
type SyntaxWork struct {
	Document *workcoord.SourceRevision
	Request  change.SyntaxRequest
	ID       change.OperationID
}

// SyntaxOutcome describes syntax outcome.
type SyntaxOutcome struct {
	Err    error
	Result change.SyntaxResult
	ID     change.OperationID
}

type syntaxJob struct {
	ctx  context.Context
	work SyntaxWork
}

const syntaxResultConstructionBytes = change.MaxBlobBytes +
	3*change.MaxSyntaxBytes + source.MaxSourceErrorConstructionBytes

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
		coordinator.required.Wait(job.work.ID)
		coordinator.reportProgress(Progress{
			ID:     job.work.ID,
			Total:  1,
			Detail: boundedOperationDetail("loading and preparing syntax"),
		})
		opened, revision, available := job.work.Document.Materialization()
		if !available {
			coordinator.complete(SyntaxOutcome{
				ID: job.work.ID, Err: errors.New("syntax source revision is unavailable"),
			})
			continue
		}
		result, err := coordinator.service.LoadSyntaxWithCPUAdmission(
			job.ctx,
			opened,
			revision,
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
