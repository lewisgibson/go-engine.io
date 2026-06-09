package engineio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/quic-go/webtransport-go"
)

// Sentinel Errors.
var (
	errWebTransportMissingSessionID = errors.New("webtransport upgrade is missing a session id")
)

// WithWebTransportServer enables the WebTransport (HTTP/3) transport on the
// server. The given *webtransport.Server upgrades each Extended CONNECT request
// into a WebTransport session; the caller runs its HTTP/3 listener (for example
// wt.ListenAndServeTLS) with this Server mounted on the same handler the TCP
// listener serves, so polling and websocket continue over TCP while WebTransport
// runs over UDP. WebTransport must also appear in WithServerTransports for the
// server to accept it and advertise it as an upgrade target.
//
// It calls webtransport.ConfigureHTTP3Server on wt.H3 so the HTTP/3 server
// advertises WebTransport in its SETTINGS and installs the request context that
// Upgrade needs; webtransport-go does not do this automatically, so a caller need
// not (and a repeat call is harmless).
func WithWebTransportServer(wt *webtransport.Server) ServerOption {
	if wt == nil || wt.H3 == nil {
		panic("engineio: WithWebTransportServer requires a non-nil *webtransport.Server with its H3 server set")
	}
	webtransport.ConfigureHTTP3Server(wt.H3)

	return func(c *serverConfig) {
		c.webTransportUpgrade = func(s *Server, w http.ResponseWriter, r *http.Request) {
			s.serveWebTransport(w, r, wt)
		}
	}
}

// serverWebTransportTransport is the server side of the WebTransport transport.
// It carries every packet of one session as a length-framed message on a single
// bidirectional QUIC stream. Until it is promoted it is in the probing state,
// handling only the upgrade handshake.
type serverWebTransportTransport struct {
	// socket, session, and stream are set once at construction and never mutated.
	socket  *ServerSocket
	session *webtransport.Session
	stream  *webtransport.Stream
	// reader buffers the stream so the first packet consumed during the handshake
	// and every packet the read loop consumes afterwards draw from one place.
	reader *bufio.Reader

	// writeMu serializes writes; a single QUIC stream allows only one writer.
	writeMu sync.Mutex

	// mu guards closed and probing.
	mu      sync.Mutex
	closed  bool
	probing bool

	// done is closed when the read loop exits, so the HTTP/3 handler can block on
	// it for the lifetime of the session.
	done chan struct{}
}

// newServerWebTransportTransport creates a transport over an accepted session and
// its first stream. When probing is true the transport handles only the upgrade
// handshake until it is promoted.
func newServerWebTransportTransport(session *webtransport.Session, stream *webtransport.Stream, reader *bufio.Reader, probing bool) *serverWebTransportTransport {
	return &serverWebTransportTransport{
		session: session,
		stream:  stream,
		reader:  reader,
		probing: probing,
		done:    make(chan struct{}),
	}
}

// transportType reports the transport kind.
func (t *serverWebTransportTransport) transportType() TransportType {
	return TransportTypeWebTransport
}

// send frames every packet and writes them to the stream in one call.
func (t *serverWebTransportTransport) send(packets []Packet) (bool, error) {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return false, nil
	}

	if err := t.writeFrames(packets); err != nil {
		return false, err
	}

	return true, nil
}

// writeFrames frames the packets into one buffer and writes them under writeMu,
// bounding the write with a deadline so a stalled peer cannot block the session
// indefinitely.
func (t *serverWebTransportTransport) writeFrames(packets []Packet) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	var buffer []byte
	for _, packet := range packets {
		buffer = appendWebTransportFrame(buffer, packet)
	}

	if err := t.stream.SetWriteDeadline(time.Now().Add(t.socket.options.pingTimeout)); err != nil {
		return fmt.Errorf("setting write deadline: %w", err)
	}
	if _, err := t.stream.Write(buffer); err != nil {
		return fmt.Errorf("writing webtransport frame: %w", err)
	}

	return nil
}

