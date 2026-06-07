package engineio_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_WebSocket_FreshHandshake(t *testing.T) {
	t.Parallel()

	// Arrange: a server capturing inbound messages
	messages := make(chan string, 4)
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Second),
		engineio.WithPingTimeout(time.Second),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, _ bool) { messages <- string(data) })
	})

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	// Act: open a websocket directly (no polling phase)
	conn, _, err := websocket.Dial(t.Context(), httpServer.URL+"/engine.io/?EIO=4&transport=websocket", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
			t.Logf("failed to close websocket: %v", err)
		}
	})

	// Assert: the first frame is the open packet
	_, openFrame, err := conn.Read(t.Context())
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(openFrame), "0{"))

	// Act + Assert: a message sent over the websocket reaches the server
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("4hello")))
	require.Equal(t, "hello", <-messages)
}

func TestServer_WebSocket_HandshakeSetsCookie(t *testing.T) {
	t.Parallel()

	// Arrange: a server configured with a session-affinity cookie
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Second),
		engineio.WithPingTimeout(time.Second),
		engineio.WithCookie(engineio.CookieOptions{HTTPOnly: true}),
	)
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	// Act: open a fresh websocket handshake
	conn, resp, err := websocket.Dial(t.Context(), httpServer.URL+"/engine.io/?EIO=4&transport=websocket", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
			t.Logf("failed to close websocket: %v", err)
		}
	})

	// Assert: the upgrade response carried the affinity cookie, just like the
	// polling handshake does.
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "io" {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	require.NotEmpty(t, cookie.Value)
	require.True(t, cookie.HttpOnly)
}

func TestServer_WebSocket_UnknownSession(t *testing.T) {
	t.Parallel()

	// Arrange: a server with no sessions
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Second),
		engineio.WithPingTimeout(time.Second),
	)
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	// Act: dial a websocket upgrade for an unknown session
	conn, _, err := websocket.Dial(t.Context(), httpServer.URL+"/engine.io/?EIO=4&transport=websocket&sid=does-not-exist", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
			t.Logf("failed to close websocket: %v", err)
		}
	})

	// Assert: the server rejects the upgrade by closing the connection
	_, _, readErr := conn.Read(t.Context())
	require.Error(t, readErr)
}

func TestServer_WebSocket_UpgradeProbeTimeout(t *testing.T) {
	t.Parallel()

	// Arrange: a server with a short upgrade timeout
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Second),
		engineio.WithPingTimeout(time.Second),
		engineio.WithUpgradeTimeout(50*time.Millisecond),
	)
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	// Act: handshake over polling
	resp, err := http.Get(httpServer.URL + "/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	var open engineio.OpenPacket
	require.NoError(t, json.Unmarshal(body[1:], &open))

	// Act: start the probe but never commit the upgrade
	conn, _, err := websocket.Dial(t.Context(), httpServer.URL+"/engine.io/?EIO=4&transport=websocket&sid="+open.SessionID, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
			t.Logf("failed to close websocket: %v", err)
		}
	})

	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("2probe")))
	_, probePong, err := conn.Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, "3probe", string(probePong))

	// Assert: the stalled probe times out and the server closes the websocket
	_, _, readErr := conn.Read(t.Context())
	require.Error(t, readErr)
}

func TestServer_WebSocket_UpgradeFromPolling(t *testing.T) {
	t.Parallel()

	// Arrange: a server capturing inbound messages
	messages := make(chan string, 4)
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Second),
		engineio.WithPingTimeout(time.Second),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, _ bool) { messages <- string(data) })
	})

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	// Act: handshake over polling
	resp, err := http.Get(httpServer.URL + "/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	var open engineio.OpenPacket
	require.NoError(t, json.Unmarshal(body[1:], &open))

	// Act: hold a long-poll so the upgrade noop has somewhere to land
	var pollBody = make(chan string, 1)
	go func() {
		pollResp, pollErr := http.Get(httpServer.URL + "/engine.io/?EIO=4&transport=polling&sid=" + open.SessionID)
		if pollErr != nil {
			pollBody <- ""
			return
		}
		data, readErr := io.ReadAll(pollResp.Body)
		pollResp.Body.Close() //nolint:errcheck // best-effort close in the poll goroutine
		if readErr != nil {
			pollBody <- ""
			return
		}
		pollBody <- string(data)
	}()

	// Act: dial the websocket and run the probe handshake
	conn, _, err := websocket.Dial(t.Context(), httpServer.URL+"/engine.io/?EIO=4&transport=websocket&sid="+open.SessionID, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
			t.Logf("failed to close websocket: %v", err)
		}
	})

	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("2probe")))

	// Assert: the server replies with a probe pong and flushes the held poll
	// with a noop.
	_, probePong, err := conn.Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, "3probe", string(probePong))
	require.Equal(t, "6", <-pollBody)

	// Act: commit the upgrade and send a message over the websocket
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("5")))
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("4upgraded")))

	// Assert: the message arrives, proving traffic now flows over the websocket
	require.Equal(t, "upgraded", <-messages)
}

func TestServer_WebSocket_ProbeParseErrorKeepsSession(t *testing.T) {
	t.Parallel()

	// Arrange: a server capturing inbound messages
	messages := make(chan string, 4)
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Second),
		engineio.WithPingTimeout(time.Second),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, _ bool) { messages <- string(data) })
	})

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	// Act: handshake over polling
	resp, err := http.Get(httpServer.URL + "/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	var open engineio.OpenPacket
	require.NoError(t, json.Unmarshal(body[1:], &open))

	// Act: start a probe, then send a malformed frame over the probe connection
	conn, _, err := websocket.Dial(t.Context(), httpServer.URL+"/engine.io/?EIO=4&transport=websocket&sid="+open.SessionID, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
			t.Logf("failed to close websocket: %v", err)
		}
	})

	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("2probe")))
	_, probePong, err := conn.Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, "3probe", string(probePong))

	// "9" is not a valid packet type, so the frame fails to decode.
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("9")))

	// Assert: the server abandons only the probe (closing the websocket)
	_, _, readErr := conn.Read(t.Context())
	require.Error(t, readErr)

	// Assert: the polling session survives the bad probe frame; a message posted
	// over polling still reaches the server.
	payload := engineio.EncodePayload([]engineio.Packet{{Type: engineio.PacketMessage, Data: []byte("survived")}})
	post, err := http.Post(httpServer.URL+"/engine.io/?EIO=4&transport=polling&sid="+open.SessionID, "text/plain", bytes.NewReader(payload))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, post.StatusCode)
	require.NoError(t, post.Body.Close())
	require.Equal(t, "survived", <-messages)
}
