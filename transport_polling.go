package engineio

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

// PollingTransport is a transport that uses the HTTP long polling protocol.
type PollingTransport struct {
	// url is the URL of the transport.
	url *url.URL
	// client is the http.Client to use for the transport.
	client TransportClient
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
	// polling is a wait group that completes when the in-flight poll is complete.
	polling *sync.WaitGroup
}

// NewPollingTransport creates a new PollingTransport.
func NewPollingTransport(url *url.URL, opts TransportOptions) (Transport, error) {
	if url == nil {
		return nil, ErrURLRequired
	}

	var client TransportClient = http.DefaultClient
	if opts.Client != nil {
		client = opts.Client
	}

	var header = http.Header{}
	if opts.Header != nil {
		header = opts.Header
	}

	return &PollingTransport{
		url:    url,
		client: client,
		header: header,

		state:   TransportStateClosed,
		polling: &sync.WaitGroup{},
	}, nil
}

// Type returns the type of the transport.
func (t *PollingTransport) Type() TransportType {
	return TransportTypePolling
}

// State returns the state of the transport.
func (t *PollingTransport) State() TransportState {
	return t.state
}

// SetURL sets the URL for the transport.
func (t *PollingTransport) SetURL(url *url.URL) {
	t.url = url
}

// Open opens the transport.
func (t *PollingTransport) Open(ctx context.Context) {
	switch t.state {
	// These states are valid for opening the transport.
	case TransportStateClosed:
		break

	default:
		return
	}

	// Set the state to opening.
	t.state = TransportStateOpening

	// Poll to open the transport.
	t.poll(ctx)
}

// Pause pauses the transport.
func (t *PollingTransport) Pause(ctx context.Context) {
	switch t.state {
	// If the transport is opening or open, set the state to paused.
	case TransportStateOpening, TransportStateOpen:
		break

	default:
		return
	}

	// Set the state to paused.
	t.state = TransportStatePausing

	// Wait for polling to be complete.
	t.polling.Wait()

	// Set the state to paused.
	t.state = TransportStatePaused
}

// Send sends packets through the transport.
func (t *PollingTransport) Send(ctx context.Context, packets []Packet) error {
	switch t.state {
	// These states are valid for sending packets.
	case TransportStateOpen:
		break

	default:
		return nil
	}

	return t.write(ctx, EncodePayload(packets))
}

// Close closes the transport.
func (t *PollingTransport) Close(ctx context.Context) {
	switch t.state {
	// These states are valid for closing the transport.
	case TransportStateOpening, TransportStateOpen:
		break

	default:
		return
	}

	// Send a close packet
	t.Send(ctx, []Packet{{Type: PacketClose}})

	// Set the state to closing to prevent further polling and Close being called again.
	t.state = TransportStateClosing

	// Wait for polling to be complete
	t.polling.Wait()
}

// poll requests data from the server.
func (t *PollingTransport) poll(ctx context.Context) {
	// If polling is still being held, wait for it to complete.
	t.polling.Wait()

	// Increment the polling wait group.
	t.polling.Add(1)
	// Polling is complete when the function returns.
	defer t.polling.Done()

	res, err := t.request(ctx, nil)
	switch {
	case err != nil:
		t.onError(ctx, fmt.Errorf("polling error: %w", err))
		return

	case res.StatusCode != http.StatusOK:
		t.onError(ctx, fmt.Errorf("polling error: %d", res.StatusCode))
		return
	}

	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	switch {
	case err != nil:
		t.onError(ctx, fmt.Errorf("read error: %w", err))

	case len(b) != 0:
		t.onData(ctx, b)
	}
}

// write sends data to the server.
func (t *PollingTransport) write(ctx context.Context, data []byte) error {
	res, err := t.request(ctx, data)
	switch {
	case err != nil:
		return fmt.Errorf("write error: %w", err)

	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("write error: %d", res.StatusCode)

	default:
		return nil
	}
}

// request sends a request to the server. If data is not empty, it sends a POST request.
func (t *PollingTransport) request(ctx context.Context, data []byte) (*http.Response, error) {
	var (
		method           = http.MethodGet
		header           = t.header.Clone()
		body   io.Reader = http.NoBody
	)
	if len(data) != 0 {
		method = http.MethodPost
		header.Set("content-type", "text/plain; charset=UTF-8")
		body = bytes.NewReader(data)
	}

	u, err := url.Parse(t.url.String())
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	req.Header = header

	return t.client.Do(req)
}

// onData processes data received from the server.
func (t *PollingTransport) onData(ctx context.Context, data []byte) {
	packets, err := DecodePayload(data)
	if err != nil {
		t.onError(ctx, fmt.Errorf("decode error: %w", err))
		return
	}

	// Process each packet.
	for _, packet := range packets {
		switch {
		// If the packet is an open packet and the transport is opening, call the onOpen method.
		case packet.Type == PacketOpen && t.state == TransportStateOpening:
			t.onOpen(ctx)

		// If the packet is a close packet and the transport is not closed, call the onClose method.
		case packet.Type == PacketClose && t.state != TransportStateClosed:
			t.onClose(ctx)
		}

		t.onPacket(ctx, packet)
	}

	// Poll again if the transport is open or pausing.
	if t.state == TransportStateOpen || t.state == TransportStatePausing {
		go t.poll(ctx)
	}
}

// onOpen sets the state of the transport to open.
func (t *PollingTransport) onOpen(ctx context.Context) {
	t.state = TransportStateOpen

	if t.onOpenHandler != nil {
		t.onOpenHandler(ctx)
	}
}

// onClose sets the state of the transport to closed.
func (t *PollingTransport) onClose(ctx context.Context) {
	t.state = TransportStateClosed

	if t.onCloseHandler != nil {
		t.onCloseHandler(ctx)
	}
}

// onPacket calls the onPacket handler.
func (t *PollingTransport) onPacket(ctx context.Context, packet Packet) {
	if t.onPacketHandler != nil {
		t.onPacketHandler(ctx, packet)
	}
}

// onError calls the onError handler.
func (t *PollingTransport) onError(ctx context.Context, err error) {
	if t.onErrorHandler != nil {
		t.onErrorHandler(ctx, err)
	}
}

// OnOpen sets the handler for when the transport opens.
func (t *PollingTransport) OnOpen(handler TransportOpenHandler) {
	t.onOpenHandler = handler
}

// OnClose sets the handler for when the transport closes.
func (t *PollingTransport) OnClose(handler TransportCloseHandler) {
	t.onCloseHandler = handler
}

// OnPacket sets the handler for when the transport receives packets.
func (t *PollingTransport) OnPacket(handler TransportPacketHandler) {
	t.onPacketHandler = handler
}

// OnError sets the handler for when the transport encounters an error.
func (t *PollingTransport) OnError(handler TransportErrorHandler) {
	t.onErrorHandler = handler
}
