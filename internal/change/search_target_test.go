package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

func TestSearchAccountingChargesCoverLayouts(t *testing.T) {
	tests := []struct {
		name   string
		charge int64
		layout uintptr
	}{
		{name: "file ID", charge: searchFileIDStorageBytes, layout: unsafe.Sizeof(FileID(0))},
		{name: "hit", charge: SearchHitStorageBytes, layout: unsafe.Sizeof(SearchHit{})},
		{name: "target", charge: searchTargetMetadataBytes, layout: unsafe.Sizeof(SearchTarget{})},
		{
			name:   "prepared query",
			charge: preparedSearchQueryMetadataBytes,
			layout: unsafe.Sizeof(PreparedSearchQuery{}),
		},
		{name: "request", charge: searchRequestMetadataBytes, layout: unsafe.Sizeof(SearchRequest{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.charge < int64(test.layout) {
				t.Fatalf("metadata charge = %d, layout = %d", test.charge, test.layout)
			}
		})
	}
}

func TestSearchTargetOwnsExactOrderedFiles(t *testing.T) {
	snapshot := testSnapshot(t, 1, "one.go", "two.go", "three.go")
	files := []FileID{2, 0}
	target, err := NewSearchTarget(snapshot, files)
	if err != nil {
		t.Fatal(err)
	}
	files[0] = 1
	if got := slices.Collect(target.Files()); !slices.Equal(got, []FileID{2, 0}) {
		t.Fatalf("target files = %v, want [2 0]", got)
	}
	if target.RetainedBytes() != 8 {
		t.Fatalf("retained bytes = %d, want 8", target.RetainedBytes())
	}
}

func TestSearchTargetRejectsInvalidMembership(t *testing.T) {
	snapshot := testSnapshot(t, 1, "one.go", "two.go")
	for name, files := range map[string][]FileID{
		"empty":     nil,
		"duplicate": {0, 0},
		"missing":   {2},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewSearchTarget(snapshot, files); err == nil {
				t.Fatal("invalid target was accepted")
			}
		})
	}
}

func TestStartSearchRejectsTargetFromAnotherSource(t *testing.T) {
	target, err := NewSearchTarget(testSnapshot(t, 1, "one.go"), []FileID{0})
	if err != nil {
		t.Fatal(err)
	}
	current, err := NewReview(testReviewRequest(testSnapshot(t, 2, "one.go")))
	if err != nil {
		t.Fatal(err)
	}
	query, _ := NewSearchQuery("one")
	_, admission, err := current.StartSearch(1, query, WholeFileContext())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSearchRequestCancellable(nil, admission, target); err == nil {
		t.Fatal("stale target was accepted")
	}
}

func TestSearchHitExcerptIsBoundedAndContainsTheMatch(t *testing.T) {
	line := strings.Repeat("a", 300) + "needle" + strings.Repeat("b", 300)
	patch, err := TakePatch([]byte("@@ -0,0 +1 @@\n+" + line + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	query, _ := NewSearchQuery("needle")
	batch := ScanSearchBatch(patch, 0, PrepareSearchQuery(query), SearchCursor{})
	if batch.HitCount() != 1 {
		t.Fatalf("hits = %d, want 1", batch.HitCount())
	}
	hit, ok := batch.Hit(0)
	if !ok {
		t.Fatal("first hit is unavailable")
	}
	excerpt := hit.Excerpt()
	if len(excerpt) > MaxSearchExcerptBytes || !strings.Contains(excerpt, "needle") {
		t.Fatalf("excerpt bytes=%d value=%q", len(excerpt), excerpt)
	}
	if hit.RetainedBytes() != SearchHitStorageBytes+int64(len(excerpt)) {
		t.Fatalf("retained bytes = %d", hit.RetainedBytes())
	}
}

func TestSearchBatchCapacityGrowthAndPointAccess(t *testing.T) {
	tests := []struct {
		name         string
		matches      int
		wantCapacity int
	}{
		{name: "zero", matches: 0, wantCapacity: 0},
		{name: "seed", matches: 1, wantCapacity: searchHitGrowthSeed},
		{name: "full seed", matches: searchHitGrowthSeed, wantCapacity: searchHitGrowthSeed},
		{name: "first growth", matches: searchHitGrowthSeed + 1, wantCapacity: 16},
		{name: "exact batch limit", matches: maxSearchBatchHits, wantCapacity: maxSearchBatchHits},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			patchText := fmt.Sprintf("@@ -0,0 +1,%d @@\n", max(test.matches, 1))
			if test.matches == 0 {
				patchText += "+absent\n"
			} else {
				patchText += strings.Repeat("+x\n", test.matches)
			}
			patch, err := TakePatch([]byte(patchText))
			if err != nil {
				t.Fatal(err)
			}
			query, _ := NewSearchQuery("x")
			batch := ScanSearchBatch(
				patch,
				0,
				PrepareSearchQuery(query),
				SearchCursor{line: 1},
			)
			if batch.HitCount() != test.matches || cap(batch.hits) != test.wantCapacity {
				t.Fatalf(
					"batch hits/capacity = %d/%d, want %d/%d",
					batch.HitCount(),
					cap(batch.hits),
					test.matches,
					test.wantCapacity,
				)
			}
			if _, ok := batch.Hit(-1); ok {
				t.Fatal("negative hit index was accepted")
			}
			if _, ok := batch.Hit(batch.HitCount()); ok {
				t.Fatal("end hit index was accepted")
			}
			if test.matches != 0 {
				hit, ok := batch.Hit(test.matches - 1)
				if !ok || hit.Excerpt() != "x" {
					t.Fatalf("last hit = %q/%t, want x/true", hit.Excerpt(), ok)
				}
			}
		})
	}
}

