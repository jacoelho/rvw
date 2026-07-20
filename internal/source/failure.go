package source

import (
	"context"
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/subprocess"
)

var (
	ErrSourceLimited = errors.New("source operation exceeded its limit")
	ErrSourceStale   = errors.New("source revision became stale")
)

type FailureKind uint8

const (
	FailureUnavailable FailureKind = iota + 1
	FailureStale
	FailureCancelled
	FailureLimited
	FailureFailed
	FailureNonGraceful
)

type FailureOperation string

const (
	OperationDiscoverRepository FailureOperation = "discover-repository"
	OperationOpenSource         FailureOperation = "open-source"
	OperationCheckPublication   FailureOperation = "check-publication"
)

type Failure struct {
	Kind      FailureKind
	Operation FailureOperation
	cause     error
}

type LimitResource string

const (
	LimitRenameCandidates LimitResource = "rename-candidates"
	LimitPullRequestDepth LimitResource = "pull-request-fetch-depth"
	LimitRemoteThreads    LimitResource = "remote-review-thread-identities"
)

type LimitError struct {
	Resource LimitResource
	Limit    uint32
}

func (failure *LimitError) Error() string {
	return fmt.Sprintf("%s: %s exceed %d", ErrSourceLimited, failure.Resource, failure.Limit)
}

func (failure *LimitError) Unwrap() error {
	return ErrSourceLimited
}

func (failure *Failure) Error() string {
	return fmt.Sprintf("%s: %v", failure.Operation, failure.cause)
}

func (failure *Failure) Unwrap() error {
	return failure.cause
}

func sourceFailure(operation FailureOperation, err error) error {
	if err == nil {
		return nil
	}
	var existing *Failure
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
	return &Failure{Kind: kind, Operation: operation, cause: err}
}

func isStartFailure(err error) bool {
	var startError *subprocess.StartError
	return errors.As(err, &startError)
}
