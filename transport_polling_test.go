package engineio_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/lewisgibson/go-engine.io/internal/mocks"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewPollingTransport(t *testing.T) {
	t.Parallel()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: http.DefaultClient,
		Header: http.Header{},
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Assert: the transport is a polling transport with the correct state.
	require.Equal(t, engineio.TransportTypePolling, transport.Type())
	require.Equal(t, engineio.TransportStateClosed, transport.State())
}

func TestNewPollingTransport_NilUrl(t *testing.T) {
	t.Parallel()

	// Act: create a new polling transport.
	transport, err := engineio.NewPollingTransport(nil, engineio.TransportOptions{
		Client: http.DefaultClient,
		Header: http.Header{},
	})

	// Assert: the ErrURLRequired error is returned.
	require.ErrorIsf(t, err, engineio.ErrURLRequired, "transport should return an error")
	require.Nilf(t, transport, "transport should be nil")
}

func TestPollingTransport_SetURL(t *testing.T) {
	t.Parallel()

	// Arrange: parse the initial target url.
	initial, err := url.Parse("http://a/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Arrange: parse a new target url.
	next, err := url.Parse("http://bbbbbbbbbbbbb/engine.io/?EIO=4&transport=polling")
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
			require.Equal(t, "http://bbbbbbbbbbbbb/engine.io/?EIO=4&transport=polling", req.URL.String())
			return nil, errors.New("mock error")
		}).
		MinTimes(1).
		MaxTimes(2)

	// Act: create a new polling transport.
	transport, err := engineio.NewPollingTransport(initial, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Act: set the new url.
	transport.SetURL(next)

	// Act: open the transport.
	transport.Open(context.Background())

	// Act: pause the transport to prevent further polling.
	transport.Pause(context.Background())
}

func TestPollingTransport_Open_SetsStateToOpening(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Return(nil, errors.New("mock error")).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Act: open the transport.
	transport.Open(context.Background())

	// Assert: the transport state is opening.
	require.Equal(t, engineio.TransportStateOpening, transport.State())
}

func TestPollingTransport_Open_SendsOpenPacket(t *testing.T) {
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
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())
			return nil, errors.New("mock error")
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Act: open the transport.
	transport.Open(context.Background())

	// Assert: the transport state is opening.
	require.Equal(t, engineio.TransportStateOpening, transport.State())

	// Act: pause the transport to prevent further polling.
	transport.Pause(context.Background())
}

func TestPollingTransport_Open_CallsOnErrorHandler(t *testing.T) {
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
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())

			// Act: return a new response with the open packet.
			return nil, errors.New("mock error")
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
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
	transport.Open(context.Background())

	// Act: pause the transport to prevent further polling.
	transport.Pause(context.Background())

	// Wait for the transport to be opened.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestPollingTransport_Open_CallsOnOpenHandler(t *testing.T) {
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
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())

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
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(packet)),
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onOpenChan := make(chan struct{}, 1)
	transport.OnOpen(func(ctx context.Context) {
		// Assert: the transport state is open.
		require.Equal(t, engineio.TransportStateOpen, transport.State())

		// Signal that the transport is opened.
		onOpenChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Act: pause the transport to prevent further polling.
	transport.Pause(context.Background())

	// Wait for the transport to be opened.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to open")

	case <-onOpenChan:
	}
}

func TestPollingTransport_CallsOnPacketHandler(t *testing.T) {
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
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())

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
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(packet)),
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport receives a packet.
	onPacketChan := make(chan struct{}, 1)
	transport.OnPacket(func(ctx context.Context, pkt engineio.Packet) {
		// Assert: the packet is an open packet.
		require.Equal(t, engineio.PacketOpen, pkt.Type)

		// Signal that the transport is opened.
		onPacketChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Act: pause the transport to prevent further polling.
	transport.Pause(context.Background())

	// Wait for the transport to receive a packet.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to open")

	case <-onPacketChan:
	}
}

