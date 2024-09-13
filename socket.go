package engineio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// ErrInvalidURL is an error that is returned when a URI is invalid.
var ErrInvalidURL = errors.New("invalid URL")

// SocketState represents the state of a socket.
type SocketState string

const (
	// SocketStateOpen represents an open socket.
	SocketStateOpen SocketState = "open"
	// SocketStateOpening represents a socket that is opening.
	SocketStateOpening SocketState = "opening"
	// SocketStateClosed represents a socket that is closed.
	SocketStateClosed SocketState = "closed"
	// SocketStateClosing represents a socket that is closing.
	SocketStateClosing SocketState = "closing"
)

// SocketOpenHandler is a function that is called when a socket opens.
type SocketOpenHandler func()

// SocketCloseHandler is a function that is called when a socket closes.
type SocketCloseHandler func(reason string, cause error)

// SocketPacketHandler is a function that is called when a socket receives packets.
type SocketPacketHandler func(Packet)

// SocketMessageHandler is a function that is called when a socket receives a message packet.
type SocketMessageHandler func([]byte)

// SocketErrorHandler is a function that is called when a socket encounters an error.
type SocketErrorHandler func(error)

// SocketOptions is a struct that represents options for a socket connection.
type SocketOptions struct {
	// Client is the http.Client to use for the transport.
	// Default: a new http.Client
	Client TransportClient
	// Header is the headers to use for the transport.
	// Default: an empty http.Header
	Header *http.Header
	// Upgrade determines whether the client should try to upgrade the transport from long-polling to something better.
	// Default: true
	Upgrade *bool
	// RememberUpgrade determines whether the client should remember to upgrade to a better transport from the previous connection upgrade.
	// Default: false
	RememberUpgrade *bool
	// Transports is a list of transports to try (in order).
	// Engine.io always attempts to connect directly with the first one, provided the feature detection test for it passes.
	// Default: ['polling', 'websocket', 'webtransport']
	Transports *[]TransportType
	// TryAllTransports determines whether the client should try all transports in the list before giving up.
	// Default: false
	TryAllTransports *bool
}

// Socket is a struct that represents a socket connection.
type Socket struct {
	// url is the target to connect to.
	url *url.URL
	// client is the TransportClient to use for the transport.
	client TransportClient
	// header is the headers to use for the transport.
	header http.Header
	// upgrade determines whether the client should try to upgrade the transport from long-polling to something better.
	upgrade bool
	// rememberUpgrade determines whether the client should remember to upgrade to a better transport from the previous connection upgrade.
	rememberUpgrade bool
	// transports is a list of transports to try (in order).
	transports []TransportType
	// tryAllTransports determines whether the client should try all transports in the list before giving up.
	tryAllTransports bool

	// onOpenHandler is the handler for when the socket opens.
	onOpenHandler SocketOpenHandler
	// onCloseHandler is the handler for when the socket closes.
	onCloseHandler SocketCloseHandler
	// onPacketHandler is the handler for when the socket receives packets.
	onPacketHandler SocketPacketHandler
	// onErrorHandler is the handler for when the socket receives a message packet.
	onMessageHandler SocketMessageHandler
	// onErrorHandler is the handler for when the socket encounters an error.
	onErrorHandler SocketErrorHandler

	// sessionID is a unique session identifier.
	sessionID string
	// The ping interval, used in the heartbeat mechanism (in milliseconds).
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#heartbeat
	pingInterval time.Duration
	// The ping timeout, used in the heartbeat mechanism (in milliseconds).
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#heartbeat
	pingTimeout time.Duration
	// The maximum number of bytes per chunk, used by the client to aggregate packets into payloads.
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#packet-encoding
	maxPayload int

	// state is the state of the socket.
	state SocketState
	// transport is the transport for the socket.
	transport Transport
	// priorUpgradeSuccess determines whether the prior upgrade was successful.
	priorUpgradeSuccess bool
	// pingTimeoutTimer is a timer that closes the transport if the server does not respond to pings.
	pingTimeoutTimer *time.Timer
}