func TestSearchBatchConstructionBoundsMatchGrowthPhases(t *testing.T) {
	wantRetained := int64(maxSearchBatchHits) *
		(SearchHitStorageBytes + MaxSearchExcerptBytes)
	wantGrowth := int64(maxSearchBatchHits/2)*MaxSearchExcerptBytes +
		int64(maxSearchBatchHits/2+maxSearchBatchHits)*SearchHitStorageBytes +
		MaxSearchExcerptBytes
	if MaxSearchBatchRetainedBytes != wantRetained ||
		maxSearchBatchGrowthBytes != wantGrowth ||
		MaxSearchBatchConstructionBytes != max(wantRetained, wantGrowth) {
		t.Fatalf(
			"batch retained/growth/construction = %d/%d/%d, want %d/%d/%d",
			MaxSearchBatchRetainedBytes,
			maxSearchBatchGrowthBytes,
			MaxSearchBatchConstructionBytes,
			wantRetained,
			wantGrowth,
			max(wantRetained, wantGrowth),
		)
	}
	wantAggregateGrowth := int64(maxSearchAggregatePredecessorHits)*MaxSearchExcerptBytes +
		int64(maxSearchAggregatePredecessorHits+MaxSearchHits)*SearchHitStorageBytes +
		MaxSearchExcerptBytes
	if maxSearchAggregateGrowthBytes != wantAggregateGrowth ||
		MaxSearchAggregateConstructionBytes != max(MaxSearchHitBytes, wantAggregateGrowth) {
		t.Fatalf(
			"aggregate growth/construction = %d/%d, want %d/%d",
			maxSearchAggregateGrowthBytes,
			MaxSearchAggregateConstructionBytes,
			wantAggregateGrowth,
			max(MaxSearchHitBytes, wantAggregateGrowth),
		)
	}
}

