//go:build darwin

// Command rvw is the human checkpoint in an agent loop: it serves a review UI for one change and
// prints the human's review on stdout when they finish.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/round"
	"github.com/jacoelho/rvw/internal/server"
	"github.com/jacoelho/rvw/internal/source"
	"github.com/jacoelho/rvw/web"
)

// Exit statuses.
const (
	exitOK        = 0
	exitFailure   = 1
	exitUsage     = 2
	exitCancelled = 3
	// exitPageClosed: the review page was closed; the saved draft is kept (REQ-SES-012).
	exitPageClosed = 4
	exitSIGINT     = 130
	exitSIGTERM    = 143
)

const openTimeout = 10 * time.Second

// recordTimeout bounds recording a round after delivery: the ledger lock wait and the git calls
// that keep and prune retention refs.
const recordTimeout = 10 * time.Second

func main() {
	// Without this, a write to a closed stdout pipe kills the process before deliver can report
	// the failure and keep the draft.
	signal.Ignore(syscall.SIGPIPE)
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	msg := newLogger(stderr)
	cmd, err := parseArgs(args)
	if err != nil {
		msg.Error("invalid command line", "err", err)
		// Usage help is plain text after the record; the exit status says the command line was
		// invalid whether or not it was written.
		if _, werr := io.WriteString(stderr, "\n"+usage); werr != nil {
			msg.Error("write usage help", "err", werr)
		}
		return exitUsage
	}
	if cmd.help {
		if _, werr := io.WriteString(stderr, usage); werr != nil {
			msg.Error("write usage help", "err", werr)
			return exitFailure
		}
		return exitOK
	}

	// Signals cancel the session context; the received signal decides the exit status. The buffer
	// holds two, so a second signal that arrives before the first is read is not dropped (Q-45).
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	// current is the running review, once its server exists. It is never cleared, so a second
	// signal after a delivered review still reports the delivery.
	var current atomic.Pointer[live]
	var received os.Signal
	sigDone := make(chan struct{})
	go func() {
		select {
		case received = <-sigs:
			cancel()
			close(sigDone)
		case <-ctx.Done():
			close(sigDone)
			return
		}
		// The first signal lets a delivery in progress decide the outcome, within the server's
		// finish wait. A second signal stops rvw at once: it asks the server, which asks the session
		// (Abandon), so the exit status follows the session's own fate, reported once through live.
		// Before the server exists no finish or cancel can have run, so the first signal's status
		// holds.
		select {
		case <-sigs:
			code := signalCode(received)
			if l := current.Load(); l != nil {
				o := l.srv.Abandon()
				cause := ""
				if o == server.DeliveryFailed {
					cause = "a second signal ended the session before the review result was delivered"
				}
				if c := l.report(o, cause, msg); c != exitInterrupted {
					code = c
				}
			}
			os.Exit(code)
		case <-stop:
		}
	}()
	signalExit := func() int {
		cancel()
		<-sigDone
		return signalCode(received)
	}

	code := session(ctx, cmd, stdin, stdout, msg, &current)
	if code == exitInterrupted {
		return signalExit()
	}
	return code
}

func signalCode(sig os.Signal) int {
	if sig == syscall.SIGTERM {
		return exitSIGTERM
	}
	return exitSIGINT
}

// exitInterrupted is session's internal marker for an interrupted session.
const exitInterrupted = -1

// live is the running review a second signal settles. report writes the outcome to stderr once,
// whether main or the second signal gets there first, and returns the exit status.
type live struct {
	srv  *server.Server
	sess *review.Session // its draft path follows a refresh
	once sync.Once
	code int
}

func (l *live) report(o server.Outcome, cause string, msg *slog.Logger) int {
	l.once.Do(func() {
		if cause != "" {
			msg.Error(cause)
		}
		l.code = exitFor(o, l.sess.DraftPath(), msg)
	})
	return l.code
}

