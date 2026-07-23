package source

import (
	"context"
	"errors"
	"strings"

	"github.com/jacoelho/rvw/internal/review"
)

// PatchAcquisition owns patch bytes acquired from Git until CPU preparation.
type PatchAcquisition struct {
	native  *nativePatchInput
	result  review.PatchResult
	valid   bool
	prepare bool
}

// RequiresPreparation reports whether Finish must run under CPU admission.
func (acquisition *PatchAcquisition) RequiresPreparation() bool {
	return acquisition != nil && acquisition.valid && acquisition.prepare
}

// Close releases unprepared patch bytes.
func (acquisition *PatchAcquisition) Close() {
	if acquisition == nil {
		return
	}
	*acquisition = PatchAcquisition{}
}

// Finish consumes an acquisition and performs only cancellable CPU preparation.
func (acquisition *PatchAcquisition) Finish(
	ctx context.Context,
	runCPU CPUAdmission,
) (review.PatchResult, error) {
	if acquisition == nil || !acquisition.valid {
		return review.PatchResult{}, errors.New("patch acquisition is invalid")
	}
	if runCPU == nil {
		return review.PatchResult{}, errors.New("patch CPU admission is nil")
	}
	result := acquisition.result
	native := acquisition.native
	prepare := acquisition.prepare
	acquisition.Close()
	if !prepare {
		return result, nil
	}
	var prepared *review.PreparedPatch
	var prepareErr error
	admissionErr := runCPU(ctx, func() error {
		if native == nil {
			prepareErr = errors.New("native patch input is unavailable")
			return nil
		}
		prepared, prepareErr = prepareNativePatch(ctx, native)
		return nil
	})
	if admissionErr != nil {
		if !contextCancellation(ctx, admissionErr) {
			return review.PatchResult{}, admissionErr
		}
		prepareErr = admissionErr
	}
	if prepareErr != nil {
		if contextCancellation(ctx, prepareErr) {
			result.Status = review.PatchCancelled
		} else {
			result.Status = review.PatchLimited
		}
		result.Detail = review.PatchDetail(boundedText(prepareErr.Error(), 16<<10))
		return result, nil
	}
	result.Status = review.PatchReady
	result.Prepared = prepared
	return result, nil
}

// LoadPatch loads and prepares a patch without scheduler integration.
func (service *Service) LoadPatch(
	ctx context.Context,
	document *Document,
	request review.PatchRequest,
) (review.PatchResult, error) {
	acquisition, err := service.AcquirePatch(ctx, document, request)
	if err != nil {
		return review.PatchResult{}, err
	}
	defer acquisition.Close()
	return acquisition.Finish(ctx, directCPUAdmission)
}

// AcquirePatch performs validation, filesystem inspection, and Git acquisition only.
func (service *Service) AcquirePatch(
	ctx context.Context,
	document *Document,
	request review.PatchRequest,
) (PatchAcquisition, error) {
	if document == nil {
		return PatchAcquisition{}, errors.New("source document is nil")
	}

	result := review.PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
	}
	if request.Handle.SourceID() != document.snapshot.SourceID() {
		return PatchAcquisition{}, errors.New("patch request has a stale source identity")
	}
	fileID := request.Handle.FileID()
	file, ok := document.snapshot.File(fileID)
	if !ok || file.PatchHandle() != request.Handle {
		return PatchAcquisition{}, errors.New("patch request has an invalid file identity")
	}

	if document.revision.repository == nil {
		return PatchAcquisition{}, errors.New("source document has no native repository revision")
	}
	oldPath, newPath := "", ""
	if path, ok := file.OldPath(); ok {
		oldPath = string(path.Raw())
	}
	if path, ok := file.NewPath(); ok {
		newPath = string(path.Raw())
	}
	if _, conflict := document.revision.unmerged[primaryAvailablePath(oldPath, newPath)]; conflict {
		result.Status = review.PatchUnavailable
		result.Detail = review.PatchDetail("unmerged index path has no single prepared patch")
		return PatchAcquisition{result: result, valid: true}, nil
	}
	native, err := acquireNativePatch(ctx, document, file, request.Context)
	if err != nil {
		if strings.Contains(err.Error(), "submodule") {
			result.Status = review.PatchUnavailable
			result.Detail = review.PatchDetail(boundedText(err.Error(), 16<<10))
			return PatchAcquisition{result: result, valid: true}, nil
		}
		return PatchAcquisition{}, err
	}
	return PatchAcquisition{
		result: result, native: native, valid: true, prepare: true,
	}, nil
}

// SyntaxAcquisition owns one Git blob until CPU preparation completes.
type SyntaxAcquisition struct {
	identity review.SyntaxIdentity
	lines    []review.LineNumber
	blob     []byte
	valid    bool
}

// Close releases an unprepared blob.
func (acquisition *SyntaxAcquisition) Close() {
	if acquisition == nil {
		return
	}
	*acquisition = SyntaxAcquisition{}
}

