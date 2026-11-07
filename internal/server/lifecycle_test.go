package server_test

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/server"
)

type finishDTO struct {
	Markdown  string `json:"markdown"`
	Error     string `json:"error"`
	Delivered bool   `json:"delivered"`
}

func draftExists(t *testing.T, f *fixture) bool {
	t.Helper()
	_, err := os.Stat(f.draftPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// assertStopped checks that the fixture's server no longer answers. A parallel test may already
// listen on the freed port, so only an answer that accepts this fixture's token counts.
func assertStopped(t *testing.T, f *fixture) {
	t.Helper()
	r, err := f.send(http.MethodGet, "/api/session", "")
	if err == nil && r.status == http.StatusOK {
		t.Errorf("%s still serves the session after Run returned", f.addr)
	}
}

// TestFinishDeliversThenReplies checks that finish delivers once, replies with the delivered
// Markdown (equal to what Copy gave), deletes the draft and ends Run.
func TestFinishDeliversThenReplies(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	done, _ := f.serveRun()
	f.mustStatus(f.createComment("c1", targetDTO{Target: "review"}, "redo it"), http.StatusOK, "comment")
	copied := f.do(http.MethodGet, "/api/result.md", "")
	f.mustStatus(copied, http.StatusOK, "result.md")
	if n := f.delivered.count(); n != 0 {
		t.Fatalf("delivered %d results before finish", n)
	}

	r := f.do(http.MethodPost, "/api/finish", "")
	f.mustStatus(r, http.StatusOK, "finish")
	var fin finishDTO
	decode(t, r.body, &fin)
	if !fin.Delivered || fin.Error != "" {
		t.Errorf("finish replied %+v, want delivered", fin)
	}
	if fin.Markdown != string(copied.body) {
		t.Errorf("finish Markdown %q differs from the copied result %q", fin.Markdown, copied.body)
	}

	res := waitRun(t, done)
	if res.outcome != server.Finished || res.err != nil {
		t.Errorf("Run = %v, %v; want Finished", res.outcome, res.err)
	}
	if n := f.delivered.count(); n != 1 {
		t.Errorf("delivered %d times, want once", n)
	} else if got := f.delivered.results[0]; len(got.Comments) != 1 {
		t.Errorf("delivered %+v", got)
	}
	if draftExists(t, f) {
		t.Error("the draft survived a delivered finish")
	}
	assertStopped(t, f)
}

// TestFinishEmptyReview: a review with no comments and no summary finishes, and its result is the
// title and source alone.
func TestFinishEmptyReview(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	r := f.do(http.MethodPost, "/api/finish", "")
	f.mustStatus(r, http.StatusOK, "finish")
	var fin finishDTO
	decode(t, r.body, &fin)
	if !fin.Delivered || !strings.HasPrefix(fin.Markdown, "# Review\n\nSource: ") || strings.Contains(fin.Markdown, "##") {
		t.Errorf("finish replied %+v; want delivered Markdown with only the title and source", fin)
	}
	if n := f.delivered.count(); n != 1 {
		t.Errorf("delivered %d results, want 1", n)
	}
}

// TestOneTerminalOutcome checks that after a finish, a second finish, a cancel and every mutation
// are refused, and nothing is delivered again.
func TestOneTerminalOutcome(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler() // without Run the server keeps answering after the session ends
	f.mustStatus(f.do(http.MethodPost, "/api/finish", ""), http.StatusOK, "first finish")

	for _, c := range []struct {
		method, path, body string
		opts               []reqOption
	}{
		{http.MethodPost, "/api/finish", "", nil},
		{http.MethodPost, "/api/cancel", "", nil},
		{http.MethodPut, "/api/summary", `{"summary":"late"}`, nil},
		{http.MethodPut, "/api/comments/late", `{"target":{"target":"review"},"body":"late"}`, []reqOption{createOnly}},
		{http.MethodPut, "/api/comments/late", `{"body":"late"}`, []reqOption{editAt(1)}},
		{http.MethodDelete, "/api/comments/late", "", []reqOption{editAt(1)}},
		{http.MethodPut, "/api/viewed", `{"files":[0],"viewed":true}`, nil},
	} {
		r := f.do(c.method, c.path, c.body, c.opts...)
		if r.status != http.StatusGone {
			t.Errorf("%s %s after finish: status %d, want 410", c.method, c.path, r.status)
		}
	}
	if n := f.delivered.count(); n != 1 {
		t.Errorf("delivered %d times, want once", n)
	}
	if draftExists(t, f) {
		t.Error("a refused late mutation recreated the draft")
	}
}

// TestCancelEndsRun checks that cancel deletes the draft, delivers nothing and Run returns
// Cancelled.
func TestCancelEndsRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	done, _ := f.serveRun()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"draft"}`), http.StatusNoContent, "summary")
	if !draftExists(t, f) {
		t.Fatal("no draft after a mutation")
	}
	f.mustStatus(f.do(http.MethodPost, "/api/cancel", ""), http.StatusNoContent, "cancel")
	res := waitRun(t, done)
	if res.outcome != server.Cancelled || res.err != nil {
		t.Errorf("Run = %v, %v; want Cancelled", res.outcome, res.err)
	}
	if draftExists(t, f) {
		t.Error("the draft survived cancel")
	}
	if n := f.delivered.count(); n != 0 {
		t.Errorf("cancel delivered %d results", n)
	}
	assertStopped(t, f)
}

// TestFinishRacesCancel checks that concurrent finish and cancel produce exactly one terminal
// outcome, and Run reports the one that won.
func TestFinishRacesCancel(t *testing.T) {
	t.Parallel()
	repo := smallRepo(t)
	for range 5 {
		f := newFixture(t, repo, fixtureOptions{})
		done, _ := f.serveRun()

		var wg sync.WaitGroup
		start := make(chan struct{})
		var finishResp, cancelResp response
		var finishErr, cancelErr error
		wg.Go(func() {
			<-start
			finishResp, finishErr = f.send(http.MethodPost, "/api/finish", "")
		})
		wg.Go(func() {
			<-start
			cancelResp, cancelErr = f.send(http.MethodPost, "/api/cancel", "")
		})
		close(start)
		wg.Wait()

		finished := finishErr == nil && finishResp.status == http.StatusOK
		cancelled := cancelErr == nil && cancelResp.status == http.StatusNoContent
		if finished == cancelled {
			t.Fatalf("finish (%d, %v) and cancel (%d, %v): want exactly one to succeed",
				finishResp.status, finishErr, cancelResp.status, cancelErr)
		}
		res := waitRun(t, done)
		want := server.Cancelled
		if finished {
			want = server.Finished
		}
		if res.outcome != want {
			t.Errorf("Run = %v, want %v", res.outcome, want)
		}
		if n, wantN := f.delivered.count(), map[bool]int{true: 1, false: 0}[finished]; n != wantN {
			t.Errorf("delivered %d times, want %d", n, wantN)
		}
	}
}

// TestDeliveryFailureKeepsDraft checks that a failed delivery is reported in the finish reply, Run
// returns DeliveryFailed and the draft is kept.
func TestDeliveryFailureKeepsDraft(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{deliverErr: errors.New("stdout closed")})
	done, _ := f.serveRun()
	// A saved draft, so the test can see the failed delivery keep it.
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"kept"}`), http.StatusNoContent, "summary")

	r := f.do(http.MethodPost, "/api/finish", "")
	f.mustStatus(r, http.StatusOK, "finish")
	var fin finishDTO
	decode(t, r.body, &fin)
	if fin.Delivered || fin.Markdown != "" || !strings.Contains(fin.Error, "stdout closed") {
		t.Errorf("finish replied %+v, want not delivered with the failure", fin)
	}
	res := waitRun(t, done)
	if res.outcome != server.DeliveryFailed {
		t.Errorf("Run = %v, want DeliveryFailed", res.outcome)
	}
	if !draftExists(t, f) {
		t.Error("the draft was deleted after a failed delivery")
	}
}

