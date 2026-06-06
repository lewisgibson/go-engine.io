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

// PollingTransport is a transport that uses the HTTP long-polling protocol.
// Long-poll GET requests carry a per-request cache-busting timestamp in the "t"
// query parameter, mirroring the engine.io-client default, so a caching
// intermediary cannot serve a stale poll body. This is layered on the server's
// Cache-Control: no-store response header rather than replacing it.
type PollingTransport struct {
	// client and header are set once at construction and never mutated.
	client TransportClient
	header http.Header

	// mu guards url, the handlers, and state, all of which are read and written
	// from both caller goroutines and the polling goroutine.
	mu              sync.Mutex
	url             *url.URL
	onOpenHandler   TransportOpenHandler
	onCloseHandler  TransportCloseHandler
	onPacketHandler TransportPacketHandler
	onErrorHandler  TransportErrorHandler
	state           TransportState

	// pollMu serializes long-poll requests so only one is ever in flight, and
	// lets Pause and Close wait for the in-flight request to finish.
	pollMu sync.Mutex
	// pollWG tracks an in-flight poll's dispatch, which runs after pollMu is
	// released. Pause waits on it so a message a poll delivers is not reordered
	// after the new transport's traffic during an upgrade.
	pollWG sync.WaitGroup
}

// NewPollingTransport creates a new PollingTransport. A nil client defaults to
// http.DefaultClient and a nil header to an empty header.
func NewPollingTransport(url *url.URL, client TransportClient, header http.Header) (Transport, error) {
	if url == nil {
		return nil, ErrURLRequired
	}

	if client == nil {
		client = http.DefaultClient
	}

	if header == nil {
		header = http.Header{}
	}

	return &PollingTransport{
		client: client,
		header: header,
		url:    url,
		state:  TransportStateClosed,
	}, nil
}

// Type returns the type of the transport.
func (t *PollingTransport) Type() TransportType {
	return TransportTypePolling
}

// State returns the state of the transport.
func (t *PollingTransport) State() TransportState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// SetURL sets the URL for the transport.
func (t *PollingTransport) SetURL(url *url.URL) {
	t.mu.Lock()
	t.url = url
	t.mu.Unlock()
}

// Open opens the transport by issuing the first long-poll.
func (t *PollingTransport) Open(ctx context.Context) {
	if !t.beginOpen() {
		return
	}

	t.poll(ctx)
}

// beginOpen claims the opening transition under a single lock, so Open can poll
// without touching the mutex. It reports false when the transport is not in a
// state that can be opened.
func (t *PollingTransport) beginOpen() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state != TransportStateClosed {
		return false
	}
	t.state = TransportStateOpening

	return true
}

// Pause stops polling and waits for any in-flight poll to finish. It is used
// while a transport upgrade is being probed.
func (t *PollingTransport) Pause(_ context.Context) {
	if !t.beginPause() {
		return
	}

	// Take pollMu to wait for the in-flight request (which records its dispatch on
	// pollWG before releasing pollMu), mark the transport paused, then release
	// pollMu and wait for the dispatch. The wait happens after pollMu is released
	// because the dispatch may itself close the transport, and Close also takes
	// pollMu; the paused state stops any new poll from starting in the meantime.
	t.pollMu.Lock()
	t.mu.Lock()
	t.state = TransportStatePaused
	t.mu.Unlock()
	t.pollMu.Unlock()

	t.pollWG.Wait()
}

// beginPause claims the pausing transition under a single lock. It reports false
// when the transport is not running and so cannot be paused.
func (t *PollingTransport) beginPause() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state == TransportStateOpening || t.state == TransportStateOpen {
		t.state = TransportStatePausing
		return true
	}

	return false
}

// Send sends packets through the transport as a single POST request.
func (t *PollingTransport) Send(ctx context.Context, packets []Packet) error {
	t.mu.Lock()
	state := t.state
	t.mu.Unlock()

	if state != TransportStateOpen {
		return nil
	}

	return t.write(ctx, EncodePayload(packets))
}

