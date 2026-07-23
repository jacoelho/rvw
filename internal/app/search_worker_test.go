package app

import (
	"context"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"github.com/jacoelho/rvw/internal/review"
)

var searchAccumulatorSink searchAccumulator

func TestSearchCandidatesBorrowImmutableBackingAcrossHeaderReplacement(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "one\n",
		"beta.txt":  "two\n",
	})
	settleModelWork(t, model, coordinator)
	original := model.fileOrder
	candidates, err := newSearchCandidates(model.review, original)
	if err != nil {
		t.Fatal(err)
	}
	model.fileOrder = slices.Clone(original[:1])
	if candidates.count != len(original) || !slices.Equal(candidates.files, original) ||
		len(candidates.files) == 0 || &candidates.files[0] != &original[0] {
		t.Fatalf(
			"borrowed candidates count/backing = %d/%t, want %d/true",
			candidates.count,
			len(candidates.files) != 0 && &candidates.files[0] == &original[0],
			len(original),
		)
	}
}

func TestSearchWorkAccountingIncludesBorrowAndPreparedConstruction(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "one\n"})
	settleModelWork(t, model, coordinator)
	files := make([]review.FileID, 1, 1_000)
	candidates, err := newSearchCandidates(model.review, files)
	if err != nil {
		t.Fatal(err)
	}
	query, _ := review.NewSearchQuery("needle")
	_, admission, err := model.review.StartSearch(10_000, query, review.WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	work := SearchWork{
		Document:   model.document,
		Candidates: candidates,
		Admission:  admission,
	}
	requestBytes, err := work.requestBytes()
	if err != nil {
		t.Fatal(err)
	}
	candidateBytes, _ := review.SearchCandidateRetainedBytes(cap(files))
	wantRequest := int64(unsafe.Sizeof(work)) + int64(unsafe.Sizeof(*candidates)) +
		candidateBytes + int64(len(query))
	if requestBytes != wantRequest {
		t.Fatalf("request bytes = %d, want %d", requestBytes, wantRequest)
	}
	construction, _ := review.SearchRequestConstructionBytes(query, len(files))
	resultBytes, err := work.resultBytes()
	if err != nil {
		t.Fatal(err)
	}
	wantSearchPhase := max(
		review.MaxSearchHitBytes+review.MaxSearchBatchConstructionBytes,
		review.MaxSearchAggregateConstructionBytes+review.MaxSearchBatchRetainedBytes,
	)
	wantResult := construction + patchResultReservation + wantSearchPhase
	if resultBytes != wantResult || resultBytes > MaxRetainedResultBytes {
		t.Fatalf("result bytes = %d, want %d within %d", resultBytes, wantResult, MaxRetainedResultBytes)
	}
}

func TestSearchAccumulatorGrowsGeometricallyToExactCountLimit(t *testing.T) {
	hit := searchAccumulatorTestHit(t, 1)
	var accumulator searchAccumulator
	if accumulator.hits != nil || accumulator.retainedBytes != 0 {
		t.Fatalf(
			"zero accumulator = %v/%d, want nil/0",
			accumulator.hits,
			accumulator.retainedBytes,
		)
	}
	wantCapacities := map[int]int{
		1:                               searchAccumulatorGrowthSeed,
		searchAccumulatorGrowthSeed:     searchAccumulatorGrowthSeed,
		searchAccumulatorGrowthSeed + 1: 16,
		65_536:                          65_536,
		65_537:                          review.MaxSearchHits,
		review.MaxSearchHits:            review.MaxSearchHits,
	}
	for count := 1; count <= review.MaxSearchHits; count++ {
		if !accumulator.append(hit) {
			t.Fatalf("append %d was rejected", count)
		}
		if wantCapacity, ok := wantCapacities[count]; ok && cap(accumulator.hits) != wantCapacity {
			t.Fatalf(
				"capacity after %d hits = %d, want %d",
				count,
				cap(accumulator.hits),
				wantCapacity,
			)
		}
	}
	if accumulator.append(hit) {
		t.Fatal("one hit over the count limit was accepted")
	}
	wantBytes := int64(review.MaxSearchHits) * (review.SearchHitStorageBytes + 1)
	if len(accumulator.hits) != review.MaxSearchHits ||
		cap(accumulator.hits) != review.MaxSearchHits ||
		accumulator.retainedBytes != wantBytes {
		t.Fatalf(
			"count/capacity/bytes = %d/%d/%d, want %d/%d/%d",
			len(accumulator.hits),
			cap(accumulator.hits),
			accumulator.retainedBytes,
			review.MaxSearchHits,
			review.MaxSearchHits,
			wantBytes,
		)
	}
}

