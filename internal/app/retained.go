package app

import (
	"errors"
	"fmt"
)

// ErrRetainedResultLimited identifies a result whose owned construction graph
// cannot fit the coordinator-wide result bound.
var ErrRetainedResultLimited = errors.New("operation result exceeds retained limit")

type retainedLedger struct {
	limit int64
	used  int64
}

func newRetainedLedger(limit, initial int64) (*retainedLedger, error) {
	ledger := &retainedLedger{limit: limit}
	if limit <= 0 || initial < 0 {
		return nil, errors.New("retained ledger limits are invalid")
	}
	if err := ledger.reserve(initial); err != nil {
		return nil, err
	}
	return ledger, nil
}

func (ledger *retainedLedger) reserve(bytes int64) error {
	if ledger == nil || bytes < 0 || bytes > ledger.limit-ledger.used {
		limit := int64(0)
		used := int64(0)
		if ledger != nil {
			limit = ledger.limit
			used = ledger.used
		}
		return fmt.Errorf(
			"%w: %d retained plus %d requested exceeds %d bytes",
			ErrRetainedResultLimited,
			used,
			bytes,
			limit,
		)
	}
	ledger.used += bytes
	return nil
}

func (ledger *retainedLedger) release(bytes int64) {
	if ledger == nil || bytes < 0 || bytes > ledger.used {
		panic("invalid retained ledger release")
	}
	ledger.used -= bytes
}
