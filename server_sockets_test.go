package engineio_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestServer_SocketsAndCount_ReflectLiveSessions(t *testing.T) {
	t.Parallel()

	// Arrange: a fresh server
	server := engineio.NewServer()
	t.Cleanup(server.Close)

	// Assert: an empty server reports no sessions
	require.Zero(t, server.Count())
	require.Empty(t, server.Sockets())
	_, ok := server.Socket("absent")
	require.False(t, ok)

	// Act: handshake three sessions
	var ids = make(map[string]bool, 3)
	for range 3 {
		ids[handshake(t, server).SessionID] = true
	}

	// Assert: Count and Sockets reflect all three sessions
	require.Equal(t, 3, server.Count())
	sockets := server.Sockets()
	require.Len(t, sockets, 3)

	// Assert: every snapshot socket is reachable by id and is the same instance
	for _, socket := range sockets {
		require.True(t, ids[socket.ID()])
		got, ok := server.Socket(socket.ID())
		require.True(t, ok)
		require.Same(t, socket, got)
	}

	// Assert: an unknown id is reported missing
	_, ok = server.Socket("absent")
	require.False(t, ok)
}

func TestServer_Count_DropsClosedSession(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// Arrange: one handshaken session that never polls or pongs, signalling its
		// close. The registry delete happens before the close handler fires, so the
		// signal is a deterministic point at which the session has left the registry.
		closed := make(chan struct{})
		server := engineio.NewServer(fastServerOptions()...)
		server.OnConnection(func(socket *engineio.ServerSocket) {
			socket.OnClose(func(_ string, _ error) { close(closed) })
		})
		open := handshake(t, server)
		require.Equal(t, 1, server.Count())

		// Act: let the heartbeat deadline close the session
		<-closed

		// Assert: the closed session leaves the registry
		require.Zero(t, server.Count())
		require.Empty(t, server.Sockets())
		_, ok := server.Socket(open.SessionID)
		require.False(t, ok)
	})
}

func TestServer_Sockets_ConcurrentAccessIsRaceFree(t *testing.T) {
	t.Parallel()

	// Arrange: a server hammered by readers while writers register sessions
	server := engineio.NewServer()
	t.Cleanup(server.Close)

	// Act: handshake from many goroutines while others iterate the registry, so
	// the race detector exercises the session lock under concurrent reads/writes.
	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers * 2)
	for range workers {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pollingURL(""), nil))
		}()
		go func() {
			defer wg.Done()
			_ = server.Sockets()
			_ = server.Count()
			_, _ = server.Socket("none")
		}()
	}
	wg.Wait()

	// Assert: every handshake registered a live session
	require.Equal(t, workers, server.Count())
	require.Len(t, server.Sockets(), workers)
}
