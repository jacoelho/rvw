package source

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestSnapshotAcceptsMaximumFilesAndPathOwnership(t *testing.T) {
	records := make([]diffRecord, review.MaxFiles)
	basePathBytes := int(review.MaxSourcePaths) / len(records)
	extraPathBytes := int(review.MaxSourcePaths) % len(records)
	mode := review.FileMode("100644")
	for index := range records {
		pairBytes := basePathBytes
		if index < extraPathBytes {
			pairBytes++
		}
		oldPrefix := fmt.Sprintf("old/%06d/", index)
		newPrefix := fmt.Sprintf("new/%06d/", index)
		oldBytes := pairBytes / 2
		newBytes := pairBytes - oldBytes
		records[index] = diffRecord{
			oldMode: mode,
			newMode: mode,
			oldPath: oldPrefix + strings.Repeat("o", oldBytes-len(oldPrefix)),
			newPath: newPrefix + strings.Repeat("n", newBytes-len(newPrefix)),
			status:  'R',
		}
	}
	const wantRecordBytes int64 = 99_108_864
	if got := sourceSnapshotRecordBytes(records); got != wantRecordBytes {
		t.Fatalf("record ownership = %d, want %d", got, wantRecordBytes)
	}
	const wantBuilderPeak int64 = 131_508_928
	peak := sourceSnapshotRecordBytes(records) + (OpenOptions{}).RetainedBytes() +
		int64(len(records))*4 + review.SnapshotBuilderConstructionBytes(len(records))
	if peak != wantBuilderPeak || peak > maxSourceConstructionBytes {
		t.Fatalf("builder peak = %d, want %d under %d", peak, wantBuilderPeak, maxSourceConstructionBytes)
	}
	snapshot, err := (&Service{}).snapshot(
		t.Context(),
		directCPUAdmission,
		review.SourceID(1),
		nil,
		records,
		OpenOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	const wantRetained int64 = 99_108_864
	if got := snapshot.RetainedBytes(); got != wantRetained {
		t.Fatalf("snapshot retained = %d, want %d", got, wantRetained)
	}
	if wantRetained != review.MaxBaseSnapshotRetainedBytes ||
		review.MaxBaseSnapshotRetainedBytes > review.MaxSnapshotRetainedBytes {
		t.Fatalf(
			"snapshot bounds = retained %d base %d complete %d",
			wantRetained,
			review.MaxBaseSnapshotRetainedBytes,
			review.MaxSnapshotRetainedBytes,
		)
	}
}
