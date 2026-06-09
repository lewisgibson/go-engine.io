package engineio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Sentinel Errors.
var (
	ErrInvalidURL   = errors.New("invalid URL")
	ErrNoTransports = errors.New("no transports available")
)

// SocketState is the lifecycle state of a client Socket. The Socket guards it
// under its lock and uses it to make Open, Close, Send, and the upgrade flow
// idempotent and to decide whether buffered writes may flush.
type SocketState string

const (
	// SocketStateOpen indicates the handshake has completed and the socket can send
	// and receive packets.
	SocketStateOpen SocketState = "open"
	// SocketStateOpening indicates the socket is connecting and awaiting the
	// server's open packet. Writes made now are buffered until it opens.
	SocketStateOpening SocketState = "opening"
	// SocketStateClosed indicates the socket is fully torn down. It is the initial
	// state and the state a Socket can be reopened from.
	SocketStateClosed SocketState = "closed"
	// SocketStateClosing indicates a close is in progress; the socket no longer
	// accepts writes and is delivering its final packets before closing.
	SocketStateClosing SocketState = "closing"
)

// SocketOpenHandler is invoked once when the socket opens, after the handshake
// completes. It is the point at which the application can safely start sending.
// It runs on a transport goroutine and must not block.
type SocketOpenHandler func()

// SocketCloseHandler is invoked once when the socket closes. The reason is a
// short human-readable description and the cause is the underlying error that
// triggered the close, or nil when no error was involved -- both a graceful close
// and a ping timeout pass a nil cause, so branch on the reason, not on whether
// cause is nil. It runs on a transport goroutine.
type SocketCloseHandler func(reason string, cause error)

// SocketPacketHandler is invoked for every packet the socket receives, including
// protocol packets such as ping and open, in arrival order. Use OnMessage to
// receive only application data; this handler is for callers that need to
// observe the raw protocol. It runs on a transport goroutine and must not block.
type SocketPacketHandler func(Packet)

// SocketMessageHandler is called when the socket receives a message packet. The
// isBinary flag reports whether the peer sent the payload as a binary frame, so
// the application can round-trip binary data without downgrading it to text.
type SocketMessageHandler func(data []byte, isBinary bool)

// SocketErrorHandler is invoked when the socket encounters an error, such as a
// transport failure or a malformed packet. An error does not always close the
// socket: a failed upgrade probe is non-fatal and leaves the current transport
// running. A probe failure is delivered to the upgrade-error handler when one is
// set (see OnUpgradeError) and only falls back to this handler otherwise. It runs
// on a transport goroutine and must not block.
type SocketErrorHandler func(error)

// SocketUpgradeHandler is invoked when the socket finishes upgrading to a new
// transport, with the type it switched to. It fires after the switch is
// committed and before buffered writes are flushed over the new transport, so
// the application can observe the better transport taking over.
type SocketUpgradeHandler func(transportType TransportType)

// SocketUpgradeErrorHandler is invoked when an upgrade probe fails. A failed
// probe is non-fatal: the socket keeps running on its current transport, so this
// is distinct from the error handler (used for fatal transport errors) and lets
// an application detect, e.g. a proxy that blocks WebSocket. When no
// upgrade-error handler is set, a probe failure is reported to the error handler
// instead.
type SocketUpgradeErrorHandler func(error)

// socketConfig holds the resolved socket options.
type socketConfig struct {
	client           TransportClient
	header           http.Header
	upgrade          bool
	rememberUpgrade  bool
	transports       []TransportType
	tryAllTransports bool
	// webTransportConstructor builds the WebTransport transport. It is nil unless
	// WithWebTransportDialer is set; storing a constructor closure that captures the
	// dialer keeps the webtransport-go import out of this file.
	webTransportConstructor TransportConstructor
}

// SocketOption configures a Socket.
type SocketOption func(*socketConfig)

// WithClient sets the HTTP client used by the socket's transports.
// Default: a new http.Client.
func WithClient(client TransportClient) SocketOption {
	return func(c *socketConfig) {
		c.client = client
	}
}

// WithHeader sets the headers sent by the socket's transports.
// Default: an empty http.Header.
func WithHeader(header http.Header) SocketOption {
	return func(c *socketConfig) {
		c.header = header
	}
}

// WithUpgrade determines whether the socket tries to upgrade from long-polling
// to a better transport. Default: true.
func WithUpgrade(upgrade bool) SocketOption {
	return func(c *socketConfig) {
		c.upgrade = upgrade
	}
}

// WithRememberUpgrade determines whether the socket reuses a previous successful
// upgrade on the next connection. Default: false.
func WithRememberUpgrade(rememberUpgrade bool) SocketOption {
	return func(c *socketConfig) {
		c.rememberUpgrade = rememberUpgrade
	}
}