// Finish consumes a blob and performs only cancellable CPU preparation.
func (acquisition *SyntaxAcquisition) Finish(
	ctx context.Context,
	runCPU CPUAdmission,
) (*review.PreparedSyntax, error) {
	if acquisition == nil || !acquisition.valid {
		return nil, errors.New("syntax acquisition is invalid")
	}
	if runCPU == nil {
		return nil, errors.New("syntax CPU admission is nil")
	}
	identity := acquisition.identity
	lines := acquisition.lines
	blob := acquisition.blob
	acquisition.Close()
	var prepared *review.PreparedSyntax
	var prepareErr error
	if err := runCPU(ctx, func() error {
		prepared, prepareErr = review.PrepareSyntaxCancellable(
			func() error { return context.Cause(ctx) },
			identity,
			lines,
			blob,
		)
		return nil
	}); err != nil {
		return nil, err
	}
	if prepareErr != nil {
		return nil, prepareErr
	}
	return prepared, nil
}

// LoadSyntax loads syntax without scheduler integration.
func (service *Service) LoadSyntax(
	ctx context.Context,
	document *Document,
	request review.SyntaxRequest,
) (review.SyntaxResult, error) {
	return service.LoadSyntaxWithCPUAdmission(ctx, document, request, directCPUAdmission)
}

// LoadSyntaxWithCPUAdmission acquires Git blobs outside CPU admission and
// prepares each acquired blob synchronously under admission.
func (service *Service) LoadSyntaxWithCPUAdmission(
	ctx context.Context,
	document *Document,
	request review.SyntaxRequest,
	runCPU CPUAdmission,
) (review.SyntaxResult, error) {
	result := review.SyntaxResult{
		Operation: request.Operation,
		Workspace: request.Workspace,
		Source:    request.Source,
		File:      request.File,
	}
	if document == nil || request.Operation == 0 ||
		request.Source != document.Snapshot().SourceID() {
		return result, errors.New("syntax request has a stale source identity")
	}
	file, ok := document.Snapshot().File(request.File)
	if !ok || len(request.Demands) == 0 {
		return result, errors.New("syntax request file is unavailable")
	}
	if runCPU == nil {
		return result, errors.New("syntax CPU admission is nil")
	}
	var seenOld, seenNew bool
	for _, demand := range request.Demands {
		content, exists := file.Content(demand.Side)
		if !exists || content != demand.Identity.Content() {
			return result, errors.New("syntax request content identity is stale")
		}
		switch demand.Side {
		case review.LineOld:
			if seenOld {
				return result, errors.New("syntax request repeats old-side demand")
			}
			seenOld = true
		case review.LineNew:
			if seenNew {
				return result, errors.New("syntax request repeats new-side demand")
			}
			seenNew = true
		default:
			return result, errors.New("syntax request side is invalid")
		}
	}
	for _, demand := range request.Demands {
		content := demand.Identity.Content()
		var blob []byte
		var err error
		if document.revision.repository == nil {
			return result, errors.New("source document has no native repository revision")
		}
		blob, err = readNativeRecordSide(ctx, document.revision.repository, &content)
		if err == nil && int64(len(blob)) > review.MaxBlobBytes {
			err = &LimitError{Resource: LimitDiffInputBytes, Limit: uint64(review.MaxBlobBytes)}
		}
		if err != nil {
			switch {
			case contextCancellation(ctx, err):
				result.Status = review.SyntaxCancelled
			case errors.Is(err, ErrSourceLimited):
				result.Status = review.SyntaxLimited
			default:
				result.Status = review.SyntaxUnavailable
			}
			result.Detail = boundedOperationDetail(err.Error())
			return result, nil
		}
		acquisition := SyntaxAcquisition{
			identity: demand.Identity,
			lines:    demand.Lines,
			blob:     blob,
			valid:    true,
		}
		prepared, err := acquisition.Finish(ctx, runCPU)
		acquisition.Close()
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				result.Status = review.SyntaxCancelled
			case errors.Is(err, review.ErrSyntaxLimited):
				result.Status = review.SyntaxLimited
			default:
				result.Status = review.SyntaxFailed
			}
			result.Detail = boundedOperationDetail(err.Error())
			return result, nil
		}
		switch demand.Side {
		case review.LineOld:
			result.Old = prepared
		case review.LineNew:
			result.New = prepared
		}
	}
	result.Status = review.SyntaxReady
	return result, nil
}

func contextCancellation(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	cause := context.Cause(ctx)
	return cause != nil && errors.Is(err, cause)
}

// Reload performs the reload operation.
func (service *Service) Reload(
	ctx context.Context,
	document *Document,
	options OpenOptions,
	runCPU CPUAdmission,
) (*Document, error) {
	if document == nil {
		return nil, errors.New("source document is nil")
	}
	target := document.target
	return service.openWithCPU(ctx, target, options, runCPU)
}
