package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestSourceLedgerRejectsBeforeOneByteOverLimit(t *testing.T) {
	ledger, err := newSourceLedgerWithLimit(10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.reserve(7); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}
	if err := ledger.reserve(1); !errors.Is(err, errSourceConstructionLimited) {
		t.Fatalf("one byte over error = %v", err)
	}
	if err := ledger.reserve(1); !errors.Is(err, ErrSourceLimited) {
		t.Fatalf("construction error is not a source limit: %v", err)
	}
	ledger.release(4)
	if err := ledger.reserve(4); err != nil {
		t.Fatalf("released ownership was not reusable: %v", err)
	}
	if ledger.used != 10 {
		t.Fatalf("used = %d, want 10", ledger.used)
	}
}

func TestConstructionLimitClassificationRemainsLimited(t *testing.T) {
	limitErr := fmt.Errorf("construction: %w", errSourceConstructionLimited)
	if status := pendingFailureStatus(limitErr); status != review.PendingReviewLimited {
		t.Fatalf("pending status = %s", status)
	}
	if status := remoteThreadFailureStatus(limitErr); status != review.RemoteThreadsLimited {
		t.Fatalf("remote thread status = %s", status)
	}
	failure := sourceFailure(OperationOpenSource, limitErr)
	var classified *FailureError
	if !errors.As(failure, &classified) || classified.Kind != FailureLimited {
		t.Fatalf("required source failure = %#v", classified)
	}
}

func TestRawDiffConstructionChargeCoversSHA256RecordAtBoundary(t *testing.T) {
	const path = "p"
	oid := strings.Repeat("1", 64)
	raw := ":100644 100755 " + oid + " " + oid + " M\x00" + path + "\x00"
	exact := int64(16)*diffRecordConstructionCharge + int64(len(path))

	ledger, err := newSourceLedgerWithLimit(exact, 0)
	if err != nil {
		t.Fatal(err)
	}
	records, err := parseRawDiffStreamWithLedgerLimits(
		context.Background(),
		strings.NewReader(raw),
		ledger,
		16,
		int64(len(path)),
	)
	if err != nil || len(records) != 1 || ledger.used != exact {
		t.Fatalf("exact records=%d used=%d err=%v", len(records), ledger.used, err)
	}

	ledger, err = newSourceLedgerWithLimit(exact-1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseRawDiffStreamWithLedgerLimits(
		context.Background(),
		strings.NewReader(raw),
		ledger,
		16,
		int64(len(path)),
	); !errors.Is(err, ErrSourceLimited) {
		t.Fatalf("one-byte-over error = %v", err)
	}
}

func TestSourceLedgerCheckpointRollsBackOrCommitsExactOwnership(t *testing.T) {
	ledger, err := newSourceLedgerWithLimit(20, 3)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := ledger.checkpoint()
	if err := ledger.reserve(10); err != nil {
		t.Fatal(err)
	}
	checkpoint.rollback()
	if ledger.used != 3 {
		t.Fatalf("rollback used = %d, want 3", ledger.used)
	}
	checkpoint = ledger.checkpoint()
	if err := ledger.reserve(10); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.commit(4); err != nil {
		t.Fatal(err)
	}
	if ledger.used != 7 {
		t.Fatalf("commit used = %d, want 7", ledger.used)
	}
}
