package engineio_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

// findCookie returns the cookie with the given name, or nil if it is absent.
func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}

	return nil
}

func TestServer_Cookie_SetOnHandshake(t *testing.T) {
	t.Parallel()

	// Arrange: a server configured to set a session-affinity cookie
	server := engineio.NewServer(engineio.WithCookie(engineio.CookieOptions{HTTPOnly: true}))
	t.Cleanup(server.Close)

	// Act: perform the polling handshake
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(""), nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var open engineio.OpenPacket
	require.NoError(t, json.Unmarshal(rec.Body.Bytes()[1:], &open))

	// Assert: the response sets the default "io" cookie carrying the session id, so
	// a load balancer can pin every later request for this session to this node
	cookie := findCookie(rec.Result().Cookies(), "io")
	require.NotNil(t, cookie)
	require.Equal(t, open.SessionID, cookie.Value)
	require.Equal(t, "/", cookie.Path)
	require.True(t, cookie.HttpOnly)
}

func TestServer_Cookie_HonoursCustomAttributes(t *testing.T) {
	t.Parallel()

	// Arrange: a server whose affinity cookie overrides every attribute
	server := engineio.NewServer(engineio.WithCookie(engineio.CookieOptions{
		Name:     "lb",
		Path:     "/engine.io/",
		HTTPOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   3600,
	}))
	t.Cleanup(server.Close)

	// Act: perform the polling handshake
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(""), nil))
	require.Equal(t, http.StatusOK, rec.Code)

	// Assert: the cookie carries the configured name and attributes
	cookie := findCookie(rec.Result().Cookies(), "lb")
	require.NotNil(t, cookie)
	require.Equal(t, "/engine.io/", cookie.Path)
	require.True(t, cookie.HttpOnly)
	require.True(t, cookie.Secure)
	require.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	require.Equal(t, 3600, cookie.MaxAge)
}

func TestServer_Cookie_AbsentByDefault(t *testing.T) {
	t.Parallel()

	// Arrange: a server with no cookie configured
	server := engineio.NewServer()
	t.Cleanup(server.Close)

	// Act: perform the polling handshake
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(""), nil))
	require.Equal(t, http.StatusOK, rec.Code)

	// Assert: the affinity cookie is opt-in, so none is set without WithCookie
	require.Empty(t, rec.Result().Cookies())
}

func TestServer_Cookie_SetOnlyAtSessionCreation(t *testing.T) {
	t.Parallel()

	// Arrange: a cookie-configured server with one handshaken session
	server := engineio.NewServer(engineio.WithCookie(engineio.CookieOptions{}))
	t.Cleanup(server.Close)

	open := handshake(t, server)

	// Act: send on the established session
	rec := postPackets(server, open.SessionID, []byte("4hi"))
	require.Equal(t, http.StatusOK, rec.Code)

	// Assert: the cookie is set once at session creation, not re-set on every later
	// request -- the load balancer pins the session from the handshake response
	require.Nil(t, findCookie(rec.Result().Cookies(), "io"))
}
