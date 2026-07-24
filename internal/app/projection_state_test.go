package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jacoelho/rvw/internal/review"
)

func TestEmptyFileProjectionDistinguishesLoadingEmptyAndFailure(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	styles := newSemanticStyles(model.settings)
	model.visibleFileTree = fileTree{}
	model.allFileTree = fileTree{}
	model.filteredFileTree = nil
	tests := []struct {
		name       string
		projection fileProjectionState
		want       string
		forbid     string
	}{
		{
			name: "initial loading",
			projection: fileProjectionState{
				workspace: projectionLoading, filter: projectionReady,
			},
			want: "Loading changed files", forbid: "No changed files",
		},
		{
			name: "loaded empty",
			projection: fileProjectionState{
				workspace: projectionReady, filter: projectionReady,
			},
			want: "No changed files",
		},
		{
			name: "workspace failure",
			projection: fileProjectionState{
				workspace:       projectionFailed,
				filter:          projectionReady,
				workspaceDetail: "index unavailable",
			},
			want: "Error: changed files unavailable", forbid: "No changed files",
		},
		{
			name: "filtered empty",
			projection: fileProjectionState{
				workspace:      projectionReady,
				filter:         projectionReady,
				requestedQuery: "missing",
				appliedQuery:   "missing",
			},
			want: "No files match the filter",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model.fileProjection = test.projection
			lines, _ := model.fileTreeLines(60, 5, styles)
			got := ansi.Strip(strings.Join(lines, "\n"))
			if !strings.Contains(got, test.want) ||
				test.forbid != "" && strings.Contains(got, test.forbid) {
				t.Fatalf("empty projection = %q, want %q and forbid %q", got, test.want, test.forbid)
			}
		})
	}
}

func TestFileFilterKeepsAppliedProjectionSeparateFromRequestedQuery(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{
		"alpha.txt": "changed\n",
		"beta.txt":  "changed\n",
	})
	prior := model.visibleFileTree
	model.filteredFileTree = &prior
	model.fileProjection = fileProjectionState{
		workspace:      projectionReady,
		filter:         projectionReady,
		requestedQuery: "alpha",
		appliedQuery:   "alpha",
	}

	if command := model.startFileFilter("beta"); command == nil {
		t.Fatal("new file filter did not schedule incremental work")
	}
	if model.fileProjection.requestedQuery != "beta" ||
		model.fileProjection.appliedQuery != "alpha" ||
		model.fileProjection.filter != projectionLoading {
		t.Fatalf("loading projection = %#v", model.fileProjection)
	}
	if suffix := model.fileProjectionHeaderSuffix(); suffix != " · FILTERING" {
		t.Fatalf("loading header suffix = %q", suffix)
	}

	model.filterTreeOperation = 99
	model.filterTreeGeneration = model.filterGeneration
	model.applyFileFilterTree(FileTreeOutcome{
		ID: 99, Generation: model.filterGeneration, Err: errors.New("worker failed"),
	})
	if model.fileProjection.filter != projectionFailed ||
		model.fileProjection.appliedQuery != "alpha" ||
		model.fileProjection.requestedQuery != "beta" {
		t.Fatalf("failed projection = %#v", model.fileProjection)
	}
	status := strings.Join(model.statusFactLines(), "\n")
	for _, want := range []string{
		"file filter requested beta",
		"file filter applied   alpha",
		"error; prior results retained",
		"worker failed",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status omitted %q:\n%s", want, status)
		}
	}
}

func TestRemoteCommentDegradationRemainsVisibleWhenRowsFillViewport(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	detail, _ := review.NewOperationDetail("GitHub unavailable")
	snapshot, err := model.review.Snapshot().WithRemoteThreadFailure(
		review.RemoteThreadsUnavailable,
		detail,
	)
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = review.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	appendTestReviewComments(t, model,
		"one", "two", "three", "four", "five", "six", "seven", "eight",
	)
	lines, _ := model.commentLines(32, 3, newSemanticStyles(model.settings))
	if len(lines) != 3 {
		t.Fatalf("comment viewport rows = %d, want 3", len(lines))
	}
	model.size.Width = 160
	model.size.Height = 50
	header := ansi.Strip(model.workspaceHeaderLine(
		model.workspaceLayout(model.size.Width, model.size.Height),
		newSemanticStyles(model.settings),
	))
	if !strings.Contains(header, "! REMOTE") {
		t.Fatalf("full comment viewport hid persistent degradation:\n%s", header)
	}
	status := ansi.Strip(model.statusLine(model.size.Width, newSemanticStyles(model.settings)))
	if !strings.Contains(status, "Remote comments unavailable") {
		t.Fatalf("status line hid persistent remote degradation: %q", status)
	}
}

func TestRemoteCommentLoadingAndFailureRetainAppliedRows(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	file, ok := model.review.Snapshot().File(0)
	if !ok {
		t.Fatal("fixture file is unavailable")
	}
	thread := newTestRemoteThread(t, "thread-stable", file.IdentityPath())
	snapshot, err := model.review.Snapshot().WithRemoteThreads([]review.RemoteThread{thread})
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = review.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	model.selectCommentIndex(0)
	model.foreground.reload.phase = reloadAcquiring
	if suffix := model.commentSourceHeaderSuffix(); suffix != " · REMOTE LOADING" {
		t.Fatalf("loading suffix = %q", suffix)
	}
	if status := strings.Join(model.statusFactLines(), "\n"); !strings.Contains(status, "loading; prior results retained (1)") {
		t.Fatalf("loading status omitted retained projection:\n%s", status)
	}

	model.foreground.reload.phase = reloadIdle
	detail, _ := review.NewOperationDetail("GitHub unavailable")
	snapshot, err = snapshot.WithRemoteThreadFailure(review.RemoteThreadsUnavailable, detail)
	if err != nil {
		t.Fatal(err)
	}
	model.review, err = review.NewReview(testReviewRequest(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if model.commentRowCount() != 1 ||
		model.commentSourceHeaderSuffix() != " · ! REMOTE UNAVAILABLE" {
		t.Fatalf("failed remote projection = count %d, suffix %q", model.commentRowCount(), model.commentSourceHeaderSuffix())
	}
	if summary := model.workspaceConditionSummary(); !strings.Contains(summary, "prior results retained") {
		t.Fatalf("failure summary omitted stale cue: %q", summary)
	}
}

func TestStatusOverlayEscapesDynamicTerminalControls(t *testing.T) {
	model, _ := newTestModel(t, map[string]string{"alpha.txt": "changed\n"})
	model.size = TerminalSize{Width: 100, Height: 30, Known: true}
	model.overlay = OverlayStatus
	model.fileProjection = fileProjectionState{
		workspace:       projectionFailed,
		filter:          projectionFailed,
		workspaceDetail: "workspace \x1b[2J cleared",
		filterDetail:    "filter \u202E hidden",
	}
	model.notice = &Notice{Kind: NoticeError, Text: "notice \x1b]52;c;payload\a"}

	content := model.View().Content
	for _, unsafe := range []string{"\x1b[2J", "\x1b]52", "\u202E"} {
		if strings.Contains(content, unsafe) {
			t.Fatalf("status overlay retained terminal control %q in %q", unsafe, content)
		}
	}
	plain := ansi.Strip(content)
	for _, escaped := range []string{`\x1B[2J`, `\u{202E}`, `\x1B]52`, `\x07`} {
		if !strings.Contains(plain, escaped) {
			t.Fatalf("status overlay omitted escaped evidence %q:\n%s", escaped, plain)
		}
	}
}
