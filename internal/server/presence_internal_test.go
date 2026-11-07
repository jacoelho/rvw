package server

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestPresenceStopIsTerminal: a notice after stop, as after Run returned, starts no timer and
// nothing is sent on gone.
func TestPresenceStopIsTerminal(t *testing.T) {
	t.Parallel()
	p := newPresence(time.Millisecond)
	p.stop()
	p.close("a")
	p.mu.Lock()
	armed := p.timer != nil
	p.mu.Unlock()
	if armed {
		t.Error("a notice after stop armed the grace timer")
	}
	select {
	case <-p.gone:
		t.Error("a notice after stop ended the session")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestPresenceBoundsClosedPages: the closed page ids stay bounded however many pages close, and the
// most recent ones stay closed.
func TestPresenceBoundsClosedPages(t *testing.T) {
	t.Parallel()
	p := newPresence(time.Hour)
	defer p.stop()
	const n = 3 * closedPagesKept
	for i := range n {
		p.close(pageID(fmt.Sprint("p", i)))
	}
	p.close(pageID(fmt.Sprint("p", n-1))) // a repeated notice adds nothing
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.closed) != closedPagesKept {
		t.Fatalf("%d closed ids kept; want %d", len(p.closed), closedPagesKept)
	}
	for i := n - closedPagesKept; i < n; i++ {
		if !slices.Contains(p.closed, pageID(fmt.Sprint("p", i))) {
			t.Errorf("recent page p%d is no longer closed", i)
		}
	}
}

// TestParsePageID: the page id the UI sends (a UUID) is accepted; an empty, overlong or
// non-token id is refused.
func TestParsePageID(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in string
		ok bool
	}{
		{"3f2b8c1e-9d4a-4e7b-8a6f-0c1d2e3f4a5b", true},
		{"page_1", true},
		{"", false},
		{string(make([]byte, maxPageIDLen+1)), false},
		{"a b", false},
		{"a,b", false},
		{"é", false},
	} {
		got, err := parsePageID(c.in)
		if (err == nil) != c.ok || (c.ok && string(got) != c.in) {
			t.Errorf("parsePageID(%q) = %q, %v; want ok=%v", c.in, got, err, c.ok)
		}
	}
	if _, err := parsePageID(strings.Repeat("a", maxPageIDLen)); err != nil {
		t.Errorf("a %d-byte id refused: %v", maxPageIDLen, err)
	}
}