// NewSocket creates a new Socket.
func NewSocket(serverURL string, socketOptions ...SocketOptions) (*Socket, error) {
	url, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	var client TransportClient = &http.Client{}
	if len(socketOptions) != 0 && socketOptions[0].Client != nil {
		client = socketOptions[0].Client
	}

	var header = http.Header{}
	if len(socketOptions) != 0 && socketOptions[0].Header != nil {
		header = *socketOptions[0].Header
	}

	var upgrade = true
	if len(socketOptions) != 0 && socketOptions[0].Upgrade != nil {
		upgrade = *socketOptions[0].Upgrade
	}

	var tryAllTransports = false
	if len(socketOptions) != 0 && socketOptions[0].TryAllTransports != nil {
		tryAllTransports = *socketOptions[0].TryAllTransports
	}

	var rememberUpgrade = false
	if len(socketOptions) != 0 && socketOptions[0].RememberUpgrade != nil {
		rememberUpgrade = *socketOptions[0].RememberUpgrade
	}

	var transports = []TransportType{
		TransportTypePolling,
		TransportTypeWebSocket,
	}
	if len(socketOptions) != 0 && socketOptions[0].Transports != nil {
		transports = *socketOptions[0].Transports
	}

	return &Socket{
		url:              url,
		client:           client,
		header:           header,
		upgrade:          upgrade,
		rememberUpgrade:  rememberUpgrade,
		transports:       transports,
		tryAllTransports: tryAllTransports,

		state: SocketStateClosed,
	}, nil
}

// Open opens the socket.
func (s *Socket) Open(ctx context.Context) {
	// The socket must be closed to begin opening.
	if s.state != SocketStateClosed {
		return
	}

	// Create a new transport.
	var err error
	s.transport, err = s.createTransport()
	if err != nil {
		// It may not be possible to create a transport if no transports are available.
		if s.onErrorHandler != nil {
			s.onErrorHandler(errors.New("no transports available"))
		}
		return
	}

	// Bind the transport handlers.
	s.transport.OnPacket(s.onPacket)
	s.transport.OnError(s.onError)
	s.transport.OnClose(func(ctx context.Context) {
		s.onClose(ctx, "transport closed", nil)
	})

	// Open the transport.
	s.state = SocketStateOpening
	s.transport.Open(ctx)
}

// createTransport creates a new transport.
func (s *Socket) createTransport() (Transport, error) {
	if len(s.transports) == 0 {
		return nil, errors.New("no transports available")
	}

	var transportType TransportType
	switch {
	// The WebSocket transport can be used if a prior upgrade was successful and the WebSocket transport is available.
	case s.rememberUpgrade && s.priorUpgradeSuccess && slices.Contains(s.transports, TransportTypeWebSocket):
		transportType = TransportTypeWebSocket

	// Use the first transport in the list.
	default:
		transportType = s.transports[0]
	}

	// Resolve the URL for the transport.
	url, err := s.resolveURL(transportType)
	if err != nil {
		return nil, fmt.Errorf("resolve URL: %w", err)
	}

	// Create a new transport.
	return Transports[transportType](url, TransportOptions{
		Client: s.client,
		Header: s.header,
	})
}

// resolveURL resolves the URL for the transport by applying query parameters to the base URL.
func (s *Socket) resolveURL(transportType TransportType) (*url.URL, error) {
	// Make a copy of the URL to avoid modifying the original URL.
	u, err := url.Parse(s.url.String())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	// Set query parameters.
	q := s.url.Query()
	q.Set("EIO", strconv.Itoa(Protocol))
	q.Set("transport", string(transportType))
	if s.sessionID != "" {
		q.Set("sid", s.sessionID)
	}
	u.RawQuery = q.Encode()

	return u, nil
}

// Close closes the socket.
func (s *Socket) Close(ctx context.Context) {
	switch s.state {
	// The socket must be open or opening to send packets.
	case SocketStateOpen, SocketStateOpening:
		break

	default:
		return
	}

	s.state = SocketStateClosing
	s.transport.Close(ctx)
}

// Send sends packets through the socket.
func (s *Socket) Send(ctx context.Context, packets []Packet) error {
	switch s.state {
	// The socket must be open to send packets.
	case SocketStateOpen:
		break

	default:
		return nil
	}

	// TODO: Enforce the maximum payload size.
	// Loop over the packets building up the largest payload possible until s.maxPayload is reached.
	// If more packets exist, begin adding those to the next payload. Continue until all packets are sent.
	return s.transport.Send(ctx, packets)
}

func (s *Socket) onError(ctx context.Context, err error) {
	if s.onErrorHandler != nil {
		s.onErrorHandler(fmt.Errorf("transport error: %w", err))
	}

	// The transport could not upgrade because the connection was unsuccessful.
	s.priorUpgradeSuccess = false

	// If multiple transports exist, try the next one.
	if s.tryAllTransports && len(s.transports) > 1 && s.state == SocketStateOpening {
		s.transports = s.transports[1:] // The current transport does not work so it is removed from the list.

		// If the transport is not closed, close it.
		s.transport.OnOpen(nil)
		s.transport.OnClose(nil)
		s.transport.OnPacket(nil)
		s.transport.OnError(nil)
		s.transport.Close(ctx)

		// Create a new transport.
		s.Open(ctx)
		return
	}

	s.onClose(ctx, "transport error", err)
}

