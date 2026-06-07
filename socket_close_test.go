package engineio_test

import (
	"bytes"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSocket_ClosesOnPingTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: a mock client that completes the handshake then goes silent
		var getCount atomic.Int32
		mockTransportClient := NewMockTransportClient(gomock.NewController(t))
		mockTransportClient.EXPECT().
			Do(gomock.Any()).
			DoAndReturn(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.Method == http.MethodPost:
					return okResponse(nil), nil

				case getCount.Add(1) == 1:
					return okResponse(clientHandshakeBody(t, 100, 100)), nil
				}
				// Subsequent polls return nothing: the server is silent.
				return okResponse(nil), nil
			}).
			AnyTimes()

		socket, err := engineio.NewSocket("http://localhost/engine.io/",
			engineio.WithClient(mockTransportClient),
			engineio.WithUpgrade(false),
		)
		require.NoError(t, err)

		closed := make(chan string, 1)
		socket.OnClose(func(reason string, _ error) { closed <- reason })

		// Act: open the socket and let the heartbeat deadline elapse
		socket.Open(t.Context())
		synctest.Wait()

		// Assert: the socket closes with the ping-timeout reason
		require.Equal(t, "ping timeout", <-closed)
	})
}

func TestSocket_ClosesWhenPollFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: a mock client that completes the handshake then rejects the
		// next poll. The rejection must close the socket and must not deadlock the
		// poll goroutine against its own transport close.
		var getCount atomic.Int32
		mockTransportClient := NewMockTransportClient(gomock.NewController(t))
		mockTransportClient.EXPECT().
			Do(gomock.Any()).
			DoAndReturn(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.Method == http.MethodPost:
					return okResponse(nil), nil

				case getCount.Add(1) == 1:
					return okResponse(clientHandshakeBody(t, 100, 100)), nil
				}
				return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(bytes.NewReader(nil))}, nil
			}).
			AnyTimes()

		socket, err := engineio.NewSocket("http://localhost/engine.io/",
			engineio.WithClient(mockTransportClient),
			engineio.WithUpgrade(false),
		)
		require.NoError(t, err)

		closed := make(chan string, 1)
		socket.OnClose(func(reason string, _ error) { closed <- reason })

		// Act: open the socket; the failing poll drives the close
		socket.Open(t.Context())
		synctest.Wait()

		// Assert: the socket closes with the transport-error reason
		require.Equal(t, "transport error", <-closed)
	})
}