// Close closes the transport by sending a close packet and waiting for any
// in-flight poll to finish.
func (t *PollingTransport) Close(ctx context.Context) {
	t.mu.Lock()
	if t.state != TransportStateOpening && t.state != TransportStateOpen {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	// Tell the server the transport is closing. This is best-effort: the
	// connection is going away regardless, so a failure is surfaced through the
	// error handler rather than blocking the close.
	if err := t.Send(ctx, []Packet{{Type: PacketClose}}); err != nil {
		t.onError(ctx, fmt.Errorf("sending close packet: %w", err))
	}

	if !t.beginClose() {
		return
	}

	// Wait for the in-flight poll to finish, then mark the transport closed.
	// onClose is idempotent, so a close packet observed by the in-flight poll
	// closing first is harmless.
	t.pollMu.Lock()
	defer t.pollMu.Unlock()

	t.onClose(ctx)
}

// beginClose claims the closing transition under a single lock. It reports false
// when an in-flight poll has already observed the server's close packet and
// closed the transport, so the caller does not overwrite the closed state and
// let onClose fire a second time.
func (t *PollingTransport) beginClose() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state == TransportStateClosed {
		return false
	}
	t.state = TransportStateClosing

	return true
}

// poll issues a single long-poll request and dispatches the packets it returns.
// pollMu serializes the requests so only one is in flight at a time and lets
// Pause and Close wait for it. The lock is released before any handler runs: a
// handler may close the transport (which waits on pollMu), so holding it across
// the dispatch would deadlock the poll goroutine against itself.
func (t *PollingTransport) poll(ctx context.Context) {
	t.pollMu.Lock()

	t.mu.Lock()
	state := t.state
	t.mu.Unlock()

	// Only poll while opening or open; a state change to pausing/closing/closed
	// since this poll was scheduled means it must not run.
	if state != TransportStateOpening && state != TransportStateOpen {
		t.pollMu.Unlock()
		return
	}

	res, err := t.request(ctx, nil)
	if err != nil {
		// Mark the dispatch in flight before releasing pollMu so Pause, which
		// acquires pollMu next, observes it and waits for the dispatch to finish.
		t.pollWG.Add(1)
		t.pollMu.Unlock()
		t.onError(ctx, fmt.Errorf("polling: %w", err))
		t.pollWG.Done()
		return
	}
	defer res.Body.Close() //nolint:errcheck // best-effort close of the polling response body

	var body []byte
	var status = res.StatusCode
	if status == http.StatusOK {
		body, err = io.ReadAll(res.Body)
	}

	// Dispatch outside pollMu so a handler that closes the transport does not
	// deadlock against the in-flight poll. pollWG keeps Pause waiting for the
	// dispatch even though pollMu is already released.
	t.pollWG.Add(1)
	t.pollMu.Unlock()
	defer t.pollWG.Done()

	switch {
	case status != http.StatusOK:
		t.onError(ctx, fmt.Errorf("polling: %w: %d", ErrUnexpectedStatus, status))

	case err != nil:
		t.onError(ctx, fmt.Errorf("reading poll response: %w", err))

	case len(body) != 0:
		t.onData(ctx, body)
	}
}

