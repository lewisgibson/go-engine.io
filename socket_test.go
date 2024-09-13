package engineio_test

import (
	"errors"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/lewisgibson/go-engine.io/internal/mocks"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewSocket(t *testing.T) {
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

		// Act: create a new polling transport with the mock transport client.
	url := "http://localhost/engine.io/?EIO=4&transport=polling"
	socket, err := engineio.NewSocket(url, engineio.SocketOptions{
		Client: mockTransportClient,
	})

	// Assert: the socket is not nil and there is no error.
	require.Nilf(t, err, "NewSocket() error = %v", err)
	require.NotNil(t, socket)
}
