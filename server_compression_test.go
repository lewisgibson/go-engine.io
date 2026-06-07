package engineio_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_HTTPCompression_GzipsLargePoll(t *testing.T) {
	t.Parallel()

	// Arrange: a server (compression is on by default) and a captured session
	sockets := make(chan *engineio.ServerSocket, 1)
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Second),
		engineio.WithPingTimeout(time.Second),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) { sockets <- socket })
	t.Cleanup(server.Close)

	open := handshake(t, server)
	socket := <-sockets

	// Arrange: a payload above the compression threshold
	large := strings.Repeat("y", 4096)
	require.NoError(t, socket.Send([]byte(large), false))

	// Act: poll advertising gzip
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, pollingURL(open.SessionID), nil)
	req.Header.Set("Accept-Encoding", "gzip")
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// Assert: the body is gzip-encoded and decompresses to the original payload
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	reader, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	require.NoError(t, err)
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())

	packets, err := engineio.DecodePayload(engineio.ProtocolVersion4, body)
	require.NoError(t, err)
	require.Len(t, packets, 1)
	require.Equal(t, large, string(packets[0].Data))
}

func TestServer_HTTPCompression_DisabledLeavesBodyPlain(t *testing.T) {
	t.Parallel()

	// Arrange: a server with compression disabled
	sockets := make(chan *engineio.ServerSocket, 1)
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Second),
		engineio.WithPingTimeout(time.Second),
		engineio.WithHTTPCompression(false),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) { sockets <- socket })
	t.Cleanup(server.Close)

	open := handshake(t, server)
	socket := <-sockets
	require.NoError(t, socket.Send([]byte(strings.Repeat("y", 4096)), false))

	// Act: poll advertising gzip
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, pollingURL(open.SessionID), nil)
	req.Header.Set("Accept-Encoding", "gzip")
	server.ServeHTTP(rec, req)

	// Assert: the body is left uncompressed
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Header().Get("Content-Encoding"))
}
