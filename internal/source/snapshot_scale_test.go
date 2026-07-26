package source

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func TestSnapshotAcceptsMaximumFilesAndPathOwnership(t *testing.T) {
	records := make([]diffRecord, change.MaxFiles)
	basePathBytes := int(change.MaxSourcePaths) / len(records)
	extraPathBytes := int(change.MaxSourcePaths) % len(records)
	mode := change.FileMode("100644")
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
		int64(len(records))*4 + change.SnapshotBuilderConstructionBytes(len(records))
	if peak != wantBuilderPeak || peak > maxSourceConstructionBytes {
		t.Fatalf("builder peak = %d, want %d under %d", peak, wantBuilderPeak, maxSourceConstructionBytes)
	}
	snapshot, err := (&Service{}).snapshot(
		t.Context(),
		directCPUAdmission,
		change.SourceID(1),
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
	if wantRetained != change.MaxBaseSnapshotRetainedBytes ||
		change.MaxBaseSnapshotRetainedBytes > change.MaxSnapshotRetainedBytes {
		t.Fatalf(
			"snapshot bounds = retained %d base %d complete %d",
			wantRetained,
			change.MaxBaseSnapshotRetainedBytes,
			change.MaxSnapshotRetainedBytes,
		)
	}
}