// postFinishAsync posts a finish and returns a channel that receives its reply.
func postFinishAsync(f *fixture) <-chan response {
	replied := make(chan response, 1)
	go func() {
		r, err := f.send(http.MethodPost, "/api/finish", "")
		if err != nil {
			r = response{status: -1, body: []byte(err.Error())}
		}
		replied <- r
	}()
	return replied
}

// TestSignalDuringAFailingDelivery: a signal during a delivery lets the delivery decide; one that
// then fails is a delivery failure, with the draft kept.
func TestSignalDuringAFailingDelivery(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFixture(t, smallRepo(t), fixtureOptions{deliverHook: func() error {
		close(entered)
		<-release
		return errors.New("stdout closed")
	}})
	done, stop := f.serveRun()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"kept"}`), http.StatusNoContent, "summary")
	replied := postFinishAsync(f)
	<-entered
	stop()
	close(release)
	if res := waitRun(t, done); res.outcome != server.DeliveryFailed {
		t.Errorf("Run = %v, %v; want DeliveryFailed", res.outcome, res.err)
	}
	if r := <-replied; r.status != http.StatusOK {
		t.Errorf("finish replied %d %s; want 200 reporting the failure", r.status, r.body)
	}
	if !draftExists(t, f) {
		t.Error("the draft was deleted after a failed delivery")
	}
}

// TestSignalAfterCancel: a signal after the session was cancelled reports the cancel, at once.
func TestSignalAfterCancel(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{finishWait: time.Hour})
	done, stop := f.serveRun()
	if err := f.sess.Cancel(); err != nil {
		t.Fatal(err)
	}
	stop()
	if res := waitRun(t, done); res.outcome != server.Cancelled {
		t.Errorf("Run = %v, %v; want Cancelled", res.outcome, res.err)
	}
}

// TestSignalDuringAStuckDelivery: a delivery that does not complete within the finish wait after a
// signal is a delivery failure: part of the result may already be written, so it is not an
// interruption. When the stuck write then completes, the finish reply, Run's outcome and the draft
// still agree that the review was not delivered, and a later run resumes the draft.
func TestSignalDuringAStuckDelivery(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFixture(t, smallRepo(t), fixtureOptions{finishWait: time.Millisecond, deliverHook: func() error {
		close(entered)
		<-release
		return nil
	}})
	done, stop := f.serveRun()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"kept"}`), http.StatusNoContent, "summary")
	replied := postFinishAsync(f)
	<-entered
	stop()
	// Run stops listening only after it settled the outcome, so the wait has run out by then.
	waitStoppedListening(t, f)
	close(release)

	r := recv(t, replied, "the finish reply")
	var fin finishDTO
	if r.status == http.StatusOK {
		decode(t, r.body, &fin)
	}
	if r.status != http.StatusOK || fin.Delivered || !strings.Contains(fin.Error, "abandoned") {
		t.Errorf("finish replied %d %s; want 200, not delivered because delivery was abandoned", r.status, r.body)
	}
	if res := waitRun(t, done); res.outcome != server.DeliveryFailed {
		t.Errorf("Run = %v, %v; want DeliveryFailed", res.outcome, res.err)
	}
	if got := resumedSummary(t, f); got != "kept" {
		t.Errorf("the next run resumed summary %q; want %q", got, "kept")
	}
}

