package source

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

const (
	benchmarkCommentPages       = 10
	benchmarkThreadsPerPage     = 100
	benchmarkCommentsPerThread  = 100
	benchmarkUnknownRepetitions = 64
	benchmarkMaximumInputBytes  = 64 << 20
	benchmarkBaseOID            = "1111111111111111111111111111111111111111"
	benchmarkHeadOID            = "2222222222222222222222222222222222222222"
)

type remoteThreadFixtureResult struct {
	pages    int
	threads  int
	comments int
}

type remoteThreadFixtureExpectation struct {
	pages             int
	threadsPerPage    int
	commentsPerThread int
}

func TestRemoteThreadJSONRoutingBenchmarkFixtures(t *testing.T) {
	tests := []struct {
		name        string
		encoded     string
		expectation remoteThreadFixtureExpectation
	}{
		{
			name:        "paginated comments",
			encoded:     buildRemoteThreadPaginationFixture(2, 3, 4),
			expectation: remoteThreadFixtureExpectation{pages: 2, threadsPerPage: 3, commentsPerThread: 4},
		},
		{
			name:        "maximum-depth unknown wrappers",
			encoded:     buildRemoteThreadMaximumDepthFixture(2),
			expectation: remoteThreadFixtureExpectation{pages: 1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger, metrics, err := inspectRemoteThreadFixture(t.Context(), test.encoded)
			if err != nil {
				t.Fatal(err)
			}
			got, decodeErr := decodeRemoteThreadFixture(
				t.Context(), strings.NewReader(test.encoded), ledger, metrics, test.expectation,
			)
			metrics.release(ledger)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			assertRemoteThreadFixtureResult(t, got, test.expectation)
			if ledger.used != 0 {
				t.Fatalf("fixture retained %d bytes after release", ledger.used)
			}
		})
	}
}

func BenchmarkRemoteThreadJSONRouting100000Comments(b *testing.B) {
	encoded := buildRemoteThreadPaginationFixture(
		benchmarkCommentPages,
		benchmarkThreadsPerPage,
		benchmarkCommentsPerThread,
	)
	expectation := remoteThreadFixtureExpectation{
		pages:             benchmarkCommentPages,
		threadsPerPage:    benchmarkThreadsPerPage,
		commentsPerThread: benchmarkCommentsPerThread,
	}
	if got := expectation.pages * expectation.threadsPerPage * expectation.commentsPerThread; got != 100_000 {
		b.Fatalf("fixture comments = %d, want 100000", got)
	}
	if len(encoded) >= benchmarkMaximumInputBytes {
		b.Fatalf("fixture bytes = %d, want less than %d", len(encoded), benchmarkMaximumInputBytes)
	}
	benchmarkRemoteThreadFixture(b, encoded, expectation)
}

func BenchmarkRemoteThreadJSONRoutingMaximumDepthUnknownWrappers(b *testing.B) {
	encoded := buildRemoteThreadMaximumDepthFixture(benchmarkUnknownRepetitions)
	if len(encoded) >= benchmarkMaximumInputBytes {
		b.Fatalf("fixture bytes = %d, want less than %d", len(encoded), benchmarkMaximumInputBytes)
	}
	benchmarkRemoteThreadFixture(b, encoded, remoteThreadFixtureExpectation{pages: 1})
}

func benchmarkRemoteThreadFixture(
	b *testing.B,
	encoded string,
	expectation remoteThreadFixtureExpectation,
) {
	b.Helper()
	ledger, metrics, err := inspectRemoteThreadFixture(b.Context(), encoded)
	if err != nil {
		b.Fatal(err)
	}
	got, err := decodeRemoteThreadFixture(
		b.Context(), strings.NewReader(encoded), ledger, metrics, expectation,
	)
	if err != nil {
		metrics.release(ledger)
		b.Fatal(err)
	}
	assertRemoteThreadFixtureResult(b, got, expectation)
	if ledger.used != metrics.cardinalityBytes {
		metrics.release(ledger)
		b.Fatalf("validated fixture retained %d decoder bytes", ledger.used-metrics.cardinalityBytes)
	}

	readers := make([]strings.Reader, b.N)
	for index := range readers {
		readers[index].Reset(encoded)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		got, err = decodeRemoteThreadFixture(
			b.Context(), &readers[index], ledger, metrics, expectation,
		)
		if err != nil {
			b.Fatal(err)
		}
		assertRemoteThreadFixtureResult(b, got, expectation)
	}
	b.StopTimer()
	b.ReportMetric(float64(len(encoded)), "input-B")
	b.ReportMetric(float64(expectation.pages*expectation.threadsPerPage*expectation.commentsPerThread), "comments/op")
	metrics.release(ledger)
	if ledger.used != 0 {
		b.Fatalf("benchmark retained %d bytes after release", ledger.used)
	}
}

