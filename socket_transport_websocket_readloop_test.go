package engineio_test

import (
	"context"
	"testing"

	"github.com/coder/websocket"
	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestWebSocketTransport_CallsOnPacketHandler(t *testing.T) {
	t.Parallel()

	// Arrange: start a websocket server and open a transport against it
	u, conns := newWebSocketTestServer(t)

	// Arrange: capture received packets
	packets := make(chan engineio.Packet, 2)

	transport, err := engineio.NewWebSocketTransport(u, nil, nil)
	require.NoError(t, err)
	transport.OnPacket(func(_ context.Context, p engineio.Packet) {
		packets <- p
	})

	onOpen := make(chan struct{}, 1)
	transport.OnOpen(func(context.Context) {
		onOpen <- struct{}{}
	})
	transport.Open(t.Context())
	t.Cleanup(func() {
		transport.Close(t.Context())
	})

	conn := <-conns
	<-onOpen

	// Act: the server writes a text frame and a binary frame
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("4hello")))
	require.NoError(t, conn.Write(t.Context(), websocket.MessageBinary, []byte{0x05, 0x06, 0x07}))

	// Assert: the text frame decodes to a text message
	textPacket := <-packets
	require.Equal(t, engineio.PacketMessage, textPacket.Type)
	require.False(t, textPacket.IsBinary)
	require.Equal(t, []byte("hello"), textPacket.Data)

	// Assert: the binary frame decodes to a binary message carrying raw bytes
	binaryPacket := <-packets
	require.Equal(t, engineio.PacketMessage, binaryPacket.Type)
	require.True(t, binaryPacket.IsBinary)
	require.Equal(t, []byte{0x05, 0x06, 0x07}, binaryPacket.Data)
}

func TestWebSocketTransport_ReadLoop_ContinuesAfterBadFrame(t *testing.T) {
	t.Parallel()

	// Arrange: start a websocket server and open a transport against it
	u, conns := newWebSocketTestServer(t)

	errs := make(chan error, 1)
	packets := make(chan engineio.Packet, 1)

	transport, err := engineio.NewWebSocketTransport(u, nil, nil)
	require.NoError(t, err)
	transport.OnError(func(_ context.Context, err error) {
		select {
		case errs <- err:

		default:
		}
	})
	transport.OnPacket(func(_ context.Context, p engineio.Packet) {
		select {
		case packets <- p:

		default:
		}
	})

	onOpen := make(chan struct{}, 1)
	transport.OnOpen(func(context.Context) { onOpen <- struct{}{} })
	transport.Open(t.Context())
	t.Cleanup(func() { transport.Close(t.Context()) })

	conn := <-conns
	<-onOpen

	// Act: the server writes a frame whose packet type ('9') is invalid, then a
	// valid message frame.
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("9")))
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("4hello")))

	// Assert: the bad frame is reported as an error but the read loop keeps going,
	// so the following valid frame is still delivered (the connection is not torn
	// down by one bad frame).
	require.ErrorContains(t, <-errs, "decoding websocket frame")

	packet := <-packets
	require.Equal(t, engineio.PacketMessage, packet.Type)
	require.Equal(t, []byte("hello"), packet.Data)
	require.Equal(t, engineio.TransportStateOpen, transport.State())
}

func TestWebSocketTransport_ReadLoop_ServerCloseFrameClosesTransport(t *testing.T) {
	t.Parallel()

	// Arrange: start a websocket server and open a transport against it
	u, conns := newWebSocketTestServer(t)

	packets := make(chan engineio.Packet, 1)

	transport, err := engineio.NewWebSocketTransport(u, nil, nil)
	require.NoError(t, err)
	transport.OnPacket(func(_ context.Context, p engineio.Packet) {
		select {
		case packets <- p:

		default:
		}
	})

	onOpen := make(chan struct{}, 1)
	transport.OnOpen(func(context.Context) { onOpen <- struct{}{} })
	onClose := make(chan struct{}, 1)
	transport.OnClose(func(context.Context) { onClose <- struct{}{} })

	transport.Open(t.Context())
	t.Cleanup(func() { transport.Close(t.Context()) })

	conn := <-conns
	<-onOpen

	// Act: the server writes a close packet frame
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("1")))

	// Assert: the close packet is delivered, then the transport closes
	packet := <-packets
	require.Equal(t, engineio.PacketClose, packet.Type)
	<-onClose
	require.Equal(t, engineio.TransportStateClosed, transport.State())
}
