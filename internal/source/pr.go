package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jacoelho/rvw/internal/change"
)

type pullRequestPayload struct {
	MergedAt *string `json:"merged_at"`
	User     *struct {
		Login string `json:"login"`
	} `json:"user"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Title        string `json:"title"`
	HTMLURL      string `json:"html_url"`
	State        string `json:"state"`
	Number       uint64 `json:"number"`
	ChangedFiles uint64 `json:"changed_files"`
	Draft        bool   `json:"draft"`
}

func (service *Service) openPullRequest(
	ctx context.Context,
	target Target,
	options OpenOptions,
	runCPU CPUAdmission,
) (_ Opened, _ *Revision, returnErr error) {
	selector, ok := target.PullRequest()
	if !ok {
		return Opened{}, nil, fmt.Errorf("%w: missing pull-request selector", ErrInvalidTarget)
	}
	repository, ok := selector.Repository()
	if !ok {
		var err error
		repository, err = service.currentGitHubRepository(ctx, runCPU)
		if err != nil {
			return Opened{}, nil, err
		}
	}
	payload, err := service.pullRequestMetadata(ctx, repository, selector.Number(), runCPU)
	if err != nil {
		return Opened{}, nil, err
	}

	details, err := validatePullRequestPayload(repository, selector.Number(), payload)
	if err != nil {
		return Opened{}, nil, err
	}
	workspace, err := os.MkdirTemp("", "rvw-pr-")
	if err != nil {
		return Opened{}, nil, fmt.Errorf("create pull-request workspace: %w", err)
	}
	var rootOwner *repositorySession
	defer func() {
		if returnErr != nil {
			if rootOwner != nil {
				returnErr = errors.Join(returnErr, rootOwner.Close())
			} else {
				returnErr = errors.Join(returnErr, os.RemoveAll(workspace))
			}
		}
	}()
	rootOwner, err = createPullRequestRepository(workspace, service.options)
	if err != nil {
		return Opened{}, nil, err
	}
	snapshot, preparedRevision, err := service.prepareNativePullRequest(
		ctx,
		runCPU,
		rootOwner,
		details,
		options,
	)
	if err != nil {
		return Opened{}, nil, err
	}
	ledger, err := newSourceLedger(snapshot.RetainedBytes())
	if err != nil {
		return Opened{}, nil, err
	}
	snapshot, err = service.acquirePullRequestOptionalData(
		ctx,
		runCPU,
		details,
		options,
		snapshot,
		ledger,
	)
	if err != nil {
		return Opened{}, nil, err
	}
	if cancellationErr := context.Cause(ctx); cancellationErr != nil {
		return Opened{}, nil, cancellationErr
	}
	preparedRevision.repository = rootOwner
	opened, revision := newPullRequestOpened(snapshot, target, options, details, preparedRevision)
	rootOwner = nil
	return opened, revision, nil
}

func (service *Service) acquirePullRequestOptionalData(
	ctx context.Context,
	runCPU CPUAdmission,
	details PullRequestDetails,
	options OpenOptions,
	snapshot change.Snapshot,
	ledger *sourceLedger,
) (change.Snapshot, error) {
	var err error
	if options.FetchThreads {
		threads, threadErr := service.loadRemoteThreadsWithLedgerCPU(
			ctx, details, ledger, runCPU,
		)
		if threadErr != nil {
			if cancellationErr := context.Cause(ctx); cancellationErr != nil {
				return change.Snapshot{}, cancellationErr
			}
			err = runCPU(ctx, func() error {
				status := remoteThreadFailureStatus(threadErr)
				detail := boundedOperationDetail(threadErr.Error())
				if reserveErr := ledger.reserve(detail.Bytes()); reserveErr != nil {
					return reserveErr
				}
				var installErr error
				snapshot, installErr = snapshot.WithRemoteThreadFailure(status, detail)
				return installErr
			})
		} else {
			err = runCPU(ctx, func() error {
				scratchBytes, retainedBytes, accountingErr := remoteThreadInstallationBytes(
					ctx,
					snapshot,
					threads,
				)
				if accountingErr != nil {
					return accountingErr
				}
				if reserveErr := ledger.reserve(scratchBytes + retainedBytes); reserveErr != nil {
					return reserveErr
				}
				var installErr error
				snapshot, installErr = snapshot.TakeRemoteThreadsCancellable(
					func() error { return context.Cause(ctx) },
					threads,
				)
				ledger.release(scratchBytes)
				if installErr != nil {
					return fmt.Errorf("validate GitHub review threads: %w", installErr)
				}
				return nil
			})
		}
		if err != nil {
			return change.Snapshot{}, fmt.Errorf("retain GitHub review thread status: %w", err)
		}
	}
	if options.FetchPending {
		pending, status, detail := service.loadPendingReviewWithLedger(
			ctx, details, ledger, runCPU,
		)
		if cancellationErr := context.Cause(ctx); cancellationErr != nil {
			return change.Snapshot{}, cancellationErr
		}
		err = runCPU(ctx, func() error {
			switch status {
			case change.PendingReviewReady:
				scratchBytes := snapshot.PendingReviewInstallationBytes(pending)
				if reserveErr := ledger.reserve(scratchBytes); reserveErr != nil {
					return reserveErr
				}
				var installErr error
				snapshot, installErr = snapshot.TakePendingReview(pending)
				ledger.release(scratchBytes)
				return installErr
			case change.PendingReviewAbsent, change.PendingReviewUnavailable,
				change.PendingReviewLimited, change.PendingReviewAmbiguous:
				if reserveErr := ledger.reserve(detail.Bytes()); reserveErr != nil {
					return reserveErr
				}
				var installErr error
				snapshot, installErr = snapshot.WithPendingReviewStatus(status, detail)
				return installErr
			default:
				return errors.New("GitHub pending review returned an invalid acquisition status")
			}
		})
		if err != nil {
			return change.Snapshot{}, fmt.Errorf("retain GitHub pending review status: %w", err)
		}
	}
	return snapshot, nil
}

func remoteThreadFailureStatus(err error) change.RemoteThreadStatus {
	var limited *LimitError
	if errors.As(err, &limited) || errors.Is(err, ErrSourceLimited) {
		return change.RemoteThreadsLimited
	}
	return change.RemoteThreadsUnavailable
}

func (service *Service) currentGitHubRepository(
	ctx context.Context,
	runCPU CPUAdmission,
) (OwnerRepository, error) {
	_ = runCPU
	return service.inferGitHubRepository(ctx)
}

func (service *Service) pullRequestMetadata(
	ctx context.Context,
	repository OwnerRepository,
	number PullRequestNumber,
	runCPU CPUAdmission,
) (pullRequestPayload, error) {
	_ = runCPU
	payload, err := service.github.pullRequest(ctx, repository, number)
	if err != nil {
		return pullRequestPayload{}, fmt.Errorf("read pull-request metadata: %w", err)
	}
	return payload, nil
}

func validatePullRequestPayload(
	repository OwnerRepository,
	number PullRequestNumber,
	payload pullRequestPayload,
) (PullRequestDetails, error) {
	if payload.Number != number.Uint64() {
		return PullRequestDetails{}, fmt.Errorf("GitHub returned pull request %d, expected %d", payload.Number, number)
	}
	expectedURL := "https://github.com/" + repository.String() + "/pull/" +
		strconv.FormatUint(number.Uint64(), 10)
	if payload.HTMLURL != expectedURL {
		return PullRequestDetails{}, fmt.Errorf("GitHub returned unexpected pull-request URL %q", payload.HTMLURL)
	}
	if int64(len(payload.Title)) > 1<<20 || strings.IndexByte(payload.Title, 0) >= 0 {
		return PullRequestDetails{}, errors.New("GitHub returned an invalid pull-request title")
	}
	baseOID, err := change.NewObjectID(payload.Base.SHA)
	if err != nil {
		return PullRequestDetails{}, fmt.Errorf("validate pull-request base object: %w", err)
	}
	headOID, err := change.NewObjectID(payload.Head.SHA)
	if err != nil {
		return PullRequestDetails{}, fmt.Errorf("validate pull-request head object: %w", err)
	}
	baseRef, err := NewGitReference(payload.Base.Ref)
	if err != nil {
		return PullRequestDetails{}, fmt.Errorf("validate pull-request base ref: %w", err)
	}
	headRef, err := NewGitReference(payload.Head.Ref)
	if err != nil {
		return PullRequestDetails{}, fmt.Errorf("validate pull-request head ref: %w", err)
	}
	merge := githubNotMerged
	if payload.MergedAt != nil {
		if *payload.MergedAt == "" {
			return PullRequestDetails{}, errors.New(
				"GitHub returned an invalid merge timestamp",
			)
		}
		if _, parseErr := time.Parse(time.RFC3339, *payload.MergedAt); parseErr != nil {
			return PullRequestDetails{}, errors.New(
				"GitHub returned an invalid merge timestamp",
			)
		}
		merge = githubMerged
	}
	state, err := githubState(payload.State, merge)
	if err != nil {
		return PullRequestDetails{}, err
	}
	var author *GitHubAuthor
	if payload.User != nil {
		if payload.User.Login == "" || int64(len(payload.User.Login)) > 256 {
			return PullRequestDetails{}, errors.New("GitHub returned an invalid author")
		}
		for _, character := range payload.User.Login {
			if unicode.IsControl(character) {
				return PullRequestDetails{}, errors.New("GitHub returned an invalid author")
			}
		}
		value := GitHubAuthor(payload.User.Login)
		author = new(value)
	}
	return PullRequestDetails{
		Repository: repository,
		Number:     number,
		Title:      Title(payload.Title),
		BaseOID:    baseOID,
		HeadOID:    headOID,
		BaseRef:    baseRef,
		HeadRef:    headRef,
		URL:        GitHubURL(expectedURL),
		State:      state,
		Draft:      payload.Draft,
		Author:     author,
	}, nil
}

type githubMerge uint8

const (
	githubNotMerged githubMerge = iota
	githubMerged
)

func githubState(value string, merge githubMerge) (GitHubState, error) {
	if merge == githubMerged {
		return GitHubMerged, nil
	}
	switch value {
	case "open":
		return GitHubOpen, nil
	case "closed":
		return GitHubClosed, nil
	default:
		return "", fmt.Errorf("GitHub returned invalid pull-request state %q", value)
	}
}
