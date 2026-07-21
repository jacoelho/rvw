package source

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/subprocess"
)

// LoadPatch loads patch.
func (service *Service) LoadPatch(
	ctx context.Context,
	document *Document,
	request review.PatchRequest,
) (review.PatchResult, error) {
	if document == nil {
		return review.PatchResult{}, errors.New("source document is nil")
	}

	result := review.PatchResult{
		Workspace: request.Workspace,
		Handle:    request.Handle,
	}
	if request.Handle.SourceID() != document.snapshot.SourceID() {
		return result, errors.New("patch request has a stale source identity")
	}
	fileID := request.Handle.FileID()
	file, ok := document.snapshot.File(fileID)
	if !ok || file.PatchHandle() != request.Handle {
		return result, errors.New("patch request has an invalid file identity")
	}

	paths := make([]string, 0, 2)
	if path, ok := file.OldPath(); ok {
		paths = append(paths, string(path.Raw()))
	}
	if path, ok := file.NewPath(); ok {
		if oldPath, oldOK := file.OldPath(); !oldOK || !path.Equal(oldPath) {
			paths = append(paths, string(path.Raw()))
		}
	}
	if document.revision.kind == revisionIndex {
		unmerged, err := service.indexPathUnmerged(
			ctx,
			document.revision.repository,
			document.revision.environment,
			paths,
		)
		if err != nil {
			return result, err
		}
		if unmerged {
			result.Status = review.PatchUnavailable
			result.Detail = review.PatchDetail("unmerged index path has no single staged patch")
			return result, nil
		}
	}

	arguments := []string{"--literal-pathspecs"}
	if document.revision.kind == revisionIndex {
		arguments = append(arguments,
			gitDiff,
			gitCached,
			"-p",
		)
	} else {
		arguments = append(arguments,
			"diff-tree",
			"--no-commit-id",
			"-r",
			"-p",
		)
	}
	arguments = append(arguments,
		"--no-color",
		gitNoExtDiff,
		gitNoTextconv,
		"--unified="+strconv.FormatUint(uint64(request.Context.UnifiedLines()), 10),
		document.revision.base.String(),
	)
	if document.revision.kind == revisionTree {
		arguments = append(arguments,
			document.revision.head.String(),
		)
	}
	arguments = append(arguments,
		"--",
	)
	arguments = append(arguments, paths...)
	processResult, err := service.runGit(
		ctx,
		document.revision.repository.String(),
		document.revision.environment,
		arguments,
		nil,
		review.MaxPatchBytes,
	)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			result.Status = review.PatchCancelled
		case errors.Is(err, subprocess.ErrLimited):
			result.Status = review.PatchLimited
		default:
			result.Status = review.PatchFailed
		}
		result.Detail = review.PatchDetail(boundedText(err.Error(), 16<<10))
	} else if prepared, prepareErr := review.TakePatch(processResult.Stdout); prepareErr != nil {
		result.Status = review.PatchLimited
		result.Detail = review.PatchDetail(prepareErr.Error())
	} else {
		result.Status = review.PatchReady
		result.Prepared = prepared
	}
	return result, nil
}

func (service *Service) indexPathUnmerged(
	ctx context.Context,
	repository RepositoryPath,
	environment map[string]string,
	paths []string,
) (bool, error) {
	arguments := []string{"--literal-pathspecs", gitLSFiles, "--unmerged", "-z", "--"}
	arguments = append(arguments, paths...)
	result, err := service.runGit(
		ctx,
		repository.String(),
		environment,
		arguments,
		nil,
		64<<10,
	)
	if err != nil {
		return false, fmt.Errorf("inspect staged conflict: %w", err)
	}
	return len(result.Stdout) != 0, nil
}

// LoadSyntax loads syntax.
func (service *Service) LoadSyntax(
	ctx context.Context,
	document *Document,
	request review.SyntaxRequest,
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
	for _, demand := range request.Demands {
		content, exists := file.Content(demand.Side)
		if !exists || content != demand.Identity.Content() {
			return result, errors.New("syntax request content identity is stale")
		}
		command, err := service.runGit(
			ctx,
			document.revision.repository.String(),
			document.revision.environment,
			[]string{"cat-file", "blob", content.String()},
			nil,
			review.MaxBlobBytes,
		)
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				result.Status = review.SyntaxCancelled
			case errors.Is(err, subprocess.ErrLimited):
				result.Status = review.SyntaxLimited
			default:
				result.Status = review.SyntaxUnavailable
			}
			result.Detail = boundedOperationDetail(err.Error())
			return result, nil
		}
		prepared, err := review.PrepareSyntaxCancellable(
			func() error { return context.Cause(ctx) },
			demand.Identity,
			demand.Lines,
			command.Stdout,
		)
		if err != nil {
			if errors.Is(err, review.ErrSyntaxLimited) {
				result.Status = review.SyntaxLimited
			} else {
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
		default:
			return result, errors.New("syntax request side is invalid")
		}
	}
	result.Status = review.SyntaxReady
	return result, nil
}

// Reload performs the reload operation.
func (service *Service) Reload(
	ctx context.Context,
	document *Document,
	options OpenOptions,
) (*Document, error) {
	if document == nil {
		return nil, errors.New("source document is nil")
	}
	target := document.target
	return service.Open(ctx, target, options)
}
