package engineio_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_Poll_DeliversBufferedMessages(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: a handshaken session
		sockets := make(chan *engineio.ServerSocket, 1)
		server := engineio.NewServer(fastServerOptions()...)
		server.OnConnection(func(socket *engineio.ServerSocket) { sockets <- socket })
		open := handshake(t, server)
		socket := <-sockets

		// Assert: the session reports the negotiated id
		require.Equal(t, open.SessionID, socket.ID())

		// Act: buffer a message before any poll, then poll
		require.NoError(t, socket.Send([]byte("queued"), false))
		result := pollInBackground(server, open.SessionID)
		synctest.Wait()

		// Assert: the poll returns the buffered message
		require.Equal(t, "4queued", <-result)
	})
}

func TestServer_Poll_DeliversToHeldPoll(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: a handshaken session with a poll already held
		sockets := make(chan *engineio.ServerSocket, 1)
		server := engineio.NewServer(fastServerOptions()...)
		server.OnConnection(func(socket *engineio.ServerSocket) { sockets <- socket })
		open := handshake(t, server)
		socket := <-sockets

		result := pollInBackground(server, open.SessionID)
		synctest.Wait()

		// Act: send to the held poll
		require.NoError(t, socket.Send([]byte{0x09, 0x08}, true))
		synctest.Wait()

		// Assert: the binary message is delivered base64-encoded behind 'b'
		require.Equal(t, "b"+base64Std([]byte{0x09, 0x08}), <-result)
	})
}

func TestServer_Poll_RejectsOverlappingPolls(t *testing.T) {
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
		<-sockets

		first := pollInBackground(server, open.SessionID)
		synctest.Wait()

		// Act: issue a second, overlapping poll
		second := pollSync(server, open.SessionID)
		synctest.Wait()

		// Assert: the overlapping poll is rejected and the session closes
		require.Equal(t, http.StatusBadRequest, second.Code)
		require.Equal(t, "transport error", <-closed)

		// Assert: the original poll is released with a close packet
		require.Equal(t, "1", <-first)
	})
}

func TestServer_Poll_ClientDisconnectClosesSession(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: a handshaken session with a poll held on a cancellable request
		sockets := make(chan *engineio.ServerSocket, 1)
		closed := make(chan string, 1)
		server := engineio.NewServer(fastServerOptions()...)
		server.OnConnection(func(socket *engineio.ServerSocket) {
			socket.OnClose(func(reason string, _ error) { closed <- reason })
			sockets <- socket
		})
		open := handshake(t, server)
		<-sockets

		ctx, cancel := context.WithCancel(t.Context())
		var done = make(chan struct{})
		go func() {
			defer close(done)
			req := httptest.NewRequest(http.MethodGet, pollingURL(open.SessionID), nil).WithContext(ctx)
			server.ServeHTTP(httptest.NewRecorder(), req)
		}()
		synctest.Wait()

		// Act: the client disconnects mid-poll
		cancel()
		synctest.Wait()

		// Assert: the session closes with the transport-close reason
		require.Equal(t, "transport close", <-closed)
		<-done
	})
}
