package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestDecodeRemoteThreadPagesRequiresExactUTF8SlurpedEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value []byte
		valid bool
	}{
		{name: "valid", value: []byte(`[{"data":{},"errors":[]}]`), valid: true},
		{name: "empty", value: []byte(`[]`)},
		{name: "null page", value: []byte(`[null]`)},
		{name: "flat", value: []byte(`{"data":{}}`)},
		{name: "invalid utf8", value: []byte{'[', '"', 0xff, '"', ']'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			pages, err := decodeRemoteThreadPages(test.value)
			if test.valid {
				if err != nil || len(pages) != 1 {
					t.Fatalf("pages=%d error=%v", len(pages), err)
				}
				return
			}
			if err == nil {
				t.Fatalf("decodeRemoteThreadPages(%q) unexpectedly succeeded", test.value)
			}
		})
	}
}

func TestDecodeRemoteThreadPagesRejectsNonObjectAfterValidPage(t *testing.T) {
	valid := `{"data":{},"errors":[]}`
	for _, suffix := range []string{"null", "[]", "true"} {
		if _, err := decodeRemoteThreadPages([]byte("[" + valid + "," + suffix + "]")); err == nil {
			t.Fatalf("non-object page %s after a valid page was accepted", suffix)
		}
	}
}

func TestDecodeRemoteThreadPagesAllowsSparseRootBeyondNestedLimit(t *testing.T) {
	page := `{"data":{},"errors":[]}`
	encoded := "[" + strings.Repeat(page+",", 100) + page + "]"
	pages, err := decodeRemoteThreadPages([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 101 {
		t.Fatalf("pages = %d, want 101", len(pages))
	}
}

func TestRemoteThreadTargetRejectsGraphQLErrorsAndRevisionDrift(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	var page remoteThreadPage
	value := `{"data":{"repository":{"nameWithOwner":"owner/repository","pullRequest":` +
		`{"number":7,"baseRefOid":"` + details.BaseOID.String() + `","headRefOid":"` +
		details.HeadOID.String() + `","reviewThreads":{"pageInfo":{},"nodes":[]}}}}}`
	if err := json.Unmarshal([]byte(value), &page); err != nil {
		t.Fatal(err)
	}
	if err := validateRemoteThreadTarget(page, details); err != nil {
		t.Fatal(err)
	}
	page.Errors = make(githubErrors, 1)
	if err := validateRemoteThreadTarget(page, details); err == nil {
		t.Fatal("GraphQL errors unexpectedly accepted")
	}
	page.Errors = nil
	page.Data.Repository.PullRequest.HeadRefOID = strings.Repeat("3", 40)
	if err := validateRemoteThreadTarget(page, details); err == nil {
		t.Fatal("revision drift unexpectedly accepted")
	}
}

func TestLoadRemoteThreadsFetchesAllPagesOfNestedComments(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	topLevel := remoteThreadResponseForTest(
		details,
		`{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[`+
			`{"id":"thread-1","isResolved":false,"isOutdated":false,`+
			`"path":"reviewed.go","line":12,"startLine":null,"diffSide":"RIGHT",`+
			`"comments":{"pageInfo":{"hasNextPage":true,"endCursor":"comments-1"},`+
			`"nodes":[`+remoteCommentJSONForTest("comment-1", "first")+`]}}]}`,
	)
	nested := remoteCommentResponseForTest(
		details,
		"thread-1",
		`{"hasNextPage":false,"endCursor":"comments-2"}`,
		`[`+remoteCommentJSONForTest("comment-2", "second")+`]`,
	)
	service := newRemoteThreadTestService(t, topLevel, nested)

	threads, err := service.loadRemoteThreads(t.Context(), details)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 {
		t.Fatalf("threads = %d, want 1", len(threads))
	}
	thread := threads[0]
	if thread.ID() != "thread-1" || thread.CommentCount() != 2 {
		t.Fatalf("thread = %q with %d comments", thread.ID(), thread.CommentCount())
	}
	comments := thread.Comments(0, thread.CommentCount())
	if comments[0].ID() != "comment-1" || comments[1].ID() != "comment-2" {
		t.Fatalf("comment IDs = %q, %q", comments[0].ID(), comments[1].ID())
	}
}

func TestLoadRemoteThreadsDefersContinuationIOUntilAfterCPUAdmission(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	topLevel := remoteThreadResponseForTest(
		details,
		`{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[`+
			`{"id":"thread-1","isResolved":false,"isOutdated":false,`+
			`"path":"reviewed.go","line":12,"startLine":null,"diffSide":"RIGHT",`+
			`"comments":{"pageInfo":{"hasNextPage":true,"endCursor":"comments-1"},`+
			`"nodes":[`+remoteCommentJSONForTest("comment-1", "first")+`]}}]}`,
	)
	nested := remoteCommentResponseForTest(
		details,
		"thread-1",
		`{"hasNextPage":false,"endCursor":null}`,
		`[`+remoteCommentJSONForTest("comment-2", "second")+`]`,
	)
	service := newRemoteThreadTestService(t, topLevel, nested)
	marker := filepath.Join(t.TempDir(), "cpu-active")
	t.Setenv("RVW_CPU_MARKER", marker)
	active := false
	admit := func(ctx context.Context, work func() error) error {
		if active {
			return errors.New("CPU admission re-entered")
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		active = true
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			return err
		}
		defer func() {
			active = false
			_ = os.Remove(marker)
		}()
		return work()
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	threads, err := service.loadRemoteThreadsWithLedgerCPU(t.Context(), details, ledger, admit)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 {
		t.Fatalf("threads = %d, want 1", len(threads))
	}
	if threads[0].CommentCount() != 2 {
		t.Fatalf("comments = %d, want 2", threads[0].CommentCount())
	}
}

func TestLoadRemoteThreadsRejectsMissingOrNullRequiredConnectionFields(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	tests := []struct {
		name       string
		connection string
	}{
		{name: "review threads missing"},
		{name: "review threads null", connection: "null"},
		{name: "page info missing", connection: `{"nodes":[]}`},
		{name: "page info null", connection: `{"pageInfo":null,"nodes":[]}`},
		{name: "has next page missing", connection: `{"pageInfo":{},"nodes":[]}`},
		{
			name:       "has next page null",
			connection: `{"pageInfo":{"hasNextPage":null},"nodes":[]}`,
		},
		{
			name:       "nodes missing",
			connection: `{"pageInfo":{"hasNextPage":false}}`,
		},
		{
			name:       "nodes null",
			connection: `{"pageInfo":{"hasNextPage":false},"nodes":null}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			topLevel := remoteThreadResponseForTest(details, test.connection)
			service := newRemoteThreadTestService(t, topLevel, "")

			if _, err := service.loadRemoteThreads(t.Context(), details); err == nil {
				t.Fatal("incomplete review-thread connection unexpectedly accepted")
			}
		})
	}
}

func TestRemoteCommentTargetRejectsMissingOrNullRequiredEnvelopeFields(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	pullRequest := fmt.Sprintf(
		`{"number":%d,"baseRefOid":%q,"headRefOid":%q}`,
		details.Number.Uint64(),
		details.BaseOID.String(),
		details.HeadOID.String(),
	)
	repository := fmt.Sprintf(
		`"repository":{"nameWithOwner":%q,"pullRequest":%s}`,
		details.Repository.String(),
		pullRequest,
	)
	validPageInfo := `{"hasNextPage":false,"endCursor":null}`
	nodeWithComments := func(comments string) string {
		return `"node":{"id":"thread-1","comments":` + comments + `}`
	}
	validComments := `{"pageInfo":` + validPageInfo + `,"nodes":[]}`
	validNode := nodeWithComments(validComments)
	tests := []struct {
		name       string
		dataFields string
	}{
		{name: "repository missing", dataFields: validNode},
		{name: "repository null", dataFields: `"repository":null,` + validNode},
		{
			name: "pull request missing",
			dataFields: fmt.Sprintf(
				`"repository":{"nameWithOwner":%q},%s`,
				details.Repository.String(),
				validNode,
			),
		},
		{
			name: "pull request null",
			dataFields: fmt.Sprintf(
				`"repository":{"nameWithOwner":%q,"pullRequest":null},%s`,
				details.Repository.String(),
				validNode,
			),
		},
		{name: "node missing", dataFields: repository},
		{name: "node null", dataFields: repository + `,"node":null`},
		{
			name:       "comments missing",
			dataFields: repository + `,"node":{"id":"thread-1"}`,
		},
		{
			name:       "comments null",
			dataFields: repository + `,` + nodeWithComments("null"),
		},
		{
			name: "page info missing",
			dataFields: repository + `,` +
				nodeWithComments(`{"nodes":[]}`),
		},
		{
			name: "page info null",
			dataFields: repository + `,` +
				nodeWithComments(`{"pageInfo":null,"nodes":[]}`),
		},
		{
			name: "has next page missing",
			dataFields: repository + `,` +
				nodeWithComments(`{"pageInfo":{},"nodes":[]}`),
		},
		{
			name: "has next page null",
			dataFields: repository + `,` +
				nodeWithComments(`{"pageInfo":{"hasNextPage":null},"nodes":[]}`),
		},
		{
			name: "nodes missing",
			dataFields: repository + `,` +
				nodeWithComments(`{"pageInfo":`+validPageInfo+`}`),
		},
		{
			name: "nodes null",
			dataFields: repository + `,` +
				nodeWithComments(`{"pageInfo":`+validPageInfo+`,"nodes":null}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := `{"errors":[],"data":{` + test.dataFields + `}}`
			var page remoteCommentPage
			if err := json.Unmarshal([]byte(value), &page); err != nil {
				t.Fatal(err)
			}
			if err := validateRemoteCommentTarget(page, details, "thread-1"); err == nil {
				t.Fatal("incomplete nested-comment envelope unexpectedly accepted")
			}
		})
	}
}

func TestLoadRemainingRemoteCommentsRejectsRepeatedCursor(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	nested := `[` +
		remoteCommentResponsePageForTest(
			details,
			"thread-1",
			`{"hasNextPage":true,"endCursor":"comments-2"}`,
			`[`+remoteCommentJSONForTest("comment-2", "second")+`]`,
		) + `,` +
		remoteCommentResponsePageForTest(
			details,
			"thread-1",
			`{"hasNextPage":false,"endCursor":"comments-2"}`,
			`[`+remoteCommentJSONForTest("comment-3", "third")+`]`,
		) +
		`]`
	service := newRemoteThreadTestService(t, "", nested)
	hasNextPage := true
	endCursor := "comments-1"
	initialComments := remoteCommentNodes{}
	initial := &remoteCommentConnection{
		PageInfo: &remotePageInfo{
			HasNextPage: &hasNextPage,
			EndCursor:   &endCursor,
		},
		Nodes: &initialComments,
	}

	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.loadRemainingRemoteComments(
		t.Context(),
		details,
		"thread-1",
		initial,
		review.MaxRemoteThreadBytes,
		ledger,
		directCPUAdmission,
	); err == nil || !strings.Contains(err.Error(), "cursor repeated") {
		t.Fatalf("repeated cursor error = %v", err)
	}
}

func TestRemoteCommentPagesShareOneConstructionLedger(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	first := remoteCommentPageForTest(
		t,
		remoteCommentResponsePageForTest(
			details,
			"thread-1",
			`{"hasNextPage":true,"endCursor":"comments-2"}`,
			`[`+remoteCommentJSONForTest("comment-2", "second")+`]`,
		),
	)
	second := remoteCommentPageForTest(
		t,
		remoteCommentResponsePageForTest(
			details,
			"thread-1",
			`{"hasNextPage":false,"endCursor":null}`,
			`[`+remoteCommentJSONForTest("comment-3", "third")+`]`,
		),
	)
	retainedBytes := remoteCommentPayloadBytes(*first.Data.Node.Comments.Nodes) +
		remoteCommentPayloadBytes(*second.Data.Node.Comments.Nodes)
	wantRetained := int64(16)*remoteCommentPayloadMetadataBytes + retainedBytes
	exact := wantRetained + int64(64+len("comments-2"))

	for _, test := range []struct {
		name    string
		limit   int64
		wantErr bool
	}{
		{name: "exact", limit: exact},
		{name: "one over", limit: exact - 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger, err := newSourceLedgerWithLimit(test.limit, 0)
			if err != nil {
				t.Fatal(err)
			}
			comments := make(remoteCommentNodes, 0)
			seen := make(map[string]struct{})
			retained := int64(0)
			storage := int64(0)
			charge, err := collectRemoteCommentPage(
				first,
				remotePageIntermediate,
				details,
				"thread-1",
				&comments,
				seen,
				ledger,
				&storage,
				&retained,
			)
			if err == nil {
				err = ledger.reserve(charge)
				retained += remoteCommentPayloadBytes(*first.Data.Node.Comments.Nodes)
			}
			if err == nil {
				charge, err = collectRemoteCommentPage(
					second,
					remotePageFinal,
					details,
					"thread-1",
					&comments,
					seen,
					ledger,
					&storage,
					&retained,
				)
				if err == nil {
					err = ledger.reserve(charge)
					retained += remoteCommentPayloadBytes(*second.Data.Node.Comments.Nodes)
				}
			}
			if test.wantErr != (err != nil) {
				t.Fatalf("error = %v, want error %v", err, test.wantErr)
			}
			if !test.wantErr && (ledger.used != exact || retained != exact) {
				t.Fatalf(
					"used=%d retained=%d, want %d and %d",
					ledger.used,
					retained,
					exact,
					exact,
				)
			}
		})
	}
}

func TestRemoteThreadConstructionAccountingStopsNearEnd(t *testing.T) {
	node := remoteThreadNodeForCancellationTest(513)
	cause := errors.New("stop remote thread accounting")
	for _, cancelAt := range []int{3, 4} {
		checks := 0
		domainBytes, identityBytes, err := remoteThreadNodeConstructionBytes(func() error {
			checks++
			if checks == cancelAt {
				return cause
			}
			return nil
		}, node)
		if !errors.Is(err, cause) {
			t.Fatalf("cancel at %d error = %v", cancelAt, err)
		}
		if checks != cancelAt || domainBytes != 0 || identityBytes != 0 {
			t.Fatalf(
				"cancel at %d: checks=%d domain=%d identities=%d",
				cancelAt,
				checks,
				domainBytes,
				identityBytes,
			)
		}
	}
}

func TestRemoteThreadDTOValidationStopsBeforeLastComment(t *testing.T) {
	node := remoteThreadNodeForCancellationTest(3)
	cause := errors.New("stop remote thread DTO validation")
	for _, cancelAt := range []int{4, 5} {
		checks := 0
		thread, err := validateRemoteThreadCancellable(func() error {
			checks++
			if checks == cancelAt {
				return cause
			}
			return nil
		}, node)
		if !errors.Is(err, cause) {
			t.Fatalf("cancel at %d error = %v", cancelAt, err)
		}
		if checks != cancelAt || thread.CommentCount() != 0 {
			t.Fatalf(
				"cancel at %d: checks=%d comments=%d",
				cancelAt,
				checks,
				thread.CommentCount(),
			)
		}
	}
}

func TestRemoteThreadGlobalIdentityInsertionStopsAtFinalCheck(t *testing.T) {
	thread, err := validateRemoteThread(remoteThreadNodeForCancellationTest(3))
	if err != nil {
		t.Fatal(err)
	}
	collector := remoteThreadCollector{
		ctx:        t.Context(),
		threadIDs:  make(map[string]struct{}),
		commentIDs: make(map[string]struct{}),
	}
	cause := errors.New("stop remote thread identity insertion")
	checks := 0
	err = collector.installIdentities(func() error {
		checks++
		if checks == 4 {
			return cause
		}
		return nil
	}, thread)
	if !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
	if checks != 4 {
		t.Fatalf("cancellation checks = %d, want 4", checks)
	}
}

func TestRemoteThreadInstallationAccountingStopsNearEnd(t *testing.T) {
	thread, err := validateRemoteThread(remoteThreadNodeForCancellationTest(0))
	if err != nil {
		t.Fatal(err)
	}
	threads := make([]review.RemoteThread, 513)
	for index := range threads {
		threads[index] = thread
	}
	cause := errors.New("stop remote thread installation accounting")
	for _, cancelAt := range []int{3, 4} {
		checks := 0
		scratch, retained, err := remoteThreadInstallationBytesCancellable(
			func() error {
				checks++
				if checks == cancelAt {
					return cause
				}
				return nil
			},
			review.Snapshot{},
			threads,
		)
		if !errors.Is(err, cause) {
			t.Fatalf("cancel at %d error = %v", cancelAt, err)
		}
		if checks != cancelAt || scratch != 0 || retained != 0 {
			t.Fatalf(
				"cancel at %d: checks=%d scratch=%d retained=%d",
				cancelAt,
				checks,
				scratch,
				retained,
			)
		}
	}
}

func TestRemoteThreadCollectorDiscardsStateAndCheckpointRollsBack(t *testing.T) {
	const initialBytes = int64(17)
	ledger, err := newSourceLedgerWithLimit(maxSourceConstructionBytes, initialBytes)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := ledger.checkpoint()
	collector := remoteThreadCollector{
		ctx:        t.Context(),
		threadIDs:  make(map[string]struct{}),
		commentIDs: map[string]struct{}{"comment-1": {}},
		ledger:     ledger,
		runCPU:     directCPUAdmission,
	}
	err = collector.addNode(remoteThreadNodeForCancellationTest(2))
	checkpoint.rollback()
	if err == nil || !strings.Contains(err.Error(), "duplicate identity") {
		t.Fatalf("collector error = %v", err)
	}
	if !collector.poisoned || collector.domainBytes != 0 || len(collector.threads) != 0 {
		t.Fatalf(
			"poisoned=%t domain=%d threads=%d",
			collector.poisoned,
			collector.domainBytes,
			len(collector.threads),
		)
	}
	if ledger.used != initialBytes {
		t.Fatalf("ledger used = %d, want %d", ledger.used, initialBytes)
	}
}

func TestRemoteCommentRejectsOversizedTimestampAndURLBeforeParsing(t *testing.T) {
	comment := (*remoteThreadNodeForCancellationTest(1).Comments.Nodes)[0]
	comment.CreatedAt = strings.Repeat("x", int(review.MaxPathBytes)+1)
	if _, err := validateRemoteComment(comment); err == nil ||
		!strings.Contains(err.Error(), "timestamp") {
		t.Fatalf("oversized timestamp error = %v", err)
	}

	comment = (*remoteThreadNodeForCancellationTest(1).Comments.Nodes)[0]
	comment.URL = strings.Repeat("x", (16<<10)+1)
	if _, err := validateRemoteComment(comment); err == nil ||
		!strings.Contains(err.Error(), "URL") {
		t.Fatalf("oversized URL error = %v", err)
	}
}

func remoteThreadNodeForCancellationTest(commentCount int) remoteThreadNode {
	resolved := false
	outdated := false
	hasNextPage := false
	comments := make(remoteCommentNodes, commentCount)
	for index := range comments {
		comments[index] = remoteCommentNode{
			ID:        fmt.Sprintf("comment-%d", index),
			Body:      new("body"),
			CreatedAt: "2026-07-20T12:00:00Z",
			URL:       "https://github.com/owner/repository/pull/7#discussion_r1",
		}
	}
	return remoteThreadNode{
		IsResolved: &resolved,
		IsOutdated: &outdated,
		Comments: &remoteCommentConnection{
			PageInfo: &remotePageInfo{HasNextPage: &hasNextPage},
			Nodes:    &comments,
		},
		ID:       "thread",
		Path:     "reviewed.go",
		DiffSide: githubSideRight,
	}
}

func remoteCommentPageForTest(t *testing.T, encoded string) remoteCommentPage {
	t.Helper()
	var page remoteCommentPage
	if err := json.Unmarshal([]byte(encoded), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestRemoteCommentTargetRejectsRevisionOrThreadDrift(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	valid := remoteCommentResponsePageForTest(
		details,
		"thread-1",
		`{"hasNextPage":false,"endCursor":null}`,
		`[]`,
	)
	tests := []struct {
		name   string
		mutate func(*remoteCommentPage)
	}{
		{
			name: "repository",
			mutate: func(page *remoteCommentPage) {
				page.Data.Repository.NameWithOwner = "other/repository"
			},
		},
		{
			name: "pull request",
			mutate: func(page *remoteCommentPage) {
				page.Data.Repository.PullRequest.Number++
			},
		},
		{
			name: "base revision",
			mutate: func(page *remoteCommentPage) {
				page.Data.Repository.PullRequest.BaseRefOID = strings.Repeat("3", 40)
			},
		},
		{
			name: "head revision",
			mutate: func(page *remoteCommentPage) {
				page.Data.Repository.PullRequest.HeadRefOID = strings.Repeat("3", 40)
			},
		},
		{
			name: "thread",
			mutate: func(page *remoteCommentPage) {
				page.Data.Node.ID = "thread-2"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var page remoteCommentPage
			if err := json.Unmarshal([]byte(valid), &page); err != nil {
				t.Fatal(err)
			}
			test.mutate(&page)
			if err := validateRemoteCommentTarget(page, details, "thread-1"); err == nil {
				t.Fatal("changed nested-comment target unexpectedly accepted")
			}
		})
	}
}

func remoteThreadDetailsForTest(t *testing.T) PullRequestDetails {
	t.Helper()
	repository, err := NewOwnerRepository("owner/repository")
	if err != nil {
		t.Fatal(err)
	}
	base, err := review.NewObjectID(strings.Repeat("1", 40))
	if err != nil {
		t.Fatal(err)
	}
	head, err := review.NewObjectID(strings.Repeat("2", 40))
	if err != nil {
		t.Fatal(err)
	}
	return PullRequestDetails{
		Repository: repository,
		Number:     PullRequestNumber(7),
		BaseOID:    base,
		HeadOID:    head,
	}
}

func remoteThreadResponseForTest(
	details PullRequestDetails,
	connection string,
) string {
	reviewThreads := ""
	if connection != "" {
		reviewThreads = `,"reviewThreads":` + connection
	}
	return fmt.Sprintf(
		`[{"errors":[],"data":{"repository":{"nameWithOwner":%q,`+
			`"pullRequest":{"number":%d,"baseRefOid":%q,"headRefOid":%q%s}}}}]`,
		details.Repository.String(),
		details.Number.Uint64(),
		details.BaseOID.String(),
		details.HeadOID.String(),
		reviewThreads,
	)
}

func remoteCommentResponseForTest(
	details PullRequestDetails,
	threadID, pageInfo, nodes string,
) string {
	return `[` + remoteCommentResponsePageForTest(
		details,
		threadID,
		pageInfo,
		nodes,
	) + `]`
}

func remoteCommentResponsePageForTest(
	details PullRequestDetails,
	threadID, pageInfo, nodes string,
) string {
	return fmt.Sprintf(
		`{"errors":[],"data":{"repository":{"nameWithOwner":%q,`+
			`"pullRequest":{"number":%d,"baseRefOid":%q,"headRefOid":%q}},`+
			`"node":{"id":%q,"comments":{"pageInfo":%s,"nodes":%s}}}}`,
		details.Repository.String(),
		details.Number.Uint64(),
		details.BaseOID.String(),
		details.HeadOID.String(),
		threadID,
		pageInfo,
		nodes,
	)
}

func remoteCommentJSONForTest(id, body string) string {
	return fmt.Sprintf(
		`{"id":%q,"body":%q,"createdAt":"2026-07-20T12:00:00Z",`+
			`"url":"https://github.com/owner/repository/pull/7#discussion_r1",`+
			`"author":{"login":"reviewer"}}`,
		id,
		body,
	)
}

func newRemoteThreadTestService(
	t *testing.T,
	topLevel, nested string,
) *Service {
	t.Helper()
	binaryDirectory := t.TempDir()
	binary := filepath.Join(binaryDirectory, "gh")
	script := `#!/bin/sh
if [ -n "$RVW_CPU_MARKER" ] && [ -e "$RVW_CPU_MARKER" ]; then
	echo 'gh invoked during CPU admission' >&2
	exit 97
fi
for argument in "$@"; do
	if [ "$argument" = "id=thread-1" ]; then
		printf '%s' "$RVW_THREAD_NESTED_RESPONSE"
		exit 0
	fi
done
printf '%s' "$RVW_THREAD_TOP_RESPONSE"
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RVW_THREAD_TOP_RESPONSE", topLevel)
	t.Setenv("RVW_THREAD_NESTED_RESPONSE", nested)
	service := newTestService(t, t.TempDir())
	t.Cleanup(func() {
		closeTestService(t, service)
	})
	return service
}
