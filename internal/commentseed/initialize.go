package commentseed

import (
	"context"
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/source"
)

// PatchLoader materializes one requested review patch.
type PatchLoader interface {
	LoadPatch(
		ctx context.Context,
		opened source.Opened,
		revision *source.Revision,
		request change.PatchRequest,
	) (change.PatchResult, error)
}

type resolvedComment struct {
	target  change.CommentTarget
	initial change.InitialComment
}

// Initialize validates comment applicability and atomically initializes review.
func Initialize(
	ctx context.Context,
	loader PatchLoader,
	opened source.Opened,
	revision *source.Revision,
	review change.Review,
	patchContext change.PatchContext,
	document Document,
) (change.Review, error) {
	if !document.present || len(document.comments) == 0 {
		return review, nil
	}
	if ctx == nil {
		return review, errors.New("comment seed initialization requires context")
	}
	if err := context.Cause(ctx); err != nil {
		return review, err
	}

	resolved, groups, order, err := resolveComments(review, document)
	if err != nil {
		return review, err
	}
	current, err := materializeCommentLines(
		ctx,
		loader,
		opened,
		revision,
		review,
		patchContext,
		document,
		resolved,
		groups,
		order,
	)
	if err != nil {
		return review, err
	}
	initial := make([]change.InitialComment, len(resolved))
	for index := range resolved {
		initial[index] = resolved[index].initial
	}
	initialization, err := change.NewCommentInitialization(review.Snapshot(), initial)
	if err != nil {
		return review, err
	}
	initialized, err := current.InitializeComments(
		func() error { return context.Cause(ctx) },
		initialization,
	)
	if err != nil {
		return review, err
	}
	return initialized, nil
}

func resolveComments(
	review change.Review,
	document Document,
) ([]resolvedComment, map[change.FileID][]int, []change.FileID, error) {
	resolved := make([]resolvedComment, len(document.comments))
	files := make(map[string]change.File, review.Snapshot().FileCount())
	for file := range review.Snapshot().AllFiles() {
		identity := string(file.IdentityPath().Raw())
		if _, duplicate := files[identity]; duplicate {
			return nil, nil, nil, fmt.Errorf(
				"comment seed path %q is ambiguous",
				file.IdentityPath().Display(),
			)
		}
		files[identity] = file
	}

	groups := make(map[change.FileID][]int)
	order := make([]change.FileID, 0)
	for index, seed := range document.comments {
		var target change.CommentTarget
		switch seed.target.kind {
		case change.TargetReview:
			target = change.ReviewCommentTarget()
		case change.TargetFile, change.TargetLine:
			file, exists := files[string(seed.target.path.Raw())]
			if !exists || file.Flags().Hidden() {
				return nil, nil, nil, fmt.Errorf(
					"comment %d path %q is not an active file in this review",
					index+1,
					seed.target.path.Display(),
				)
			}
			var err error
			if seed.target.kind == change.TargetFile {
				target, err = change.FileCommentTarget(file.ID())
			} else {
				target, err = change.LineCommentTarget(
					file.ID(),
					seed.target.side,
					seed.target.start,
					seed.target.end,
				)
				if _, grouped := groups[file.ID()]; !grouped {
					order = append(order, file.ID())
				}
				groups[file.ID()] = append(groups[file.ID()], index)
			}
			if err != nil {
				return nil, nil, nil, fmt.Errorf("comment %d target: %w", index+1, err)
			}
		default:
			return nil, nil, nil, fmt.Errorf("comment %d has an unknown target", index+1)
		}
		initial, err := change.NewInitialComment(review.Snapshot(), target, seed.body)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("comment %d: %w", index+1, err)
		}
		resolved[index] = resolvedComment{initial: initial, target: target}
	}
	return resolved, groups, order, nil
}

func materializeCommentLines(
	ctx context.Context,
	loader PatchLoader,
	opened source.Opened,
	revision *source.Revision,
	review change.Review,
	patchContext change.PatchContext,
	document Document,
	resolved []resolvedComment,
	groups map[change.FileID][]int,
	order []change.FileID,
) (change.Review, error) {
	current := review
	for _, fileID := range order {
		if loader == nil || revision == nil {
			return review, errors.New("line comment applicability requires an available source revision")
		}
		if err := context.Cause(ctx); err != nil {
			return review, err
		}
		requested, request, err := current.RequestPatch(fileID, patchContext)
		if err != nil {
			return review, fmt.Errorf("request comment seed patch: %w", err)
		}
		result, err := loader.LoadPatch(ctx, opened, revision, request)
		if err != nil {
			return review, fmt.Errorf("load comment seed patch: %w", err)
		}
		current, err = requested.ApplyPatch(result)
		if err != nil {
			return review, fmt.Errorf("apply comment seed patch: %w", err)
		}
		state := current.Patch(fileID)
		if state.Status != change.PatchReady {
			detail := state.Detail.String()
			if detail == "" {
				detail = patchStatusName(state.Status)
			}
			file, _ := current.Snapshot().File(fileID)
			return review, fmt.Errorf(
				"comment seed patch for %q is not ready: %s",
				file.IdentityPath().Display(),
				detail,
			)
		}
		for _, index := range groups[fileID] {
			target := resolved[index].target
			side, start, end, _ := target.Lines()
			if _, ok := current.PatchLineForSource(fileID, side, start); !ok {
				return review, inapplicableLineError(index, document.comments[index].target, start)
			}
			if end != start {
				if _, ok := current.PatchLineForSource(fileID, side, end); !ok {
					return review, inapplicableLineError(index, document.comments[index].target, end)
				}
			}
		}
	}
	return current, nil
}

func inapplicableLineError(index int, target seedTarget, line change.LineNumber) error {
	side := "new"
	if target.side == change.LineOld {
		side = "old"
	}
	return fmt.Errorf(
		"comment %d target %q %s line %d is not present in the configured patch context",
		index+1,
		target.path.Display(),
		side,
		line.Uint32(),
	)
}

func patchStatusName(status change.PatchStatus) string {
	switch status {
	case change.PatchUnavailable:
		return "unavailable"
	case change.PatchPending:
		return "pending"
	case change.PatchReady:
		return "ready"
	case change.PatchFailed:
		return "failed"
	case change.PatchCancelled:
		return "cancelled"
	case change.PatchLimited:
		return "limited"
	default:
		return "unknown"
	}
}
