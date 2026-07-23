package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/subprocess"
)

const (
	githubFilesAPICap = 3_000
	ghRepo            = "repo"
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
) (_ *Document, returnErr error) {
	selector, ok := target.PullRequest()
	if !ok {
		return nil, fmt.Errorf("%w: missing pull-request selector", ErrInvalidTarget)
	}
	repository, ok := selector.Repository()
	if !ok {
		var err error
		repository, err = service.currentGitHubRepository(ctx, runCPU)
		if err != nil {
			return nil, err
		}
	}
	payload, err := service.pullRequestMetadata(ctx, repository, selector.Number(), runCPU)
	if err != nil {
		return nil, err
	}

	details, err := validatePullRequestPayload(repository, selector.Number(), payload)
	if err != nil {
		return nil, err
	}
	if payload.ChangedFiles > review.MaxFiles {
		return nil, fmt.Errorf("pull request has %d files; limit is %d", payload.ChangedFiles, review.MaxFiles)
	}

	workspace, err := os.MkdirTemp("", "rvw-pr-")
	if err != nil {
		return nil, fmt.Errorf("create pull-request workspace: %w", err)
	}
	var rootOwner *rootedRepository
	defer func() {
		if returnErr != nil {
			if rootOwner != nil {
				returnErr = errors.Join(returnErr, rootOwner.Close())
			} else {
				returnErr = errors.Join(returnErr, os.RemoveAll(workspace))
			}
		}
	}()
	bare, err := service.fetchPullRequest(ctx, workspace, details)
	if err != nil {
		return nil, err
	}
	rootOwner, err = openRootedRepository(bare)
	if err != nil {
		return nil, err
	}
	rootOwner.cleanup = workspace
	snapshot, preparedRevision, err := service.preparePullRequestSnapshot(
		ctx,
		runCPU,
		rootOwner,
		details,
		payload,
		options,
	)
	if err != nil {
		return nil, err
	}
	ledger, err := newSourceLedger(snapshot.RetainedBytes())
	if err != nil {
		return nil, err
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
		return nil, err
	}
	if cancellationErr := context.Cause(ctx); cancellationErr != nil {
		return nil, cancellationErr
	}
	preparedRevision.repository = rootOwner
	document := newPullRequestDocument(snapshot, target, options, details, preparedRevision)
	rootOwner = nil
	return document, nil
}

