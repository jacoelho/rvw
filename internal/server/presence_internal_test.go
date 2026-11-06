package server

import (
	"testing"
	"time"
)

// TestPresenceStopIsTerminal: a notice after stop, as after Run returned, starts no timer and
// nothing is sent on gone.
func TestPresenceStopIsTerminal(t *testing.T) {
	t.Parallel()
	p := newPresence(time.Millisecond)
	p.stop()
	p.closed()
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
