package engineio_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_AllowRequest_RejectsForbidden(t *testing.T) {
	t.Parallel()

	// Arrange: a server that rejects every handshake and records the rejection
	type rejection struct {
		code   engineio.ConnectionErrorCode
		reason string
	}
	rejections := make(chan rejection, 1)
	server := engineio.NewServer(engineio.WithAllowRequest(func(*http.Request) error {
		return errors.New("nope")
	}))
	t.Cleanup(server.Close)
	server.OnConnectionError(func(_ *http.Request, code engineio.ConnectionErrorCode, reason string) {
		rejections <- rejection{code: code, reason: reason}
	})

	// Act: attempt a handshake
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(""), nil))

	// Assert: it is rejected with 403 Forbidden and the rejection is reported with
	// the gate's error message.
	require.Equal(t, http.StatusForbidden, rec.Code)
	got := <-rejections
	require.Equal(t, engineio.ConnectionErrorForbidden, got.code)
	require.Equal(t, "nope", got.reason)
}

func TestServer_AllowRequest_AllowsValidHandshake(t *testing.T) {
	t.Parallel()

	// Arrange: a gate that inspects the request and allows it
	server := engineio.NewServer(engineio.WithAllowRequest(func(r *http.Request) error {
		require.Equal(t, "4", r.URL.Query().Get("EIO"))
		return nil
	}))
	t.Cleanup(server.Close)

	// Act + Assert: the handshake succeeds
	open := handshake(t, server)
	require.NotEmpty(t, open.SessionID)
}
