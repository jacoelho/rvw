package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestPublicationCheckFindsExistingPendingReviewForNewDraft(t *testing.T) {
	details := remoteThreadDetailsForTest(t)
	pendingPage := unwrapGitHubTestPages(t, pendingReviewResponseForTest(details, `[{`+
		`"id":"existing-review","fullDatabaseId":"42","body":"existing",`+
		`"state":"PENDING","viewerDidAuthor":true,"viewerCanUpdate":true,`+
		`"commit":{"oid":"`+details.HeadOID.String()+`"}}]`))[0]
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/repos/owner/repository/pulls/7":
			_, _ = fmt.Fprintf(
				response,
				`{"title":"title","html_url":"https://github.com/owner/repository/pull/7",`+
					`"state":"open","number":7,"changed_files":1,"draft":false,`+
					`"merged_at":null,"user":{"login":"owner"},`+
					`"base":{"ref":"main","sha":%q},`+
					`"head":{"ref":"feature","sha":%q}}`,
				details.BaseOID.String(),
				details.HeadOID.String(),
			)
		case request.Method == http.MethodPost && request.URL.Path == "/graphql":
			_, _ = response.Write(pendingPage)
		case request.Method == http.MethodGet &&
			request.URL.Path == "/repos/owner/repository/pulls/7/reviews/42/comments":
			_, _ = response.Write([]byte(`[]`))
		default:
			http.Error(response, "unexpected request", http.StatusBadRequest)
		}
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
	target := PublicationTarget{
		repository: details.Repository, number: details.Number,
		base: details.BaseOID, head: details.HeadOID, source: 1,
	}
	policies, _ := review.NewPublicationPolicies()
	options, _ := review.NewPublicationOptions(review.PublicationEventDraft, policies)
	acquisition, err := service.AcquirePublicationCheck(
		t.Context(), target, options, PublicationCheckNew, directCPUAdmission,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := acquisition.FreshPending()
	if !ok || pending.NodeID().String() != "existing-review" ||
		pending.DatabaseID().Uint64() != 42 {
		t.Fatalf("fresh pending = (%t, %q, %d)", ok, pending.NodeID(), pending.DatabaseID())
	}
}

func TestDecodePublicationReviewIDUsesCanonicalFramer(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		want    uint64
	}{
		{name: "valid", encoded: `{"id":7,"unknown":{"nested":true}}`, want: 7},
		{name: "missing", encoded: `{}`},
		{name: "zero", encoded: `{"id":0}`},
		{name: "string", encoded: `{"id":"7"}`},
		{name: "duplicate", encoded: `{"id":7,"id":8}`},
		{name: "container", encoded: `{"id":{}}`},
		{name: "root array", encoded: `[{"id":7}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodePublicationReviewID(t.Context(), []byte(test.encoded))
			if test.want == 0 {
				if err == nil {
					t.Fatalf("invalid response returned review %d", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("review identity = (%d, %v), want %d", got, err, test.want)
			}
		})
	}
}

func TestDecodePublicationReviewIDPropagatesCancellation(t *testing.T) {
	cause := errors.New("stop publication response")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if _, err := decodePublicationReviewID(ctx, []byte(`{"id":7}`)); !errors.Is(err, cause) {
		t.Fatalf("publication response cancellation = %v", err)
	}
}
