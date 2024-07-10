package engineio_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/lewisgibson/go-engine.io/internal/mocks"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewPollingTransport(t *testing.T) {
	t.Parallel()

	t.Run("open sends a poll", func(t *testing.T) {
		t.Parallel()

		// Arrange: setup a requests counter
		calls := 0

		// Arrange: create a fake transport client
		ctrl := gomock.NewController(t)
		mockTransportClient := mocks.NewMockTransportClient(ctrl)
		mockTransportClient.EXPECT().
			Do(gomock.Any()).
			DoAndReturn(func(req *http.Request) (*http.Response, error) {
				if calls == 0 {
					// Assert: check the method and url of the request
					require.Equal(t, "GET", req.Method)
					require.Equal(t, "http://localhost/engine.io/?EIO=4&transport=polling", req.URL.String())
					require.Equal(t, "Bearer token", req.Header.Get("Authorization"))
					calls++
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte{engineio.PacketOpen.Byte()}))}, nil
			}).
			Times(2) // one for open and one for close

		// Arrange: create a mock url
		u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
		require.NoError(t, err)

		// Arrange: set up a context
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Arrange: create a new polling transport
		transport := engineio.NewPollingTransport(u, engineio.TransportOptions{
			Client: mockTransportClient,
			Header: http.Header{
				"Authorization": []string{"Bearer token"},
			},
		})
		require.NotNil(t, transport)

		// Act: open the transport
		transport.Open(ctx)
		defer transport.Close(ctx)

		// Assert: one poll request was made
		require.Equal(t, 1, calls)
	})
}
