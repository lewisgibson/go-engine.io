package engineio_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_Handshake_AdvertisesParameters(t *testing.T) {
	t.Parallel()

	// Arrange: a server with default options
	connections := make(chan *engineio.ServerSocket, 1)
	server := engineio.NewServer()
	server.OnConnection(func(socket *engineio.ServerSocket) { connections <- socket })
	t.Cleanup(server.Close)

	// Act: perform the handshake
	open := handshake(t, server)

	// Assert: the advertised parameters match the defaults
	require.NotEmpty(t, open.SessionID)
	require.Equal(t, []engineio.TransportType{engineio.TransportTypeWebSocket}, open.Upgrades)
	require.Equal(t, 25000, open.PingInterval)
	require.Equal(t, 20000, open.PingTimeout)
	require.Equal(t, 1_000_000, open.MaxPayload)

	// Assert: the connection handler fired exactly once
	require.Len(t, connections, 1)
}

func TestServer_Handshake_WriteFailureRemovesSession(t *testing.T) {
	t.Parallel()

	// Arrange: a server with a fixed id generator so the session can be looked up
	const sessionID = "fixed-handshake-id"
	server := engineio.NewServer(engineio.WithGenerateID(func(*http.Request) string { return sessionID }))
	t.Cleanup(server.Close)

	// Act: drive the handshake against a writer that fails the open-packet write
	w := newFailingResponseWriter()
	server.ServeHTTP(w, httptest.NewRequest(http.MethodGet, pollingURL(""), nil))

	// Assert: the failed write left no session behind, so a poll for that id is
	// rejected as an unknown session (code 1) rather than served.
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(sessionID), nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	var payload struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, 1, payload.Code)
}
