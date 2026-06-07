package engineio_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWebSocketTransport_SetURL(t *testing.T) {
	t.Parallel()

	// Arrange: parse the initial target url
	initial, err := url.Parse("http://a/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: parse a new target url
	next, err := url.Parse("http://bbbbbbbbbbbbb/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, "http://bbbbbbbbbbbbb/engine.io/?EIO=4&transport=websocket", req.URL.String())
			return nil, errors.New("mock error")
		}).
		MinTimes(1).
		MaxTimes(2)

	// Act: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(initial, mockTransportClient, nil)
	require.NoError(t, err)

	// Act: set the new url
	transport.SetURL(next)

	// Act: open the transport
	transport.Open(t.Context())

	// Act: close the transport
	transport.Close(t.Context())
}

func TestWebSocketTransport_Open_SetsStateToOpening(t *testing.T) {
	t.Parallel()

	// Arrange: create a channel to signal when the transport is opened
	onOpeningChan := make(chan struct{}, 1)

	// Arrange: create a channel to release the mock client after the assertion
	releaseChan := make(chan struct{})

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(*http.Request) (*http.Response, error) {
			onOpeningChan <- struct{}{}
			<-releaseChan
			return nil, errors.New("mock error")
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Act: open the transport
	go transport.Open(t.Context())

	// Act: wait for the transport to begin opening
	<-onOpeningChan

	// Assert: the transport state is opening
	require.Equal(t, engineio.TransportStateOpening, transport.State())

	// Act: release the mock client now that the assertion is complete
	close(releaseChan)
}

func TestWebSocketTransport_Open_CallsOnOpenHandler(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=websocket", req.URL.String())

			// Marshal the open packet data
			data, err := json.Marshal(engineio.OpenPacket{
				SessionID: "252937f5-aff9-4885-91ca-234802cede79",
			})
			require.NoError(t, err)

			// Encode the open packet
			packet := engineio.EncodePacket(engineio.Packet{
				Type: engineio.PacketOpen,
				Data: data,
			})

			// Respond with the open packet
			return &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Header: http.Header{
					"Connection":           []string{"Upgrade"},
					"Upgrade":              []string{"WebSocket"},
					"Sec-Websocket-Accept": []string{secWebSocketAccept(req.Header.Get("Sec-Websocket-Key"))},
				},
				Body: &mockReadWriteCloser{buffer: bytes.NewBuffer(packet)},
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Act: create a new websocket transport with the mock transport client
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport encounters an
	// error. The send is non-blocking: once the transport has opened the test
	// stops draining this channel, and the cleanup Close surfaces a benign
	// error from the mock loopback that must not block the transport.
	onErrChan := make(chan error, 1)
	transport.OnError(func(ctx context.Context, err error) {
		select {
		case onErrChan <- err:

		default:
		}
	})

	// Arrange: create a new channel to signal when the transport is opened
	onOpenChan := make(chan struct{}, 1)
	transport.OnOpen(func(ctx context.Context) {
		// Assert: the transport state is open
		require.Equal(t, engineio.TransportStateOpen, transport.State())

		// Signal that the transport is opened.
		onOpenChan <- struct{}{}
	})

	// Act: open the transport
	go transport.Open(t.Context())

	// Arrange: close the transport
	t.Cleanup(func() {
		transport.Close(t.Context())
	})

	// Wait for the transport to open. OnOpen fires synchronously within Open,
	// before the read loop starts, so it is signaled before the mock loopback's
	// benign read error; waiting on it directly keeps that error from preempting
	// the assertion.
	<-onOpenChan
}

func TestWebSocketTransport_Open_CallsOnErrorHandler(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=websocket", req.URL.String())

			// Respond with the open packet
			return nil, errors.New("mock error")
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport encounters an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "mock error")

		// Signal that the transport encountered an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport
	go transport.Open(t.Context())

	// Arrange: close the transport
	t.Cleanup(func() {
		transport.Close(t.Context())
	})

	// Wait for the transport to encounter an error.
	<-onErrorChan
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithBadURL(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Times(0)

	// Arrange: create an invalid URL
	u := &url.URL{Scheme: ":"}
	_, err := url.Parse(u.String())
	require.Errorf(t, err, "url should be invalid: '%s'", u.String())

	// Arrange: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport encounters an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "failed to parse url")

		// Signal that the transport encountered an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport
	go transport.Open(t.Context())

	// Arrange: close the transport
	t.Cleanup(func() {
		transport.Close(t.Context())
	})

	// Wait for the transport to encounter an error.
	<-onErrorChan
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithBadContext(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Times(0)

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport encounters an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "net/http: nil Context")

		// Signal that the transport encountered an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport with a nil context to exercise the error path
	go transport.Open(nil)

	// Arrange: close the transport
	t.Cleanup(func() {
		transport.Close(t.Context())
	})

	// Wait for the transport to encounter an error.
	<-onErrorChan
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithBadStatusCode(t *testing.T) {
	t.Parallel()

	var tests = []struct {
		name       string
		statusCode int
	}{
		{name: "not found", statusCode: http.StatusNotFound},
		{name: "bad gateway", statusCode: http.StatusBadGateway},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// Arrange: create a new mock transport client
			mockTransportClient := NewMockTransportClient(gomock.NewController(t))
			mockTransportClient.EXPECT().
				Do(gomock.Any()).
				DoAndReturn(func(req *http.Request) (*http.Response, error) {
					// Respond with a bad status code
					return &http.Response{
						StatusCode: test.statusCode,
					}, nil
				}).
				AnyTimes()

			// Arrange: parse the target url
			u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
			require.NoError(t, err)

			// Arrange: create a new websocket transport
			transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
			require.NoError(t, err)

			// Arrange: create a new channel to signal when the transport is opened
			onErrorChan := make(chan struct{}, 1)
			transport.OnError(func(ctx context.Context, err error) {
				// Assert: the error is not nil
				require.ErrorContains(t, err, "expected handshake response status code 101")

				// Signal that the transport is opened.
				onErrorChan <- struct{}{}
			})

			// Act: open the transport
			go transport.Open(t.Context())

			// Arrange: close the transport
			t.Cleanup(func() {
				transport.Close(t.Context())
			})

			// Wait for the transport to encounter an error.
			<-onErrorChan
		})
	}
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithNilResponseBody(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Respond with a bad status code
			return &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Header: http.Header{
					"Connection":           []string{"Upgrade"},
					"Upgrade":              []string{"WebSocket"},
					"Sec-Websocket-Accept": []string{secWebSocketAccept(req.Header.Get("Sec-Websocket-Key"))},
				},
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport encounters an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "response body is not a io.ReadWriteCloser")

		// Signal that the transport encountered an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport
	go transport.Open(t.Context())

	// Arrange: close the transport
	t.Cleanup(func() {
		transport.Close(t.Context())
	})

	// Wait for the transport to encounter an error.
	<-onErrorChan
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithBadResponseBody(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Respond with a bad status code
			return &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Header: http.Header{
					"Connection":           []string{"Upgrade"},
					"Upgrade":              []string{"WebSocket"},
					"Sec-Websocket-Accept": []string{secWebSocketAccept(req.Header.Get("Sec-Websocket-Key"))},
				},
				Body: &badReadWriteCloser{},
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal the expected read error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(_ context.Context, err error) {
		// Assert: the bad response body fails the first frame read. Ignore any
		// later best-effort error, such as one from closing the broken connection
		// during cleanup, which would otherwise race this assertion.
		if !strings.Contains(err.Error(), "failed to read frame header") {
			return
		}

		// Signal that the transport reported the expected read error.
		select {
		case onErrorChan <- struct{}{}:

		default:
		}
	})

	// Act: open the transport
	go transport.Open(t.Context())

	// Arrange: close the transport
	t.Cleanup(func() {
		transport.Close(t.Context())
	})

	// Wait for the transport to encounter the expected read error.
	<-onErrorChan
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithoutConnectionHeader(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Respond with a bad status code
			return &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Header: http.Header{
					"Upgrade":              []string{"WebSocket"},
					"Sec-Websocket-Accept": []string{secWebSocketAccept(req.Header.Get("Sec-Websocket-Key"))},
				},
				Body: &badReadWriteCloser{},
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport encounters an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "WebSocket protocol violation: Connection header")

		// Signal that the transport encountered an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport
	go transport.Open(t.Context())

	// Arrange: close the transport
	t.Cleanup(func() {
		transport.Close(t.Context())
	})

	// Wait for the transport to encounter an error.
	<-onErrorChan
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithoutUpgradeHeader(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Respond with a bad status code
			return &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Header: http.Header{
					"Connection":           []string{"Upgrade"},
					"Sec-Websocket-Accept": []string{secWebSocketAccept(req.Header.Get("Sec-Websocket-Key"))},
				},
				Body: &badReadWriteCloser{},
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport encounters an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "WebSocket protocol violation: Upgrade header")

		// Signal that the transport encountered an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport
	go transport.Open(t.Context())

	// Arrange: close the transport
	t.Cleanup(func() {
		transport.Close(t.Context())
	})

	// Wait for the transport to encounter an error.
	<-onErrorChan
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithBadSecWebSocketAcceptHeader(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Respond with a bad status code
			return &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Header: http.Header{
					"Connection": []string{"Upgrade"},
					"Upgrade":    []string{"WebSocket"},
				},
				Body: &badReadWriteCloser{},
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport encounters an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "WebSocket protocol violation: invalid Sec-WebSocket-Accept")

		// Signal that the transport encountered an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport
	go transport.Open(t.Context())

	// Arrange: close the transport
	t.Cleanup(func() {
		transport.Close(t.Context())
	})

	// Wait for the transport to encounter an error.
	<-onErrorChan
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithResponseBody(t *testing.T) {
	t.Parallel()

	// Arrange: a mock client that fails the handshake with a non-101 status and a
	// response body explaining why.
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(bytes.NewReader([]byte("origin not allowed"))),
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport
	transport, err := engineio.NewWebSocketTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: capture the failed-dial error
	onErrorChan := make(chan error, 1)
	transport.OnError(func(_ context.Context, err error) {
		select {
		case onErrorChan <- err:

		default:
		}
	})

	// Act: open the transport
	go transport.Open(t.Context())
	t.Cleanup(func() { transport.Close(t.Context()) })

	// Assert: the failed dial appends the server's response body to the error
	err = <-onErrorChan
	require.ErrorContains(t, err, "dialing websocket connection")
	require.ErrorContains(t, err, "origin not allowed")
}
