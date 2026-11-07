package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"
)

// pageHeader names the page a request comes from. Each page load picks a random id, and a page
// restored from the back/forward cache picks a new one, so an id that sent a close notice never
// returns (Q-42).
const pageHeader = "Rvw-Page"

// maxPageIDLen bounds a page id; the UI sends a 36-character UUID.
const maxPageIDLen = 64

// closedPagesKept bounds the remembered closed page ids. An id forgotten only matters if its
// request is still in flight after this many later notices.
const closedPagesKept = 32

// pageID identifies one page load; parsePageID makes one from a request header.
type pageID string

var errPageID = errors.New("missing or malformed " + pageHeader + " header")

// parsePageID validates a page id: 1 to 64 ASCII letters, digits, '-' or '_'.
func parsePageID(s string) (pageID, error) {
	if s == "" || len(s) > maxPageIDLen {
		return "", errPageID
	}
	for i := range len(s) {
		if c := s[i]; !pageIDByte(c) {
			return "", fmt.Errorf("%w: byte %d is %q", errPageID, i, c)
		}
	}
	return pageID(s), nil
}

func pageIDByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_':
		return true
	}
	return false
}

// requestPage is the page of a request that passed the handler chain, which validated the header.
func requestPage(r *http.Request) pageID { return pageID(r.Header.Get(pageHeader)) }

// presence is the single owner of the close grace (Q-42). A close notice arms one timer and marks
// its page closed; a request from any page not marked closed disarms it; when it fires, gone
// receives once. A closed page's request, such as a poll in flight when it closed, is no
// presence. Each arm or disarm bumps gen, so a callback whose timer was replaced or stopped too
// late sends nothing.
type presence struct {
	timer   *time.Timer
	gone    chan struct{} // capacity 1
	closed  []pageID      // oldest first, at most closedPagesKept
	grace   time.Duration
	gen     uint64
	mu      sync.Mutex
	stopped bool
}

func newPresence(grace time.Duration) *presence {
	return &presence{grace: grace, gone: make(chan struct{}, 1)}
}

// close records page's close notice: gone receives after the grace unless another page is seen
// or stop comes first. A later notice restarts the grace.
func (p *presence) close(page pageID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	if !slices.Contains(p.closed, page) {
		if len(p.closed) == closedPagesKept {
			p.closed = append(p.closed[:0], p.closed[1:]...)
		}
		p.closed = append(p.closed, page)
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

// seen records an authenticated request from page: unless that page has closed, a page is still
// there.
func (p *presence) seen(page pageID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || slices.Contains(p.closed, page) {
		return
	}
	p.disarm()
}

// stop ends presence tracking. After stop, close and seen do nothing.
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
