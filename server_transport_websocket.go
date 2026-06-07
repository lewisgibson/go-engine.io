package engineio

import (
	"context"
	"fmt"
	"sync"

	"github.com/coder/websocket"
)

// serverWebSocketTransport is the server side of the WebSocket transport. It
// writes one Engine.IO packet per frame and reads one packet per frame. Until it
// is promoted it is in the probing state, handling only the upgrade handshake.
type serverWebSocketTransport struct {
	// socket and ws are set once at construction and never mutated.
	socket *ServerSocket
	ws     *websocket.Conn

	// writeMu serializes writes; a WebSocket connection allows only one
	// concurrent writer.
	writeMu sync.Mutex

	// mu guards closed and probing.
	mu      sync.Mutex
	closed  bool
	probing bool

	// ctx is the transport's lifecycle context, cancelled by close, so an
	// in-flight read or write is unblocked when the session is torn down instead
	// of relying solely on the connection close to surface.
	ctx    context.Context
	cancel context.CancelFunc

	// done is closed when the read loop exits.
	done chan struct{}
}

// newServerWebSocketTransport creates a websocket transport over conn. When
// probing is true the transport handles only the upgrade handshake until it is
// promoted.
func newServerWebSocketTransport(conn *websocket.Conn, probing bool) *serverWebSocketTransport {
	ctx, cancel := context.WithCancel(context.Background())

	return &serverWebSocketTransport{
		ws:      conn,
		probing: probing,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
}

// transportType reports the transport kind.
func (t *serverWebSocketTransport) transportType() TransportType {
	return TransportTypeWebSocket
}

// send writes each packet as its own frame: binary messages as binary frames,
// everything else as text frames.
func (t *serverWebSocketTransport) send(packets []Packet) (bool, error) {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return false, nil
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	for _, packet := range packets {
		if err := t.writeFrame(packet); err != nil {
			return false, fmt.Errorf("writing websocket frame: %w", err)
		}
	}

	return true, nil
}

// writeFrame writes a single packet as one frame. The caller must hold writeMu.
func (t *serverWebSocketTransport) writeFrame(packet Packet) error {
	ctx, cancel := context.WithTimeout(t.ctx, t.socket.options.pingTimeout)
	defer cancel()

	if packet.IsBinary {
		return t.ws.Write(ctx, websocket.MessageBinary, packet.Data)
	}

	return t.ws.Write(ctx, websocket.MessageText, EncodePacket(packet))
}

// readLoop reads one packet per frame until the connection closes, dispatching
// to the session (or the upgrade handshake while probing).
func (t *serverWebSocketTransport) readLoop() {
	defer close(t.done)

	for {
		messageType, frame, err := t.ws.Read(t.ctx)
		if err != nil {
			t.mu.Lock()
			closed := t.closed
			probing := t.probing
			t.mu.Unlock()

			switch {
			case closed:
				// Expected teardown initiated by the server.

			case probing:
				// The probe connection failed; keep running on polling.
				t.socket.abortUpgrade(t)

			default:
				t.socket.closeWithReason("transport close", err)
			}
			return
		}

		packet, err := decodeWebSocketFrame(messageType, frame)
		if err != nil {
			// A malformed frame on a probe connection abandons only the probe and
			// keeps the polling session running, matching the read-error path above.
			if t.isProbing() {
				t.socket.abortUpgrade(t)
			} else {
				t.socket.closeWithReason("parse error", err)
			}
			return
		}

		if t.isProbing() {
			t.handleProbePacket(packet)
			continue
		}

		t.socket.handlePacket(packet)
	}
}

// handleProbePacket drives the upgrade handshake on a probing transport.
func (t *serverWebSocketTransport) handleProbePacket(packet Packet) {
	switch {
	// A probe ping is answered with a probe pong; the held poll is then flushed
	// with noops so the client can finish pausing the polling transport.
	case packet.Type == PacketPing && string(packet.Data) == "probe":
		if err := t.writeProbePong(); err != nil {
			t.socket.abortUpgrade(t)
			return
		}
		t.socket.onProbe()

	// An upgrade packet commits the switch to this transport.
	case packet.Type == PacketUpgrade:
		t.socket.completeUpgrade(t)

	default:
		// Ignore any other packet during the probe.
	}
}

// writeProbePong replies to a probe ping with a probe pong.
func (t *serverWebSocketTransport) writeProbePong() error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	return t.writeFrame(Packet{Type: PacketPong, Data: []byte("probe")})
}

// isProbing reports whether the transport is still probing.
func (t *serverWebSocketTransport) isProbing() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.probing
}

// promote ends the probing state so subsequent frames dispatch to the session.
func (t *serverWebSocketTransport) promote() {
	t.mu.Lock()
	t.probing = false
	t.mu.Unlock()
}

// close marks the transport closed and tears down the connection. It is
// idempotent.
func (t *serverWebSocketTransport) close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()

	// Cancel the lifecycle context first so a read or write blocked on it returns
	// promptly, then close the underlying connection.
	t.cancel()

	if err := t.ws.Close(websocket.StatusNormalClosure, ""); err != nil {
		// The connection is already going away; nothing else to do.
		return
	}
}
