package engineio_test

import (
	"context"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestWebSocketTransport_Close_CallsOnCloseHandler(t *testing.T) {
	t.Parallel()

	// Arrange: start a websocket server and open a transport against it
	u, conns := newWebSocketTestServer(t)

	transport, err := engineio.NewWebSocketTransport(u, nil, nil)
	require.NoError(t, err)

	onOpen := make(chan struct{}, 1)
	transport.OnOpen(func(context.Context) {
		onOpen <- struct{}{}
	})
	onClose := make(chan struct{}, 1)
	transport.OnClose(func(context.Context) {
		onClose <- struct{}{}
	})

	transport.Open(t.Context())
	<-conns
	<-onOpen

	// Act: close the transport
	transport.Close(t.Context())

	// Assert: the close handler fires and the transport reports closed
	<-onClose
	require.Equal(t, engineio.TransportStateClosed, transport.State())
}
