package server

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/round"
	"github.com/jacoelho/rvw/internal/source"
)

// generationHeader names the generation a page shows, on requests, and the current one, on a 409
// reply to a request from another (REQ-SES-015).
const generationHeader = "Rvw-Generation"

// generation is one snapshot the review is served from and what the server derived from it. A
// refresh that gives other trees replaces it with the next id; one that gives the same trees keeps
// the id and the facts, so pages showing it keep working.
type generation struct {
	// checked is when the last change check ended; logged is set once a failed check was logged.
	// Both are guarded by Server.probeMu.
	checked time.Time
	snap    *source.Snapshot
	prior   *round.Prior  // snap's round facts, from Config.Ledger
	facts   *contentFacts // read by the run stopFacts ends
	// stopFacts cancels the facts read and waits for it; set before the generation is served.
	stopFacts func()
	moved     []review.Moved // the comments the refresh that made this generation moved
	id        uint64
	changed   atomic.Bool // sticky: once the change under review changed, it stays changed
	logged    bool
}

// bound serves h for the current generation, if the request names it. Holding refreshMu shared,
// no refresh runs until h returns.
func (s *Server) bound(h func(http.ResponseWriter, *http.Request, *generation)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.refreshMu.RLock()
		defer s.refreshMu.RUnlock()
		g := s.gen.Load()
		if !s.sameGeneration(w, r, g) {
			return
		}
		h(w, r, g)
	}
}

// loading serves h, the session document, for the current generation: a page loads whichever is
// current, so it names none.
func (s *Server) loading(h func(http.ResponseWriter, *http.Request, *generation)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.refreshMu.RLock()
		defer s.refreshMu.RUnlock()
		h(w, r, s.gen.Load())
	}
}

// draftOnly adapts a handler that uses the session but no generation's facts to bound.
func draftOnly(h http.HandlerFunc) func(http.ResponseWriter, *http.Request, *generation) {
	return func(w http.ResponseWriter, r *http.Request, _ *generation) { h(w, r) }
}

// sameGeneration reports whether r names g. Otherwise it replies: 400 when r names none, 409 with
// the current generation when r names another.
func (s *Server) sameGeneration(w http.ResponseWriter, r *http.Request, g *generation) bool {
	v := r.Header.Get(generationHeader)
	if v == "" {
		http.Error(w, "send "+generationHeader+", the generation the page shows", http.StatusBadRequest)
		return false
	}
	if id, err := strconv.ParseUint(v, 10, 64); err == nil && id == g.id {
		return true
	}
	stale(w, g.id)
	return false
}

// stale replies 409 to a request from a page showing another generation than current.
func stale(w http.ResponseWriter, current uint64) {
	w.Header().Set(generationHeader, strconv.FormatUint(current, 10))
	http.Error(w, "the review moved to a new snapshot; reload", http.StatusConflict)
}

type movedJSON struct {
	ID   string     `json:"id"`
	From targetJSON `json:"from"`
	To   targetJSON `json:"to"`
}

func toMovedJSON(moved []review.Moved) []movedJSON {
	out := make([]movedJSON, len(moved))
	for k, m := range moved {
		out[k] = movedJSON{ID: m.ID, From: toTargetJSON(m.From), To: toTargetJSON(m.To)}
	}
	return out
}

type refreshJSON struct {
	Moved      []movedJSON `json:"moved"`
	Generation uint64      `json:"generation"`
	Unchanged  bool        `json:"unchanged"`
}

// postRefresh takes a new snapshot of the same source and carries the draft over (REQ-SES-013).
// It holds refreshMu exclusively, so it waits for the bound requests in progress and holds new
// ones until the new generation is current. Its own request names the generation it refreshes, so
// two pages' clicks refresh once.
func (s *Server) postRefresh(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Refresh == nil {
		http.Error(w, "this review cannot be refreshed", http.StatusNotImplemented)
		return
	}
	s.refreshMu.Lock()
	old := s.gen.Load()
	if !s.sameGeneration(w, r, old) {
		s.refreshMu.Unlock()
		return
	}
	res, err := s.cfg.Refresh(r.Context())
	if err != nil {
		s.refreshMu.Unlock()
		s.fail(w, r, err)
		return
	}
	snap := s.cfg.Session.Snapshot()
	next := &generation{snap: snap, prior: round.NewPrior(snap, s.cfg.Ledger)}
	if res.Unchanged {
		// The same trees: the same files and facts. The check starts again on the new snapshot.
		next.id, next.facts, next.stopFacts, next.moved = old.id, old.facts, old.stopFacts, old.moved
	} else {
		next.id, next.facts, next.moved = old.id+1, newContentFacts(snap), res.Moved
		next.stopFacts = s.facts.start(next.facts, snap) //nolint:contextcheck // The reads outlive the request, under Run's context.
	}
	s.gen.Store(next)
	s.refreshMu.Unlock()
	if !res.Unchanged {
		old.stopFacts()
	}
	s.writeJSON(w, refreshJSON{Unchanged: res.Unchanged, Generation: next.id, Moved: toMovedJSON(res.Moved)})
}

// checkChanged runs g's change check unless one is running, a refresh is, g is no longer current,
// g already changed, or the last check ended less than checkInterval ago (REQ-SES-014). A failed
// check counts as a check, so a broken repository costs one git run per interval; it is logged
// once per generation.
func (s *Server) checkChanged(ctx context.Context, g *generation) {
	if g.changed.Load() || !s.probeMu.TryLock() {
		return
	}
	defer s.probeMu.Unlock()
	if !s.refreshMu.TryRLock() {
		return
	}
	defer s.refreshMu.RUnlock()
	if s.gen.Load() != g || (!g.checked.IsZero() && time.Since(g.checked) < s.checkInterval) {
		return
	}
	changed, err := g.snap.Changed(ctx)
	if err != nil && ctx.Err() != nil {
		return // the request ended, not the check
	}
	g.checked = time.Now()
	if err != nil {
		if !g.logged {
			g.logged = true
			s.log.Warn("check whether the change under review changed", "err", err)
		}
		return
	}
	if changed {
		g.changed.Store(true)
	}
}

// factRuns owns the content-fact reads of every generation: they run under Run's context, and
// Run waits for all of them before it returns.
type factRuns struct {
	ctx     context.Context //nolint:containedctx // Run's, for reads a refresh starts.
	stop    context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	stopped bool
}

// begin makes ctx the parent of every read; Run calls it before serving.
func (f *factRuns) begin(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctx, f.stop = context.WithCancel(ctx)
}

// start runs c's reads of snap and returns what cancels them and waits for them to end. After
// cancel it starts nothing: the session is ending and nobody reads the facts. Before begin, as
// when tests serve the handler without Run, it starts nothing either.
func (f *factRuns) start(c *contentFacts, snap *source.Snapshot) (stop func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped || f.ctx == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	f.wg.Go(func() {
		defer close(done)
		c.run(ctx, snap)
	})
	return func() {
		cancel()
		<-done
	}
}

// cancel ends every read and stops new ones starting.
func (f *factRuns) cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	if f.stop != nil {
		f.stop()
	}
}

// end cancels every read and waits for them to end.
func (f *factRuns) end() {
	f.cancel()
	f.wg.Wait()
}
