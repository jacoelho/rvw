package source

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestWorktreePathBatchAcceptsExactOneMiBAndMakesProgress(t *testing.T) {
	path := strings.Repeat("p", 4_095)
	paths := make([]string, 257)
	for index := range paths {
		paths[index] = path
	}
	batch, err := buildWorktreePathBatch(t.Context(), paths, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.input) != indexUpdateBatchBytes || cap(batch.input) != indexUpdateBatchBytes ||
		batch.next != 256 {
		t.Fatalf("exact path batch = len %d cap %d next %d", len(batch.input), cap(batch.input), batch.next)
	}
	if bytes.Count(batch.input, []byte{0}) != batch.next {
		t.Fatal("path batch does not contain one direct NUL terminator per record")
	}
	next, err := buildWorktreePathBatch(t.Context(), paths, batch.next)
	if err != nil || next.next != len(paths) || len(next.input) != len(path)+1 {
		t.Fatalf("next path batch = len %d next %d error %v", len(next.input), next.next, err)
	}
}

func TestWorktreeIndexBatchAcceptsExactOneMiBAndEncodesDirectly(t *testing.T) {
	mode := review.FileMode("100644")
	object := review.ObjectID(strings.Repeat("1", 40))
	path := strings.Repeat("p", 4_047)
	updates := make([]indexUpdate, 257)
	for index := range updates {
		updates[index] = indexUpdate{mode: mode, object: object, path: path}
	}
	batch, err := buildWorktreeIndexBatch(
		t.Context(), strings.Repeat("0", 40), updates, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.input) != indexUpdateBatchBytes || cap(batch.input) != indexUpdateBatchBytes ||
		batch.next != 256 {
		t.Fatalf("exact index batch = len %d cap %d next %d", len(batch.input), cap(batch.input), batch.next)
	}
	if bytes.Count(batch.input, []byte{0}) != batch.next {
		t.Fatal("index batch does not contain one direct NUL terminator per record")
	}

	updates = []indexUpdate{
		{mode: mode, object: object, path: "add"},
		{path: "remove", remove: true},
	}
	batch, err = buildWorktreeIndexBatch(
		t.Context(), strings.Repeat("0", 40), updates, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "100644 " + object.String() + "\tadd\x00" +
		"0 " + strings.Repeat("0", 40) + "\tremove\x00"
	if string(batch.input) != want {
		t.Fatalf("direct index encoding = %q, want %q", batch.input, want)
	}
}

func TestWorktreeBatchBuildersPollCancellation(t *testing.T) {
	paths := make([]string, 5_000)
	updates := make([]indexUpdate, len(paths))
	for index := range paths {
		paths[index] = "path"
		updates[index] = indexUpdate{
			mode:   review.FileMode("100644"),
			object: review.ObjectID(strings.Repeat("1", 40)),
			path:   paths[index],
		}
	}
	for name, build := range map[string]func(context.Context) error{
		"path": func(ctx context.Context) error {
			_, err := buildWorktreePathBatch(ctx, paths, 0)
			return err
		},
		"index": func(ctx context.Context) error {
			_, err := buildWorktreeIndexBatch(ctx, strings.Repeat("0", 40), updates, 0)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := &cancelAfterWorktreeChecks{Context: t.Context(), remaining: 2}
			if err := build(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error = %v", err)
			}
		})
	}
}

type cancelAfterWorktreeChecks struct {
	context.Context
	remaining int
}

func (ctx *cancelAfterWorktreeChecks) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}
