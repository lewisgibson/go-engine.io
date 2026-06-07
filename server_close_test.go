package engineio_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_Close_DeliversClosePacketToHeldPoll(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: a handshaken session with a poll held
		sockets := make(chan *engineio.ServerSocket, 1)
		closed := make(chan string, 1)
		server := engineio.NewServer(fastServerOptions()...)
		server.OnConnection(func(socket *engineio.ServerSocket) {
			socket.OnClose(func(reason string, _ error) { closed <- reason })
			sockets <- socket
		})
		open := handshake(t, server)
		socket := <-sockets

		result := pollInBackground(server, open.SessionID)
		synctest.Wait()

		// Act: gracefully close the session
		require.NoError(t, socket.Close())
		synctest.Wait()

		// Assert: the held poll returns a close packet and the session closes
		require.Equal(t, "1", <-result)
		require.Equal(t, "forced close", <-closed)
	})
}

func TestServer_Close_DrainsBufferedDataBeforeClosing(t *testing.T) {
	t.Parallel()

	// Arrange: a server that hands us each session
	sockets := make(chan *engineio.ServerSocket, 1)
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Second),
		engineio.WithPingTimeout(time.Second),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) { sockets <- socket })
	t.Cleanup(server.Close)

	// Arrange: handshake over polling; no poll is held afterwards
	open := handshake(t, server)
	socket := <-sockets

	// Act: queue a message and close before the client polls again
	require.NoError(t, socket.Send([]byte("final"), false))
	require.NoError(t, socket.Close())

	// Act: the client's next poll
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(open.SessionID), nil))
	require.Equal(t, http.StatusOK, rec.Code)

	// Assert: the poll delivered the buffered message first, then the close packet,
	// rather than dropping the message when the session tore down.
	packets, err := engineio.DecodePayload(engineio.ProtocolVersion4, rec.Body.Bytes())
	require.NoError(t, err)
	require.Len(t, packets, 2)
	require.Equal(t, engineio.PacketMessage, packets[0].Type)
	require.Equal(t, "final", string(packets[0].Data))
	require.Equal(t, engineio.PacketClose, packets[1].Type)
}
