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
	"sync/atomic"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestPollingTransport_Pause_SetsStateToPaused(t *testing.T) {
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

	// Act: pause the transport to prevent further polling
	transport.Pause(t.Context())

	// Assert: the transport state is paused
	require.Equal(t, engineio.TransportStatePaused, transport.State())
}