// TestSignalDuringASucceedingDelivery: a delivery that completes within the finish wait after a
// signal decides the outcome: Finished, with the draft deleted.
func TestSignalDuringASucceedingDelivery(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFixture(t, smallRepo(t), fixtureOptions{finishWait: time.Hour, deliverHook: func() error {
		close(entered)
		<-release
		return nil
	}})
	done, stop := f.serveRun()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"sent"}`), http.StatusNoContent, "summary")
	replied := postFinishAsync(f)
	<-entered
	stop()
	close(release)
	if res := waitRun(t, done); res.outcome != server.Finished {
		t.Errorf("Run = %v, %v; want Finished", res.outcome, res.err)
	}
	r := recv(t, replied, "the finish reply")
	var fin finishDTO
	if r.status == http.StatusOK {
		decode(t, r.body, &fin)
	}
	if r.status != http.StatusOK || !fin.Delivered {
		t.Errorf("finish replied %d %s; want 200, delivered", r.status, r.body)
	}
	if draftExists(t, f) {
		t.Error("the draft survived a delivered result")
	}
}

// TestAbandonAfterFinish: Abandon after a delivered finish reports Finished.
func TestAbandonAfterFinish(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	f.mustStatus(f.do(http.MethodPost, "/api/finish", ""), http.StatusOK, "finish")
	if got := f.srv.Abandon(); got != server.Finished {
		t.Errorf("Abandon after a delivered finish = %v; want Finished", got)
	}
}

// TestAbandonAfterCancel: Abandon after a cancel reports Cancelled.
func TestAbandonAfterCancel(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	f.mustStatus(f.do(http.MethodPost, "/api/cancel", ""), http.StatusNoContent, "cancel")
	if got := f.srv.Abandon(); got != server.Cancelled {
		t.Errorf("Abandon after a cancel = %v; want Cancelled", got)
	}
}

// TestAbandonWhileIdle: Abandon on an idle session ends it as Interrupted: a later finish or
// cancel is refused and the draft stays.
func TestAbandonWhileIdle(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"keep"}`), http.StatusNoContent, "summary")
	if got := f.srv.Abandon(); got != server.Interrupted {
		t.Errorf("Abandon while idle = %v; want Interrupted", got)
	}
	f.mustStatus(f.do(http.MethodPost, "/api/finish", ""), http.StatusGone, "finish after Abandon")
	f.mustStatus(f.do(http.MethodPost, "/api/cancel", ""), http.StatusGone, "cancel after Abandon")
	if !draftExists(t, f) {
		t.Error("the draft was deleted after Abandon")
	}
}

