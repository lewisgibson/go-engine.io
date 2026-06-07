package engineio_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_Errors(t *testing.T) {
	t.Parallel()

	// Arrange: a server with a small max payload and one handshaken session
	server := engineio.NewServer(engineio.WithMaxPayload(8))
	t.Cleanup(server.Close)

	open := handshake(t, server)

	tests := []struct {
		name       string
		method     string
		target     string
		body       []byte
		wantStatus int
		wantCode   int
	}{
		{
			name:       "unsupported protocol version",
			method:     http.MethodGet,
			target:     "/engine.io/?EIO=3&transport=polling",
			wantStatus: http.StatusBadRequest,
			wantCode:   5,
		},
		{
			name:       "unknown transport",
			method:     http.MethodGet,
			target:     "/engine.io/?EIO=4&transport=carrier-pigeon",
			wantStatus: http.StatusBadRequest,
			wantCode:   0,
		},
		{
			name:       "bad handshake method",
			method:     http.MethodPost,
			target:     pollingURL(""),
			wantStatus: http.StatusBadRequest,
			wantCode:   2,
		},
		{
			name:       "unknown session",
			method:     http.MethodGet,
			target:     pollingURL("does-not-exist"),
			wantStatus: http.StatusBadRequest,
			wantCode:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act: issue the request
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.target, bytes.NewReader(tt.body)))

			// Assert: the status and JSON error code match
			require.Equal(t, tt.wantStatus, rec.Code)
			require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

			var payload struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
			require.Equal(t, tt.wantCode, payload.Code)
			require.NotEmpty(t, payload.Message)
		})
	}

	// These subtests share the handshaken session and run serially: the
	// octet-stream case closes it.
	t.Run("payload too large", func(t *testing.T) {
		// Act: POST a body larger than MaxPayload
		rec := postPackets(server, open.SessionID, []byte("4this-is-too-long-for-the-limit"))

		// Assert: the request is rejected with 413
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})

	t.Run("octet-stream body rejected", func(t *testing.T) {
		// Act: POST with an octet-stream content type
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, pollingURL(open.SessionID), bytes.NewReader([]byte("4hi")))
		req.Header.Set("Content-Type", "application/octet-stream")
		server.ServeHTTP(rec, req)

		// Assert: the request is rejected
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}
