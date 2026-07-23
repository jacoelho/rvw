package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jacoelho/rvw/internal/review"
)

type worktreeInventoryDigests struct {
	stage        [sha256.Size]byte
	flags        [sha256.Size]byte
	untracked    [sha256.Size]byte
	hasUntracked bool
}

func (service *Service) captureInitialWorktreeInventories(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	contents worktreeContents,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) (worktreeInventoryDigests, []untrackedWorktreeRecord, int64, error) {
	flags, err := service.runRootedGitToSpool(
		ctx,
		worktree,
		environment,
		[]string{gitLSFiles, "-v", "-z", "--"},
		flagInventoryWireLimit,
	)
	if err != nil {
		return worktreeInventoryDigests{}, nil, 0, fmt.Errorf("inspect worktree index flags: %w", err)
	}
	flagDigest, err := digestAndValidateFlagInventoryContextCPU(
		ctx, flags.Reader(), ledger, runCPU,
	)
	err = errors.Join(err, flags.Close())
	if err != nil {
		return worktreeInventoryDigests{}, nil, 0, err
	}

	stage, err := service.runRootedGitToSpool(
		ctx,
		worktree,
		environment,
		[]string{gitLSFiles, gitStage, "-z"},
		stageInventoryWireLimit,
	)
	if err != nil {
		return worktreeInventoryDigests{}, nil, 0, fmt.Errorf("read live Git index: %w", err)
	}
	stageDigest, err := digestAndValidateStageInventoryContextCPU(
		ctx, stage.Reader(), ledger, runCPU,
	)
	err = errors.Join(err, stage.Close())
	if err != nil {
		return worktreeInventoryDigests{}, nil, 0, err
	}

	digests := worktreeInventoryDigests{stage: stageDigest, flags: flagDigest}
	if contents != worktreeWithUntracked {
		return digests, nil, 0, nil
	}
	untracked, err := service.runRootedGitToSpool(
		ctx,
		worktree,
		environment,
		[]string{gitLSFiles, "--others", "-z", "--"},
		untrackedInventoryWireLimit,
	)
	if err != nil {
		return worktreeInventoryDigests{}, nil, 0, fmt.Errorf("read untracked files: %w", err)
	}
	records, pathBytes, untrackedDigest, err := parseAndDigestUntrackedInventoryWithLedgerCPU(
		ctx,
		untracked.Reader(),
		ledger,
		runCPU,
	)
	err = errors.Join(err, untracked.Close())
	if err != nil {
		return worktreeInventoryDigests{}, nil, 0, err
	}
	digests.untracked = untrackedDigest
	digests.hasUntracked = true
	return digests, records, pathBytes, nil
}

func (service *Service) verifyWorktreeInventories(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	expected worktreeInventoryDigests,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) (*commandSpool, error) {
	stage, err := service.runRootedGitToSpool(
		ctx,
		worktree,
		environment,
		[]string{gitLSFiles, gitStage, "-z"},
		stageInventoryWireLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("verify live Git index: %w", err)
	}
	stageDigest, err := digestAndValidateStageInventoryContextCPU(
		ctx, stage.Reader(), ledger, runCPU,
	)
	if err != nil {
		return nil, errors.Join(err, stage.Close())
	}
	if stageDigest != expected.stage {
		return nil, errors.Join(
			errors.New("live Git index changed while snapshotting worktree"),
			stage.Close(),
		)
	}

	flags, err := service.runRootedGitToSpool(
		ctx,
		worktree,
		environment,
		[]string{gitLSFiles, "-v", "-z", "--"},
		flagInventoryWireLimit,
	)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("verify worktree index flags: %w", err), stage.Close())
	}
	flagDigest, err := digestAndValidateFlagInventoryContextCPU(
		ctx, flags.Reader(), ledger, runCPU,
	)
	err = errors.Join(err, flags.Close())
	if err != nil {
		return nil, errors.Join(err, stage.Close())
	}
	if flagDigest != expected.flags {
		return nil, errors.Join(
			errors.New("live Git index flags changed while snapshotting worktree"),
			stage.Close(),
		)
	}

	if expected.hasUntracked {
		untracked, runErr := service.runRootedGitToSpool(
			ctx,
			worktree,
			environment,
			[]string{gitLSFiles, "--others", "-z", "--"},
			untrackedInventoryWireLimit,
		)
		if runErr != nil {
			return nil, errors.Join(fmt.Errorf("verify untracked files: %w", runErr), stage.Close())
		}
		untrackedDigest, digestErr := digestCommandSpool(
			ctx, untracked, ledger, runCPU,
		)
		digestErr = errors.Join(digestErr, untracked.Close())
		if digestErr != nil {
			return nil, errors.Join(digestErr, stage.Close())
		}
		if untrackedDigest != expected.untracked {
			return nil, errors.Join(
				errors.New("untracked file inventory changed while snapshotting worktree"),
				stage.Close(),
			)
		}
	}
	if err := stage.Rewind(); err != nil {
		return nil, errors.Join(err, stage.Close())
	}
	return stage, nil
}

func digestCommandSpool(
	ctx context.Context,
	spool *commandSpool,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) ([sha256.Size]byte, error) {
	if err := spool.Rewind(); err != nil {
		return [sha256.Size]byte{}, err
	}
	return walkNULTokensWithCPU(
		ctx, spool.Reader(), runCPU, ledger, sha256.New(), func([]byte) error { return nil },
	)
}

