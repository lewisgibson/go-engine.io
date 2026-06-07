package engineio

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// pollCompressionThreshold is the minimum long-poll body size, in bytes, that is
// worth gzip-compressing; it matches the reference server's default threshold.
const pollCompressionThreshold = 1024

// Server is an Engine.IO v4 server. It is an http.Handler; mount it at the
// Engine.IO path (typically "/engine.io/"). The server is v4-only: it rejects
// any other protocol version.
type Server struct {
	// options and sessions are set once at construction and never replaced.
	options  serverConfig
	sessions *sessionStore

	// mu guards the connection and connection-error handlers.
	mu                sync.RWMutex
	onConnection      ServerConnectionHandler
	onConnectionError ServerConnectionErrorHandler
}

// ServerConnectionErrorHandler is called when a connection is rejected before a
// session is established. code is the Engine.IO error code (one of the
// ConnectionError* constants) and reason is the human-readable message (for an
// allowRequest rejection it is that error's message), so an operator can log or
// alert on refused connections and branch on why they were refused.
type ServerConnectionErrorHandler func(r *http.Request, code ConnectionErrorCode, reason string)

// NewServer creates a Server, applying the options over the defaults (see the
// DefaultX constants). Register a connection handler with OnConnection and mount
// the returned Server as an http.Handler to start accepting sessions.
func NewServer(options ...ServerOption) *Server {
	var config = defaultServerConfig()
	for _, option := range options {
		option(&config)
	}

	return &Server{
		options:  config,
		sessions: newSessionStore(),
	}
}

// OnConnection registers the handler invoked once per new session, after the
// open packet is sent. The application configures the socket here.
func (s *Server) OnConnection(handler ServerConnectionHandler) {
	s.mu.Lock()
	s.onConnection = handler
	s.mu.Unlock()
}

// connectionHandler returns the connection handler under the lock.
func (s *Server) connectionHandler() ServerConnectionHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.onConnection
}

// OnConnectionError registers the handler invoked when a connection is rejected
// before a session is established. It replaces any previously registered handler;
// passing nil clears it.
func (s *Server) OnConnectionError(handler ServerConnectionErrorHandler) {
	s.mu.Lock()
	s.onConnectionError = handler
	s.mu.Unlock()
}

// connectionErrorHandler returns the connection-error handler under the lock.
func (s *Server) connectionErrorHandler() ServerConnectionErrorHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.onConnectionError
}

// rejectConnection writes the Engine.IO error response and reports the rejection
// to the connection-error handler.
func (s *Server) rejectConnection(w http.ResponseWriter, r *http.Request, code ConnectionErrorCode, reason string) {
	writeServerError(w, code)

	if handler := s.connectionErrorHandler(); handler != nil {
		handler(r, code, reason)
	}
}

// allowHandshake runs the allow-request gate, if configured. When the gate
// rejects the request it writes a 403 Forbidden, reports the rejection, and
// returns false.
func (s *Server) allowHandshake(w http.ResponseWriter, r *http.Request) bool {
	if s.options.allowRequest == nil {
		return true
	}

	if err := s.options.allowRequest(r); err != nil {
		s.rejectConnection(w, r, ConnectionErrorForbidden, err.Error())
		return false
	}

	return true
}

// setSessionCookie writes the session-affinity cookie on the handshake response
// when WithCookie is configured.
func (s *Server) setSessionCookie(w http.ResponseWriter, id string) {
	var cookie = s.options.cookie
	if cookie == nil {
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookie.Name,
		Value:    id,
		Path:     cookie.Path,
		HttpOnly: cookie.HTTPOnly,
		Secure:   cookie.Secure,
		SameSite: cookie.SameSite,
		MaxAge:   cookie.MaxAge,
	})
}

// writePollResponse writes a long-poll body, gzip-compressing it when compression
// is enabled, the client advertises gzip, and the body is large enough to be
// worth it. It returns any write error so the caller can close the session.
func (s *Server) writePollResponse(w http.ResponseWriter, r *http.Request, payload []byte) error {
	s.writePollHeaders(w)

	if s.options.httpCompression && len(payload) >= pollCompressionThreshold && acceptsGzip(r) {
		var buffer bytes.Buffer
		var writer = gzip.NewWriter(&buffer)
		if _, err := writer.Write(payload); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}

		w.Header().Set("Content-Encoding", "gzip")
		if _, err := w.Write(buffer.Bytes()); err != nil {
			return err
		}

		return nil
	}

	if _, err := w.Write(payload); err != nil {
		return err
	}

	return nil
}

// acceptsGzip reports whether the request's Accept-Encoding advertises gzip.
func acceptsGzip(r *http.Request) bool {
	for encoding := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(encoding, ";", 2)[0]), "gzip") {
			return true
		}
	}

	return false
}

