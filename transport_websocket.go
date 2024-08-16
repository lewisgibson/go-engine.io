package engineio

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"nhooyr.io/websocket"
)

type WebSocketTransport struct {
	// url is the URL of the transport.
	url *url.URL
	// client is the http.Client to use for the transport.
	client *http.Client
	// header is the header to use for the transport.
	header http.Header
	// state is the state of the transport.
	state TransportState
	// onOpenHandler is the handler for when the transport opens.
	onOpenHandler TransportOpenHandler
	// onCloseHandler is the handler for when the transport closes.
	onCloseHandler TransportCloseHandler
	// onPacketHandler is the handler for when the transport receives packets.
	onPacketHandler TransportPacketHandler
	// onErrorHandler is the handler for when the transport encounters an error.
	onErrorHandler TransportErrorHandler
	// ws is the websocket connection.
	ws *websocket.Conn
}

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
	if t.state != TransportStateClosed {
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

	t.state = TransportStateOpen

	// Call the onOpenHandler if it is set.
	if t.onOpenHandler != nil {
		t.onOpenHandler(ctx)
	}
}

// Close closes the transport.
func (t *WebSocketTransport) Close(ctx context.Context) {
	switch t.state {
	// These states are valid states to close the transport.
	case TransportStateOpening, TransportStateOpen, TransportStateClosing:
		break

	default:
		return
	}

	t.Send(ctx, []Packet{{Type: PacketClose}})
}

// Pause pauses the transport. It is a no-op for the WebSocket transport.
func (t *WebSocketTransport) Pause(ctx context.Context) {
	// Do nothing.
}

// Send sends packets to the transport.
func (t *WebSocketTransport) Send(ctx context.Context, packets []Packet) error {
	switch t.state {
	// These states are valid states to send packets.
	case TransportStateOpen, TransportStateClosing:
		break

	default:
		return nil
	}

	b, err := EncodePayload(packets)
	if err != nil {
		return err
	}

	return t.ws.Write(ctx, websocket.MessageText, b)
}

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

	for _, packet := range packets {
		switch {
		// If the packet is a close packet, call the onClose method.
		case packet.Type == PacketClose:
			t.onClose(ctx)
		}

		// Call the onPacketHandler if it is set...
		if t.onPacketHandler != nil {
			t.onPacketHandler(ctx, packet)
		}
	}
}

// onError calls the onError handler.
func (t *WebSocketTransport) onError(ctx context.Context, err error) {
	if t.onErrorHandler != nil {
		t.onErrorHandler(ctx, err)
	}
}

// onClose sets the state of the transport to closed.
func (t *WebSocketTransport) onClose(ctx context.Context) {
	t.state = TransportStateClosed

	// Call the onCloseHandler if it is set.
	if t.onCloseHandler != nil {
		t.onCloseHandler(ctx)
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
