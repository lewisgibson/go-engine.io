package engineio_test

import (
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSocket_PacketResetsThePingDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: a mock client whose second poll delivers a message at T=50ms,
		// well before the T=200ms (pingInterval + pingTimeout) deadline. Receiving
		// it must reset the deadline to T=250ms.
		var getCount atomic.Int32
		mockTransportClient := NewMockTransportClient(gomock.NewController(t))
		mockTransportClient.EXPECT().
			Do(gomock.Any()).
			DoAndReturn(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodPost {
					return okResponse(nil), nil
				}
				switch getCount.Add(1) {
				case 1:
					return okResponse(clientHandshakeBody(t, 100, 100)), nil

				case 2:
					<-time.After(50 * time.Millisecond)
					return okResponse(engineio.EncodePayload([]engineio.Packet{
						{Type: engineio.PacketMessage, Data: []byte("x")},
					})), nil

				default:
					return okResponse(nil), nil
				}
			}).
			AnyTimes()

		socket, err := engineio.NewSocket("http://localhost/engine.io/",
			engineio.WithClient(mockTransportClient),
			engineio.WithUpgrade(false),
		)
		require.NoError(t, err)

		closed := make(chan string, 1)
		socket.OnClose(func(reason string, _ error) { closed <- reason })

		// Act: open, then advance past the original deadline (200ms)
		socket.Open(t.Context())
		time.Sleep(220 * time.Millisecond)
		synctest.Wait()

		// Assert: the message at T=50ms reset the deadline, so the socket is still
		// open past the original 200ms deadline.
		require.Empty(t, closed)

		// Act: advance past the reset deadline (250ms)
		time.Sleep(60 * time.Millisecond)
		synctest.Wait()

		// Assert: the socket then closes with the ping-timeout reason
		require.Equal(t, "ping timeout", <-closed)
	})
}
