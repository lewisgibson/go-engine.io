package engineio_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/coder/websocket"
	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

// secWebSocketAccept returns the value of the Sec-WebSocket-Accept header for the given Sec-WebSocket-Key.
func secWebSocketAccept(secWebSocketKey string) string {
	const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	var hash = sha1.Sum([]byte(secWebSocketKey + guid))
	return base64.StdEncoding.EncodeToString(hash[:])
}

func TestNewWebSocketTransport(t *testing.T) {
	t.Parallel()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Act: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, http.DefaultClient, http.Header{})
	require.NoError(t, err)

	// Assert: the transport is a websocket transport with the correct state
	require.Equal(t, engineio.TransportTypeWebSocket, transport.Type())
	require.Equal(t, engineio.TransportStateClosed, transport.State())
}

func TestNewWebSocketTransport_NilURL(t *testing.T) {
	t.Parallel()

	// Act: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(nil, http.DefaultClient, http.Header{})

	// Assert: the ErrURLRequired error is returned
	require.ErrorIs(t, err, engineio.ErrURLRequired)
	require.Nil(t, transport)
}

// mockReadWriteCloser is a mock implementation of io.ReadWriteCloser. A real
// net.Conn has independent read and write streams; this mock collapses them
// onto one buffer, so a mutex guards the buffer and the closed flag against the
// concurrent read loop and writer.
type mockReadWriteCloser struct {
	mu     sync.Mutex
	buffer *bytes.Buffer
	closed bool
}

// Read reads from the buffer.
func (m *mockReadWriteCloser) Read(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, io.EOF
	}

	return m.buffer.Read(p)
}

// Write writes to the buffer.
func (m *mockReadWriteCloser) Write(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, io.EOF
	}

	return m.buffer.Write(p)
}

// Close marks the ReadWriteCloser as closed
func (m *mockReadWriteCloser) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closed = true

	return nil
}

// newWebSocketTestServer starts an httptest server that accepts a single
// websocket connection, hands the accepted server-side connection to the
// caller, and holds the request open until the connection closes. It returns
// the URL the client transport should dial. A real bidirectional connection is
// far more faithful than a loopback buffer for exercising per-frame send and
// receive.
func newWebSocketTestServer(t *testing.T) (*url.URL, <-chan *websocket.Conn) {
	t.Helper()

	conns := make(chan *websocket.Conn, 1)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		conns <- conn

		// Hold the handler open so the connection stays usable until the test
		// tears down (a hijacked connection's request context is not cancelled on
		// close), then close the connection so the handler goroutine exits.
		<-done
		if closeErr := conn.CloseNow(); closeErr != nil {
			return
		}
	}))
	t.Cleanup(func() {
		close(done)
		server.Close()
	})

	u, err := url.Parse(server.URL)
	require.NoError(t, err)

	return u, conns
}

// openWebSocketTransport opens a websocket transport against u and waits for it
// to be both accepted by the server and reported open.
func openWebSocketTransport(t *testing.T, u *url.URL, conns <-chan *websocket.Conn) (engineio.Transport, *websocket.Conn) {
	t.Helper()

	transport, err := engineio.NewWebSocketTransport(u, nil, nil)
	require.NoError(t, err)

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

	return transport, conn
}
