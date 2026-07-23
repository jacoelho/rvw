package source

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
)

func TestNextEmbeddedWorktreeComponentSkipsOnlyTheEmptyLeadingComponent(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		start         int
		wantComponent string
		wantNext      int
		wantOK        bool
	}{
		{name: "empty", start: 0, wantNext: 1},
		{name: "leading slash", path: "/a", start: 0, wantComponent: "/a", wantNext: 3, wantOK: true},
		{name: "first component", path: "a/b", start: 0, wantComponent: "a", wantNext: 2, wantOK: true},
		{name: "resumed component", path: "a/b", start: 2, wantComponent: "a/b", wantNext: 4, wantOK: true},
		{name: "negative start", path: "a/b", start: -1, wantNext: -1},
		{name: "start beyond path", path: "a/b", start: 4, wantNext: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			component, next, ok := nextEmbeddedWorktreeComponent(test.path, test.start)
			if component != test.wantComponent || next != test.wantNext || ok != test.wantOK {
				t.Fatalf(
					"component = (%q, %d, %t), want (%q, %d, %t)",
					component, next, ok,
					test.wantComponent, test.wantNext, test.wantOK,
				)
			}
		})
	}
}

func TestEmbeddedWorktreeDiscoveryReleasesProbesAndAdmitsResults(t *testing.T) {
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	admitted := false
	admissions := 0
	runCPU := func(ctx context.Context, work func() error) error {
		if admitted {
			t.Fatal("nested worktree CPU admission")
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		admitted = true
		admissions++
		err := work()
		admitted = false
		return err
	}
	probed := make([]string, 0)
	roots, err := discoverEmbeddedWorktreeRepositories(
		t.Context(),
		[]string{"a/b/file", "a/c", "repo/child"},
		ledger,
		runCPU,
		func(_ context.Context, path string) (embeddedWorktreeState, error) {
			if admitted {
				t.Fatal("filesystem or Git probe ran under CPU admission")
			}
			probed = append(probed, path)
			switch path {
			case "a", "a/b":
				return embeddedWorktreeAbsent, nil
			case "repo":
				return embeddedWorktreeRepository, nil
			default:
				return embeddedWorktreeTerminal, nil
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(roots, []string{"repo"}) {
		t.Fatalf("embedded roots = %q", roots)
	}
	if !slices.Equal(probed, []string{"a", "repo", "a/b", "a/c", "a/b/file"}) {
		t.Fatalf("released probes = %q", probed)
	}
	if ledger.used != int64(len(probed))*embeddedWorktreeStateBytes {
		t.Fatalf("embedded cache ledger = %d", ledger.used)
	}
	if admissions < 3 {
		t.Fatalf("embedded discovery used only %d admissions", admissions)
	}
}

func TestEmbeddedWorktreeDiscoveryWindowsBoundRecordsAndPathBytes(t *testing.T) {
	paths := make([]string, 3_000)
	for index := range paths {
		paths[index] = "directory-" + strconv.Itoa(index)
	}
	discovery := embeddedWorktreeDiscovery{
		checked: make(map[string]embeddedWorktreeState),
		paths:   paths,
	}
	if err := discovery.loadWindow(t.Context()); err != nil {
		t.Fatal(err)
	}
	pathBytes := 0
	for _, cursor := range discovery.cursors {
		pathBytes += len(cursor.path)
	}
	if len(discovery.cursors) > embeddedWorktreeQuantumRecords ||
		pathBytes > embeddedWorktreeQuantumBytes {
		t.Fatalf("window = %d records, %d bytes", len(discovery.cursors), pathBytes)
	}
	if cap(discovery.cursors) != len(discovery.cursors) {
		t.Fatalf("window retained capacity = %d records, want %d", cap(discovery.cursors), len(discovery.cursors))
	}
	if discovery.nextPath == 0 || discovery.nextPath == len(paths) {
		t.Fatalf("window progress = %d of %d paths", discovery.nextPath, len(paths))
	}
	requests, err := discovery.prepareProbes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	requestBytes := 0
	for _, request := range requests {
		requestBytes += len(request.path)
	}
	if len(requests) > embeddedWorktreeQuantumRecords || requestBytes > embeddedWorktreeQuantumBytes {
		t.Fatalf("probe batch = %d records, %d bytes", len(requests), requestBytes)
	}
}

func TestEmbeddedWorktreeProbeInstallRejectsOneByteOverQuantum(t *testing.T) {
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	discovery := embeddedWorktreeDiscovery{checked: make(map[string]embeddedWorktreeState)}
	results := []embeddedWorktreeProbeRequest{
		{path: string(make([]byte, embeddedWorktreeQuantumBytes)), state: embeddedWorktreeAbsent},
		{path: "x", state: embeddedWorktreeAbsent},
	}
	if err := discovery.installProbeResults(t.Context(), ledger, results); err == nil {
		t.Fatal("one-byte-over embedded result batch was accepted")
	}
	if ledger.used != 0 || len(discovery.checked) != 0 {
		t.Fatalf("rejected result batch mutated cache: used=%d checked=%d", ledger.used, len(discovery.checked))
	}
}

func TestSanitizedEmbeddedWorktreeEnvironmentIsIndependent(t *testing.T) {
	original := map[string]string{
		"GIT_DIR":                          "dir",
		"GIT_WORK_TREE":                    "tree",
		"GIT_OBJECT_DIRECTORY":             "objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": "alternate",
		"GIT_INDEX_FILE":                   "index",
		"LC_ALL":                           "C",
	}
	sanitized := sanitizedEmbeddedWorktreeEnvironment(original)
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_INDEX_FILE",
	} {
		if _, exists := sanitized[key]; exists {
			t.Fatalf("sanitized environment retained %s", key)
		}
		if _, exists := original[key]; !exists {
			t.Fatalf("sanitization mutated original environment at %s", key)
		}
	}
	if sanitized["LC_ALL"] != "C" {
		t.Fatalf("sanitized environment = %v", sanitized)
	}
}

func TestEmbeddedWorktreeDiscoveryPreservesCancellationCauseBeforeProbe(t *testing.T) {
	cause := errors.New("cancel embedded discovery")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = discoverEmbeddedWorktreeRepositories(
		ctx,
		[]string{"path"},
		ledger,
		directCPUAdmission,
		func(context.Context, string) (embeddedWorktreeState, error) {
			t.Fatal("cancelled discovery performed a probe")
			return 0, nil
		},
	)
	if !errors.Is(err, cause) {
		t.Fatalf("embedded cancellation error = %v", err)
	}
}
