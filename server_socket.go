package engineio

import (
	"errors"
	"slices"
	"sync"
	"time"
)

// Sentinel Errors.
var (
	ErrSocketClosed = errors.New("socket is closed")
)

// sessionState is the lifecycle state of a server session.
type sessionState int

const (
	// sessionOpening is the brief state between handshake and the open packet.
	sessionOpening sessionState = iota
	// sessionOpen is a live session.
	sessionOpen
	// sessionClosing is a session draining its final packets before closing.
	sessionClosing
	// sessionClosed is a torn-down session.
	sessionClosed
)

// serverTransport is the server side of an Engine.IO transport. A transport
// carries packets for exactly one session.
type serverTransport interface {
	// transportType reports the transport kind.
	transportType() TransportType
	// send writes packets to the peer. It reports whether the packets were
	// written; a polling transport with no poll currently held reports false so
	// the socket keeps them buffered.
	send(packets []Packet) (sent bool, err error)
	// close tears down the underlying HTTP or WebSocket resources. It is
	// idempotent.
	close()
}

// ServerConnectionHandler is invoked once for each new session, after the open
// packet has been sent. It is where the application installs the session's
// message and close handlers and may begin sending. It must not block, since on
// the websocket transport it runs before the read loop starts.
type ServerConnectionHandler func(*ServerSocket)

// ServerMessageHandler is invoked for each message a session receives. The
// isBinary flag reports whether the peer sent the payload as binary, so the
// application can round-trip binary data without downgrading it to text.
type ServerMessageHandler func(data []byte, isBinary bool)

// ServerCloseHandler is called once when a session closes. The reason is a short
// human-readable label (e.g. "transport close", "ping timeout", "forced close").
// The cause is the underlying error when one was captured, or nil otherwise; a
// nil cause does not by itself mean a graceful close (a ping timeout also passes
// nil), so branch on the reason rather than on whether cause is nil.
type ServerCloseHandler func(reason string, cause error)

// ServerSocket is a single connected Engine.IO session, handed to the
// application through Server.OnConnection.
type ServerSocket struct {
	// id, server, and options are set once at construction and never mutated.
	id      string
	server  *Server
	options serverConfig

	// mu guards the state machine, the active transport, the outbound buffer,
	// the heartbeat and upgrade timers, and the handlers below.
	mu sync.Mutex

	state            sessionState
	transport        serverTransport
	upgradeTransport serverTransport
	upgrading        bool
	writeBuffer      []Packet

	pingTimer    *time.Timer
	pongTimer    *time.Timer
	upgradeTimer *time.Timer
	// closeTimer forces a graceful close to complete if the client never polls
	// again to drain the buffered close packet, so a vanished client cannot leak
	// a session stuck in the closing state.
	closeTimer *time.Timer

	probeNoopStop chan struct{}

	onMessageHandler ServerMessageHandler
	onCloseHandler   ServerCloseHandler

	// flushMu serializes flush so buffered packets are never written twice.
	flushMu sync.Mutex
}

// newServerSocket creates a session bound to the given transport in the opening
// state.
func newServerSocket(id string, server *Server, transport serverTransport) *ServerSocket {
	return &ServerSocket{
		id:        id,
		server:    server,
		options:   server.options,
		state:     sessionOpening,
		transport: transport,
	}
}

// ID returns the session identifier assigned at the handshake. It is stable for
// the life of the session and safe to read concurrently, since it is set once at
// construction and never mutated.
func (s *ServerSocket) ID() string {
	return s.id
}

// activePollingTransport returns the active transport if it is a polling
// transport.
func (s *ServerSocket) activePollingTransport() (*serverPollingTransport, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if polling, ok := s.transport.(*serverPollingTransport); ok {
		return polling, true
	}
	return nil, false
}

// OnMessage registers the handler invoked for each message the session receives.
// It replaces any previously registered handler; passing nil clears it. It is
// typically called from the connection handler.
func (s *ServerSocket) OnMessage(handler ServerMessageHandler) {
	s.mu.Lock()
	s.onMessageHandler = handler
	s.mu.Unlock()
}

