package engineio_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSocket_Send_DropsWhenClosed(t *testing.T) {
	// Arrange: a fake transport injected for a never-opened socket
	fake := newFakeTransport(engineio.TransportTypePolling)
	withFakeTransports(t, map[engineio.TransportType]engineio.Transport{
		engineio.TransportTypePolling: fake,
	})

	socket, err := engineio.NewSocket("http://localhost/engine.io/",
		engineio.WithTransports(engineio.TransportTypePolling),
	)
	require.NoError(t, err)

	// Act: send on the socket before it is opened
	require.NoError(t, socket.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	}))

	// Assert: nothing reached the transport, since a send on a closed socket is a
	// no-op rather than a buffered write.
	require.Empty(t, fake.sentPackets())
}

func TestSocket_Send_RetainsBufferOnError(t *testing.T) {
	// Arrange: a fake whose first send fails, so the failed write must stay
	// buffered for the next flush to retry. No send happens during the handshake
	// (the write buffer is empty), so the first send is the application message.
	fake := newFakeTransport(engineio.TransportTypePolling)
	fake.sendErrs = []error{errors.New("boom")}
	withFakeTransports(t, map[engineio.TransportType]engineio.Transport{
		engineio.TransportTypePolling: fake,
	})

	socket, err := engineio.NewSocket("http://localhost/engine.io/",
		engineio.WithTransports(engineio.TransportTypePolling),
		engineio.WithUpgrade(false),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	socket.OnOpen(func() { opened <- struct{}{} })
	socket.OnError(func(error) {})
	t.Cleanup(func() { socket.Close(context.WithoutCancel(t.Context())) })

	// Act: open and complete the handshake so the socket is live
	socket.Open(t.Context())
	fake.deliverPacket(t.Context(), engineio.Packet{Type: engineio.PacketOpen, Data: handshakeData(t)})
	<-opened

	// Act: a send whose write fails
	require.Error(t, socket.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	}))

	// Act: a healthy retry drains the buffer
	require.NoError(t, socket.Send(t.Context(), nil))

	// Assert: the previously failed packet was delivered exactly once on retry
	require.Equal(t, []engineio.Packet{{Type: engineio.PacketMessage, Data: []byte("hello")}}, fake.sentPackets())
}

func TestSocket_Send_BuffersWhileOpening(t *testing.T) {
	// Arrange: a controllable polling transport whose open the test drives, so a
	// send can be issued while the socket is still opening.
	polling := newControllableTransport(engineio.TransportTypePolling)
	withFakeTransports(t, map[engineio.TransportType]engineio.Transport{
		engineio.TransportTypePolling: polling,
	})

	socket, err := engineio.NewSocket("http://localhost/engine.io/",
		engineio.WithTransports(engineio.TransportTypePolling),
		engineio.WithUpgrade(false),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	socket.OnOpen(func() { opened <- struct{}{} })
	socket.OnError(func(error) {})
	t.Cleanup(func() { socket.Close(context.WithoutCancel(t.Context())) })

	// Act: open and, while still opening (no handshake yet), send a message
	socket.Open(t.Context())
	require.NoError(t, socket.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	}))

	// Assert: the packet was buffered, not sent, while opening
	require.Empty(t, polling.sent)

	// Act: complete the handshake, which flushes the buffered write
	polling.deliverPacket(t.Context(), engineio.Packet{Type: engineio.PacketOpen, Data: handshakeData(t)})
	<-opened

	// Assert: the previously buffered packet was delivered after the open
	require.Equal(t, []engineio.Packet{{Type: engineio.PacketMessage, Data: []byte("hello")}}, flatten(polling.sent))
}

func TestSocket_Send_ChunksToMaxPayload(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: a mock client that handshakes with a small MaxPayload and records
		// every POST body, so the socket's chunking is observable on the wire.
		var (
			mu       sync.Mutex
			posts    [][]byte
			getCount atomic.Int32
		)
		opened := make(chan struct{}, 1)
		mockTransportClient := NewMockTransportClient(gomock.NewController(t))
		mockTransportClient.EXPECT().
			Do(gomock.Any()).
			DoAndReturn(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.Method == http.MethodPost:
					body, readErr := io.ReadAll(req.Body)
					require.NoError(t, readErr)
					mu.Lock()
					posts = append(posts, body)
					mu.Unlock()
					return okResponse([]byte("ok")), nil

				case getCount.Add(1) == 1:
					// MaxPayload 9 forces multi-packet writes to split.
					data, err := json.Marshal(engineio.OpenPacket{
						SessionID:    "sid",
						Upgrades:     []engineio.TransportType{},
						PingInterval: 1000,
						PingTimeout:  1000,
						MaxPayload:   9,
					})
					require.NoError(t, err)
					return okResponse(engineio.EncodePayload([]engineio.Packet{
						{Type: engineio.PacketOpen, Data: data},
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
		socket.OnOpen(func() {
			select {
			case opened <- struct{}{}:

			default:
			}
		})
		socket.OnError(func(error) {})

		// Act: open, then send three small messages in one call. "aaa", "bbb", "ccc"
		// each encode to "4aaa" (4 bytes); two fit in 9 bytes (4+1+4) but three do
		// not, so the write must split into two POSTs.
		socket.Open(t.Context())
		<-opened
		require.NoError(t, socket.Send(t.Context(), []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte("aaa")},
			{Type: engineio.PacketMessage, Data: []byte("bbb")},
			{Type: engineio.PacketMessage, Data: []byte("ccc")},
		}))
		synctest.Wait()

		// Assert: the three messages were delivered as two chunked POSTs, each within
		// the negotiated limit, preserving order.
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, [][]byte{
			engineio.EncodePayload([]engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte("aaa")},
				{Type: engineio.PacketMessage, Data: []byte("bbb")},
			}),
			engineio.EncodePayload([]engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte("ccc")},
			}),
		}, posts)
	})
}
