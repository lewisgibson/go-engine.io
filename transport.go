package engineio

import (
	"context"
	"net/http"
	"net/url"
)

// TransportOpenHandler is a function that is called when a transport opens.
type TransportOpenHandler func(context.Context)

// TransportCloseHandler is a function that is called when a transport closes.
type TransportCloseHandler func(context.Context)

// TransportPacketHandler is a function that is called when a transport receives packets.
type TransportPacketHandler func(context.Context, Packet)

// TransportErrorHandler is a function that is called when a transport encounters an error.
type TransportErrorHandler func(context.Context, error)

// Transport is an interface that represents a transport.
type Transport interface {
	// Type returns the type of the transport.
	Type() TransportType
	// State returns the state of the transport.
	State() TransportState

	// SetURL sets the URL for the transport.
	SetURL(url *url.URL)

	// Open opens the transport.
	Open(ctx context.Context)
	// Close closes the transport.
	Close(ctx context.Context)
	// Pause pauses the transport.
	Pause(ctx context.Context)

	// Send sends packets through the transport.
	Send(ctx context.Context, packets []Packet) error

	// OnOpen sets the handler for when the transport opens.
	OnOpen(handler TransportOpenHandler)
	// OnClose sets the handler for when the transport closes.
	OnClose(handler TransportCloseHandler)
	// OnPacket sets the handler for when the transport receives packets.
	OnPacket(handler TransportPacketHandler)
	// OnError sets the handler for when the transport encounters an error.
	OnError(handler TransportErrorHandler)
}

// TransportType represents the type of a transport.
type TransportType string

const (
	// TransportTypePolling represents a polling transport.
	TransportTypePolling TransportType = "polling"
	// TransportTypeWebSocket represents a WebSocket transport.
	TransportTypeWebSocket TransportType = "websocket"
)

// TransportState represents the state of a transport.
type TransportState string

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
	// TransportStatePaused represents a transport that is paused. A paused transport will not send or receive any packets.
	TransportStatePaused TransportState = "paused"
)

// TransportClient is an interface that represents an HTTP client.
//
//go:generate mockgen -package=mocks -destination=./internal/mocks/mock_transport_client.go -source=transport.go TransportClient
type TransportClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// TransportOptions represents options for a transport.
type TransportOptions struct {
	Client TransportClient
	Header http.Header
}

// TransportConstructor is a function that creates a new transport.
type TransportConstructor func(url *url.URL, opts TransportOptions) Transport

// Transports is a map of transport types to transport constructors.
var Transports = map[TransportType]TransportConstructor{
	TransportTypePolling:   NewPollingTransport,
	TransportTypeWebSocket: NewWebSocketTransport,
}