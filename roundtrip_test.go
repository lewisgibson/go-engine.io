package engineio_test

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

// serverMessage is a message received by the server, with its binary flag.
type serverMessage struct {
	data     []byte
	isBinary bool
}

// newRoundTripServer starts a real httptest server wrapping an Engine.IO
// server, returning the server, its URL, and channels for the first session and
// its inbound messages. Heartbeat values are small but the real client pongs, so
// the session stays alive for the duration of a test.
func newRoundTripServer(t *testing.T) (string, <-chan *engineio.ServerSocket, <-chan serverMessage) {
	t.Helper()

	sockets := make(chan *engineio.ServerSocket, 1)
	messages := make(chan serverMessage, 16)

	server := engineio.NewServer(
		engineio.WithPingInterval(200*time.Millisecond),
		engineio.WithPingTimeout(2*time.Second),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, isBinary bool) {
			messages <- serverMessage{data: data, isBinary: isBinary}
		})
		select {
		case sockets <- socket:

		default:
		}
	})

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	return httpServer.URL + "/engine.io/", sockets, messages
}

func TestRoundTrip_Polling(t *testing.T) {
	t.Parallel()

	// Arrange: a real server and a polling-only client
	url, sockets, serverMessages := newRoundTripServer(t)

	clientMessages := make(chan []byte, 16)
	client, err := engineio.NewSocket(url,
		engineio.WithUpgrade(false),
		engineio.WithTransports(engineio.TransportTypePolling),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	client.OnOpen(func() { opened <- struct{}{} })
	client.OnMessage(func(data []byte, _ bool) { clientMessages <- data })
	client.OnError(func(error) {})
	client.OnPacket(func(engineio.Packet) {})

	// Act: open and wait for both ends to establish
	client.Open(t.Context())
	t.Cleanup(func() { client.Close(t.Context()) })
	<-opened
	socket := <-sockets

	// Act + Assert: client -> server text
	require.NoError(t, client.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	}))
	text := <-serverMessages
	require.Equal(t, "hello", string(text.data))
	require.False(t, text.isBinary)

	// Act + Assert: server -> client text
	require.NoError(t, socket.Send([]byte("world"), false))
	require.Equal(t, []byte("world"), <-clientMessages)

	// Act + Assert: client -> server binary
	require.NoError(t, client.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03, 0x04}, IsBinary: true},
	}))
	binary := <-serverMessages
	require.Equal(t, []byte{0x01, 0x02, 0x03, 0x04}, binary.data)
	require.True(t, binary.isBinary)

	// Act + Assert: server -> client binary
	require.NoError(t, socket.Send([]byte{0x05, 0x06, 0x07, 0x08}, true))
	require.Equal(t, []byte{0x05, 0x06, 0x07, 0x08}, <-clientMessages)
}

func TestRoundTrip_Upgrade(t *testing.T) {
	t.Parallel()

	// Arrange: a real server and a client that upgrades from polling to websocket
	url, sockets, serverMessages := newRoundTripServer(t)

	clientMessages := make(chan []byte, 16)
	upgraded := make(chan engineio.TransportType, 1)
	client, err := engineio.NewSocket(url,
		engineio.WithUpgrade(true),
		engineio.WithTransports(engineio.TransportTypePolling, engineio.TransportTypeWebSocket),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	client.OnOpen(func() { opened <- struct{}{} })
	client.OnUpgrade(func(transportType engineio.TransportType) {
		select {
		case upgraded <- transportType:

		default:
		}
	})
	client.OnMessage(func(data []byte, _ bool) { clientMessages <- data })
	client.OnError(func(error) {})
	client.OnPacket(func(engineio.Packet) {})

	// Act: open, wait for the handshake, then wait for the upgrade to complete
	// Keying on the upgrade event makes the round trips below deterministic.
	client.Open(t.Context())
	t.Cleanup(func() { client.Close(t.Context()) })
	<-opened
	socket := <-sockets
	require.Equal(t, engineio.TransportTypeWebSocket, <-upgraded)

	// Act + Assert: client -> server text over the upgraded websocket. Receiving
	// it proves the server processed the upgrade packet that preceded it.
	require.NoError(t, client.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	}))
	text := <-serverMessages
	require.Equal(t, "hello", string(text.data))
	require.False(t, text.isBinary)

	// Act + Assert: server -> client binary over the upgraded websocket
	require.NoError(t, socket.Send([]byte{0x05, 0x06, 0x07, 0x08}, true))
	require.Equal(t, []byte{0x05, 0x06, 0x07, 0x08}, <-clientMessages)
}

