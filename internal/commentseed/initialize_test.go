package commentseed

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/source"
)

type seedPatchLoader struct {
	err      error
	prepared *change.PreparedPatch
	detail   change.PatchDetail
	calls    int
	status   change.PatchStatus
}

func (loader *seedPatchLoader) LoadPatch(
	_ context.Context,
	_ source.Opened,
	_ *source.Revision,
	request change.PatchRequest,
) (change.PatchResult, error) {
	loader.calls++
	if loader.err != nil {
		return change.PatchResult{}, loader.err
	}
	return change.PatchResult{
		Prepared:  loader.prepared,
		Detail:    loader.detail,
		Handle:    request.Handle,
		Workspace: request.Workspace,
		Status:    loader.status,
	}, nil
}

func TestInitializeMaterializesEachReferencedFileOnceAndSeedsMappedLines(t *testing.T) {
	review := seedTestReview(t, "a.go")
	document := readSeedTestDocument(t, `{"comments":[
		{"target":{"type":"line","path":"a.go","side":"old","line":2},"binding":"active","comment":"old"},
		{"target":{"type":"range","path":"a.go","side":"new","start":2,"end":3},"binding":"active","comment":"new range"},
		{"target":{"type":"file","path":"a.go"},"binding":"active","comment":"file"},
		{"target":{"type":"review"},"binding":"active","comment":"review"}
	]}`)
	prepared, err := change.TakePatch([]byte("@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n"))
	if err != nil {
		t.Fatal(err)
	}
	loader := &seedPatchLoader{prepared: prepared, status: change.PatchReady}
	contextLines, _ := change.NewContextLines(3)
	patchContext, _ := change.ContextAround(contextLines)
	initialized, err := Initialize(
		t.Context(),
		loader,
		source.Opened{},
		&source.Revision{},
		review,
		patchContext,
		document,
	)
	if err != nil {
		t.Fatal(err)
	}
	if loader.calls != 1 || initialized.CommentCount() != 4 || review.CommentCount() != 0 {
		t.Fatalf("loads=%d initialized=%d original=%d", loader.calls, initialized.CommentCount(), review.CommentCount())
	}
	line, _ := initialized.CommentAt(0)
	side, start, end, ok := line.Target().Lines()
	if !ok || side != change.LineOld || start.Uint32() != 2 || end.Uint32() != 2 {
		t.Fatalf("mapped old target = side %d start %d end %d present %t", side, start, end, ok)
	}
}

func TestInitializeRejectsMissingPathsLinesAndUnavailablePatchesAtomically(t *testing.T) {
	contextLines, _ := change.NewContextLines(0)
	patchContext, _ := change.ContextAround(contextLines)
	for _, test := range []struct {
		name     string
		document string
		loader   *seedPatchLoader
		want     string
	}{
		{
			name:     "missing path",
			document: `{"comments":[{"target":{"type":"file","path":"missing.go"},"binding":"active","comment":"x"}]}`,
			loader:   &seedPatchLoader{},
			want:     "not an active file",
		},
		{
			name:     "line outside patch",
			document: `{"comments":[{"target":{"type":"line","path":"a.go","side":"new","line":9},"binding":"active","comment":"x"}]}`,
			loader:   readySeedPatchLoader(t),
			want:     "not present in the configured patch context",
		},
		{
			name:     "unavailable patch",
			document: `{"comments":[{"target":{"type":"line","path":"a.go","side":"new","line":2},"binding":"active","comment":"x"}]}`,
			loader: &seedPatchLoader{
				status: change.PatchUnavailable,
				detail: "binary file has no patch",
			},
			want: "binary file has no patch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			review := seedTestReview(t, "a.go")
			next, err := Initialize(
				t.Context(),
				test.loader,
				source.Opened{},
				&source.Revision{},
				review,
				patchContext,
				readSeedTestDocument(t, test.document),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) || next.CommentCount() != 0 || review.CommentCount() != 0 {
				t.Fatalf("Initialize() = %d comments, error %v, want %q", next.CommentCount(), err, test.want)
			}
		})
	}
}

func TestInitializeObservesCancellationBeforeMaterialization(t *testing.T) {
	review := seedTestReview(t, "a.go")
	document := readSeedTestDocument(t, `{"comments":[{"target":{"type":"line","path":"a.go","side":"new","line":2},"binding":"active","comment":"x"}]}`)
	loader := readySeedPatchLoader(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("stop")
	cancel(cause)
	next, err := Initialize(ctx, loader, source.Opened{}, &source.Revision{}, review, change.WholeFileContext(), document)
	if !errors.Is(err, cause) || loader.calls != 0 || next.CommentCount() != 0 {
		t.Fatalf("cancelled initialization = calls %d comments %d error %v", loader.calls, next.CommentCount(), err)
	}
}

func readySeedPatchLoader(t *testing.T) *seedPatchLoader {
	t.Helper()
	prepared, err := change.TakePatch([]byte("@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n"))
	if err != nil {
		t.Fatal(err)
	}
	return &seedPatchLoader{prepared: prepared, status: change.PatchReady}
}

func readSeedTestDocument(t *testing.T, value string) Document {
	t.Helper()
	document, err := Read(strings.NewReader(value))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func seedTestReview(t *testing.T, paths ...string) change.Review {
	t.Helper()
	sourceID, err := change.NewSourceID(1)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := change.NewSnapshotBuilder(sourceID, len(paths))
	if err != nil {
		t.Fatal(err)
	}
	flags, err := change.NewFlags(change.FileTraits{}, change.HiddenReasons{})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range paths {
		path, pathErr := change.NewPath([]byte(value))
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		fileChange, changeErr := change.NewUnclassifiedChange(change.ChangeModified, 1, 1)
		if changeErr != nil {
			t.Fatal(changeErr)
		}
		if _, addErr := builder.AddFile(&path, &path, fileChange, flags); addErr != nil {
			t.Fatal(addErr)
		}
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	review, err := change.NewReview(change.ReviewRequest{Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	return review
}