// OnClose registers the handler invoked once when the session closes. It
// replaces any previously registered handler; passing nil clears it.
func (s *ServerSocket) OnClose(handler ServerCloseHandler) {
	s.mu.Lock()
	s.onCloseHandler = handler
	s.mu.Unlock()
}

// Send queues a message for delivery to the client. It is delivered on the next
// flush: immediately over WebSocket, or on the next poll for long-polling. Send
// is safe for concurrent use.
func (s *ServerSocket) Send(data []byte, isBinary bool) error {
	s.mu.Lock()
	if s.state == sessionClosed || s.state == sessionClosing {
		s.mu.Unlock()
		return ErrSocketClosed
	}
	s.writeBuffer = append(s.writeBuffer, Packet{Type: PacketMessage, Data: data, IsBinary: isBinary})
	s.mu.Unlock()

	s.flush()
	return nil
}

// Close gracefully closes the session, delivering a close packet to the client
// before tearing down.
func (s *ServerSocket) Close() error {
	if !s.beginClose() {
		return nil
	}

	// Deliver everything still buffered followed by the close packet, then tear
	// down. flush completes the teardown once the transport accepts the buffer:
	// immediately over WebSocket, or on the client's next poll for long-polling,
	// so a message sent just before Close is not dropped.
	s.flush()

	// If a long-poll transport had no poll held, the buffer is still pending and
	// the next poll will drain it and close the session. Arm a fallback so a
	// client that never polls again cannot leak the session in the closing state.
	s.mu.Lock()
	if s.state == sessionClosing && s.closeTimer == nil {
		s.closeTimer = time.AfterFunc(s.options.pingTimeout, func() {
			s.closeWithReason("forced close", nil)
		})
	}
	s.mu.Unlock()

	return nil
}

// beginClose claims the closing transition under a single lock, stops the
// heartbeat, and queues the close packet, so Close can flush and tear down
// without touching the mutex. The heartbeat is stopped now so a ping or
// pong-timeout cannot fire during the drain wait and tear the session down early
// (with the wrong reason and before the buffered data is delivered). It reports
// false when the session is not in a state that can be closed.
func (s *ServerSocket) beginClose() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionOpening && s.state != sessionOpen {
		return false
	}
	s.state = sessionClosing
	s.stopTimersLocked()
	s.writeBuffer = append(s.writeBuffer, Packet{Type: PacketClose})

	return true
}

// open marks the session open and arms the heartbeat once the handshake values
// are known.
func (s *ServerSocket) open() {
	s.mu.Lock()
	if s.state != sessionOpening {
		s.mu.Unlock()
		return
	}
	s.state = sessionOpen
	s.armPingLocked()
	s.mu.Unlock()
}

// flush writes any buffered packets to the active transport. It must be called
// without holding s.mu. flushMu serializes it so packets are never written
// twice, and the actual transport write happens outside s.mu so a slow client
// cannot block the heartbeat.
func (s *ServerSocket) flush() {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	for {
		s.mu.Lock()
		if s.state == sessionClosed || s.transport == nil || len(s.writeBuffer) == 0 {
			s.mu.Unlock()
			return
		}
		packets := slices.Clone(s.writeBuffer)
		transport := s.transport
		s.mu.Unlock()

		sent, err := transport.send(packets)
		switch {
		case err != nil:
			s.closeWithReason("transport error", err)
			return

		case !sent:
			// A polling transport with no poll held keeps the packets buffered.
			return
		}

		s.mu.Lock()
		// Drop the packets handed off, tolerating a concurrent close that may
		// have already emptied the buffer.
		if count := len(packets); count <= len(s.writeBuffer) {
			s.writeBuffer = s.writeBuffer[count:]
		} else {
			s.writeBuffer = nil
		}
		drained := len(s.writeBuffer) == 0
		closing := s.state == sessionClosing
		closed := s.state == sessionClosed
		s.mu.Unlock()

		if drained && closing {
			// The graceful close packet, and everything queued before it, has been
			// handed to the transport; complete the teardown now.
			s.closeWithReason("forced close", nil)
			return
		}

		if drained || closed {
			return
		}
	}
}

