package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/subprocess"
)

func TestTargetValidation(t *testing.T) {
	rangeTarget, err := NewRangeTarget("main..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base, head, ok := rangeTarget.Range()
	if !ok || base.String() != "main" || head.String() != "HEAD" {
		t.Fatalf("unexpected range: %q %q %v", base, head, ok)
	}

	repository, err := NewOwnerRepository("owner/repo.name")
	if err != nil {
		t.Fatal(err)
	}
	selector, err := NewPullRequestSelector(
		"https://github.com/owner/repo.name/pull/18446744073709551615",
		&repository,
	)
	if err != nil {
		t.Fatal(err)
	}
	if selector.Number().Uint64() != ^uint64(0) {
		t.Fatalf("number = %d", selector.Number().Uint64())
	}
}

func TestServiceSerializesRepositoryDiscoveryAndSourceIdentity(t *testing.T) {
	root := initializedRepository(t)
	service := newTestService(t, root)
	defer closeTestService(t, service)

	const calls = 32
	identities := make(chan review.SourceID, calls)
	errs := make(chan error, calls*2)
	var workers sync.WaitGroup
	for range calls {
		workers.Go(func() {
			if _, err := service.RepositoryRoot(t.Context()); err != nil {
				errs <- err
				return
			}
			identity, err := service.newSourceID()
			if err != nil {
				errs <- err
				return
			}
			identities <- identity
		})
	}
	workers.Wait()
	close(errs)
	close(identities)
	for err := range errs {
		t.Error(err)
	}
	seen := make(map[review.SourceID]bool, calls)
	for identity := range identities {
		if seen[identity] {
			t.Fatalf("duplicate source identity %d", identity)
		}
		seen[identity] = true
	}
	if len(seen) != calls {
		t.Fatalf("source identities = %d, want %d", len(seen), calls)
	}
}

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
	numstat, err := parseNumstat([]byte("3\t0\tadded.go\x000\t0\t\x00old.go\x00new.go\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(numstat) != 2 {
		t.Fatalf("record counts = %d %d", len(records), len(numstat))
	}
	if !matchingNumstatPath(records[0], numstat[0]) || !matchingNumstatPath(records[1], numstat[1]) {
		t.Fatal("raw and numstat paths did not match")
	}
	records[0].additions = numstat[0].additions
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

func TestOpenLocalSources(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\n")
	runGitTest(t, root, "add", "tracked.txt")
	runGitTest(t, root, "commit", "-m", "first")
	first := strings.TrimSpace(runGitTest(t, root, "rev-parse", "HEAD"))

	writeFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\n")
	writeFile(t, filepath.Join(root, "untracked.txt"), "new\n")

	service := newTestService(t, root)
	defer closeTestService(t, service)
	options := OpenOptions{IncludeUntracked: true}
	worktree, err := service.Open(t.Context(), WorktreeTarget(), options)
	if err != nil {
		t.Fatal(err)
	}
	if worktree.Snapshot().FileCount() != 2 {
		t.Fatalf("worktree file count = %d", worktree.Snapshot().FileCount())
	}
	if err := worktree.Close(); err != nil {
		t.Fatal(err)
	}

	runGitTest(t, root, "add", "tracked.txt")
	staged, err := service.Open(t.Context(), StagedTarget(), options)
	if err != nil {
		t.Fatal(err)
	}
	stagedFile, _ := staged.Snapshot().File(0)
	if staged.Snapshot().FileCount() != 1 || stagedFile.DisplayPath() != "tracked.txt" {
		t.Fatalf("unexpected staged snapshot: %+v", staged.Snapshot())
	}
	if err := staged.Close(); err != nil {
		t.Fatal(err)
	}

	runGitTest(t, root, "commit", "-m", "second")
	second := strings.TrimSpace(runGitTest(t, root, "rev-parse", "HEAD"))
	target, err := NewRangeTarget(first + ".." + second)
	if err != nil {
		t.Fatal(err)
	}
	ranged, err := service.Open(t.Context(), target, options)
	if err != nil {
		t.Fatal(err)
	}
	rangedFile, _ := ranged.Snapshot().File(0)
	if ranged.Snapshot().FileCount() != 1 || rangedFile.DisplayPath() != "tracked.txt" {
		t.Fatalf("unexpected range snapshot: %+v", ranged.Snapshot())
	}
	if err := ranged.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeSnapshotRejectsFilterDriversWithoutExecutingThem(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "tracked.txt"), "base\n")
	runGitTest(t, root, "add", "tracked.txt")
	runGitTest(t, root, "commit", "-m", "base")
	writeFile(t, filepath.Join(root, "tracked.txt"), "worktree\n")
	writeFile(t, filepath.Join(root, ".gitattributes"), "*.txt filter=evil\n")

	marker := filepath.Join(root, "filter-ran")
	driver := filepath.Join(root, "filter-driver")
	writeFile(
		t,
		driver,
		"#!/bin/sh\nprintf executed > "+strconv.Quote(marker)+"\nexit 1\n",
	)
	if err := os.Chmod(driver, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "config", "filter.evil.clean", driver)
	runGitTest(t, root, "config", "filter.evil.process", driver)

	service := newTestService(t, root)
	defer closeTestService(t, service)
	if _, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{}); err == nil ||
		!strings.Contains(err.Error(), "clean/process filters") {
		t.Fatalf("filtered worktree error = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configured filter driver ran: %v", err)
	}
}