// WithTransports sets the transports the socket tries, in order.
// Default: polling then websocket.
func WithTransports(transports ...TransportType) SocketOption {
	return func(c *socketConfig) {
		c.transports = transports
	}
}

// WithTryAllTransports determines whether the socket tries every transport in
// the list before giving up. Default: false.
func WithTryAllTransports(tryAllTransports bool) SocketOption {
	return func(c *socketConfig) {
		c.tryAllTransports = tryAllTransports
	}
}

// Socket is an Engine.IO v4 client connection to a server. It performs the
// handshake, runs the server-initiated heartbeat, buffers writes across a
// transport upgrade, and probes for a better transport in the background. A
// Socket is configured with SocketOptions, opened with Open, and observed
// through the OnX handlers; all of its methods are safe for concurrent use.
type Socket struct {
	// The following fields are set once at construction and never mutated.
	url              *url.URL
	client           TransportClient
	header           http.Header
	upgrade          bool
	rememberUpgrade  bool
	tryAllTransports bool
	// transportConstructors is the socket's private snapshot of the Transports
	// registry, taken at construction. Reading from it instead of the global map
	// keeps the background upgrade probe from racing a caller that mutates the
	// registry.
	transportConstructors map[TransportType]TransportConstructor

	// mu guards every field below it: the handlers, the negotiated session
	// parameters, the state machine, the active transport, and the heartbeat
	// timer. They are read and written from the caller, the transport
	// goroutines, and the heartbeat goroutine.
	mu sync.Mutex

	onOpenHandler         SocketOpenHandler
	onCloseHandler        SocketCloseHandler
	onPacketHandler       SocketPacketHandler
	onMessageHandler      SocketMessageHandler
	onErrorHandler        SocketErrorHandler
	onUpgradeHandler      SocketUpgradeHandler
	onUpgradeErrorHandler SocketUpgradeErrorHandler

	// transports is the list of transports to try, in order.
	transports []TransportType

	// sessionID is the unique session identifier from the handshake.
	sessionID string
	// pingInterval is how often the server pings, from the handshake.
	pingInterval time.Duration
	// pingTimeout is how long to wait for server activity, from the handshake.
	pingTimeout time.Duration
	// maxPayload is the maximum payload size in bytes, from the handshake.
	maxPayload int

	state     SocketState
	transport Transport
	// priorUpgrade is the transport a prior upgrade succeeded onto (websocket or
	// webtransport), or "" if none; with rememberUpgrade the next open starts on it
	// instead of polling.
	priorUpgrade TransportType
	// pingTimeoutTimer closes the transport if the server goes silent.
	pingTimeoutTimer *time.Timer
	// baseCtx is the caller's context from Open. Each connection attempt derives
	// its run context from this, so a transport-fallback retry starts from a live
	// context rather than the cancelled run context of the attempt that failed.
	baseCtx context.Context
	// runCancel cancels the context shared by the active transport and any
	// in-flight upgrade probe, so closing the socket unblocks a probe that is
	// still waiting for its handshake instead of leaking its goroutines.
	runCancel context.CancelFunc

	// writeBuffer holds packets queued for the active transport. Sends append to
	// it and flush drains it, which lets writes survive a transport upgrade.
	writeBuffer []Packet
	// upgrading suppresses flushes while a transport upgrade is in progress, so
	// buffered writes are sent over the new transport once the switch completes.
	upgrading bool
	// flushing indicates a flush is draining the write buffer, so a concurrent
	// flush defers to the running one instead of writing the same packets twice.
	flushing bool
	// flushWG tracks an in-flight flush. An upgrade waits on it so a write already
	// being sent over the old transport completes there before the switch, rather
	// than being silently dropped when the old transport is paused mid-send.
	flushWG sync.WaitGroup
}

// NewSocket creates a Socket for the given server URL, applying the options. It
// only parses configuration and does not connect; call Open to start the
// handshake. It returns ErrInvalidURL if serverURL cannot be parsed. The
// transport registry is snapshotted here, so later changes to Transports do not
// affect this socket.
func NewSocket(serverURL string, options ...SocketOption) (*Socket, error) {
	target, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	var config = socketConfig{
		client:     &http.Client{},
		header:     http.Header{},
		upgrade:    true,
		transports: []TransportType{TransportTypePolling, TransportTypeWebSocket},
	}
	for _, option := range options {
		option(&config)
	}

	// Snapshot the registry, then add the WebTransport constructor for this socket
	// only when WithWebTransportDialer supplied a dialer, since WebTransport cannot
	// be built from the registry's URL/client/header signature alone.
	constructors := maps.Clone(Transports)
	if config.webTransportConstructor != nil {
		constructors[TransportTypeWebTransport] = config.webTransportConstructor
	}

	return &Socket{
		url:                   target,
		client:                config.client,
		header:                config.header,
		upgrade:               config.upgrade,
		rememberUpgrade:       config.rememberUpgrade,
		tryAllTransports:      config.tryAllTransports,
		transportConstructors: constructors,

		transports: config.transports,
		state:      SocketStateClosed,
	}, nil
}

