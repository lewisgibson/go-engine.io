package engineio_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

// failingResponseWriter is an http.ResponseWriter whose Write always fails, used
// to exercise the server path that closes a session when a held poll cannot
// deliver its payload to the client.
type failingResponseWriter struct {
	header http.Header
	status int
}

// newFailingResponseWriter creates a failingResponseWriter with an empty header.
func newFailingResponseWriter() *failingResponseWriter {
	return &failingResponseWriter{header: http.Header{}}
}

func (w *failingResponseWriter) Header() http.Header       { return w.header }
func (w *failingResponseWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func (w *failingResponseWriter) WriteHeader(status int)    { w.status = status }

func TestServerSocket_Send_ReturnsErrorWhenClosed(t *testing.T) {
	t.Parallel()

	// Arrange: a handshaken session that is then closed
	sockets := make(chan *engineio.ServerSocket, 1)
	server := engineio.NewServer()
	server.OnConnection(func(socket *engineio.ServerSocket) { sockets <- socket })
	t.Cleanup(server.Close)

	handshake(t, server)
	socket := <-sockets
	require.NoError(t, socket.Close())

	// Act: send on the closed session
	err := socket.Send([]byte("hello"), false)

	// Assert: the send is rejected with ErrSocketClosed
	require.ErrorIs(t, err, engineio.ErrSocketClosed)
}

func TestServerSocket_Close_OnClosedSessionReturnsNil(t *testing.T) {
	t.Parallel()

	// Arrange: a handshaken session that is then closed
	sockets := make(chan *engineio.ServerSocket, 1)
	server := engineio.NewServer()
	server.OnConnection(func(socket *engineio.ServerSocket) { sockets <- socket })
	t.Cleanup(server.Close)

	handshake(t, server)
	socket := <-sockets
	require.NoError(t, socket.Close())

	// Act: close the already-closed session
	err := socket.Close()

	// Assert: the duplicate close is harmless and returns nil
	require.NoError(t, err)
}

func TestServerSocket_CloseWithReason_PropagatesCause(t *testing.T) {
	t.Parallel()

	// Arrange: a handshaken session that captures the close reason and cause
	type closeEvent struct {
		reason string
		cause  error
	}
	closes := make(chan closeEvent, 1)
	server := engineio.NewServer()
	t.Cleanup(server.Close)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnClose(func(reason string, cause error) {
			closes <- closeEvent{reason: reason, cause: cause}
		})
	})

	open := handshake(t, server)

	// Act: POST a body the v4 codec cannot decode
	post := postPackets(server, open.SessionID, []byte("7"))

	// Assert: the close handler receives the parse-error reason and a non-nil cause
	// that wraps the underlying decode failure.
	require.Equal(t, http.StatusBadRequest, post.Code)
	event := <-closes
	require.Equal(t, "parse error", event.reason)
	require.Error(t, event.cause)
}

func TestServerSocket_HeldPoll_WriteFailureClosesSession(t *testing.T) {
	t.Parallel()

	// Arrange: a handshaken session with a buffered message and a captured close
	type closeEvent struct {
		reason string
		cause  error
	}
	closes := make(chan closeEvent, 1)
	sockets := make(chan *engineio.ServerSocket, 1)
	server := engineio.NewServer()
	t.Cleanup(server.Close)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnClose(func(reason string, cause error) {
			closes <- closeEvent{reason: reason, cause: cause}
		})
		sockets <- socket
	})

	open := handshake(t, server)
	socket := <-sockets
	require.NoError(t, socket.Send([]byte("buffered"), false))

	// Act: a poll on a writer that fails; the buffered message flushes to it and
	// the write fails.
	w := newFailingResponseWriter()
	req := httptest.NewRequest(http.MethodGet, pollingURL(open.SessionID), nil)
	server.ServeHTTP(w, req)

	// Assert: the failed poll write closes the session as a transport error,
	// carrying the underlying write error as the cause.
	event := <-closes
	require.Equal(t, "transport error", event.reason)
	require.Error(t, event.cause)
}