func TestPollingTransport_Polls(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a variable to track whether the "server" has accepted the connection.
	opened := atomic.Bool{}

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request path is correct.
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())

			switch {
			case req.Method == http.MethodGet && !opened.Load():
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
				opened.Store(true)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodGet && opened.Load():
				// Arrange: encode the message packet.
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketMessage,
					Data: []byte("hello"),
				})

				// Act: return a new response with a message packet.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			default:
				return nil, errors.New("mock error")
			}
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onOpenChan := make(chan struct{}, 1)
	transport.OnOpen(func(ctx context.Context) {
		onOpenChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Wait for the transport to be opened.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to open")

	case <-onOpenChan:
	}

	// Arrange: create a new channel to signal when the transport receives a packet.
	onPacketChan := make(chan struct{}, 4)
	transport.OnPacket(func(ctx context.Context, p engineio.Packet) {
		if p.Type == engineio.PacketOpen {
			return
		}

		// Assert: the packet is a message packet.
		require.Equal(t, engineio.PacketMessage, p.Type)
		require.Equal(t, "hello", string(p.Data))

		// Signal that the transport has received a packet.
		onPacketChan <- struct{}{}
	})

	// Wait for the transport to read message packets.
	var timeoutAt = time.After(100 * time.Millisecond)
	for i := 0; i < 3; i++ {
		select {
		case <-timeoutAt:
			t.Fatal("timeout waiting for transport to read packet")

		case <-onPacketChan:
			t.Log("packet received")
		}
	}

	// Act: pause the transport to prevent further polling.
	transport.Pause(context.Background())
}

func TestPollingTransport_Poll_CallsOnErrorHandler_WithBadURL(t *testing.T) {
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

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport receives an error.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "missing protocol scheme", "transport should return an error")

		// Signal that the transport has received an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Wait for the transport to receive an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestPollingTransport_Poll_CallsOnErrorHandler_WithBadContext(t *testing.T) {
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
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport receives an error.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "net/http: nil Context", "transport should return an error")

		// Signal that the transport has received an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(nil)

	// Wait for the transport to receive an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestPollingTransport_Poll_CallsOnErrorHandler_WithBadStatusCode(t *testing.T) {
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
						Body:       io.NopCloser(bytes.NewReader([]byte{})),
					}, nil
				}).
				AnyTimes()

			// Arrange: parse the target url.
			u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
			require.NoError(t, err)

			// Act: create a new polling transport with the mock transport client.
			transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
				Client: mockTransportClient,
			})
			require.NoErrorf(t, err, "transport should not return an error")

			// Arrange: create a new channel to signal when the transport receives an error.
			onErrorChan := make(chan struct{}, 1)
			transport.OnError(func(ctx context.Context, err error) {
				// Assert: the error is not nil.
				require.ErrorContainsf(t, err, "polling error", "transport should return an error")

				// Signal that the transport has received an error.
				onErrorChan <- struct{}{}
			})

			// Act: open the transport.
			transport.Open(context.Background())

			// Wait for the transport to receive an error.
			select {
			case <-time.After(100 * time.Millisecond):
				t.Fatal("timeout waiting for transport to error")

			case <-onErrorChan:
			}
		})
	}
}

// badReadWriteCloser is a mock io.ReadWriteCloser that returns an error when read or write is called.
type badReadWriteCloser struct{}

// Read implements the io.Reader interface.
func (r *badReadWriteCloser) Read(p []byte) (n int, err error) {
	return 0, errors.New("mock error")
}

// Write implements the io.Writer interface.
func (r *badReadWriteCloser) Write(p []byte) (n int, err error) {
	return 0, errors.New("mock error")
}

// Close implements the io.Closer interface.
func (r *badReadWriteCloser) Close() error {
	return nil
}

func TestPollingTransport_CallsOnErrorHandler_WithNilResponseBody(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Assert: decoding the malformed base64 string should cause an error
	_, err := base64.StdEncoding.DecodeString("aa")
	require.Error(t, err)

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Act: return a new response with a bad status code.
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &badReadWriteCloser{},
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport receives an error.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "read error", "transport should return an error")

		// Signal that the transport has received an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Wait for the transport to receive an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestPollingTransport_CallsOnErrorHandler_WithBadResponseBody(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Assert: decoding the malformed base64 string should cause an error
	_, err := base64.StdEncoding.DecodeString("aa")
	require.Error(t, err)

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Act: return a new response with a bad status code.
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte("baa"))),
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport receives an error.
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil.
		require.ErrorContainsf(t, err, "illegal base64 data", "transport should return an error")

		// Signal that the transport has received an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Wait for the transport to receive an error.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to error")

	case <-onErrorChan:
	}
}

