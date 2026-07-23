package source

import (
	"context"
	"errors"
	"testing"
)

func TestReviewHistoryRadixRejectsDuplicatesForAdversarialIdentities(t *testing.T) {
	identities := []uint64{
		0xff00ff00ff00ff00,
		1,
		0xffffffffffffffff,
		0x00ff00ff00ff00ff,
		1 << 63,
		0xff00ff00ff00ff00,
	}
	scratch := make([]uint64, len(identities))
	if err := validateReviewHistoryIdentitiesRadix(t.Context(), identities, scratch); err == nil {
		t.Fatal("adversarial duplicate identities were accepted")
	}
}

func TestReviewHistoryRadixAuxiliaryOwnershipIsExactAndReleased(t *testing.T) {
	identities := []uint64{4, 2, 3, 1}
	identityBytes, err := reviewHistoryIdentitiesBytes(len(identities))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		limit   int64
		wantErr bool
	}{
		{name: "exact", limit: identityBytes * 2},
		{name: "one byte short", limit: identityBytes*2 - 1, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger, err := newSourceLedgerWithLimit(test.limit, identityBytes)
			if err != nil {
				t.Fatal(err)
			}
			baseline := ledger.used
			err = validateReviewHistoryIdentities(
				t.Context(),
				append([]uint64(nil), identities...),
				directCPUAdmission,
				ledger,
			)
			if (err != nil) != test.wantErr {
				t.Fatalf("validation error = %v, want error %t", err, test.wantErr)
			}
			if ledger.used != baseline {
				t.Fatalf("radix retained %d bytes, started with %d", ledger.used, baseline)
			}
		})
	}
}

func TestReviewHistoryRadixPollsCancellationAtMaximumCardinality(t *testing.T) {
	identities := make([]uint64, maxGitHubReviewHistoryItems)
	for index := range identities {
		identities[index] = uint64(maxGitHubReviewHistoryItems - index)
	}
	cause := errors.New("stop review history radix")
	ctx, cancel := context.WithCancelCause(t.Context())
	runCPU := func(_ context.Context, work func() error) error {
		cancel(cause)
		return work()
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReviewHistoryIdentities(ctx, identities, runCPU, ledger); !errors.Is(err, cause) {
		t.Fatalf("radix cancellation = %v", err)
	}
	if ledger.used != 0 {
		t.Fatalf("cancelled radix retained %d bytes", ledger.used)
	}
}
