package review

import (
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"
)

func TestOperationDetailOwnsBackingAcrossRetainedStates(t *testing.T) {
	t.Run("operation settlement", func(t *testing.T) {
		current := testReview(t, testSnapshot(t, 1, "one.go"))
		firstID, _ := NewOperationID(1)
		current, err := current.BeginOperation(firstID, OperationPatch)
		if err != nil {
			t.Fatal(err)
		}
		firstSource, firstDetail := oversizedOperationDetail(t, "failed operation")
		current, err = current.ResolveOperation(firstID, OperationFailed, firstDetail)
		if err != nil {
			t.Fatal(err)
		}
		assertOperationDetailOwned(t, current.Operations()[firstID].Detail, firstSource)

		secondID, _ := NewOperationID(2)
		current, err = current.BeginOperation(secondID, OperationPublication)
		if err != nil {
			t.Fatal(err)
		}
		unknownSource, unknownDetail := oversizedOperationDetail(t, "unknown operation")
		current, err = current.ResolveOperation(secondID, OperationUnknown, unknownDetail)
		if err != nil {
			t.Fatal(err)
		}
		assertOperationDetailOwned(t, current.Operations()[secondID].Detail, unknownSource)

		settledSource, settledDetail := oversizedOperationDetail(t, "settled unknown operation")
		current, err = current.SettleUnknownOperation(secondID, OperationFailed, settledDetail)
		if err != nil {
			t.Fatal(err)
		}
		assertOperationDetailOwned(t, current.Operations()[secondID].Detail, settledSource)
	})

	t.Run("search result", func(t *testing.T) {
		current := testReview(t, testSnapshot(t, 1, "one.go"))
		id, _ := NewOperationID(1)
		current.search = searchState{status: SearchActive, operation: id}
		source, detail := oversizedOperationDetail(t, "search failed")
		next, err := current.ApplySearch(SearchResult{
			Operation: id,
			Workspace: current.workspace,
			Source:    current.snapshot.SourceID(),
			Status:    SearchFailed,
			detail:    detail,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertOperationDetailOwned(t, next.SearchSummary().Detail, source)
	})

	t.Run("publication and reconciliation", func(t *testing.T) {
		current := testReview(t, testSnapshot(t, 1, "one.go"))
		publicationID, _ := NewOperationID(1)
		current.publication = publicationState{
			status: PublicationPublishing, operation: publicationID,
		}
		publicationSource, publicationDetail := oversizedOperationDetail(t, "publication failed")
		next, err := current.ApplyPublication(PublicationResult{
			Operation:   publicationID,
			Workspace:   current.workspace,
			Source:      current.snapshot.SourceID(),
			Disposition: PublicationDispositionFailedBeforeCommit,
			Detail:      publicationDetail,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertOperationDetailOwned(t, next.PublicationSummary().Detail, publicationSource)

		reconciliationID, _ := NewOperationID(2)
		next.publication.status = PublicationReconciling
		next.publication.operation = reconciliationID
		reconciliationSource, reconciliationDetail := oversizedOperationDetail(t, "reconciliation unknown")
		next, err = next.ApplyReconciliation(ReconciliationResult{
			Operation:   reconciliationID,
			Workspace:   next.workspace,
			Source:      next.snapshot.SourceID(),
			Disposition: ReconciliationStillUnknown,
			Detail:      reconciliationDetail,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertOperationDetailOwned(t, next.PublicationSummary().Detail, reconciliationSource)
	})
}

func TestNewOperationDetailRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{
		strings.Repeat("x", int(MaxOperationDetail)+1),
		string([]byte{0xff}),
	} {
		if _, err := NewOperationDetail(value); err == nil {
			t.Fatal("invalid operation detail was accepted")
		}
	}
	if detail, err := NewOperationDetail(""); err != nil || !detail.IsEmpty() {
		t.Fatalf("empty operation detail = %+v, %v", detail, err)
	}
}

func TestBoundedOperationDetailLimitsWorkBeforeUTF8Normalization(t *testing.T) {
	rawPrefix := strings.Repeat("x", int(MaxOperationDetail)-1) + string([]byte{0xff})
	largeTail := strings.Repeat(string([]byte{0xfe}), 1<<20)
	detail := NewBoundedOperationDetail(rawPrefix + largeTail)
	if detail.Bytes() > MaxOperationDetail || !utf8.ValidString(detail.String()) {
		t.Fatalf("bounded detail uses %d bytes and valid=%v", detail.Bytes(), utf8.ValidString(detail.String()))
	}
	if strings.Contains(detail.String(), "\uFFFD\uFFFD") {
		t.Fatal("normalization inspected bytes beyond the bounded raw prefix")
	}

	trimmed := NewBoundedTrimmedOperationDetail("  detail  " + largeTail)
	if !strings.HasPrefix(trimmed.String(), "detail  ") || trimmed.Bytes() > MaxOperationDetail {
		t.Fatalf("trimmed bounded detail = %q (%d bytes)", trimmed.String(), trimmed.Bytes())
	}
}

func BenchmarkNewBoundedOperationDetailLargeInvalidInput(b *testing.B) {
	for _, testCase := range []struct {
		name string
		size int
	}{
		{name: "1MiB", size: 1 << 20},
		{name: "16MiB", size: 16 << 20},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			value := strings.Repeat(string([]byte{0xff, 'x'}), testCase.size/2)
			b.ReportAllocs()
			b.SetBytes(int64(len(value)))
			b.ResetTimer()
			for b.Loop() {
				_ = NewBoundedOperationDetail(value)
			}
		})
	}
}

func oversizedOperationDetail(t *testing.T, value string) (string, OperationDetail) {
	t.Helper()
	parent := strings.Repeat("x", 1<<20) + value
	source := parent[len(parent)-len(value):]
	detail, err := NewOperationDetail(source)
	if err != nil {
		t.Fatal(err)
	}
	return source, detail
}

func assertOperationDetailOwned(t *testing.T, detail OperationDetail, source string) {
	t.Helper()
	if unsafe.StringData(detail.String()) == unsafe.StringData(source) {
		t.Fatal("retained operation detail aliases caller backing")
	}
}
