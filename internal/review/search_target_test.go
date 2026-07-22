package review

import (
	"slices"
	"strings"
	"testing"
)

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
	current, err := NewReview(testSnapshot(t, 2, "one.go"))
	if err != nil {
		t.Fatal(err)
	}
	query, _ := NewSearchQuery("one")
	if _, _, err := current.StartSearch(1, query, target, WholeFileContext()); err == nil {
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
	if len(batch.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(batch.Hits))
	}
	excerpt := batch.Hits[0].Excerpt()
	if len(excerpt) > MaxSearchExcerptBytes || !strings.Contains(excerpt, "needle") {
		t.Fatalf("excerpt bytes=%d value=%q", len(excerpt), excerpt)
	}
	if batch.Hits[0].RetainedBytes() != searchHitRetainedBytes+int64(len(excerpt)) {
		t.Fatalf("retained bytes = %d", batch.Hits[0].RetainedBytes())
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
