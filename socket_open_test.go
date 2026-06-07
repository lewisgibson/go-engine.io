package engineio_test

import (
	"context"
	"errors"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestSocket_Open_NoTransports(t *testing.T) {
	t.Parallel()

	// Arrange: a socket configured with no transports
	socket, err := engineio.NewSocket("http://localhost/engine.io/", engineio.WithTransports())
	require.NoError(t, err)

	errs := make(chan error, 1)
	socket.OnError(func(err error) { errs <- err })

	// Act: open the socket
	socket.Open(t.Context())

	// Assert: opening reports that no transports are available
	require.ErrorIs(t, <-errs, engineio.ErrNoTransports)
}

func TestSocket_OnOpen_FlushFailureReportsError(t *testing.T) {
	// Arrange: a controllable transport whose first send fails, so the flush of a
	// write buffered while opening fails once the handshake completes.
	polling := newControllableTransport(engineio.TransportTypePolling)
	polling.sendErrs = []error{errors.New("flush write failed")}
	withFakeTransports(t, map[engineio.TransportType]engineio.Transport{
		engineio.TransportTypePolling: polling,
	})

	socket, err := engineio.NewSocket("http://localhost/engine.io/",
		engineio.WithTransports(engineio.TransportTypePolling),
		engineio.WithUpgrade(false),
	)
	require.NoError(t, err)

	errs := make(chan error, 1)
	socket.OnError(func(err error) {
		select {
		case errs <- err:

		default:
		}
	})
	t.Cleanup(func() { socket.Close(context.WithoutCancel(t.Context())) })

	// Act: open, buffer a write while still opening, then complete the handshake,
	// which flushes the buffered write and fails on the first send.
	socket.Open(t.Context())
	require.NoError(t, socket.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("queued")},
	}))
	polling.deliverPacket(t.Context(), engineio.Packet{Type: engineio.PacketOpen, Data: handshakeData(t)})

	// Assert: the failed flush is reported to the error handler
	require.ErrorContains(t, <-errs, "flushing buffered packets")
}

func TestSocket_TryAllTransports_FallbackUsesLiveContext(t *testing.T) {
	// Arrange: a websocket transport that fails to open and a polling fallback. A
	// prior bug reopened the fallback from the run context that the first Open had
	// already cancelled, so the fallback could never connect in production.
	failing := newControllableTransport(engineio.TransportTypeWebSocket)
	failing.openErr = errors.New("dial failed")
	fallback := newControllableTransport(engineio.TransportTypePolling)
	withFakeTransports(t, map[engineio.TransportType]engineio.Transport{
		engineio.TransportTypeWebSocket: failing,
		engineio.TransportTypePolling:   fallback,
	})

	socket, err := engineio.NewSocket("http://localhost/engine.io/",
		engineio.WithTransports(engineio.TransportTypeWebSocket, engineio.TransportTypePolling),
		engineio.WithTryAllTransports(true),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	// The fallback only fires OnOpen when it was opened with a live context, so a
	// fired OnOpen proves the fallback received an uncancelled context.
	fallback.OnOpen(func(context.Context) {
		select {
		case opened <- struct{}{}:

		default:
		}
	})
	socket.OnError(func(error) {})
	t.Cleanup(func() { socket.Close(context.WithoutCancel(t.Context())) })

	// Act: open. The websocket transport fails, so the socket falls back.
	socket.Open(t.Context())

	// Assert: the fallback opened with a live (uncancelled) context
	<-opened
	require.NoError(t, fallback.openContextErr())
}