func TestTrackedWorktreeUsesLiveIndexMembership(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "tracked.txt"), "base\n")
	writeFile(t, filepath.Join(root, "removed-from-index.txt"), "base\n")
	runGitTest(t, root, "add", ".")
	runGitTest(t, root, "commit", "-m", "base")

	writeFile(t, filepath.Join(root, "tracked.txt"), "worktree\n")
	runGitTest(t, root, "rm", "--cached", "--quiet", "removed-from-index.txt")
	writeFile(t, filepath.Join(root, "staged.txt"), "staged\n")
	runGitTest(t, root, "add", "staged.txt")
	writeFile(t, filepath.Join(root, "untracked.txt"), "untracked\n")

	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()

	got := make(map[string]review.ChangeKind)
	for file := range document.Snapshot().AllFiles() {
		got[file.DisplayPath()] = file.Change().Kind()
	}
	want := map[string]review.ChangeKind{
		"tracked.txt":            review.ChangeModified,
		"removed-from-index.txt": review.ChangeDeleted,
		"staged.txt":             review.ChangeAdded,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("tracked-only worktree changes = %v, want %v", got, want)
	}
}

func TestWorktreeRejectsIndexFlagsThatHideTrackedChanges(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "tracked.txt"), "base\n")
	runGitTest(t, root, "add", "tracked.txt")
	runGitTest(t, root, "commit", "-m", "base")
	runGitTest(t, root, "update-index", "--assume-unchanged", "tracked.txt")
	writeFile(t, filepath.Join(root, "tracked.txt"), "hidden change\n")

	service := newTestService(t, root)
	defer closeTestService(t, service)
	if _, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{}); err == nil ||
		!strings.Contains(err.Error(), "assume-unchanged") {
		t.Fatalf("hidden worktree change error = %v", err)
	}
}

func TestWorktreeSnapshotsInitializedGitlinks(t *testing.T) {
	child := initializedRepository(t)
	writeFile(t, filepath.Join(child, "child.txt"), "base\n")
	runGitTest(t, child, "add", "child.txt")
	runGitTest(t, child, "commit", "-m", "child")

	root := initializedRepository(t)
	runGitTest(
		t,
		root,
		"-c", "protocol.file.allow=always",
		"submodule", "add", "--quiet", child, "module",
	)
	runGitTest(t, root, "commit", "-m", "gitlink")

	service := newTestService(t, root)
	defer closeTestService(t, service)
	unchanged, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Snapshot().FileCount() != 0 {
		t.Fatalf("unchanged submodule produced %d changes", unchanged.Snapshot().FileCount())
	}
	if err := unchanged.Close(); err != nil {
		t.Fatal(err)
	}

	module := filepath.Join(root, "module")
	runGitTest(t, module, "config", "user.email", "rvw@example.invalid")
	runGitTest(t, module, "config", "user.name", "rvw")
	writeFile(t, filepath.Join(module, "child.txt"), "advanced\n")
	runGitTest(t, module, "add", "child.txt")
	runGitTest(t, module, "commit", "-m", "advance")

	advanced, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer advanced.Close()
	file, ok := advanced.Snapshot().File(0)
	if !ok || advanced.Snapshot().FileCount() != 1 ||
		file.DisplayPath() != "module" ||
		file.Change().Kind() != review.ChangeSubmodule {
		t.Fatalf("advanced submodule snapshot = %+v, present=%v", file, ok)
	}
}

