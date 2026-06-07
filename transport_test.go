package engineio_test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestTransportType_String(t *testing.T) {
	t.Parallel()

	// Assert: the string representation of the transport type should never be empty
	require.NotEmpty(t, engineio.TransportTypePolling.String())
}

func TestTransportState_String(t *testing.T) {
	t.Parallel()

	// Assert: the string representation of the transport state should never be empty
	require.NotEmpty(t, engineio.TransportStateOpen.String())
}

func TestTransportRoundTripper_RoundTrip_NilTransportClient(t *testing.T) {
	t.Parallel()

	// Arrange: create a new transport round tripper
	transport := engineio.TransportRoundTripper{}

	// Act: round trip a request
	_, err := transport.RoundTrip(&http.Request{})
	require.ErrorIs(t, err, engineio.ErrTransportRoundTripperClientRequired)
}

func TestTransportRoundTripper_RoundTrip_CallsTransportClient(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock transport client
	mockTransportClient := NewMockTransportClient(gomock.NewController(t))
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Return(&http.Response{
			StatusCode: http.StatusOK,
		}, nil)

	// Arrange: create a new transport round tripper
	transport := engineio.TransportRoundTripper{
		Client: mockTransportClient,
	}

	// Act: round trip a request
	r, err := transport.RoundTrip(&http.Request{})
	require.NoError(t, err)

	// Assert: the response should not be nil
	require.NotNil(t, r)
	require.Equal(t, http.StatusOK, r.StatusCode)
}

func TestTransports(t *testing.T) {
	t.Parallel()

	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	for transportType, transportConstructor := range engineio.Transports {
		t.Run(fmt.Sprintf("%s without url", transportType), func(t *testing.T) {
			t.Parallel()

			// Act: create a new transport without a URL
			transport, err := transportConstructor(nil, nil, nil)

			// Assert: an error should be returned and the transport should be nil
			require.Error(t, err)
			require.Nil(t, transport)
		})

		t.Run(fmt.Sprintf("%s without options", transportType), func(t *testing.T) {
			t.Parallel()

			// Act: create a new transport without options
			transport, err := transportConstructor(u, nil, nil)

			// Assert: no error should be returned and the transport should not be nil
			require.NoError(t, err)
			require.NotNil(t, transport)
		})

		t.Run(fmt.Sprintf("%s with options", transportType), func(t *testing.T) {
			t.Parallel()

			// Act: create a new transport with options
			transport, err := transportConstructor(u, http.DefaultClient, http.Header{
				"Authorization": []string{"Bearer token"},
			})

			// Assert: no error should be returned and the transport should not be nil
			require.NoError(t, err)
			require.NotNil(t, transport)
		})
	}
}