// TestAbandonDuringABlockedDelivery: Abandon during a delivery reports DeliveryFailed at once;
// when the write then completes, the finish reply still says not delivered and the draft resumes.
func TestAbandonDuringABlockedDelivery(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFixture(t, smallRepo(t), fixtureOptions{finishWait: time.Hour, deliverHook: func() error {
		close(entered)
		<-release
		return nil
	}})
	f.serveHandler()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"kept"}`), http.StatusNoContent, "summary")
	replied := postFinishAsync(f)
	recv(t, entered, "the delivery")
	if got := f.srv.Abandon(); got != server.DeliveryFailed {
		t.Errorf("Abandon during a delivery = %v; want DeliveryFailed", got)
	}
	close(release)
	r := recv(t, replied, "the finish reply")
	var fin finishDTO
	if r.status == http.StatusOK {
		decode(t, r.body, &fin)
	}
	if r.status != http.StatusOK || fin.Delivered || !strings.Contains(fin.Error, "abandoned") {
		t.Errorf("finish replied %d %s; want 200, not delivered because delivery was abandoned", r.status, r.body)
	}
	if got := resumedSummary(t, f); got != "kept" {
		t.Errorf("the next run resumed summary %q; want %q", got, "kept")
	}
}

// TestAbandonAfterAFailedDelivery: Abandon after a failed delivery reports DeliveryFailed and the
// draft stays.
func TestAbandonAfterAFailedDelivery(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{deliverHook: func() error { return errors.New("stdout closed") }})
	f.serveHandler()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"kept"}`), http.StatusNoContent, "summary")
	f.mustStatus(f.do(http.MethodPost, "/api/finish", ""), http.StatusOK, "finish")
	if got := f.srv.Abandon(); got != server.DeliveryFailed {
		t.Errorf("Abandon after a failed delivery = %v; want DeliveryFailed", got)
	}
	if !draftExists(t, f) {
		t.Error("the draft was deleted after a failed delivery")
	}
}

