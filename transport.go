package engineio

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// Sentinel Errors.
var (
	ErrTransportRoundTripperClientRequired = errors.New("transport round tripper client is required")
	ErrURLRequired                         = errors.New("url is required")
	ErrUnexpectedStatus                    = errors.New("unexpected HTTP status")
)

// TransportOpenHandler is invoked once when a transport finishes opening, before
// any packet is delivered. It runs on a transport goroutine, so it must not
// block; the Socket uses it to drive the upgrade handshake and flush buffered
// writes.
type TransportOpenHandler func(context.Context)

// TransportCloseHandler is invoked once when a transport closes, whether the
// close was requested locally or observed from the peer. It runs on a transport
// goroutine and must not block. Transports guarantee it fires at most once.
type TransportCloseHandler func(context.Context)

// TransportPacketHandler is invoked for each packet a transport receives, in the
// order the packets arrive. It runs on the transport's read or poll goroutine,
// so a slow handler stalls further reads on that transport; it must not block
// indefinitely.
type TransportPacketHandler func(context.Context, Packet)

// TransportErrorHandler is invoked when a transport encounters an error, such as
// a failed request or a read failure. It runs on a transport goroutine and must
// not block. An error is typically followed by the transport closing.
type TransportErrorHandler func(context.Context, error)

// Transport is the client side of a single Engine.IO transport (long-polling,
// WebSocket, or WebTransport). The Socket drives a transport through its lifecycle
// and reacts to the transport's events through the OnX handlers; a transport never
// interprets packets itself. All methods are safe for concurrent use, and the OnX
// handlers fire on the transport's own goroutines.
type Transport interface {
	// Type reports which transport kind this is, so the Socket can decide whether
	// an offered upgrade is worth probing and record a successful WebSocket upgrade.
	Type() TransportType
	// State reports the transport's current lifecycle state. It is a snapshot taken
	// under the transport's lock; the state may change immediately after it returns.
	State() TransportState

	// SetURL replaces the URL the transport requests. The Socket calls it after the
	// handshake to attach the negotiated session id to subsequent requests. It is
	// safe to call concurrently with the transport's own requests.
	SetURL(url *url.URL)

	// Open starts the transport: a polling transport issues its first long-poll, and
	// a WebSocket or WebTransport transport dials and begins reading. It transitions
	// the transport from closed to open and is a no-op if the transport is not
	// closed, so a duplicate Open cannot start a second connection. OnOpen fires once
	// the transport is ready.
	Open(ctx context.Context)
	// Close shuts the transport down, sending a best-effort close packet to the peer
	// and tearing down the underlying connection. It is idempotent and fires OnClose
	// at most once; a close already triggered by the peer is not duplicated.
	Close(ctx context.Context)
	// Pause stops a transport from sending or receiving and waits for any in-flight
	// work to drain. The Socket calls it on the old transport during an upgrade so a
	// packet that transport is mid-delivery is delivered before traffic moves to the
	// new transport, preserving ordering. For a transport with nothing to drain
	// (WebSocket or WebTransport) it is a no-op.
	Pause(ctx context.Context)

	// Send writes packets to the peer. A polling transport sends them as one POST; a
	// WebSocket transport writes one frame per packet; a WebTransport transport writes
	// length-framed packets on its stream. Packets sent while the transport is not
	// open are dropped (returning nil) rather than erroring, so the Socket's buffering
	// decides what is retained. It returns an error only when a write actually fails.
	Send(ctx context.Context, packets []Packet) error

	// OnOpen registers the handler invoked when the transport opens. It replaces any
	// previously registered handler; passing nil clears it. The Socket clears the
	// handlers before closing a transport so the teardown cannot re-enter the Socket.
	OnOpen(handler TransportOpenHandler)
	// OnClose registers the handler invoked when the transport closes. It replaces
	// any previously registered handler; passing nil clears it.
	OnClose(handler TransportCloseHandler)
	// OnPacket registers the handler invoked for each packet the transport receives.
	// It replaces any previously registered handler; passing nil clears it.
	OnPacket(handler TransportPacketHandler)
	// OnError registers the handler invoked when the transport encounters an error.
	// It replaces any previously registered handler; passing nil clears it.
	OnError(handler TransportErrorHandler)
}

// TransportType names a transport kind. It doubles as the wire value of the
// "transport" query parameter and the entries of an open packet's upgrade list,
// so its string form is the protocol name rather than a Go identifier.
type TransportType string

// String returns the transport name as it appears on the wire ("polling",
// "websocket", or "webtransport").
func (t TransportType) String() string {
	return string(t)
}

const (
	// TransportTypePolling represents a polling transport.
	TransportTypePolling TransportType = "polling"
	// TransportTypeWebSocket represents a WebSocket transport.
	TransportTypeWebSocket TransportType = "websocket"
	// TransportTypeWebTransport represents a WebTransport (HTTP/3) transport.
	TransportTypeWebTransport TransportType = "webtransport"
)

// TransportState is the lifecycle state of a transport. Transports advance
// through these states under their own lock and use them to make Open, Close,
// and Pause idempotent and to decide whether a poll or write may proceed.
type TransportState string

// String returns the state name for logging and test output.
func (s TransportState) String() string {
	return string(s)
}

const (
	// TransportStateOpening represents a transport that is opening.
	TransportStateOpening TransportState = "opening"
	// TransportStateOpen represents an open transport.
	TransportStateOpen TransportState = "open"
	// TransportStateClosing represents a transport that is closing.
	TransportStateClosing TransportState = "closing"
	// TransportStateClosed represents a transport that is closed.
	TransportStateClosed TransportState = "closed"
	// TransportStatePausing represents a transport that is pausing.
	TransportStatePausing TransportState = "pausing"
	// TransportStatePaused represents a transport that is paused.
	TransportStatePaused TransportState = "paused"
)

// TransportClient is the minimal HTTP surface a transport needs: a way to
// execute a request. *http.Client satisfies it, and it is accepted (rather than
// the concrete type) so callers can inject a custom client or a mock in tests.
//
//go:generate mockgen -package=engineio_test -destination=mock_transport_client_test.go -source=transport.go TransportClient
type TransportClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// TransportRoundTripper adapts a TransportClient into an http.RoundTripper. The
// WebSocket transport needs an *http.Client to dial, so a caller-supplied
// TransportClient is wrapped here rather than mutating the shared
// http.DefaultClient.
type TransportRoundTripper struct {
	// Client is the underlying client requests are forwarded to. RoundTrip returns
	// ErrTransportRoundTripperClientRequired if it is nil.
	Client TransportClient
}

// RoundTrip forwards the request to the wrapped TransportClient, satisfying
// http.RoundTripper. It returns ErrTransportRoundTripperClientRequired when no
// client was set.
func (t *TransportRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Client == nil {
		return nil, ErrTransportRoundTripperClientRequired
	}
	return t.Client.Do(req)
}

// TransportConstructor builds a transport for a target URL using the given
// client and headers. The Transports registry maps each TransportType to its
// constructor, and the Socket calls the matching one when opening or upgrading.
type TransportConstructor func(url *url.URL, client TransportClient, header http.Header) (Transport, error)

// Transports maps each transport type to the constructor that builds it. A
// Socket snapshots this registry at construction, so replacing an entry affects
// only sockets created afterwards; this is the seam for injecting a custom or
// mock transport in tests.
var Transports = map[TransportType]TransportConstructor{
	TransportTypePolling:   NewPollingTransport,
	TransportTypeWebSocket: NewWebSocketTransport,
}