// write sends data to the server as a POST request.
func (t *PollingTransport) write(ctx context.Context, data []byte) error {
	res, err := t.request(ctx, data)
	if err != nil {
		return fmt.Errorf("writing poll body: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck // best-effort close of the polling response body

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("writing poll body: %w: %d", ErrUnexpectedStatus, res.StatusCode)
	}

	return nil
}

// request sends a request to the server. A non-empty body is sent as a POST.
// GET requests are the long-polls and carry a per-request cache-busting
// timestamp in the "t" query parameter, mirroring the engine.io-client default,
// so a caching intermediary cannot serve a stale poll body; POST writes are not
// timestamped. The parameter is appended to preserve the existing query order
// (EIO/sid/transport) rather than re-encoding through url.Values.
func (t *PollingTransport) request(ctx context.Context, data []byte) (*http.Response, error) {
	t.mu.Lock()
	target := t.url.String()
	t.mu.Unlock()

	var (
		method           = http.MethodGet
		header           = t.header.Clone()
		body   io.Reader = http.NoBody
	)
	if len(data) != 0 {
		method = http.MethodPost
		header.Set("Content-Type", "text/plain; charset=UTF-8")
		body = bytes.NewReader(data)
	} else {
		// Append the cache-busting timestamp to the GET only. The value's
		// characters are already URL-safe, but escape it anyway for safety, and
		// append the single parameter so the existing query order is preserved.
		target += "&t=" + url.QueryEscape(defaultYeast.next())
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header = header

	return t.client.Do(req)
}

// onData decodes a payload and dispatches its packets, then schedules the next
// poll if the transport is still open.
func (t *PollingTransport) onData(ctx context.Context, data []byte) {
	packets, err := DecodePayload(ProtocolVersion4, data)
	if err != nil {
		t.onError(ctx, fmt.Errorf("decoding payload: %w", err))
		return
	}

	for _, packet := range packets {
		t.mu.Lock()
		state := t.state
		t.mu.Unlock()

		switch {
		// An open packet during opening transitions the transport to open.
		case packet.Type == PacketOpen && state == TransportStateOpening:
			t.onOpen(ctx)

		// A close packet on a live transport closes it.
		case packet.Type == PacketClose && state != TransportStateClosed:
			t.onClose(ctx)
		}

		t.onPacket(ctx, packet)
	}

	t.mu.Lock()
	open := t.state == TransportStateOpen
	t.mu.Unlock()

	if open {
		go t.poll(ctx)
	}
}

// onOpen marks the transport open and notifies the handler.
func (t *PollingTransport) onOpen(ctx context.Context) {
	t.mu.Lock()
	t.state = TransportStateOpen
	handler := t.onOpenHandler
	t.mu.Unlock()

	if handler != nil {
		handler(ctx)
	}
}

// onClose marks the transport closed and notifies the handler. It is idempotent.
func (t *PollingTransport) onClose(ctx context.Context) {
	t.mu.Lock()
	if t.state == TransportStateClosed {
		t.mu.Unlock()
		return
	}
	t.state = TransportStateClosed
	handler := t.onCloseHandler
	t.mu.Unlock()

	if handler != nil {
		handler(ctx)
	}
}

// onPacket notifies the packet handler.
func (t *PollingTransport) onPacket(ctx context.Context, packet Packet) {
	t.mu.Lock()
	handler := t.onPacketHandler
	t.mu.Unlock()

	if handler != nil {
		handler(ctx, packet)
	}
}

// onError notifies the error handler.
func (t *PollingTransport) onError(ctx context.Context, err error) {
	t.mu.Lock()
	handler := t.onErrorHandler
	t.mu.Unlock()

	if handler != nil {
		handler(ctx, err)
	}
}

// OnOpen sets the handler for when the transport opens.
func (t *PollingTransport) OnOpen(handler TransportOpenHandler) {
	t.mu.Lock()
	t.onOpenHandler = handler
	t.mu.Unlock()
}

// OnClose sets the handler for when the transport closes.
func (t *PollingTransport) OnClose(handler TransportCloseHandler) {
	t.mu.Lock()
	t.onCloseHandler = handler
	t.mu.Unlock()
}

// OnPacket sets the handler for when the transport receives packets.
func (t *PollingTransport) OnPacket(handler TransportPacketHandler) {
	t.mu.Lock()
	t.onPacketHandler = handler
	t.mu.Unlock()
}

// OnError sets the handler for when the transport encounters an error.
func (t *PollingTransport) OnError(handler TransportErrorHandler) {
	t.mu.Lock()
	t.onErrorHandler = handler
	t.mu.Unlock()
}