func digestAndValidateFlagInventoryContextCPU(
	ctx context.Context,
	input io.Reader,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) ([sha256.Size]byte, error) {
	return walkNULTokensWithCPU(
		ctx, input, runCPU, ledger, sha256.New(), func(entry []byte) error {
			if len(entry) < 3 || entry[1] != ' ' {
				return errors.New("worktree index flag entry is malformed")
			}
			if entry[0] >= 'a' && entry[0] <= 'z' || entry[0] == 'S' {
				return errors.New(
					"assume-unchanged and skip-worktree paths cannot be snapshotted safely",
				)
			}
			return nil
		})
}

func digestAndValidateStageInventory(input io.Reader) ([sha256.Size]byte, error) {
	return digestAndValidateStageInventoryContext(context.Background(), input)
}

func digestAndValidateStageInventoryContext(
	ctx context.Context,
	input io.Reader,
) ([sha256.Size]byte, error) {
	ledger, err := newSourceLedger(0)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return digestAndValidateStageInventoryContextCPU(
		ctx, input, ledger, directCPUAdmission,
	)
}

func digestAndValidateStageInventoryContextCPU(
	ctx context.Context,
	input io.Reader,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) ([sha256.Size]byte, error) {
	return walkNULTokensWithCPU(
		ctx, input, runCPU, ledger, sha256.New(), func(entry []byte) error {
			tab := bytes.IndexByte(entry, '\t')
			if tab <= 0 || tab == len(entry)-1 {
				return errors.New("worktree stage entry is malformed")
			}
			fields := bytes.Fields(entry[:tab])
			if len(fields) != 3 {
				return errors.New("worktree stage entry prefix is malformed")
			}
			if _, modeErr := review.NewFileMode(string(fields[0])); modeErr != nil {
				return fmt.Errorf("worktree stage mode: %w", modeErr)
			}
			if _, objectErr := review.NewObjectID(string(fields[1])); objectErr != nil {
				return fmt.Errorf("worktree stage object: %w", objectErr)
			}
			stage, err := strconv.ParseUint(string(fields[2]), 10, 8)
			if err != nil || stage > 3 {
				return errors.New("worktree stage number is invalid")
			}
			return validateRepositoryRelativePath(entry[tab+1:])
		})
}

func parseAndDigestUntrackedInventoryWithLedger(
	ctx context.Context,
	input io.Reader,
	ledger *sourceLedger,
) ([]untrackedWorktreeRecord, int64, [sha256.Size]byte, error) {
	return parseAndDigestUntrackedInventoryWithLedgerCPU(
		ctx, input, ledger, directCPUAdmission,
	)
}

func parseAndDigestUntrackedInventoryWithLedgerCPU(
	ctx context.Context,
	input io.Reader,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) ([]untrackedWorktreeRecord, int64, [sha256.Size]byte, error) {
	records := make([]untrackedWorktreeRecord, 0)
	seen := make(map[string]untrackedWorktreeKind)
	var pathBytes int64
	digest, err := walkNULTokensWithCPU(
		ctx, input, runCPU, ledger, sha256.New(), func(path []byte) error {
			kind := untrackedWorktreeOrdinary
			if len(path) > 0 && path[len(path)-1] == '/' {
				kind = untrackedWorktreeGitlink
				path = path[:len(path)-1]
			}
			if err := validateRepositoryRelativePath(path); err != nil {
				return err
			}
			if len(records) == review.MaxFiles {
				return fmt.Errorf("worktree paths exceed %d", review.MaxFiles)
			}
			if int64(len(path)) > review.MaxSourcePaths-pathBytes {
				return fmt.Errorf(
					"worktree path bytes exceed %d",
					review.MaxSourcePaths,
				)
			}
			const untrackedRecordCharge = 112
			if reserveErr := ledger.reserve(int64(len(path)) + untrackedRecordCharge); reserveErr != nil {
				return reserveErr
			}
			key := string(path)
			if previous, exists := seen[key]; exists {
				if previous != kind {
					return fmt.Errorf("untracked inventory changes kind for path %q", path)
				}
				return fmt.Errorf("untracked inventory repeats path %q", path)
			}
			seen[key] = kind
			records = append(records, untrackedWorktreeRecord{path: key, kind: kind})
			pathBytes += int64(len(path))
			return nil
		})
	if err != nil {
		return nil, 0, [sha256.Size]byte{}, err
	}
	return records, pathBytes, digest, nil
}

func (service *Service) initializePrivateIndexFromSpool(
	ctx context.Context,
	worktree *rootedRepository,
	environment map[string]string,
	workspace string,
	stage *commandSpool,
) (map[string]string, error) {
	privateEnvironment := cloneEnvironment(environment)
	indexPath := filepath.Join(workspace, "index")
	indexDescriptorPath, err := worktree.indexPath()
	if err != nil {
		return nil, err
	}
	privateEnvironment[gitIndexFileKey] = indexDescriptorPath
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("prepare private worktree index: %w", err)
	}
	if err := service.runRootedGitFromSpool(
		ctx,
		worktree,
		privateEnvironment,
		[]string{gitUpdateIndex, "-z", gitIndexInfo},
		stage,
		stageInventoryWireLimit,
		64<<10,
	); err != nil {
		return nil, fmt.Errorf("copy live Git index: %w", err)
	}
	return privateEnvironment, nil
}
