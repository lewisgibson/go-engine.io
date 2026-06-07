package engineio_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

// base64Std returns the standard padded base64 encoding of data.
func base64Std(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// fastServerOptions are small but non-trivial heartbeat values for synctest,
// where the fake clock makes their absolute size irrelevant.
func fastServerOptions() []engineio.ServerOption {
	return []engineio.ServerOption{
		engineio.WithPingInterval(100 * time.Millisecond),
		engineio.WithPingTimeout(100 * time.Millisecond),
	}
}

// pollingURL builds an Engine.IO polling request URL for the given session.
func pollingURL(sid string) string {
	var target = "/engine.io/?EIO=4&transport=polling"
	if sid != "" {
		target += "&sid=" + sid
	}
	return target
}

// handshake performs the synchronous handshake and returns the open packet.
func handshake(t *testing.T, server *engineio.Server) engineio.OpenPacket {
	t.Helper()

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(""), nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.Bytes()
	require.NotEmpty(t, body)
	require.Equal(t, byte('0'), body[0])

	var open engineio.OpenPacket
	require.NoError(t, json.Unmarshal(body[1:], &open))

	return open
}

// pollInBackground issues a long-poll in a bubble goroutine and returns a
// channel that receives the response body once the poll completes.
func pollInBackground(server *engineio.Server, sid string) <-chan string {
	var result = make(chan string, 1)
	go func() {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(sid), nil))
		result <- rec.Body.String()
	}()
	return result
}

// postPackets sends a POST of encoded packets and returns the recorder.
func postPackets(server *engineio.Server, sid string, body []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, pollingURL(sid), bytes.NewReader(body))
	server.ServeHTTP(rec, req)
	return rec
}

// pollSync issues a synchronous GET poll and returns the recorder.
func pollSync(server *engineio.Server, sid string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(sid), nil))
	return rec
}
