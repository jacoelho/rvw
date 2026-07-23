package source

import (
	"context"
	"fmt"
	"strings"

	"github.com/jacoelho/rvw/internal/review"
)

func rawWorktreeUpdates(entries []rawWorktreeEntry) ([]indexUpdate, error) {
	updates := make([]indexUpdate, 0, len(entries))
	for _, entry := range entries {
		if entry.oid == nil {
			return nil, fmt.Errorf("worktree path %q has no object identity", entry.path)
		}
		updates = append(updates, indexUpdate{
			path:   entry.path,
			mode:   entry.mode,
			object: *entry.oid,
		})
	}
	return updates, nil
}

func (service *Service) resolveRawWorktreeGitlinks(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	entries []rawWorktreeEntry,
) ([]rawWorktreeEntry, error) {
	for index := range entries {
		if entries[index].mode.String() != "160000" {
			continue
		}
		oid, err := service.worktreeGitlinkHead(
			ctx,
			worktree,
			environment,
			entries[index].path,
		)
		if err != nil {
			return nil, err
		}
		entries[index].oid = new(oid)
	}
	return entries, nil
}

func (service *Service) verifyRawWorktreeGitlinks(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	entries []rawWorktreeEntry,
) error {
	for _, entry := range entries {
		if entry.mode.String() != "160000" {
			continue
		}
		if entry.oid == nil {
			return fmt.Errorf("submodule %q has no object identity", entry.path)
		}
		current, err := service.worktreeGitlinkHead(ctx, worktree, environment, entry.path)
		if err != nil {
			return err
		}
		if current != *entry.oid {
			return fmt.Errorf("submodule %q changed while snapshotting worktree", entry.path)
		}
	}
	return nil
}

func (service *Service) worktreeGitlinkHead(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	path string,
) (review.ObjectID, error) {
	gitlinkEnvironment := cloneEnvironment(environment)
	delete(gitlinkEnvironment, gitObjectDirectoryKey)
	delete(gitlinkEnvironment, gitAlternateObjectDirectoriesKey)
	delete(gitlinkEnvironment, gitIndexFileKey)
	delete(gitlinkEnvironment, gitDirectoryKey)
	delete(gitlinkEnvironment, gitWorkTreeKey)
	result, err := service.runRootedGitAt(
		ctx,
		worktree,
		path,
		gitlinkEnvironment,
		[]string{gitRevParse, gitVerify, "HEAD^{commit}"},
		nil,
		128,
	)
	if err != nil {
		return "", fmt.Errorf(
			"worktree gitlink %q HEAD does not resolve to a commit: %w",
			path,
			err,
		)
	}
	value := strings.TrimSuffix(string(result.Stdout), "\n")
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("submodule %q returned multiple HEAD records", path)
	}
	oid, err := review.NewObjectID(value)
	if err != nil {
		return "", fmt.Errorf("validate worktree gitlink %q HEAD: %w", path, err)
	}
	return oid, nil
}

func (service *Service) applyIndexUpdates(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	base review.ObjectID,
	updates []indexUpdate,
	runCPU CPUAdmission,
) error {
	var zero string
	if err := runCPU(ctx, func() error {
		zero = strings.Repeat("0", len(base.String()))
		return context.Cause(ctx)
	}); err != nil {
		return err
	}
	for start := 0; start < len(updates); {
		var batch worktreeIndexBatch
		if err := runCPU(ctx, func() error {
			var buildErr error
			batch, buildErr = buildWorktreeIndexBatch(ctx, zero, updates, start)
			return buildErr
		}); err != nil {
			return err
		}
		if _, err := service.runRootedGitWithInput(
			ctx,
			worktree,
			environment,
			[]string{gitUpdateIndex, "-z", gitIndexInfo},
			batch.input,
			64<<10,
		); err != nil {
			return fmt.Errorf("update private worktree index: %w", err)
		}
		start = batch.next
	}
	return nil
}

func (service *Service) writeWorktreeIndexTree(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
) (review.ObjectID, error) {
	result, err := service.runRootedGit(
		ctx,
		worktree,
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