// Open connects the socket: it creates the first transport, opens it, and drives
// the handshake. The given context bounds the connection's lifetime; cancelling
// it tears down the active transport and any in-flight upgrade probe. Open is a
// no-op unless the socket is closed, so it is safe to call once and to reopen
// after a close. Failure to create a transport is reported through the error
// handler rather than returned.
func (s *Socket) Open(ctx context.Context) {
	runCtx, ok := s.beginOpen(ctx)
	if !ok {
		return
	}

	transport, err := s.createTransport()
	if err != nil {
		s.mu.Lock()
		s.state = SocketStateClosed
		s.mu.Unlock()

		// Report the real failure (no transports, an unregistered transport, or a
		// constructor error) rather than flattening every case to ErrNoTransports.
		if handler := s.errorHandler(); handler != nil {
			handler(err)
		}
		return
	}

	s.mu.Lock()
	s.transport = transport
	s.mu.Unlock()

	// Bind the transport handlers and open it outside the lock; transport
	// methods may call back into the socket.
	transport.OnPacket(s.onPacket)
	transport.OnError(s.onError)
	transport.OnClose(func(ctx context.Context) {
		s.onClose(ctx, "transport closed", nil)
	})
	transport.Open(runCtx)
}

// beginOpen claims the opening transition, resets any state left over from a
// prior connection, and derives the run context under a single lock, so Open can
// create and open the transport without touching the mutex. The run context
// derives from baseCtx (the caller's context), never from a prior attempt's run
// context, so a transport-fallback retry is not born cancelled. Cancelling it
// tears down the active transport and any probe. It reports false when the socket
// is not closed and so cannot be opened.
func (s *Socket) beginOpen(ctx context.Context) (runCtx context.Context, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != SocketStateClosed {
		return nil, false
	}

	// Claim the opening transition so a concurrent Open cannot also proceed, and
	// record the caller's context as the base for this whole connection: every run
	// context (this open and any transport-fallback retry) derives from it.
	s.state = SocketStateOpening
	s.baseCtx = ctx

	// Reset anything carried over from a previous connection on this reused socket.
	// A fresh open must not believe it is still mid-upgrade, nor inherit a prior
	// attempt's unsent writes.
	s.upgrading = false
	s.writeBuffer = nil

	// Cancel the previous run context if one is still live (cancelling it tears
	// down the old transport and any upgrade probe), then derive a fresh run
	// context from the caller's context -- never from a prior attempt's, so a
	// fallback retry is not born already cancelled.
	if s.runCancel != nil {
		s.runCancel()
	}
	runCtx, runCancel := context.WithCancel(ctx)
	s.runCancel = runCancel

	return runCtx, true
}

// createTransport builds the transport to open with: it picks the transport kind,
// resolves that transport's URL, and constructs it from the registry snapshot
// taken at NewSocket. The locking lives in selectTransport, which hands back
// everything the rest of this needs, so the construction itself is lock-free.
func (s *Socket) createTransport() (Transport, error) {
	// Choose the transport kind and snapshot the client and headers under the lock.
	transportType, client, header, ok := s.selectTransport()
	if !ok {
		return nil, ErrNoTransports
	}

	// Build this transport's URL: it carries the EIO/transport/sid query, so each
	// transport (and a fallback to the next) gets its own.
	target, err := s.resolveURL(transportType)
	if err != nil {
		return nil, fmt.Errorf("resolving URL: %w", err)
	}

	// Construct it from the per-socket constructor snapshot, so a caller mutating
	// the global Transports registry cannot race an open in progress.
	constructor, err := s.constructorFor(transportType)
	if err != nil {
		return nil, err
	}

	return constructor(target, client, header)
}

// constructorFor returns the constructor registered for the transport, or an error
// naming it when none is registered (for example webtransport configured without
// WithWebTransportDialer), so a missing constructor is reported rather than
// dereferenced as a nil function.
func (s *Socket) constructorFor(transportType TransportType) (TransportConstructor, error) {
	constructor, ok := s.transportConstructors[transportType]
	if !ok {
		return nil, fmt.Errorf("transport %q is not registered", transportType)
	}

	return constructor, nil
}

// selectTransport chooses the transport kind to open with and snapshots the
// client and header under a single lock, so createTransport can resolve the URL
// and construct the transport without touching the mutex. It reports false when
// no transports are configured.
func (s *Socket) selectTransport() (transportType TransportType, client TransportClient, header http.Header, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.transports) == 0 {
		return "", nil, nil, false
	}

	// The first transport in the list is used, unless a prior upgrade is remembered,
	// in which case that transport is used directly.
	transportType = s.transports[0]
	if s.rememberUpgrade && s.priorUpgrade != "" && slices.Contains(s.transports, s.priorUpgrade) {
		transportType = s.priorUpgrade
	}

	return transportType, s.client, s.header, true
}

