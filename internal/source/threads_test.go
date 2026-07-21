package source

import (
	"encoding/json"
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
	page.Errors = []json.RawMessage{json.RawMessage(`{"message":"partial"}`)}
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
	initialComments := []remoteCommentNode{}
	initial := &remoteCommentConnection{
		PageInfo: &remotePageInfo{
			HasNextPage: &hasNextPage,
			EndCursor:   &endCursor,
		},
		Nodes: &initialComments,
	}

	if _, _, err := service.loadRemainingRemoteComments(
		t.Context(),
		details,
		"thread-1",
		initial,
		review.MaxRemoteThreadBytes,
	); err == nil || !strings.Contains(err.Error(), "cursor repeated") {
		t.Fatalf("repeated cursor error = %v", err)
	}
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