// Close tears down every live session with reason "forced close", firing each
// socket's close handler. It does not stop the underlying http.Server; the
// caller shuts that down separately.
func (s *Server) Close() {
	for _, socket := range s.sessions.all() {
		socket.closeWithReason("forced close", nil)
	}
}

// Sockets returns a snapshot of every live session's socket. The slice is a copy
// taken under the session lock, so it is safe to range over and to call socket
// methods on while sessions concurrently open and close: a session that ends
// after the snapshot is taken simply rejects further sends with ErrSocketClosed,
// and one that opens after it is omitted until the next call. It is the building
// block for fan-out, since a ServerSocket only ever talks to its own client.
// This mirrors the reference engine.io server's clients registry.
func (s *Server) Sockets() []*ServerSocket {
	return s.sessions.all()
}

// Count returns the number of live sessions. It mirrors the reference engine.io
// server's clientsCount.
func (s *Server) Count() int {
	return s.sessions.count()
}

// Socket returns the socket for the given session identifier, reporting whether a
// live session with that id exists. It mirrors indexing the reference engine.io
// server's clients registry by id.
func (s *Server) Socket(id string) (*ServerSocket, bool) {
	if socket, ok := s.sessions.get(id); ok {
		return socket, true
	}
	return nil, false
}

// ServeHTTP implements http.Handler. It applies CORS, rejects any protocol
// version other than v4 and any disabled transport, then dispatches the request
// to the polling or websocket handler. Errors are written as Engine.IO JSON
// error bodies.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS runs before validation so a preflight never fails on a missing sid.
	if s.applyCORS(w, r) {
		return
	}

	var query = r.URL.Query()
	if query.Get("EIO") != "4" {
		s.rejectConnection(w, r, ConnectionErrorUnsupportedProtocolVersion, ConnectionErrorUnsupportedProtocolVersion.message())
		return
	}

	var transport = TransportType(query.Get("transport"))
	if !s.options.allowsTransport(transport) {
		s.rejectConnection(w, r, ConnectionErrorUnknownTransport, ConnectionErrorUnknownTransport.message())
		return
	}

	var sid = query.Get("sid")
	switch transport {
	case TransportTypeWebSocket:
		s.handleWebSocket(w, r, sid)

	case TransportTypePolling:
		s.handlePolling(w, r, sid)

	default:
		s.rejectConnection(w, r, ConnectionErrorUnknownTransport, ConnectionErrorUnknownTransport.message())
	}
}

// handlePolling dispatches a long-polling request.
func (s *Server) handlePolling(w http.ResponseWriter, r *http.Request, sid string) {
	if sid == "" {
		if r.Method != http.MethodGet {
			s.rejectConnection(w, r, ConnectionErrorBadHandshakeMethod, ConnectionErrorBadHandshakeMethod.message())
			return
		}
		s.handleHandshake(w, r)
		return
	}

	socket, ok := s.sessions.get(sid)
	if !ok {
		s.rejectConnection(w, r, ConnectionErrorUnknownSessionID, ConnectionErrorUnknownSessionID.message())
		return
	}

	switch r.Method {
	case http.MethodGet:
		polling, ok := socket.activePollingTransport()
		if !ok {
			writeServerError(w, ConnectionErrorBadRequest)
			return
		}
		s.handlePoll(w, r, socket, polling)

	case http.MethodPost:
		s.handleSend(w, r, socket)

	default:
		writeServerError(w, ConnectionErrorBadRequest)
	}
}

// handleHandshake creates a new session and writes the open packet as the body
// of the handshake GET.
func (s *Server) handleHandshake(w http.ResponseWriter, r *http.Request) {
	if !s.allowHandshake(w, r) {
		return
	}

	id := s.options.generateID(r)
	s.setSessionCookie(w, id)
	polling := newServerPollingTransport()
	socket := newServerSocket(id, s, polling)

	open, err := s.buildOpenPacket(id, true)
	if err != nil {
		writeServerError(w, ConnectionErrorBadRequest)
		return
	}

	s.sessions.put(socket)

	s.writePollHeaders(w)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(EncodePayload([]Packet{open})); err != nil {
		s.sessions.delete(id)
		return
	}

	socket.open()

	if handler := s.connectionHandler(); handler != nil {
		handler(socket)
	}
}

// handlePoll holds a long-poll GET until the session has data to deliver, the
// session closes, or the client disconnects.
func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request, socket *ServerSocket, polling *serverPollingTransport) {
	var channel = make(chan []byte, 1)
	if err := polling.hold(channel); err != nil {
		if errors.Is(err, errPollOverlap) {
			// A second concurrent poll breaks delivery ordering; reject it and
			// close the session.
			writeServerError(w, ConnectionErrorBadRequest)
			socket.closeWithReason("transport error", nil)
			return
		}
		writeServerError(w, ConnectionErrorUnknownSessionID)
		return
	}

	// Deliver any already-buffered packets immediately.
	socket.flush()

	select {
	case payload := <-channel:
		if err := s.writePollResponse(w, r, payload); err != nil {
			// The client did not receive this payload. flush already handed it off,
			// so close the session rather than continue with a silent gap; the
			// client treats a failed poll the same way and reconnects.
			socket.closeWithReason("transport error", err)
			return
		}

	case <-r.Context().Done():
		polling.release(channel)
		socket.closeWithReason("transport close", r.Context().Err())
	}
}