func TestRoundTrip_BuffersSendDuringUpgrade(t *testing.T) {
	t.Parallel()

	// Arrange: a real server that echoes every message, and a client that
	// upgrades from polling to websocket.
	const count = 100

	server := engineio.NewServer(
		engineio.WithPingInterval(20*time.Millisecond),
		engineio.WithPingTimeout(2*time.Second),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, isBinary bool) {
			// Best-effort echo; a send error (e.g. a close during cleanup) just
			// stops this echo and is caught by the delivery assertions below.
			if err := socket.Send(data, isBinary); err != nil {
				return
			}
		})
	})

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	clientMessages := make(chan []byte, count)
	upgraded := make(chan engineio.TransportType, 1)
	client, err := engineio.NewSocket(httpServer.URL+"/engine.io/",
		engineio.WithUpgrade(true),
		engineio.WithTransports(engineio.TransportTypePolling, engineio.TransportTypeWebSocket),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	client.OnOpen(func() { opened <- struct{}{} })
	client.OnUpgrade(func(transportType engineio.TransportType) {
		select {
		case upgraded <- transportType:

		default:
		}
	})
	client.OnMessage(func(data []byte, _ bool) { clientMessages <- data })
	client.OnError(func(error) {})
	client.OnPacket(func(engineio.Packet) {})

	client.Open(t.Context())
	t.Cleanup(func() { client.Close(t.Context()) })
	<-opened

	// Act: stream sequentially numbered messages starting the instant the socket
	// is open. The burst spans the asynchronous upgrade, so some sends land before
	// it, some are buffered during it, and some land after it.
	for index := range count {
		require.NoError(t, client.Send(t.Context(), []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte(strconv.Itoa(index))},
		}))
	}

	// Assert: the upgrade completed, and every message was echoed back exactly
	// once and in order. Nothing was lost or reordered across the transport switch.
	require.Equal(t, engineio.TransportTypeWebSocket, <-upgraded)
	for index := range count {
		require.Equal(t, strconv.Itoa(index), string(<-clientMessages))
	}
}

func TestRoundTrip_ConcurrentSendsDuringUpgrade(t *testing.T) {
	t.Parallel()

	// Arrange: a real server that records every message it receives, and a client
	// that upgrades from polling to websocket.
	const count = 200

	received := make(chan string, count)
	server := engineio.NewServer(
		engineio.WithPingInterval(20*time.Millisecond),
		engineio.WithPingTimeout(2*time.Second),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, _ bool) { received <- string(data) })
	})

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	client, err := engineio.NewSocket(httpServer.URL+"/engine.io/",
		engineio.WithUpgrade(true),
		engineio.WithTransports(engineio.TransportTypePolling, engineio.TransportTypeWebSocket),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	client.OnOpen(func() { opened <- struct{}{} })
	client.OnError(func(error) {})
	client.OnPacket(func(engineio.Packet) {})

	client.Open(t.Context())
	t.Cleanup(func() { client.Close(t.Context()) })
	<-opened

	// Act: from a separate goroutine, send numbered messages back to back so a
	// send is in flight over the old transport when the upgrade pauses it.
	go func() {
		for index := range count {
			// Stop if a send fails (e.g. the socket closes during cleanup); the
			// delivery assertion below catches any genuinely lost message.
			if err := client.Send(t.Context(), []engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte(strconv.Itoa(index))},
			}); err != nil {
				return
			}
		}
	}()

	// Assert: every message reaches the server exactly once, none dropped across
	// the upgrade boundary.
	var got = make(map[string]bool, count)
	var timeout = time.After(10 * time.Second)
	for len(got) < count {
		select {
		case message := <-received:
			got[message] = true

		case <-timeout:
			require.FailNowf(t, "messages lost across the upgrade", "received %d/%d", len(got), count)
		}
	}
}

func TestRoundTrip_CloseDuringUpgradeProbe(t *testing.T) {
	t.Parallel()

	// Arrange: a real server and a client that upgrades
	url, _, _ := newRoundTripServer(t)

	client, err := engineio.NewSocket(url,
		engineio.WithUpgrade(true),
		engineio.WithTransports(engineio.TransportTypePolling, engineio.TransportTypeWebSocket),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	closed := make(chan struct{}, 1)
	client.OnOpen(func() { opened <- struct{}{} })
	client.OnClose(func(string, error) {
		select {
		case closed <- struct{}{}:

		default:
		}
	})
	client.OnError(func(error) {})

	// Act: open, then close while the upgrade probe is likely still in flight
	client.Open(t.Context())
	<-opened
	client.Close(t.Context())

	// Assert: the socket closes promptly rather than hanging on the in-flight
	// probe, which the run context cancels.
	select {
	case <-closed:

	case <-time.After(5 * time.Second):
		require.FailNow(t, "client did not close during the upgrade probe")
	}
}

func TestRoundTrip_ServerCloseNotifiesClient(t *testing.T) {
	t.Parallel()

	// Arrange: a real server and a polling-only client
	url, sockets, _ := newRoundTripServer(t)

	client, err := engineio.NewSocket(url,
		engineio.WithUpgrade(false),
		engineio.WithTransports(engineio.TransportTypePolling),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	client.OnOpen(func() { opened <- struct{}{} })
	clientClosed := make(chan string, 1)
	client.OnClose(func(reason string, _ error) {
		select {
		case clientClosed <- reason:

		default:
		}
	})

	client.Open(t.Context())
	t.Cleanup(func() { client.Close(t.Context()) })
	<-opened
	socket := <-sockets

	// Act: close the session from the server
	require.NoError(t, socket.Close())

	// Assert: the client observes the close (via a close packet or a stale-sid
	// rejection on its next poll; either way it tears down deterministically).
	require.NotEmpty(t, <-clientClosed)
}
