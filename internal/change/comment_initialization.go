package change

import (
	"errors"
	"fmt"
)

// ErrInvalidCommentInitialization identifies invalid initial local comments.
var ErrInvalidCommentInitialization = errors.New("invalid comment initialization")

// InitialComment is one validated, source-bound initial local comment.
type InitialComment struct {
	body   CommentBody
	target CommentTarget
	source SourceID
	valid  bool
}

// NewInitialComment validates and binds one initial local comment to snapshot.
func NewInitialComment(
	snapshot Snapshot,
	target CommentTarget,
	body string,
) (InitialComment, error) {
	if snapshot.SourceID() == 0 {
		return InitialComment{}, fmt.Errorf(
			"%w: snapshot has no source",
			ErrInvalidCommentInitialization,
		)
	}
	validatedBody, err := NewCommentBody(body)
	if err != nil {
		return InitialComment{}, fmt.Errorf("%w: %w", ErrInvalidCommentInitialization, err)
	}
	switch target.Kind() {
	case TargetReview:
	case TargetFile, TargetLine:
		fileID, mapped := target.FileID()
		file, exists := snapshot.File(fileID)
		if !mapped || !exists {
			return InitialComment{}, fmt.Errorf(
				"%w: target file is not in the source",
				ErrInvalidCommentInitialization,
			)
		}
		target = target.withPath(file.IdentityPath())
	default:
		return InitialComment{}, fmt.Errorf(
			"%w: target kind is unknown",
			ErrInvalidCommentInitialization,
		)
	}
	return InitialComment{
		target: target,
		body:   validatedBody,
		source: snapshot.SourceID(),
		valid:  true,
	}, nil
}

// CommentInitialization is an opaque source-bound batch of initial comments.
type CommentInitialization struct {
	comments  []InitialComment
	bodyBytes int64
	source    SourceID
	valid     bool
}

// NewCommentInitialization constructs a validated initial comment batch.
func NewCommentInitialization(
	snapshot Snapshot,
	comments []InitialComment,
) (CommentInitialization, error) {
	source := snapshot.SourceID()
	if source == 0 {
		return CommentInitialization{}, fmt.Errorf(
			"%w: snapshot has no source",
			ErrInvalidCommentInitialization,
		)
	}
	if len(comments) > MaxComments {
		return CommentInitialization{}, fmt.Errorf(
			"%w: comment count %d exceeds %d",
			ErrInvalidCommentInitialization,
			len(comments),
			MaxComments,
		)
	}
	owned := make([]InitialComment, len(comments))
	var bodyBytes int64
	for index, comment := range comments {
		if !comment.valid || comment.source != source {
			return CommentInitialization{}, fmt.Errorf(
				"%w: comment %d belongs to another source",
				ErrInvalidCommentInitialization,
				index+1,
			)
		}
		if int64(len(comment.body)) > MaxCommentBodyTotal-bodyBytes {
			return CommentInitialization{}, fmt.Errorf(
				"%w: comment bodies exceed %d bytes",
				ErrInvalidCommentInitialization,
				MaxCommentBodyTotal,
			)
		}
		bodyBytes += int64(len(comment.body))
		owned[index] = comment
	}
	return CommentInitialization{
		comments:  owned,
		bodyBytes: bodyBytes,
		source:    source,
		valid:     true,
	}, nil
}

// InitializeComments atomically appends initial local comments after imported
// pending-review comments. A failure leaves review unchanged.
func (review Review) InitializeComments(
	cancelled func() error,
	initialization CommentInitialization,
) (Review, error) {
	if !initialization.valid || initialization.source != review.snapshot.SourceID() ||
		review.revision != 1 || review.activeMutation != 0 || review.draft != nil ||
		review.pendingDirty != 0 {
		return review, ErrInvalidCommentInitialization
	}
	seedCount := len(initialization.comments)
	if seedCount > MaxComments-len(review.comments) ||
		seedCount > MaxComments-int(review.nextComment) {
		return review, fmt.Errorf(
			"%w: combined comment count exceeds %d",
			ErrInvalidCommentInitialization,
			MaxComments,
		)
	}
	if initialization.bodyBytes > MaxCommentBodyTotal-review.commentBody {
		return review, fmt.Errorf(
			"%w: combined comment bodies exceed %d bytes",
			ErrInvalidCommentInitialization,
			MaxCommentBodyTotal,
		)
	}
	if err := cancellationError(cancelled); err != nil {
		return review, err
	}

	state := review.commentState
	state.comments = make([]Comment, len(review.comments), len(review.comments)+seedCount)
	for start := 0; start < len(review.comments); start += 2_048 {
		if err := cancellationError(cancelled); err != nil {
			return review, err
		}
		finish := min(start+2_048, len(review.comments))
		copy(state.comments[start:finish], review.comments[start:finish])
	}
	for index, initial := range initialization.comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return review, err
			}
		}
		state.comments = append(state.comments, Comment{
			id:     CommentID(state.nextComment),
			target: initial.target,
			body:   initial.body,
		})
		state.nextComment++
	}
	state.commentBody += initialization.bodyBytes
	state.revision++
	if err := state.rebuildCommentAnchorsCancellable(cancelled); err != nil {
		return review, err
	}
	if err := state.refreshCancellable(cancelled); err != nil {
		return review, err
	}

	next := review.clone()
	next.commentState = state
	return next, nil
}
