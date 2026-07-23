package source

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestLoadPendingReviewAcquiresViewerDraftWith64BitIdentities(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	reviewResponse := pendingReviewResponseForTest(details, `[{`+
		`"id":"PRR_node","fullDatabaseId":"4294967297","body":"summary",`+
		`"state":"PENDING","viewerDidAuthor":true,"viewerCanUpdate":true,`+
		`"commit":{"oid":"`+details.HeadOID.String()+`"}}]`)
	commentsResponse := fmt.Sprintf(`[[{`+
		`"id":4294967298,"node_id":"PRRC_node","pull_request_review_id":4294967297,`+
		`"body":"inline","path":"reviewed.go","commit_id":%q,"original_commit_id":%q,`+
		`"subject_type":"line","line":12,"start_line":null,"side":"RIGHT",`+
		`"start_side":null,"original_line":12,"original_start_line":null,`+
		`"in_reply_to_id":null}]]`, details.HeadOID.String(), details.HeadOID.String())
	service := newPendingReviewTestService(t, reviewResponse, commentsResponse)

	pending, status, detail := service.loadPendingReview(t.Context(), details, directCPUAdmission)
	if status != review.PendingReviewReady || !detail.IsEmpty() {
		t.Fatalf("status=%s detail=%q", status, detail.String())
	}
	if pending.DatabaseID().Uint64() != 4_294_967_297 || pending.CommentCount() != 1 {
		t.Fatalf("review=%d comments=%d", pending.DatabaseID().Uint64(), pending.CommentCount())
	}
	comment := pending.Comments(0, 1)[0]
	if comment.DatabaseID().Uint64() != 4_294_967_298 ||
		comment.Target().Subject() != review.PendingSubjectLine {
		t.Fatalf("comment=%d subject=%d", comment.DatabaseID().Uint64(), comment.Target().Subject())
	}
}

func TestLoadPendingReviewKeepsAbsenceAndAmbiguityDistinct(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	tests := []struct {
		name   string
		nodes  string
		status review.PendingReviewStatus
	}{
		{name: "absent", nodes: `[]`, status: review.PendingReviewAbsent},
		{
			name: "other viewer only",
			nodes: `[{"id":"other","fullDatabaseId":"1","body":"x",` +
				`"state":"PENDING","viewerDidAuthor":false,"viewerCanUpdate":false,` +
				`"commit":{"oid":"` + details.HeadOID.String() + `"}}]`,
			status: review.PendingReviewAbsent,
		},
		{
			name: "ambiguous",
			nodes: `[{"id":"one","fullDatabaseId":"1","body":"x",` +
				`"state":"PENDING","viewerDidAuthor":true,"viewerCanUpdate":true,` +
				`"commit":{"oid":"` + details.HeadOID.String() + `"}},` +
				`{"id":"two","fullDatabaseId":"2","body":"y",` +
				`"state":"PENDING","viewerDidAuthor":true,"viewerCanUpdate":true,` +
				`"commit":{"oid":"` + details.HeadOID.String() + `"}}]`,
			status: review.PendingReviewAmbiguous,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newPendingReviewTestService(
				t,
				pendingReviewResponseForTest(details, test.nodes),
				`[]`,
			)
			_, status, detail := service.loadPendingReview(t.Context(), details, directCPUAdmission)
			if status != test.status {
				t.Fatalf("status=%s detail=%q", status, detail.String())
			}
			if status == review.PendingReviewAbsent && !detail.IsEmpty() {
				t.Fatalf("absence detail=%q", detail.String())
			}
			if status == review.PendingReviewAmbiguous && detail.IsEmpty() {
				t.Fatal("ambiguity has no detail")
			}
		})
	}
}

