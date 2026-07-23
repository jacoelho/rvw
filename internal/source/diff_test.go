package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseRawAndNumstat(t *testing.T) {
	const oidA = "1111111111111111111111111111111111111111"
	const oidB = "2222222222222222222222222222222222222222"
	raw := []byte(
		":000000 100644 0000000000000000000000000000000000000000 " + oidA + " A\x00added.go\x00" +
			":100644 100644 " + oidA + " " + oidB + " R100\x00old.go\x00new.go\x00",
	)
	records, err := parseRawDiff(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := mergeNumstatStream(
		strings.NewReader("3\t0\tadded.go\x000\t0\t\x00old.go\x00new.go\x00"),
		records,
	); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d", len(records))
	}
	change, err := changeFromDiff(records[0])
	if err != nil {
		t.Fatal(err)
	}
	if change.Kind() != "added" || change.Additions() != 3 {
		t.Fatalf("unexpected change: %+v", change)
	}
}

func TestParseRawDiffRejectsMalformedStatusSuffixes(t *testing.T) {
	const oid = "1111111111111111111111111111111111111111"
	for _, status := range []string{"Mgarbage", "A100", "Rxyz", "R101", "C-1"} {
		t.Run(status, func(t *testing.T) {
			raw := []byte(
				":100644 100644 " + oid + " " + oid + " " + status +
					"\x00old.go\x00new.go\x00",
			)
			if _, err := parseRawDiff(raw); err == nil {
				t.Fatalf("status %q unexpectedly accepted", status)
			}
		})
	}
}

func TestDiffMetadataStreamsStopOnCancellation(t *testing.T) {
	const oid = "1111111111111111111111111111111111111111"
	raw := ":100644 100644 " + oid + " " + oid + " M\x00file.go\x00"
	cause := errors.New("stop metadata parsing")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if _, err := parseRawDiffStreamContext(ctx, strings.NewReader(raw)); !errors.Is(err, cause) {
		t.Fatalf("raw cancellation error = %v", err)
	}
	if err := mergeNumstatStreamContext(
		ctx,
		strings.NewReader("1\t1\tfile.go\x00"),
		[]diffRecord{{oldPath: "file.go", newPath: "file.go", status: 'M'}},
	); !errors.Is(err, cause) {
		t.Fatalf("numstat cancellation error = %v", err)
	}
	if err := enforceRenameCandidateLimitStreamContext(
		ctx,
		strings.NewReader(raw),
	); !errors.Is(err, cause) {
		t.Fatalf("rename preflight cancellation error = %v", err)
	}
}

func TestRenameCandidatePreflightRejectsOneOverLimit(t *testing.T) {
	const oid = "1111111111111111111111111111111111111111"
	var raw strings.Builder
	for index := range 1_001 {
		fmt.Fprintf(
			&raw,
			":000000 100644 0000000000000000000000000000000000000000 %s A\x00file-%04d.go\x00",
			oid,
			index,
		)
	}
	var limited *LimitError
	err := enforceRenameCandidateLimitStream(strings.NewReader(raw.String()))
	if !errors.As(err, &limited) || limited.Resource != LimitRenameCandidates {
		t.Fatalf("rename candidate limit error = %v", err)
	}
}
