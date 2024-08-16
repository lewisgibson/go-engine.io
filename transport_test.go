package engineio_test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/lewisgibson/go-engine.io/internal/mocks"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestTransportType_String(t *testing.T) {
	t.Parallel()

	// Assert: the string representation of the transport type should never be empty.
	require.NotEmpty(t, engineio.TransportTypePolling.String())
}

func TestTransportState_String(t *testing.T) {
	t.Parallel()

	// Assert: the string representation of the transport state should never be empty.
	require.NotEmpty(t, engineio.TransportStateOpen.String())
}

func TestTransportRoundTripper_RoundTrip_NilTransportClient(t *testing.T) {
	t.Parallel()

	// Arrange: create a new transport round tripper.
	transport := engineio.TransportRoundTripper{}

	// Act: round trip a request.
	_, err := transport.RoundTrip(&http.Request{})
	require.ErrorIsf(t, err, engineio.ErrTransportRoundTripperClientRequired, "error should be ErrTransportRoundTripperClientRequired")
}

func TestTransportRoundTripper_RoundTrip_CallsTransportClient(t *testing.T) {
	t.Parallel()

	// Arrange: create a new mock controller.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Arrange: create a new mock transport client.
	mockTransportClient := mocks.NewMockTransportClient(ctrl)
	mockTransportClient.EXPECT().
		Do(gomock.Any()).
		Return(&http.Response{
			StatusCode: http.StatusOK,
		}, nil)

	// Arrange: create a new transport round tripper.
	transport := engineio.TransportRoundTripper{
		Client: mockTransportClient,
	}

	// Act: round trip a request.
	r, err := transport.RoundTrip(&http.Request{})
	require.NoErrorf(t, err, "error should be nil")

	// Assert: the response should not be nil.
	require.NotNilf(t, r, "response should not be nil")
	require.Equalf(t, http.StatusOK, r.StatusCode, "response status code should be http.StatusOK")
}

func TestTransports(t *testing.T) {
	t.Parallel()

	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	for transportType, transportConstructor := range engineio.Transports {
		t.Run(fmt.Sprintf("%s without url", transportType), func(t *testing.T) {
			t.Parallel()

			// Act: create a new transport without a URL.
			transport, err := transportConstructor(nil, engineio.TransportOptions{})

			// Assert: an error should be returned and the transport should be nil.
			require.Errorf(t, err, "url is required")
			require.Nilf(t, transport, "transport should be nil")
		})

		t.Run(fmt.Sprintf("%s without options", transportType), func(t *testing.T) {
			t.Parallel()

			// Act: create a new transport without options.
			transport, err := transportConstructor(u, engineio.TransportOptions{})

			// Assert: no error should be returned and the transport should not be nil.
			require.NoErrorf(t, err, "transport should not return an error")
			require.NotNilf(t, transport, "transport should not be nil")
		})

		t.Run(fmt.Sprintf("%s with options", transportType), func(t *testing.T) {
			t.Parallel()

			// Act: create a new transport with options.
			transport, err := transportConstructor(u, engineio.TransportOptions{
				Client: http.DefaultClient,
				Header: http.Header{
					"Authorization": []string{"Bearer token"},
				},
			})

			// Assert: no error should be returned and the transport should not be nil.
			require.NoErrorf(t, err, "transport should not return an error")
			require.NotNilf(t, transport, "transport should not be nil")
		})
	}
}