// resolveURL builds the transport URL by applying the query parameters to the
// base URL.
func (s *Socket) resolveURL(transportType TransportType) (*url.URL, error) {
	s.mu.Lock()
	sessionID := s.sessionID
	s.mu.Unlock()

	// Copy the URL so the original is never mutated.
	target, err := url.Parse(s.url.String())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	q := s.url.Query()
	q.Set("EIO", strconv.Itoa(int(Protocol)))
	q.Set("transport", string(transportType))
	if sessionID != "" {
		q.Set("sid", sessionID)
	}
	target.RawQuery = q.Encode()

	return target, nil
}

// Close closes the socket, asking the active transport to send a close packet
// and tear down. It is a no-op unless the socket is open or opening, so a
// duplicate Close is harmless. The close handler fires once the teardown
// completes.
//
// Anything still in the write buffer is flushed before the transport is torn
// down, so a message sent immediately before Close is delivered rather than
// dropped. A flush is only attempted while the socket is open and not mid-upgrade
// (the buffer is flushed over the new transport when an upgrade completes).
func (s *Socket) Close(ctx context.Context) {
	canClose, canFlush := s.beginClose()
	if !canClose {
		return
	}

	// Drain buffered writes while the socket is still open; flush is a no-op once
	// the state leaves open, so it must run before the transition below.
	if canFlush {
		if err := s.flush(ctx); err != nil {
			if handler := s.errorHandler(); handler != nil {
				handler(fmt.Errorf("flushing on close: %w", err))
			}
		}
	}

	transport, ok := s.beginCloseTransport()
	if !ok {
		return
	}

	if transport != nil {
		transport.Close(ctx)
	}
}

// beginClose reports whether the socket can be closed and, if so, whether its
// buffer should be drained first, snapshotting both under a single lock so Close
// can flush without touching the mutex. canClose is false unless the socket is
// open or opening, making a duplicate Close a no-op.
func (s *Socket) beginClose() (canClose, canFlush bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != SocketStateOpen && s.state != SocketStateOpening {
		return false, false
	}
	canFlush = s.state == SocketStateOpen && !s.upgrading && len(s.writeBuffer) != 0

	return true, canFlush
}

// beginCloseTransport claims the closing transition and snapshots the transport
// to tear down under a single lock, re-checking the state after the flush since a
// concurrent Close may have moved it on. It reports false when the socket is no
// longer open or opening.
func (s *Socket) beginCloseTransport() (transport Transport, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != SocketStateOpen && s.state != SocketStateOpening {
		return nil, false
	}
	s.state = SocketStateClosing

	return s.transport, true
}

// Send appends packets to the write buffer and flushes it. Packets sent while
// the socket is still opening are buffered and flushed once it opens; while a
// transport upgrade is in progress the flush is deferred so they are sent over
// the new transport once the switch completes. This mirrors the reference client
// and keeps writes from being lost or reordered. Sends on a closing or closed
// socket are dropped, matching the reference's fire-and-forget contract.
func (s *Socket) Send(ctx context.Context, packets []Packet) error {
	s.mu.Lock()
	if s.state != SocketStateOpen && s.state != SocketStateOpening {
		s.mu.Unlock()
		return nil
	}
	s.writeBuffer = append(s.writeBuffer, packets...)
	s.mu.Unlock()

	return s.flush(ctx)
}

// flush drains the write buffer over the active transport, splitting it into
// payloads no larger than the negotiated maximum. It is a no-op while an upgrade
// is in progress or another flush is already running, in which case the running
// flush drains whatever is appended in the meantime. Only the packets actually
// written are removed from the buffer, so a failed write is retried by the next
// flush rather than being lost.
func (s *Socket) flush(ctx context.Context) error {
	for {
		s.mu.Lock()
		// Nothing to flush while closed, mid-upgrade, transportless, empty, or while
		// another flush is already draining the buffer.
		if s.state != SocketStateOpen || s.upgrading || s.transport == nil || len(s.writeBuffer) == 0 || s.flushing {
			s.mu.Unlock()
			return nil
		}
		s.flushing = true
		// Record the in-flight flush so a concurrent upgrade waits for it to finish
		// over the old transport before pausing it. The guard above already keeps a
		// new flush from starting once upgrading is set, so the upgrade only ever
		// waits for this one.
		s.flushWG.Add(1)
		packets := slices.Clone(s.writeBuffer)
		transport := s.transport
		maxPayload := s.maxPayload
		s.mu.Unlock()

		sent, err := s.sendChunks(ctx, transport, packets, maxPayload)

		s.mu.Lock()
		// Remove only the packets that were written. onClose clears the buffer when
		// the socket is no longer open, so leave it untouched in that case.
		if s.state == SocketStateOpen {
			s.writeBuffer = s.writeBuffer[sent:]
			if len(s.writeBuffer) == 0 {
				s.writeBuffer = nil
			}
		}
		s.flushing = false
		s.flushWG.Done()
		more := err == nil && s.state == SocketStateOpen && !s.upgrading && s.transport != nil && len(s.writeBuffer) != 0
		s.mu.Unlock()

		switch {
		case err != nil:
			return err

		case !more:
			return nil
		}
	}
}

