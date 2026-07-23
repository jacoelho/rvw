package source

import (
	"context"
	"errors"
)

const (
	worktreeBuildPollRecords = 2_048
	worktreeBuildPollBytes   = 16 << 10
)

type worktreePathBatch struct {
	input       []byte
	outputLimit int64
	next        int
}

func buildWorktreePathBatch(
	ctx context.Context,
	paths []string,
	start int,
) (worktreePathBatch, error) {
	if start < 0 || start >= len(paths) {
		return worktreePathBatch{}, errors.New("worktree filter batch start is invalid")
	}
	if err := context.Cause(ctx); err != nil {
		return worktreePathBatch{}, err
	}
	input := make([]byte, 0, indexUpdateBatchBytes)
	end := start
	bytesSincePoll := 0
	for end < len(paths) {
		recordBytes := len(paths[end]) + 1
		if recordBytes > indexUpdateBatchBytes {
			return worktreePathBatch{}, errors.New("worktree filter path exceeds batch capacity")
		}
		if recordBytes > indexUpdateBatchBytes-len(input) {
			break
		}
		if end > start && ((end-start)%worktreeBuildPollRecords == 0 ||
			recordBytes > worktreeBuildPollBytes-bytesSincePoll) {
			if err := context.Cause(ctx); err != nil {
				return worktreePathBatch{}, err
			}
			bytesSincePoll = 0
		}
		input = append(input, paths[end]...)
		input = append(input, 0)
		bytesSincePoll += recordBytes
		end++
	}
	if end == start {
		return worktreePathBatch{}, errors.New("worktree filter batch made no progress")
	}
	if err := context.Cause(ctx); err != nil {
		return worktreePathBatch{}, err
	}
	outputLimit, err := worktreeFilterOutputLimit(len(input), end-start)
	if err != nil {
		return worktreePathBatch{}, err
	}
	return worktreePathBatch{input: input, outputLimit: outputLimit, next: end}, nil
}

type worktreeIndexBatch struct {
	input []byte
	next  int
}

func buildWorktreeIndexBatch(
	ctx context.Context,
	zero string,
	updates []indexUpdate,
	start int,
) (worktreeIndexBatch, error) {
	if start < 0 || start >= len(updates) || zero == "" {
		return worktreeIndexBatch{}, errors.New("worktree index batch input is invalid")
	}
	if err := context.Cause(ctx); err != nil {
		return worktreeIndexBatch{}, err
	}
	input := make([]byte, 0, indexUpdateBatchBytes)
	end := start
	bytesSincePoll := 0
	for end < len(updates) {
		update := updates[end]
		recordBytes := worktreeIndexRecordBytes(update, len(zero))
		if recordBytes > indexUpdateBatchBytes {
			return worktreeIndexBatch{}, errors.New("worktree index update exceeds batch capacity")
		}
		if recordBytes > indexUpdateBatchBytes-len(input) {
			break
		}
		if end > start && ((end-start)%worktreeBuildPollRecords == 0 ||
			recordBytes > worktreeBuildPollBytes-bytesSincePoll) {
			if err := context.Cause(ctx); err != nil {
				return worktreeIndexBatch{}, err
			}
			bytesSincePoll = 0
		}
		if update.remove {
			input = append(input, '0', ' ')
			input = append(input, zero...)
		} else {
			input = append(input, update.mode.String()...)
			input = append(input, ' ')
			input = append(input, update.object.String()...)
		}
		input = append(input, '\t')
		input = append(input, update.path...)
		input = append(input, 0)
		bytesSincePoll += recordBytes
		end++
	}
	if end == start {
		return worktreeIndexBatch{}, errors.New("worktree index batch made no progress")
	}
	if err := context.Cause(ctx); err != nil {
		return worktreeIndexBatch{}, err
	}
	return worktreeIndexBatch{input: input, next: end}, nil
}

func worktreeIndexRecordBytes(update indexUpdate, objectBytes int) int {
	if update.remove {
		return len("0 ") + objectBytes + len("\t") + len(update.path) + 1
	}
	return len(update.mode.String()) + 1 + len(update.object.String()) +
		len("\t") + len(update.path) + 1
}