func TestSearchAccumulatorStopsAtExactByteLimit(t *testing.T) {
	hit := searchAccumulatorTestHit(t, review.MaxSearchExcerptBytes)
	var accumulator searchAccumulator
	for accumulator.append(hit) {
	}
	wantHits := int(
		(review.MaxSearchHitBytes - int64(review.MaxSearchHits)*review.SearchHitStorageBytes) /
			review.MaxSearchExcerptBytes,
	)
	wantBytes := int64(review.MaxSearchHits)*review.SearchHitStorageBytes +
		int64(wantHits)*review.MaxSearchExcerptBytes
	if len(accumulator.hits) != wantHits || cap(accumulator.hits) != review.MaxSearchHits ||
		accumulator.retainedBytes != wantBytes ||
		accumulator.retainedBytes > review.MaxSearchHitBytes {
		t.Fatalf(
			"byte-limited count/capacity/bytes = %d/%d/%d, want %d/%d/%d",
			len(accumulator.hits),
			cap(accumulator.hits),
			accumulator.retainedBytes,
			wantHits,
			review.MaxSearchHits,
			wantBytes,
		)
	}
}

func TestScanSearchPatchCancellationDropsAccumulatedHits(t *testing.T) {
	hit := searchAccumulatorTestHit(t, 1)
	accumulator := searchAccumulator{}
	if !accumulator.append(hit) {
		t.Fatal("test hit was rejected")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	status, got := (&Coordinator{}).scanSearchPatch(
		searchJob{ctx: ctx},
		1,
		review.SearchRequest{},
		nil,
		0,
		0,
		1,
		review.SearchComplete,
		accumulator,
	)
	if status != review.SearchCancelled || got.hits != nil || got.retainedBytes != 0 {
		t.Fatalf(
			"cancelled status/hits/bytes = %v/%v/%d",
			status,
			got.hits,
			got.retainedBytes,
		)
	}
}

func TestFinishSearchResultPreservesAccumulatorAndDetailAccounting(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "one\n"})
	settleModelWork(t, model, coordinator)
	query, _ := review.NewSearchQuery("needle")
	_, admission, err := model.review.StartSearch(10_003, query, review.WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := newSearchCandidates(model.review, model.fileOrder)
	if err != nil {
		t.Fatal(err)
	}
	job := searchJob{
		ctx: t.Context(),
		work: SearchWork{
			Document:   model.document,
			Candidates: candidates,
			Admission:  admission,
		},
	}
	request, err := coordinator.prepareSearchRequest(&job)
	if err != nil {
		t.Fatal(err)
	}
	hit := searchAccumulatorTestHit(t, 8)
	empty, err := finishSearchResult(
		func() error { return nil },
		request,
		review.SearchComplete,
		0,
		searchAccumulator{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if empty.HitCount() != 0 || empty.RetainedBytes() != 0 {
		t.Fatalf(
			"empty result hits/bytes = %d/%d, want 0/0",
			empty.HitCount(),
			empty.RetainedBytes(),
		)
	}
	tests := []struct {
		name        string
		status      review.SearchStatus
		unavailable uint32
		wantStatus  review.SearchStatus
		wantDetail  bool
	}{
		{name: "complete", status: review.SearchComplete, wantStatus: review.SearchComplete},
		{name: "limited", status: review.SearchLimited, wantStatus: review.SearchLimited, wantDetail: true},
		{name: "partial", status: review.SearchComplete, unavailable: 2, wantStatus: review.SearchPartial, wantDetail: true},
		{name: "cancelled", status: review.SearchCancelled, wantStatus: review.SearchCancelled, wantDetail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accumulator := searchAccumulator{}
			if !accumulator.append(hit) {
				t.Fatal("test hit was rejected")
			}
			result, err := finishSearchResult(
				func() error { return nil },
				request,
				test.status,
				test.unavailable,
				accumulator,
			)
			if err != nil {
				t.Fatal(err)
			}
			wantBytes := accumulator.retainedBytes + result.Detail().Bytes()
			if result.Status() != test.wantStatus || result.RetainedBytes() != wantBytes ||
				result.HitCount() != 1 || result.Detail().IsEmpty() == test.wantDetail {
				t.Fatalf(
					"result status/bytes/hits/detail = %v/%d/%d/%q, want %v/%d/1/nonempty=%t",
					result.Status(),
					result.RetainedBytes(),
					result.HitCount(),
					result.Detail().String(),
					test.wantStatus,
					wantBytes,
					test.wantDetail,
				)
			}
		})
	}

	bad := searchAccumulator{}
	if !bad.append(hit) {
		t.Fatal("test hit was rejected")
	}
	bad.retainedBytes++
	if _, err := finishSearchResult(
		func() error { return nil },
		request,
		review.SearchComplete,
		0,
		bad,
	); err == nil {
		t.Fatal("accounting drift was accepted")
	}
}

func BenchmarkSearchAccumulatorSparse(b *testing.B) {
	hit := searchAccumulatorTestHit(b, 1)
	b.ReportAllocs()
	for b.Loop() {
		var accumulator searchAccumulator
		if !accumulator.append(hit) {
			b.Fatal("sparse append was rejected")
		}
		searchAccumulatorSink = accumulator
	}
}

func BenchmarkSearchAccumulatorDense(b *testing.B) {
	hit := searchAccumulatorTestHit(b, 1)
	b.ReportAllocs()
	for b.Loop() {
		var accumulator searchAccumulator
		for range review.MaxSearchHits {
			if !accumulator.append(hit) {
				b.Fatal("dense append was rejected")
			}
		}
		searchAccumulatorSink = accumulator
	}
}

func searchAccumulatorTestHit(tb testing.TB, excerptBytes int) review.SearchHit {
	tb.Helper()
	line := strings.Repeat("x", excerptBytes)
	patch, err := review.TakePatch([]byte("@@ -0,0 +1 @@\n+" + line + "\n"))
	if err != nil {
		tb.Fatal(err)
	}
	query, err := review.NewSearchQuery("x")
	if err != nil {
		tb.Fatal(err)
	}
	batch := review.ScanSearchBatch(
		patch,
		0,
		review.PrepareSearchQuery(query),
		review.SearchCursor{},
	)
	hit, ok := batch.Hit(0)
	if !ok || len(hit.Excerpt()) != excerptBytes {
		tb.Fatalf("test hit excerpt bytes = %d/%t, want %d/true", len(hit.Excerpt()), ok, excerptBytes)
	}
	return hit
}

func TestPrepareSearchRequestClearsBorrowedAndRawAdmissionOwnership(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{
		"alpha.txt": "one\n",
		"beta.txt":  "two\n",
	})
	settleModelWork(t, model, coordinator)
	query, _ := review.NewSearchQuery("needle")
	_, admission, err := model.review.StartSearch(10_001, query, review.WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := newSearchCandidates(model.review, model.fileOrder)
	if err != nil {
		t.Fatal(err)
	}
	job := searchJob{
		ctx: t.Context(),
		work: SearchWork{
			Document:   model.document,
			Candidates: candidates,
			Admission:  admission,
		},
	}
	request, err := coordinator.prepareSearchRequest(&job)
	if err != nil {
		t.Fatal(err)
	}
	if job.work.Candidates != nil || job.work.Admission.Operation() != 0 ||
		request.Operation() != admission.Operation() ||
		request.Target().FileCount() != len(model.fileOrder) ||
		request.Query().RetainedBytes() == 0 {
		t.Fatalf(
			"prepared request ownership = candidates %v admission %d request %d/%d/%d",
			job.work.Candidates,
			job.work.Admission.Operation(),
			request.Operation(),
			request.Target().FileCount(),
			request.Query().RetainedBytes(),
		)
	}
}

func TestPrepareSearchRequestRejectsInvalidCandidatesAndClearsOwnership(t *testing.T) {
	model, coordinator := newTestModel(t, map[string]string{"alpha.txt": "one\n"})
	settleModelWork(t, model, coordinator)
	query, _ := review.NewSearchQuery("needle")
	_, admission, err := model.review.StartSearch(10_002, query, review.WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := newSearchCandidates(model.review, []review.FileID{0, 0})
	if err != nil {
		t.Fatal(err)
	}
	job := searchJob{
		ctx: t.Context(),
		work: SearchWork{
			Document:   model.document,
			Candidates: candidates,
			Admission:  admission,
		},
	}
	if _, err := coordinator.prepareSearchRequest(&job); err == nil {
		t.Fatal("duplicate candidates were accepted")
	}
	if job.work.Candidates != nil || job.work.Admission.Operation() != 0 {
		t.Fatalf(
			"rejected request retained candidates/admission: %v/%d",
			job.work.Candidates,
			job.work.Admission.Operation(),
		)
	}
}
