package engineio_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// fakeTransport is a minimal engineio.Transport used to drive the client's
// handshake, buffering, and packet-dispatch behavior deterministically, with no
// network I/O. It records every packet handed to Send, can be configured to fail
// a chosen Send attempt, and exposes deliverPacket to feed packets to the
// attached handler. It is injected through the exported engineio.Transports
// registry, so tests using it must not run in parallel (see withFakeTransports).
type fakeTransport struct {
	transportType engineio.TransportType

	mu        sync.Mutex
	onPacket  engineio.TransportPacketHandler
	sent      [][]engineio.Packet
	sendErrs  []error // sendErrs[i] is returned by the (i+1)th Send when non-nil
	sendCount int
}

// newFakeTransport creates a fake transport of the given kind.
func newFakeTransport(transportType engineio.TransportType) *fakeTransport {
	return &fakeTransport{transportType: transportType}
}

func (t *fakeTransport) Type() engineio.TransportType   { return t.transportType }
func (t *fakeTransport) State() engineio.TransportState { return engineio.TransportStateOpen }
func (t *fakeTransport) SetURL(*url.URL)                {}
func (t *fakeTransport) Open(context.Context)           {}
func (t *fakeTransport) Close(context.Context)          {}
func (t *fakeTransport) Pause(context.Context)          {}

// Send records the packets and returns the configured error for this attempt.
func (t *fakeTransport) Send(_ context.Context, packets []engineio.Packet) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	index := t.sendCount
	t.sendCount++
	if index < len(t.sendErrs) && t.sendErrs[index] != nil {
		return t.sendErrs[index]
	}
	t.sent = append(t.sent, packets)

	return nil
}

func (t *fakeTransport) OnOpen(engineio.TransportOpenHandler)   {}
func (t *fakeTransport) OnClose(engineio.TransportCloseHandler) {}
func (t *fakeTransport) OnError(engineio.TransportErrorHandler) {}

func (t *fakeTransport) OnPacket(h engineio.TransportPacketHandler) {
	t.mu.Lock()
	t.onPacket = h
	t.mu.Unlock()
}

// deliverPacket dispatches a packet to the attached packet handler.
func (t *fakeTransport) deliverPacket(ctx context.Context, packet engineio.Packet) {
	t.mu.Lock()
	handler := t.onPacket
	t.mu.Unlock()

	if handler != nil {
		handler(ctx, packet)
	}
}

// sentPackets returns a flat copy of every packet handed to Send, in order.
func (t *fakeTransport) sentPackets() []engineio.Packet {
	t.mu.Lock()
	defer t.mu.Unlock()

	var out []engineio.Packet
	for _, chunk := range t.sent {
		out = append(out, chunk...)
	}

	return out
}

// controllableTransport is an engineio.Transport whose lifecycle the test drives
// directly, so the open/retry and upgrade-probe paths can be exercised without
// network I/O. Open reports either success or a configured error, Send can be made
// to fail on a chosen attempt, and deliverPacket feeds a packet to the attached
// handler. It is injected through the exported engineio.Transports registry, so
// tests using it must not run in parallel (see withFakeTransports).
type controllableTransport struct {
	transportType engineio.TransportType

	mu        sync.Mutex
	onOpen    engineio.TransportOpenHandler
	onClose   engineio.TransportCloseHandler
	onPacket  engineio.TransportPacketHandler
	onError   engineio.TransportErrorHandler
	state     engineio.TransportState
	openErr   error           // when set, Open reports this error instead of opening
	openedCtx context.Context // the context the most recent Open received
	sent      [][]engineio.Packet
	sendErrs  []error // sendErrs[i] is returned by the (i+1)th Send when non-nil
	sendCount int
	closed    bool
	opened    chan struct{} // signaled after each Open returns
}