func TestLoadPendingReviewConsumesEveryValidatedPage(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	page := func(disposition pendingPageDisposition, cursor, nodes string) string {
		return fmt.Sprintf(
			`{"errors":[],"data":{"viewer":{"login":"reviewer"},`+
				`"repository":{"nameWithOwner":%q,"pullRequest":{`+
				`"number":%d,"baseRefOid":%q,"headRefOid":%q,`+
				`"reviews":{"pageInfo":{"hasNextPage":%t,"endCursor":%s},"nodes":%s}}}}}`,
			details.Repository.String(), details.Number.Uint64(),
			details.BaseOID.String(), details.HeadOID.String(),
			disposition == pendingPageContinues, cursor, nodes,
		)
	}
	other := `[{"id":"other","fullDatabaseId":"1","body":"other",` +
		`"state":"PENDING","viewerDidAuthor":false,"viewerCanUpdate":false,` +
		`"commit":{"oid":"` + details.HeadOID.String() + `"}}]`
	owned := `[{"id":"owned","fullDatabaseId":"2","body":"",` +
		`"state":"PENDING","viewerDidAuthor":true,"viewerCanUpdate":true,` +
		`"commit":{"oid":"` + details.HeadOID.String() + `"}}]`
	response := "[" + page(pendingPageContinues, `"cursor-1"`, other) + "," +
		page(pendingPageFinal, "null", owned) + "]"
	service := newPendingReviewTestService(t, response, `[[]]`)
	pending, status, detail := service.loadPendingReview(t.Context(), details, directCPUAdmission)
	if status != review.PendingReviewReady || !detail.IsEmpty() ||
		pending.DatabaseID().Uint64() != 2 {
		t.Fatalf(
			"status=%s detail=%q review=%d",
			status, detail.String(), pending.DatabaseID().Uint64(),
		)
	}
}

func TestLoadPendingReviewRejectsCapabilityIdentityMismatch(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	reviewResponse := pendingReviewResponseForTest(details, `[{`+
		`"id":"PRR_node","fullDatabaseId":"4294967297","body":"summary",`+
		`"state":"PENDING","viewerDidAuthor":true,"viewerCanUpdate":true,`+
		`"commit":{"oid":"`+details.HeadOID.String()+`"}}]`)
	commentsResponse := fmt.Sprintf(`[[{`+
		`"id":4294967298,"node_id":"PRRC_node","pull_request_review_id":4294967297,`+
		`"body":"inline","path":"reviewed.go","commit_id":%q,"original_commit_id":%q,`+
		`"subject_type":"file","line":null,"start_line":null,"side":"",`+
		`"start_side":null,"original_line":null,"original_start_line":null,`+
		`"in_reply_to_id":null}]]`, details.HeadOID.String(), details.HeadOID.String())
	service := newPendingReviewTestService(t, reviewResponse, commentsResponse)
	t.Setenv("RVW_PENDING_CAPABILITIES", fmt.Sprintf(
		`[{"errors":[],"data":{"repository":{"nameWithOwner":%q,"pullRequest":{`+
			`"number":%d,"baseRefOid":%q,"headRefOid":%q}},"node":{"id":"PRR_node",`+
			`"comments":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[{`+
			`"id":"PRRC_node","fullDatabaseId":"999",`+
			`"viewerCanUpdate":true,"viewerCanDelete":true}]}}}}]`,
		details.Repository.String(), details.Number.Uint64(),
		details.BaseOID.String(), details.HeadOID.String(),
	))
	_, status, detail := service.loadPendingReview(t.Context(), details, directCPUAdmission)
	if status != review.PendingReviewUnavailable || detail.IsEmpty() {
		t.Fatalf("status=%s detail=%q", status, detail.String())
	}
}

func TestPendingCommentRejectsReplyWithInvalidDatabaseIdentity(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	value := pendingReviewCommentPayload{
		Body:                new("reply"),
		NodeID:              "node",
		Path:                "reviewed.go",
		CommitID:            details.HeadOID.String(),
		OriginalCommitID:    details.HeadOID.String(),
		ID:                  2,
		PullRequestReviewID: 1,
		InReplyToID:         new(githubUint64(0)),
	}
	if _, err := validatePendingComment(value, map[githubUint64]review.CommentNodeID{2: "node"}); err == nil {
		t.Fatal("zero reply database identity unexpectedly accepted")
	}
}

func TestPendingCommentPreservesOutdatedMultilineRange(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	value := pendingReviewCommentPayload{
		Body:                new("outdated range"),
		NodeID:              "node",
		Path:                "reviewed.go",
		CommitID:            details.HeadOID.String(),
		OriginalCommitID:    details.HeadOID.String(),
		Side:                "RIGHT",
		StartSide:           new("RIGHT"),
		SubjectType:         "line",
		ID:                  2,
		PullRequestReviewID: 1,
		OriginalLine:        new(uint32(12)),
		OriginalStartLine:   new(uint32(10)),
		capabilities:        pendingCapabilities(t),
	}
	comment, err := validatePendingComment(
		value,
		map[githubUint64]review.CommentNodeID{2: "node"},
	)
	if err != nil {
		t.Fatal(err)
	}
	startSide, start, side, line := comment.Target().Lines()
	if !comment.Target().Outdated() || start == nil || line == nil ||
		start.Uint32() != 10 || line.Uint32() != 12 ||
		startSide != review.LineNew || side != review.LineNew {
		t.Fatalf(
			"outdated=%t start=%v line=%v sides=%d/%d",
			comment.Target().Outdated(), start, line, startSide, side,
		)
	}
}

