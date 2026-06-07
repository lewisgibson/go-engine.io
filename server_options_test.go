package engineio_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestNewServer_WithPingInterval(t *testing.T) {
	t.Parallel()

	// Arrange: a server configured with a ping interval
	server := engineio.NewServer(engineio.WithPingInterval(1234 * time.Millisecond))
	t.Cleanup(server.Close)

	// Act: perform the handshake
	open := handshake(t, server)

	// Assert: the advertised ping interval reflects the option
	require.Equal(t, 1234, open.PingInterval)
}

func TestNewServer_WithPingTimeout(t *testing.T) {
	t.Parallel()

	// Arrange: a server configured with a ping timeout
	server := engineio.NewServer(engineio.WithPingTimeout(4321 * time.Millisecond))
	t.Cleanup(server.Close)

	// Act: perform the handshake
	open := handshake(t, server)

	// Assert: the advertised ping timeout reflects the option
	require.Equal(t, 4321, open.PingTimeout)
}

func TestNewServer_WithMaxPayload_AdvertisesLimit(t *testing.T) {
	t.Parallel()

	// Arrange: a server configured with a max payload
	server := engineio.NewServer(engineio.WithMaxPayload(2048))
	t.Cleanup(server.Close)

	// Act: perform the handshake
	open := handshake(t, server)

	// Assert: the advertised max payload reflects the option
	require.Equal(t, 2048, open.MaxPayload)
}

func TestNewServer_WithServerTransports_PollingOnly(t *testing.T) {
	t.Parallel()

	// Arrange: a server restricted to the polling transport
	server := engineio.NewServer(engineio.WithServerTransports(engineio.TransportTypePolling))
	t.Cleanup(server.Close)

	// Act: perform the handshake
	open := handshake(t, server)

	// Assert: no websocket upgrade is advertised
	require.Empty(t, open.Upgrades)
}

func TestNewServer_WithAllowUpgrades_False(t *testing.T) {
	t.Parallel()

	// Arrange: a server with upgrades disabled
	server := engineio.NewServer(engineio.WithAllowUpgrades(false))
	t.Cleanup(server.Close)

	// Act: perform the handshake
	open := handshake(t, server)

	// Assert: no websocket upgrade is advertised
	require.Empty(t, open.Upgrades)
}

func TestNewServer_WithUpgradeTimeout(t *testing.T) {
	t.Parallel()

	// Arrange: a server configured with an upgrade timeout
	server := engineio.NewServer(engineio.WithUpgradeTimeout(3 * time.Second))
	t.Cleanup(server.Close)

	// Act: perform the handshake
	open := handshake(t, server)

	// Assert: the handshake still serves a session id
	require.NotEmpty(t, open.SessionID)
}

func TestNewServer_WithGenerateID(t *testing.T) {
	t.Parallel()

	// Arrange: a server whose id generator returns a fixed value
	const sessionID = "fixed-session-id"
	server := engineio.NewServer(engineio.WithGenerateID(func(*http.Request) string { return sessionID }))
	t.Cleanup(server.Close)

	// Act: perform the handshake
	open := handshake(t, server)

	// Assert: the advertised session id is the one the generator returned
	require.Equal(t, sessionID, open.SessionID)
}

func TestNewServer_WithMaxPayload_RejectsOversizedBody(t *testing.T) {
	t.Parallel()

	// Arrange: a server with a tiny payload limit and a handshaken session
	server := engineio.NewServer(engineio.WithMaxPayload(8))
	t.Cleanup(server.Close)

	open := handshake(t, server)

	// Act: POST a body larger than the configured limit
	rec := httptest.NewRecorder()
	target := "/engine.io/?EIO=4&transport=polling&sid=" + open.SessionID
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader([]byte("4this-body-is-too-long")))
	server.ServeHTTP(rec, req)

	// Assert: the oversized body is rejected and the limit was advertised
	require.Equal(t, 8, open.MaxPayload)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestNewServer_WithCORS(t *testing.T) {
	t.Parallel()

	const origin = "https://allowed.example.com"

	newCORSServer := func() *engineio.Server {
		server := engineio.NewServer(engineio.WithCORS(engineio.CORSOptions{
			AllowCredentials: true,
			AllowedOrigins:   []string{origin},
		}))
		t.Cleanup(server.Close)

		return server
	}

	t.Run("answers the preflight", func(t *testing.T) {
		t.Parallel()

		// Arrange: a CORS-configured server
		server := newCORSServer()

		// Act: send an OPTIONS preflight from the allowed origin
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/engine.io/?EIO=4&transport=polling", nil)
		req.Header.Set("Origin", origin)
		server.ServeHTTP(rec, req)

		// Assert: the preflight carries the CORS headers
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.Equal(t, origin, rec.Header().Get("Access-Control-Allow-Origin"))
		require.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
	})

	t.Run("sets headers on a normal request", func(t *testing.T) {
		t.Parallel()

		// Arrange: a CORS-configured server
		server := newCORSServer()

		// Act: perform a handshake GET from the allowed origin
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/engine.io/?EIO=4&transport=polling", nil)
		req.Header.Set("Origin", origin)
		server.ServeHTTP(rec, req)

		// Assert: the response carries the CORS headers
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, origin, rec.Header().Get("Access-Control-Allow-Origin"))
		require.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
	})
}
