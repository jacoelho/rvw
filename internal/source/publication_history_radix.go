package source

import (
	"context"
	"errors"
	"fmt"
	"math"
)

const (
	maxGitHubReviewHistoryItems = 100_000
	reviewHistoryIdentityBytes  = int64(8)
	reviewHistoryRadixBuckets   = 256
	reviewHistoryRadixPasses    = 8
	reviewHistoryPollItems      = 2_048
)

func reviewHistoryIdentitiesBytes(count int) (int64, error) {
	if count < 0 || int64(count) > math.MaxInt64/reviewHistoryIdentityBytes {
		return 0, errors.New("GitHub review history identity size overflows")
	}
	return int64(count) * reviewHistoryIdentityBytes, nil
}

func validateReviewHistoryIdentities(
	ctx context.Context,
	identities []uint64,
	runCPU CPUAdmission,
	ledger *sourceLedger,
) error {
	if runCPU == nil || ledger == nil {
		return errors.New("GitHub review history validation is incomplete")
	}
	bytes, err := reviewHistoryIdentitiesBytes(len(identities))
	if err != nil {
		return err
	}
	if reserveErr := ledger.reserve(bytes); reserveErr != nil {
		return reserveErr
	}
	var scratch []uint64
	err = runCPU(ctx, func() error {
		scratch = make([]uint64, len(identities))
		return validateReviewHistoryIdentitiesRadix(ctx, identities, scratch)
	})
	scratch = nil
	ledger.release(bytes)
	return err
}

func validateReviewHistoryIdentitiesRadix(
	ctx context.Context,
	identities []uint64,
	scratch []uint64,
) error {
	if len(scratch) != len(identities) {
		return errors.New("GitHub review history radix scratch is incomplete")
	}
	input, output := identities, scratch
	for pass := range reviewHistoryRadixPasses {
		var offsets [reviewHistoryRadixBuckets]int
		shift := uint(pass * 8)
		for index, identity := range input {
			if index%reviewHistoryPollItems == 0 {
				if err := context.Cause(ctx); err != nil {
					return err
				}
			}
			offsets[byte((identity>>shift)&0xff)]++
		}
		next := 0
		for bucket := range offsets {
			count := offsets[bucket]
			offsets[bucket] = next
			next += count
		}
		for index, identity := range input {
			if index%reviewHistoryPollItems == 0 {
				if err := context.Cause(ctx); err != nil {
					return err
				}
			}
			bucket := byte((identity >> shift) & 0xff)
			output[offsets[bucket]] = identity
			offsets[bucket]++
		}
		input, output = output, input
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	for index := 1; index < len(input); index++ {
		if index%reviewHistoryPollItems == 0 {
			if err := context.Cause(ctx); err != nil {
				return err
			}
		}
		if input[index] == input[index-1] {
			return fmt.Errorf("GitHub review history repeats review %d", input[index])
		}
	}
	return context.Cause(ctx)
}
