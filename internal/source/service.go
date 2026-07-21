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

const (
	gitNoExtDiff      = "--no-ext-diff"
	gitNoTextconv     = "--no-textconv"
	gitRaw            = "--raw"
	gitDiff           = "diff"
	gitCached         = "--cached"
	gitNoAbbrev       = "--no-abbrev"
	gitNoRenames      = "--no-renames"
	gitStdin          = "--stdin"
	gitLSFiles        = "ls-files"
	ghAPI             = "api"
	gitRevParse       = "rev-parse"
	gitVerify         = "--verify"
	gitEndOfOptions   = "--end-of-options"
	localeAllKey      = "LC_ALL"
	localeLanguageKey = "LANG"
	ghPaginate        = "--paginate"
	ghSlurp           = "--slurp"
	gitStage          = "--stage"
	gitUpdateIndex    = "update-index"
	gitIndexInfo      = "--index-info"
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
	runner, err := subprocess.NewRunner(subprocess.DefaultConcurrency)
	if err != nil {
		return nil, fmt.Errorf("create source subprocess runner: %w", err)
	}
	return &Service{runner: runner, cwd: validated}, nil
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
	defer func() {
		returnErr = sourceFailure(OperationOpenSource, returnErr)
	}()
	options = options.clone()
	if err := validateOpenOptions(options); err != nil {
		return nil, err
	}
	switch target.Kind() {
	case TargetWorktree, TargetStaged, TargetRange:
		return service.openLocal(ctx, target, options)
	case TargetPullRequest:
		return service.openPullRequest(ctx, target, options)
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
) (_ *Document, returnErr error) {
	root, err := service.RepositoryRoot(ctx)
	if err != nil {
		return nil, err
	}

	var cleanupPath string
	defer func() {
		if returnErr != nil && cleanupPath != "" {
			returnErr = errors.Join(returnErr, os.RemoveAll(cleanupPath))
		}
	}()

	environment := baseGitEnvironment()
	var base, head review.ObjectID
	var details LocalDetails
	revisionType := revisionTree
	repository := root
	if utf8.ValidString(root.String()) {
		details.Repo = new(repository)
	}

	switch target.Kind() {
	case TargetWorktree, TargetStaged:
		cleanupPath, environment, err = service.privateObjectEnvironment(ctx, root)
		if err != nil {
			return nil, err
		}
		base, err = service.headTree(ctx, root, environment)
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
				root,
				environment,
				base,
				contents,
				cleanupPath,
			)
			details.Kind = SourceLocalWorktree
			headRef := RevisionRef("worktree")
			details.Head = new(headRef)
		} else {
			environment, err = service.copyIndex(ctx, root, environment, cleanupPath)
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
		base, err = service.resolveTree(ctx, root, environment, baseRef)
		if err != nil {
			return nil, fmt.Errorf("resolve range base: %w", err)
		}
		head, err = service.resolveTree(ctx, root, environment, headRef)
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
		files, err = service.diffIndexMetadata(ctx, root, environment, base)
	} else {
		files, err = service.diffMetadata(ctx, root, environment, base, head)
	}
	if err != nil {
		return nil, err
	}
	sourceID, err := service.newSourceID()
	if err != nil {
		return nil, err
	}
	snapshot, err := service.snapshot(ctx, sourceID, root, environment, files, options)
	if err != nil {
		return nil, err
	}
	return newLocalDocument(snapshot, target, options, details, revision{
		kind:        revisionType,
		repository:  root,
		base:        base,
		head:        head,
		environment: environment,
		cleanupPath: cleanupPath,
	}), nil
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
	root RepositoryPath,
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

	result, err := service.runGit(
		ctx,
		root.String(),
		nil,
		[]string{gitRevParse, "--git-path", "objects"},
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
		alternate = filepath.Join(root.String(), alternate)
	}
	alternate, err = filepath.Abs(alternate)
	if err != nil {
		return "", nil, cleanupWorkspaceError(
			workspace,
			fmt.Errorf("resolve Git object path: %w", err),
		)
	}

	environment := baseGitEnvironment()
	environment["GIT_OBJECT_DIRECTORY"] = objects
	environment["GIT_ALTERNATE_OBJECT_DIRECTORIES"] = alternate
	return workspace, environment, nil
}

