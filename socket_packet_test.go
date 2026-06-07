package engineio_test

import (
	"context"
	"errors"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestSocket_OnPacket_DropsWhenNotLive(t *testing.T) {
	// Arrange: a controllable transport whose packet handler the socket wires on
	// Open, with a message handler that would record any delivery.
	polling := newControllableTransport(engineio.TransportTypePolling)
	withFakeTransports(t, map[engineio.TransportType]engineio.Transport{
		engineio.TransportTypePolling: polling,
	})

	socket, err := engineio.NewSocket("http://localhost/engine.io/",
		engineio.WithTransports(engineio.TransportTypePolling),
		engineio.WithUpgrade(false),
	)
	require.NoError(t, err)

	messages := make(chan []byte, 1)
	socket.OnMessage(func(data []byte, _ bool) { messages <- data })
	socket.OnError(func(error) {})

	// Arrange: open the socket, capturing the packet handler the socket wired to
	// the transport, then close so the socket is no longer live.
	socket.Open(t.Context())
	deliver := polling.packetHandler()
	require.NotNil(t, deliver)
	socket.Close(context.WithoutCancel(t.Context()))

	// Act: deliver a message through the socket's own handler after the close,
	// simulating a packet still in flight on the transport goroutine.
	deliver(t.Context(), engineio.Packet{Type: engineio.PacketMessage, Data: []byte("dropped")})

	// Assert: the not-live state guard dropped the packet, so the message handler
	// never fired.
	require.Empty(t, messages)
}

func TestSocket_OnPacket_OpenPacketInvalidJSONReportsError(t *testing.T) {
	// Arrange: an opening socket on a controllable transport
	polling := newControllableTransport(engineio.TransportTypePolling)
	withFakeTransports(t, map[engineio.TransportType]engineio.Transport{
		engineio.TransportTypePolling: polling,
	})

	socket, err := engineio.NewSocket("http://localhost/engine.io/",
		engineio.WithTransports(engineio.TransportTypePolling),
		engineio.WithUpgrade(false),
	)
	require.NoError(t, err)

	errs := make(chan error, 1)
	opened := make(chan struct{}, 1)
	socket.OnError(func(err error) { errs <- err })
	socket.OnOpen(func() { opened <- struct{}{} })
	t.Cleanup(func() { socket.Close(context.WithoutCancel(t.Context())) })

	// Act: open, then deliver an open packet whose body is not valid JSON
	socket.Open(t.Context())
	polling.deliverPacket(t.Context(), engineio.Packet{Type: engineio.PacketOpen, Data: []byte("{not json")})

	// Assert: the malformed open packet is reported and the socket never opened
	require.ErrorContains(t, <-errs, "unmarshalling open packet")
	require.Empty(t, opened)
}

func TestSocket_OnPacket_PingPongSendFailureReportsError(t *testing.T) {
	// Arrange: an open socket whose transport fails the pong send. The handshake's
	// first send (none here) and the application pong are tracked: make the pong
	// answering the server ping fail.
	polling := newControllableTransport(engineio.TransportTypePolling)
	polling.sendErrs = []error{errors.New("pong write failed")}
	withFakeTransports(t, map[engineio.TransportType]engineio.Transport{
		engineio.TransportTypePolling: polling,
	})

	socket, err := engineio.NewSocket("http://localhost/engine.io/",
		engineio.WithTransports(engineio.TransportTypePolling),
		engineio.WithUpgrade(false),
	)
	require.NoError(t, err)

	errs := make(chan error, 1)
	opened := make(chan struct{}, 1)
	socket.OnError(func(err error) {
		select {
		case errs <- err:

		default:
		}
	})
	socket.OnOpen(func() { opened <- struct{}{} })
	t.Cleanup(func() { socket.Close(context.WithoutCancel(t.Context())) })

	// Act: open and complete the handshake so the socket is live
	socket.Open(t.Context())
	polling.deliverPacket(t.Context(), engineio.Packet{Type: engineio.PacketOpen, Data: handshakeData(t)})
	<-opened

	// Act: deliver a server ping, which the socket answers with a pong that fails
	polling.deliverPacket(t.Context(), engineio.Packet{Type: engineio.PacketPing})

	// Assert: the failed pong send is reported to the error handler
	require.ErrorContains(t, <-errs, "sending pong packet")
}
