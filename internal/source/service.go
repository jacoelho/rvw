// Package source acquires and materializes Git review sources without invoking
// external Git or GitHub command-line programs.
package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
)

const (
	// MaxSourceErrorConstructionBytes bounds one retained boundary error.
	MaxSourceErrorConstructionBytes int64 = 64 << 10
	// MaxPatchResultConstructionBytes bounds a native prepared patch result.
	MaxPatchResultConstructionBytes int64 = max(
		review.MaxPreparedPatchConstructionBytes,
		review.MaxPreparedPatchRetainedBytes,
	)
)

const (
	githubPending     = "PENDING"
	githubSubjectLine = "line"
	githubSideLeft    = "LEFT"
	githubSideRight   = "RIGHT"

	graphqlReviewID = "pullRequestReviewId"
	graphqlBody     = "body"
	graphqlClientID = "clientMutationId"
	graphqlPath     = "path"
	graphqlNull     = "null"
)

// WorkingDirectory represents working directory.
type WorkingDirectory string

// NewWorkingDirectory constructs and validates working directory.
func NewWorkingDirectory(value string) (WorkingDirectory, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("working directory must be nonempty and contain no NUL")
	}
	return WorkingDirectory(value), nil
}

// String returns the receiver's string value.
func (directory WorkingDirectory) String() string { return string(directory) }

// Service owns local repository discovery and the GitHub client.
type Service struct {
	github               *githubClient
	repositoryDescriptor *os.File
	repository           *RepositoryPath
	discovering          chan struct{}
	cwd                  WorkingDirectory
	options              ServiceOptions
	nextSource           uint64
	mu                   sync.Mutex
}

// NewService constructs and validates service.
func NewService(cwd WorkingDirectory, options ServiceOptions) (*Service, error) {
	validated, err := NewWorkingDirectory(cwd.String())
	if err != nil {
		return nil, errors.New("source service requires a working directory")
	}
	ownedOptions, err := options.clone()
	if err != nil {
		return nil, err
	}
	return &Service{
		github: newGitHubClient(ownedOptions.GitHubToken),
		cwd:    validated, options: ownedOptions,
	}, nil
}

// Close releases idle network connections.
func (service *Service) Close(context.Context) error {
	if service == nil {
		return nil
	}
	service.mu.Lock()
	descriptor := service.repositoryDescriptor
	service.repositoryDescriptor = nil
	service.mu.Unlock()
	service.github.close()
	if descriptor != nil {
		return descriptor.Close()
	}
	return nil
}

// Open performs the open operation.
func (service *Service) Open(
	ctx context.Context,
	target Target,
	options OpenOptions,
) (*Document, error) {
	return service.openWithCPU(ctx, target, options, directCPUAdmission)
}

func (service *Service) openWithCPU(
	ctx context.Context,
	target Target,
	options OpenOptions,
	runCPU CPUAdmission,
) (document *Document, returnErr error) {
	defer func() { returnErr = sourceFailure(OperationOpenSource, returnErr) }()
	if runCPU == nil {
		return nil, errors.New("source CPU admission is nil")
	}
	options = options.clone()
	if err := validateOpenOptions(options); err != nil {
		return nil, err
	}
	switch target.Kind() {
	case TargetWorktree, TargetStaged, TargetRange:
		return service.openLocal(ctx, target, options, runCPU)
	case TargetPullRequest:
		return service.openPullRequest(ctx, target, options, runCPU)
	default:
		return nil, fmt.Errorf("%w: unknown target kind %d", ErrInvalidTarget, target.Kind())
	}
}

// RepositoryRoot returns the descriptor-discovered repository root.
func (service *Service) RepositoryRoot(ctx context.Context) (RepositoryPath, error) {
	for {
		service.mu.Lock()
		if service.repository != nil {
			root := *service.repository
			descriptor := service.repositoryDescriptor
			service.mu.Unlock()
			if descriptor == nil {
				return "", sourceFailure(OperationDiscoverRepository, errors.New("source service is closed"))
			}
			current, err := os.Open(root.String())
			if err != nil {
				return "", sourceFailure(OperationDiscoverRepository, errors.Join(
					ErrSourceStale,
					fmt.Errorf("reopen repository root: %w", err),
				))
			}
			same, identityErr := sameDirectory(descriptor, current)
			closeErr := current.Close()
			if identityErr != nil || closeErr != nil {
				return "", sourceFailure(OperationDiscoverRepository, errors.Join(identityErr, closeErr))
			}
			if !same {
				return "", sourceFailure(OperationDiscoverRepository, fmt.Errorf(
					"%w: repository root identity changed", ErrSourceStale,
				))
			}
			return root, nil
		}
		if service.discovering != nil {
			done := service.discovering
			service.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return "", sourceFailure(OperationDiscoverRepository, context.Cause(ctx))
			}
		}
		done := make(chan struct{})
		service.discovering = done
		service.mu.Unlock()

		root, descriptor, err := discoverRepositoryRoot(ctx, service.cwd)
		service.mu.Lock()
		if err == nil {
			service.repository = new(root)
			service.repositoryDescriptor = descriptor
		} else if descriptor != nil {
			err = errors.Join(err, descriptor.Close())
		}
		service.discovering = nil
		close(done)
		service.mu.Unlock()
		if err != nil {
			return "", sourceFailure(OperationDiscoverRepository, err)
		}
		return root, nil
	}
}