func inspectRemoteThreadFixture(
	ctx context.Context,
	encoded string,
) (*sourceLedger, jsonCommandMetrics, error) {
	ledger, err := newSourceLedger(0)
	if err != nil {
		return nil, jsonCommandMetrics{}, err
	}
	limits, err := newJSONFramingLimits(change.MaxRemoteThreads, maxGitHubPageCollectionElements)
	if err != nil {
		return nil, jsonCommandMetrics{}, err
	}
	metrics, err := inspectJSONWithLimits(
		ctx,
		strings.NewReader(encoded),
		maxGitHubJSONTokenBytes,
		directCPUAdmission,
		ledger,
		limits,
	)
	if err != nil {
		return nil, jsonCommandMetrics{}, fmt.Errorf("inspect benchmark fixture: %w", err)
	}
	return ledger, metrics, nil
}

func decodeRemoteThreadFixture(
	ctx context.Context,
	input *strings.Reader,
	ledger *sourceLedger,
	metrics jsonCommandMetrics,
	expectation remoteThreadFixtureExpectation,
) (remoteThreadFixtureResult, error) {
	result := remoteThreadFixtureResult{}
	err := decodeRemoteThreadStream(
		ctx,
		input,
		directCPUAdmission,
		ledger,
		metrics,
		func(index int, position remotePagePosition, page remoteThreadPage) (int64, error) {
			if index != result.pages {
				return 0, fmt.Errorf("page index = %d, want %d", index, result.pages)
			}
			wantPosition := remotePageIntermediate
			if index+1 == expectation.pages {
				wantPosition = remotePageFinal
			}
			if position != wantPosition {
				return 0, fmt.Errorf("page %d position = %d, want %d", index, position, wantPosition)
			}
			if err := visitRemoteThreadFixturePage(&result, page, position, expectation); err != nil {
				return 0, fmt.Errorf("page %d: %w", index, err)
			}
			result.pages++
			return 0, nil
		},
	)
	return result, err
}

func visitRemoteThreadFixturePage(
	result *remoteThreadFixtureResult,
	page remoteThreadPage,
	position remotePagePosition,
	expectation remoteThreadFixtureExpectation,
) error {
	if len(page.Errors) != 0 || page.Data.Repository == nil || page.Data.Repository.PullRequest == nil {
		return errorsNewBenchmarkFixture("envelope is incomplete")
	}
	repository := page.Data.Repository
	pull := repository.PullRequest
	if repository.NameWithOwner != "octocat/rvw" || pull.Number != 42 ||
		pull.BaseRefOID != benchmarkBaseOID || pull.HeadRefOID != benchmarkHeadOID {
		return errorsNewBenchmarkFixture("pull request target differs")
	}
	connection := pull.ReviewThreads
	if connection == nil || connection.PageInfo == nil || connection.PageInfo.HasNextPage == nil || connection.Nodes == nil {
		return errorsNewBenchmarkFixture("thread connection is incomplete")
	}
	wantNext := position == remotePageIntermediate
	if *connection.PageInfo.HasNextPage != wantNext {
		return fmt.Errorf("hasNextPage = %t, want %t", *connection.PageInfo.HasNextPage, wantNext)
	}
	if wantNext == (connection.PageInfo.EndCursor == nil) {
		return errorsNewBenchmarkFixture("thread cursor differs from page position")
	}
	if len(*connection.Nodes) != expectation.threadsPerPage {
		return fmt.Errorf("threads = %d, want %d", len(*connection.Nodes), expectation.threadsPerPage)
	}
	for _, thread := range *connection.Nodes {
		if thread.ID == "" || thread.Path != "internal/change/change.go" || thread.DiffSide != "RIGHT" ||
			thread.IsResolved == nil || *thread.IsResolved || thread.IsOutdated == nil || *thread.IsOutdated ||
			thread.Line == nil || thread.StartLine == nil || thread.Comments == nil {
			return errorsNewBenchmarkFixture("thread differs")
		}
		comments := thread.Comments
		if comments.PageInfo == nil || comments.PageInfo.HasNextPage == nil || *comments.PageInfo.HasNextPage ||
			comments.PageInfo.EndCursor != nil || comments.Nodes == nil {
			return errorsNewBenchmarkFixture("comment connection is incomplete")
		}
		if len(*comments.Nodes) != expectation.commentsPerThread {
			return fmt.Errorf("comments = %d, want %d", len(*comments.Nodes), expectation.commentsPerThread)
		}
		result.threads++
		for _, comment := range *comments.Nodes {
			if comment.ID == "" || comment.Body == nil || comment.CreatedAt != "2026-07-23T12:34:56Z" ||
				comment.URL == "" || comment.Author == nil || comment.Author.Login != "reviewer" {
				return errorsNewBenchmarkFixture("comment differs")
			}
			result.comments++
		}
	}
	return nil
}

