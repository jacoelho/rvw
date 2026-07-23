package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/subprocess"
)

// MaxCommandStderrBytes bounds stderr captured by one source-owned command.
const MaxCommandStderrBytes int64 = subprocess.DefaultStderrLimit

// MaxCommandErrorConstructionBytes bounds captured stderr plus the terminal-safe
// retained and formatted error representations for one source-owned command.
const MaxCommandErrorConstructionBytes int64 = subprocess.MaxErrorConstructionBytes

const (
	// MaxPatchCaptureConstructionBytes bounds simultaneous stdout and stderr
	// capture growth while Git is producing one patch.
	MaxPatchCaptureConstructionBytes int64 = review.MaxPatchBytes + review.MaxPatchBytes/2 +
		subprocess.DefaultStderrLimit + subprocess.DefaultStderrLimit/2
	// MaxPatchErrorConstructionBytes bounds retained stdout while stderr is
	// converted into one terminal-safe command error.
	MaxPatchErrorConstructionBytes int64 = review.MaxPatchBytes + MaxCommandErrorConstructionBytes
	// MaxPatchResultConstructionBytes bounds every non-overlapping patch result
	// phase. App reservation integration derives from this owner-level maximum.
	MaxPatchResultConstructionBytes int64 = max(
		MaxPatchCaptureConstructionBytes,
		MaxPatchErrorConstructionBytes,
		review.MaxPreparedPatchConstructionBytes,
		review.MaxPreparedPatchRetainedBytes,
	)
)

