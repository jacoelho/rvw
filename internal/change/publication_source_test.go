package change

import (
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestPublicationSourcePreservesPendingAcquisitionFailure(t *testing.T) {
	snapshot := testSnapshot(t, 1, "file.go")
	detail, err := NewOperationDetail("GraphQL pending-review query was rejected")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = snapshot.WithPendingReviewStatus(PendingReviewUnavailable, detail)
	if err != nil {
		t.Fatal(err)
	}
	current, err := NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventComment, policies)
	_, err = current.PublicationSource().PreparePublication(options)
	if !errors.Is(err, ErrInvalidPublication) || !strings.Contains(err.Error(), detail.String()) {
		t.Fatalf("preparation error = %v, want invalid publication with %q", err, detail.String())
	}
}

func TestPublicationSourceIsConstantTimeAndAccountsOnlyCapturedState(t *testing.T) {
	current, err := NewReview(testReviewRequest(testSnapshot(t, 1, "file.go")))
	if err != nil {
		t.Fatal(err)
	}
	current = saveTestComment(t, current, ReviewCommentTarget(), summaryValue)

	var captured PublicationSource
	if allocations := testing.AllocsPerRun(100, func() {
		captured = current.PublicationSource()
	}); allocations != 0 {
		t.Fatalf("PublicationSource allocations = %f, want 0", allocations)
	}
	wantRetained := current.retainedBytes - draftRetainedBytes(current.draft) +
		publicationSourceMetadataBytes
	if !captured.Ready() || captured.RetainedBytes() != wantRetained {
		t.Fatalf(
			"source = ready %t retained %d, want true and %d",
			captured.Ready(),
			captured.RetainedBytes(),
			wantRetained,
		)
	}
	if captured.ConstructionBytes() <= captured.RetainedBytes() {
		t.Fatalf(
			"construction bytes = %d, retained = %d",
			captured.ConstructionBytes(),
			captured.RetainedBytes(),
		)
	}
	if captured.Workspace() != current.workspace || captured.Source() != current.snapshot.SourceID() {
		t.Fatalf(
			"source identity = workspace %d source %d",
			captured.Workspace(),
			captured.Source(),
		)
	}
	if publicationSourceMetadataBytes < int64(unsafe.Sizeof(PublicationSource{})) {
		t.Fatalf(
			"source metadata charge = %d, layout = %d",
			publicationSourceMetadataBytes,
			unsafe.Sizeof(PublicationSource{}),
		)
	}

	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventComment, policies)
	fromSource, err := captured.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	fromReview, err := current.PreparePublication(options)
	if err != nil {
		t.Fatal(err)
	}
	if fromSource.SummaryBody() != fromReview.SummaryBody() ||
		fromSource.IncludedCount() != fromReview.IncludedCount() ||
		fromSource.RetainedBytes() == 0 ||
		fromSource.RetainedBytes() > captured.ConstructionBytes() {
		t.Fatalf(
			"source preparation = summary %q included %d retained %d; review = summary %q included %d",
			fromSource.SummaryBody(),
			fromSource.IncludedCount(),
			fromSource.RetainedBytes(),
			fromReview.SummaryBody(),
			fromReview.IncludedCount(),
		)
	}
}

func TestPublicationSourceChargesPendingBaselineFromCachedComponents(t *testing.T) {
	current, err := NewReview(testReviewRequest(pendingTestSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	var source PublicationSource
	if allocations := testing.AllocsPerRun(100, func() {
		source = current.PublicationSource()
	}); allocations != 0 {
		t.Fatalf("pending PublicationSource allocations = %f, want 0", allocations)
	}
	want := current.retainedBytes - draftRetainedBytes(current.draft) +
		current.snapshot.pendingRetainedBytes + publicationSourceMetadataBytes
	if source.RetainedBytes() != want {
		t.Fatalf("pending source retained bytes = %d, want %d", source.RetainedBytes(), want)
	}
}

func TestPublicationOptionsRetainedBytesCoversFixedLayout(t *testing.T) {
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventComment, policies)
	if options.RetainedBytes() != publicationOptionsBytes ||
		publicationOptionsBytes < int64(unsafe.Sizeof(PublicationOptions{})) {
		t.Fatalf(
			"options retained bytes = %d, charge = %d, layout = %d",
			options.RetainedBytes(),
			publicationOptionsBytes,
			unsafe.Sizeof(PublicationOptions{}),
		)
	}
	if (PublicationOptions{}).RetainedBytes() != 0 {
		t.Fatal("invalid options reported retained ownership")
	}
}

func TestPreparePublicationCancellableChecksLargeSummaryByByteInterval(t *testing.T) {
	current, err := NewReview(testReviewRequest(testSnapshot(t, 1, "file.go")))
	if err != nil {
		t.Fatal(err)
	}
	current = saveTestComment(
		t,
		current,
		ReviewCommentTarget(),
		strings.Repeat("x", int(MaxCommentBodyBytes)-1),
	)
	policies, _ := NewPublicationPolicies()
	options, _ := NewPublicationOptions(PublicationEventComment, policies)
	cause := errors.New("cancel publication preparation")
	checks := 0
	_, err = current.PublicationSource().PreparePublicationCancellable(func() error {
		checks++
		if checks == 3 {
			return cause
		}
		return nil
	}, options)
	if !errors.Is(err, cause) {
		t.Fatalf("preparation error = %v, want %v", err, cause)
	}
	if checks != 3 {
		t.Fatalf("cancellation checks = %d, want 3", checks)
	}
}

func TestExportSourceConstructionBytesCoversSourceProjectionAndOutput(t *testing.T) {
	current, err := NewReview(testReviewRequest(testSnapshot(t, 1, "file.go")))
	if err != nil {
		t.Fatal(err)
	}
	current = saveTestComment(t, current, ReviewCommentTarget(), "body")
	source := current.ExportSource()
	input, err := source.ProjectCancellable(nil)
	if err != nil {
		t.Fatal(err)
	}
	wantMinimum := source.RetainedBytes() + input.RetainedBytes() + MaxPreparedExportBytes
	if source.ConstructionBytes() < wantMinimum {
		t.Fatalf(
			"construction bytes = %d, want at least %d",
			source.ConstructionBytes(),
			wantMinimum,
		)
	}
}
