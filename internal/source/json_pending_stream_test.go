package source

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPendingReviewStreamVisitsLargeRootInsideCPUAdmission(t *testing.T) {
	const pageCount = maxGitHubPageCollectionElements + 1
	pages := make([]string, pageCount)
	for index := range pages {
		pages[index] = "{}"
	}
	encoded := "[" + strings.Join(
		pages,
		","+strings.Repeat(" ", sourceStreamBufferBytes/16),
	) + "]"
	limits, err := newJSONFramingLimits(pageCount, maxGitHubPageCollectionElements)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	active := false
	admissions := 0
	admit := func(ctx context.Context, work func() error) error {
		if active {
			return errors.New("pending JSON CPU admission re-entered")
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		active = true
		admissions++
		defer func() { active = false }()
		return work()
	}
	metrics, err := inspectJSONWithLimits(
		t.Context(), strings.NewReader(encoded), maxGitHubJSONTokenBytes, admit, ledger, limits,
	)
	if err != nil {
		t.Fatal(err)
	}
	decodeAdmissionStart := admissions
	visits := 0
	err = decodePendingReviewStream(
		t.Context(), strings.NewReader(encoded), admit, ledger, metrics,
		func(index int, position pendingPagePosition, _ *pendingReviewPage) error {
			if !active {
				return errors.New("pending review page visited outside CPU admission")
			}
			if index != visits {
				return errors.New("pending review page visited out of order")
			}
			wantPosition := pendingJSONPageIntermediate
			if index+1 == pageCount {
				wantPosition = pendingJSONPageFinal
			}
			if position != wantPosition {
				return errors.New("pending review page position is wrong")
			}
			visits++
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if visits != pageCount {
		t.Fatalf("page visits = %d, want %d", visits, pageCount)
	}
	if admissions-decodeAdmissionStart < 2 {
		t.Fatalf("decode admissions = %d, want multiple bounded chunks", admissions-decodeAdmissionStart)
	}
	if ledger.used != metrics.cardinalityBytes {
		t.Fatalf("decode retained %d bytes outside framing metrics", ledger.used-metrics.cardinalityBytes)
	}
	metrics.release(ledger)
	if ledger.used != 0 {
		t.Fatalf("released decoder retained %d bytes", ledger.used)
	}
}

func TestPendingStreamsRejectMixedRootItemShapes(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		decode  func(*sourceLedger, jsonCommandMetrics) error
	}{
		{
			name:    "reviews",
			encoded: `[{},[]]`,
			decode: func(ledger *sourceLedger, metrics jsonCommandMetrics) error {
				return decodePendingReviewStream(
					t.Context(), strings.NewReader(`[{},[]]`), directCPUAdmission, ledger, metrics,
					func(_ int, _ pendingPagePosition, _ *pendingReviewPage) error { return nil },
				)
			},
		},
		{
			name:    "comments",
			encoded: `[[],{}]`,
			decode: func(ledger *sourceLedger, metrics jsonCommandMetrics) error {
				return decodePendingCommentStream(
					t.Context(), strings.NewReader(`[[],{}]`), directCPUAdmission, ledger, metrics,
					func(_ int, _ *pendingReviewCommentPage) error { return nil },
				)
			},
		},
		{
			name:    "capabilities",
			encoded: `[{},[]]`,
			decode: func(ledger *sourceLedger, metrics jsonCommandMetrics) error {
				return decodePendingCapabilityStream(
					t.Context(), strings.NewReader(`[{},[]]`), directCPUAdmission, ledger, metrics,
					func(_ int, _ pendingPagePosition, _ *pendingCommentCapabilityPage) error {
						return nil
					},
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger, err := newSourceLedger(0)
			if err != nil {
				t.Fatal(err)
			}
			limits, err := newJSONFramingLimits(2, maxGitHubPageCollectionElements)
			if err != nil {
				t.Fatal(err)
			}
			metrics, err := inspectJSONWithLimits(
				t.Context(), strings.NewReader(test.encoded), maxGitHubJSONTokenBytes,
				directCPUAdmission, ledger, limits,
			)
			if err != nil {
				t.Fatal(err)
			}
			err = test.decode(ledger, metrics)
			metrics.release(ledger)
			if err == nil || !strings.Contains(err.Error(), "is not an") {
				t.Fatalf("mixed root item error = %v", err)
			}
			if ledger.used != 0 {
				t.Fatalf("failed decode retained %d bytes", ledger.used)
			}
		})
	}
}

func TestPendingJSONLimitsRootPagesSeparatelyFromPageItems(t *testing.T) {
	rootPages := "[" + strings.TrimSuffix(strings.Repeat("{},", 101), ",") + "]"
	limits, err := newJSONFramingLimits(101, maxGitHubPageCollectionElements)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := inspectJSONWithLimits(
		t.Context(), strings.NewReader(rootPages), maxGitHubJSONTokenBytes,
		directCPUAdmission, ledger, limits,
	)
	if err != nil {
		t.Fatalf("101 root pages rejected by nested limit: %v", err)
	}
	metrics.release(ledger)

	nestedItems := "[[" + strings.TrimSuffix(strings.Repeat("{},", 101), ",") + "]]"
	if _, err := inspectJSONWithLimits(
		t.Context(), strings.NewReader(nestedItems), maxGitHubJSONTokenBytes,
		directCPUAdmission, ledger, limits,
	); err == nil || !strings.Contains(err.Error(), "exceeds 100 elements") {
		t.Fatalf("nested page limit error = %v", err)
	}
}
