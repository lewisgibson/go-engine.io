package engineio_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestSocket_Probe_UpgradesOnPongProbe(t *testing.T) {
	// Arrange: drive a socket through its websocket upgrade probe
	socket, _, ws := driveProbe(t, nil)

	upgraded := make(chan engineio.TransportType, 1)
	socket.OnUpgrade(func(transportType engineio.TransportType) { upgraded <- transportType })

	// Act: answer the probe ping with a probe pong
	ws.deliverPacket(t.Context(), engineio.Packet{Type: engineio.PacketPong, Data: []byte("probe")})

	// Assert: the socket upgraded to the websocket transport
	require.Equal(t, engineio.TransportTypeWebSocket, <-upgraded)
}

func TestSocket_Probe_RecoversWhenUpgradeSendFails(t *testing.T) {
	// Arrange: drive the probe with a websocket whose first send (the probe ping)
	// succeeds and whose second send (the upgrade packet) fails.
	socket, _, ws := driveProbe(t, []error{nil, errors.New("upgrade write failed")})

	upgraded := make(chan engineio.TransportType, 1)
	upgradeErrs := make(chan error, 1)
	socket.OnUpgrade(func(transportType engineio.TransportType) { upgraded <- transportType })
	socket.OnUpgradeError(func(err error) {
		select {
		case upgradeErrs <- err:

		default:
		}
	})

	// Act: answer the probe pong, which triggers the failing upgrade commit
	ws.deliverPacket(t.Context(), engineio.Packet{Type: engineio.PacketPong, Data: []byte("probe")})

	// Assert: the failed upgrade is reported and the socket never upgraded, so the
	// probe websocket was abandoned (closed) and traffic stayed on polling.
	require.Error(t, <-upgradeErrs)
	require.Empty(t, upgraded)
	require.True(t, ws.isClosed())
}

func TestSocket_Probe_UpgradeErrorHandlerReceivesProbeFailure(t *testing.T) {
	// Arrange: a polling transport plus a websocket constructor that always fails
	polling := newControllableTransport(engineio.TransportTypePolling)
	withFakeTransports(t, map[engineio.TransportType]engineio.Transport{
		engineio.TransportTypePolling: polling,
	})
	// Override websocket separately so its constructor returns an error.
	originalWS := engineio.Transports[engineio.TransportTypeWebSocket]
	engineio.Transports[engineio.TransportTypeWebSocket] = func(*url.URL, engineio.TransportClient, http.Header) (engineio.Transport, error) {
		return nil, errors.New("cannot build websocket")
	}
	t.Cleanup(func() { engineio.Transports[engineio.TransportTypeWebSocket] = originalWS })

	socket, err := engineio.NewSocket("http://localhost/engine.io/")
	require.NoError(t, err)
	t.Cleanup(func() { socket.Close(context.WithoutCancel(t.Context())) })

	upgradeErrors := make(chan error, 1)
	socket.OnUpgradeError(func(err error) { upgradeErrors <- err })

	// Act: open and deliver a handshake advertising a websocket upgrade, so the
	// background probe runs and fails to build the transport.
	socket.Open(t.Context())
	polling.deliverPacket(t.Context(), engineio.Packet{
		Type: engineio.PacketOpen,
		Data: handshakeData(t, engineio.TransportTypeWebSocket),
	})

	// Assert: the probe failure reached the upgrade-error handler
	select {
	case err := <-upgradeErrors:
		require.ErrorContains(t, err, "cannot build websocket")

	case <-time.After(2 * time.Second):
		require.FailNow(t, "upgrade-error handler did not fire")
	}
}

func TestSocket_Probe_AbortsWhenSocketClosed(t *testing.T) {
	// Arrange: drive a socket through its websocket upgrade probe
	socket, _, ws := driveProbe(t, nil)

	// Arrange: the socket closes before the probe resolves
	socket.Close(context.WithoutCancel(t.Context()))

	// Act: a late probe pong arrives
	ws.deliverPacket(t.Context(), engineio.Packet{Type: engineio.PacketPong, Data: []byte("probe")})

	// Assert: the probe transport was discarded and never adopted
	require.True(t, ws.isClosed())
}
