// Package server serves the review UI and its JSON API on the loopback listener and turns the
// human's finish or cancel into a session outcome.
package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// Outcome is how a session ended.
type Outcome int

// Outcomes.
const (
	// Finished: the result was delivered.
	Finished Outcome = iota + 1
	// DeliveryFailed: writing the result failed; the draft is kept.
	DeliveryFailed
	// Cancelled: the human cancelled; the draft is deleted.
	Cancelled
	// Interrupted: Run's context ended first; the draft is kept.
	Interrupted
	// Failed: the server stopped serving on its own; the draft is kept.
	Failed
	// PageClosed: the page sent a close notice and no page made a request within the close grace;
	// saved draft changes are kept.
	PageClosed
)

// Timeouts and limits.
const (
	maxRequestBody  = 1 << 20
	highlightBudget = 2 * time.Second
	shutdownTimeout = 5 * time.Second
	// defaultFinishWait is Config.FinishWait's default.
	defaultFinishWait = 30 * time.Second
	// defaultCloseGrace is Config.CloseGrace's default (REQ-SES-012).
	defaultCloseGrace = 10 * time.Second
	// minCloseGrace keeps the status interval, half the grace in whole milliseconds, at least
	// 1 ms; the UI rejects a session document with a smaller status_interval_ms.
	minCloseGrace = 2 * time.Millisecond
	// maxStatusInterval caps the UI's status poll; REQ-SES-010 bounds the moved-HEAD notice to 10 s.
	maxStatusInterval = 5 * time.Second
	headCacheTTL      = 2 * time.Second
	readHeaderTimout  = 10 * time.Second
	// fileSlots bounds file and context requests in flight. Each holds a file's content, patch
	// and rendered rows, so this bounds the server's memory to a fixed number of files.
	fileSlots = 2
)

// Config is what a server needs. The server reviews Session's snapshot. Deliver writes the review
// result to the agent; it is called at most once, after the session has ended and before the finish
// request is answered.
type Config struct {
	Assets  fs.FS
	Session *review.Session
	Deliver func(review.Result) error
	Log     *log.Logger
	Token   string
	// FinishWait bounds how long Run, told to stop, waits for a delivery in progress; zero means
	// 30 seconds.
	FinishWait time.Duration
	// CloseGrace is how long Run waits after a page's close notice for any page's request before
	// it ends the session as PageClosed; zero means 10 seconds. The status interval the UI is told
	// to poll at is derived from it. Non-zero values exist for tests and must be at least 2 ms;
	// New panics on a smaller or negative value.
	CloseGrace time.Duration
}

// Server is one session's HTTP server.
type Server struct {
	ended    chan Outcome
	slots    chan struct{} // see fileSlots
	facts    *contentFacts
	snap     *source.Snapshot // cfg.Session's
	presence *presence
	head     headState
	cfg      Config
	// statusInterval is how often the UI polls the status. Half the close grace, so a sibling
	// page's poll falls within the grace, and at most maxStatusInterval.
	statusInterval time.Duration
	endOnce        sync.Once
	// pageClosed is set before Run ends the session because the page closed, so Abandon reports
	// PageClosed rather than Interrupted for the session's interrupted fate.
	pageClosed atomic.Bool
}

// New returns a server for cfg. It panics if cfg.CloseGrace is non-zero and below 2 ms.
func New(cfg Config) *Server {
	snap := cfg.Session.Snapshot()
	grace := cfg.CloseGrace
	if grace == 0 {
		grace = defaultCloseGrace
	}
	if grace < minCloseGrace {
		panic(fmt.Sprintf("server: CloseGrace %v is below %v", grace, minCloseGrace))
	}
	return &Server{
		cfg: cfg, snap: snap, facts: newContentFacts(snap), ended: make(chan Outcome, 1),
		slots: make(chan struct{}, fileSlots), presence: newPresence(grace),
		statusInterval: min(maxStatusInterval, grace/2),
	}
}