// sendChunks writes packets over the transport in payloads no larger than the
// negotiated maximum, returning how many packets were written before any error.
func (s *Socket) sendChunks(ctx context.Context, transport Transport, packets []Packet, maxPayload int) (int, error) {
	var sent int
	for _, chunk := range chunkPackets(packets, maxPayload) {
		if err := transport.Send(ctx, chunk); err != nil {
			return sent, fmt.Errorf("sending packets: %w", err)
		}
		sent += len(chunk)
	}

	return sent, nil
}

// chunkPackets splits packets into groups whose encoded payload does not exceed
// maxPayload. A maxPayload of zero or less means no limit, and a single packet
// larger than maxPayload is sent on its own.
func chunkPackets(packets []Packet, maxPayload int) [][]Packet {
	if maxPayload <= 0 || len(packets) <= 1 {
		return [][]Packet{packets}
	}

	var (
		chunks  [][]Packet
		current []Packet
		size    int
	)
	for _, packet := range packets {
		// Adding to a non-empty chunk also adds one separator byte.
		var addition = len(EncodePacket(packet))
		if len(current) != 0 {
			addition++
		}

		if len(current) != 0 && size+addition > maxPayload {
			chunks = append(chunks, current)
			current = nil
			size = 0
			addition = len(EncodePacket(packet))
		}

		current = append(current, packet)
		size += addition
	}
	chunks = append(chunks, current)

	return chunks
}

// onError handles a transport error, retrying with the next transport when
// configured to do so.
func (s *Socket) onError(ctx context.Context, err error) {
	if handler := s.errorHandler(); handler != nil {
		handler(fmt.Errorf("transport error: %w", err))
	}

	retry, transport, baseCtx := s.beginRetry()

	if !retry {
		s.onClose(ctx, "transport error", err)
		return
	}

	if transport != nil {
		transport.OnOpen(nil)
		transport.OnClose(nil)
		transport.OnPacket(nil)
		transport.OnError(nil)
		transport.Close(ctx)
	}

	s.Open(baseCtx)
}

// beginRetry decides whether to fall back to the next transport and, when it
// does, drops the failed transport and resets for a fresh open, all under a
// single lock so onError can tear down the old transport and reopen without
// touching the mutex. It snapshots baseCtx (the caller's context) rather than
// reusing the cancelled run context, since Open cancels the run context this
// onError received and reusing it would hand the fallback transport an
// already-cancelled context.
func (s *Socket) beginRetry() (retry bool, transport Transport, baseCtx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A transport error clears any remembered upgrade, so the retry restarts from
	// the configured transport list rather than jumping straight onto the upgraded
	// transport.
	s.priorUpgrade = ""

	// Fall back to the next transport only when configured to try them all, another
	// transport remains to try, and we are still opening -- a settled session must
	// not silently re-handshake on a mid-session error.
	retry = s.tryAllTransports && len(s.transports) > 1 && s.state == SocketStateOpening
	transport = s.transport
	baseCtx = s.baseCtx

	if retry {
		// Drop the transport that just failed and reset for a fresh open over the
		// next one in the list.
		s.transports = s.transports[1:]
		s.state = SocketStateClosed
		s.transport = nil
		s.writeBuffer = nil
	}

	return retry, transport, baseCtx
}

// onClose tears down the socket. It is idempotent.
func (s *Socket) onClose(ctx context.Context, reason string, cause error) {
	transport, cancel, handler, ok := s.beginTeardown()
	if !ok {
		return
	}

	if transport != nil {
		// Detach the handlers before closing so the close does not re-enter.
		transport.OnOpen(nil)
		transport.OnClose(nil)
		transport.OnPacket(nil)
		transport.OnError(nil)
		transport.Close(ctx)
	}

	// Cancel the run context after the graceful close so any in-flight upgrade
	// probe unblocks and its goroutines exit instead of leaking.
	if cancel != nil {
		cancel()
	}

	if handler != nil {
		handler(reason, cause)
	}
}