func cleanupWorkspaceError(workspace string, cause error) error {
	return errors.Join(cause, os.RemoveAll(workspace))
}

func (service *Service) headTree(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
) (review.ObjectID, error) {
	result, err := service.runGit(
		ctx,
		root.String(),
		environment,
		[]string{gitRevParse, gitVerify, gitEndOfOptions, "HEAD^{tree}"},
		[]int{0, 128},
		64<<10,
	)
	if err != nil {
		return "", fmt.Errorf("resolve HEAD tree: %w", err)
	}
	if result.ExitCode == 0 {
		return parseObjectID(result.Stdout)
	}

	symbolic, err := service.runGit(
		ctx,
		root.String(),
		environment,
		[]string{"symbolic-ref", "-q", "HEAD"},
		[]int{0, 1},
		64<<10,
	)
	if err != nil {
		return "", fmt.Errorf("inspect unborn HEAD: %w", err)
	}
	if symbolic.ExitCode != 0 {
		return "", errors.New("HEAD does not resolve to a tree and is not an unborn symbolic branch")
	}
	empty, err := service.runGit(
		ctx,
		root.String(),
		environment,
		[]string{"hash-object", "-t", "tree", gitStdin},
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
	root RepositoryPath,
	environment map[string]string,
	base review.ObjectID,
	contents worktreeContents,
	workspace string,
) (review.ObjectID, error) {
	return service.synthesizeRawWorktree(
		ctx,
		root,
		environment,
		base,
		contents,
		workspace,
	)
}

func (service *Service) writeIndexTree(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
) (review.ObjectID, error) {
	result, err := service.runGit(
		ctx,
		root.String(),
		environment,
		[]string{"write-tree"},
		nil,
		64<<10,
	)
	if err != nil {
		return "", fmt.Errorf("write index tree: %w", err)
	}
	return parseObjectID(result.Stdout)
}

func (service *Service) copyIndex(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	workspace string,
) (map[string]string, error) {
	entries, err := service.runGit(
		ctx,
		root.String(),
		environment,
		[]string{gitLSFiles, gitStage, "-z"},
		nil,
		64<<20,
	)
	if err != nil {
		return nil, fmt.Errorf("read staged index: %w", err)
	}
	privateEnvironment := cloneEnvironment(environment)
	indexPath := filepath.Join(workspace, "index")
	privateEnvironment["GIT_INDEX_FILE"] = indexPath
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("prepare private staged index: %w", err)
	}
	if _, err := service.runGitWithInput(
		ctx,
		root.String(),
		privateEnvironment,
		[]string{gitUpdateIndex, "-z", gitIndexInfo},
		entries.Stdout,
		nil,
		64<<10,
	); err != nil {
		return nil, fmt.Errorf("copy staged index: %w", err)
	}
	return privateEnvironment, nil
}

func (service *Service) resolveTree(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	ref RevisionRef,
) (review.ObjectID, error) {
	result, err := service.runGit(
		ctx,
		root.String(),
		environment,
		[]string{gitRevParse, gitVerify, gitEndOfOptions, ref.String() + "^{tree}"},
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
	return service.runGitWithInput(
		ctx,
		directory,
		environment,
		arguments,
		nil,
		acceptedExits,
		stdoutLimit,
	)
}

func (service *Service) runGitWithInput(
	ctx context.Context,
	directory string,
	environment map[string]string,
	arguments []string,
	stdin []byte,
	acceptedExits []int,
	stdoutLimit int64,
) (subprocess.Result, error) {
	merged := baseGitEnvironment()
	maps.Copy(merged, environment)
	return service.runner.Run(ctx, subprocess.Request{
		Name:            "git",
		Args:            arguments,
		Dir:             directory,
		Env:             merged,
		Stdin:           stdin,
		StdoutLimit:     stdoutLimit,
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