// Run serves on ln until the human finishes or cancels, the page closes, or ctx ends. It returns
// after the final response has been written and the server has shut down.
func (s *Server) Run(ctx context.Context, ln net.Listener) (Outcome, error) {
	defer s.presence.stop()
	// Requests end with the session: a signal or the session's outcome cancels file, context and
	// result requests still running, so Shutdown does not wait on their git work. A finish detaches
	// its delivery from this context.
	reqCtx, stopRequests := context.WithCancel(ctx)
	defer stopRequests()
	srv := &http.Server{
		Handler:           s.handler(ln.Addr().String()),
		ReadHeaderTimeout: readHeaderTimout,
		BaseContext:       func(net.Listener) context.Context { return reqCtx },
	}
	// The content reads end before Run returns, so no git process outlives the session.
	factsCtx, stopFacts := context.WithCancel(ctx)
	factsDone := make(chan struct{})
	go func() {
		defer close(factsDone)
		s.facts.run(factsCtx, s.snap)
	}()
	defer func() {
		stopFacts()
		<-factsDone
	}()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	// settle ends the session with o unless a finish reached delivery or a cancel has run; then
	// that outcome decides. A delivery that does not complete within the finish wait, such as a
	// write to a pipe nobody reads, is given up through Abandon and is a delivery failure: part of
	// the result may already be on stdout, so it can count neither as interrupted nor as failed
	// before delivery. Abandon makes the session agree: if the write later completes, Finish still
	// reports it undelivered and keeps the draft, so Run's outcome, the finish reply and the draft
	// never disagree. A delivery that completed just as the wait ran out is Finished.
	finishWait := s.cfg.FinishWait
	if finishWait == 0 {
		finishWait = defaultFinishWait
	}
	settle := func(o Outcome) Outcome {
		switch s.cfg.Session.End() {
		case review.EndedByCancel:
			return Cancelled
		case review.EndedByFinish:
			select {
			case o = <-s.ended:
				return o
			case <-time.After(finishWait):
				return s.Abandon()
			}
		default:
			return o
		}
	}
	var outcome Outcome
	var runErr error
	select {
	case outcome = <-s.ended:
	case err := <-serveErr:
		outcome, runErr = settle(Failed), fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		outcome = settle(Interrupted)
	case <-s.presence.gone:
		// Set before settle ends the session, so a concurrent Abandon agrees with this outcome.
		s.pageClosed.Store(true)
		outcome = settle(PageClosed)
	}
	stopRequests()
	stopFacts()
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return outcome, errors.Join(runErr, fmt.Errorf("shut down the review server: %w", err), srv.Close())
	}
	return outcome, runErr
}

// Abandon ends the session now, giving up on any delivery in progress, and returns the session's
// outcome: Finished, DeliveryFailed, Cancelled, PageClosed when Run ended the session because the
// page closed, or Interrupted. It is safe to call concurrently with Run and handlers, is
// idempotent, does not stop Run, and never returns Failed.
func (s *Server) Abandon() Outcome {
	switch s.cfg.Session.Abandon() {
	case review.FateDelivered:
		return Finished
	case review.FateUndelivered:
		return DeliveryFailed
	case review.FateCancelled:
		return Cancelled
	case review.FateInterrupted:
	}
	if s.pageClosed.Load() {
		return PageClosed
	}
	return Interrupted
}

// acquire takes a file slot, or returns why it could not before ctx ended. Every handler that runs
// git holds a slot while it does, with two exceptions: Finish, which the session phase serialises
// and whose delivery must not wait behind UI reads, and the status HEAD check, which its cache
// bounds to one run per interval.
func (s *Server) acquire(ctx context.Context) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for a file slot: %w", context.Cause(ctx))
	}
}

func (s *Server) release() { <-s.slots }

func (s *Server) end(o Outcome) {
	s.endOnce.Do(func() { s.ended <- o })
}

// URL returns the review URL for a listener address, with the token in the fragment.
func URL(addr, token string) string {
	return "http://" + addr + "/#token=" + token
}

// handler returns the HTTP handler for a listener bound to addr (host:port).
func (s *Server) handler(addr string) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/session", s.getSession)
	api.HandleFunc("GET /api/file", s.getFile)
	api.HandleFunc("GET /api/file/context", s.getContext)
	api.HandleFunc("GET /api/quotes", s.getQuotes)
	api.HandleFunc("PUT /api/comments/{id}", s.putComment)
	api.HandleFunc("DELETE /api/comments/{id}", s.deleteComment)
	api.HandleFunc("PUT /api/viewed/{i}", s.putViewed)
	api.HandleFunc("PUT /api/summary", s.putSummary)
	api.HandleFunc("GET /api/result.md", s.getResult)
	api.HandleFunc("POST /api/finish", s.postFinish)
	api.HandleFunc("POST /api/cancel", s.postCancel)
	api.HandleFunc("GET /api/status", s.getStatus)
	api.HandleFunc("POST /api/page-closed", s.postPageClosed)

	assets := http.FileServerFS(s.cfg.Assets)
	// Check never calls a deny handler, so none is set. Q-44 explains the order of the checks.
	cop := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())
		// DNS rebinding: a page on another name resolved to the loopback address passes the
		// cross-origin check, whose Origin comparison is against this very Host header.
		if r.Host != addr {
			http.Error(w, "unexpected Host", http.StatusMisdirectedRequest)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			assets.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if !s.authorized(r) {
			http.Error(w, "missing or wrong session token", http.StatusUnauthorized)
			return
		}
		if err := cop.Check(r); err != nil {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		// Any request that passed the checks shows a page is still there, the close notice too:
		// its handler then starts the grace.
		s.presence.seen()
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		api.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && s.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) == 1
}

// csp allows scripts, styles and connections only from the UI's own origin.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