// handlePacket dispatches an inbound packet received on the active transport.
func (s *ServerSocket) handlePacket(p Packet) {
	switch p.Type {
	case PacketPong:
		s.onPong()

	case PacketMessage:
		s.deliverMessage(p.Data, p.IsBinary)

	case PacketClose:
		s.closeWithReason("transport close", nil)

	case PacketPing, PacketUpgrade, PacketOpen, PacketNoop:
		// A v4 client never originates these on an active transport; ignore them.
	}
}

// deliverMessage invokes the message handler outside the lock.
func (s *ServerSocket) deliverMessage(data []byte, isBinary bool) {
	s.mu.Lock()
	handler := s.onMessageHandler
	s.mu.Unlock()

	if handler != nil {
		handler(data, isBinary)
	}
}

// onPong clears the pong deadline and schedules the next ping.
func (s *ServerSocket) onPong() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionOpen {
		return
	}
	if s.pongTimer != nil {
		s.pongTimer.Stop()
		s.pongTimer = nil
	}
	s.armPingLocked()
}

// armPingLocked schedules the next ping. The caller must hold s.mu.
func (s *ServerSocket) armPingLocked() {
	if s.pingTimer != nil {
		s.pingTimer.Stop()
	}
	s.pingTimer = time.AfterFunc(s.options.pingInterval, s.sendPing)
}

// sendPing emits a ping and arms the pong deadline.
func (s *ServerSocket) sendPing() {
	s.mu.Lock()
	if s.state != sessionOpen {
		s.mu.Unlock()
		return
	}
	s.writeBuffer = append(s.writeBuffer, Packet{Type: PacketPing})
	if s.pongTimer != nil {
		s.pongTimer.Stop()
	}
	s.pongTimer = time.AfterFunc(s.options.pingTimeout, func() {
		s.closeWithReason("ping timeout", nil)
	})
	s.mu.Unlock()

	s.flush()
}

// closeWithReason tears the session down, reporting the human-readable reason
// and the underlying cause (which may be nil) to the close handler. It is
// idempotent.
func (s *ServerSocket) closeWithReason(reason string, cause error) {
	transport, upgrade, probeNoopStop, handler, ok := s.beginTeardown()
	if !ok {
		return
	}

	if probeNoopStop != nil {
		close(probeNoopStop)
	}
	if transport != nil {
		transport.close()
	}
	if upgrade != nil {
		upgrade.close()
	}
	s.server.sessions.delete(s.id)

	if handler != nil {
		handler(reason, cause)
	}
}

// beginTeardown claims the closed transition, stops the timers, and snapshots the
// resources to release under a single lock, so closeWithReason can close the
// channels and transports and notify the handler without touching the mutex. It
// reports false when the session is already closed, making the teardown
// idempotent.
func (s *ServerSocket) beginTeardown() (transport, upgrade serverTransport, probeNoopStop chan struct{}, handler ServerCloseHandler, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == sessionClosed {
		return nil, nil, nil, nil, false
	}
	s.state = sessionClosed
	s.stopTimersLocked()
	transport = s.transport
	upgrade = s.upgradeTransport
	probeNoopStop = s.probeNoopStop
	handler = s.onCloseHandler
	s.transport = nil
	s.upgradeTransport = nil
	s.upgrading = false
	s.probeNoopStop = nil
	s.writeBuffer = nil

	return transport, upgrade, probeNoopStop, handler, true
}

// stopTimersLocked stops and clears the heartbeat and upgrade timers. The caller
// must hold s.mu.
func (s *ServerSocket) stopTimersLocked() {
	if s.pingTimer != nil {
		s.pingTimer.Stop()
		s.pingTimer = nil
	}
	if s.pongTimer != nil {
		s.pongTimer.Stop()
		s.pongTimer = nil
	}
	if s.upgradeTimer != nil {
		s.upgradeTimer.Stop()
		s.upgradeTimer = nil
	}
	if s.closeTimer != nil {
		s.closeTimer.Stop()
		s.closeTimer = nil
	}
}

