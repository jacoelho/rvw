package source

import (
	"context"
	"errors"

	"github.com/jacoelho/rvw/internal/subprocess"
)

func (service *Service) runRootedGit(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	arguments []string,
	acceptedExits []int,
	stdoutLimit int64,
) (subprocess.Result, error) {
	return service.runGitWithInputIn(
		ctx, worktree.gitDirectory(), environment, arguments, nil, acceptedExits, stdoutLimit,
	)
}

func (service *Service) runRootedGitWithInput(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	arguments []string,
	stdin []byte,
	stdoutLimit int64,
) (subprocess.Result, error) {
	return service.runGitWithInputIn(
		ctx, worktree.gitDirectory(), environment, arguments, stdin, nil, stdoutLimit,
	)
}

func (service *Service) runRootedGitToSpool(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	arguments []string,
	wireLimit int64,
) (*commandSpool, error) {
	return service.runGitToSpoolIn(
		ctx, worktree.gitDirectory(), environment, arguments, wireLimit,
	)
}

func (service *Service) runRootedGitFromSpool(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	arguments []string,
	spool *commandSpool,
	stdinLimit int64,
	stdoutLimit int64,
) error {
	return service.runGitFromSpoolIn(
		ctx,
		worktree.gitDirectory(),
		environment,
		arguments,
		spool,
		stdinLimit,
		stdoutLimit,
	)
}

func (service *Service) runRootedGitAt(
	ctx context.Context,
	worktree *rootedRepository,
	path string,
	environment map[string]string,
	arguments []string,
	acceptedExits []int,
	stdoutLimit int64,
) (result subprocess.Result, returnErr error) {
	directory, err := worktree.filesystem.Open(path)
	if err != nil {
		return subprocess.Result{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, directory.Close())
	}()
	return service.runGitWithInputIn(
		ctx,
		gitDirectory{file: directory},
		environment,
		arguments,
		nil,
		acceptedExits,
		stdoutLimit,
	)
}