// newControllableTransport creates a controllable transport of the given kind.
func newControllableTransport(transportType engineio.TransportType) *controllableTransport {
	return &controllableTransport{
		transportType: transportType,
		state:         engineio.TransportStateClosed,
		opened:        make(chan struct{}, 1),
	}
}

func (t *controllableTransport) Type() engineio.TransportType { return t.transportType }

func (t *controllableTransport) State() engineio.TransportState {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.state
}

func (t *controllableTransport) SetURL(*url.URL) {}

// Open records the context, then reports either the configured error or a
// successful open, and finally signals the opened channel.
func (t *controllableTransport) Open(ctx context.Context) {
	t.mu.Lock()
	t.openedCtx = ctx
	var openErr = t.openErr
	var onOpen = t.onOpen
	var onError = t.onError
	if openErr == nil {
		t.state = engineio.TransportStateOpen
	}
	t.mu.Unlock()

	switch {
	case openErr != nil:
		if onError != nil {
			onError(ctx, openErr)
		}

	default:
		if onOpen != nil {
			onOpen(ctx)
		}
	}

	select {
	case t.opened <- struct{}{}:

	default:
	}
}

// Close marks the transport closed and fires the registered close handler, like
// a real transport, so the socket can complete its teardown.
func (t *controllableTransport) Close(ctx context.Context) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.state = engineio.TransportStateClosed
	onClose := t.onClose
	t.mu.Unlock()

	if onClose != nil {
		onClose(ctx)
	}
}

func (t *controllableTransport) Pause(context.Context) {}

// Send records the packets and returns the configured error for this attempt.
func (t *controllableTransport) Send(_ context.Context, packets []engineio.Packet) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	index := t.sendCount
	t.sendCount++
	if index < len(t.sendErrs) && t.sendErrs[index] != nil {
		return t.sendErrs[index]
	}
	t.sent = append(t.sent, packets)

	return nil
}

func (t *controllableTransport) OnOpen(h engineio.TransportOpenHandler) {
	t.set(func() { t.onOpen = h })
}

func (t *controllableTransport) OnClose(h engineio.TransportCloseHandler) {
	t.set(func() { t.onClose = h })
}

func (t *controllableTransport) OnPacket(h engineio.TransportPacketHandler) {
	t.set(func() { t.onPacket = h })
}

func (t *controllableTransport) OnError(h engineio.TransportErrorHandler) {
	t.set(func() { t.onError = h })
}

// set runs fn under the transport lock.
func (t *controllableTransport) set(fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()

	fn()
}

// deliverPacket dispatches a packet to the attached packet handler.
func (t *controllableTransport) deliverPacket(ctx context.Context, packet engineio.Packet) {
	t.mu.Lock()
	handler := t.onPacket
	t.mu.Unlock()

	if handler != nil {
		handler(ctx, packet)
	}
}

// packetHandler returns the currently attached packet handler, or nil. It lets a
// test capture the socket's wiring before a close detaches it.
func (t *controllableTransport) packetHandler() engineio.TransportPacketHandler {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.onPacket
}

// isClosed reports whether the transport has been closed.
func (t *controllableTransport) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.closed
}

// openContextErr returns the error of the context the transport was opened with,
// or a sentinel if it was never opened.
func (t *controllableTransport) openContextErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.openedCtx == nil {
		return errors.New("transport was never opened")
	}

	return t.openedCtx.Err()
}

// withFakeTransports replaces the named entries of the exported engineio.Transports
// registry with constructors that return the supplied fakes, and restores the
// originals on cleanup. A Socket snapshots the registry at construction, so the
// fakes take effect only for sockets created while the override is installed.
//
// Because it mutates a process-global map, every test that uses it must NOT call
// t.Parallel(): Go runs non-parallel tests sequentially while parallel ones are
// paused, so the override is observed only by this test and is always restored.
func withFakeTransports(t *testing.T, fakes map[engineio.TransportType]engineio.Transport) {
	t.Helper()

	var originals = map[engineio.TransportType]engineio.TransportConstructor{}
	for transportType, fake := range fakes {
		originals[transportType] = engineio.Transports[transportType]
		engineio.Transports[transportType] = func(*url.URL, engineio.TransportClient, http.Header) (engineio.Transport, error) {
			return fake, nil
		}
	}
	t.Cleanup(func() {
		for transportType, original := range originals {
			engineio.Transports[transportType] = original
		}
	})
}