func (service *Service) openLocal(
	ctx context.Context,
	target Target,
	options OpenOptions,
	runCPU CPUAdmission,
) (_ *Document, returnErr error) {
	root, err := service.RepositoryRoot(ctx)
	if err != nil {
		return nil, err
	}
	repository, err := openRepositorySession(root, service.options)
	if err != nil {
		return nil, err
	}
	service.mu.Lock()
	pinned := service.repositoryDescriptor
	service.mu.Unlock()
	if pinned == nil {
		return nil, errors.New("source service is closed")
	}
	same, identityErr := sameDirectory(pinned, repository.directory)
	if identityErr != nil || !same {
		return nil, errors.Join(
			fmt.Errorf("%w: repository root identity changed while opening", ErrSourceStale),
			identityErr,
		)
	}
	defer func() {
		if returnErr != nil && repository != nil {
			returnErr = errors.Join(returnErr, repository.Close())
		}
	}()
	base, _, files, unmerged, err := nativeLocalRecords(ctx, repository, target, options)
	if err != nil {
		return nil, err
	}
	details := LocalDetails{}
	if utf8.ValidString(root.String()) {
		details.Repo = new(root)
	}
	switch target.Kind() {
	case TargetWorktree, TargetStaged:
		baseRef := RevisionRef(base.String())
		details.Base = new(baseRef)
		if target.Kind() == TargetWorktree {
			details.Kind = SourceLocalWorktree
			headRef := RevisionRef("worktree")
			details.Head = new(headRef)
		} else {
			details.Kind = SourceLocalStaged
			headRef := RevisionRef("index")
			details.Head = new(headRef)
		}
	case TargetRange:
		baseRef, headRef, ok := target.Range()
		if !ok {
			return nil, fmt.Errorf("%w: missing range", ErrInvalidTarget)
		}
		details.Kind, details.Base, details.Head = SourceLocalRange, new(baseRef), new(headRef)
	default:
		return nil, fmt.Errorf("%w: target is not local", ErrInvalidTarget)
	}
	sourceID, err := service.newSourceID()
	if err != nil {
		return nil, err
	}
	snapshot, err := service.snapshot(ctx, runCPU, sourceID, repository, files, options)
	if err != nil {
		return nil, err
	}
	document := newLocalDocument(snapshot, target, options, details, revision{
		repository: repository, unmerged: unmerged, similarities: nativeSimilarityScores(files),
	})
	repository = nil
	return document, nil
}

func (service *Service) newSourceID() (review.SourceID, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.nextSource == ^uint64(0) {
		return 0, errors.New("source identity exhausted")
	}
	service.nextSource++
	return review.NewSourceID(service.nextSource)
}

func validateOpenOptions(options OpenOptions) error {
	if len(options.Include)+len(options.Exclude) > 4_096 {
		return errors.New("source filter patterns exceed 4096")
	}
	patterns := make([]FilterPattern, 0, len(options.Include)+len(options.Exclude))
	patterns = append(patterns, options.Include...)
	patterns = append(patterns, options.Exclude...)
	for _, pattern := range patterns {
		if _, err := NewFilterPattern(pattern.String()); err != nil {
			return err
		}
	}
	if _, err := NewIgnoreFile(options.IgnoreFile.String()); err != nil {
		return err
	}
	return nil
}

func boundedText(value string, limit int64) string {
	if int64(len(value)) <= limit {
		return value
	}
	return value[:int(limit)] + "…"
}

func pullReference(number PullRequestNumber) string {
	return "refs/pull/" + strconv.FormatUint(number.Uint64(), 10) + "/head"
}