func recv[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// waitStoppedListening waits until the fixture's server no longer answers with this fixture's
// session.
func waitStoppedListening(t *testing.T, f *fixture) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := f.send(http.MethodGet, "/api/session", "")
		if err != nil || r.status != http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the server still answers after Run was stopped")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// resumedSummary starts a new session from a copy of the fixture's draft, as the next run would,
// and returns its summary.
func resumedSummary(t *testing.T, f *fixture) string {
	t.Helper()
	data, err := os.ReadFile(f.draftPath)
	if err != nil {
		t.Fatalf("read the kept draft: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(f.draftPath)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSuffix(filepath.Base(f.draftPath), ".json")
	store, err := review.OpenStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	s, o, err := review.Start(t.Context(), store, f.snap, review.Fresh{Seed: review.Seed{}})
	if err != nil {
		t.Fatalf("start from the kept draft: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if !o.Resumed() {
		t.Fatal("the next run did not resume the kept draft")
	}
	return s.Draft().Summary
}

// TestInterruptWhileIdle checks that ending Run's context while nothing is in flight returns
// Interrupted, keeps the draft and stops listening.
func TestInterruptWhileIdle(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	done, stop := f.serveRun()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"keep"}`), http.StatusNoContent, "summary")
	stop()
	res := waitRun(t, done)
	if res.outcome != server.Interrupted {
		t.Errorf("Run = %v, %v; want Interrupted", res.outcome, res.err)
	}
	if !draftExists(t, f) {
		t.Error("the draft was deleted on interruption")
	}
	if n := f.delivered.count(); n != 0 {
		t.Errorf("interruption delivered %d results", n)
	}
	assertStopped(t, f)
}

// failingListener is a listener whose Accept fails with a permanent error.
type failingListener struct{ err error }

func (l failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (failingListener) Close() error                { return nil }
func (failingListener) Addr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }

// TestServeFailureEndsWithDraftKept: a server that stops serving on its own is a failure, not an
// interrupt, and the session can no longer be changed while the draft stays for a later run.
func TestServeFailureEndsWithDraftKept(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	if err := f.sess.SetSummary("keep"); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	done := make(chan runResult, 1)
	go func() {
		o, err := f.srv.Run(t.Context(), failingListener{err: boom})
		done <- runResult{outcome: o, err: err}
	}()
	r := waitRun(t, done)
	if r.outcome != server.Failed || !errors.Is(r.err, boom) {
		t.Errorf("Run = %v, %v; want Failed wrapping boom", r.outcome, r.err)
	}
	if n := f.delivered.count(); n != 0 {
		t.Errorf("delivered %d times; want none", n)
	}
	if err := f.sess.SetSummary("x"); !errors.Is(err, review.ErrEnded) {
		t.Errorf("a mutation after the server failed: %v; want ErrEnded", err)
	}
	if !draftExists(t, f) {
		t.Error("the draft was deleted")
	}
}

// TestQuoteStatesBytesCutFromAHugeLine checks end to end that a quoted line longer than the
// large-file limit is cut to the quote limit, and the result states every byte not shown.
func TestQuoteStatesBytesCutFromAHugeLine(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.commit("base")
	huge := 3 << 20
	repo.write("huge.txt", strings.Repeat("x", huge)+"\ny\n")
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	f.mustStatus(f.createComment("c1", targetDTO{Target: "line", Path: "huge.txt", Side: "new", Line: 1}, "long"), http.StatusOK, "comment")
	r := f.do(http.MethodGet, "/api/result.md", "")
	f.mustStatus(r, http.StatusOK, "result.md")
	md := string(r.body)
	if !strings.Contains(md, "\n"+strings.Repeat("x", review.MaxQuoteLineBytes)+"\n") {
		t.Errorf("result does not quote exactly %d bytes of the line", review.MaxQuoteLineBytes)
	}
	if want := fmt.Sprintf("%d bytes cut from long lines", huge-review.MaxQuoteLineBytes); !strings.Contains(md, want) {
		t.Errorf("result does not state %q:\n%.300s", want, md[strings.LastIndex(md, "x")+1:])
	}
}

// Close graces for the page-closed tests. Tests that wait for PageClosed use the short one; tests
// that check the session survives use the long one and send their request at once. No test
// depends on the exact edge of the grace.
const (
	shortGrace = 100 * time.Millisecond
	longGrace  = 500 * time.Millisecond
)

// postPageClosed sends the page's close notice and requires the 204.
func (f *fixture) postPageClosed() {
	f.t.Helper()
	f.mustStatus(f.do(http.MethodPost, "/api/page-closed", ""), http.StatusNoContent, "page-closed")
}

// assertStillRunning fails the test if Run returns within d. It sends no request, since any
// request counts as a page's presence.
func assertStillRunning(t *testing.T, done <-chan runResult, d time.Duration, what string) {
	t.Helper()
	select {
	case r := <-done:
		t.Fatalf("%s: Run returned %v, %v; want it still serving", what, r.outcome, r.err)
	case <-time.After(d):
	}
}

// TestPageClosedEndsRun: a close notice followed by no request ends Run as PageClosed with the
// saved draft kept and nothing delivered, and Abandon then agrees.
func TestPageClosedEndsRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{closeGrace: shortGrace})
	done, _ := f.serveRun()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"kept"}`), http.StatusNoContent, "summary")
	f.postPageClosed()
	if res := waitRun(t, done); res.outcome != server.PageClosed || res.err != nil {
		t.Errorf("Run = %v, %v; want PageClosed", res.outcome, res.err)
	}
	if n := f.delivered.count(); n != 0 {
		t.Errorf("delivered %d results; want none", n)
	}
	if got := resumedSummary(t, f); got != "kept" {
		t.Errorf("the next run resumed summary %q; want %q", got, "kept")
	}
	if got := f.srv.Abandon(); got != server.PageClosed {
		t.Errorf("Abandon after the page closed = %v; want PageClosed", got)
	}
}

// TestRequestAfterPageClosedKeepsSession: a request after the close notice, such as a reload's or
// another tab's, keeps the session.
func TestRequestAfterPageClosedKeepsSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{closeGrace: longGrace})
	done, _ := f.serveRun()
	f.postPageClosed()
	f.mustStatus(f.do(http.MethodGet, "/api/status", ""), http.StatusOK, "status")
	assertStillRunning(t, done, 3*longGrace, "a request followed the notice")
}