func (service *Service) acquirePullRequestOptionalData(
	ctx context.Context,
	runCPU CPUAdmission,
	details PullRequestDetails,
	options OpenOptions,
	snapshot review.Snapshot,
	ledger *sourceLedger,
) (review.Snapshot, error) {
	var err error
	if options.FetchThreads {
		threads, threadErr := service.loadRemoteThreadsWithLedgerCPU(
			ctx, details, ledger, runCPU,
		)
		if threadErr != nil {
			if cancellationErr := context.Cause(ctx); cancellationErr != nil {
				return review.Snapshot{}, cancellationErr
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
			return review.Snapshot{}, fmt.Errorf("retain GitHub review thread status: %w", err)
		}
	}
	if options.FetchPending {
		pending, status, detail := service.loadPendingReviewWithLedger(
			ctx, details, ledger, runCPU,
		)
		if cancellationErr := context.Cause(ctx); cancellationErr != nil {
			return review.Snapshot{}, cancellationErr
		}
		err = runCPU(ctx, func() error {
			switch status {
			case review.PendingReviewReady:
				scratchBytes := snapshot.PendingReviewInstallationBytes(pending)
				if reserveErr := ledger.reserve(scratchBytes); reserveErr != nil {
					return reserveErr
				}
				var installErr error
				snapshot, installErr = snapshot.TakePendingReview(pending)
				ledger.release(scratchBytes)
				return installErr
			case review.PendingReviewAbsent, review.PendingReviewUnavailable,
				review.PendingReviewLimited, review.PendingReviewAmbiguous:
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
			return review.Snapshot{}, fmt.Errorf("retain GitHub pending review status: %w", err)
		}
	}
	return snapshot, nil
}

func remoteThreadFailureStatus(err error) review.RemoteThreadStatus {
	var limited *LimitError
	if errors.As(err, &limited) || errors.Is(err, ErrSourceLimited) {
		return review.RemoteThreadsLimited
	}
	return review.RemoteThreadsUnavailable
}

func (service *Service) preparePullRequestSnapshot(
	ctx context.Context,
	runCPU CPUAdmission,
	repository *rootedRepository,
	details PullRequestDetails,
	payload pullRequestPayload,
	options OpenOptions,
) (review.Snapshot, revision, error) {
	baseCommit, err := service.resolveCommit(ctx, repository, RevisionRef("refs/rvw/base"))
	if err != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("validate fetched base: %w", err)
	}
	if baseCommit != details.BaseOID {
		return review.Snapshot{}, revision{}, fmt.Errorf(
			"%w: pull-request base changed while opening; retry explicitly",
			ErrSourceStale,
		)
	}
	headCommit, err := service.resolveCommit(ctx, repository, RevisionRef("refs/rvw/head"))
	if err != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("validate fetched head: %w", err)
	}
	if headCommit != details.HeadOID {
		return review.Snapshot{}, revision{}, fmt.Errorf(
			"%w: pull-request head changed while opening; retry explicitly",
			ErrSourceStale,
		)
	}
	mergeBase, err := service.mergeBase(ctx, repository, baseCommit, headCommit)
	if err != nil {
		return review.Snapshot{}, revision{}, err
	}
	baseTree, err := service.resolveTree(ctx, repository, nil, RevisionRef(mergeBase.String()))
	if err != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("resolve pull-request base tree: %w", err)
	}
	headTree, err := service.resolveTree(ctx, repository, nil, RevisionRef(headCommit.String()))
	if err != nil {
		return review.Snapshot{}, revision{}, fmt.Errorf("resolve pull-request head tree: %w", err)
	}
	files, err := service.diffMetadata(ctx, repository, nil, baseTree, headTree, runCPU)
	if err != nil {
		return review.Snapshot{}, revision{}, err
	}
	if uint64(len(files)) != payload.ChangedFiles {
		return review.Snapshot{}, revision{}, fmt.Errorf(
			"%w: pull-request metadata changed while opening: API reports %d files, Git reports %d",
			ErrSourceStale,
			payload.ChangedFiles,
			len(files),
		)
	}
	options.Gitignore = false
	options.RVWIgnore = false
	sourceID, err := service.newSourceID()
	if err != nil {
		return review.Snapshot{}, revision{}, err
	}
	snapshot, err := service.snapshot(
		ctx,
		runCPU,
		sourceID,
		repository,
		nil,
		files,
		options,
	)
	if err != nil {
		return review.Snapshot{}, revision{}, err
	}
	return snapshot, revision{
		kind:       revisionTree,
		repository: repository,
		base:       baseTree,
		head:       headTree,
	}, nil
}

func (service *Service) currentGitHubRepository(
	ctx context.Context,
	runCPU CPUAdmission,
) (OwnerRepository, error) {
	result, err := service.runGH(
		ctx,
		[]string{ghRepo, "view", "--json", jsonFieldNameWithOwner},
		256<<10,
	)
	if err != nil {
		return OwnerRepository{}, fmt.Errorf("resolve GitHub repository: %w", err)
	}
	name, err := decodeRepositoryJSON(ctx, result.Stdout, runCPU)
	if err != nil {
		return OwnerRepository{}, fmt.Errorf("decode GitHub repository: %w", err)
	}
	return NewOwnerRepository(name)
}

func (service *Service) pullRequestMetadata(
	ctx context.Context,
	repository OwnerRepository,
	number PullRequestNumber,
	runCPU CPUAdmission,
) (pullRequestPayload, error) {
	endpoint := "repos/" + repository.String() + "/pulls/" + strconv.FormatUint(number.Uint64(), 10)
	result, err := service.runGH(ctx, []string{ghAPI, endpoint}, 1<<20)
	if err != nil {
		return pullRequestPayload{}, fmt.Errorf("read pull-request metadata: %w", err)
	}
	payload, err := decodePullRequestJSON(ctx, result.Stdout, runCPU)
	if err != nil {
		return pullRequestPayload{}, fmt.Errorf("decode pull-request metadata: %w", err)
	}
	return payload, nil
}