func session(ctx context.Context, cmd command, stdin io.Reader, stdout io.Writer, msg *slog.Logger, current *atomic.Pointer[live]) int {
	// fail ends a startup step. A signal that cut the step short is reported as the signal, not
	// as the step's failure.
	fail := func(err error) int {
		msg.Error("cannot start the review", "err", err)
		if ctx.Err() != nil {
			return exitInterrupted
		}
		return exitFailure
	}
	wd, err := os.Getwd()
	if err != nil {
		return fail(err)
	}
	repo, err := source.Open(ctx, wd)
	if err != nil {
		return fail(err)
	}
	defer func() {
		if closeErr := repo.Close(); closeErr != nil {
			msg.Warn("close repository", "err", closeErr)
		}
	}()
	src, err := resolveRequest(ctx, repo, cmd.request)
	if err != nil {
		return fail(err)
	}
	snap, err := repo.Resolve(ctx, src, msg)
	if err != nil {
		return fail(err)
	}
	msg.LogAttrs(ctx, slog.LevelInfo, "reviewing", review.SourceAttrs(snap)...)

	var seed review.Seed
	if cmd.seedPath != "" {
		if seed, err = readSeed(cmd.seedPath, stdin); err != nil {
			return fail(err)
		}
	}

	dir, err := review.DraftDir()
	if err != nil {
		return fail(err)
	}
	if expireErr := review.ExpireDrafts(dir, time.Now()); expireErr != nil {
		msg.Warn("expire drafts", "err", expireErr)
	}
	// A held lock fails here too; LockedError carries the other session's URL.
	store, err := review.OpenStore(dir, review.DraftKey(snap))
	if err != nil {
		return fail(err)
	}
	ledgerStore, ledger := openLedger(snap, msg)
	prior := round.NewPrior(snap, ledger)
	// The session owns the store from here, failed or not; main only records the URL in it below,
	// before serving.
	sess, opening, err := review.Start(ctx, store, snap, review.Fresh{
		Seed: seed, Viewed: prior.PresetMarks(), HideWhitespace: prior.HideWhitespace(),
	})
	if err != nil {
		return fail(err)
	}
	defer func() {
		if closeErr := sess.Close(); closeErr != nil {
			msg.Warn("close draft store", "err", closeErr)
		}
	}()
	if path, moved, reason := opening.MovedAside(); moved {
		msg.Warn("the saved draft could not be restored and was moved aside", "path", path, "reason", reason)
	} else if opening.Resumed() {
		msg.Info("resuming the saved draft", "path", sess.DraftPath())
		if cmd.seedPath != "" {
			msg.Info("the seed was ignored because a draft exists")
		}
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fail(fmt.Errorf("listen: %w", err))
	}
	token := rand.Text()
	url := server.URL(ln.Addr().String(), token)
	if err = store.SetURL(url); err != nil {
		return fail(errors.Join(err, ln.Close()))
	}
	drafts := &drafts{repo: repo, sess: sess, src: src, dir: dir, url: url, msg: msg}
	srv := server.New(server.Config{
		Session: sess, Ledger: ledger, Token: token, Assets: web.Assets(), Log: msg,
		Deliver: func(r review.Result) error { return deliver(stdout, r, cmd.json) },
		Refresh: drafts.refresh,
	})
	l := &live{srv: srv, sess: sess}
	current.Store(l)
	msg.Info("review at", "url", url)
	stopOpener := startOpener(ctx, url, msg)
	outcome, err := srv.Run(ctx, ln)
	stopOpener()
	if err != nil {
		msg.Error("serve the review", "err", err)
	}
	if outcome == server.Finished && ledgerStore != nil {
		// The result is already delivered: a ledger failure costs the next round its history,
		// never this round its result (REQ-RND-006).
		d := round.Delivered(sess.Snapshot(), sess.Draft(), time.Now())
		// Recording runs after a signal too, but bounded, so a stuck git cannot hold the exit.
		recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
		recErr := ledgerStore.Record(recCtx, repo, d)
		cancel()
		if recErr != nil {
			msg.Warn("record the review round", "err", recErr)
		}
	}
	return l.report(outcome, "", msg)
}

// drafts refreshes the session: it resolves the source again and opens the store of a new draft
// key (REQ-SES-013). The session owns every store it is given.
type drafts struct {
	repo *source.Repo
	sess *review.Session
	msg  *slog.Logger
	dir  string
	url  string // the review URL, recorded in every store so another rvw can name it
	src  source.Source
}

// refresh resolves the session's source again and moves the session to it. On failure the
// session is unchanged and what refresh opened is closed.
func (d *drafts) refresh(ctx context.Context) (review.Refreshed, error) {
	snap, err := d.repo.Resolve(ctx, d.src, d.msg)
	if err != nil {
		return review.Refreshed{}, err
	}
	opener := func(key string) (*review.Store, error) {
		st, openErr := review.OpenStore(d.dir, key)
		if openErr != nil {
			return nil, openErr
		}
		if urlErr := st.SetURL(d.url); urlErr != nil {
			return nil, errors.Join(urlErr, st.Close())
		}
		return st, nil
	}
	res, err := d.sess.Refresh(ctx, snap, opener)
	if err != nil {
		return review.Refreshed{}, errors.Join(err, snap.Close())
	}
	if res.Unchanged {
		d.msg.Info("refreshed: nothing changed")
	} else {
		d.msg.LogAttrs(ctx, slog.LevelInfo, "refreshed", append(review.SourceAttrs(snap), slog.Int("moved", len(res.Moved)))...)
	}
	if res.MovedAside != "" {
		d.msg.Warn("a draft saved for the new snapshot was moved aside", "path", res.MovedAside)
	}
	if res.OldDraftErr != nil {
		d.msg.Warn("release the replaced draft", "err", res.OldDraftErr)
	}
	if err := res.OldSnapshot.Close(); err != nil {
		// The refresh happened; what it replaced is released by exit at the latest.
		d.msg.Warn("release the replaced snapshot", "err", err)
	}
	return res, nil
}

