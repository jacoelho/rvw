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
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jacoelho/rvw/internal/review"
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

func main() {
	// Without this, a write to a closed stdout pipe kills the process before deliver can report
	// the failure and keep the draft.
	signal.Ignore(syscall.SIGPIPE)
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// msg writes whole lines to stderr; it appends a newline only when one is missing.
	msg := log.New(stderr, "", 0)
	cmd, err := parseArgs(args)
	if err != nil {
		msg.Printf("rvw: %v\n\n%s", err, usage)
		return exitUsage
	}
	if cmd.help {
		msg.Print(usage)
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
					cause = "rvw: a second signal ended the session before the review result was delivered"
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
	srv   *server.Server
	draft string
	once  sync.Once
	code  int
}

func (l *live) report(o server.Outcome, cause string, msg *log.Logger) int {
	l.once.Do(func() {
		if cause != "" {
			msg.Println(cause)
		}
		l.code = exitFor(o, l.draft, msg)
	})
	return l.code
}

func session(ctx context.Context, cmd command, stdin io.Reader, stdout io.Writer, msg *log.Logger, current *atomic.Pointer[live]) int {
	// fail ends a startup step. A signal that cut the step short is reported as the signal, not
	// as the step's failure.
	fail := func(err error) int {
		msg.Printf("rvw: %v", err)
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
			msg.Printf("rvw: %v", closeErr)
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
	msg.Println(review.DescribeSource(snap))

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
		msg.Printf("rvw: %v", expireErr)
	}
	// A held lock fails here too; LockedError carries the other session's URL.
	store, err := review.OpenStore(dir, review.DraftKey(snap))
	if err != nil {
		return fail(err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			msg.Printf("rvw: %v", closeErr)
		}
	}()

	sess, opening, err := review.Start(ctx, store, snap, seed)
	if err != nil {
		return fail(err)
	}
	if path, moved, reason := opening.MovedAside(); moved {
		msg.Printf("rvw: the saved draft could not be restored (%v); it was moved to %s", reason, path)
	} else if opening.Resumed() {
		msg.Printf("rvw: resuming the saved draft %s", store.Path())
		if cmd.seedPath != "" {
			msg.Println("rvw: the seed was ignored because a draft exists")
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
	srv := server.New(server.Config{
		Session: sess, Token: token, Assets: web.Assets(), Log: msg,
		Deliver: func(r review.Result) error { return deliver(stdout, r, cmd.json) },
	})
	l := &live{srv: srv, draft: store.Path()}
	current.Store(l)
	msg.Printf("rvw: review at %s", url)
	stopOpener := startOpener(ctx, url, msg)
	outcome, err := srv.Run(ctx, ln)
	stopOpener()
	if err != nil {
		msg.Printf("rvw: %v", err)
	}
	return l.report(outcome, "", msg)
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
func exitFor(outcome server.Outcome, draft string, msg *log.Logger) int {
	switch outcome {
	case server.Finished:
		return exitOK
	case server.Cancelled:
		msg.Println("rvw: review cancelled")
		return exitCancelled
	case server.DeliveryFailed, server.Failed:
		msg.Printf("rvw: the draft is kept at %s", draft)
		return exitFailure
	case server.Interrupted:
		msg.Printf("rvw: interrupted; the draft is kept at %s", draft)
		return exitInterrupted
	case server.PageClosed:
		msg.Printf("rvw: the review page was closed; saved draft changes are kept at %s", draft)
		return exitPageClosed
	default:
		msg.Printf("rvw: internal error: unexpected outcome %d; the draft is kept at %s", outcome, draft)
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
func startOpener(ctx context.Context, url string, msg *log.Logger) (stop func()) {
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
func openBrowser(ctx context.Context, url string, msg *log.Logger) {
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	// G204: the executable is fixed and url is the session URL rvw built.
	cmd := exec.CommandContext(ctx, "open", url) //nolint:gosec // see above
	// A killed open's children could hold its output pipe; do not wait on them.
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != context.Canceled {
		msg.Printf("rvw: could not open the browser (%v): open %s yourself", errWithOutput(err, out), url)
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
