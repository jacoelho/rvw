package app

import (
	"errors"
	"testing"
)

func TestRetainedLedgerRejectsBeforeOneByteOverLimit(t *testing.T) {
	ledger, err := newRetainedLedger(10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.reserve(7); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}
	if err := ledger.reserve(1); !errors.Is(err, ErrRetainedResultLimited) {
		t.Fatalf("one byte over error = %v", err)
	}
	ledger.release(4)
	if err := ledger.reserve(4); err != nil {
		t.Fatalf("released ownership was not reusable: %v", err)
	}
	if ledger.used != 10 {
		t.Fatalf("used = %d, want 10", ledger.used)
	}
}
