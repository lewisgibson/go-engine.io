package engineio

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/coder/websocket"
)

// webSocketReadLimit caps the size of a single inbound WebSocket message. The
// coder/websocket default is 32 KiB, which is smaller than a typical maxPayload
// and would reject large messages; this bounds memory while comfortably
// exceeding the default 1 MB payload limit.
const webSocketReadLimit = 16 << 20

// WebSocketTransport is a transport that uses the WebSocket protocol.
type WebSocketTransport struct {
	// client and header are set once at construction and never mutated.
	client *http.Client
	header http.Header

	// mu guards url, the handlers, state, and the connection.
	mu              sync.Mutex
	url             *url.URL
	onOpenHandler   TransportOpenHandler
	onCloseHandler  TransportCloseHandler
	onPacketHandler TransportPacketHandler
	onErrorHandler  TransportErrorHandler
	state           TransportState
	ws              *websocket.Conn

	// writeMu serializes writes; a WebSocket connection allows only one
	// concurrent writer.
	writeMu sync.Mutex
}

// NewWebSocketTransport creates a new WebSocket transport. A nil client defaults
// to http.DefaultClient and a nil header to an empty header.
func NewWebSocketTransport(url *url.URL, client TransportClient, header http.Header) (Transport, error) {
	if url == nil {
		return nil, ErrURLRequired
	}

	// A custom client is wrapped in its own http.Client so the transport's
	// RoundTripper is never written onto the shared http.DefaultClient.
	var httpClient = http.DefaultClient
	if client != nil {
		httpClient = &http.Client{
			Transport: &TransportRoundTripper{Client: client},
		}
	}

	if header == nil {
		header = http.Header{}
	}

	return &WebSocketTransport{
		client: httpClient,
		header: header,
		url:    url,
		state:  TransportStateClosed,
	}, nil
}

// Type returns the type of the transport.
func (t *WebSocketTransport) Type() TransportType {
	return TransportTypeWebSocket
}

// State returns the state of the transport.
func (t *WebSocketTransport) State() TransportState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// SetURL sets the URL for the transport.
func (t *WebSocketTransport) SetURL(url *url.URL) {
	t.mu.Lock()
	t.url = url
	t.mu.Unlock()
}

// Open dials the WebSocket connection and starts reading frames.
func (t *WebSocketTransport) Open(ctx context.Context) {
	target, header, client, ok := t.beginOpen()
	if !ok {
		return
	}

	ws, res, err := websocket.Dial(ctx, target, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: header,
	})
	if err != nil {
		t.mu.Lock()
		t.state = TransportStateClosed
		t.mu.Unlock()

		t.onError(ctx, dialError(err, res))
		return
	}

	// Raise the read limit above the small library default so large messages are
	// not rejected mid-stream.
	ws.SetReadLimit(webSocketReadLimit)

	t.mu.Lock()
	t.ws = ws
	t.mu.Unlock()

	t.onOpen(ctx)

	go t.readLoop(ctx)
}

// beginOpen claims the opening transition and snapshots the dial parameters under
// a single lock, so Open can dial without touching the mutex. It reports false
// when the transport is not in a state that can be opened.
func (t *WebSocketTransport) beginOpen() (target string, header http.Header, client *http.Client, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state != TransportStateClosed {
		return "", nil, nil, false
	}
	t.state = TransportStateOpening

	return t.url.String(), t.header, t.client, true
}