// handshakeData marshals an open packet advertising the given upgrades, for
// delivery through a fake transport's packet handler.
func handshakeData(t *testing.T, upgrades ...engineio.TransportType) []byte {
	t.Helper()

	data, err := json.Marshal(engineio.OpenPacket{
		SessionID:    "sid",
		Upgrades:     upgrades,
		PingInterval: 1000,
		PingTimeout:  1000,
	})
	require.NoError(t, err)

	return data
}

func TestNewSocket(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Return(nil, errors.New("mock error")).
		AnyTimes()

	// Act: create a new polling transport with the mock transport client
	url := "http://localhost/engine.io/?EIO=4&transport=polling"
	socket, err := engineio.NewSocket(url, engineio.WithClient(mockTransportClient))

	// Assert: the socket is not nil and there is no error
	require.NoErrorf(t, err, "NewSocket() error = %v", err)
	require.NotNil(t, socket)
}

func TestNewSocket_ClientOptions(t *testing.T) {
	t.Parallel()

	t.Run("WithTransports orders the first request", func(t *testing.T) {
		t.Parallel()

		// Arrange: a mock client that records the transport of the first request,
		// then fails it so the open terminates promptly. WebSocket is configured
		// first, so the first request must target websocket.
		transports := make(chan string, 1)
		mockTransportClient := NewMockTransportClient(gomock.NewController(t))
		mockTransportClient.EXPECT().
			Do(gomock.Any()).
			DoAndReturn(func(req *http.Request) (*http.Response, error) {
				select {
				case transports <- req.URL.Query().Get("transport"):

				default:
				}

				return nil, errors.New("dial failed")
			}).
			AnyTimes()

		socket, err := engineio.NewSocket("http://localhost/engine.io/",
			engineio.WithClient(mockTransportClient),
			engineio.WithTransports(engineio.TransportTypeWebSocket),
		)
		require.NoError(t, err)

		// Act: open the socket so the first request is issued
		socket.Open(t.Context())

		// Assert: the first request targeted the configured first transport
		require.Equal(t, string(engineio.TransportTypeWebSocket), <-transports)
	})

	t.Run("WithClient uses the supplied client", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			// Arrange: a mock client that signals once it is used
			used := make(chan struct{}, 1)
			mockTransportClient := NewMockTransportClient(gomock.NewController(t))
			mockTransportClient.EXPECT().
				Do(gomock.Any()).
				DoAndReturn(func(_ *http.Request) (*http.Response, error) {
					select {
					case used <- struct{}{}:

					default:
					}

					return nil, errors.New("poll failed")
				}).
				AnyTimes()

			socket, err := engineio.NewSocket("http://localhost/engine.io/",
				engineio.WithClient(mockTransportClient),
			)
			require.NoError(t, err)

			// Act: open the socket so the configured client is exercised
			socket.Open(t.Context())

			// Assert: the supplied client received the request
			<-used
		})
	})

	t.Run("WithHeader sends the configured headers", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			// Arrange: a mock client that captures the request headers
			headers := make(chan http.Header, 1)
			mockTransportClient := NewMockTransportClient(gomock.NewController(t))
			mockTransportClient.EXPECT().
				Do(gomock.Any()).
				DoAndReturn(func(req *http.Request) (*http.Response, error) {
					select {
					case headers <- req.Header.Clone():

					default:
					}

					return nil, errors.New("poll failed")
				}).
				AnyTimes()

			header := http.Header{"X-Custom-Header": []string{"custom-value"}}
			socket, err := engineio.NewSocket("http://localhost/engine.io/",
				engineio.WithClient(mockTransportClient),
				engineio.WithHeader(header),
			)
			require.NoError(t, err)

			// Act: open the socket so the request carries the header
			socket.Open(t.Context())

			// Assert: the configured header reached the request
			got := <-headers
			require.Equal(t, "custom-value", got.Get("X-Custom-Header"))
		})
	})

	t.Run("WithUpgrade false never probes another transport", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			// Arrange: a mock client that completes the handshake advertising a
			// websocket upgrade, then stays silent on later polls. Every request's
			// transport is recorded so an upgrade probe would be observable.
			var (
				mu       sync.Mutex
				getCount atomic.Int32
			)
			seen := map[string]struct{}{}
			openedSig := make(chan struct{}, 1)
			mockTransportClient := NewMockTransportClient(gomock.NewController(t))
			mockTransportClient.EXPECT().
				Do(gomock.Any()).
				DoAndReturn(func(req *http.Request) (*http.Response, error) {
					mu.Lock()
					seen[req.URL.Query().Get("transport")] = struct{}{}
					mu.Unlock()

					if req.Method == http.MethodPost {
						return okResponse(nil), nil
					}

					if getCount.Add(1) == 1 {
						return okResponse(clientHandshakeBodyWithUpgrades(t, []engineio.TransportType{engineio.TransportTypeWebSocket})), nil
					}

					return okResponse(nil), nil
				}).
				AnyTimes()

			socket, err := engineio.NewSocket("http://localhost/engine.io/",
				engineio.WithClient(mockTransportClient),
				engineio.WithUpgrade(false),
			)
			require.NoError(t, err)
			socket.OnOpen(func() {
				select {
				case openedSig <- struct{}{}:

				default:
				}
			})

			// Act: open the socket and let every scheduled poll settle
			socket.Open(t.Context())
			<-openedSig
			synctest.Wait()

			// Assert: only the polling transport was ever requested; no upgrade
			// probe to websocket was issued.
			mu.Lock()
			_, sawWebSocket := seen[string(engineio.TransportTypeWebSocket)]
			_, sawPolling := seen[string(engineio.TransportTypePolling)]
			mu.Unlock()

			require.True(t, sawPolling)
			require.False(t, sawWebSocket)
		})
	})

	t.Run("WithRememberUpgrade constructs a usable socket", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			// Arrange: a mock client whose poll fails immediately so the open closes
			mockTransportClient := NewMockTransportClient(gomock.NewController(t))
			mockTransportClient.EXPECT().
				Do(gomock.Any()).
				Return(nil, errors.New("poll failed")).
				AnyTimes()

			// Act: construct a socket with the option set
			socket, err := engineio.NewSocket("http://localhost/engine.io/",
				engineio.WithClient(mockTransportClient),
				engineio.WithRememberUpgrade(true),
			)

			// Assert: construction succeeds and the socket opens and closes cleanly
			require.NoError(t, err)
			require.NotNil(t, socket)

			closed := make(chan struct{}, 1)
			socket.OnClose(func(string, error) {
				select {
				case closed <- struct{}{}:

				default:
				}
			})
			socket.Open(t.Context())

			<-closed
		})
	})

	t.Run("WithTryAllTransports falls back to the next transport", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			// Arrange: a mock client that records each request's transport and fails
			// every request. With two transports and try-all enabled, the failed
			// polling open must be retried on the next transport (websocket).
			transports := make(chan string, 4)
			mockTransportClient := NewMockTransportClient(gomock.NewController(t))
			mockTransportClient.EXPECT().
				Do(gomock.Any()).
				DoAndReturn(func(req *http.Request) (*http.Response, error) {
					select {
					case transports <- req.URL.Query().Get("transport"):

					default:
					}

					return nil, errors.New("open failed")
				}).
				AnyTimes()

			socket, err := engineio.NewSocket("http://localhost/engine.io/",
				engineio.WithClient(mockTransportClient),
				engineio.WithTransports(engineio.TransportTypePolling, engineio.TransportTypeWebSocket),
				engineio.WithTryAllTransports(true),
			)
			require.NoError(t, err)

			// Act: open the socket; the failing polling open must roll over
			socket.Open(t.Context())

			// Assert: the first request used polling and a later request used
			// websocket, proving the failed polling open fell back.
			require.Equal(t, string(engineio.TransportTypePolling), <-transports)

			var sawWebSocket = false
			for !sawWebSocket {
				sawWebSocket = <-transports == string(engineio.TransportTypeWebSocket)
			}
			require.True(t, sawWebSocket)
		})
	})
}

