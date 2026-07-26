// Package projection builds bounded immutable indexes over change values.
package projection

import (
	"errors"
	"fmt"
)

// ErrRetainedLimited identifies a projection whose owned construction graph
// cannot fit its caller-provided bound.
var ErrRetainedLimited = errors.New("operation result exceeds retained limit")

// Ledger accounts temporary and retained projection ownership while a worker
// constructs an index. A Ledger is worker-confined.
type Ledger struct {
	limit int64
	used  int64
}

// NewLedger constructs a ledger and reserves the caller-owned initial graph.
func NewLedger(limit, initial int64) (*Ledger, error) {
	ledger := &Ledger{limit: limit}
	if limit <= 0 || initial < 0 {
		return nil, errors.New("retained ledger limits are invalid")
	}
	if err := ledger.Reserve(initial); err != nil {
		return nil, err
	}
	return ledger, nil
}

// Reserve accounts bytes until a matching Release or ownership transfer.
func (ledger *Ledger) Reserve(bytes int64) error {
	if ledger == nil || bytes < 0 || bytes > ledger.limit-ledger.used {
		limit := int64(0)
		used := int64(0)
		if ledger != nil {
			limit = ledger.limit
			used = ledger.used
		}
		return fmt.Errorf(
			"%w: %d retained plus %d requested exceeds %d bytes",
			ErrRetainedLimited,
			used,
			bytes,
			limit,
		)
	}
	ledger.used += bytes
	return nil
}

// Release removes temporary ownership from the ledger.
func (ledger *Ledger) Release(bytes int64) {
	if ledger == nil || bytes < 0 || bytes > ledger.used {
		panic("invalid retained ledger release")
	}
	ledger.used -= bytes
}