// TestPageClosedDuringAStuckDelivery: a close notice during a delivery that does not complete
// within the finish wait is a delivery failure, and the draft is kept.
func TestPageClosedDuringAStuckDelivery(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFixture(t, smallRepo(t), fixtureOptions{closeGrace: shortGrace, finishWait: time.Millisecond, deliverHook: func() error {
		close(entered)
		<-release
		return nil
	}})
	done, _ := f.serveRun()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"kept"}`), http.StatusNoContent, "summary")
	replied := postFinishAsync(f)
	recv(t, entered, "the delivery")
	f.postPageClosed()
	// Run stops listening only after it settled the outcome. The probe has no token, so it is no
	// page's presence.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := f.send(http.MethodGet, "/api/session", "", withoutToken()); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server still listens after the close grace")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	recv(t, replied, "the finish reply")
	if res := waitRun(t, done); res.outcome != server.DeliveryFailed {
		t.Errorf("Run = %v, %v; want DeliveryFailed", res.outcome, res.err)
	}
	if got := resumedSummary(t, f); got != "kept" {
		t.Errorf("the next run resumed summary %q; want %q", got, "kept")
	}
}

// TestPageClosedDuringASucceedingDelivery: a delivery that completes within the finish wait after
// the close grace ran out decides the outcome: Finished, with the draft deleted.
func TestPageClosedDuringASucceedingDelivery(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFixture(t, smallRepo(t), fixtureOptions{closeGrace: shortGrace, finishWait: time.Hour, deliverHook: func() error {
		close(entered)
		<-release
		return nil
	}})
	done, _ := f.serveRun()
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"sent"}`), http.StatusNoContent, "summary")
	replied := postFinishAsync(f)
	recv(t, entered, "the delivery")
	f.postPageClosed()
	// Nothing observable marks the grace's end while the delivery holds the session, so wait
	// past it before releasing the delivery.
	time.Sleep(3 * shortGrace)
	close(release)
	if res := waitRun(t, done); res.outcome != server.Finished {
		t.Errorf("Run = %v, %v; want Finished", res.outcome, res.err)
	}
	if r := recv(t, replied, "the finish reply"); r.status != http.StatusOK {
		t.Errorf("finish replied %d %s; want 200", r.status, r.body)
	}
	if draftExists(t, f) {
		t.Error("the draft survived a delivered result")
	}
}