func TestSearchBatchHitLimitResumesSameLineWithoutDuplicates(t *testing.T) {
	line := strings.Repeat("x ", maxSearchBatchHits+2)
	patch, err := TakePatch([]byte("@@ -0,0 +1 @@\n+" + line + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	query, err := NewSearchQuery("x")
	if err != nil {
		t.Fatal(err)
	}
	prepared := PrepareSearchQuery(query)

	first := ScanSearchBatch(patch, 0, prepared, SearchCursor{line: 1})
	if first.HitCount() != maxSearchBatchHits || cap(first.hits) != maxSearchBatchHits ||
		first.Done() {
		t.Fatalf(
			"first batch hits/capacity/done = %d/%d/%t",
			first.HitCount(),
			cap(first.hits),
			first.Done(),
		)
	}
	wantCursor := SearchCursor{
		line:   1,
		offset: 2*maxSearchBatchHits - 1,
	}
	if first.Cursor() != wantCursor {
		t.Fatalf("first cursor = %+v, want %+v", first.Cursor(), wantCursor)
	}
	retained := int64(cap(first.hits)) * SearchHitStorageBytes
	for _, hit := range first.hits {
		retained += int64(len(hit.excerpt))
	}
	if retained != MaxSearchBatchRetainedBytes || retained > MaxSearchBatchConstructionBytes {
		t.Fatalf(
			"first batch retained/construction limit = %d/%d, want retained %d",
			retained,
			MaxSearchBatchConstructionBytes,
			MaxSearchBatchRetainedBytes,
		)
	}

	second := ScanSearchBatch(patch, 0, prepared, first.Cursor())
	if second.HitCount() != 2 || !second.Done() {
		t.Fatalf("second batch hits/done = %d/%t, want 2/true", second.HitCount(), second.Done())
	}
	wantRanges := [][2]uint32{
		{2 * maxSearchBatchHits, 2*maxSearchBatchHits + 1},
		{2*maxSearchBatchHits + 2, 2*maxSearchBatchHits + 3},
	}
	for index, want := range wantRanges {
		hit, ok := second.Hit(index)
		if !ok || hit.byteStart != want[0] || hit.byteEnd != want[1] {
			t.Fatalf(
				"second hit %d range = [%d,%d]/%t, want [%d,%d]/true",
				index,
				hit.byteStart,
				hit.byteEnd,
				ok,
				want[0],
				want[1],
			)
		}
	}
}

func TestSearchBatchByteLimitPreservesPartialMatchOnSameLine(t *testing.T) {
	//nolint:dupword // Adjacent identical matches exercise exact resume ranges.
	line := strings.Repeat("x", MaxSearchBatchBytes-3) + "needle needle"
	patch, err := TakePatch([]byte("@@ -0,0 +1 @@\n+" + line + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	query, err := NewSearchQuery("needle")
	if err != nil {
		t.Fatal(err)
	}
	prepared := PrepareSearchQuery(query)

	first := ScanSearchBatch(patch, 0, prepared, SearchCursor{line: 1})
	wantCursor := SearchCursor{
		line:    1,
		offset:  MaxSearchBatchBytes,
		matched: 3,
	}
	if first.HitCount() != 0 || first.Done() || first.Cursor() != wantCursor {
		t.Fatalf(
			"first batch hits/done/cursor = %d/%t/%+v, want 0/false/%+v",
			first.HitCount(),
			first.Done(),
			first.Cursor(),
			wantCursor,
		)
	}

	second := ScanSearchBatch(patch, 0, prepared, first.Cursor())
	if second.HitCount() != 2 || !second.Done() {
		t.Fatalf("second batch hits/done = %d/%t, want 2/true", second.HitCount(), second.Done())
	}
	wantRanges := [][2]uint32{
		{MaxSearchBatchBytes - 3, MaxSearchBatchBytes + 3},
		{MaxSearchBatchBytes + 4, MaxSearchBatchBytes + 10},
	}
	for index, want := range wantRanges {
		hit, ok := second.Hit(index)
		if !ok || hit.byteStart != want[0] || hit.byteEnd != want[1] {
			t.Fatalf(
				"second hit %d range = [%d,%d]/%t, want [%d,%d]/true",
				index,
				hit.byteStart,
				hit.byteEnd,
				ok,
				want[0],
				want[1],
			)
		}
	}
}

func TestSearchBatchZeroHitScanAndConsumeAllocatesNothing(t *testing.T) {
	patch, err := TakePatch([]byte("@@ -1,2 +1,2 @@\n one\n-two\n+three\n"))
	if err != nil {
		t.Fatal(err)
	}
	query, _ := NewSearchQuery("absent")
	prepared := PrepareSearchQuery(query)
	allocations := testing.AllocsPerRun(100, func() {
		batch := ScanSearchBatch(patch, 0, prepared, SearchCursor{})
		for index := range batch.HitCount() {
			searchBatchHitSink, _ = batch.Hit(index)
		}
	})
	if allocations != 0 {
		t.Fatalf("zero-hit scan allocations = %f, want 0", allocations)
	}
}

func TestSearchResultRejectsOversizedExcerpt(t *testing.T) {
	hits := []SearchHit{{excerpt: strings.Repeat("x", MaxSearchExcerptBytes+1)}}
	_, err := TakeSearchResult(SearchResultDescriptor{
		Operation: 1,
		Workspace: 1,
		Source:    1,
		Status:    SearchComplete,
	}, hits)
	if err == nil {
		t.Fatal("oversized excerpt was accepted")
	}
}

func TestSearchResultAccountingIncludesSliceCapacity(t *testing.T) {
	hits := make([]SearchHit, 1, 1_000)
	hits[0].excerpt = "match"
	want := int64(cap(hits))*SearchHitStorageBytes + int64(len(hits[0].excerpt))
	result, err := TakeSearchResult(SearchResultDescriptor{
		Operation: 1,
		Workspace: 1,
		Source:    1,
		Status:    SearchComplete,
	}, hits)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.RetainedBytes(); got != want {
		t.Fatalf("retained bytes = %d, want %d", got, want)
	}

	oversizedCapacity := int(MaxSearchHitBytes/SearchHitStorageBytes) + 1
	oversized := make([]SearchHit, 1, oversizedCapacity)
	if _, err := TakeSearchResult(SearchResultDescriptor{
		Operation: 1,
		Workspace: 1,
		Source:    1,
		Status:    SearchComplete,
	}, oversized); err == nil {
		t.Fatal("oversized hit backing capacity was accepted")
	}
}

func TestSearchTargetConstructionIsCancellableAndExactCapacity(t *testing.T) {
	paths := make([]string, 4_097)
	files := make([]FileID, len(paths))
	for index := range paths {
		paths[index] = "file-" + string(rune(index+1))
		files[index] = FileID(index)
	}
	snapshot := testSnapshot(t, 1, paths...)
	target, err := NewSearchTargetCancellable(nil, snapshot, files)
	if err != nil {
		t.Fatal(err)
	}
	if target.FileCount() != len(files) || cap(target.files) != len(files) ||
		target.RetainedBytes() != int64(len(files))*searchFileIDStorageBytes {
		t.Fatalf(
			"target count/capacity/bytes = %d/%d/%d",
			target.FileCount(),
			cap(target.files),
			target.RetainedBytes(),
		)
	}

	checks := 0
	_, err = NewSearchTargetCancellable(func() error {
		checks++
		if checks == 3 {
			return context.Canceled
		}
		return nil
	}, snapshot, files)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled target error = %v", err)
	}
}

func TestPreparedSearchQueryAccountingAndCancellation(t *testing.T) {
	query, err := NewSearchQuery(strings.Repeat("x", MaxSearchQueryBytes))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSearchQuery(strings.Repeat("x", MaxSearchQueryBytes+1)); err == nil {
		t.Fatal("one-byte-over query was accepted")
	}
	prepared, err := PrepareSearchQueryCancellable(nil, query)
	if err != nil {
		t.Fatal(err)
	}
	want := preparedSearchQueryMetadataBytes + int64(len(query))*5
	if prepared.RetainedBytes() != want || cap(prepared.bytes) != len(query) ||
		cap(prepared.prefix) != len(query) {
		t.Fatalf(
			"prepared bytes/capacities = %d/%d/%d, want %d/%d/%d",
			prepared.RetainedBytes(),
			cap(prepared.bytes),
			cap(prepared.prefix),
			want,
			len(query),
			len(query),
		)
	}
	construction, err := SearchRequestConstructionBytes(query, MaxFiles)
	if err != nil {
		t.Fatal(err)
	}
	wantConstruction := searchRequestMetadataBytes +
		int64(MaxFiles)*searchFileIDStorageBytes + int64(len(query))*5
	if construction != wantConstruction {
		t.Fatalf("request construction bytes = %d, want %d", construction, wantConstruction)
	}
	if _, err := SearchRequestConstructionBytes(query, MaxFiles+1); err == nil {
		t.Fatal("one-file-over request construction was accepted")
	}

	checks := 0
	_, err = PrepareSearchQueryCancellable(func() error {
		checks++
		if checks == 35 {
			return context.Canceled
		}
		return nil
	}, query)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query error = %v", err)
	}
	_, err = PrepareSearchQueryCancellable(func() error { return context.Canceled }, query)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled-before-allocation query error = %v", err)
	}
}

func TestSearchResultSealingIsCancellableAndApplyUsesCachedBytes(t *testing.T) {
	hits := make([]SearchHit, 4_097, 5_000)
	checks := 0
	_, err := TakeSearchResultCancellable(func() error {
		checks++
		if checks == 3 {
			return context.Canceled
		}
		return nil
	}, SearchResultDescriptor{
		Operation: 1,
		Workspace: 1,
		Source:    1,
		Status:    SearchComplete,
	}, hits)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled result error = %v", err)
	}

	result, err := TakeSearchResult(SearchResultDescriptor{
		Operation: 1,
		Workspace: 1,
		Source:    1,
		Status:    SearchComplete,
	}, hits)
	if err != nil {
		t.Fatal(err)
	}
	current := testReview(t, testSnapshot(t, 1, "one.go"))
	current.search = searchState{status: SearchActive, operation: 1}
	next, err := current.ApplySearch(result)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := int64(cap(hits)) * SearchHitStorageBytes
	if next.search.hitBytes != wantBytes || result.RetainedBytes() != wantBytes ||
		result.Operation() != 1 || result.Workspace() != 1 || result.Source() != 1 ||
		result.Status() != SearchComplete {
		t.Fatalf(
			"sealed result = bytes %d/%d identities %d/%d/%d status %v",
			next.search.hitBytes,
			result.RetainedBytes(),
			result.Operation(),
			result.Workspace(),
			result.Source(),
			result.Status(),
		)
	}
}