func TestPollingTransport_Pause_SetsStateToPaused(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a variable to track whether the "server" has accepted the connection.
	opened := atomic.Bool{}

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request path is correct.
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())

			switch {
			case req.Method == http.MethodGet && !opened.Load():
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
				opened.Store(true)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodGet && opened.Load():
				// Arrange: encode the message packet.
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketMessage,
					Data: []byte("hello"),
				})

				// Act: return a new response with a message packet.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			default:
				return nil, errors.New("mock error")
			}
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onOpenChan := make(chan struct{}, 1)
	transport.OnOpen(func(ctx context.Context) {
		onOpenChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Wait for the transport to be opened.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to open")

	case <-onOpenChan:
	}

	// Act: pause the transport to prevent further polling.
	transport.Pause(context.Background())

	// Assert: the transport state is paused.
	require.Equal(t, engineio.TransportStatePaused, transport.State())
}

func TestPollingTransport_Send_WritesPacket_WithOpenState(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a variable to track whether the "server" has accepted the connection.
	opened := atomic.Bool{}

	// Arrange: create a new channel to signal when the transport receives a packet.
	onPacketReceived := make(chan struct{}, 1)

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request path is correct.
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())

			switch {
			case req.Method == http.MethodGet && !opened.Load():
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
				opened.Store(true)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodGet && opened.Load():
				// Arrange: encode the message packet.
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketMessage,
					Data: []byte("hello"),
				})

				// Act: return a new response with a message packet.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodPost:
				// Arrange: read the request body.
				defer req.Body.Close()
				b, err := io.ReadAll(req.Body)
				require.NoError(t, err)

				// Assert: the payload contains one packet.
				packets, err := engineio.DecodePayload(b)
				require.NoError(t, err)
				require.Lenf(t, packets, 1, "payload should contain one packet")

				// Assert: the packet is a message packet.
				require.Equal(t, engineio.PacketMessage, packets[0].Type)
				require.Equal(t, "world", string(packets[0].Data))

				// Act: signal that the packet was received.
				onPacketReceived <- struct{}{}

				// Act: return a new response with a message packet.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte{})),
				}, nil

			default:
				return nil, errors.New("mock error")
			}
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onOpenChan := make(chan struct{}, 1)
	transport.OnOpen(func(ctx context.Context) {
		onOpenChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Wait for the transport to be opened.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to open")

	case <-onOpenChan:
	}

	// Act: send a message packet.
	require.NoErrorf(t, transport.Send(context.Background(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("world")},
	}), "transport should not return an error")

	// Act: pause the transport to prevent further polling.
	transport.Pause(context.Background())

	// Wait for the transport to receive the written message packet.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to receive written packet")

	case <-onPacketReceived:
	}
}

func TestPollingTransport_Send_IgnoresPacket_WithClosedState(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Times(0) // Assert: this should never be called

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Act: send a message packet.
	require.NoErrorf(t, transport.Send(context.Background(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("world")},
	}), "transport should not return an error")

	// Act: pause the transport to prevent further polling.
	transport.Pause(context.Background())
}

func TestPollingTransport_Close_SetsStateToClosing(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Return(nil, errors.New("mock error")).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Act: open the transport.
	transport.Open(context.Background())

	// Assert: the transport state is opening.
	require.Equal(t, engineio.TransportStateOpening, transport.State())

	// Act: open the transport.
	transport.Close(context.Background())

	// Assert: the transport state is closing.
	require.Equal(t, engineio.TransportStateClosing, transport.State())
}

func TestPollingTransport_Close_SendsClosePacket(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a variable to track whether the "server" has accepted the connection.
	opened := atomic.Bool{}

	// Arrange: create a variable to track whether the "server" wants to close the connection.
	closing := atomic.Bool{}

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request path is correct.
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())

			switch {
			case req.Method == http.MethodGet && !opened.Load():
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
				opened.Store(true)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodGet && opened.Load() && closing.Load():
				// Arrange: encode the close packet.
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketClose,
				})

				// Act: return a new response with a close packet.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodGet && opened.Load():
				// Arrange: encode the message packet.
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketMessage,
					Data: []byte("hello"),
				})

				// Act: return a new response with a message packet.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodPost:
				// Arrange: read the request body.
				defer req.Body.Close()
				b, err := io.ReadAll(req.Body)
				require.NoError(t, err)

				// Assert: the payload contains one packet.
				packets, err := engineio.DecodePayload(b)
				require.NoError(t, err)
				require.Lenf(t, packets, 1, "payload should contain one packet")

				// Assert: the packet is a close packet.
				require.Equal(t, engineio.PacketClose, packets[0].Type)

				// Act: signal that the server wants to close the connection.
				closing.Store(true)

				// Act: return a new empty response.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte{})),
				}, nil

			default:
				return nil, errors.New("mock error")
			}
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onOpenChan := make(chan struct{}, 1)
	transport.OnOpen(func(ctx context.Context) {
		onOpenChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Assert: wait for the transport to be opened.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to open")

	case <-onOpenChan:
	}

	// Act: close the transport.
	transport.Close(context.Background())
}

