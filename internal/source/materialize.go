package source

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/jacoelho/rvw/internal/change"
)

// PatchAcquisition owns patch bytes acquired from Git until CPU preparation.
type PatchAcquisition struct {
	state *patchAcquisitionState
}

type patchAcquisitionState struct {
	native  *nativePatchInput
	result  change.PatchResult
	valid   bool
	prepare bool
	mu      sync.Mutex
}

// RequiresPreparation reports whether Finish must run under CPU admission.
func (acquisition *PatchAcquisition) RequiresPreparation() bool {
	if acquisition == nil || acquisition.state == nil {
		return false
	}
	acquisition.state.mu.Lock()
	defer acquisition.state.mu.Unlock()
	return acquisition.state.valid && acquisition.state.prepare
}

// Close releases unprepared patch bytes.
func (acquisition *PatchAcquisition) Close() {
	acquisition.consume()
}

// Finish consumes an acquisition and performs only cancellable CPU preparation.
func (acquisition *PatchAcquisition) Finish(
	ctx context.Context,
	runCPU CPUAdmission,
) (change.PatchResult, error) {
	if runCPU == nil {
		return change.PatchResult{}, errors.New("patch CPU admission is nil")
	}
	result, native, prepare, ok := acquisition.consume()
	if !ok {
		return change.PatchResult{}, errors.New("patch acquisition is invalid")
	}
	if !prepare {
		return result, nil
	}
	var prepared *change.PreparedPatch
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
			return change.PatchResult{}, admissionErr
		}
		prepareErr = admissionErr
	}
	if prepareErr != nil {
		if contextCancellation(ctx, prepareErr) {
			result.Status = change.PatchCancelled
		} else {
			result.Status = change.PatchLimited
		}
		result.Detail = change.PatchDetail(boundedText(prepareErr.Error(), 16<<10))
		return result, nil
	}
	result.Status = change.PatchReady
	result.Prepared = prepared
	return result, nil
}

func (acquisition *PatchAcquisition) consume() (
	change.PatchResult,
	*nativePatchInput,
	bool,
	bool,
) {
	if acquisition == nil || acquisition.state == nil {
		return change.PatchResult{}, nil, false, false
	}
	state := acquisition.state
	acquisition.state = nil
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.valid {
		return change.PatchResult{}, nil, false, false
	}
	result, native, prepare := state.result, state.native, state.prepare
	state.result = change.PatchResult{}
	state.native = nil
	state.valid = false
	state.prepare = false
	return result, native, prepare, true
}

// LoadPatch loads and prepares a patch without scheduler integration.
func (service *Service) LoadPatch(
	ctx context.Context,
	opened Opened,
	revision *Revision,
	request change.PatchRequest,
) (change.PatchResult, error) {
	acquisition, err := service.AcquirePatch(ctx, opened, revision, request)
	if err != nil {
		return change.PatchResult{}, err
	}
	defer acquisition.Close()
	return acquisition.Finish(ctx, directCPUAdmission)
}

// AcquirePatch performs validation, filesystem inspection, and Git acquisition only.
func (service *Service) AcquirePatch(
	ctx context.Context,
	opened Opened,
	revision *Revision,
	request change.PatchRequest,
) (PatchAcquisition, error) {
	if revision == nil || revision.retired {
		return PatchAcquisition{}, errors.New("source revision is unavailable")
	}

	result := change.PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
	}
	if request.Handle.SourceID() != opened.snapshot.SourceID() {
		return PatchAcquisition{}, errors.New("patch request has a stale source identity")
	}
	fileID := request.Handle.FileID()
	file, ok := opened.snapshot.File(fileID)
	if !ok || file.PatchHandle() != request.Handle {
		return PatchAcquisition{}, errors.New("patch request has an invalid file identity")
	}

	if revision.state.repository == nil {
		return PatchAcquisition{}, errors.New("source revision has no native repository")
	}
	oldPath, newPath := "", ""
	if path, ok := file.OldPath(); ok {
		oldPath = string(path.Raw())
	}
	if path, ok := file.NewPath(); ok {
		newPath = string(path.Raw())
	}
	if _, conflict := revision.state.unmerged[primaryAvailablePath(oldPath, newPath)]; conflict {
		result.Status = change.PatchUnavailable
		result.Detail = change.PatchDetail("unmerged index path has no single prepared patch")
		return newPatchAcquisition(result, nil, false), nil
	}
	native, err := acquireNativePatch(ctx, revision, file, request.Context)
	if err != nil {
		if strings.Contains(err.Error(), "submodule") {
			result.Status = change.PatchUnavailable
			result.Detail = change.PatchDetail(boundedText(err.Error(), 16<<10))
			return newPatchAcquisition(result, nil, false), nil
		}
		return PatchAcquisition{}, err
	}
	return newPatchAcquisition(result, native, true), nil
}

func newPatchAcquisition(
	result change.PatchResult,
	native *nativePatchInput,
	prepare bool,
) PatchAcquisition {
	return PatchAcquisition{state: &patchAcquisitionState{
		result: result, native: native, valid: true, prepare: prepare,
	}}
}