// handleSend reads a POST body of client packets and dispatches them to the
// session.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request, socket *ServerSocket) {
	// v4 carries binary as base64 in a text body; an octet-stream body is invalid.
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/octet-stream") {
		writeServerError(w, ConnectionErrorBadRequest)
		socket.closeWithReason("transport error", nil)
		return
	}

	// Read at most MaxPayload+1 bytes so an oversized body is detected.
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(s.options.maxPayload)+1))
	switch {
	case err != nil:
		writeServerError(w, ConnectionErrorBadRequest)
		return

	case len(body) > s.options.maxPayload:
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}

	// An empty body carries no packets; acknowledge it without decoding, since
	// DecodePayload treats empty input as malformed and a stray empty POST must
	// not tear the session down.
	if len(body) != 0 {
		packets, err := DecodePayload(ProtocolVersion4, body)
		if err != nil {
			writeServerError(w, ConnectionErrorBadRequest)
			socket.closeWithReason("parse error", err)
			return
		}

		for _, packet := range packets {
			socket.handlePacket(packet)
		}
	}

	// text/html dodges browser content sniffing, matching the reference server.
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("ok")); err != nil {
		return
	}
}

// handleWebSocket accepts a websocket connection, either as a fresh handshake or
// as the upgrade of an existing polling session.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request, sid string) {
	// Gate a fresh handshake before upgrading; once the connection is accepted it
	// is too late to answer with a 403.
	if sid == "" && !s.allowHandshake(w, r) {
		return
	}

	// For a fresh handshake, mint the session id and set the affinity cookie
	// before the upgrade, since no header can be added once Accept has written the
	// 101 response.
	var id string
	if sid == "" {
		id = s.options.generateID(r)
		s.setSessionCookie(w, id)
	}

	var insecureSkipVerify bool
	var originPatterns []string
	if s.allowsAllOrigins() {
		insecureSkipVerify = true
	} else {
		originPatterns = s.options.cors.AllowedOrigins
	}

	acceptOptions := &websocket.AcceptOptions{
		InsecureSkipVerify: insecureSkipVerify,
		OriginPatterns:     originPatterns,
	}

	conn, err := websocket.Accept(w, r, acceptOptions)
	if err != nil {
		// Accept already wrote the failure response.
		return
	}

	// Bound an inbound message to the configured payload size; the library's
	// default read limit is far smaller than a typical maxPayload.
	conn.SetReadLimit(int64(s.options.maxPayload))

	if sid == "" {
		s.handleWebSocketHandshake(conn, id)
		return
	}
	s.handleWebSocketUpgrade(conn, sid)
}

// handleWebSocketHandshake establishes a session directly on a websocket
// transport, with no polling phase. The session id is minted by the caller
// before the upgrade so the affinity cookie can be set on the 101 response.
func (s *Server) handleWebSocketHandshake(conn *websocket.Conn, id string) {
	transport := newServerWebSocketTransport(conn, false)
	socket := newServerSocket(id, s, transport)
	transport.socket = socket

	open, err := s.buildOpenPacket(id, false)
	if err != nil {
		closeWebSocket(conn, "handshake failed")
		return
	}

	s.sessions.put(socket)

	if _, err := transport.send([]Packet{open}); err != nil {
		s.sessions.delete(id)
		transport.close()
		return
	}

	socket.open()

	// Register the connection handler before reading any frames, so a message
	// sent immediately after the open packet cannot be read and dropped before
	// the application has installed its message handler.
	if handler := s.connectionHandler(); handler != nil {
		handler(socket)
	}

	go transport.readLoop()

	// Hold the request open for the lifetime of the connection.
	<-transport.done
}

// handleWebSocketUpgrade probes and, on success, upgrades an existing polling
// session to a websocket transport.
func (s *Server) handleWebSocketUpgrade(conn *websocket.Conn, sid string) {
	socket, ok := s.sessions.get(sid)
	if !ok {
		closeWebSocket(conn, "Session ID unknown")
		return
	}

	transport := newServerWebSocketTransport(conn, true)
	transport.socket = socket

	if !socket.startUpgrade(transport) {
		transport.close()
		return
	}

	go transport.readLoop()

	// Hold the request open for the lifetime of the connection.
	<-transport.done
}

