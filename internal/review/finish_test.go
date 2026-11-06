package review_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
)

// The tests in this file observe the session while a finish fails or delivers, and while a
// comment's validation runs git.

const wait = 10 * time.Second

// startQuoting starts a session whose result quotes one line.
func startQuoting(t *testing.T) (*review.Session, *review.Store, *fixture) {
	t.Helper()
	fx := newFixture(t)
	s, st := startQuotingIn(t, fx)
	return s, st, fx
}

// startQuotingIn starts a session in fx, with a draft of its own, whose result quotes one line.
func startQuotingIn(t *testing.T, fx *fixture) (*review.Session, *review.Store) {
	t.Helper()
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "c1", on(t, s, line(pathA, review.New, 2)), "quoted"); err != nil {
		t.Fatal(err)
	}
	return s, st
}

func recv[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(wait):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// counter is a deliver func that counts its calls.
type counter struct{ n atomic.Int32 }

func (c *counter) deliver(review.Result) error { c.n.Add(1); return nil }

// A build that fails delivers nothing, keeps the draft and reopens the session.
func TestFailedBuildReopensTheSession(t *testing.T) {
	t.Parallel()
	t.Run("quote error", func(t *testing.T) {
		t.Parallel()
		s, st, fx := startQuoting(t)
		restore := fx.hideBlob(t, "HEAD:"+pathA)
		var d counter
		if err := second(s.Finish(t.Context(), d.deliver)); err == nil {
			t.Fatal("Finish with the quoted blob missing = nil; want an error")
		}
		if n := d.n.Load(); n != 0 || !fileExists(t, st.Path()) {
			t.Fatalf("after a failed build: delivered %d times, draft kept %v; want 0 and true", n, fileExists(t, st.Path()))
		}
		if err := s.SetSummary("x"); err != nil {
			t.Fatalf("a mutation after a failed build: %v", err)
		}
		restore()
		if err := second(s.Finish(t.Context(), d.deliver)); err != nil {
			t.Fatalf("second Finish: %v", err)
		}
		if n := d.n.Load(); n != 1 || fileExists(t, st.Path()) {
			t.Errorf("after the second Finish: delivered %d times, draft kept %v; want 1 and false", n, fileExists(t, st.Path()))
		}
	})
	t.Run("caller cancelled", func(t *testing.T) {
		t.Parallel()
		s, st, _ := startQuoting(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var d counter
		if err := second(s.Finish(ctx, d.deliver)); !errors.Is(err, context.Canceled) {
			t.Fatalf("Finish = %v; want context.Canceled", err)
		}
		if n := d.n.Load(); n != 0 || !fileExists(t, st.Path()) {
			t.Fatalf("after a cancelled build: delivered %d times, draft kept %v; want 0 and true", n, fileExists(t, st.Path()))
		}
		if err := s.SetSummary("x"); err != nil {
			t.Errorf("a mutation after a cancelled build: %v", err)
		}
		if err := second(s.Finish(t.Context(), d.deliver)); err != nil {
			t.Fatalf("second Finish: %v", err)
		}
		if n := d.n.Load(); n != 1 {
			t.Errorf("delivered %d times; want once", n)
		}
	})
}

// Once delivery starts the session has ended: every mutation is rejected and the delivered result
// is the draft as it was when Finish was called.
func TestDeliveringRejectsMutations(t *testing.T) {
	t.Parallel()
	s, _, _ := startQuoting(t)
	before := s.View()
	entered, release := make(chan review.Result, 1), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- second(s.Finish(t.Context(), func(r review.Result) error {
			entered <- r
			<-release
			return nil
		}))
	}()
	r := recv(t, entered, "delivery")

	ctx := t.Context()
	var d counter
	for name, err := range map[string]error{
		"CreateComment": second(s.CreateComment(ctx, "late", on(t, s, reviewTarget), "x")),
		"EditComment":   second(s.EditComment("c1", versionOf(t, s, "c1"), "x")),
		"DeleteComment": s.DeleteComment("c1", 1),
		"SetViewed":     s.SetViewed(0, true),
		"SetSummary":    s.SetSummary("late"),
		"Cancel":        s.Cancel(),
		"Finish":        second(s.Finish(ctx, d.deliver)),
	} {
		if !errors.Is(err, review.ErrEnded) {
			t.Errorf("%s while delivering: err = %v; want ErrEnded", name, err)
		}
	}
	if got := s.View(); !reflect.DeepEqual(got, before) {
		t.Errorf("View while delivering = %+v; want %+v", got, before)
	}
	close(release)
	if err := recv(t, finished, "Finish"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if n := d.n.Load(); n != 0 {
		t.Errorf("a second Finish delivered %d times", n)
	}
	if r.Summary != "" || len(r.Comments) != 1 {
		t.Errorf("result has summary %q, %d comments; want the pre-finish draft", r.Summary, len(r.Comments))
	}
}

// gatedGit puts a git shim first on PATH. While the gate file exists, every patch command (one
// with a standalone -p) and every cat-file start appends a line to the log and waits until the
// gate is removed; every other command runs the real git at once. The gate starts open; hold
// creates it.
//
// It sets PATH for the whole process, so its tests cannot run in parallel.
type gatedGit struct {
	t         *testing.T
	gate, log string
}

func newGatedGit(t *testing.T) *gatedGit {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	g := &gatedGit{t: t, gate: filepath.Join(dir, "gate"), log: filepath.Join(dir, "log")}
	script := "#!/bin/sh\n" +
		"case \" $* \" in *\" -p \"*|*\" cat-file \"*)\n" +
		"  if [ -e '" + g.gate + "' ]; then\n" +
		"    echo x >> '" + g.log + "'\n" +
		"    while [ -e '" + g.gate + "' ]; do sleep 0.05; done\n" +
		"  fi;;\n" +
		"esac\n" +
		"exec '" + gitPath + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(g.release)
	return g
}

func (g *gatedGit) hold() {
	g.t.Helper()
	if err := os.WriteFile(g.gate, nil, 0o600); err != nil {
		g.t.Fatal(err)
	}
}

func (g *gatedGit) release() {
	if err := os.Remove(g.gate); err != nil && !os.IsNotExist(err) {
		g.t.Error(err)
	}
}

// waitHeld waits until n patch commands have reached the gate.
func (g *gatedGit) waitHeld(n int) {
	g.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		data, err := os.ReadFile(g.log)
		if err != nil && !os.IsNotExist(err) {
			g.t.Fatal(err)
		}
		held := strings.Count(string(data), "\n")
		if held >= n {
			return
		}
		if time.Now().After(deadline) {
			g.t.Fatalf("%d patch commands reached the gate; want %d", held, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// within runs fn and fails the test if it does not return in time, naming the call that blocked.
func within[T any](t *testing.T, name string, fn func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- fn() }()
	return recv(t, done, name+" while a comment's validation is held")
}

// TestValidationDoesNotHoldTheSession covers plan item E: validating a line comment runs git, so
// it runs without the session lock; other mutations, View and End answer meanwhile, and a
// validation that outlasts the session does not commit.
//
// Not parallel: gatedGit sets PATH for the process.
func TestValidationDoesNotHoldTheSession(t *testing.T) {
	g := newGatedGit(t)
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	g.hold()
	held := make(chan error, 1)
	go func() {
		_, err := s.CreateComment(t.Context(), "held", on(t, s, line(pathA, review.New, 2)), "late")
		held <- err
	}()
	g.waitHeld(1)

	// None of these runs a patch command, so none waits at the gate unless it waits for the lock.
	if err := within(t, "SetSummary", func() error { return s.SetSummary("sum") }); err != nil {
		t.Errorf("SetSummary: %v", err)
	}
	if err := within(t, "CreateComment", func() error {
		_, err := s.CreateComment(t.Context(), "r1", on(t, s, reviewTarget), "x")
		return err
	}); err != nil {
		t.Errorf("CreateComment: %v", err)
	}
	want := within(t, "View", s.View)
	if want.Summary != "sum" || len(want.Comments) != 1 || want.Comments[0].ID != "r1" {
		t.Errorf("View while a validation is held = %+v; want summary and comment r1", want)
	}
	if got := within(t, "End", s.End); got != review.EndedByEnd {
		t.Errorf("End = %v; want EndedByEnd", got)
	}

	g.release()
	if err := recv(t, held, "the held CreateComment"); !errors.Is(err, review.ErrEnded) {
		t.Errorf("CreateComment whose validation outlasted the session = %v; want ErrEnded", err)
	}
	if got := s.View(); !reflect.DeepEqual(got, want) {
		t.Errorf("View after the held CreateComment = %+v; want %+v", got, want)
	}
	assertDraftRestores(t, fx, st, want)
}

// A draft that cannot be deleted does not undo a delivered result: Finish succeeds, reports the
// failed delete as a warning, and the session stays ended.
func TestFinishWithAnUndeletableDraft(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	fx := newFixture(t)
	dir := t.TempDir()
	s, st := fx.start(t, dir, review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "c1", on(t, s, reviewTarget), "delivered"); err != nil {
		t.Fatal(err)
	}
	if !fileExists(t, st.Path()) {
		t.Fatal("no draft after a comment")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	// Registered after t.TempDir, so it runs before the directory is removed.
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})

	var delivered []review.Result
	d, err := s.Finish(t.Context(), func(r review.Result) error {
		delivered = append(delivered, r)
		return nil
	})
	if err != nil {
		t.Fatalf("Finish: %v; want nil", err)
	}
	if !errors.Is(d.DraftErr, fs.ErrPermission) {
		t.Errorf("DraftErr = %v; want a permission error", d.DraftErr)
	}
	if len(delivered) != 1 || len(delivered[0].Comments) != 1 || delivered[0].Comments[0].Body != "delivered" {
		t.Errorf("delivered %+v; want one result with the comment", delivered)
	}
	if !fileExists(t, st.Path()) {
		t.Error("the draft is gone, yet its delete reported a failure")
	}
	if err := s.SetSummary("late"); !errors.Is(err, review.ErrEnded) {
		t.Errorf("SetSummary after Finish: err = %v; want ErrEnded", err)
	}
	if _, err := s.CreateComment(t.Context(), "late", on(t, s, reviewTarget), "x"); !errors.Is(err, review.ErrEnded) {
		t.Errorf("CreateComment after Finish: err = %v; want ErrEnded", err)
	}
}

// finishBlocked starts a Finish whose deliver waits for release and returns its result, and waits
// until delivery has started.
func finishBlocked(t *testing.T, s *review.Session, result error) (release func(), finished <-chan error) {
	t.Helper()
	entered, gate, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- second(s.Finish(t.Context(), func(review.Result) error {
			close(entered)
			<-gate
			return result
		}))
	}()
	recv(t, entered, "delivery")
	return func() { close(gate) }, done
}

// assertEndedKeepingDraft checks that every mutation, Finish and Cancel answer ErrEnded, that
// nothing is delivered, and that the draft at path still holds want.
func assertEndedKeepingDraft(t *testing.T, s *review.Session, path, want string) {
	t.Helper()
	var d counter
	if err := second(s.Finish(t.Context(), d.deliver)); !errors.Is(err, review.ErrEnded) {
		t.Errorf("Finish after Abandon = %v; want ErrEnded", err)
	}
	if n := d.n.Load(); n != 0 {
		t.Errorf("Finish after Abandon delivered %d times; want 0", n)
	}
	if err := s.SetSummary("late"); !errors.Is(err, review.ErrEnded) {
		t.Errorf("SetSummary after Abandon = %v; want ErrEnded", err)
	}
	if _, err := s.CreateComment(t.Context(), "late", on(t, s, reviewTarget), "x"); !errors.Is(err, review.ErrEnded) {
		t.Errorf("CreateComment after Abandon = %v; want ErrEnded", err)
	}
	if err := s.Cancel(); !errors.Is(err, review.ErrEnded) {
		t.Errorf("Cancel after Abandon = %v; want ErrEnded", err)
	}
	if got := readFile(t, path); got != want {
		t.Errorf("draft after Abandon = %q; want it unchanged, %q", got, want)
	}
}

// abandonTwice calls Abandon twice and checks that both calls report want.
func abandonTwice(t *testing.T, s *review.Session, want review.Fate) {
	t.Helper()
	for k := range 2 {
		if got := s.Abandon(); got != want {
			t.Errorf("Abandon call %d = %v; want %v", k+1, got, want)
		}
	}
}

// Abandoning an open session ends it as interrupted: nothing is delivered and the draft is kept.
func TestAbandonAnOpenSession(t *testing.T) {
	s, st, _ := startQuoting(t)
	draft := readFile(t, st.Path())
	abandonTwice(t, s, review.FateInterrupted)
	assertEndedKeepingDraft(t, s, st.Path(), draft)
}

// Abandon while the result is being built cancels the build: the finish delivers nothing and the
// session ends as interrupted. The gate holds the build's first blob read, so Abandon lands in
// that window deterministically.
//
// Not parallel: gatedGit sets PATH for the process.
func TestAbandonWhileTheResultIsBuilt(t *testing.T) {
	g := newGatedGit(t)
	s, st, fx := startQuoting(t)
	// A failed blob read discards the object reader, so the build below starts a fresh cat-file,
	// which the gate holds.
	restore := fx.hideBlob(t, "HEAD:"+pathA)
	if _, err := s.Result(t.Context()); err == nil {
		t.Fatal("Result with the quoted blob missing = nil; want an error")
	}
	restore()
	draft := readFile(t, st.Path())
	g.hold()
	var d counter
	finished := make(chan error, 1)
	go func() { finished <- second(s.Finish(t.Context(), d.deliver)) }()
	g.waitHeld(1)

	abandonTwice(t, s, review.FateInterrupted)
	g.release()
	if err := recv(t, finished, "Finish"); !errors.Is(err, review.ErrEnded) {
		t.Errorf("Finish whose build was abandoned = %v; want ErrEnded", err)
	}
	if n := d.n.Load(); n != 0 {
		t.Errorf("an abandoned build delivered %d times; want 0", n)
	}
	abandonTwice(t, s, review.FateInterrupted)
	assertEndedKeepingDraft(t, s, st.Path(), draft)
}

// A delivery abandoned while in progress never counts as delivered: Finish reports ErrAbandoned
// and keeps the draft even when deliver then succeeds, so the caller's "not delivered" stays true.
func TestAbandonedDelivery(t *testing.T) {
	s, st, _ := startQuoting(t)
	release, finished := finishBlocked(t, s, nil)
	abandonTwice(t, s, review.FateUndelivered)
	if got := s.End(); got != review.EndedByFinish {
		t.Errorf("End after an abandoned delivery = %v; want EndedByFinish", got)
	}
	release()
	err := recv(t, finished, "Finish")
	if de, ok := errors.AsType[*review.DeliveryError](err); !ok || !errors.Is(de, review.ErrAbandoned) {
		t.Errorf("Finish after its delivery was abandoned = %v; want a *DeliveryError with ErrAbandoned", err)
	}
	if !fileExists(t, st.Path()) {
		t.Error("an abandoned delivery deleted the draft")
	}
	abandonTwice(t, s, review.FateUndelivered)
}

// An abandoned delivery that also failed reports both causes.
func TestAbandonedDeliveryThatFailed(t *testing.T) {
	s, st, _ := startQuoting(t)
	broken := errors.New("stdout closed")
	release, finished := finishBlocked(t, s, broken)
	abandonTwice(t, s, review.FateUndelivered)
	release()
	err := recv(t, finished, "Finish")
	if de, ok := errors.AsType[*review.DeliveryError](err); !ok || !errors.Is(de, review.ErrAbandoned) || !errors.Is(de, broken) {
		t.Errorf("Finish = %v; want a *DeliveryError with ErrAbandoned and the deliver error", err)
	}
	if !fileExists(t, st.Path()) {
		t.Error("a failed, abandoned delivery deleted the draft")
	}
}

// A session that ended before Abandon is reported as it ended, and left as it was.
func TestAbandonAfterTheSessionEnded(t *testing.T) {
	t.Run("delivered", func(t *testing.T) {
		s, st, _ := startQuoting(t)
		var d counter
		if err := second(s.Finish(t.Context(), d.deliver)); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		abandonTwice(t, s, review.FateDelivered)
		if n := d.n.Load(); n != 1 {
			t.Errorf("delivered %d times; want once", n)
		}
		if fileExists(t, st.Path()) {
			t.Error("the draft survived a delivery")
		}
	})
	t.Run("failed", func(t *testing.T) {
		s, st, _ := startQuoting(t)
		broken := errors.New("stdout closed")
		if err := second(s.Finish(t.Context(), func(review.Result) error { return broken })); !errors.Is(err, broken) {
			t.Fatalf("Finish = %v; want the deliver error", err)
		}
		abandonTwice(t, s, review.FateUndelivered)
		if !fileExists(t, st.Path()) {
			t.Error("a failed delivery deleted the draft")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		s, st, _ := startQuoting(t)
		if err := s.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		abandonTwice(t, s, review.FateCancelled)
		if fileExists(t, st.Path()) {
			t.Error("the draft survived a cancel")
		}
	})
}

// However Abandon interleaves with a finish, the fate it reports agrees with what the finish did
// and with the draft.
func TestAbandonRacingFinish(t *testing.T) {
	fx := newFixture(t)
	for k := range 20 {
		s, st := startQuotingIn(t, fx)
		var d counter
		finished := make(chan error, 1)
		go func() { finished <- second(s.Finish(t.Context(), d.deliver)) }()
		fate := s.Abandon()
		err := recv(t, finished, "Finish")
		n, kept := d.n.Load(), fileExists(t, st.Path())
		var ok bool
		switch fate {
		case review.FateInterrupted:
			ok = errors.Is(err, review.ErrEnded) && n == 0 && kept
		case review.FateUndelivered:
			de, isDE := errors.AsType[*review.DeliveryError](err)
			ok = isDE && errors.Is(de, review.ErrAbandoned) && kept
		case review.FateDelivered:
			ok = err == nil && n == 1 && !kept
		case review.FateCancelled:
		}
		if !ok {
			t.Errorf("iteration %d: fate %v, but Finish = %v, delivered %d times, draft kept %v", k, fate, err, n, kept)
		}
	}
}
