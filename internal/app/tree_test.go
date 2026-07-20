package app

import (
	"context"
	"errors"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestFileTreeBudgetRejectsNodeAndByteOverflow(t *testing.T) {
	nodeBudget := fileTreeBudget{nodes: maxFileTreeNodes}
	if err := nodeBudget.reserve("", ""); !errors.Is(err, ErrFileTreeLimited) {
		t.Fatalf("node overflow error = %v, want ErrFileTreeLimited", err)
	}

	byteBudget := fileTreeBudget{retainedBytes: maxFileTreeRetainedBytes}
	if err := byteBudget.reserve("", ""); !errors.Is(err, ErrFileTreeLimited) {
		t.Fatalf("byte overflow error = %v, want ErrFileTreeLimited", err)
	}
}

func TestFileTreeBuildObservesCancellationBeforeWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := buildFileTreeContext(
		ctx,
		review.Snapshot{},
		nil,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled build error = %v, want context.Canceled", err)
	}
}
