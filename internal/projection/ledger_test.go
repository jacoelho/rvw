package projection

import (
	"errors"
	"testing"
)

func TestRetainedLedgerRejectsBeforeOneByteOverLimit(t *testing.T) {
	ledger, err := NewLedger(10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Reserve(7); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}
	if err := ledger.Reserve(1); !errors.Is(err, ErrRetainedLimited) {
		t.Fatalf("one byte over error = %v", err)
	}
	ledger.Release(4)
	if err := ledger.Reserve(4); err != nil {
		t.Fatalf("released ownership was not reusable: %v", err)
	}
	if ledger.used != 10 {
		t.Fatalf("used = %d, want 10", ledger.used)
	}
}