// startUpgrade begins probing an upgrade to the given websocket transport while
// the polling transport keeps running. It reports whether the probe may proceed.
func (s *ServerSocket) startUpgrade(transport serverTransport) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionOpen || s.upgrading || s.transport == nil || s.transport.transportType() != TransportTypePolling {
		return false
	}

	s.upgrading = true
	s.upgradeTransport = transport
	s.upgradeTimer = time.AfterFunc(s.options.upgradeTimeout, func() {
		s.abortUpgrade(transport)
	})

	return true
}

// onProbe responds to a probe ping by flushing the held poll with noop packets
// until the upgrade resolves, so the client can finish pausing the polling
// transport.
func (s *ServerSocket) onProbe() {
	s.mu.Lock()
	if !s.upgrading || s.probeNoopStop != nil {
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	s.probeNoopStop = stop
	s.mu.Unlock()

	go func() {
		var ticker = time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		s.flushProbeNoop()
		for {
			select {
			case <-stop:
				return

			case <-ticker.C:
				s.flushProbeNoop()
			}
		}
	}()
}

// flushProbeNoop writes a noop to the held poll, if the active transport is
// still polling.
func (s *ServerSocket) flushProbeNoop() {
	s.mu.Lock()
	transport := s.transport
	s.mu.Unlock()

	if polling, ok := transport.(*serverPollingTransport); ok {
		polling.writeNoop()
	}
}

// completeUpgrade promotes the probing websocket transport to the active
// transport and discards the polling transport.
func (s *ServerSocket) completeUpgrade(transport *serverWebSocketTransport) {
	old, stop, ok := s.beginCompleteUpgrade(transport)
	if !ok {
		return
	}

	transport.promote()
	if stop != nil {
		close(stop)
	}
	if old != nil {
		old.close()
	}

	s.flush()
}

// beginCompleteUpgrade swaps the probing websocket transport in as the active
// transport and snapshots the polling transport to discard and the probe-noop
// stop channel under a single lock, so completeUpgrade can promote, close, and
// flush without touching the mutex. It reports false when the probe no longer
// matches the in-flight upgrade.
func (s *ServerSocket) beginCompleteUpgrade(transport *serverWebSocketTransport) (old serverTransport, stop chan struct{}, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.upgrading || s.upgradeTransport != transport {
		return nil, nil, false
	}
	s.upgrading = false
	old = s.transport
	s.transport = transport
	s.upgradeTransport = nil
	if s.upgradeTimer != nil {
		s.upgradeTimer.Stop()
		s.upgradeTimer = nil
	}
	stop = s.probeNoopStop
	s.probeNoopStop = nil

	return old, stop, true
}

// abortUpgrade abandons a stalled or failed probe and keeps the session running
// on the polling transport.
func (s *ServerSocket) abortUpgrade(transport serverTransport) {
	stop, ok := s.beginAbortUpgrade(transport)
	if !ok {
		return
	}

	if stop != nil {
		close(stop)
	}
	transport.close()
}

// beginAbortUpgrade clears the in-flight upgrade and snapshots the probe-noop
// stop channel under a single lock, so abortUpgrade can close the channel and the
// probing transport without touching the mutex. It reports false when the probe
// no longer matches the in-flight upgrade.
func (s *ServerSocket) beginAbortUpgrade(transport serverTransport) (stop chan struct{}, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.upgrading || s.upgradeTransport != transport {
		return nil, false
	}
	s.upgrading = false
	s.upgradeTransport = nil
	if s.upgradeTimer != nil {
		s.upgradeTimer.Stop()
		s.upgradeTimer = nil
	}
	stop = s.probeNoopStop
	s.probeNoopStop = nil

	return stop, true
}