func TestPollingTransport_Close_CallsOnCloseHandler(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a variable to track whether the "server" has accepted the connection.
	opened := atomic.Bool{}

	// Arrange: create a variable to track whether the "server" wants to close the connection.
	closing := atomic.Bool{}

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request path is correct.
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())

			switch {
			case req.Method == http.MethodGet && !opened.Load():
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
				opened.Store(true)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodGet && opened.Load() && closing.Load():
				// Arrange: encode the close packet.
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketClose,
				})

				// Act: return a new response with a close packet.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodGet && opened.Load():
				// Arrange: encode the message packet.
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketMessage,
					Data: []byte("hello"),
				})

				// Act: return a new response with a message packet.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodPost:
				// Arrange: read the request body.
				defer req.Body.Close()
				b, err := io.ReadAll(req.Body)
				require.NoError(t, err)

				// Assert: the payload contains one packet.
				packets, err := engineio.DecodePayload(b)
				require.NoError(t, err)
				require.Lenf(t, packets, 1, "payload should contain one packet")

				// Assert: the packet is a close packet.
				require.Equal(t, engineio.PacketClose, packets[0].Type)

				// Act: signal that the server wants to close the connection.
				closing.Store(true)

				// Act: return a new empty response.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte{})),
				}, nil

			default:
				return nil, errors.New("mock error")
			}
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onOpenChan := make(chan struct{}, 1)
	transport.OnOpen(func(ctx context.Context) {
		onOpenChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Assert: wait for the transport to be opened.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to open")

	case <-onOpenChan:
	}

	// Arrange: create a new channel to signal when the transport is closed.
	onCloseChan := make(chan struct{}, 1)
	transport.OnClose(func(ctx context.Context) {
		onCloseChan <- struct{}{}
	})

	// Act: close the transport.
	transport.Close(context.Background())

	// Assert: wait for the transport to be closed.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to close")

	case <-onCloseChan:
	}
}

func TestPollingTransport_Close_CallsOnPacketHandler(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a variable to track whether the "server" has accepted the connection.
	opened := atomic.Bool{}

	// Arrange: create a variable to track whether the "server" wants to close the connection.
	closing := atomic.Bool{}

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request path is correct.
			require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())

			switch {
			case req.Method == http.MethodGet && !opened.Load():
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
				opened.Store(true)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodGet && opened.Load() && closing.Load():
				// Arrange: encode the close packet.
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketClose,
				})

				// Act: return a new response with a close packet.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodGet && opened.Load():
				// Arrange: encode the message packet.
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketMessage,
					Data: []byte("hello"),
				})

				// Act: return a new response with a message packet.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodPost:
				// Arrange: read the request body.
				defer req.Body.Close()
				b, err := io.ReadAll(req.Body)
				require.NoError(t, err)

				// Assert: the payload contains one packet.
				packets, err := engineio.DecodePayload(b)
				require.NoError(t, err)
				require.Lenf(t, packets, 1, "payload should contain one packet")

				// Assert: the packet is a close packet.
				require.Equal(t, engineio.PacketClose, packets[0].Type)

				// Act: signal that the server wants to close the connection.
				closing.Store(true)

				// Act: return a new empty response.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte{})),
				}, nil

			default:
				return nil, errors.New("mock error")
			}
		}).
		AnyTimes()

	// Arrange: parse the target url.
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client.
	transport, err := engineio.NewPollingTransport(u, engineio.TransportOptions{
		Client: mockTransportClient,
	})
	require.NoErrorf(t, err, "transport should not return an error")

	// Arrange: create a new channel to signal when the transport is opened.
	onOpenChan := make(chan struct{}, 1)
	transport.OnOpen(func(ctx context.Context) {
		onOpenChan <- struct{}{}
	})

	// Act: open the transport.
	transport.Open(context.Background())

	// Assert: wait for the transport to be opened.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to open")

	case <-onOpenChan:
	}

	// Arrange: create a new channel to signal when the transport receives a packet.
	onPacketChan := make(chan struct{}, 1)
	transport.OnPacket(func(ctx context.Context, p engineio.Packet) {
		onPacketChan <- struct{}{}
	})

	// Act: close the transport.
	transport.Close(context.Background())

	// Assert: wait for the transport to be closed.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for transport to receive a packet")

	case <-onPacketChan:
	}
}