// Close sends a best-effort close packet and tears down the connection. The close
// packet is only written while the transport is open; a Close during the opening
// window tears the connection down without sending one.
func (t *WebSocketTransport) Close(ctx context.Context) {
	t.mu.Lock()
	if t.state != TransportStateOpening && t.state != TransportStateOpen {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	// Tell the server the transport is closing. This is best-effort: a failure
	// is surfaced through the error handler rather than blocking the close.
	if err := t.Send(ctx, []Packet{{Type: PacketClose}}); err != nil {
		t.onError(ctx, fmt.Errorf("sending close packet: %w", err))
	}

	// Closing the connection unblocks the read loop, which observes the closed
	// state and exits.
	t.onClose(ctx)
}

// Pause is a no-op for the WebSocket transport, which has no polling to drain.
func (t *WebSocketTransport) Pause(_ context.Context) {}

// Send writes each packet as its own frame: binary messages as raw binary
// frames, everything else as text frames.
func (t *WebSocketTransport) Send(ctx context.Context, packets []Packet) error {
	t.mu.Lock()
	state := t.state
	ws := t.ws
	t.mu.Unlock()

	if state != TransportStateOpen || ws == nil {
		return nil
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	for _, packet := range packets {
		if packet.IsBinary {
			if err := ws.Write(ctx, websocket.MessageBinary, packet.Data); err != nil {
				return fmt.Errorf("writing binary frame: %w", err)
			}
			continue
		}

		if err := ws.Write(ctx, websocket.MessageText, EncodePacket(packet)); err != nil {
			return fmt.Errorf("writing frame: %w", err)
		}
	}

	return nil
}

// readLoop reads one packet per frame until the connection is closed.
func (t *WebSocketTransport) readLoop(ctx context.Context) {
	for {
		t.mu.Lock()
		ws := t.ws
		state := t.state
		t.mu.Unlock()

		if ws == nil || state == TransportStateClosing || state == TransportStateClosed {
			return
		}

		messageType, frame, err := ws.Read(ctx)
		if err != nil {
			t.mu.Lock()
			deliberate := t.state == TransportStateClosing || t.state == TransportStateClosed
			t.mu.Unlock()

			if !deliberate {
				t.onError(ctx, fmt.Errorf("reading websocket connection: %w", err))
			}
			t.onClose(ctx)
			return
		}

		packet, err := decodeWebSocketFrame(messageType, frame)
		if err != nil {
			t.onError(ctx, err)
			continue
		}

		if packet.Type == PacketClose {
			t.onPacket(ctx, packet)
			t.onClose(ctx)
			return
		}

		t.onPacket(ctx, packet)
	}
}

// decodeWebSocketFrame decodes a single WebSocket frame into a packet. A binary
// frame is always a binary message; a text frame is decoded normally.
func decodeWebSocketFrame(messageType websocket.MessageType, frame []byte) (Packet, error) {
	if messageType == websocket.MessageBinary {
		return Packet{Type: PacketMessage, Data: frame, IsBinary: true}, nil
	}

	packet, err := DecodePacket(frame)
	if err != nil {
		return Packet{}, fmt.Errorf("decoding websocket frame: %w", err)
	}

	return packet, nil
}

// dialError builds an error from a failed dial, including the response body when
// the server sent one.
func dialError(cause error, res *http.Response) error {
	if res != nil {
		defer res.Body.Close() //nolint:errcheck // best-effort close of the dial response body
		if body, err := io.ReadAll(res.Body); err == nil && len(body) != 0 {
			return fmt.Errorf("dialing websocket connection: %w: %s", cause, body)
		}
	}

	return fmt.Errorf("dialing websocket connection: %w", cause)
}

// onOpen marks the transport open and notifies the handler.
func (t *WebSocketTransport) onOpen(ctx context.Context) {
	t.mu.Lock()
	t.state = TransportStateOpen
	handler := t.onOpenHandler
	t.mu.Unlock()

	if handler != nil {
		handler(ctx)
	}
}

// onClose marks the transport closed, tears down the connection, and notifies
// the handler. It is idempotent.
func (t *WebSocketTransport) onClose(ctx context.Context) {
	t.mu.Lock()
	if t.state == TransportStateClosed {
		t.mu.Unlock()
		return
	}
	t.state = TransportStateClosed
	ws := t.ws
	t.ws = nil
	handler := t.onCloseHandler
	t.mu.Unlock()

	if ws != nil {
		if err := ws.CloseNow(); err != nil {
			t.onError(ctx, fmt.Errorf("closing websocket connection: %w", err))
		}
	}

	if handler != nil {
		handler(ctx)
	}
}

// onPacket notifies the packet handler.
func (t *WebSocketTransport) onPacket(ctx context.Context, packet Packet) {
	t.mu.Lock()
	handler := t.onPacketHandler
	t.mu.Unlock()

	if handler != nil {
		handler(ctx, packet)
	}
}

// onError notifies the error handler.
func (t *WebSocketTransport) onError(ctx context.Context, err error) {
	t.mu.Lock()
	handler := t.onErrorHandler
	t.mu.Unlock()

	if handler != nil {
		handler(ctx, err)
	}
}

// OnOpen sets the handler for when the transport opens.
func (t *WebSocketTransport) OnOpen(handler TransportOpenHandler) {
	t.mu.Lock()
	t.onOpenHandler = handler
	t.mu.Unlock()
}

// OnClose sets the handler for when the transport closes.
func (t *WebSocketTransport) OnClose(handler TransportCloseHandler) {
	t.mu.Lock()
	t.onCloseHandler = handler
	t.mu.Unlock()
}

// OnPacket sets the handler for when the transport receives packets.
func (t *WebSocketTransport) OnPacket(handler TransportPacketHandler) {
	t.mu.Lock()
	t.onPacketHandler = handler
	t.mu.Unlock()
}

// OnError sets the handler for when the transport encounters an error.
func (t *WebSocketTransport) OnError(handler TransportErrorHandler) {
	t.mu.Lock()
	t.onErrorHandler = handler
	t.mu.Unlock()
}
