package engineio_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestPollingTransport_Polls(t *testing.T) {
	t.Parallel()

	// Arrange: create a variable to track whether the "server" has accepted the connection
	opened := atomic.Bool{}

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request path is correct. GET long-polls carry a
			// cache-busting timestamp; POST writes are not timestamped.
			if req.Method == http.MethodGet {
				require.True(t, strings.HasPrefix(req.URL.String(), "http://localhost/engine.io/?EIO=4&transport=polling"))
				require.NotEmpty(t, req.URL.Query().Get("t"))
			} else {
				require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())
			}

			switch {
			case req.Method == http.MethodGet && !opened.Load():
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
				opened.Store(true)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			case req.Method == http.MethodGet && opened.Load():
				// Encode the message packet
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketMessage,
					Data: []byte("hello"),
				})

				// Respond with a message packet
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			default:
				return nil, errors.New("mock error")
			}
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client
	transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport is opened
	onOpenChan := make(chan struct{}, 1)
	transport.OnOpen(func(ctx context.Context) {
		onOpenChan <- struct{}{}
	})

	// Act: open the transport
	transport.Open(t.Context())

	// Wait for the transport to be opened.
	<-onOpenChan

	// Arrange: create a new channel to signal when the transport receives a packet
	onPacketChan := make(chan struct{}, 4)
	transport.OnPacket(func(ctx context.Context, p engineio.Packet) {
		if p.Type == engineio.PacketOpen {
			return
		}

		// Assert: the packet is a message packet
		require.Equal(t, engineio.PacketMessage, p.Type)
		require.Equal(t, "hello", string(p.Data))

		// Signal that the transport has received a packet. The send is non-blocking
		// because the mock answers every poll, so a dispatch that Pause waits for
		// must not block on a full channel.
		select {
		case onPacketChan <- struct{}{}:

		default:
		}
	})

	// Wait for the transport to read message packets.
	for range 3 {
		<-onPacketChan
	}

	// Act: pause the transport to prevent further polling
	transport.Pause(t.Context())
}

func TestPollingTransport_Poll_CarriesDistinctCacheBustingTimestamps(t *testing.T) {
	t.Parallel()

	// Arrange: create a variable to track whether the "server" has accepted the connection
	opened := atomic.Bool{}

	// Arrange: capture the cache-busting timestamp of each GET poll. The channel is
	// buffered so the mock never blocks while answering successive polls.
	timestamps := make(chan string, 8)

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET
			require.Equal(t, http.MethodGet, req.Method)

			// Assert: the GET targets the base url and carries a cache-busting timestamp
			require.True(t, strings.HasPrefix(req.URL.String(), "http://localhost/engine.io/?EIO=4&transport=polling"))

			// Record the timestamp of this poll, non-blocking once the buffer is full
			ts := req.URL.Query().Get("t")
			require.NotEmpty(t, ts)
			select {
			case timestamps <- ts:

			default:
			}

			switch {
			case !opened.Load():
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
				opened.Store(true)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil

			default:
				// Encode the message packet
				packet := engineio.EncodePacket(engineio.Packet{
					Type: engineio.PacketMessage,
					Data: []byte("hello"),
				})

				// Respond with a message packet
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(packet)),
				}, nil
			}
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client
	transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Act: open the transport, which issues the first poll and then continues polling
	transport.Open(t.Context())

	// Assert: the first two polls carry non-empty, distinct cache-busting timestamps
	first := <-timestamps
	second := <-timestamps
	require.NotEmpty(t, first)
	require.NotEmpty(t, second)
	require.NotEqual(t, first, second)

	// Act: pause the transport to prevent further polling
	transport.Pause(t.Context())
}

func TestPollingTransport_Poll_CallsOnErrorHandler_WithBadURL(t *testing.T) {
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

	// Act: create a new polling transport with the mock transport client
	transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport receives an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "missing protocol scheme")

		// Signal that the transport has received an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport
	transport.Open(t.Context())

	// Wait for the transport to receive an error.
	<-onErrorChan
}

func TestPollingTransport_Poll_CallsOnErrorHandler_WithBadContext(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Times(0)

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client
	transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport receives an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "net/http: nil Context")

		// Signal that the transport has received an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport with a nil context to exercise the error path
	transport.Open(nil)

	// Wait for the transport to receive an error.
	<-onErrorChan
}

func TestPollingTransport_Poll_CallsOnErrorHandler_WithBadStatusCode(t *testing.T) {
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
						Body:       io.NopCloser(bytes.NewReader([]byte{})),
					}, nil
				}).
				AnyTimes()

			// Arrange: parse the target url
			u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
			require.NoError(t, err)

			// Act: create a new polling transport with the mock transport client
			transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
			require.NoError(t, err)

			// Arrange: create a new channel to signal when the transport receives an error
			onErrorChan := make(chan struct{}, 1)
			transport.OnError(func(ctx context.Context, err error) {
				// Assert: the error is not nil
				require.ErrorContains(t, err, "polling")

				// Signal that the transport has received an error.
				onErrorChan <- struct{}{}
			})

			// Act: open the transport
			transport.Open(t.Context())

			// Wait for the transport to receive an error.
			<-onErrorChan
		})
	}
}

func TestPollingTransport_Poll_CallsOnErrorHandler_WithNilResponseBody(t *testing.T) {
	t.Parallel()

	// Assert: decoding the malformed base64 string should cause an error
	_, err := base64.StdEncoding.DecodeString("aa")
	require.Error(t, err)

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Respond with a bad status code
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &badReadWriteCloser{},
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client
	transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport receives an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "reading poll response")

		// Signal that the transport has received an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport
	transport.Open(t.Context())

	// Wait for the transport to receive an error.
	<-onErrorChan
}

func TestPollingTransport_Poll_CallsOnErrorHandler_WithBadResponseBody(t *testing.T) {
	t.Parallel()

	// Assert: decoding the malformed base64 string should cause an error
	_, err := base64.StdEncoding.DecodeString("aa")
	require.Error(t, err)

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Respond with a bad status code
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte("baa"))),
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client
	transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport receives an error
	onErrorChan := make(chan struct{}, 1)
	transport.OnError(func(ctx context.Context, err error) {
		// Assert: the error is not nil
		require.ErrorContains(t, err, "illegal base64 data")

		// Signal that the transport has received an error.
		onErrorChan <- struct{}{}
	})

	// Act: open the transport
	transport.Open(t.Context())

	// Wait for the transport to receive an error.
	<-onErrorChan
}
