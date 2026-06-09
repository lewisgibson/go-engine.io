package engineio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/quic-go/webtransport-go"
)

// Sentinel Errors.
var (
	ErrWebTransportDialerRequired = errors.New("webtransport dialer is required")
)

// webTransportReadLimit caps the size of a single inbound framed packet on the
// client, bounding memory against a server that declares an oversized frame
// length before any payload is read. It mirrors the websocket transport's read
// limit and comfortably exceeds the default 1 MB payload.
const webTransportReadLimit = 16 << 20

// WithWebTransportDialer enables the WebTransport (HTTP/3) transport on the
// client, dialing sessions with the given *webtransport.Dialer (which carries the
// TLS and QUIC configuration). WebTransport must also appear in WithTransports for
// the socket to try it, either as the initial transport or as an upgrade target.
func WithWebTransportDialer(dialer *webtransport.Dialer) SocketOption {
	return func(c *socketConfig) {
		c.webTransportConstructor = func(target *url.URL, _ TransportClient, header http.Header) (Transport, error) {
			return NewWebTransportTransport(target, dialer, header)
		}
	}
}

// WebTransportTransport is a transport that carries Engine.IO packets over a
// single bidirectional stream of an HTTP/3 WebTransport session.
type WebTransportTransport struct {
	// dialer and header are set once at construction and never mutated.
	dialer *webtransport.Dialer
	header http.Header

	// mu guards url, the handlers, state, and the session.
	mu              sync.Mutex
	url             *url.URL
	onOpenHandler   TransportOpenHandler
	onCloseHandler  TransportCloseHandler
	onPacketHandler TransportPacketHandler
	onErrorHandler  TransportErrorHandler
	state           TransportState
	session         *webtransport.Session
	stream          *webtransport.Stream
	// reader buffers the stream so the read loop can consume one framed packet at a
	// time without short reads.
	reader *bufio.Reader

	// writeMu serializes writes; a single QUIC stream allows only one writer.
	writeMu sync.Mutex
}

// NewWebTransportTransport creates a WebTransport transport that dials sessions
// with dialer (which carries the TLS and QUIC configuration). A nil header
// defaults to an empty header; a nil dialer or URL is an error. It is exported for
// parity with NewPollingTransport and NewWebSocketTransport, but is not in the
// Transports registry because it needs a dialer the registry's constructor
// signature does not carry; WithWebTransportDialer wires it per socket.
func NewWebTransportTransport(target *url.URL, dialer *webtransport.Dialer, header http.Header) (Transport, error) {
	if target == nil {
		return nil, ErrURLRequired
	}
	if dialer == nil {
		return nil, ErrWebTransportDialerRequired
	}
	if header == nil {
		header = http.Header{}
	}

	return &WebTransportTransport{
		dialer: dialer,
		header: header,
		url:    target,
		state:  TransportStateClosed,
	}, nil
}

// Type returns the type of the transport.
func (t *WebTransportTransport) Type() TransportType {
	return TransportTypeWebTransport
}

// State returns the state of the transport.
func (t *WebTransportTransport) State() TransportState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// SetURL sets the URL for the transport.
func (t *WebTransportTransport) SetURL(url *url.URL) {
	t.mu.Lock()
	t.url = url
	t.mu.Unlock()
}