// SyntaxAcquisition owns one Git blob until CPU preparation completes.
type SyntaxAcquisition struct {
	state *syntaxAcquisitionState
}

type syntaxAcquisitionState struct {
	identity change.SyntaxIdentity
	lines    []change.LineNumber
	blob     []byte
	valid    bool
	mu       sync.Mutex
}

// Close releases an unprepared blob.
func (acquisition *SyntaxAcquisition) Close() {
	acquisition.consume()
}

// Finish consumes a blob and performs only cancellable CPU preparation.
func (acquisition *SyntaxAcquisition) Finish(
	ctx context.Context,
	runCPU CPUAdmission,
) (*change.PreparedSyntax, error) {
	if runCPU == nil {
		return nil, errors.New("syntax CPU admission is nil")
	}
	identity, lines, blob, ok := acquisition.consume()
	if !ok {
		return nil, errors.New("syntax acquisition is invalid")
	}
	var prepared *change.PreparedSyntax
	var prepareErr error
	if err := runCPU(ctx, func() error {
		prepared, prepareErr = change.PrepareSyntaxCancellable(
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

func (acquisition *SyntaxAcquisition) consume() (
	change.SyntaxIdentity,
	[]change.LineNumber,
	[]byte,
	bool,
) {
	if acquisition == nil || acquisition.state == nil {
		return change.SyntaxIdentity{}, nil, nil, false
	}
	state := acquisition.state
	acquisition.state = nil
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.valid {
		return change.SyntaxIdentity{}, nil, nil, false
	}
	identity, lines, blob := state.identity, state.lines, state.blob
	state.identity = change.SyntaxIdentity{}
	state.lines = nil
	state.blob = nil
	state.valid = false
	return identity, lines, blob, true
}

// LoadSyntax loads syntax without scheduler integration.
func (service *Service) LoadSyntax(
	ctx context.Context,
	opened Opened,
	revision *Revision,
	request change.SyntaxRequest,
) (change.SyntaxResult, error) {
	return service.LoadSyntaxWithCPUAdmission(ctx, opened, revision, request, directCPUAdmission)
}

// LoadSyntaxWithCPUAdmission acquires Git blobs outside CPU admission and
// prepares each acquired blob synchronously under admission.
func (service *Service) LoadSyntaxWithCPUAdmission(
	ctx context.Context,
	opened Opened,
	revision *Revision,
	request change.SyntaxRequest,
	runCPU CPUAdmission,
) (change.SyntaxResult, error) {
	result := change.SyntaxResult{
		Operation: request.Operation,
		Workspace: request.Workspace,
		Source:    request.Source,
		File:      request.File,
	}
	if revision == nil || revision.retired || request.Operation == 0 ||
		request.Source != opened.Snapshot().SourceID() {
		return result, errors.New("syntax request has a stale source identity")
	}
	file, ok := opened.Snapshot().File(request.File)
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
		case change.LineOld:
			if seenOld {
				return result, errors.New("syntax request repeats old-side demand")
			}
			seenOld = true
		case change.LineNew:
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
		if revision.state.repository == nil {
			return result, errors.New("source document has no native repository revision")
		}
		blob, err = readNativeRecordSide(ctx, revision.state.repository, &content)
		if err == nil && int64(len(blob)) > change.MaxBlobBytes {
			err = &LimitError{Resource: LimitDiffInputBytes, Limit: uint64(change.MaxBlobBytes)}
		}
		if err != nil {
			switch {
			case contextCancellation(ctx, err):
				result.Status = change.SyntaxCancelled
			case errors.Is(err, ErrSourceLimited):
				result.Status = change.SyntaxLimited
			default:
				result.Status = change.SyntaxUnavailable
			}
			result.Detail = boundedOperationDetail(err.Error())
			return result, nil
		}
		acquisition := SyntaxAcquisition{state: &syntaxAcquisitionState{
			identity: demand.Identity,
			lines:    demand.Lines,
			blob:     blob,
			valid:    true,
		}}
		prepared, err := acquisition.Finish(ctx, runCPU)
		acquisition.Close()
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				result.Status = change.SyntaxCancelled
			case errors.Is(err, change.ErrSyntaxLimited):
				result.Status = change.SyntaxLimited
			default:
				result.Status = change.SyntaxFailed
			}
			result.Detail = boundedOperationDetail(err.Error())
			return result, nil
		}
		switch demand.Side {
		case change.LineOld:
			result.Old = prepared
		case change.LineNew:
			result.New = prepared
		}
	}
	result.Status = change.SyntaxReady
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
	opened Opened,
	revision *Revision,
	options OpenOptions,
	runCPU CPUAdmission,
) (Opened, *Revision, error) {
	if revision == nil || revision.retired {
		return Opened{}, nil, errors.New("source revision is unavailable")
	}
	return service.openWithCPU(ctx, OpenRequest{Target: opened.target, Options: options}, runCPU)
}
