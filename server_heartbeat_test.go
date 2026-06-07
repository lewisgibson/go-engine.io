package engineio_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_Heartbeat_DeliversPingAndHonoursPong(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: a handshaken session that records closure
		sockets := make(chan *engineio.ServerSocket, 1)
		closed := make(chan string, 1)
		server := engineio.NewServer(fastServerOptions()...)
		server.OnConnection(func(socket *engineio.ServerSocket) {
			socket.OnClose(func(reason string, _ error) { closed <- reason })
			sockets <- socket
		})
		open := handshake(t, server)
		socket := <-sockets

		// Arrange: a well-behaved client that polls and pongs every ping until
		// the session closes (its poll then returns a close packet).
		var clientDone = make(chan struct{})
		go func() {
			defer close(clientDone)
			for {
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(open.SessionID), nil))
				if rec.Code != http.StatusOK {
					return
				}
				packets, err := engineio.DecodePayload(engineio.ProtocolVersion4, rec.Body.Bytes())
				if err != nil {
					return
				}
				for _, packet := range packets {
					switch packet.Type {
					case engineio.PacketClose:
						return

					case engineio.PacketPing:
						postPackets(server, open.SessionID, engineio.EncodePayload([]engineio.Packet{
							{Type: engineio.PacketPong},
						}))
					}
				}
			}
		}()

		// Act: let several heartbeat intervals elapse on the fake clock
		time.Sleep(time.Second)
		synctest.Wait()

		// Assert: the session stays open while the client keeps ponging
		require.Empty(t, closed)

		// Close the session, which releases the client's held poll with a close
		// packet so its goroutine can drain before the bubble ends.
		require.NoError(t, socket.Close())
		<-clientDone
	})
}

func TestServer_Heartbeat_ClosesOnMissedPong(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: a handshaken session that records closure
		closed := make(chan string, 1)
		server := engineio.NewServer(fastServerOptions()...)
		server.OnConnection(func(socket *engineio.ServerSocket) {
			socket.OnClose(func(reason string, _ error) { closed <- reason })
		})
		handshake(t, server)

		// Act: never poll or pong; let the heartbeat deadline elapse
		synctest.Wait()

		// Assert: the session closes with the ping-timeout reason
		require.Equal(t, "ping timeout", <-closed)
	})
}