func errorsNewBenchmarkFixture(message string) error {
	return fmt.Errorf("benchmark fixture %s", message)
}

func assertRemoteThreadFixtureResult(
	tb testing.TB,
	got remoteThreadFixtureResult,
	expectation remoteThreadFixtureExpectation,
) {
	tb.Helper()
	want := remoteThreadFixtureResult{
		pages:    expectation.pages,
		threads:  expectation.pages * expectation.threadsPerPage,
		comments: expectation.pages * expectation.threadsPerPage * expectation.commentsPerThread,
	}
	if got != want {
		tb.Fatalf("fixture result = %+v, want %+v", got, want)
	}
}

func buildRemoteThreadPaginationFixture(pages, threadsPerPage, commentsPerThread int) string {
	var builder strings.Builder
	builder.Grow(pages * threadsPerPage * commentsPerThread * 180)
	builder.WriteByte('[')
	threadID := 0
	commentID := 0
	for page := range pages {
		if page != 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"data":{"repository":{"nameWithOwner":"octocat/rvw","pullRequest":{`)
		builder.WriteString(`"baseRefOid":"` + benchmarkBaseOID + `",`)
		builder.WriteString(`"headRefOid":"` + benchmarkHeadOID + `","number":42,`)
		builder.WriteString(`"reviewThreads":{"pageInfo":{"hasNextPage":`)
		if page+1 == pages {
			builder.WriteString(`false,"endCursor":null},"nodes":[`)
		} else {
			builder.WriteString(`true,"endCursor":"page-`)
			builder.WriteString(strconv.Itoa(page + 1))
			builder.WriteString(`"},"nodes":[`)
		}
		for thread := range threadsPerPage {
			if thread != 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(`{"id":"thread-`)
			builder.WriteString(strconv.Itoa(threadID))
			builder.WriteString(`","path":"internal/change/change.go","diffSide":"RIGHT",`)
			builder.WriteString(`"isResolved":false,"isOutdated":false,"line":123,"startLine":120,`)
			builder.WriteString(`"comments":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[`)
			for comment := range commentsPerThread {
				if comment != 0 {
					builder.WriteByte(',')
				}
				builder.WriteString(`{"id":"comment-`)
				builder.WriteString(strconv.Itoa(commentID))
				builder.WriteString(`","body":"Review note `)
				builder.WriteString(strconv.Itoa(commentID))
				builder.WriteString(`","createdAt":"2026-07-23T12:34:56Z","url":"https://example.test/pulls/42/comments/`)
				builder.WriteString(strconv.Itoa(commentID))
				builder.WriteString(`","author":{"login":"reviewer"}}`)
				commentID++
			}
			builder.WriteString(`]}}`)
			threadID++
		}
		builder.WriteString(`]}}}},"errors":[]}`)
	}
	builder.WriteByte(']')
	return builder.String()
}

func buildRemoteThreadMaximumDepthFixture(repetitions int) string {
	var builder strings.Builder
	builder.Grow(repetitions * maxGitHubJSONDepth * 16)
	builder.WriteString(`[{`)
	for repetition := range repetitions {
		if repetition != 0 {
			builder.WriteByte(',')
		}
		writeUnknownWrapperChain(&builder, "emptyUnknown"+strconv.Itoa(repetition), "")
		builder.WriteByte(',')
		writeUnknownWrapperChain(&builder, "namedUnknown"+strconv.Itoa(repetition), "unknown")
	}
	if repetitions != 0 {
		builder.WriteByte(',')
	}
	builder.WriteString(`"data":{"repository":{"nameWithOwner":"octocat/rvw","pullRequest":{`)
	builder.WriteString(`"baseRefOid":"` + benchmarkBaseOID + `",`)
	builder.WriteString(`"headRefOid":"` + benchmarkHeadOID + `","number":42,`)
	builder.WriteString(`"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[]}}}},`)
	builder.WriteString(`"errors":[]}]`)
	return builder.String()
}

func writeUnknownWrapperChain(builder *strings.Builder, outerName, innerName string) {
	const pageEnvelopeContainers = 2
	wrappers := maxGitHubJSONDepth - pageEnvelopeContainers
	for depth := range wrappers {
		name := innerName
		if depth == 0 {
			name = outerName
		}
		builder.WriteString(strconv.Quote(name))
		builder.WriteString(`:{`)
	}
	builder.WriteString(`"id":"shadow","body":"ignored"`)
	builder.WriteString(strings.Repeat("}", wrappers))
}
