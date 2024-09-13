package engineio

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"

	"nhooyr.io/websocket"
)

// WebSocketTransport is a transport that uses the WebSocket protocol.
type WebSocketTransport struct {
	// url is the URL of the transport.
	url *url.URL
	// client is the http.Client to use for the transport.
	client *http.Client
	// header is the header to use for the transport.
	header http.Header

	// onOpenHandler is the handler for when the transport opens.
	onOpenHandler TransportOpenHandler
	// onCloseHandler is the handler for when the transport closes.
	onCloseHandler TransportCloseHandler
	// onPacketHandler is the handler for when the transport receives packets.
	onPacketHandler TransportPacketHandler
	// onErrorHandler is the handler for when the transport encounters an error.
	onErrorHandler TransportErrorHandler

	// state is the state of the transport.
	state TransportState
	// ws is the websocket connection.
	ws *websocket.Conn
}

// NewWebSocketTransport creates a new WebSocket transport.
func NewWebSocketTransport(url *url.URL, opts TransportOptions) (Transport, error) {
	if url == nil {
		return nil, ErrURLRequired
	}

	var client = http.DefaultClient
	if opts.Client != nil {
		client.Transport = &TransportRoundTripper{
			Client: opts.Client,
		}
	}

	var header = http.Header{}
	if opts.Header != nil {
		header = opts.Header
	}

	return &WebSocketTransport{
		url:    url,
		client: client,
		header: header,
		state:  TransportStateClosed,
	}, nil
}

// Type returns the type of the transport.
func (t *WebSocketTransport) Type() TransportType {
	return TransportTypeWebSocket
}

// State returns the state of the transport.
func (t *WebSocketTransport) State() TransportState {
	return t.state
}

// SetURL sets the URL of the transport.
func (t *WebSocketTransport) SetURL(url *url.URL) {
	t.url = url
}

// Open opens the transport.
func (t *WebSocketTransport) Open(ctx context.Context) {
	switch t.state {
	// These states are valid states to open the transport.
	case TransportStateClosed:
		break

	default:
		return
	}

	// Set the state of the transport to opening.
	t.state = TransportStateOpening

	// Dial the websocket connection.
	ws, res, err := websocket.Dial(ctx, t.url.String(), &websocket.DialOptions{
		HTTPClient: t.client,
		HTTPHeader: t.header,
	})
	if err != nil {
		t.state = TransportStateClosed
		if res != nil {
			defer res.Body.Close()
			b, berr := io.ReadAll(res.Body)
			if berr != nil {
				t.onError(ctx, fmt.Errorf("failed to dial websocket connection: %w\n%s", err, string(b)))
				return
			}
		}
		t.onError(ctx, fmt.Errorf("failed to dial websocket connection: %w", err))
		return
	}
	t.ws = ws

	// The transport is now open.
	t.onOpen(ctx)

	// Read packets from the websocket connection.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return

			default:
				// Kill the goroutine if the transport state is closed.
				if t.state == TransportStateClosed {
					return
				}

				// Read packets from the websocket connection.
				t.read(ctx)
			}
		}
	}()
}

// Close closes the transport.
func (t *WebSocketTransport) Close(ctx context.Context) {
	switch t.state {
	// These states are valid states to close the transport.
	case TransportStateOpening, TransportStateOpen:
		break

	default:
		return
	}

	// Send a close packet to the server.
	t.Send(ctx, []Packet{{Type: PacketClose}})

	// Set the state to closing to prevent Close being called again.
	t.state = TransportStateClosing
}

// Pause pauses the transport. It is a no-op for the WebSocket transport.
func (t *WebSocketTransport) Pause(ctx context.Context) {
	// Do nothing.
}

// Send sends packets to the transport.
func (t *WebSocketTransport) Send(ctx context.Context, packets []Packet) error {
	switch t.state {
	// These states are valid states to send packets.
	case TransportStateOpen:
		break

	default:
		return nil
	}

	// Write the payload to the websocket connection.
	return t.ws.Write(ctx, websocket.MessageText, EncodePayload(packets))
}

// read reads packets from the websocket connection.
func (t *WebSocketTransport) read(ctx context.Context) {
	messageType, b, err := t.ws.Read(ctx)
	switch {
	case err != nil:
		t.onError(ctx, fmt.Errorf("failed to read from websocket connection: %w", err))
		return

	// Binary messages are not supported.
	case messageType != websocket.MessageText:
		t.onError(ctx, fmt.Errorf("unsupported message type: %d", messageType))
		return
	}

	// Decode the payload into packets.
	packets, err := DecodePayload(b)
	if err != nil {
		t.onError(ctx, fmt.Errorf("failed to decode payload: %w", err))
		return
	}

	// Handle each packet.
	for _, packet := range packets {
		switch {
		// If the packet is a close packet, call the onClose method.
		case packet.Type == PacketClose:
			t.onClose(ctx)
		}

		// A packet is received, call the onPacket method.
		t.onPacket(ctx, packet)
	}
}

// onOpen sets the state of the transport to open.
func (t *WebSocketTransport) onOpen(ctx context.Context) {
	t.state = TransportStateOpen

	if t.onOpenHandler != nil {
		t.onOpenHandler(ctx)
	}
}

// onClose sets the state of the transport to closed.
func (t *WebSocketTransport) onClose(ctx context.Context) {
	// Set the state of the transport to closed.
	t.state = TransportStateClosed

	// The websocket connection is closed, so the finalizer is no longer needed.
	runtime.SetFinalizer(t.ws, nil)

	// Clear the reference to the websocket connection.
	t.ws = nil

	if t.onCloseHandler != nil {
		t.onCloseHandler(ctx)
	}
}

// onPacket calls the onPacket handler.
func (t *WebSocketTransport) onPacket(ctx context.Context, packet Packet) {
	if t.onPacketHandler != nil {
		t.onPacketHandler(ctx, packet)
	}
}

// onError calls the onError handler.
func (t *WebSocketTransport) onError(ctx context.Context, err error) {
	if t.onErrorHandler != nil {
		t.onErrorHandler(ctx, err)
	}
}

// OnOpen sets the handler for when the transport opens.
func (t *WebSocketTransport) OnOpen(handler TransportOpenHandler) {
	t.onOpenHandler = handler
}

// OnClose sets the handler for when the transport closes.
func (t *WebSocketTransport) OnClose(handler TransportCloseHandler) {
	t.onCloseHandler = handler
}

// OnPacket sets the handler for when the transport receives packets.
func (t *WebSocketTransport) OnPacket(handler TransportPacketHandler) {
	t.onPacketHandler = handler
}

// OnError sets the handler for when the transport encounters an error.
func (t *WebSocketTransport) OnError(handler TransportErrorHandler) {
	t.onErrorHandler = handler
}
