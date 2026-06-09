package engineio_test

import (
	"net/url"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/require"
)

func TestWebTransportTransport_Send_IgnoresPacket_WithClosedState(t *testing.T) {
	t.Parallel()

	// Arrange: a webtransport transport that has never been opened, so it is closed.
	u, err := url.Parse("https://localhost/engine.io/?EIO=4&transport=webtransport")
	require.NoError(t, err)

	transport, err := engineio.NewWebTransportTransport(u, &webtransport.Dialer{}, nil)
	require.NoError(t, err)

	// Act: send while the transport is closed.
	err = transport.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	})

	// Assert: the packet is dropped without error, leaving retention to the socket's
	// buffer, and the transport stays closed.
	require.NoError(t, err)
	require.Equal(t, engineio.TransportStateClosed, transport.State())
}