// applyCORS sets the cross-origin response headers and answers a preflight
// request. It returns true when it has fully handled the request.
func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	cors := s.options.cors

	var allowOrigin string
	switch {
	case s.allowsAllOrigins():
		if cors.AllowCredentials && origin != "" {
			allowOrigin = origin
		} else {
			allowOrigin = "*"
		}

	case origin != "" && slices.Contains(cors.AllowedOrigins, origin):
		allowOrigin = origin
	}

	var header = w.Header()
	if allowOrigin != "" {
		header.Set("Access-Control-Allow-Origin", allowOrigin)
		if allowOrigin != "*" {
			header.Add("Vary", "Origin")
		}
		if cors.AllowCredentials {
			header.Set("Access-Control-Allow-Credentials", "true")
		}
	}

	if r.Method == http.MethodOptions {
		var allowedHeaders = cors.AllowedHeaders
		if len(allowedHeaders) == 0 {
			allowedHeaders = []string{"Content-Type"}
		}
		header.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		header.Set("Access-Control-Allow-Headers", strings.Join(allowedHeaders, ", "))
		w.WriteHeader(http.StatusNoContent)
		return true
	}

	return false
}

// allowsAllOrigins reports whether the CORS policy permits every origin.
func (s *Server) allowsAllOrigins() bool {
	var origins = s.options.cors.AllowedOrigins
	return len(origins) == 0 || slices.Contains(origins, "*")
}

// writePollHeaders sets the headers common to long-polling responses.
func (s *Server) writePollHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Type", "text/plain; charset=UTF-8")
	header.Set("Cache-Control", "no-store")
}

// closeWebSocket closes a websocket connection with a policy-violation status,
// ignoring the error since the connection is being abandoned.
func closeWebSocket(conn *websocket.Conn, reason string) {
	if err := conn.Close(websocket.StatusPolicyViolation, reason); err != nil {
		return
	}
}

// ConnectionErrorCode is an Engine.IO connection error code. It is sent in the
// JSON error body of a rejected handshake or polling request and passed to a
// ServerConnectionErrorHandler, so callers can branch on why a connection was
// refused.
//
// https://github.com/socketio/engine.io/blob/main/packages/engine.io/lib/server.ts
type ConnectionErrorCode int

const (
	// ConnectionErrorUnknownTransport is sent when the requested transport is not enabled.
	ConnectionErrorUnknownTransport ConnectionErrorCode = 0
	// ConnectionErrorUnknownSessionID is sent when the session identifier is not recognised.
	ConnectionErrorUnknownSessionID ConnectionErrorCode = 1
	// ConnectionErrorBadHandshakeMethod is sent when a handshake uses the wrong HTTP method.
	ConnectionErrorBadHandshakeMethod ConnectionErrorCode = 2
	// ConnectionErrorBadRequest is sent when a request is otherwise malformed.
	ConnectionErrorBadRequest ConnectionErrorCode = 3
	// ConnectionErrorForbidden is sent when a request is rejected by the origin policy.
	ConnectionErrorForbidden ConnectionErrorCode = 4
	// ConnectionErrorUnsupportedProtocolVersion is sent when EIO is not 4.
	ConnectionErrorUnsupportedProtocolVersion ConnectionErrorCode = 5
)

// message returns the human-readable message that accompanies the code.
func (c ConnectionErrorCode) message() string {
	switch c {
	case ConnectionErrorUnknownTransport:
		return "Transport unknown"

	case ConnectionErrorUnknownSessionID:
		return "Session ID unknown"

	case ConnectionErrorBadHandshakeMethod:
		return "Bad handshake method"

	case ConnectionErrorBadRequest:
		return "Bad request"

	case ConnectionErrorForbidden:
		return "Forbidden"

	case ConnectionErrorUnsupportedProtocolVersion:
		return "Unsupported protocol version"

	default:
		return "Bad request"
	}
}

// status returns the HTTP status code for the error.
func (c ConnectionErrorCode) status() int {
	if c == ConnectionErrorForbidden {
		return http.StatusForbidden
	}

	return http.StatusBadRequest
}

// writeServerError writes the Engine.IO JSON error body for the given code. The
// body is the { "code": <int>, "message": <string> } shape the protocol defines
// (https://github.com/socketio/engine.io-protocol), built from a map so the wire
// keys are spelled out here rather than hidden behind struct tags.
func writeServerError(w http.ResponseWriter, code ConnectionErrorCode) {
	body, err := json.Marshal(map[string]any{
		"code":    int(code),
		"message": code.message(),
	})
	if err != nil {
		http.Error(w, code.message(), code.status())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code.status())

	// The status and headers are already committed, so a write failure here only
	// means the client disconnected mid-response: there is nothing actionable to
	// do, and no session exists yet to surface it on.
	_, _ = w.Write(body) //nolint:errcheck // unactionable: response already committed
}