// beginTeardown claims the closed transition, stops the heartbeat, and snapshots
// the transport, run-context cancel, and close handler under a single lock,
// resetting the session state so onClose can tear the transport down, cancel the
// run context, and notify the handler without touching the mutex. It reports
// false when the socket is already closed, making the teardown idempotent.
func (s *Socket) beginTeardown() (transport Transport, cancel context.CancelFunc, handler SocketCloseHandler, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != SocketStateOpen && s.state != SocketStateOpening && s.state != SocketStateClosing {
		return nil, nil, nil, false
	}
	s.state = SocketStateClosed
	if s.pingTimeoutTimer != nil {
		s.pingTimeoutTimer.Stop()
		s.pingTimeoutTimer = nil
	}
	transport = s.transport
	s.transport = nil
	s.sessionID = ""
	s.writeBuffer = nil
	// Clear the upgrade flag so a probe torn down by this close cannot leave it
	// stuck, which would otherwise suppress every flush after a reopen.
	s.upgrading = false
	cancel = s.runCancel
	s.runCancel = nil
	handler = s.onCloseHandler

	return transport, cancel, handler, true
}

// onPacket handles a packet received from the transport.
func (s *Socket) onPacket(ctx context.Context, p Packet) {
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()

	if state != SocketStateOpen && state != SocketStateOpening && state != SocketStateClosing {
		return
	}

	s.reschedulePingTimeout(ctx)

	switch p.Type {
	// An open packet means the server has completed the handshake.
	case PacketOpen:
		var openPacket OpenPacket
		switch err := json.Unmarshal(p.Data, &openPacket); {
		case err != nil:
			if handler := s.errorHandler(); handler != nil {
				handler(fmt.Errorf("unmarshalling open packet: %w", err))
			}

		default:
			s.onOpen(ctx, openPacket)
		}

	// A ping must be answered with a pong (v4 heartbeat is server-initiated).
	case PacketPing:
		if err := s.Send(ctx, []Packet{{Type: PacketPong}}); err != nil {
			if handler := s.errorHandler(); handler != nil {
				handler(fmt.Errorf("sending pong packet: %w", err))
			}
		}

	case PacketMessage:
		if handler := s.messageHandler(); handler != nil {
			handler(p.Data, p.IsBinary)
		}
	}

	if handler := s.packetHandler(); handler != nil {
		handler(p)
	}
}

// reschedulePingTimeout restarts the heartbeat timeout. If the server does not
// send anything within pingInterval+pingTimeout, the socket closes.
func (s *Socket) reschedulePingTimeout(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pingTimeoutTimer != nil {
		s.pingTimeoutTimer.Stop()
	}

	// The interval and timeout are zero until the handshake is processed; there
	// is nothing to arm until then.
	var timeout = s.pingInterval + s.pingTimeout
	if timeout <= 0 {
		s.pingTimeoutTimer = nil
		return
	}

	s.pingTimeoutTimer = time.AfterFunc(timeout, func() {
		s.onClose(ctx, "ping timeout", nil)
	})
}

// onOpen handles the server's open packet: it records the session parameters,
// arms the heartbeat, and probes for an upgrade in the background.
func (s *Socket) onOpen(ctx context.Context, p OpenPacket) {
	transport, upgrade, transports := s.recordOpen(p)

	if transport == nil {
		return
	}

	// Arm the heartbeat now that the interval and timeout are known.
	s.reschedulePingTimeout(ctx)

	// Update the transport's URL with the session ID for subsequent requests.
	target, err := s.resolveURL(transport.Type())
	if err != nil {
		if handler := s.errorHandler(); handler != nil {
			handler(fmt.Errorf("resolving URL: %w", err))
		}
		return
	}
	transport.SetURL(target)

	// Flush any packets buffered while the socket was still opening, now that the
	// transport is ready and carries the session id.
	if err := s.flush(ctx); err != nil {
		if handler := s.errorHandler(); handler != nil {
			handler(fmt.Errorf("flushing buffered packets: %w", err))
		}
	}

	if handler := s.openHandler(); handler != nil {
		handler()
	}

	// Probe upgrades in the background so the current transport keeps
	// delivering packets while the upgrade is negotiated.
	if upgrade && len(p.Upgrades) != 0 {
		go s.probeUpgrades(ctx, p.Upgrades, transports)
	}
}

// recordOpen records the negotiated session parameters, marks the socket open,
// and snapshots the active transport, the upgrade flag, and the transport list
// under a single lock, so onOpen can resolve the URL, flush, and probe without
// touching the mutex.
func (s *Socket) recordOpen(p OpenPacket) (transport Transport, upgrade bool, transports []TransportType) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessionID = p.SessionID
	s.maxPayload = p.MaxPayload
	s.pingTimeout = time.Duration(p.PingTimeout) * time.Millisecond
	s.pingInterval = time.Duration(p.PingInterval) * time.Millisecond
	s.state = SocketStateOpen
	transport = s.transport
	upgrade = s.upgrade
	transports = slices.Clone(s.transports)
	if transport != nil && transport.Type() != TransportTypePolling {
		s.priorUpgrade = transport.Type()
	} else {
		s.priorUpgrade = ""
	}

	return transport, upgrade, transports
}

