package review

import (
	"strings"
	"testing"
)

var (
	denseSearchBatchCountSink int
	denseSearchHitCountSink   int
)

func BenchmarkSearchDenseSameLine(b *testing.B) {
	const fullHitBatches = 2
	matchCount := fullHitBatches*maxSearchBatchHits + 2
	patch, err := TakePatch([]byte(
		"@@ -0,0 +1 @@\n+" + strings.Repeat("x ", matchCount) + "\n",
	))
	if err != nil {
		b.Fatal(err)
	}
	query, err := NewSearchQuery("x")
	if err != nil {
		b.Fatal(err)
	}
	prepared := PrepareSearchQuery(query)
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		cursor := SearchCursor{}
		batchCount := 0
		hitCount := 0
		for {
			batch := ScanSearchBatch(patch, 0, prepared, cursor)
			batchCount++
			hitCount += batch.HitCount()
			if batch.Done() {
				break
			}
			cursor = batch.Cursor()
			if batchCount > fullHitBatches+1 {
				b.Fatalf("dense search did not complete after %d batches", batchCount)
			}
		}
		denseSearchBatchCountSink = batchCount
		denseSearchHitCountSink = hitCount
	}
}
