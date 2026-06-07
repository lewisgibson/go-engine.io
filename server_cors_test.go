package engineio_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_CORS_AnswersPreflight(t *testing.T) {
	t.Parallel()

	// Arrange: a server that allows credentialed requests from a fixed origin
	server := engineio.NewServer(engineio.WithCORS(engineio.CORSOptions{
		AllowCredentials: true,
		AllowedOrigins:   []string{"https://example.com"},
	}))
	t.Cleanup(server.Close)

	// Act: send a preflight from an allowed origin
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, pollingURL(""), nil)
	req.Header.Set("Origin", "https://example.com")
	server.ServeHTTP(rec, req)

	// Assert: the preflight is answered with the CORS headers
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "https://example.com", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
	require.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), http.MethodPost)
}