// headState caches whether HEAD moved, for the moved-HEAD notice. A failed check is cached like a
// successful one, so a broken repository costs one git run per headCacheTTL, not one per poll; the
// notice keeps its last known answer meanwhile.
type headState struct {
	checked time.Time
	mu      sync.Mutex
	moved   bool
}

// movedSince reports whether HEAD moved. The error is returned only by the check that observed
// it, so the caller reports each failure once.
func (h *headState) movedSince(ctx context.Context, snap *source.Snapshot) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.checked.IsZero() && time.Since(h.checked) < headCacheTTL {
		return h.moved, nil
	}
	moved, err := snap.HeadMoved(ctx)
	if err != nil && ctx.Err() != nil {
		return h.moved, nil // the request ended, not the check
	}
	h.checked = time.Now()
	if err != nil {
		return h.moved, fmt.Errorf("check whether HEAD moved: %w", err)
	}
	h.moved = moved
	return moved, nil
}

// contentFacts owns the facts read from file contents after the UI is served, not before: which Go
// files are generated and line counts. Both reads run at once, but no count is published before the
// marks: until they land, counts wait in held, so a generated file's count is never published and a
// client at cursor 0 gets the marks before any count.
type contentFacts struct {
	genErr    string
	countErr  string
	vendored  []bool // by file index; set before either read starts
	gen       []bool // by file index once genDone; nil when the read failed
	generated []int  // file indexes, ascending
	counts    []fileCount
	held      []fileCount // counts found before genDone; at most one per file
	mu        sync.Mutex
	genDone   bool
	countDone bool
}

type fileCount struct {
	i int
	c source.LineCount
}

// factsView is what one status request reads from contentFacts.
type factsView struct {
	genErr, countErr string
	generated        []int
	counts           []fileCount
	next             int
	genDone          bool
	countDone        bool
}

// newContentFacts returns the facts for files, with no read started.
func newContentFacts(snap *source.Snapshot) *contentFacts {
	vendored := make([]bool, snap.FileCount())
	for i := range vendored {
		vendored[i] = snap.VendoredDir(i) != ""
	}
	return &contentFacts{vendored: vendored}
}

// run reads both facts and returns when both reads have ended; cancelling ctx ends them.
func (c *contentFacts) run(ctx context.Context, snap *source.Snapshot) {
	var wg sync.WaitGroup
	wg.Go(func() {
		gen, err := snap.GoGenerated(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			return // the session ended; nobody reads the facts
		}
		c.mark(gen, err)
	})
	err := snap.NumStat(ctx, c.count)
	// countDone waits for the marks: held counts are published only then.
	wg.Wait()
	if errors.Is(ctx.Err(), context.Canceled) {
		err = nil
	}
	c.finishCounts(err)
}

// mark records the generated marks and publishes the counts held for them. A failed read (err
// set, gen nil) marks nothing, so every file that is not vendored is then counted.
func (c *contentFacts) mark(gen []bool, err error) {
	var marks []int
	for i, g := range gen {
		if g {
			marks = append(marks, i)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen, c.generated, c.genDone = gen, marks, true
	if err != nil {
		c.genErr = err.Error()
	}
	for _, fc := range c.held {
		c.publish(fc)
	}
	c.held = nil
}

// count records file i's line count: held until the marks land, then published unless the file
// gets none.
func (c *contentFacts) count(i int, lc source.LineCount) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.genDone {
		c.held = append(c.held, fileCount{i, lc})
		return
	}
	c.publish(fileCount{i, lc})
}

// finishCounts records that the count read ended, with err if it failed. The caller has waited
// for the marks.
func (c *contentFacts) finishCounts(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.countDone = true
	if err != nil {
		c.countErr = err.Error()
	}
}

// publish appends fc unless its file gets no count: vendored and generated files. NumStat still
// computes their counts: restricting its pathspecs would change which renames Git detects. The
// caller holds mu, after genDone.
func (c *contentFacts) publish(fc fileCount) {
	if c.vendored[fc.i] || (c.gen != nil && c.gen[fc.i]) {
		return
	}
	c.counts = append(c.counts, fc)
}

// since returns the counts after cursor from. The generated marks belong to the stream's start:
// they and genDone are returned only for from 0. Every count is published after the marks, so a
// client whose cursor moved past 0 already holds them, and one at 0 never sees genDone without
// them. One lock makes each view a consistent snapshot.
func (c *contentFacts) since(from int) factsView {
	c.mu.Lock()
	defer c.mu.Unlock()
	from = min(max(from, 0), len(c.counts))
	v := factsView{
		counts: append([]fileCount(nil), c.counts[from:]...), next: len(c.counts),
		countDone: c.countDone, countErr: c.countErr,
	}
	if from == 0 {
		v.generated, v.genDone, v.genErr = c.generated, c.genDone, c.genErr
	}
	return v
}