// Open dials the WebTransport session, opens the bidirectional stream, writes the
// opening packet that identifies the session to the server, and starts reading.
func (t *WebTransportTransport) Open(ctx context.Context) {
	target, header, dialer, sid, ok := t.beginOpen()
	if !ok {
		return
	}

	// The session owns the underlying connection; the CONNECT response body must
	// not be closed, which would tear the session down.
	_, session, err := dialer.Dial(ctx, target, header) //nolint:bodyclose // the session owns the connection
	if err != nil {
		t.mu.Lock()
		t.state = TransportStateClosed
		t.mu.Unlock()

		t.onError(ctx, fmt.Errorf("dialing webtransport session: %w", err))
		return
	}

	stream, err := session.OpenStreamSync(ctx)
	if err != nil {
		_ = session.CloseWithError(0, "") //nolint:errcheck // best-effort close of a session we are abandoning

		t.mu.Lock()
		t.state = TransportStateClosed
		t.mu.Unlock()

		t.onError(ctx, fmt.Errorf("opening webtransport stream: %w", err))
		return
	}

	t.mu.Lock()
	t.session = session
	t.stream = stream
	t.reader = bufio.NewReader(stream)
	t.mu.Unlock()

	// The first write identifies the session: an empty open packet for a fresh
	// connection, or one carrying {"sid":"..."} to upgrade an existing one. The
	// server keys off this packet, so it must precede any probe ping.
	if err := t.writeOpen(ctx, sid); err != nil {
		t.onError(ctx, fmt.Errorf("sending webtransport open packet: %w", err))
		t.onClose(ctx)
		return
	}

	t.onOpen(ctx)

	go t.readLoop(ctx)
}

// beginOpen claims the opening transition and snapshots the dial parameters under
// a single lock, so Open can dial without touching the mutex. It forces the HTTPS
// scheme, since WebTransport always runs over HTTP/3, and reports false when the
// transport is not in a state that can be opened.
func (t *WebTransportTransport) beginOpen() (target string, header http.Header, dialer *webtransport.Dialer, sid string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state != TransportStateClosed {
		return "", nil, nil, "", false
	}
	t.state = TransportStateOpening

	// Copy the URL and force HTTPS so an http:// base URL still dials over HTTP/3.
	dialURL := *t.url
	dialURL.Scheme = "https"

	return dialURL.String(), t.header, t.dialer, t.url.Query().Get("sid"), true
}

// writeOpen sends the opening packet. With a session id it requests an upgrade of
// that session; without one it starts a fresh session.
func (t *WebTransportTransport) writeOpen(ctx context.Context, sid string) error {
	open := Packet{Type: PacketOpen}
	if sid != "" {
		data, err := json.Marshal(struct {
			SessionID string `json:"sid"`
		}{SessionID: sid})
		if err != nil {
			return fmt.Errorf("marshalling open packet: %w", err)
		}
		open.Data = data
	}

	return t.writeFrames(ctx, []Packet{open})
}