// --- Heartbeat and poll behavior (mock TransportClient driven) ---

// clientHandshakeBody encodes a polling handshake response advertising the given
// heartbeat parameters (milliseconds) and no upgrades.
func clientHandshakeBody(t *testing.T, pingInterval, pingTimeout int) []byte {
	t.Helper()

	data, err := json.Marshal(engineio.OpenPacket{
		SessionID:    "252937f5-aff9-4885-91ca-234802cede79",
		Upgrades:     []engineio.TransportType{},
		PingInterval: pingInterval,
		PingTimeout:  pingTimeout,
		MaxPayload:   1_000_000,
	})
	require.NoError(t, err)

	return engineio.EncodePayload([]engineio.Packet{
		{Type: engineio.PacketOpen, Data: data},
	})
}

// clientHandshakeBodyWithUpgrades encodes a polling handshake response
// advertising the given upgrades.
func clientHandshakeBodyWithUpgrades(t *testing.T, upgrades []engineio.TransportType) []byte {
	t.Helper()

	data, err := json.Marshal(engineio.OpenPacket{
		SessionID:    "9b9a4a2c-0f1e-4d3a-8c2b-1a2b3c4d5e6f",
		Upgrades:     upgrades,
		PingInterval: 100,
		PingTimeout:  100,
		MaxPayload:   1_000_000,
	})
	require.NoError(t, err)

	return engineio.EncodePayload([]engineio.Packet{
		{Type: engineio.PacketOpen, Data: data},
	})
}