// probeUpgrades probes each offered upgrade in turn, stopping at the first that
// succeeds.
func (s *Socket) probeUpgrades(ctx context.Context, upgrades, transports []TransportType) {
	for _, candidate := range upgrades {
		if !slices.Contains(transports, candidate) {
			continue
		}

		if err := s.probe(ctx, candidate); err != nil {
			err = fmt.Errorf("probing for upgrade: %w", err)
			// A probe failure is non-fatal; prefer the upgrade-error handler and
			// fall back to the error handler when none is set.
			if handler := s.upgradeErrorHandler(); handler != nil {
				handler(err)
			} else if handler := s.errorHandler(); handler != nil {
				handler(err)
			}
			continue
		}

		return
	}
}

// probe negotiates an upgrade to a new transport. On success it pauses the
// current transport and switches to the new one.
func (s *Socket) probe(ctx context.Context, upgradeTransportType TransportType) error {
	client, header := s.beginProbe()

	target, err := s.resolveURL(upgradeTransportType)
	if err != nil {
		return fmt.Errorf("resolving URL: %w", err)
	}

	constructor, err := s.constructorFor(upgradeTransportType)
	if err != nil {
		return err
	}

	transport, err := constructor(target, client, header)
	if err != nil {
		return fmt.Errorf("creating transport: %w", err)
	}

	// signal delivers the probe outcome exactly once.
	errChan := make(chan error, 1)
	var once sync.Once
	signal := func(err error) {
		once.Do(func() { errChan <- err })
	}

	// When the probe transport opens, send a probe ping.
	transport.OnOpen(func(ctx context.Context) {
		if err := transport.Send(ctx, []Packet{{Type: PacketPing, Data: []byte("probe")}}); err != nil {
			if handler := s.errorHandler(); handler != nil {
				handler(fmt.Errorf("sending probe packet: %w", err))
			}
		}
		transport.OnOpen(nil)
	})

	// A probe pong confirms the upgrade.
	transport.OnPacket(func(ctx context.Context, p Packet) {
		if p.Type != PacketPong || string(p.Data) != "probe" {
			return
		}

		// discardProbe tears down the probing transport and reports a failed probe.
		discardProbe := func(reason error) {
			transport.OnOpen(nil)
			transport.OnClose(nil)
			transport.OnError(nil)
			transport.OnPacket(nil)
			transport.Close(ctx)
			signal(reason)
		}

		// Begin the upgrade. Setting upgrading suppresses flushes so any write
		// queued from here on waits for the new transport, and snapshots the
		// transport being replaced.
		s.mu.Lock()
		if s.state != SocketStateOpen {
			s.mu.Unlock()
			discardProbe(errors.New("socket is no longer open"))
			return
		}
		s.upgrading = true
		old := s.transport
		s.mu.Unlock()

		// Detach the old transport's lifecycle handlers before the upgrade prompts
		// the server to close it. Its final poll fails once the server closes it,
		// and that failure must not reach s.onError and tear the socket down in the
		// middle of the upgrade. The packet handler stays attached so a message
		// already in flight on that poll is delivered rather than dropped.
		if old != nil {
			old.OnOpen(nil)
			old.OnClose(nil)
			old.OnError(nil)
		}

		// Wait for any write already being flushed over the old transport to finish
		// there before committing the upgrade. Setting upgrading above stops new
		// flushes from starting, so this drains exactly the in-flight one; without
		// it, that write could be silently dropped when the old transport is paused
		// mid-send, or land on the old transport after the upgrade.
		s.flushWG.Wait()

		// Commit the upgrade over the probing transport; it must be the first frame
		// on the new transport.
		if err := transport.Send(ctx, []Packet{{Type: PacketUpgrade}}); err != nil {
			// The upgrade never reached the server, so the old transport is still
			// live: restore its handlers, resume writing over it, and abandon the
			// probe.
			s.mu.Lock()
			s.upgrading = false
			s.mu.Unlock()

			if old != nil {
				old.OnError(s.onError)
				old.OnClose(func(ctx context.Context) {
					s.onClose(ctx, "transport closed", nil)
				})
			}
			if err := s.flush(ctx); err != nil {
				if handler := s.errorHandler(); handler != nil {
					handler(fmt.Errorf("flushing after a failed upgrade: %w", err))
				}
			}
			discardProbe(fmt.Errorf("sending upgrade packet: %w", err))
			return
		}

		// Drain the old transport's in-flight poll so a message still being
		// dispatched from it is delivered before the new transport takes over.
		if old != nil {
			old.Pause(ctx)
		}

		// Adopt the new transport unless the socket closed while draining.
		s.mu.Lock()
		if s.state != SocketStateOpen {
			s.mu.Unlock()
			discardProbe(errors.New("socket closed during upgrade"))
			return
		}
		s.priorUpgrade = transport.Type()
		s.transport = transport
		s.upgrading = false
		s.mu.Unlock()

		transport.OnPacket(s.onPacket)
		transport.OnError(s.onError)
		transport.OnClose(func(ctx context.Context) {
			s.onClose(ctx, "transport closed", nil)
		})

		// Notify the upgrade handler now that the switch is complete, then flush
		// any buffered writes over the new transport in order.
		if handler := s.upgradeHandler(); handler != nil {
			handler(transport.Type())
		}
		if err := s.flush(ctx); err != nil {
			if handler := s.errorHandler(); handler != nil {
				handler(fmt.Errorf("flushing after an upgrade: %w", err))
			}
		}

		signal(nil)
	})

	// A close or error before the pong fails the probe.
	transport.OnClose(func(ctx context.Context) {
		transport.OnOpen(nil)
		transport.OnClose(nil)
		transport.OnError(nil)
		transport.OnPacket(nil)
		transport.Close(ctx)
		signal(errors.New("transport closed"))
	})
	transport.OnError(func(ctx context.Context, err error) {
		transport.OnOpen(nil)
		transport.OnClose(nil)
		transport.OnError(nil)
		transport.OnPacket(nil)
		transport.Close(ctx)
		signal(fmt.Errorf("transport error: %w", err))
	})

	transport.Open(ctx)

	if err := <-errChan; err != nil {
		return fmt.Errorf("probing transport: %w", err)
	}

	return nil
}