// onClose is called when the transport closes.
func (s *Socket) onClose(ctx context.Context, reason string, cause error) {
	switch s.state {
	// These states are valid states for closing the socket.
	case SocketStateOpen, SocketStateOpening, SocketStateClosing:
		break

	default:
		return
	}

	// Stop the ping timeout timer.
	if s.pingTimeoutTimer != nil {
		s.pingTimeoutTimer.Stop()
	}

	// Ignore further communication from the transport.
	if s.transport != nil {
		s.transport.OnOpen(nil)
		s.transport.OnClose(nil)
		s.transport.OnPacket(nil)
		s.transport.OnError(nil)

		// Close the transport.
		s.transport.Close(ctx)
		s.transport = nil
	}

	// The state is now closed.
	s.state = SocketStateClosed

	// A session no longer exists.
	s.sessionID = ""

	// If the onCloseHandler is set, call it.
	if s.onCloseHandler != nil {
		s.onCloseHandler(reason, cause)
	}
}

func (s *Socket) onPacket(ctx context.Context, p Packet) {
	switch s.state {
	// These states are valid states for receiving packets.
	case SocketStateOpen, SocketStateOpening, SocketStateClosing:
		break

	default:
		return
	}

	s.reschedulePingTimeout(ctx)

	switch p.Type {
	// If an open packet is received, the server is ready to receive messages.
	case PacketOpen:
		var openPacket OpenPacket
		switch err := json.Unmarshal(p.Data, &openPacket); {
		case err != nil:
			if s.onErrorHandler != nil {
				s.onErrorHandler(fmt.Errorf("failed to unmarshal open packet: %w", err))
			}

		default:
			s.onOpen(ctx, openPacket)
		}

	// If a ping packet is received, the server is still alive.
	case PacketPing:
		// Reply with a pong packet.
		if err := s.Send(ctx, []Packet{{Type: PacketPong}}); err != nil && s.onErrorHandler != nil {
			s.onErrorHandler(fmt.Errorf("failed to send pong packet: %w", err))
		}

	case PacketMessage:
		if s.onMessageHandler != nil {
			s.onMessageHandler(p.Data)
		}
	}

	// If the onPacketHandler is set, call it.
	if s.onPacketHandler != nil {
		s.onPacketHandler(p)
	}
}

// reschedulePingTimeout stops and re-schedules the ping timeout timer. This closes the transport if the server does not send a message within the pingTimeout.
func (s *Socket) reschedulePingTimeout(ctx context.Context) {
	if s.pingTimeoutTimer != nil {
		s.pingTimeoutTimer.Stop()
	}

	// Schedule a timer that closes the transport if the server does not respond to a message.
	var timer = time.NewTimer(s.pingInterval + s.pingTimeout)
	go func() {
		select {
		// If the context is complete, the timer should be stopped.
		case <-ctx.Done():
			timer.Stop()

		// When a message is received...
		case _, closed := <-timer.C:
			// The goroutine should exit if the timer is closed.
			if closed {
				return
			}

			// Otherwise, the socket should close.
			s.onClose(ctx, "ping timeout", nil)
		}
	}()

	// Store it so it can be stopped.
	s.pingTimeoutTimer = timer
}

// onOpen is called when the server sends an open packet.
func (s *Socket) onOpen(ctx context.Context, p OpenPacket) {
	// Store the session ID, ping interval, ping timeout, and maximum payload.
	s.sessionID = p.SessionID
	s.maxPayload = p.MaxPayload
	s.pingTimeout = time.Duration(p.PingTimeout) * time.Millisecond
	s.pingInterval = time.Duration(p.PingInterval) * time.Millisecond

	// The state is now open.
	s.state = SocketStateOpen

	// Update the transport's URL with the session ID.
	url, err := s.resolveURL(s.transport.Type())
	if err != nil {
		if s.onErrorHandler != nil {
			s.onErrorHandler(fmt.Errorf("failed to resolve URL: %w", err))
		}
		return
	}
	s.transport.SetURL(url)

	// If the transport is a WebSocket, remember that the upgrade was successful.
	s.priorUpgradeSuccess = s.transport.Type() == TransportTypeWebSocket

	// If the client supports upgrades, probe the server for upgrades.
	if s.upgrade && len(p.Upgrades) != 0 {
		for _, upgrade := range p.Upgrades {
			// The transport upgrade must be in the list of supported transports.
			if slices.Contains(s.transports, upgrade) {
				// Probe the transport upgrade.
				if err := s.probe(ctx, upgrade); err != nil {
					// If an error handler is set, call it.
					if s.onErrorHandler != nil {
						s.onErrorHandler(fmt.Errorf("failed to probe for upgrade: %w", err))
					}
					// If an error occurred, the next upgrade should be attempted.
					continue
				}

				// If no error occurred, the upgrade was successful.
				break
			}
		}
	}

	// If the onOpenHandler is set, call it.
	if s.onOpenHandler != nil {
		s.onOpenHandler()
	}
}