func TestOpenStagedPreservesUnmergedIndexAsUnavailablePatch(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "conflict.txt"), "base\n")
	runGitTest(t, root, "add", "conflict.txt")
	runGitTest(t, root, "commit", "-m", "base")
	mainBranch := strings.TrimSpace(runGitTest(t, root, "branch", "--show-current"))

	runGitTest(t, root, "checkout", "-b", "side")
	writeFile(t, filepath.Join(root, "conflict.txt"), "side\n")
	runGitTest(t, root, "commit", "-am", "side")
	runGitTest(t, root, "checkout", mainBranch)
	writeFile(t, filepath.Join(root, "conflict.txt"), "main\n")
	runGitTest(t, root, "commit", "-am", "main")

	command := exec.Command("git", "merge", "--no-ff", "--no-edit", "side")
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded:\n%s", output)
	}

	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, err := service.Open(t.Context(), StagedTarget(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	file, _ := document.Snapshot().File(0)
	if document.Snapshot().FileCount() != 1 || file.DisplayPath() != "conflict.txt" ||
		file.Change().Kind() != review.ChangeModified {
		t.Fatalf("unexpected staged conflict snapshot: %+v", document.Snapshot())
	}

	current, err := review.NewReview(document.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	contextLines, _ := review.NewContextLines(3)
	patchContext, _ := review.ContextAround(contextLines)
	current, request, err := current.RequestPatch(file.ID(), patchContext)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.LoadPatch(t.Context(), document, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != review.PatchUnavailable ||
		!strings.Contains(result.Detail.String(), "unmerged index") {
		t.Fatalf("unmerged patch result = %+v", result)
	}
	if current, err = current.ApplyPatch(result); err != nil {
		t.Fatal(err)
	}
	if current.Patch(file.ID()).Status != review.PatchUnavailable {
		t.Fatal("unavailable patch state was not retained")
	}
}

func TestLoadPatchAndReloadUseGenerationFencedIdentities(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\n")
	runGitTest(t, root, "add", "tracked.txt")
	runGitTest(t, root, "commit", "-m", "first")
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\n")

	service := newTestService(t, root)
	defer closeTestService(t, service)
	options := OpenOptions{IncludeUntracked: true}
	document, err := service.Open(t.Context(), WorktreeTarget(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()

	current, err := review.NewReview(document.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	contextLines, _ := review.NewContextLines(3)
	patchContext, _ := review.ContextAround(contextLines)
	current, request, err := current.RequestPatch(0, patchContext)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.LoadPatch(t.Context(), document, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != review.PatchReady || result.Prepared.Hunks() == 0 {
		t.Fatalf("unexpected patch result: %+v", result)
	}
	current, err = current.ApplyPatch(result)
	if err != nil {
		t.Fatal(err)
	}
	if lines := patchLinesText(current.PatchLines(0, 0, 100, 4_096)); !strings.Contains(lines, "+two") {
		t.Fatalf("unexpected patch text:\n%s", lines)
	}

	reloaded, err := service.Reload(t.Context(), document, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if reloaded.Snapshot().SourceID() == document.Snapshot().SourceID() {
		t.Fatal("reload reused source identity")
	}
	if _, err := service.LoadPatch(t.Context(), reloaded, request); err == nil {
		t.Fatal("stale patch request was accepted by reloaded source")
	}
}

func TestLoadPatchTreatsRawPathAsLiteral(t *testing.T) {
	root := initializedRepository(t)
	hostileName := ":(glob)**"
	otherName := "other.txt"
	writeFile(t, filepath.Join(root, hostileName), "original\n")
	writeFile(t, filepath.Join(root, otherName), "original\n")
	runGitTest(t, root, "add", ".")
	runGitTest(t, root, "commit", "-m", "initial")
	writeFile(t, filepath.Join(root, hostileName), "hostile-only\n")
	writeFile(t, filepath.Join(root, otherName), "other-only\n")

	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	current, err := review.NewReview(document.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var hostileID review.FileID
	found := false
	for file := range document.Snapshot().AllFiles() {
		if file.DisplayPath() == hostileName {
			hostileID = file.ID()
			found = true
			break
		}
	}
	if !found {
		t.Fatal("hostile path is missing from snapshot")
	}
	contextLines, _ := review.NewContextLines(3)
	patchContext, _ := review.ContextAround(contextLines)
	var request review.PatchRequest
	current, request, err = current.RequestPatch(hostileID, patchContext)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.LoadPatch(t.Context(), document, request)
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.ApplyPatch(result)
	if err != nil {
		t.Fatal(err)
	}
	text := patchLinesText(current.PatchLines(hostileID, 0, 100, 4_096))
	if !strings.Contains(text, "+hostile-only") ||
		strings.Contains(text, "+other-only") {
		t.Fatalf("patch pathspec was not literal:\n%s", text)
	}
}

func TestRepositoryRootIsDiscoveredOnceAndCached(t *testing.T) {
	root := initializedRepository(t)
	service := newTestService(t, root)
	defer closeTestService(t, service)

	first, err := service.RepositoryRoot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, ".git-moved")); err != nil {
		t.Fatal(err)
	}
	second, err := service.RepositoryRoot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("cached roots differ: %q %q", first, second)
	}
}

func TestSourceFailureIsTypedAtPublicBoundary(t *testing.T) {
	service := newTestService(t, t.TempDir())
	defer closeTestService(t, service)
	_, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != FailureFailed ||
		failure.Operation != OperationDiscoverRepository {
		t.Fatalf("Open() error = %v, want typed repository failure", err)
	}
}

func TestSourceFailureClassificationPreservesDominantOutcome(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind FailureKind
	}{
		{
			name: "non-graceful dominates cancellation",
			err: errors.Join(
				context.Canceled,
				&subprocess.NonGracefulError{PID: 42},
			),
			kind: FailureNonGraceful,
		},
		{
			name: "stale",
			err:  fmt.Errorf("%w: changed head", ErrSourceStale),
			kind: FailureStale,
		},
		{
			name: "bounded fetch depth",
			err:  &LimitError{Resource: LimitPullRequestDepth, Limit: 1_000},
			kind: FailureLimited,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := sourceFailure(OperationOpenSource, test.err)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != test.kind {
				t.Fatalf("failure = %#v, want kind %d", failure, test.kind)
			}
			if !errors.Is(err, test.err) {
				t.Fatalf("failure does not unwrap original error: %v", err)
			}
		})
	}
}

func TestCommandScalarPreservesLegalWhitespace(t *testing.T) {
	value, err := parseCommandScalar([]byte("/tmp/repo \r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if value != "/tmp/repo \r" {
		t.Fatalf("scalar = %q", value)
	}
	for _, invalid := range [][]byte{nil, []byte("\n"), []byte("value"), []byte("a\x00b\n")} {
		if _, err := parseCommandScalar(invalid); err == nil {
			t.Fatalf("parseCommandScalar(%q) succeeded", invalid)
		}
	}
}

func TestIgnoreFileCannotFollowSymlinkOutsideRepository(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	writeFile(t, outside, "*.secret\n")
	if err := os.Symlink(outside, filepath.Join(root, ".rvwignore")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	repository, _ := NewRepositoryPath(root)
	name, _ := NewIgnoreFile(".rvwignore")
	if _, err := readIgnoreRules(repository, name); err == nil {
		t.Fatal("outside-repository ignore-file symlink was followed")
	}
}

func TestOpenUnbornWorktree(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, "new.txt"), "new\n")
	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{IncludeUntracked: true})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	file, _ := document.Snapshot().File(0)
	if document.Snapshot().FileCount() != 1 || file.DisplayPath() != "new.txt" {
		t.Fatalf("unexpected snapshot: %+v", document.Snapshot())
	}
}

func TestFiltersHideWithoutRemovingMembership(t *testing.T) {
	root := initializedRepository(t)
	writeFile(t, filepath.Join(root, ".gitignore"), "ignored.txt\n")
	writeFile(t, filepath.Join(root, "visible.go"), "package visible\n")
	writeFile(t, filepath.Join(root, "generated.pb.go"), "package generated\n")
	writeFile(t, filepath.Join(root, "ignored.txt"), "ignored\n")
	service := newTestService(t, root)
	defer closeTestService(t, service)
	document, err := service.Open(t.Context(), WorktreeTarget(), OpenOptions{
		IncludeUntracked: true,
		BuiltinDefaults:  true,
		Gitignore:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	if document.Snapshot().FileCount() != 4 {
		t.Fatalf("file count = %d", document.Snapshot().FileCount())
	}
	hidden := make(map[string][]string)
	for file := range document.Snapshot().AllFiles() {
		for _, reason := range file.Flags().HiddenReasons() {
			hidden[file.DisplayPath()] = append(hidden[file.DisplayPath()], string(reason))
		}
	}
	if !slices.Contains(hidden["generated.pb.go"], "builtin-default") {
		t.Fatalf("generated flags = %v", hidden["generated.pb.go"])
	}
	if !slices.Contains(hidden["ignored.txt"], "gitignore") {
		t.Fatalf("ignored flags = %v", hidden["ignored.txt"])
	}
}

func TestValidatePullRequestPayload(t *testing.T) {
	repository, _ := NewOwnerRepository("owner/repo")
	number := PullRequestNumber(17)
	payload := pullRequestPayload{
		Number:       17,
		Title:        "Title",
		HTMLURL:      "https://github.com/owner/repo/pull/17",
		State:        "open",
		ChangedFiles: 3_000,
	}
	payload.Base.Ref = "main"
	payload.Base.SHA = strings.Repeat("1", 40)
	payload.Head.Ref = "topic"
	payload.Head.SHA = strings.Repeat("2", 40)
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) == 0 {
		t.Fatal(err)
	}
	details, err := validatePullRequestPayload(repository, number, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !details.FilesAPICapReached || details.State != GitHubOpen {
		t.Fatalf("unexpected details: %+v", details)
	}
	for _, value := range []string{"", "not-a-time"} {
		payload.MergedAt = &value
		if _, err := validatePullRequestPayload(repository, number, payload); err == nil {
			t.Fatalf("merge timestamp %q unexpectedly accepted", value)
		}
	}
	mergedAt := "2026-07-20T12:30:00Z"
	payload.MergedAt = &mergedAt
	payload.State = "closed"
	details, err = validatePullRequestPayload(repository, number, payload)
	if err != nil || details.State != GitHubMerged {
		t.Fatalf("valid merged payload = %+v, %v", details, err)
	}
}

func initializedRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGitTest(t, root, "init")
	runGitTest(t, root, "config", "user.email", "rvw@example.test")
	runGitTest(t, root, "config", "user.name", "rvw test")
	return root
}

func newTestService(t *testing.T, root string) *Service {
	t.Helper()
	workingDirectory, err := NewWorkingDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func closeTestService(t *testing.T, service *Service) {
	t.Helper()
	if err := service.Close(context.Background()); err != nil {
		t.Error(err)
	}
}

func runGitTest(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func patchLinesText(lines []review.PatchLine) string {
	values := make([]string, len(lines))
	for index, line := range lines {
		values[index] = line.Text.String()
	}
	return strings.Join(values, "\n")
}
