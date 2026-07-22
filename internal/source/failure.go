package source

import (
	"context"
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/subprocess"
)

// ErrSourceLimited identifies an source limited failure.
var (
	ErrSourceLimited = errors.New("source operation exceeded its limit")
	ErrSourceStale   = errors.New("source revision became stale")
)

// FailureKind describes failure kind.
type FailureKind uint8

// FailureUnavailable identifies a supported failure unavailable value.
const (
	FailureUnavailable FailureKind = iota + 1
	FailureStale
	FailureCancelled
	FailureLimited
	FailureFailed
	FailureNonGraceful
)

// FailureOperation represents failure operation.
type FailureOperation string

// OperationDiscoverRepository identifies a supported operation discover repository value.
const (
	OperationDiscoverRepository FailureOperation = "discover-repository"
	OperationOpenSource         FailureOperation = "open-source"
	OperationCheckPublication   FailureOperation = "check-publication"
)

// FailureError describes failure error.
type FailureError struct {
	cause     error
	Operation FailureOperation
	Kind      FailureKind
}

// LimitResource represents limit resource.
type LimitResource string

// LimitRenameCandidates identifies a supported limit rename candidates value.
const (
	LimitRenameCandidates LimitResource = "rename-candidates"
	LimitPullRequestDepth LimitResource = "pull-request-fetch-depth"
	LimitRemoteThreads    LimitResource = "remote-review-thread-identities"
	LimitPendingReview    LimitResource = "pending-review-identities"
)

// LimitError describes limit error.
type LimitError struct {
	Resource LimitResource
	Limit    uint32
}

// Error returns the receiver's error value.
func (failure *LimitError) Error() string {
	return fmt.Sprintf("%s: %s exceed %d", ErrSourceLimited, failure.Resource, failure.Limit)
}

// Unwrap returns the receiver's unwrap value.
func (failure *LimitError) Unwrap() error {
	return ErrSourceLimited
}

// Error returns the receiver's error value.
func (failure *FailureError) Error() string {
	return fmt.Sprintf("%s: %v", failure.Operation, failure.cause)
}

// Unwrap returns the receiver's unwrap value.
func (failure *FailureError) Unwrap() error {
	return failure.cause
}

func sourceFailure(operation FailureOperation, err error) error {
	if err == nil {
		return nil
	}
	var existing *FailureError
	if errors.As(err, &existing) {
		return err
	}
	kind := FailureFailed
	switch {
	case errors.Is(err, subprocess.ErrNonGraceful):
		kind = FailureNonGraceful
	case errors.Is(err, ErrSourceStale):
		kind = FailureStale
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		kind = FailureCancelled
	case errors.Is(err, subprocess.ErrLimited), errors.Is(err, ErrSourceLimited):
		kind = FailureLimited
	case isStartFailure(err):
		kind = FailureUnavailable
	}
	return &FailureError{Kind: kind, Operation: operation, cause: err}
}

func isStartFailure(err error) bool {
	var startError *subprocess.StartError
	return errors.As(err, &startError)
}
