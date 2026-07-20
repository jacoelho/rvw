package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/subprocess"
)

const githubFilesAPICap = 3_000

type pullRequestPayload struct {
	Number       uint64  `json:"number"`
	Title        string  `json:"title"`
	HTMLURL      string  `json:"html_url"`
	State        string  `json:"state"`
	MergedAt     *string `json:"merged_at"`
	Draft        bool    `json:"draft"`
	ChangedFiles uint64  `json:"changed_files"`
	User         *struct {
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
}

func (service *Service) openPullRequest(
	ctx context.Context,
	target Target,
	options OpenOptions,
) (_ *Document, returnErr error) {
	selector, ok := target.PullRequest()
	if !ok {
		return nil, fmt.Errorf("%w: missing pull-request selector", ErrInvalidTarget)
	}
	repository, ok := selector.Repository()
	if !ok {
		var err error
		repository, err = service.currentGitHubRepository(ctx)
		if err != nil {
			return nil, err
		}
	}
	payload, err := service.pullRequestMetadata(ctx, repository, selector.Number())
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
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, os.RemoveAll(workspace))
		}
	}()
	bare, err := service.fetchPullRequest(ctx, workspace, details)
	if err != nil {
		return nil, err
	}

	baseCommit, err := service.resolveCommit(ctx, bare, RevisionRef("refs/rvw/base"))
	if err != nil {
		return nil, fmt.Errorf("validate fetched base: %w", err)
	}
	if baseCommit != details.BaseOID {
		return nil, fmt.Errorf("%w: pull-request base changed while opening; retry explicitly", ErrSourceStale)
	}
	headCommit, err := service.resolveCommit(ctx, bare, RevisionRef("refs/rvw/head"))
	if err != nil {
		return nil, fmt.Errorf("validate fetched head: %w", err)
	}
	if headCommit != details.HeadOID {
		return nil, fmt.Errorf("%w: pull-request head changed while opening; retry explicitly", ErrSourceStale)
	}
	mergeBase, err := service.mergeBase(ctx, bare, baseCommit, headCommit)
	if err != nil {
		return nil, err
	}
	baseTree, err := service.resolveTree(ctx, bare, nil, RevisionRef(mergeBase.String()))
	if err != nil {
		return nil, fmt.Errorf("resolve pull-request base tree: %w", err)
	}
	headTree, err := service.resolveTree(ctx, bare, nil, RevisionRef(headCommit.String()))
	if err != nil {
		return nil, fmt.Errorf("resolve pull-request head tree: %w", err)
	}
	files, err := service.diffMetadata(ctx, bare, nil, baseTree, headTree)
	if err != nil {
		return nil, err
	}
	if uint64(len(files)) != payload.ChangedFiles {
		return nil, fmt.Errorf(
			"%w: pull-request metadata changed while opening: API reports %d files, Git reports %d",
			ErrSourceStale,
			payload.ChangedFiles,
			len(files),
		)
	}

	prOptions := options
	prOptions.Gitignore = false
	prOptions.RVWIgnore = false
	sourceID, err := service.newSourceID()
	if err != nil {
		return nil, err
	}
	snapshot, err := service.snapshot(ctx, sourceID, bare, nil, files, prOptions)
	if err != nil {
		return nil, err
	}
	if options.FetchThreads {
		threads, err := service.loadRemoteThreads(ctx, details)
		if err != nil {
			status := review.RemoteThreadsUnavailable
			var limited *LimitError
			if errors.As(err, &limited) {
				status = review.RemoteThreadsLimited
			}
			snapshot, err = snapshot.WithRemoteThreadFailure(
				status,
				boundedPublicationDetail(err.Error()),
			)
		} else {
			snapshot, err = snapshot.WithRemoteThreads(threads)
			if err != nil {
				return nil, fmt.Errorf("validate GitHub review threads: %w", err)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("retain GitHub review thread status: %w", err)
		}
	}
	return newPullRequestDocument(snapshot, target, options, details, revision{
		kind:        revisionTree,
		repository:  bare,
		base:        baseTree,
		head:        headTree,
		cleanupPath: workspace,
	}), nil
}

func (service *Service) currentGitHubRepository(ctx context.Context) (OwnerRepository, error) {
	result, err := service.runGH(
		ctx,
		[]string{"repo", "view", "--json", "nameWithOwner"},
		256<<10,
	)
	if err != nil {
		return OwnerRepository{}, fmt.Errorf("resolve GitHub repository: %w", err)
	}
	if !utf8.Valid(result.Stdout) {
		return OwnerRepository{}, errors.New("gh repository output is not valid UTF-8")
	}
	var payload struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal(result.Stdout, &payload); err != nil {
		return OwnerRepository{}, fmt.Errorf("decode GitHub repository: %w", err)
	}
	return NewOwnerRepository(payload.NameWithOwner)
}

func (service *Service) pullRequestMetadata(
	ctx context.Context,
	repository OwnerRepository,
	number PullRequestNumber,
) (pullRequestPayload, error) {
	endpoint := "repos/" + repository.String() + "/pulls/" + strconv.FormatUint(number.Uint64(), 10)
	result, err := service.runGH(ctx, []string{"api", endpoint}, 1<<20)
	if err != nil {
		return pullRequestPayload{}, fmt.Errorf("read pull-request metadata: %w", err)
	}
	if !utf8.Valid(result.Stdout) {
		return pullRequestPayload{}, errors.New("GitHub metadata is not valid UTF-8")
	}
	var payload pullRequestPayload
	if err := json.Unmarshal(result.Stdout, &payload); err != nil {
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
	bare, _ := NewRepositoryPath(barePath)
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
	repository RepositoryPath,
	ref RevisionRef,
) (review.ObjectID, error) {
	result, err := service.runGit(
		ctx,
		repository.String(),
		nil,
		[]string{"rev-parse", "--verify", "--end-of-options", ref.String() + "^{commit}"},
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
	repository RepositoryPath,
	base, head review.ObjectID,
) (review.ObjectID, error) {
	result, err := service.runGit(
		ctx,
		repository.String(),
		nil,
		[]string{"merge-base", base.String(), head.String()},
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
		Env:             map[string]string{"LC_ALL": "C", "LANG": "C", "GH_PROMPT_DISABLED": "1"},
		StdoutLimit:     stdoutLimit,
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
		if _, err := time.Parse(time.RFC3339, *payload.MergedAt); err != nil {
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