// okResponse builds a 200 response with the given body.
func okResponse(body []byte) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
}

// driveProbe opens a socket over a controllable polling transport, delivers a
// handshake advertising a websocket upgrade, and returns the polling transport
// plus the websocket transport the probe creates. The caller drives the probe
// outcome by delivering packets to the websocket transport. The socket must not
// be run in parallel because it injects into the global registry.
func driveProbe(t *testing.T, wsSendErrs []error) (*engineio.Socket, *controllableTransport, *controllableTransport) {
	t.Helper()

	polling := newControllableTransport(engineio.TransportTypePolling)
	websocket := newControllableTransport(engineio.TransportTypeWebSocket)
	websocket.sendErrs = wsSendErrs
	withFakeTransports(t, map[engineio.TransportType]engineio.Transport{
		engineio.TransportTypePolling:   polling,
		engineio.TransportTypeWebSocket: websocket,
	})

	socket, err := engineio.NewSocket("http://localhost/engine.io/")
	require.NoError(t, err)
	socket.OnError(func(error) {})
	t.Cleanup(func() { socket.Close(context.WithoutCancel(t.Context())) })

	// Act: open over polling, then deliver a handshake advertising a websocket
	// upgrade so the background probe starts and opens its websocket transport.
	socket.Open(t.Context())
	polling.deliverPacket(t.Context(), engineio.Packet{
		Type: engineio.PacketOpen,
		Data: handshakeData(t, engineio.TransportTypeWebSocket),
	})
	<-websocket.opened

	return socket, polling, websocket
}

// flatten returns a flat copy of every packet across the given chunks, in order.
func flatten(chunks [][]engineio.Packet) []engineio.Packet {
	var out []engineio.Packet
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}

	return out
}