// newLogger writes rvw's records to stderr as text, one line each. Records carry no time: they are
// read as the session goes, and a timestamp would only make them harder to compare.
func newLogger(stderr io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

// openLedger opens and loads the worktree's review ledger. Every failure is reported and leaves the
// session without history (REQ-RND-006): the zero Ledger, and a nil store when there is none to
// record into.
func openLedger(snap *source.Snapshot, msg *slog.Logger) (*round.Store, round.Ledger) {
	dir, err := round.LedgerDir()
	if err != nil {
		msg.Warn("find the review ledger", "err", err)
		return nil, round.Ledger{}
	}
	if expireErr := round.ExpireLedgers(dir, time.Now()); expireErr != nil {
		msg.Warn("expire review ledgers", "err", expireErr)
	}
	store, err := round.OpenStore(dir, snap.CommonDir(), snap.TopLevel())
	if err != nil {
		msg.Warn("open the review ledger", "err", err)
		return nil, round.Ledger{}
	}
	l, err := store.Load(time.Now())
	if err != nil {
		msg.Warn("load the review ledger", "err", err)
	}
	return store, l
}

// resolveRequest turns the command line's request into a source. An omitted base ref is
// origin/HEAD's target; the repository says what that is.
func resolveRequest(ctx context.Context, repo *source.Repo, req request) (source.Source, error) {
	if !req.originHead {
		return req.exact, nil
	}
	ref, err := repo.OriginHead(ctx)
	if errors.Is(err, source.ErrOriginHeadUnset) {
		return source.Source{}, fmt.Errorf("%w; name a base ref: rvw branch <base-ref>", err)
	}
	if err != nil {
		return source.Source{}, err
	}
	return source.Branch(ref, req.uncommitted)
}

// exitFor reports a session's outcome on stderr and returns rvw's exit status for it.
func exitFor(outcome server.Outcome, draft string, msg *slog.Logger) int {
	switch outcome {
	case server.Finished:
		return exitOK
	case server.Cancelled:
		msg.Info("review cancelled")
		return exitCancelled
	case server.DeliveryFailed, server.Failed:
		msg.Error("the review result was not delivered; the draft is kept", "draft", draft)
		return exitFailure
	case server.Interrupted:
		msg.Info("interrupted; the draft is kept", "draft", draft)
		return exitInterrupted
	case server.PageClosed:
		msg.Info("the review page was closed; saved draft changes are kept", "draft", draft)
		return exitPageClosed
	default:
		msg.Error("internal error: unexpected outcome; the draft is kept", "outcome", int(outcome), "draft", draft)
		return exitFailure
	}
}

func readSeed(path string, stdin io.Reader) (seed review.Seed, err error) {
	if path == "-" {
		return review.ParseSeed(stdin)
	}
	f, err := os.Open(path) //nolint:gosec // G304: the user names the seed file on the command line.
	if err != nil {
		return review.Seed{}, fmt.Errorf("seed: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("seed: %w", closeErr))
		}
	}()
	return review.ParseSeed(f)
}

// deliver writes the whole result to stdout. A short or failed write is a delivery failure.
func deliver(stdout io.Writer, r review.Result, asJSON bool) error {
	var data []byte
	if asJSON {
		var err error
		if data, err = review.RenderJSON(r); err != nil {
			return err
		}
	} else {
		data = review.RenderMarkdown(r)
	}
	if _, err := stdout.Write(data); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	if f, ok := stdout.(*os.File); ok {
		// Only a regular file can report a deferred write error; Sync on a pipe or terminal fails
		// (EBADF or EINVAL on darwin).
		info, err := f.Stat()
		if err != nil {
			return fmt.Errorf("stat stdout: %w", err)
		}
		if info.Mode().IsRegular() {
			if err := f.Sync(); err != nil {
				return fmt.Errorf("write stdout: %w", err)
			}
		}
	}
	return nil
}

// startOpener opens url in the background. The returned stop cancels the open command if it is
// still running and waits for it, so no open process outlives the session.
func startOpener(ctx context.Context, url string, msg *slog.Logger) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		openBrowser(ctx, url, msg)
	}()
	return func() {
		cancel()
		<-done
	}
}

// openBrowser asks macOS to open url. Failure is reported and the session keeps serving.
func openBrowser(ctx context.Context, url string, msg *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	// G204: the executable is fixed and url is the session URL rvw built.
	cmd := exec.CommandContext(ctx, "open", url) //nolint:gosec // see above
	// A killed open's children could hold its output pipe; do not wait on them.
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != context.Canceled {
		msg.Warn("could not open the browser; open the URL yourself", "url", url, "err", errWithOutput(err, out))
	}
}

func errWithOutput(err error, out []byte) error {
	if len(out) == 0 {
		return err
	}
	return fmt.Errorf("%w: %s", err, trimNL(out))
}

func trimNL(b []byte) string {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return string(b)
}
