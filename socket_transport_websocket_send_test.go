package engineio_test

import (
	"net/url"
	"testing"

	"github.com/coder/websocket"
	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestWebSocketTransport_Send_WritesPacket_WithOpenState(t *testing.T) {
	t.Parallel()

	// Arrange: start a websocket server and open a transport against it
	u, conns := newWebSocketTestServer(t)
	transport, conn := openWebSocketTransport(t, u, conns)

	// Act: send a binary message followed by a text message
	require.NoError(t, transport.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03, 0x04}, IsBinary: true},
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	}))

	// Assert: the binary message is delivered as a binary frame of raw bytes
	messageType, data, err := conn.Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, websocket.MessageBinary, messageType)
	require.Equal(t, []byte{0x01, 0x02, 0x03, 0x04}, data)

	// Assert: the text message is delivered as a text frame encoded with its type
	messageType, data, err = conn.Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, messageType)
	require.Equal(t, []byte("4hello"), data)

	// Act: pause is a no-op for the websocket transport, which keeps it open
	transport.Pause(t.Context())
	require.Equal(t, engineio.TransportStateOpen, transport.State())
}

func TestWebSocketTransport_Send_IgnoresPacket_WithClosedState(t *testing.T) {
	t.Parallel()

	// Arrange: create a transport without opening it
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	transport, err := engineio.NewWebSocketTransport(u, nil, nil)
	require.NoError(t, err)

	// Act: send on the closed transport
	err = transport.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	})

	// Assert: the send is a no-op and the transport stays closed
	require.NoError(t, err)
	require.Equal(t, engineio.TransportStateClosed, transport.State())
}
