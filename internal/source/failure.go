package source

import (
	"context"
	"errors"
	"fmt"
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
	LimitRenameCandidates       LimitResource = "rename-candidates"
	LimitChangedFiles           LimitResource = "changed-files"
	LimitPullRequestDepth       LimitResource = "pull-request-fetch-depth"
	LimitRemoteThreads          LimitResource = "remote-review-thread-identities"
	LimitPendingReview          LimitResource = "pending-review-identities"
	LimitIndexEncoding          LimitResource = "index-encoded-bytes"
	LimitIndexPaths             LimitResource = "index-path-bytes"
	LimitIndexExtensions        LimitResource = "index-extension-bytes"
	LimitWorktreeEntries        LimitResource = "worktree-visited-entries"
	LimitWorktreePaths          LimitResource = "worktree-visited-path-bytes"
	LimitWorktreeDepth          LimitResource = "worktree-depth"
	LimitWorktreeDirectories    LimitResource = "worktree-open-directories"
	LimitWorktreeSymlink        LimitResource = "worktree-symlink-target-bytes"
	LimitEmbeddedRepositories   LimitResource = "embedded-repositories"
	LimitConfigFiles            LimitResource = "git-config-files"
	LimitConfigBytes            LimitResource = "git-config-bytes"
	LimitConfigEntries          LimitResource = "git-config-entries"
	LimitConfigIncludeDepth     LimitResource = "git-config-include-depth"
	LimitIgnoreFiles            LimitResource = "ignore-files"
	LimitIgnoreBytes            LimitResource = "ignore-bytes"
	LimitIgnoreRules            LimitResource = "ignore-rules"
	LimitAttributeFiles         LimitResource = "attribute-files"
	LimitAttributeBytes         LimitResource = "attribute-bytes"
	LimitAttributeRules         LimitResource = "attribute-rules"
	LimitPolicyNFAStates        LimitResource = "policy-nfa-states"
	LimitPolicyDFAStates        LimitResource = "policy-dfa-states"
	LimitPolicyEdges            LimitResource = "policy-edges"
	LimitPolicyCompileWork      LimitResource = "policy-compile-transitions"
	LimitPolicyMatchWork        LimitResource = "policy-match-transitions"
	LimitFilterAutomaton        LimitResource = "filter-automaton-work"
	LimitPullRequestRawRecords  LimitResource = "pull-request-raw-records"
	LimitPullRequestRecords     LimitResource = "pull-request-retained-records"
	LimitPackWireBytes          LimitResource = "pack-wire-bytes"
	LimitPackExpandedBytes      LimitResource = "pack-expanded-bytes"
	LimitPullRequestWorkspace   LimitResource = "pull-request-workspace-bytes"
	LimitDiffInputBytes         LimitResource = "diff-input-bytes"
	LimitDiffProcessedBytes     LimitResource = "diff-processed-bytes"
	LimitDiffLineTokens         LimitResource = "diff-line-tokens" //nolint:gosec // This names a line-count resource, not a credential.
	LimitDiffHistogramProbes    LimitResource = "diff-histogram-probes"
	LimitDiffSignatureSpans     LimitResource = "diff-signature-spans"
	LimitDiffSignaturePairs     LimitResource = "diff-signature-pairs"
	LimitDiffSignatureMergeWork LimitResource = "diff-signature-merge-work"
	LimitDiffScratchBytes       LimitResource = "diff-scratch-bytes"
	LimitDiffWorkspaceBytes     LimitResource = "diff-workspace-bytes"
)

// LimitError describes limit error.
type LimitError struct {
	Resource LimitResource
	Limit    uint64
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
	case errors.Is(err, ErrSourceStale):
		kind = FailureStale
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		kind = FailureCancelled
	case errors.Is(err, ErrSourceLimited):
		kind = FailureLimited
	case githubUnavailable(err):
		kind = FailureUnavailable
	}
	return &FailureError{Kind: kind, Operation: operation, cause: err}
}

func githubUnavailable(err error) bool {
	var failure *githubHTTPError
	return errors.As(err, &failure) && failure.status == 0
}
