package engineio

import (
	"errors"
	"sync"
)

// Sentinel Errors.
var (
	errPollOverlap = errors.New("poll overlap")
	errPollClosed  = errors.New("poll transport closed")
)

// serverPollingTransport is the server side of the HTTP long-polling transport.
// At most one poll GET is held at a time; send delivers a payload to the held
// poll and releases it.
type serverPollingTransport struct {
	mu     sync.Mutex
	closed bool
	// dataCh is non-nil while a poll GET is held. It is buffered so a delivery
	// never blocks the sender; the held poll receives the payload and returns.
	dataCh chan []byte
}

// newServerPollingTransport creates a polling transport with no poll held.
func newServerPollingTransport() *serverPollingTransport {
	return &serverPollingTransport{}
}

// transportType reports the transport kind.
func (t *serverPollingTransport) transportType() TransportType {
	return TransportTypePolling
}

// send delivers the packets to the held poll, if any. It reports whether a poll
// was held to receive them; otherwise the caller keeps them buffered.
func (t *serverPollingTransport) send(packets []Packet) (bool, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false, nil
	}
	var ch = t.dataCh
	if ch == nil {
		t.mu.Unlock()
		return false, nil
	}
	t.dataCh = nil
	t.mu.Unlock()

	ch <- EncodePayload(packets)

	return true, nil
}

// hold registers a poll GET as the held request. A second concurrent poll, or a
// poll on a closed transport, is rejected.
func (t *serverPollingTransport) hold(ch chan []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch {
	case t.closed:
		return errPollClosed

	case t.dataCh != nil:
		return errPollOverlap

	default:
		t.dataCh = ch
	}

	return nil
}

// release abandons a held poll (e.g. when the client disconnects).
func (t *serverPollingTransport) release(ch chan []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.dataCh == ch {
		t.dataCh = nil
	}
}

// writeNoop delivers a single noop to the held poll, releasing it. It is used
// during an upgrade to unblock the client's poll.
func (t *serverPollingTransport) writeNoop() {
	t.mu.Lock()
	ch := t.dataCh
	t.dataCh = nil
	t.mu.Unlock()

	if ch != nil {
		ch <- EncodePayload([]Packet{{Type: PacketNoop}})
	}
}

// close marks the transport closed and releases any held poll with a close
// packet, so the client returns a recognisable body and tears down cleanly
// rather than seeing an empty response. It is idempotent.
//
// A held poll's channel is consumed under the lock by exactly one of send or
// writeNoop or close, so the buffered (cap 1) channel here is always empty and
// the delivery never blocks.
func (t *serverPollingTransport) close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	ch := t.dataCh
	t.dataCh = nil
	t.mu.Unlock()

	if ch != nil {
		ch <- EncodePayload([]Packet{{Type: PacketClose}})
	}
}
