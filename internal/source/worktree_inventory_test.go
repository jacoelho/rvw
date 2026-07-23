package source

import (
	"errors"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestParseUntrackedWorktreeRecordsPreservesRecordKind(t *testing.T) {
	records, err := parseUntrackedWorktreeRecords(
		[]byte("ordinary\x00nested/\x00"),
		make(map[string]struct{}),
		review.MaxFiles,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].path != "ordinary" ||
		records[0].kind != untrackedWorktreeOrdinary ||
		records[1].path != "nested" || records[1].kind != untrackedWorktreeGitlink {
		t.Fatalf("untracked records = %+v", records)
	}
}

func TestParseUntrackedWorktreeRecordsRejectsAmbiguousOrMalformedInventory(t *testing.T) {
	for name, inventory := range map[string][]byte{
		"missing terminator":       []byte("path"),
		"duplicate ordinary":       []byte("path\x00path\x00"),
		"duplicate gitlink":        []byte("path/\x00path/\x00"),
		"kind collision":           []byte("path\x00path/\x00"),
		"root marker":              []byte("/\x00"),
		"repeated trailing slash":  []byte("path//\x00"),
		"empty interior component": []byte("a//b\x00"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseUntrackedWorktreeRecords(
				inventory,
				make(map[string]struct{}),
				review.MaxFiles,
			); err == nil {
				t.Fatal("invalid untracked inventory was accepted")
			}
		})
	}
}

func TestParseUntrackedWorktreeRecordsEnforcesLimitBeforeRetention(t *testing.T) {
	if _, err := parseUntrackedWorktreeRecords(
		[]byte("a\x00b\x00"),
		make(map[string]struct{}),
		1,
	); err == nil {
		t.Fatal("inventory above the record limit was accepted")
	}
}

func TestParseUntrackedWorktreeRecordsSharesAggregatePathBudget(t *testing.T) {
	existing := map[string]struct{}{"changed": {}}
	records, err := parseUntrackedWorktreeRecords(
		[]byte("changed\x00new\x00"),
		existing,
		2,
	)
	if err != nil || len(records) != 1 || records[0].path != "new" || len(existing) != 2 {
		t.Fatalf("exact aggregate budget = %+v, existing=%v, err=%v", records, existing, err)
	}
	if _, err := parseUntrackedWorktreeRecords([]byte("over\x00"), existing, 2); err == nil {
		t.Fatal("aggregate path budget plus one was accepted")
	}
}

func TestWorktreeFilterOutputLimitCoversExactAcceptedRecords(t *testing.T) {
	limit, err := worktreeFilterOutputLimit(10, 5)
	if err != nil {
		t.Fatal(err)
	}
	if limit != 105 {
		t.Fatalf("five-path output limit = %d, want 105", limit)
	}
	limit, err = worktreeFilterOutputLimit(17, 2)
	if err != nil || limit != 55 {
		t.Fatalf("varied output limit = %d, %v, want 55", limit, err)
	}
	maxInt := int(^uint(0) >> 1)
	if _, err := worktreeFilterOutputLimit(maxInt, 1); err == nil {
		t.Fatal("overflowing filter output limit was accepted")
	}
}

func TestDigestAndValidateStageInventory(t *testing.T) {
	valid := "100644 " + strings.Repeat("a", 40) + " 0\tpath.go\x00"
	if _, err := digestAndValidateStageInventory(strings.NewReader(valid)); err != nil {
		t.Fatalf("valid stage inventory rejected: %v", err)
	}
	for _, value := range []string{
		"100644 bad 0\tpath.go\x00",
		"100644 " + strings.Repeat("a", 40) + " 4\tpath.go\x00",
		"100644 " + strings.Repeat("a", 40) + " 0 path.go\x00",
		"100644 " + strings.Repeat("a", 40) + " 0\t../path.go\x00",
	} {
		if _, err := digestAndValidateStageInventory(strings.NewReader(value)); err == nil {
			t.Fatalf("invalid stage inventory accepted: %q", value)
		}
	}
}

func TestValidateFilterAttributeOutputRequiresExactAcceptedSequence(t *testing.T) {
	paths := []string{"a", "b"}
	valid := []byte("a\x00filter\x00unspecified\x00b\x00filter\x00unset\x00")
	names, err := validateFilterAttributeOutput(valid, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !names.has(worktreeFilterUnspecified) || !names.has(worktreeFilterUnset) {
		t.Fatalf("ambiguous filter names = %08b", names.bits)
	}
	for name, output := range map[string][]byte{
		"missing path":         nil,
		"missing attribute":    []byte("a\x00"),
		"missing value":        []byte("a\x00filter\x00"),
		"mismatched path":      []byte("b\x00filter\x00unspecified\x00b\x00filter\x00unset\x00"),
		"mismatched attribute": []byte("a\x00diff\x00unspecified\x00b\x00filter\x00unset\x00"),
		"configured value":     []byte("a\x00filter\x00driver\x00b\x00filter\x00unset\x00"),
		"extra record":         append(append([]byte{}, valid...), []byte("c\x00filter\x00unset\x00")...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateFilterAttributeOutput(output, paths); err == nil {
				t.Fatal("invalid filter attribute sequence was accepted")
			}
		})
	}
}

func TestValidateExplicitAmbiguousFilterAttributesIgnoresUnconfiguredSentinelName(t *testing.T) {
	configured := worktreeFilterNames{}
	configured.add(worktreeFilterUnspecified)
	if err := validateExplicitAmbiguousFilterAttributes(
		[]byte("unset.txt\x00filter\x00unset\x00"),
		[]string{"plain.txt", "unset.txt"},
		configured,
	); err != nil {
		t.Fatalf("safe mixed sentinel states were rejected: %v", err)
	}
}

func TestWorktreeInventoriesAndChangesShareConstructionLedger(t *testing.T) {
	const untrackedBytes = int64(113) // one-byte path plus retained record/map charge
	const firstRawCapacityBytes = int64(16) * diffRecordConstructionCharge
	const streamScratchBytes = sourceStreamBufferBytes + maxSourceProtocolToken + 1
	ledger, err := newSourceLedgerWithLimit(
		streamScratchBytes+untrackedBytes+firstRawCapacityBytes-1,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, pathBytes, _, err := parseAndDigestUntrackedInventoryWithLedger(
		t.Context(),
		strings.NewReader("u\x00"),
		ledger,
	)
	if err != nil {
		t.Fatal(err)
	}
	if pathBytes != 1 {
		t.Fatalf("aggregate untracked path bytes = %d, want 1", pathBytes)
	}
	const oid = "1111111111111111111111111111111111111111"
	raw := ":100644 100644 " + oid + " " + oid + " M\x00t\x00"
	if _, err := parseRawDiffStreamWithLedgerCPU(
		t.Context(),
		strings.NewReader(raw),
		ledger,
		directCPUAdmission,
	); !errors.Is(err, errSourceConstructionLimited) {
		t.Fatalf("combined construction error = %v", err)
	}
}