// beginProbe clears the prior-upgrade flag and snapshots the client and header
// under a single lock, so probe can resolve the URL and construct the probing
// transport without touching the mutex.
func (s *Socket) beginProbe() (client TransportClient, header http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.priorUpgrade = ""

	return s.client, s.header
}

// OnOpen registers the handler invoked when the socket opens. It replaces any
// previously registered handler; passing nil clears it. It is safe to call
// concurrently, though handlers are normally set before Open.
func (s *Socket) OnOpen(handler SocketOpenHandler) {
	s.mu.Lock()
	s.onOpenHandler = handler
	s.mu.Unlock()
}

// OnClose registers the handler invoked when the socket closes. It replaces any
// previously registered handler; passing nil clears it.
func (s *Socket) OnClose(handler SocketCloseHandler) {
	s.mu.Lock()
	s.onCloseHandler = handler
	s.mu.Unlock()
}

// OnPacket registers the handler invoked for every packet the socket receives,
// including protocol packets. It replaces any previously registered handler;
// passing nil clears it. Use OnMessage for application data only.
func (s *Socket) OnPacket(handler SocketPacketHandler) {
	s.mu.Lock()
	s.onPacketHandler = handler
	s.mu.Unlock()
}

// OnMessage registers the handler invoked for each message packet the socket
// receives. It replaces any previously registered handler; passing nil clears
// it. This is the handler most applications use.
func (s *Socket) OnMessage(handler SocketMessageHandler) {
	s.mu.Lock()
	s.onMessageHandler = handler
	s.mu.Unlock()
}

// OnError registers the handler invoked when the socket encounters an error. It
// replaces any previously registered handler; passing nil clears it. An error is
// not always fatal, so the handler should not assume the socket has closed.
func (s *Socket) OnError(handler SocketErrorHandler) {
	s.mu.Lock()
	s.onErrorHandler = handler
	s.mu.Unlock()
}

// OnUpgrade registers the handler invoked when the socket upgrades to a new
// transport. It replaces any previously registered handler; passing nil clears
// it.
func (s *Socket) OnUpgrade(handler SocketUpgradeHandler) {
	s.mu.Lock()
	s.onUpgradeHandler = handler
	s.mu.Unlock()
}

// OnUpgradeError registers the handler invoked when an upgrade probe fails. It
// replaces any previously registered handler; passing nil clears it.
func (s *Socket) OnUpgradeError(handler SocketUpgradeErrorHandler) {
	s.mu.Lock()
	s.onUpgradeErrorHandler = handler
	s.mu.Unlock()
}

// openHandler returns the open handler under the lock.
func (s *Socket) openHandler() SocketOpenHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onOpenHandler
}

// packetHandler returns the packet handler under the lock.
func (s *Socket) packetHandler() SocketPacketHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onPacketHandler
}

// messageHandler returns the message handler under the lock.
func (s *Socket) messageHandler() SocketMessageHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onMessageHandler
}

// errorHandler returns the error handler under the lock.
func (s *Socket) errorHandler() SocketErrorHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onErrorHandler
}

// upgradeHandler returns the upgrade handler under the lock.
func (s *Socket) upgradeHandler() SocketUpgradeHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onUpgradeHandler
}

// upgradeErrorHandler returns the upgrade-error handler under the lock.
func (s *Socket) upgradeErrorHandler() SocketUpgradeErrorHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onUpgradeErrorHandler
}