// Close sends a best-effort close packet and tears the session down. The close
// packet is only written while the transport is open.
func (t *WebTransportTransport) Close(ctx context.Context) {
	t.mu.Lock()
	if t.state != TransportStateOpening && t.state != TransportStateOpen {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	// Tell the server the transport is closing. This is best-effort: a failure is
	// surfaced through the error handler rather than blocking the close.
	if err := t.Send(ctx, []Packet{{Type: PacketClose}}); err != nil {
		t.onError(ctx, fmt.Errorf("sending close packet: %w", err))
	}

	t.onClose(ctx)
}

// Pause is a no-op for the WebTransport transport, which has no polling to drain.
func (t *WebTransportTransport) Pause(_ context.Context) {}

// Send frames each packet and writes them to the stream. Packets sent while the
// transport is not open are dropped, leaving retention to the socket's buffer.
func (t *WebTransportTransport) Send(ctx context.Context, packets []Packet) error {
	t.mu.Lock()
	state := t.state
	t.mu.Unlock()

	if state != TransportStateOpen {
		return nil
	}

	return t.writeFrames(ctx, packets)
}

// writeFrames frames the packets into one buffer and writes them under writeMu. It
// honours the caller's context deadline so a stalled write cannot block past it.
func (t *WebTransportTransport) writeFrames(ctx context.Context, packets []Packet) error {
	t.mu.Lock()
	stream := t.stream
	t.mu.Unlock()
	if stream == nil {
		return nil
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	var buffer []byte
	for _, packet := range packets {
		buffer = appendWebTransportFrame(buffer, packet)
	}

	// Apply the caller's deadline to the write; a context without one clears any
	// deadline left on the stream by an earlier write.
	var deadline time.Time
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	if err := stream.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("setting write deadline: %w", err)
	}

	if _, err := stream.Write(buffer); err != nil {
		return fmt.Errorf("writing webtransport frame: %w", err)
	}

	return nil
}

// readLoop reads one framed packet at a time until the stream is closed.
func (t *WebTransportTransport) readLoop(ctx context.Context) {
	for {
		t.mu.Lock()
		reader := t.reader
		state := t.state
		t.mu.Unlock()

		if reader == nil || state == TransportStateClosing || state == TransportStateClosed {
			return
		}

		// Bound an inbound frame to webTransportReadLimit so a server cannot force an
		// unbounded allocation by declaring an oversized frame length.
		packet, err := readWebTransportPacket(reader, webTransportReadLimit)
		if err != nil {
			t.mu.Lock()
			deliberate := t.state == TransportStateClosing || t.state == TransportStateClosed
			t.mu.Unlock()

			if !deliberate {
				t.onError(ctx, fmt.Errorf("reading webtransport stream: %w", err))
			}
			t.onClose(ctx)
			return
		}

		if packet.Type == PacketClose {
			t.onPacket(ctx, packet)
			t.onClose(ctx)
			return
		}

		t.onPacket(ctx, packet)
	}
}

// onOpen marks the transport open and notifies the handler.
func (t *WebTransportTransport) onOpen(ctx context.Context) {
	t.mu.Lock()
	t.state = TransportStateOpen
	handler := t.onOpenHandler
	t.mu.Unlock()

	if handler != nil {
		handler(ctx)
	}
}

// onClose marks the transport closed, tears the session down, and notifies the
// handler. It is idempotent.
func (t *WebTransportTransport) onClose(ctx context.Context) {
	t.mu.Lock()
	if t.state == TransportStateClosed {
		t.mu.Unlock()
		return
	}
	t.state = TransportStateClosed
	session := t.session
	t.session = nil
	t.stream = nil
	t.reader = nil
	handler := t.onCloseHandler
	t.mu.Unlock()

	if session != nil {
		if err := session.CloseWithError(0, ""); err != nil {
			t.onError(ctx, fmt.Errorf("closing webtransport session: %w", err))
		}
	}

	if handler != nil {
		handler(ctx)
	}
}

// onPacket notifies the packet handler.
func (t *WebTransportTransport) onPacket(ctx context.Context, packet Packet) {
	t.mu.Lock()
	handler := t.onPacketHandler
	t.mu.Unlock()

	if handler != nil {
		handler(ctx, packet)
	}
}

// onError notifies the error handler.
func (t *WebTransportTransport) onError(ctx context.Context, err error) {
	t.mu.Lock()
	handler := t.onErrorHandler
	t.mu.Unlock()

	if handler != nil {
		handler(ctx, err)
	}
}

// OnOpen sets the handler for when the transport opens.
func (t *WebTransportTransport) OnOpen(handler TransportOpenHandler) {
	t.mu.Lock()
	t.onOpenHandler = handler
	t.mu.Unlock()
}

// OnClose sets the handler for when the transport closes.
func (t *WebTransportTransport) OnClose(handler TransportCloseHandler) {
	t.mu.Lock()
	t.onCloseHandler = handler
	t.mu.Unlock()
}

// OnPacket sets the handler for when the transport receives packets.
func (t *WebTransportTransport) OnPacket(handler TransportPacketHandler) {
	t.mu.Lock()
	t.onPacketHandler = handler
	t.mu.Unlock()
}

// OnError sets the handler for when the transport encounters an error.
func (t *WebTransportTransport) OnError(handler TransportErrorHandler) {
	t.mu.Lock()
	t.onErrorHandler = handler
	t.mu.Unlock()
}
