package server

import (
	"sync"
	"time"
)

// presence is the single owner of the close grace (Q-42). A close notice arms one timer; any
// authenticated request before it fires disarms it; when it fires, gone receives once. Each arm or
// disarm bumps gen, so a callback whose timer was replaced or stopped too late sends nothing.
type presence struct {
	timer   *time.Timer
	gone    chan struct{} // capacity 1
	grace   time.Duration
	gen     uint64
	mu      sync.Mutex
	stopped bool
}

func newPresence(grace time.Duration) *presence {
	return &presence{grace: grace, gone: make(chan struct{}, 1)}
}

// closed records a close notice: gone receives after the grace unless seen or stop comes first.
// A later notice restarts the grace.
func (p *presence) closed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.disarm()
	gen := p.gen
	p.timer = time.AfterFunc(p.grace, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.gen != gen || p.stopped {
			return
		}
		select {
		case p.gone <- struct{}{}:
		default:
		}
	})
}

// seen records an authenticated request: a page is still there.
func (p *presence) seen() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.disarm()
}

// stop ends presence tracking. After stop, closed and seen do nothing.
func (p *presence) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	p.disarm()
}

// disarm invalidates the armed timer, if any. The caller holds mu.
func (p *presence) disarm() {
	p.gen++
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
}