func (service *Service) fetchPullRequest(
	ctx context.Context,
	workspace string,
	details PullRequestDetails,
) (RepositoryPath, error) {
	barePath := filepath.Join(workspace, "repository.git")
	if _, err := service.runGit(
		ctx,
		workspace,
		nil,
		[]string{"init", "--bare", barePath},
		nil,
		256<<10,
	); err != nil {
		return "", fmt.Errorf("initialize pull-request object store: %w", err)
	}
	bare := RepositoryPath(barePath)
	baseSpec := "+refs/heads/" + details.BaseRef.String() + ":refs/rvw/base"
	headSpec := "+" + pullReference(details.Number) + ":refs/rvw/head"
	cloneURL := "https://github.com/" + details.Repository.String() + ".git"
	if _, err := service.runGit(
		ctx,
		bare.String(),
		nil,
		[]string{
			"fetch", "--no-tags", "--quiet", "--filter=blob:none", "--depth=1000",
			cloneURL, baseSpec, headSpec,
		},
		nil,
		256<<10,
	); err != nil {
		return "", fmt.Errorf("fetch pull-request revisions: %w", err)
	}
	return bare, nil
}

func (service *Service) resolveCommit(
	ctx context.Context,
	repository *rootedRepository,
	ref RevisionRef,
) (review.ObjectID, error) {
	result, err := service.runGitWithInputIn(
		ctx,
		repository.gitDirectory(),
		nil,
		[]string{gitRevParse, gitVerify, gitEndOfOptions, ref.String() + "^{commit}"},
		nil,
		nil,
		64<<10,
	)
	if err != nil {
		return "", err
	}
	return parseObjectID(result.Stdout)
}

func (service *Service) mergeBase(
	ctx context.Context,
	repository *rootedRepository,
	base, head review.ObjectID,
) (review.ObjectID, error) {
	result, err := service.runGitWithInputIn(
		ctx,
		repository.gitDirectory(),
		nil,
		[]string{"merge-base", base.String(), head.String()},
		nil,
		[]int{0, 1},
		64<<10,
	)
	if err != nil {
		return "", fmt.Errorf("resolve pull-request merge base: %w", err)
	}
	if result.ExitCode != 0 {
		return "", &LimitError{Resource: LimitPullRequestDepth, Limit: 1_000}
	}
	return parseObjectID(result.Stdout)
}

func (service *Service) runGH(
	ctx context.Context,
	arguments []string,
	stdoutLimit int64,
) (subprocess.Result, error) {
	return service.runner.Run(ctx, subprocess.Request{
		Name:            "gh",
		Args:            arguments,
		Dir:             service.cwd.String(),
		Env:             map[string]string{localeAllKey: "C", localeLanguageKey: "C", ghPromptDisabled: "1"},
		StdoutLimit:     stdoutLimit,
		StderrLimit:     MaxCommandStderrBytes,
		OperationDetail: "gh " + strings.Join(arguments[:min(2, len(arguments))], " "),
	})
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
	baseOID, err := review.NewObjectID(payload.Base.SHA)
	if err != nil {
		return PullRequestDetails{}, fmt.Errorf("validate pull-request base object: %w", err)
	}
	headOID, err := review.NewObjectID(payload.Head.SHA)
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
		Repository:         repository,
		Number:             number,
		Title:              Title(payload.Title),
		BaseOID:            baseOID,
		HeadOID:            headOID,
		BaseRef:            baseRef,
		HeadRef:            headRef,
		FilesAPICapReached: payload.ChangedFiles >= githubFilesAPICap,
		URL:                GitHubURL(expectedURL),
		State:              state,
		Draft:              payload.Draft,
		Author:             author,
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
