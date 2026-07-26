package source

import (
	"fmt"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func TestLoadPendingReviewRejectsUnsafeSummaryOrInlineBodyWithoutReplacement(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	reviewNode := func(body string) string {
		return fmt.Sprintf(
			`[{"id":"PRR_node","fullDatabaseId":"4294967297","body":%q,`+
				`"state":"PENDING","viewerDidAuthor":true,"viewerCanUpdate":true,`+
				`"commit":{"oid":%q}}]`,
			body,
			details.HeadOID.String(),
		)
	}
	inline := func(body string) string {
		return fmt.Sprintf(`[[{`+
			`"id":4294967298,"node_id":"PRRC_node","pull_request_review_id":4294967297,`+
			`"body":%q,"path":"reviewed.go","commit_id":%q,"original_commit_id":%q,`+
			`"subject_type":"line","line":12,"start_line":null,"side":"RIGHT",`+
			`"start_side":null,"original_line":12,"original_start_line":null,`+
			`"in_reply_to_id":null}]]`, body, details.HeadOID.String(), details.HeadOID.String())
	}
	tests := []struct {
		name     string
		reviews  string
		comments string
	}{
		{name: "summary", reviews: reviewNode("unsafe\u202e"), comments: `[[]]`},
		{name: "inline", reviews: reviewNode("safe"), comments: inline("unsafe\u202e")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newPendingReviewTestService(
				t,
				pendingReviewResponseForTest(details, test.reviews),
				test.comments,
			)
			pending, status, detail := service.loadPendingReview(
				t.Context(),
				details,
				directCPUAdmission,
			)
			if status != change.PendingReviewUnavailable || detail.IsEmpty() {
				t.Fatalf("status %s, detail %q", status, detail.String())
			}
			if pending.DatabaseID() != 0 || pending.CommentCount() != 0 {
				t.Fatalf("unsafe response returned review %d with %d comments", pending.DatabaseID(), pending.CommentCount())
			}
		})
	}
}