const (
	gitCommand                       = "git"
	gitNoExtDiff                     = "--no-ext-diff"
	gitNoTextconv                    = "--no-textconv"
	gitRaw                           = "--raw"
	gitDiff                          = "diff"
	gitCached                        = "--cached"
	gitNoAbbrev                      = "--no-abbrev"
	gitNoRenames                     = "--no-renames"
	gitStdin                         = "--stdin"
	gitLSFiles                       = "ls-files"
	ghAPI                            = "api"
	ghGraphQL                        = "graphql"
	ghPromptDisabled                 = "GH_PROMPT_DISABLED"
	gitRevParse                      = "rev-parse"
	gitVerify                        = "--verify"
	gitEndOfOptions                  = "--end-of-options"
	localeAllKey                     = "LC_ALL"
	localeLanguageKey                = "LANG"
	ghPaginate                       = "--paginate"
	ghSlurp                          = "--slurp"
	gitStage                         = "--stage"
	gitUpdateIndex                   = "update-index"
	gitIndexInfo                     = "--index-info"
	gitHeadReference                 = "HEAD"
	gitDirectoryKey                  = "GIT_DIR"
	gitWorkTreeKey                   = "GIT_WORK_TREE"
	gitObjectDirectoryKey            = "GIT_OBJECT_DIRECTORY"
	gitAlternateObjectDirectoriesKey = "GIT_ALTERNATE_OBJECT_DIRECTORIES"
	gitIndexFileKey                  = "GIT_INDEX_FILE"
	githubPending                    = "PENDING"
	githubSubjectLine                = "line"
	githubSideLeft                   = "LEFT"
	githubSideRight                  = "RIGHT"
	graphqlReviewID                  = "pullRequestReviewId"
	graphqlBody                      = "body"
	graphqlClientID                  = "clientMutationId"
	graphqlPath                      = "path"
	graphqlNull                      = "null"
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
func (directory WorkingDirectory) String() string {
	return string(directory)
}

// Service represents service.
type Service struct {
	runner      *subprocess.Runner
	repository  *RepositoryPath
	discovering chan struct{}
	cwd         WorkingDirectory
	nextSource  uint64
	mu          sync.Mutex
}

// NewService constructs and validates service.
func NewService(cwd WorkingDirectory) (*Service, error) {
	validated, err := NewWorkingDirectory(cwd.String())
	if err != nil {
		return nil, errors.New("source service requires a working directory")
	}
	return &Service{runner: subprocess.NewRunner(), cwd: validated}, nil
}

// Escalate performs the escalate operation.
func (service *Service) Escalate() {
	service.runner.Escalate()
}

// Close performs the close operation.
func (service *Service) Close(ctx context.Context) error {
	return service.runner.Shutdown(ctx)
}

// Open performs the open operation.
func (service *Service) Open(
	ctx context.Context,
	target Target,
	options OpenOptions,
) (document *Document, returnErr error) {
	return service.openWithCPU(ctx, target, options, directCPUAdmission)
}

func (service *Service) openWithCPU(
	ctx context.Context,
	target Target,
	options OpenOptions,
	runCPU CPUAdmission,
) (document *Document, returnErr error) {
	defer func() {
		returnErr = sourceFailure(OperationOpenSource, returnErr)
	}()
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

// RepositoryRoot performs the repository root operation.
func (service *Service) RepositoryRoot(ctx context.Context) (RepositoryPath, error) {
	for {
		service.mu.Lock()
		if service.repository != nil {
			root := *service.repository
			service.mu.Unlock()
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

		root, err := service.discoverRepository(ctx)
		service.mu.Lock()
		if err == nil {
			service.repository = new(root)
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
	repository, err := openRootedRepository(root)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil && repository != nil {
			returnErr = errors.Join(returnErr, repository.Close())
		}
	}()

	cleanupPath, environment, err := service.privateObjectEnvironment(ctx, repository)
	if err != nil {
		return nil, err
	}
	var base, head review.ObjectID
	var details LocalDetails
	revisionType := revisionTree
	if utf8.ValidString(root.String()) {
		details.Repo = new(root)
	}

	switch target.Kind() {
	case TargetWorktree, TargetStaged:
		base, err = service.headTree(ctx, repository, environment)
		if err != nil {
			return nil, err
		}
		baseRef := RevisionRef(base.String())
		details.Base = new(baseRef)

		if target.Kind() == TargetWorktree {
			contents := trackedWorktree
			if options.IncludeUntracked {
				contents = worktreeWithUntracked
			}
			head, err = service.synthesizeWorktree(
				ctx,
				repository,
				environment,
				base,
				contents,
				cleanupPath,
				runCPU,
			)
			details.Kind = SourceLocalWorktree
			headRef := RevisionRef("worktree")
			details.Head = new(headRef)
		} else {
			environment, err = service.copyIndex(ctx, repository, environment, cleanupPath)
			if err != nil {
				return nil, err
			}
			head = base
			revisionType = revisionIndex
			details.Kind = SourceLocalStaged
			headRef := RevisionRef("index")
			details.Head = new(headRef)
		}
		if err != nil {
			return nil, err
		}
	case TargetRange:
		baseRef, headRef, ok := target.Range()
		if !ok {
			return nil, fmt.Errorf("%w: missing range", ErrInvalidTarget)
		}
		base, err = service.resolveTree(ctx, repository, environment, baseRef)
		if err != nil {
			return nil, fmt.Errorf("resolve range base: %w", err)
		}
		head, err = service.resolveTree(ctx, repository, environment, headRef)
		if err != nil {
			return nil, fmt.Errorf("resolve range head: %w", err)
		}
		details.Kind = SourceLocalRange
		details.Base = new(baseRef)
		details.Head = new(headRef)
	default:
		return nil, fmt.Errorf("%w: target is not local", ErrInvalidTarget)
	}

	var files []diffRecord
	if revisionType == revisionIndex {
		files, err = service.diffIndexMetadata(ctx, repository, environment, base, runCPU)
	} else {
		files, err = service.diffMetadata(ctx, repository, environment, base, head, runCPU)
	}
	if err != nil {
		return nil, err
	}
	sourceID, err := service.newSourceID()
	if err != nil {
		return nil, err
	}
	snapshot, err := service.snapshot(
		ctx, runCPU, sourceID, repository, environment, files, options,
	)
	if err != nil {
		return nil, err
	}
	document := newLocalDocument(snapshot, target, options, details, revision{
		kind:        revisionType,
		repository:  repository,
		base:        base,
		head:        head,
		environment: environment,
		cleanupPath: cleanupPath,
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

func (service *Service) discoverRepository(ctx context.Context) (RepositoryPath, error) {
	result, err := service.runGit(ctx, service.cwd.String(), nil, []string{gitRevParse, "--show-toplevel"}, nil, 64<<10)
	if err != nil {
		return "", fmt.Errorf("discover Git repository: %w", err)
	}
	value, err := parseCommandScalar(result.Stdout)
	if err != nil {
		return "", fmt.Errorf("parse Git repository: %w", err)
	}
	path, err := NewRepositoryPath(value)
	if err != nil {
		return "", fmt.Errorf("validate Git repository: %w", err)
	}
	return path, nil
}

func (service *Service) privateObjectEnvironment(
	ctx context.Context,
	repository *rootedRepository,
) (string, map[string]string, error) {
	workspace, err := os.MkdirTemp("", "rvw-source-")
	if err != nil {
		return "", nil, fmt.Errorf("create source workspace: %w", err)
	}
	objects := filepath.Join(workspace, "objects")
	if mkdirErr := os.Mkdir(objects, 0o700); mkdirErr != nil {
		return "", nil, cleanupWorkspaceError(
			workspace,
			fmt.Errorf("create source object store: %w", mkdirErr),
		)
	}

	result, err := service.runGitWithInputIn(
		ctx,
		repository.gitDirectory(),
		nil,
		[]string{gitRevParse, "--git-path", "objects"},
		nil,
		nil,
		64<<10,
	)
	if err != nil {
		return "", nil, cleanupWorkspaceError(
			workspace,
			fmt.Errorf("locate Git objects: %w", err),
		)
	}
	alternate, err := parseCommandScalar(result.Stdout)
	if err != nil {
		return "", nil, cleanupWorkspaceError(
			workspace,
			fmt.Errorf("parse Git object path: %w", err),
		)
	}
	if !filepath.IsAbs(alternate) {
		alternate = filepath.Join(repository.path.String(), alternate)
	}
	alternate, err = filepath.Abs(alternate)
	if err != nil {
		return "", nil, cleanupWorkspaceError(
			workspace,
			fmt.Errorf("resolve Git object path: %w", err),
		)
	}
	gitDirectoryResult, err := service.runGitWithInputIn(
		ctx,
		repository.gitDirectory(),
		nil,
		[]string{gitRevParse, "--absolute-git-dir"},
		nil,
		nil,
		64<<10,
	)
	if err != nil {
		return "", nil, cleanupWorkspaceError(
			workspace,
			fmt.Errorf("locate Git directory: %w", err),
		)
	}
	gitDirectoryPath, err := parseCommandScalar(gitDirectoryResult.Stdout)
	if err != nil {
		return "", nil, cleanupWorkspaceError(
			workspace,
			fmt.Errorf("parse Git directory: %w", err),
		)
	}

	//nolint:gosec // objects is the private object directory created in this workspace above.
	objectDirectory, err := os.Open(objects)
	if err != nil {
		return "", nil, cleanupWorkspaceError(workspace, fmt.Errorf("pin private object store: %w", err))
	}
	//nolint:gosec // alternate is the Git-resolved object-store capability intentionally pinned here.
	alternateDirectory, err := os.Open(alternate)
	if err != nil {
		return "", nil, errors.Join(
			cleanupWorkspaceError(workspace, fmt.Errorf("pin Git object store: %w", err)),
			objectDirectory.Close(),
		)
	}
	//nolint:gosec // workspace is the private directory created by this operation above.
	workspaceDirectory, err := os.Open(workspace)
	if err != nil {
		return "", nil, errors.Join(
			cleanupWorkspaceError(workspace, fmt.Errorf("pin source workspace: %w", err)),
			objectDirectory.Close(),
			alternateDirectory.Close(),
		)
	}
	//nolint:gosec // gitDirectoryPath is Git's resolved repository directory intentionally pinned here.
	gitDirectory, err := os.Open(gitDirectoryPath)
	if err != nil {
		return "", nil, errors.Join(
			cleanupWorkspaceError(workspace, fmt.Errorf("pin Git directory: %w", err)),
			objectDirectory.Close(),
			alternateDirectory.Close(),
			workspaceDirectory.Close(),
		)
	}
	repository.objects = objectDirectory
	repository.alternate = alternateDirectory
	repository.workspace = workspaceDirectory
	repository.gitdir = gitDirectory
	repository.cleanup = workspace
	environment, err := repository.objectEnvironment()
	if err != nil {
		return "", nil, errors.Join(err, repository.Close())
	}
	return workspace, environment, nil
}

func cleanupWorkspaceError(workspace string, cause error) error {
	return errors.Join(cause, os.RemoveAll(workspace))
}

func (service *Service) headTree(
	ctx context.Context,
	repository *rootedRepository,
	environment map[string]string,
) (review.ObjectID, error) {
	result, err := service.runGitWithInputIn(
		ctx,
		repository.gitDirectory(),
		environment,
		[]string{gitRevParse, gitVerify, gitEndOfOptions, "HEAD^{tree}"},
		nil,
		[]int{0, 128},
		64<<10,
	)
	if err != nil {
		return "", fmt.Errorf("resolve HEAD tree: %w", err)
	}
	if result.ExitCode == 0 {
		return parseObjectID(result.Stdout)
	}

	symbolic, err := service.runGitWithInputIn(
		ctx,
		repository.gitDirectory(),
		environment,
		[]string{"symbolic-ref", "-q", gitHeadReference},
		nil,
		[]int{0, 1},
		64<<10,
	)
	if err != nil {
		return "", fmt.Errorf("inspect unborn HEAD: %w", err)
	}
	if symbolic.ExitCode != 0 {
		return "", errors.New("HEAD does not resolve to a tree and is not an unborn symbolic branch")
	}
	empty, err := service.runGitWithInputIn(
		ctx,
		repository.gitDirectory(),
		environment,
		[]string{"hash-object", "-t", "tree", gitStdin},
		nil,
		nil,
		64<<10,
	)
	if err != nil {
		return "", fmt.Errorf("create empty base tree: %w", err)
	}
	return parseObjectID(empty.Stdout)
}

type worktreeContents uint8

const (
	trackedWorktree worktreeContents = iota
	worktreeWithUntracked
)

func (service *Service) synthesizeWorktree(
	ctx context.Context,
	repository *rootedRepository,
	environment map[string]string,
	base review.ObjectID,
	contents worktreeContents,
	workspace string,
	runCPU CPUAdmission,
) (review.ObjectID, error) {
	return service.synthesizeRawWorktree(
		ctx,
		repository,
		environment,
		base,
		contents,
		workspace,
		runCPU,
	)
}

func (service *Service) copyIndex(
	ctx context.Context,
	repository *rootedRepository,
	environment map[string]string,
	workspace string,
) (privateEnvironment map[string]string, returnErr error) {
	entries, err := service.runGitToSpoolIn(
		ctx,
		repository.gitDirectory(),
		environment,
		[]string{gitLSFiles, gitStage, "-z"},
		stageInventoryWireLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read staged index: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, entries.Close())
	}()
	privateEnvironment = cloneEnvironment(environment)
	indexPath := filepath.Join(workspace, "index")
	indexDescriptorPath, err := repository.indexPath()
	if err != nil {
		return nil, err
	}
	privateEnvironment[gitIndexFileKey] = indexDescriptorPath
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("prepare private staged index: %w", err)
	}
	if err := service.runGitFromSpoolIn(
		ctx,
		repository.gitDirectory(),
		privateEnvironment,
		[]string{gitUpdateIndex, "-z", gitIndexInfo},
		entries,
		stageInventoryWireLimit,
		64<<10,
	); err != nil {
		return nil, fmt.Errorf("copy staged index: %w", err)
	}
	return privateEnvironment, nil
}

func (service *Service) resolveTree(
	ctx context.Context,
	repository *rootedRepository,
	environment map[string]string,
	ref RevisionRef,
) (review.ObjectID, error) {
	result, err := service.runGitWithInputIn(
		ctx,
		repository.gitDirectory(),
		environment,
		[]string{gitRevParse, gitVerify, gitEndOfOptions, ref.String() + "^{tree}"},
		nil,
		nil,
		64<<10,
	)
	if err != nil {
		return "", err
	}
	return parseObjectID(result.Stdout)
}

func (service *Service) runGit(
	ctx context.Context,
	directory string,
	environment map[string]string,
	arguments []string,
	acceptedExits []int,
	stdoutLimit int64,
) (subprocess.Result, error) {
	return service.runGitWithInputIn(
		ctx,
		gitDirectory{path: directory},
		environment,
		arguments,
		nil,
		acceptedExits,
		stdoutLimit,
	)
}

type gitDirectory struct {
	file       *os.File
	repository *rootedRepository
	path       string
	borrowed   []*os.File
}

func (service *Service) runGitWithInputIn(
	ctx context.Context,
	directory gitDirectory,
	environment map[string]string,
	arguments []string,
	stdin []byte,
	acceptedExits []int,
	stdoutLimit int64,
) (subprocess.Result, error) {
	merged := baseGitEnvironment()
	maps.Copy(merged, environment)
	if directory.repository != nil {
		if err := directory.repository.refreshEnvironment(merged); err != nil {
			return subprocess.Result{}, err
		}
	}
	return service.runner.Run(ctx, subprocess.Request{
		Name:            gitCommand,
		Args:            arguments,
		Dir:             directory.path,
		DirFile:         directory.file,
		BorrowedFiles:   directory.borrowed,
		Env:             merged,
		Stdin:           stdin,
		StdoutLimit:     stdoutLimit,
		StderrLimit:     MaxCommandStderrBytes,
		AcceptedExits:   acceptedExits,
		OperationDetail: "git " + strings.Join(arguments[:min(2, len(arguments))], " "),
	})
}

func baseGitEnvironment() map[string]string {
	return map[string]string{
		localeAllKey:          "C",
		localeLanguageKey:     "C",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_OPTIONAL_LOCKS":  "0",
	}
}

func cloneEnvironment(environment map[string]string) map[string]string {
	clone := make(map[string]string, len(environment))
	maps.Copy(clone, environment)
	return clone
}

func parseObjectID(value []byte) (review.ObjectID, error) {
	text, err := parseCommandScalar(value)
	if err != nil {
		return "", fmt.Errorf("parse Git object ID: %w", err)
	}
	id, err := review.NewObjectID(text)
	if err != nil {
		return "", fmt.Errorf("validate Git object ID %q: %w", boundedText(text, 128), err)
	}
	return id, nil
}

func parseCommandScalar(value []byte) (string, error) {
	if len(value) < 2 || value[len(value)-1] != '\n' {
		return "", errors.New("command scalar must be nonempty and LF-terminated")
	}
	value = value[:len(value)-1]
	if len(value) == 0 || bytes.IndexByte(value, 0) >= 0 {
		return "", errors.New("command scalar is empty or contains NUL")
	}
	return string(value), nil
}

func validateOpenOptions(options OpenOptions) error {
	if len(options.Include)+len(options.Exclude) > 4_096 {
		return errors.New("source filter patterns exceed 4096")
	}
	for _, pattern := range append(append([]FilterPattern{}, options.Include...), options.Exclude...) {
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
