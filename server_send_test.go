package engineio_test

import (
	"net/http"
	"testing"
	"testing/synctest"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_Send_DispatchesMessage(t *testing.T) {
	t.Parallel()

	// Arrange: a server capturing inbound messages
	type message struct {
		data     []byte
		isBinary bool
	}
	messages := make(chan message, 4)
	server := engineio.NewServer()
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, isBinary bool) {
			messages <- message{data: data, isBinary: isBinary}
		})
	})
	t.Cleanup(server.Close)

	open := handshake(t, server)

	// Act: POST a text message and a binary message
	textResponse := postPackets(server, open.SessionID, engineio.EncodePayload([]engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	}))
	binaryResponse := postPackets(server, open.SessionID, engineio.EncodePayload([]engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03}, IsBinary: true},
	}))

	// Assert: each POST is acknowledged with "ok" as text/html
	require.Equal(t, http.StatusOK, textResponse.Code)
	require.Equal(t, "ok", textResponse.Body.String())
	require.Equal(t, "text/html", textResponse.Header().Get("Content-Type"))
	require.Equal(t, http.StatusOK, binaryResponse.Code)

	// Assert: both messages are delivered with the correct binary flag
	text := <-messages
	require.Equal(t, "hello", string(text.data))
	require.False(t, text.isBinary)

	binary := <-messages
	require.Equal(t, []byte{0x01, 0x02, 0x03}, binary.data)
	require.True(t, binary.isBinary)
}

func TestServer_Send_RejectsMalformedBody(t *testing.T) {
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
		<-sockets

		// Act: POST a body the v4 codec cannot decode
		response := postPackets(server, open.SessionID, []byte("7"))
		synctest.Wait()

		// Assert: the request is rejected and the session closes as a parse error
		require.Equal(t, http.StatusBadRequest, response.Code)
		require.Equal(t, "parse error", <-closed)
	})
}
