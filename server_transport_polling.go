package engineio

import (
	"errors"
	"sync"

	"github.com/lewisgibson/go-engine.io/internal"
)

// Sentinel Errors.
var (
	errPollOverlap = errors.New("poll overlap")
	errPollClosed  = errors.New("poll transport closed")
)

// maxPooledPayloadBuffer caps the capacity of a poll payload buffer kept in the
// pool, so a one-off large payload does not leave an oversized buffer pinned in
// memory for the life of the process.
const maxPooledPayloadBuffer = 64 * 1024

// pollPayloadPool reuses the byte buffers that carry an encoded poll payload from
// the flushing goroutine to the held poll. The held poll returns its buffer once
// it has written the response, so a steady stream of polls reuses buffers instead
// of allocating one per delivery.
var pollPayloadPool = internal.NewPool(func() *[]byte { return new([]byte) })

// getPollBuffer takes a buffer from the pool and encodes the packets into it,
// ready to deliver to a held poll.
func getPollBuffer(packets []Packet) *[]byte {
	buffer := pollPayloadPool.Get()
	*buffer = appendPayload((*buffer)[:0], packets)

	return buffer
}

// putPollBuffer returns a delivered poll buffer to the pool unless it has grown
// too large to be worth retaining.
func putPollBuffer(buffer *[]byte) {
	if cap(*buffer) > maxPooledPayloadBuffer {
		return
	}

	*buffer = (*buffer)[:0]
	pollPayloadPool.Put(buffer)
}

// serverPollingTransport is the server side of the HTTP long-polling transport.
// At most one poll GET is held at a time; send delivers a payload to the held
// poll and releases it.
type serverPollingTransport struct {
	mu     sync.Mutex
	closed bool
	// dataCh is non-nil while a poll GET is held. It is buffered so a delivery
	// never blocks the sender; the held poll receives the payload and returns.
	dataCh chan *[]byte
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

	ch <- getPollBuffer(packets)

	return true, nil
}

// hold registers a poll GET as the held request. A second concurrent poll, or a
// poll on a closed transport, is rejected.
func (t *serverPollingTransport) hold(ch chan *[]byte) error {
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

// release abandons a held poll (e.g. when the client disconnects). It reports
// whether it reclaimed the channel before any delivery. false means a send,
// writeNoop, or close already claimed it and a payload delivery is in flight on
// the channel, so the caller must receive that payload to return its buffer to
// the pool rather than orphan it.
func (t *serverPollingTransport) release(ch chan *[]byte) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.dataCh == ch {
		t.dataCh = nil
		return true
	}

	return false
}

// writeNoop delivers a single noop to the held poll, releasing it. It is used
// during an upgrade to unblock the client's poll.
func (t *serverPollingTransport) writeNoop() {
	t.mu.Lock()
	ch := t.dataCh
	t.dataCh = nil
	t.mu.Unlock()

	if ch != nil {
		ch <- getPollBuffer([]Packet{{Type: PacketNoop}})
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
		ch <- getPollBuffer([]Packet{{Type: PacketClose}})
	}
}