// TestCancelAfterPageClosed: a cancel after the close notice ends Run as Cancelled.
func TestCancelAfterPageClosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{closeGrace: longGrace})
	done, _ := f.serveRun()
	f.postPageClosed()
	f.mustStatus(f.do(http.MethodPost, "/api/cancel", ""), http.StatusNoContent, "cancel")
	if res := waitRun(t, done); res.outcome != server.Cancelled {
		t.Errorf("Run = %v, %v; want Cancelled", res.outcome, res.err)
	}
}

// TestTwoPageClosedNotices: two notices, as from two closing tabs, end the session once.
func TestTwoPageClosedNotices(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{closeGrace: shortGrace})
	done, _ := f.serveRun()
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if r, err := f.send(http.MethodPost, "/api/page-closed", ""); err != nil || r.status != http.StatusNoContent {
				t.Errorf("page-closed: %d, %v; want 204", r.status, err)
			}
		})
	}
	wg.Wait()
	if res := waitRun(t, done); res.outcome != server.PageClosed {
		t.Errorf("Run = %v, %v; want PageClosed", res.outcome, res.err)
	}
	select {
	case r := <-done:
		t.Errorf("Run reported a second outcome %v", r.outcome)
	default:
	}
}

// TestForgedPageClosedNotices: a notice that fails the token, Host or cross-origin checks starts
// no grace; a genuine one afterwards does.
func TestForgedPageClosedNotices(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{closeGrace: shortGrace})
	done, _ := f.serveRun()
	for _, c := range []struct {
		name string
		opt  reqOption
		want int
	}{
		{"without token", withoutToken(), http.StatusUnauthorized},
		{"cross-site Sec-Fetch-Site", withSecFetchSite("cross-site"), http.StatusForbidden},
		{"foreign Origin", withOrigin("http://evil.example"), http.StatusForbidden},
		{"foreign Host", withForeignHost(), http.StatusMisdirectedRequest},
	} {
		f.mustStatus(f.do(http.MethodPost, "/api/page-closed", "", c.opt), c.want, c.name)
		assertStillRunning(t, done, 5*shortGrace, "notice "+c.name)
	}
	f.postPageClosed()
	if res := waitRun(t, done); res.outcome != server.PageClosed {
		t.Errorf("Run after a genuine notice = %v, %v; want PageClosed", res.outcome, res.err)
	}
}

// TestUnauthorisedRequestIsNoPresence: a request that fails the token check after the notice
// does not keep the session.
func TestUnauthorisedRequestIsNoPresence(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{closeGrace: shortGrace})
	done, _ := f.serveRun()
	f.postPageClosed()
	f.mustStatus(f.do(http.MethodGet, "/api/status", "", withoutToken()), http.StatusUnauthorized, "status without token")
	if res := waitRun(t, done); res.outcome != server.PageClosed {
		t.Errorf("Run = %v, %v; want PageClosed", res.outcome, res.err)
	}
}

// TestPageClosedAfterCancel: a notice after the session ended is answered 204 and changes nothing.
func TestPageClosedAfterCancel(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{closeGrace: shortGrace})
	f.serveHandler()
	f.mustStatus(f.do(http.MethodPost, "/api/cancel", ""), http.StatusNoContent, "cancel")
	f.postPageClosed()
	time.Sleep(3 * shortGrace)
	if got := f.srv.Abandon(); got != server.Cancelled {
		t.Errorf("Abandon after a cancel and a notice = %v; want Cancelled", got)
	}
}

// TestStatusInterval: the session tells the UI to poll often enough that a sibling page's poll
// falls within the close grace, and within REQ-SES-010's 10 seconds.
func TestStatusInterval(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		grace time.Duration // Config.CloseGrace
		bound time.Duration // the grace it means, capped at 10 s
	}{
		{"default grace", 0, 10 * time.Second},
		{"short grace", shortGrace, shortGrace},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, smallRepo(t), fixtureOptions{closeGrace: c.grace})
			f.serveHandler()
			v := time.Duration(f.session().StatusIntervalMs) * time.Millisecond
			if v <= 0 || 2*v > c.bound {
				t.Errorf("status interval %v; want positive and at most half of %v", v, c.bound)
			}
		})
	}
}