// readLoop reads one framed packet at a time until the stream closes, dispatching
// to the session (or the upgrade handshake while probing).
func (t *serverWebTransportTransport) readLoop() {
	defer close(t.done)

	for {
		packet, err := readWebTransportPacket(t.reader, webTransportServerReadLimit(t.socket.options.maxPayload))
		if err != nil {
			t.mu.Lock()
			closed := t.closed
			probing := t.probing
			t.mu.Unlock()

			switch {
			case closed:
				// Expected teardown initiated by the server.

			case probing:
				// A failed probe stream abandons only the probe; the session keeps
				// running on its polling transport.
				t.socket.abortUpgrade(t)

			case isWebTransportParseError(err):
				t.socket.closeWithReason("parse error", err)

			default:
				t.socket.closeWithReason("transport close", err)
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

// handleProbePacket drives the upgrade handshake on a probing transport. It
// mirrors the websocket probe exactly: the upgrade sequence is transport-agnostic.
func (t *serverWebTransportTransport) handleProbePacket(packet Packet) {
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
func (t *serverWebTransportTransport) writeProbePong() error {
	return t.writeFrames([]Packet{{Type: PacketPong, Data: []byte("probe")}})
}

// isProbing reports whether the transport is still probing.
func (t *serverWebTransportTransport) isProbing() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.probing
}

// promote ends the probing state so subsequent packets dispatch to the session.
func (t *serverWebTransportTransport) promote() {
	t.mu.Lock()
	t.probing = false
	t.mu.Unlock()
}

// close marks the transport closed and tears the session down. It is idempotent.
func (t *serverWebTransportTransport) close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()

	// Closing the session unblocks the read loop, which observes the closed state
	// and exits.
	if err := t.session.CloseWithError(0, ""); err != nil {
		// The session is already going away; nothing else to do.
		return
	}
}

// serveWebTransport upgrades an Extended CONNECT request into a WebTransport
// session, reads the client's opening packet, and either establishes a fresh
// session or upgrades an existing one. It blocks for the lifetime of the session,
// since returning would tear the HTTP/3 stream down.
func (s *Server) serveWebTransport(w http.ResponseWriter, r *http.Request, wt *webtransport.Server) {
	session, err := wt.Upgrade(w, r)
	if err != nil {
		// Upgrade leaves the response unwritten when the request is not a valid
		// WebTransport CONNECT; reject it so the caller gets a clear status.
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// The client opens exactly one bidirectional stream and writes its first packet
	// on it. Bound the wait by the upgrade timeout so a session that never opens a
	// stream cannot hold the handler open.
	acceptCtx, cancel := context.WithTimeout(session.Context(), s.options.upgradeTimeout)
	defer cancel()

	stream, err := session.AcceptStream(acceptCtx)
	if err != nil {
		closeWebTransportSession(session)
		return
	}

	// Bound the wait for the client's first packet too: AcceptStream only covered
	// the stream opening, so without a deadline a peer that opens the stream but
	// never writes would hold this handler goroutine until QUIC's idle timeout.
	if err := stream.SetReadDeadline(time.Now().Add(s.options.upgradeTimeout)); err != nil {
		closeWebTransportSession(session)
		return
	}

	// The first packet is always an open packet: empty for a fresh session, or
	// carrying {"sid":"..."} to upgrade an existing one.
	reader := bufio.NewReader(stream)
	first, err := readWebTransportPacket(reader, webTransportServerReadLimit(s.options.maxPayload))
	if err != nil || first.Type != PacketOpen {
		closeWebTransportSession(session)
		return
	}

	// Clear the handshake deadline now that the open packet has arrived; the
	// heartbeat and upgrade timers bound the session from here.
	if err := stream.SetReadDeadline(time.Time{}); err != nil {
		closeWebTransportSession(session)
		return
	}

	if len(first.Data) == 0 {
		s.webTransportHandshake(r, session, stream, reader)
		return
	}

	sid, err := parseWebTransportSessionID(first.Data)
	if err != nil {
		closeWebTransportSession(session)
		return
	}

	s.webTransportUpgrade(session, stream, reader, sid)
}

// webTransportHandshake establishes a session directly on a WebTransport
// transport, with no polling phase. The opening stream and its buffered reader
// are handed to the transport so its read loop continues where the handshake read
// left off.
func (s *Server) webTransportHandshake(r *http.Request, session *webtransport.Session, stream *webtransport.Stream, reader *bufio.Reader) {
	// Honour the allow-request gate for a fresh session. The CONNECT has already
	// been upgraded, so a rejection closes the session and is reported through the
	// connection-error handler rather than written as an HTTP error.
	if s.options.allowRequest != nil {
		if err := s.options.allowRequest(r); err != nil {
			if handler := s.connectionErrorHandler(); handler != nil {
				handler(r, ConnectionErrorForbidden, err.Error())
			}
			closeWebTransportSession(session)
			return
		}
	}

	id := s.options.generateID(r)
	transport := newServerWebTransportTransport(session, stream, reader, false)
	socket := newServerSocket(id, s, transport)
	transport.socket = socket

	// A session that starts on WebTransport is already on its best transport, so
	// the open packet offers no upgrades.
	open, err := s.buildOpenPacket(id, false)
	if err != nil {
		closeWebTransportSession(session)
		return
	}

	s.sessions.put(socket)

	if _, err := transport.send([]Packet{open}); err != nil {
		s.sessions.delete(id)
		transport.close()
		return
	}

	socket.open()

	// Register the connection handler before reading any packet, so a message sent
	// immediately after the open packet cannot be read and dropped before the
	// application has installed its message handler.
	if handler := s.connectionHandler(); handler != nil {
		handler(socket)
	}

	go transport.readLoop()

	// Hold the HTTP/3 handler open for the lifetime of the session.
	<-transport.done
}

// webTransportUpgrade probes and, on success, upgrades an existing polling
// session to a WebTransport transport.
func (s *Server) webTransportUpgrade(session *webtransport.Session, stream *webtransport.Stream, reader *bufio.Reader, sid string) {
	socket, ok := s.sessions.get(sid)
	if !ok {
		closeWebTransportSession(session)
		return
	}

	transport := newServerWebTransportTransport(session, stream, reader, true)
	transport.socket = socket

	if !socket.startUpgrade(transport) {
		transport.close()
		return
	}

	go transport.readLoop()

	// Hold the HTTP/3 handler open for the lifetime of the session.
	<-transport.done
}

// parseWebTransportSessionID extracts the session id from a {"sid":"..."} upgrade
// open packet body.
func parseWebTransportSessionID(data []byte) (string, error) {
	var payload struct {
		SessionID string `json:"sid"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("parsing webtransport session id: %w", err)
	}
	if payload.SessionID == "" {
		return "", errWebTransportMissingSessionID
	}

	return payload.SessionID, nil
}

// closeWebTransportSession tears a session down, ignoring the error from a session
// that is already going away.
func closeWebTransportSession(session *webtransport.Session) {
	_ = session.CloseWithError(0, "") //nolint:errcheck // best-effort close of a session being abandoned
}

// webTransportServerReadLimit returns the inbound frame ceiling for a server read.
// It falls back to webTransportReadLimit when maxPayload is unset (0 or negative),
// so WithMaxPayload(0) cannot disable the frame bound and leave a read unbounded
// against a peer that declares an oversized frame length.
func webTransportServerReadLimit(maxPayload int) int {
	if maxPayload <= 0 {
		return webTransportReadLimit
	}

	return maxPayload
}

// isWebTransportParseError reports whether err is a framing or packet decode
// error rather than a stream read error, so the read loop can report it with the
// "parse error" reason that matches the websocket transport.
func isWebTransportParseError(err error) bool {
	return errors.Is(err, errWebTransportFrameTooLarge) ||
		errors.Is(err, ErrEmptyPacket) ||
		errors.Is(err, ErrInvalidPacketType)
}