type pendingPageDisposition uint8

const (
	pendingPageFinal pendingPageDisposition = iota + 1
	pendingPageContinues
)

func pendingCapabilities(tb testing.TB) review.PendingCapabilities {
	tb.Helper()
	capabilities, err := review.NewPendingCapabilities(
		review.PendingCapabilityUpdate,
		review.PendingCapabilityDelete,
	)
	if err != nil {
		tb.Fatal(err)
	}
	return capabilities
}

func TestPendingCommentRejectsExplicitZeroLine(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	value := pendingReviewCommentPayload{
		Body:                new("line"),
		NodeID:              "node",
		Path:                "reviewed.go",
		CommitID:            details.HeadOID.String(),
		OriginalCommitID:    details.HeadOID.String(),
		Side:                "RIGHT",
		SubjectType:         "line",
		ID:                  2,
		PullRequestReviewID: 1,
		Line:                new(uint32(0)),
	}
	if _, err := validatePendingComment(
		value,
		map[githubUint64]review.CommentNodeID{2: "node"},
	); err == nil {
		t.Fatal("zero line unexpectedly accepted")
	}
}

func pendingReviewResponseForTest(details PullRequestDetails, nodes string) string {
	return fmt.Sprintf(
		`[{"errors":[],"data":{"viewer":{"login":"reviewer"},`+
			`"repository":{"nameWithOwner":%q,"pullRequest":{`+
			`"number":%d,"baseRefOid":%q,"headRefOid":%q,`+
			`"reviews":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":%s}}}}}]`,
		details.Repository.String(),
		details.Number.Uint64(),
		details.BaseOID.String(),
		details.HeadOID.String(),
		nodes,
	)
}

func newPendingReviewTestService(t *testing.T, reviews, comments string) *Service {
	t.Helper()
	details := remoteThreadDetailsForTest(t)
	t.Setenv("RVW_PENDING_CAPABILITIES", fmt.Sprintf(
		`[{"errors":[],"data":{"repository":{"nameWithOwner":%q,"pullRequest":{`+
			`"number":%d,"baseRefOid":%q,"headRefOid":%q}},"node":{"id":"PRR_node",`+
			`"comments":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[{`+
			`"id":"PRRC_node","fullDatabaseId":"4294967298",`+
			`"viewerCanUpdate":true,"viewerCanDelete":true}]}}}}]`,
		details.Repository.String(), details.Number.Uint64(),
		details.BaseOID.String(), details.HeadOID.String(),
	))
	reviewPages := unwrapGitHubTestPages(t, reviews)
	commentPages := unwrapGitHubTestPages(t, comments)
	reviewPage, commentPage := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/graphql" {
			var body struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			if strings.Contains(body.Query, "comments(first:100") {
				pages := unwrapGitHubTestPages(t, os.Getenv("RVW_PENDING_CAPABILITIES"))
				_, _ = response.Write(pages[0])
				return
			}
			index := min(reviewPage, len(reviewPages)-1)
			reviewPage++
			_, _ = response.Write(reviewPages[index])
			return
		}
		index := min(commentPage, len(commentPages)-1)
		commentPage++
		_, _ = response.Write(commentPages[index])
	}))
	t.Cleanup(server.Close)
	workingDirectory, _ := NewWorkingDirectory(t.TempDir())
	token, _ := NewGitHubToken("test-token")
	service, err := NewService(workingDirectory, ServiceOptions{GitHubToken: &token})
	if err != nil {
		t.Fatal(err)
	}
	service.github.apiBase = server.URL
	t.Cleanup(func() { closeTestService(t, service) })
	return service
}

func unwrapGitHubTestPages(t *testing.T, encoded string) []json.RawMessage {
	t.Helper()
	var pages []json.RawMessage
	if encoded == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(encoded), &pages); err != nil {
		t.Fatalf("decode test GitHub pages: %v", err)
	}
	if len(pages) == 0 {
		return []json.RawMessage{json.RawMessage("[]")}
	}
	return pages
}

func TestDecodePendingReviewPagesRejectsInvalidEnvelopes(t *testing.T) {
	for _, encoded := range [][]byte{
		[]byte(`[]`),
		[]byte(`[null]`),
		{'[', '"', 0xff, '"', ']'},
	} {
		if err := validatePendingReviewPages(encoded); err == nil {
			t.Fatalf("decode %q unexpectedly succeeded", strings.ToValidUTF8(string(encoded), "?"))
		}
	}
}