// probe probes the server for an upgrade to a new transport.
func (s *Socket) probe(ctx context.Context, upgradeTransportType TransportType) error {
	// Reset upgrade success to false.
	s.priorUpgradeSuccess = false

	// Resolve the URL for the transport.
	url, err := s.resolveURL(upgradeTransportType)
	if err != nil {
		return fmt.Errorf("failed to resolve URL: %w", err)
	}

	// Create a transport using the upgrade transport type.
	transport, err := Transports[upgradeTransportType](url, TransportOptions{
		Client: s.client,
		Header: s.header,
	})
	if err != nil {
		return fmt.Errorf("failed to create transport: %w", err)
	}

	// errChan is a channel that is used to signal when the upgrade process is complete.
	errChan := make(chan error, 1)

	// When the transport opens, send a probe packet.
	transport.OnOpen(func(ctx context.Context) {
		// Send a probe packet.
		// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#upgrade
		transport.Send(ctx, []Packet{{Type: PacketPing, Data: []byte("probe")}})

		// The on open handler is no longer needed.
		transport.OnOpen(nil)
	})

	// When the transport receives a packet, check if it is a pong packet with the data "probe".
	transport.OnPacket(func(ctx context.Context, p Packet) {
		// Wait for a pong packet with the data "probe".
		if p.Type == PacketPong && string(p.Data) == "probe" {
			// The upgrade success is true if the transport is a WebSocket.
			s.priorUpgradeSuccess = transport.Type() == TransportTypeWebSocket

			// Ignore further communication from the transport.
			s.transport.OnOpen(nil)
			s.transport.OnClose(nil)
			s.transport.OnError(nil)
			s.transport.OnPacket(nil)
			s.transport.Pause(ctx)

			// Set the current transport to the new upgraded transport and configure the handlers.
			s.transport = transport
			s.transport.OnPacket(s.onPacket)
			s.transport.OnError(s.onError)
			s.transport.OnClose(func(ctx context.Context) {
				s.onClose(ctx, "transport closed", nil)
			})

			// Send the upgrade packet.
			s.transport.Send(ctx, []Packet{{Type: PacketUpgrade}})

			errChan <- nil
		}
	})

	// If the transport is closed, send an error to the channel.
	transport.OnClose(func(_ context.Context) {
		errChan <- errors.New("transport closed")
		transport.OnOpen(nil)
		transport.OnClose(nil)
		transport.OnError(nil)
		transport.OnPacket(nil)
		transport.Close(ctx)
	})

	// If an error occurs, close the transport and send the error to the error channel.
	transport.OnError(func(ctx context.Context, err error) {
		errChan <- fmt.Errorf("transport error: %v", err)
		transport.OnOpen(nil)
		transport.OnClose(nil)
		transport.OnError(nil)
		transport.OnPacket(nil)
		transport.Close(ctx)
	})

	// Open the transport.
	transport.Open(ctx)

	if err := <-errChan; err != nil {
		return fmt.Errorf("probing transport: %w", err)
	}

	return nil
}

// OnOpen sets the handler for when the socket opens.
func (s *Socket) OnOpen(handler SocketOpenHandler) {
	s.onOpenHandler = handler
}

// OnClose sets the handler for when the socket closes.
func (s *Socket) OnClose(handler SocketCloseHandler) {
	s.onCloseHandler = handler
}

// OnPacket sets the handler for when the socket receives packets.
func (s *Socket) OnPacket(handler SocketPacketHandler) {
	s.onPacketHandler = handler
}

// OnMessage sets the handler for when the socket receives a message packet.
func (s *Socket) OnMessage(handler SocketMessageHandler) {
	s.onMessageHandler = handler
}

// OnError sets the handler for when the socket encounters an error.
func (s *Socket) OnError(handler SocketErrorHandler) {
	s.onErrorHandler = handler
}
