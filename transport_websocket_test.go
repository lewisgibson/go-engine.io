package engineio_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/lewisgibson/go-engine.io/internal/mocks"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// secWebSocketAccept returns the value of the Sec-WebSocket-Accept header for the given Sec-WebSocket-Key.
func secWebSocketAccept(secWebSocketKey string) string {
	const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	var hash = sha1.Sum([]byte(secWebSocketKey + guid))
	return base64.StdEncoding.EncodeToString(hash[:])
}

func TestNewWebSocketTransport(t *testing.T) {
	t.Parallel()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Act: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: http.DefaultClient,
		Header: http.Header{},
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Assert: the transport is a websocket transport with the correct state.
	require.Equal(t, engineio.TransportTypeWebSocket, transport.Type())
	require.Equal(t, engineio.TransportStateClosed, transport.State())
}

func TestNewWebSocketTransport_NilUrl(t *testing.T) {
	t.Parallel()

	// Act: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(nil, engineio.TransportOptions{
		Client: http.DefaultClient,
		Header: http.Header{},
	})

	// Assert: the ErrURLRequired error is returned.
	require.ErrorIsf(t, err, engineio.ErrURLRequired, "transport should return an error")
	require.Nilf(t, transport, "transport should be nil")
}

func TestWebSocketTransport_SetURL(t *testing.T) {
	t.Parallel()

	// Arrange: parse the initial target url.
	initial, err := url.Parse("http://a/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: parse a new target url.
	next, err := url.Parse("http://bbbbbbbbbbbbb/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET.
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, "http://bbbbbbbbbbbbb/engine.io/?EIO=4&transport=websocket", req.URL.String())
			return nil, errors.New("mock error")
		}).
		MinTimes(1).
		MaxTimes(2)

	// Act: create a new polling transport.
	transport, err := engineio.NewWebSocketTransport(initial, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Act: set the new url.
	transport.SetURL(next)

	// Act: open the transport.
	transport.Open(context.Background())

	// Act: pause the transport to prevent further polling.
	transport.Close(context.Background())
}

func TestWebSocketTransport_Open_SetsStateToOpening(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a channel to signal when the transport is opened.
	onOpeningChan := make(chan struct{}, 1)

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(*http.Request) (*http.Response, error) {
			onOpeningChan <- struct{}{}
			<-time.After(1 * time.Second)
			return nil, errors.New("mock error")
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Act: open the transport.
	go transport.Open(context.Background())

	// Act: wait for the transport to begin opening.
	<-onOpeningChan

	// Assert: the transport state is opening.
	require.Equal(t, engineio.TransportStateOpening, transport.State())
}

// mockReadWriteCloser is a mock implementation of io.ReadWriteCloser
type mockReadWriteCloser struct {
	buffer *bytes.Buffer
	closed bool
}

// Read reads from the buffer.
func (m *mockReadWriteCloser) Read(p []byte) (n int, err error) {
	if m.closed {
		return 0, io.EOF
	}
	return m.buffer.Read(p)
}

// Write writes to the buffer.
func (m *mockReadWriteCloser) Write(p []byte) (n int, err error) {
	if m.closed {
		return 0, io.EOF
	}
	return m.buffer.Write(p)
}

// Close marks the ReadWriteCloser as closed
func (m *mockReadWriteCloser) Close() error {
	m.closed = true
	return nil
}

func TestWebSocketTransport_Open_CallsOnOpenHandler(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET.
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=websocket", req.URL.String())

			// Arrange: marshal open packet data.
			data, err := json.Marshal(engineio.OpenPacket{
				SessionID: "252937f5-aff9-4885-91ca-234802cede79",
			})
			require.NoError(t, err)

			// Arrange: encode the open packet.
			packet := engineio.EncodePacket(engineio.Packet{
				Type: engineio.PacketOpen,
				Data: data,
			})

			// Act: return a new response with the open packet.
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

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Act: create a new websocket transport with the mock transport client.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport encounters an error.
	onErrChan := make(chan error, 1)
	transport.OnError(func(ctx context.Context, err error) {
		onErrChan <- err
	})

	// Arrange: create a new channel to signal when the transport is opened.
	onOpenChan := make(chan struct{}, 1)
	transport.OnOpen(func(ctx context.Context) {
		// Assert: the transport state is open.
		require.Equal(t, engineio.TransportStateOpen, transport.State())

		// Signal that the transport is opened.
		onOpenChan <- struct{}{}
	})

	// Act: open the transport.
	go transport.Open(context.Background())

	// Arrange: close the transport.
	t.Cleanup(func() {
		transport.Close(context.Background())
	})

	// Wait for the transport to be opened.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to open")

	case err := <-onErrChan:
		t.Fatalf("unexpected error: %v", err)

	case <-onOpenChan:
	}
}

