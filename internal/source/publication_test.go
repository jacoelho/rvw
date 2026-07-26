package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func TestPublicationCheckRejectsTerminalPullRequestState(t *testing.T) {
	repository, err := NewOwnerRepository("fixture/repository")
	if err != nil {
		t.Fatal(err)
	}
	base, err := change.NewObjectID(strings.Repeat("1", 40))
	if err != nil {
		t.Fatal(err)
	}
	head, err := change.NewObjectID(strings.Repeat("2", 40))
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := change.NewSourceID(1)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := change.NewPublicationPolicies()
	if err != nil {
		t.Fatal(err)
	}
	options, err := change.NewPublicationOptions(change.PublicationEventComment, policies)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		mergedAt *string
		want     string
	}{
		{name: "closed-unmerged", want: "pull request is closed"},
		{
			name:     "merged",
			mergedAt: new("2026-07-26T07:07:19Z"),
			want:     "pull request is already merged",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := pullRequestPayload{
				MergedAt: test.mergedAt,
				Title:    "Terminal PR",
				HTMLURL:  "https://github.com/fixture/repository/pull/17",
				State:    strings.ToLower(string(GitHubClosed)),
				Number:   17,
			}
			payload.Base.Ref = "main"
			payload.Base.SHA = base.String()
			payload.Head.Ref = "feature"
			payload.Head.SHA = head.String()
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				if request.Method != http.MethodGet ||
					request.URL.Path != "/repos/fixture/repository/pulls/17" {
					http.Error(writer, "unexpected request", http.StatusBadRequest)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write(encoded)
			}))
			t.Cleanup(server.Close)
			token, err := NewGitHubToken("test-token")
			if err != nil {
				t.Fatal(err)
			}
			workingDirectory, err := NewWorkingDirectory(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewService(
				workingDirectory,
				ServiceOptions{GitHubToken: &token},
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeTestService(t, service) })
			service.github.apiBase = server.URL
			target := PublicationTarget{
				repository: repository,
				base:       base,
				head:       head,
				number:     17,
				source:     sourceID,
			}

			_, err = service.AcquirePublicationCheck(
				t.Context(),
				target,
				options,
				PublicationCheckNew,
				directCPUAdmission,
			)
			if err == nil || err.Error() != "check-publication: "+test.want {
				t.Fatalf("publication check error = %v, want %q", err, test.want)
			}
			var failure *FailureError
			if !errors.As(err, &failure) ||
				failure.Operation != OperationCheckPublication ||
				failure.Kind != FailureFailed {
				t.Fatalf("publication failure = %#v", failure)
			}
		})
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
