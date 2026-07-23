package source

import (
	"errors"
	"fmt"
)

const maxSourceConstructionBytes int64 = 128 << 20

var errSourceConstructionLimited = fmt.Errorf(
	"source construction exceeds retained limit: %w",
	ErrSourceLimited,
)

type sourceLedger struct {
	limit int64
	used  int64
}

func newSourceLedger(initial int64) (*sourceLedger, error) {
	return newSourceLedgerWithLimit(maxSourceConstructionBytes, initial)
}

func newSourceLedgerWithLimit(limit, initial int64) (*sourceLedger, error) {
	ledger := &sourceLedger{limit: limit}
	if limit <= 0 || initial < 0 {
		return nil, errors.New("source ledger limits are invalid")
	}
	if err := ledger.reserve(initial); err != nil {
		return nil, err
	}
	return ledger, nil
}

func (ledger *sourceLedger) reserve(bytes int64) error {
	if ledger == nil || bytes < 0 || bytes > ledger.limit-ledger.used {
		used := int64(0)
		limit := int64(0)
		if ledger != nil {
			used = ledger.used
			limit = ledger.limit
		}
		return fmt.Errorf(
			"%w: %d retained plus %d requested exceeds %d bytes",
			errSourceConstructionLimited,
			used,
			bytes,
			limit,
		)
	}
	ledger.used += bytes
	return nil
}

func (ledger *sourceLedger) release(bytes int64) {
	if ledger == nil || bytes < 0 || bytes > ledger.used {
		panic("invalid source ledger release")
	}
	ledger.used -= bytes
}

type sourceLedgerCheckpoint struct {
	ledger *sourceLedger
	start  int64
	done   bool
}

func (ledger *sourceLedger) checkpoint() sourceLedgerCheckpoint {
	if ledger == nil {
		panic("nil source ledger checkpoint")
	}
	return sourceLedgerCheckpoint{ledger: ledger, start: ledger.used}
}

func (checkpoint *sourceLedgerCheckpoint) rollback() {
	if checkpoint == nil || checkpoint.done {
		return
	}
	checkpoint.ledger.release(checkpoint.ledger.used - checkpoint.start)
	checkpoint.done = true
}

func (checkpoint *sourceLedgerCheckpoint) commit(retained int64) error {
	if checkpoint == nil || checkpoint.done || retained < 0 {
		return errors.New("source ledger checkpoint is invalid")
	}
	delta := checkpoint.ledger.used - checkpoint.start
	if retained > delta {
		return errors.New("source ledger checkpoint retained bytes exceed its reservation")
	}
	checkpoint.ledger.release(delta - retained)
	checkpoint.done = true
	return nil
}