func TestWebSocketTransport_Open_CallsOnErrorHandler(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET.
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=websocket", req.URL.String())

			// Act: return a new response with the open packet.
			return nil, errors.New("mock error")
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "mock error", "transport should return an error")

		// Signal that the transport is opened.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	go transport.Open(context.Background())

	// Arrange: close the transport.
	t.Cleanup(func() {
		transport.Close(context.Background())
	})

	// Wait for the transport to encounter an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithBadURL(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Times(0)

		// Arrange: create an invalid URL.
	u := &url.URL{Scheme: ":"}
	_, err := url.Parse(u.String())
	require.Errorf(t, err, "url should be invalid: '%s'", u.String())

	// Arrange: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "failed to parse url", "transport should return an error")

		// Signal that the transport is opened.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	go transport.Open(context.Background())

	// Arrange: close the transport.
	t.Cleanup(func() {
		transport.Close(context.Background())
	})

	// Wait for the transport to encounter an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithBadContext(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Times(0)

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "net/http: nil Context", "transport should return an error")

		// Signal that the transport is opened.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	go transport.Open(nil)

	// Arrange: close the transport.
	t.Cleanup(func() {
		transport.Close(context.Background())
	})

	// Wait for the transport to encounter an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithBadStatusCode(t *testing.T) {
	t.Parallel()

	var statusCodes = []int{
		http.StatusNotFound,
		http.StatusBadGateway,
	}
	for _, statusCode := range statusCodes {
		statusCode := statusCode
		t.Run(fmt.Sprintf("status code %d", statusCode), func(t *testing.T) {
			t.Parallel()

			// Arrange: create a new mock controller.
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Arrange: create a new mock transport client.
			mockTransportClient := mocks.NewMockTransportClient(ctrl)
			mockTransportClient.EXPECT().
				Do(gomock.Any()).
				DoAndReturn(func(req *http.Request) (*http.Response, error) {
					// Act: return a new response with a bad status code.
					return &http.Response{
						StatusCode: statusCode,
					}, nil
				}).
				AnyTimes()

			// Arrange: parse the target url.
			u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
			require.NoError(t, err)

			// Arrange: create a new websocket transport.
			transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
				Client: mockTransportClient,
			})
			require.NoErrorf(t, err, "transport should not return an error")

			// Arrange: create a new channel to signal when the transport is opened.
			onErrorChan := make(chan struct{}, 1)
			transport.OnError(func(ctx context.Context, err error) {
				// Assert: the error is not nil.
				require.ErrorContainsf(t, err, "expected handshake response status code 101", "transport should return an error")

				// Signal that the transport is opened.
				onErrorChan <- struct{}{}
			})

			// Act: open the transport.
			go transport.Open(context.Background())

			// Arrange: close the transport.
			t.Cleanup(func() {
				transport.Close(context.Background())
			})

			// Wait for the transport to encounter an error.
			select {
			case <-time.After(100 * time.Millisecond):
				t.Fatal("timeout waiting for transport to error")

			case <-onErrorChan:
			}
		})
	}
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithNilResponseBody(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Act: return a new response with a bad status code.
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

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "response body is not a io.ReadWriteCloser", "transport should return an error")

		// Signal that the transport is opened.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	go transport.Open(context.Background())

	// Arrange: close the transport.
	t.Cleanup(func() {
		transport.Close(context.Background())
	})

	// Wait for the transport to encounter an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithBadResponseBody(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Act: return a new response with a bad status code.
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

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "failed to read frame header", "transport should return an error")

		// Signal that the transport is opened.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	go transport.Open(context.Background())

	// Arrange: close the transport.
	t.Cleanup(func() {
		transport.Close(context.Background())
	})

	// Wait for the transport to encounter an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithoutConnectionHeader(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Act: return a new response with a bad status code.
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

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "WebSocket protocol violation: Connection header", "transport should return an error")

		// Signal that the transport is opened.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	go transport.Open(context.Background())

	// Arrange: close the transport.
	t.Cleanup(func() {
		transport.Close(context.Background())
	})

	// Wait for the transport to encounter an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithoutUpgradeHeader(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Act: return a new response with a bad status code.
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

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "WebSocket protocol violation: Upgrade header", "transport should return an error")

		// Signal that the transport is opened.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	go transport.Open(context.Background())

	// Arrange: close the transport.
	t.Cleanup(func() {
		transport.Close(context.Background())
	})

	// Wait for the transport to encounter an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestWebSocketTransport_Open_CallsOnErrorHandler_WithBadSecWebSocketAcceptHeader(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Act: return a new response with a bad status code.
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

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=websocket")
	require.NoError(t, err)

	// Arrange: create a new websocket transport.
	transport, err := engineio.NewWebSocketTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "WebSocket protocol violation: invalid Sec-WebSocket-Accept", "transport should return an error")

		// Signal that the transport is opened.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	go transport.Open(context.Background())

	// Arrange: close the transport.
	t.Cleanup(func() {
		transport.Close(context.Background())
	})

	// Wait for the transport to encounter an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestWebSocketTransport_CallsOnPacketHandler(t *testing.T) {

}

func TestWebSocketTransport_Send_WritesPacket_WithOpenState(t *testing.T) {

}

func TestWebSocketTransport_Send_IgnoresPacket_WithClosedState(t *testing.T) {

}

func TestWebSocketTransport_Close_SetsStateToClosing(t *testing.T) {

}

func TestWebSocketTransport_Close_SendsClosePacket(t *testing.T) {

}

func TestWebSocketTransport_Close_CallsOnCloseHandler(t *testing.T) {

}

func TestWebSocketTransport_Close_CallsOnPacketHandler(t *testing.T) {

}
