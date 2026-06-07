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

func TestPollingTransport_SetURL(t *testing.T) {
	t.Parallel()

	// Arrange: parse the initial target url
	initial, err := url.Parse("http://a/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Arrange: parse a new target url
	next, err := url.Parse("http://bbbbbbbbbbbbb/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET
			require.Equal(t, http.MethodGet, req.Method)

			// Assert: the GET targets the new url and carries a cache-busting timestamp
			require.True(t, strings.HasPrefix(req.URL.String(), "http://bbbbbbbbbbbbb/engine.io/?EIO=4&transport=polling"))
			require.NotEmpty(t, req.URL.Query().Get("t"))
			return nil, errors.New("mock error")
		}).
		MinTimes(1).
		MaxTimes(2)

	// Act: create a new polling transport
	transport, err := engineio.NewPollingTransport(initial, mockTransportClient, nil)
	require.NoError(t, err)

	// Act: set the new url
	transport.SetURL(next)

	// Act: open the transport
	transport.Open(t.Context())

	// Act: pause the transport to prevent further polling
	transport.Pause(t.Context())
}

func TestPollingTransport_Open_SetsStateToOpening(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Return(nil, errors.New("mock error")).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client
	transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Act: open the transport
	transport.Open(t.Context())

	// Assert: the transport state is opening
	require.Equal(t, engineio.TransportStateOpening, transport.State())
}

func TestPollingTransport_Open_SendsOpenPacket(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET
			require.Equal(t, http.MethodGet, req.Method)

			// Assert: the GET targets the base url and carries a cache-busting timestamp
			require.True(t, strings.HasPrefix(req.URL.String(), "http://localhost/engine.io/?EIO=4&transport=polling"))
			require.NotEmpty(t, req.URL.Query().Get("t"))
			return nil, errors.New("mock error")
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client
	transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Act: open the transport
	transport.Open(t.Context())

	// Assert: the transport state is opening
	require.Equal(t, engineio.TransportStateOpening, transport.State())

	// Act: pause the transport to prevent further polling
	transport.Pause(t.Context())
}

func TestPollingTransport_Open_CallsOnErrorHandler(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET
			require.Equal(t, http.MethodGet, req.Method)

			// Assert: the GET targets the base url and carries a cache-busting timestamp
			require.True(t, strings.HasPrefix(req.URL.String(), "http://localhost/engine.io/?EIO=4&transport=polling"))
			require.NotEmpty(t, req.URL.Query().Get("t"))

			// Respond with the open packet
			return nil, errors.New("mock error")
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client
	transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
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
	transport.Open(t.Context())

	// Act: pause the transport to prevent further polling
	transport.Pause(t.Context())

	// Wait for the transport to be opened.
	<-onErrorChan
}

func TestPollingTransport_Open_CallsOnOpenHandler(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET
			require.Equal(t, http.MethodGet, req.Method)

			// Assert: the GET targets the base url and carries a cache-busting timestamp
			require.True(t, strings.HasPrefix(req.URL.String(), "http://localhost/engine.io/?EIO=4&transport=polling"))
			require.NotEmpty(t, req.URL.Query().Get("t"))

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
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(packet)),
			}, nil
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
		// Assert: the transport state is open
		require.Equal(t, engineio.TransportStateOpen, transport.State())

		// Signal that the transport is opened.
		onOpenChan <- struct{}{}
	})

	// Act: open the transport
	transport.Open(t.Context())

	// Act: pause the transport to prevent further polling
	transport.Pause(t.Context())

	// Wait for the transport to be opened.
	<-onOpenChan
}

func TestPollingTransport_CallsOnPacketHandler(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			// Assert: the request method is GET
			require.Equal(t, http.MethodGet, req.Method)

			// Assert: the GET targets the base url and carries a cache-busting timestamp
			require.True(t, strings.HasPrefix(req.URL.String(), "http://localhost/engine.io/?EIO=4&transport=polling"))
			require.NotEmpty(t, req.URL.Query().Get("t"))

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
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(packet)),
			}, nil
		}).
		AnyTimes()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport with the mock transport client
	transport, err := engineio.NewPollingTransport(u, mockTransportClient, nil)
	require.NoError(t, err)

	// Arrange: create a new channel to signal when the transport receives a packet
	// The send is non-blocking: the mock answers every poll with an open packet, so
	// a dispatch that Pause waits for must not block on a full channel.
	onPacketChan := make(chan struct{}, 1)
	transport.OnPacket(func(ctx context.Context, pkt engineio.Packet) {
		// Assert: the packet is an open packet
		require.Equal(t, engineio.PacketOpen, pkt.Type)

		// Signal that the transport received a packet.
		select {
		case onPacketChan <- struct{}{}:

		default:
		}
	})

	// Act: open the transport
	transport.Open(t.Context())

	// Act: pause the transport to prevent further polling
	transport.Pause(t.Context())

	// Wait for the transport to receive a packet.
	<-onPacketChan
}
